// Package preflight 对「业务测试场景」做就绪自检：触发群呼/外呼/OTP/线路呼叫前，
// 检查前置条件（业务接口地址、机构 OpenAPI、mock 侧端口绑定）是否齐备，给出可操作诊断，
// 避免「触发了但什么都没进 mock」的盲目排查。
// 注意：mock 只演客户被叫腿；群呼/手动外呼接通后转坐席由真实 Hermes 坐席承担，不在此自检。
package preflight

// Status 单项检查结果级别。
type Status string

const (
	OK   Status = "OK"   // 就绪
	Warn Status = "WARN" // 可用但有隐患/降级
	Fail Status = "FAIL" // 缺失，场景大概率跑不通
)

// Check 一条检查项。
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Report 一类场景的就绪报告。
type Report struct {
	Scenario string  `json:"scenario"` // callcenter-task / autocall / otp
	Ready    bool    `json:"ready"`    // 无 FAIL 即就绪
	Checks   []Check `json:"checks"`
}

// Inputs 自检所需的运行态快照（由 api 层从各组件采集后传入）。
type Inputs struct {
	CallCenterBaseURL string // 群呼任务
	CallBotBaseURL    string // 自动外呼/任务
	OTPBaseURL        string // 语音验证码
	LineDBConnected   bool   // 机构 OpenAPI 已配置（可核验 Hermes 侧配置）
	LineCount         int    // mock 侧端口绑定数
	CustomerGroups    int    // 客户组数
	LineBindings      int    // 端口绑定数
}

func add(checks *[]Check, name string, st Status, detail string) {
	*checks = append(*checks, Check{Name: name, Status: st, Detail: detail})
}

func finalize(scenario string, checks []Check) Report {
	ready := true
	for _, c := range checks {
		if c.Status == Fail {
			ready = false
			break
		}
	}
	return Report{Scenario: scenario, Ready: ready, Checks: checks}
}

// CallCenterTask 群呼任务就绪：需 call-center 地址 + mock 侧端口绑定。
// 接通后转坐席由真实 Hermes 坐席承担，不在此检查 mock 坐席。
func CallCenterTask(in Inputs) Report {
	var c []Check
	if in.CallCenterBaseURL == "" {
		add(&c, "call-center 地址", Fail, "机构页未配 call-center 地址，无法建群呼任务")
	} else {
		add(&c, "call-center 地址", OK, in.CallCenterBaseURL)
	}
	lineReadiness(&c, in)
	add(&c, "坐席", OK, "接通后转坐席由真实 Hermes 坐席承担；mock 只演客户被叫腿")
	return finalize("callcenter-task", c)
}

// AutoCall 自动外呼就绪：需 call-bot 地址 + mock 侧端口绑定。
func AutoCall(in Inputs) Report {
	var c []Check
	if in.CallBotBaseURL == "" {
		add(&c, "call-bot 地址", Fail, "机构页未配 call-bot 地址，无法发起自动外呼")
	} else {
		add(&c, "call-bot 地址", OK, in.CallBotBaseURL)
	}
	lineReadiness(&c, in)
	return finalize("autocall", c)
}

// OTP 语音验证码就绪：需 otp OpenAPI 地址 + mock 侧端口绑定。
func OTP(in Inputs) Report {
	var c []Check
	if in.OTPBaseURL == "" {
		add(&c, "otp 地址", Fail, "机构页未配 otp 地址，无法下发语音验证码")
	} else {
		add(&c, "otp 地址", OK, in.OTPBaseURL)
	}
	lineReadiness(&c, in)
	return finalize("otp", c)
}

// lineReadiness 线路路由就绪：客户号要被路由到 mock，需 Hermes 侧线路 address 指向 mockIP:port，并在 mock 侧配置端口绑定。
func lineReadiness(c *[]Check, in Inputs) {
	switch {
	case !in.LineDBConnected:
		add(c, "Hermes 机构", Warn, "未配置当前机构 OpenAPI；无法核验 Hermes 侧线路/TTS 等配置")
	case in.LineCount == 0:
		add(c, "端口绑定", Warn, "mock 侧无端口绑定；请确认 Hermes 侧线路 address→mockIP:port，并在「客户配置」维护端口绑定")
	default:
		add(c, "端口绑定", OK, "mock 侧已有端口绑定")
	}
	if in.LineBindings == 0 {
		add(c, "客户组绑定", Warn, "无 端口→客户组 绑定；客户行为将走号码/默认匹配（非按端口）")
	} else {
		add(c, "客户组绑定", OK, "已配置端口→客户组绑定")
	}
}
