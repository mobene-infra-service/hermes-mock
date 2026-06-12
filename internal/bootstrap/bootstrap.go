// Package bootstrap 一键播种「业务测试」的最小可运行配置：
// 行为档 + 客户组 + 端口绑定（不写 Hermes 业务库），快速准备客户腿与入口端口匹配。
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
	ListenPort     int    `json:"listenPort"`     // mock SIP 入口端口（默认 5060）
	LineName       string `json:"lineName"`       // 线路名（默认 demo-line）
}

// Result 播种结果摘要。
type Result struct {
	ProfileCode   string   `json:"profileCode"`
	CustomerGroup string   `json:"customerGroup"`
	LineCode      string   `json:"lineCode,omitempty"`
	ListenPort    int      `json:"listenPort"`
	LineBinding   string   `json:"lineBinding"`
	Notes         []string `json:"notes"`
}

// withDefaults 填充默认值。
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
	if p.ListenPort == 0 {
		p.ListenPort = 5060
	}
	if p.LineName == "" {
		p.LineName = "demo-line"
	}
	return p
}

// Seed 播种最小可运行配置到 cluster（行为档 + 客户组 + 端口绑定）。
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

	// 3. 入口端口绑定：mock SIP 入口端口 ↔ 客户组。
	// Hermes 线路 address 仍需在 Hermes 侧配置为 mockIP:listenPort；mock 自己只按入口端口路由客户组。
	bindCode := "demo_bind"
	if _, err := clu.UpsertBinding(cluster.LineBinding{
		ListenPort: p.ListenPort, LineCode: bindCode, LineName: p.LineName,
		GroupCode: p.CustomerGroup, Enabled: 1,
	}); err != nil {
		return nil, fmt.Errorf("播种入口端口绑定: %w", err)
	}
	res.LineBinding = bindCode
	res.ListenPort = p.ListenPort
	res.Notes = append(res.Notes, fmt.Sprintf("Hermes 线路 address 需配到 mockIP:%d；mock 已建端口↔客户组绑定 %s", p.ListenPort, bindCode))
	res.Notes = append(res.Notes, fmt.Sprintf("客户组 %s 展开 %s%d..%d（%d 个）",
		p.CustomerGroup, p.CustomerPrefix, p.CustomerStart, p.CustomerStart+int64(p.CustomerCount)-1, p.CustomerCount))
	res.Notes = append(res.Notes, "坐席（如群呼/手动外呼场景需要）由真实 Hermes 坐席承担；mock 只演客户被叫腿")
	return res, nil
}
