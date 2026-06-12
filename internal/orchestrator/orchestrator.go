// Package orchestrator 从 mock 这一侧经 **Hermes OpenAPI/SDK**（按当前机构凭据）批量发起业务场景：
// 群呼任务 / 自动外呼 / call-bot 任务 / OTP / 坐席操作。绝不直连 Hermes 库——凭据来自 orgcfg。
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"hermes-mock/internal/entity"
	"hermes-mock/internal/hermesopenapi"
	"hermes-mock/internal/orgcfg"
)

// Orchestrator 编排器：每次调用按「当前机构」取 OpenAPI 客户端。
type Orchestrator struct {
	orgs *orgcfg.Store
}

// New 构造编排器（绑定机构配置存储）。
func New(orgs *orgcfg.Store) *Orchestrator {
	return &Orchestrator{orgs: orgs}
}

func (o *Orchestrator) ctx() (context.Context, context.CancelFunc) {
	// 45s：群呼 createAndImport+start 等重接口在 call-center 过载时响应慢（实测健康检查都要数秒），
	// 给足余量避免 mock 侧 "context deadline exceeded"。
	return context.WithTimeout(context.Background(), 45*time.Second)
}

// client 取当前机构的 OpenAPI 客户端。
func (o *Orchestrator) client() (*hermesopenapi.Client, error) {
	cli, ok := o.orgs.Client()
	if !ok {
		return nil, fmt.Errorf("当前机构未配置 OpenAPI 凭据（去「机构」页配置）")
	}
	return cli, nil
}

// CallBotScenario 一次 call-bot 任务场景。
type CallBotScenario struct {
	Name     string   `json:"name"`
	TaskType int      `json:"taskType"` // 1=IVR 2=AI_CALL
	Numbers  []string `json:"numbers"`
	Robot    string   `json:"robotCode,omitempty"`
	Script   string   `json:"salesScriptCode,omitempty"`
}

// RunCallBot 经 OpenAPI 建 call-bot 任务并导入号码。
func (o *Orchestrator) RunCallBot(s CallBotScenario) ([]byte, error) {
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.ctx()
	defer cancel()
	raw, err := cli.CreateCallBotTask(ctx, map[string]any{
		"name": s.Name, "taskType": s.TaskType, "numbers": numberInfos(s.Numbers),
		"robotCode": s.Robot, "salesScriptCode": s.Script,
	})
	return raw, err
}

// AutoCallScenario call-bot 直接自动外呼。
type AutoCallScenario struct {
	TemplateCode string            `json:"templateCode"`
	Numbers      []string          `json:"numbers"`
	TTSVars      map[string]string `json:"ttsVars"`
}

// RunAutoCall 经 OpenAPI 自动外呼。
func (o *Orchestrator) RunAutoCall(s AutoCallScenario) ([]byte, error) {
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.ctx()
	defer cancel()
	body := map[string]any{"templateCode": s.TemplateCode, "numbers": numberInfos(s.Numbers), "encrypted": false}
	if s.TTSVars != nil {
		body["ttsTextVariableMap"] = s.TTSVars
	}
	return cli.AutoCall(ctx, body)
}

// OTPScenario 语音验证码。
type OTPScenario struct {
	To           string            `json:"to"`
	TemplateCode string            `json:"templateCode"`
	Params       map[string]string `json:"params"`
}

// RunOTP 经 OpenAPI 下发语音验证码。
func (o *Orchestrator) RunOTP(s OTPScenario) ([]byte, error) {
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.ctx()
	defer cancel()
	return cli.SendOTP(ctx, s.To, s.TemplateCode, s.Params)
}

// CallCenterTaskScenario call-center 群呼任务（= entity.CallCenterTaskReq，含全部 Hermes 参数）。
type CallCenterTaskScenario = entity.CallCenterTaskReq

// RunCallCenterTask 经 OpenAPI 建 call-center 群呼任务（createAndImport 即自动拨号，无需再 start）。
func (o *Orchestrator) RunCallCenterTask(s CallCenterTaskScenario) ([]byte, error) {
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	period := s.DialTimePeriod
	if len(period) == 0 {
		period = []string{"00:00-23:59"}
	}
	startDate := s.StartDate
	endDate := s.EndDate
	if startDate == "" {
		startDate = time.Now().Format("2006-01-02")
	}
	if endDate == "" {
		endDate = time.Now().AddDate(0, 1, 0).Format("2006-01-02") // +1 个月
	}
	sortMethod := s.SortMethod
	if sortMethod == 0 {
		sortMethod = 1 // 1=优先首呼（TaskSortMethodEnum 必填）
	}
	bestRing := s.BestRingDuration
	if bestRing == 0 {
		bestRing = 40 // Hermes 默认 40s
	}
	ctx, cancel := o.ctx()
	defer cancel()
	body := map[string]any{
		"name": s.Name, "ttsCode": s.TTSCode, "ttsText": s.TTSText, "ttsTextVariableMap": map[string]string{},
		"sortMethod": sortMethod, "startDate": startDate, "endDate": endDate, "dialTimePeriod": period,
		"isPriorityTask": s.IsPriorityTask, "isVmHangup": s.IsVmHangup,
		"bestRingDuration": bestRing, "assignDelaySeconds": s.AssignDelaySeconds,
		"numbers": numberInfos(s.Numbers),
	}
	// 模式策略组合（对照 Hermes CallTaskService.validateAddRequest）：
	//   modeStrategy=1(比例) → 必填 proportion(1-10)；modeStrategy=2(PID) → 必填 lossRate(0-99)+historicalConnectionRate(1-100)。
	mode := s.ModeStrategy
	if mode == 0 {
		mode = 1
	}
	body["modeStrategy"] = mode
	if mode == 2 {
		hist := s.HistoricalConnectionRate
		if hist == 0 {
			hist = 50 // PID 必填且 1-100，给个安全默认
		}
		body["lossRate"] = s.LossRate // 0 合法，无条件下发
		body["historicalConnectionRate"] = hist
	} else {
		prop := s.Proportion
		if prop == 0 {
			prop = 1
		}
		body["proportion"] = prop
	}
	// 可选项：仅在有值时下发，缺省走 Hermes 默认。
	if s.MaxRedialTimes > 0 {
		body["maxRedialTimes"] = s.MaxRedialTimes
	}
	if s.RedialInterval > 0 {
		body["redialInterval"] = s.RedialInterval
	}
	if s.AgentMaxRingDuration > 0 {
		body["agentMaxRingDuration"] = s.AgentMaxRingDuration
	}
	if s.TransferType != "" {
		body["transferType"] = s.TransferType
	}
	if s.Description != "" {
		body["description"] = s.Description
	}
	// 坐席分配二选一（对照 Hermes AddCallTaskAndImportNumberReq）：
	//   - agentNumbers：指定坐席号列表（@Size max 500）；
	//   - agentGroupCodes：技能组（@Size(max=1,min=1) —— 只接受恰好 1 个，故取首个）。
	// 同时给则以 agentNumbers 优先。orgCode 不进 body（Hermes 经凭据头 ORG_CODE 取机构）。
	if len(s.AgentNumbers) > 0 {
		body["agentNumbers"] = s.AgentNumbers
	} else if len(s.AgentGroupCodes) > 0 {
		body["agentGroupCodes"] = []string{s.AgentGroupCodes[0]}
	}
	if s.LineType != "" {
		body["lineType"] = s.LineType // 7cbb285：任务期间仅用该 type 线路（含重试换线锁同 type）
	}
	// createAndImport 建任务后 Hermes 即异步拨号（NotifyDialJob），状态按日期判定为 IN_PROGRESS，
	// **无需再调 status/start**——start 仅用于恢复 PAUSE 态任务（证据 Hermes CallTaskService.startTask）。
	return cli.CreateCallCenterTask(ctx, body)
}

// PauseCallCenterTask 暂停群呼任务。
func (o *Orchestrator) PauseCallCenterTask(taskCode string) ([]byte, error) {
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.ctx()
	defer cancel()
	return cli.StopCallCenterTask(ctx, taskCode)
}

// ResumeCallCenterTask 恢复（启动）已暂停的群呼任务。
func (o *Orchestrator) ResumeCallCenterTask(taskCode string) ([]byte, error) {
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.ctx()
	defer cancel()
	return cli.StartCallCenterTask(ctx, taskCode)
}

// CancelCallCenterTask 取消群呼任务。
func (o *Orchestrator) CancelCallCenterTask(taskCode string) ([]byte, error) {
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.ctx()
	defer cancel()
	return cli.CancelCallCenterTask(ctx, taskCode)
}

// CallCenterTaskStatus 查群呼任务状态。
func (o *Orchestrator) CallCenterTaskStatus(taskCode string) ([]byte, error) {
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.ctx()
	defer cancel()
	return cli.GetCallCenterTaskStatus(ctx, taskCode)
}

// ---- testkit.BizCaller 适配 ----

// CallCenterTask 适配 BizCaller（全参数经 entity.CallCenterTaskReq 透传）。
func (o *Orchestrator) CallCenterTask(req entity.CallCenterTaskReq) ([]byte, error) {
	return o.RunCallCenterTask(req)
}

// CallBotTask 适配 BizCaller。
func (o *Orchestrator) CallBotTask(name string, taskType int, numbers []string, robot, script string) ([]byte, error) {
	return o.RunCallBot(CallBotScenario{Name: name, TaskType: taskType, Numbers: numbers, Robot: robot, Script: script})
}

// AutoCall 适配 BizCaller。
func (o *Orchestrator) AutoCall(templateCode string, numbers []string) ([]byte, error) {
	return o.RunAutoCall(AutoCallScenario{TemplateCode: templateCode, Numbers: numbers})
}

// OTP 适配 BizCaller。
func (o *Orchestrator) OTP(to, templateCode string, params map[string]string) ([]byte, error) {
	return o.RunOTP(OTPScenario{To: to, TemplateCode: templateCode, Params: params})
}

// numberInfos 把号码串转成 OpenAPI 的 {number} 列表。
func numberInfos(numbers []string) []map[string]string {
	out := make([]map[string]string, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, map[string]string{"number": n})
	}
	return out
}

// extractTaskCode 从创建任务响应里尽力提取任务 code（兼容 data 为字符串或对象.code）。
func extractTaskCode(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		// raw 已是 data 原文（hermesopenapi 返回 data），尝试直接解析
		var d map[string]any
		if json.Unmarshal(raw, &d) == nil {
			if c, ok := d["code"].(string); ok {
				return c
			}
			if c, ok := d["taskCode"].(string); ok {
				return c
			}
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	switch d := m["data"].(type) {
	case string:
		return d
	case map[string]any:
		if c, ok := d["code"].(string); ok {
			return c
		}
		if c, ok := d["taskCode"].(string); ok {
			return c
		}
	}
	// 顶层就是 data
	if c, ok := m["code"].(string); ok {
		return c
	}
	if c, ok := m["taskCode"].(string); ok {
		return c
	}
	return ""
}
