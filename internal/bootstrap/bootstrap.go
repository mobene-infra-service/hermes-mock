// Package bootstrap 一键播种「业务测试」的最小可运行配置：
// 行为档 + 客户组 + 线路绑定（不写 Hermes 业务库），快速准备客户腿与线路匹配。
// mock 只演客户被叫腿；坐席（如群呼/手动外呼场景需要）由真实 Hermes 坐席承担。
package bootstrap

import (
	"fmt"

	"hermes-mock/internal/cluster"
)

// Params 播种参数（均有合理默认，前端可覆盖）。
type Params struct {
	OrgCode        string `json:"orgCode"`        // 组织码（默认 demo_org）
	CustomerGroup  string `json:"customerGroup"`  // 客户组 code（默认 demo_customers）
	CustomerPrefix string `json:"customerPrefix"` // 客户号前缀（默认 8613800）
	CustomerStart  int64  `json:"customerStart"`  // 客户号起始（默认 100000）
	CustomerCount  int    `json:"customerCount"`  // 客户数（默认 20）
	LineName       string `json:"lineName"`       // 线路名（默认 demo-line）
	LineAddress    string `json:"lineAddress"`    // 线路 address（=mock 可达地址，前端填写）
}

// Result 播种结果摘要。
type Result struct {
	ProfileCode   string   `json:"profileCode"`
	CustomerGroup string   `json:"customerGroup"`
	LineCode      string   `json:"lineCode,omitempty"`
	LineBinding   string   `json:"lineBinding"`
	Notes         []string `json:"notes"`
}

// withDefaults 填充默认值。LineAddress 由前端/调用方提供（=mock 线路监听地址，Hermes t_line.address）。
func (p Params) withDefaults() Params {
	if p.OrgCode == "" {
		p.OrgCode = "demo_org"
	}
	if p.CustomerGroup == "" {
		p.CustomerGroup = "demo_customers"
	}
	if p.CustomerPrefix == "" {
		p.CustomerPrefix = "8613800"
	}
	if p.CustomerStart == 0 {
		p.CustomerStart = 100000
	}
	if p.CustomerCount == 0 {
		p.CustomerCount = 20
	}
	if p.LineName == "" {
		p.LineName = "demo-line"
	}
	return p
}

// Seed 播种最小可运行配置到 cluster（行为档 + 客户组 + 线路绑定）。
// 线路实体(t_line)须在 Hermes 侧配置，mock 不直写；这里只播种 mock 自己的绑定/集群配置。
func Seed(clu *cluster.Store, in Params) (*Result, error) {
	if clu == nil {
		return nil, fmt.Errorf("cluster store 不可用")
	}
	p := in.withDefaults()
	res := &Result{}

	// 1. 行为档：接听 + 100% 接通（最简单可跑通的客户行为）
	profileCode := "demo_answer"
	if _, err := clu.UpsertProfile(cluster.BehaviorProfile{
		Code: profileCode, Name: "demo 接听", Outcome: "ANSWER", AnswerRatio: 100, TalkMs: 8000,
	}); err != nil {
		return nil, fmt.Errorf("播种行为档: %w", err)
	}
	res.ProfileCode = profileCode

	// 2. 客户组（号段批量）
	if _, err := clu.UpsertGroup(cluster.CustomerGroup{
		Code: p.CustomerGroup, Name: "demo 客户", NumberPrefix: p.CustomerPrefix,
		NumberStart: p.CustomerStart, Count: p.CustomerCount, BehaviorCode: profileCode, State: "ENABLED",
	}); err != nil {
		return nil, fmt.Errorf("播种客户组: %w", err)
	}
	res.CustomerGroup = p.CustomerGroup

	// 3. 线路绑定：客户组 ↔ 线路（按 line_name 规范化匹配 FS 注入的 X-Line-Name）
	// lineAddress = mock 实际 SIP 可达地址（originate 的 gateway），否则 FS 的 INVITE 到不了 mock。
	// 若库里已有该绑定但地址与目标不一致（历史脏数据，如指向不存在的地址），Upsert 会一并纠正。
	bindCode := "demo_bind"
	for _, b := range clu.ListBindings() {
		if b.LineCode == bindCode && b.LineAddress != "" && b.LineAddress != p.LineAddress {
			res.Notes = append(res.Notes, fmt.Sprintf("已纠正线路绑定 %s 地址：%s → %s", bindCode, b.LineAddress, p.LineAddress))
			break
		}
	}
	if _, err := clu.UpsertBinding(cluster.LineBinding{
		LineCode: bindCode, LineName: p.LineName, LineAddress: p.LineAddress,
		GroupCode: p.CustomerGroup, Enabled: 1,
	}); err != nil {
		return nil, fmt.Errorf("播种线路绑定: %w", err)
	}
	res.LineBinding = bindCode
	res.Notes = append(res.Notes, "线路实体需在 Hermes 侧配置(address→mock)；mock 已建客户组↔线路绑定 "+bindCode)
	res.Notes = append(res.Notes, fmt.Sprintf("客户组 %s 展开 %s%d..%d（%d 个）",
		p.CustomerGroup, p.CustomerPrefix, p.CustomerStart, p.CustomerStart+int64(p.CustomerCount)-1, p.CustomerCount))
	res.Notes = append(res.Notes, "坐席（如群呼/手动外呼场景需要）由真实 Hermes 坐席承担；mock 只演客户被叫腿")
	return res, nil
}
