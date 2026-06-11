// Package orchestrator 从 mock 这一侧经 **Hermes OpenAPI/SDK**（按当前机构凭据）批量发起业务场景：
// 群呼任务 / 自动外呼 / call-bot 任务 / OTP / 坐席操作。绝不直连 Hermes 库——凭据来自 orgcfg。
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

// CallCenterTaskScenario call-center 群呼任务。
type CallCenterTaskScenario struct {
	OrgCode         string   `json:"orgCode"`
	Name            string   `json:"name"`
	Numbers         []string `json:"numbers"`
	AgentGroupCodes []string `json:"agentGroupCodes"`
	TTSCode         string   `json:"ttsCode"`
	TTSText         string   `json:"ttsText"`
	ObserveAgent    string   `json:"observeAgent"`
	Proportion      int      `json:"proportion"`
	StartDate       string   `json:"startDate"`
	EndDate         string   `json:"endDate"`
	DialTimePeriod  []string `json:"dialTimePeriod"`
	// LineType 线路类型（Hermes 2026-06 特性 7cbb285：任务期间仅用该 type 线路选号；
	// 空 = 不传，Hermes 默认 base）。
	LineType  string `json:"lineType"`
	AutoStart bool   `json:"autoStart"`
	WaitSec   int    `json:"waitSec"`
}

// RunCallCenterTask 经 OpenAPI 建并（可选）启动 call-center 群呼任务。
func (o *Orchestrator) RunCallCenterTask(s CallCenterTaskScenario) ([]byte, error) {
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	prop := s.Proportion
	if prop == 0 {
		prop = 1
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
	ctx, cancel := o.ctx()
	defer cancel()
	body := map[string]any{
		"name": s.Name, "modeStrategy": 1, "proportion": prop,
		"ttsCode": s.TTSCode, "ttsText": s.TTSText, "ttsTextVariableMap": map[string]string{},
		"sortMethod": 1, // 1=优先首呼（TaskSortMethodEnum 必填）
		"startDate":  startDate, "endDate": endDate, "dialTimePeriod": period,
		"isPriorityTask": false, "isVmHangup": false,
		"agentNumbers":    []string{}, // 必填（可空集），坐席走 agentGroupCodes
		"agentGroupCodes": s.AgentGroupCodes, "numbers": numberInfos(s.Numbers),
	}
	if s.LineType != "" {
		body["lineType"] = s.LineType // 7cbb285：任务期间仅用该 type 线路（含重试换线锁同 type）
	}
	out, err := cli.CreateCallCenterTask(ctx, body)
	if err != nil || !s.AutoStart {
		return out, err
	}
	if code := extractTaskCode(out); code != "" {
		startOut, startErr := cli.StartCallCenterTask(ctx, code)
		if startErr != nil {
			// createAndImport 在部分 call-center 版本里已经把任务推进运行/调度态；
			// 此时再调 start 会返回 TASK_STATUS_ERROR，但任务已被受理。
			if strings.Contains(startErr.Error(), "Task status is incorrect") {
				return out, nil
			}
			return startOut, startErr
		}
		return startOut, nil
	}
	return out, nil
}

// ---- testkit.BizCaller 适配 ----

// CallCenterTask 适配 BizCaller。
func (o *Orchestrator) CallCenterTask(orgCode, name string, numbers, agentGroups []string, ttsCode, ttsText string, proportion int, startDate, endDate string, dialTimePeriod []string, lineType string, autoStart bool) ([]byte, error) {
	return o.RunCallCenterTask(CallCenterTaskScenario{
		OrgCode: orgCode, Name: name, Numbers: numbers, AgentGroupCodes: agentGroups,
		TTSCode: ttsCode, TTSText: ttsText, Proportion: proportion, StartDate: startDate,
		EndDate: endDate, DialTimePeriod: dialTimePeriod, LineType: lineType, AutoStart: autoStart,
	})
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
