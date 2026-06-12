// Package testkit 提供针对 Hermes 的测试用例：触发一个动作（呼入/外呼/坐席桥接），
// 观测真实链路（mock 侧 SIP 信令 / 桥接），逐步断言并产出可观测结果。
// 不直接查 Hermes 业务库——断言基于真实通话链路本身（mock 收到的 INVITE/应答/桥接）。
package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"hermes-mock/internal/cluster"
	"hermes-mock/internal/config"
	"hermes-mock/internal/entity"
	"hermes-mock/internal/hermesprobe"
	"hermes-mock/internal/model"
	"hermes-mock/internal/orgcfg"
	"hermes-mock/internal/tracelog"
)

// Step 一个测试步骤的结果。
type Step struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
	Optional bool   `json:"optional,omitempty"` // 参考性断言，不计入 run 总体 ok（如本地走不通的坐席腿）
}

// CallPhase 是一通对话里的关键阶段，供前端直接展示通话态。
type CallPhase struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok / pending / fail
	Detail string `json:"detail"`
}

// CallView 是一次场景运行中的单通对话视图。
type CallView struct {
	ID            string      `json:"id"`
	Scenario      string      `json:"scenario"`
	Customer      string      `json:"customer"`
	Agent         string      `json:"agent,omitempty"`
	AgentGroup    string      `json:"agentGroup,omitempty"`
	Status        string      `json:"status"` // CONNECTED / OBSERVED / PENDING / FAILED
	CustomerState string      `json:"customerState"`
	AgentState    string      `json:"agentState,omitempty"`
	TraceID       string      `json:"traceId,omitempty"`
	CallUUID      string      `json:"callUuid,omitempty"`
	Detail        string      `json:"detail,omitempty"`
	DurationMs    int64       `json:"durationMs,omitempty"`
	Phases        []CallPhase `json:"phases,omitempty"`
}

// Run 一次测试用例的执行结果。
type Run struct {
	ID         string         `json:"id"`
	Case       string         `json:"case"`
	OK         bool           `json:"ok"`
	StartedAt  string         `json:"startedAt"`
	DurationMs int64          `json:"durationMs"`
	Steps      []Step         `json:"steps"`
	TraceID    string         `json:"traceId,omitempty"` // 关联的 tracelog 会话
	Artifacts  map[string]any `json:"artifacts,omitempty"`
	Calls      []CallView     `json:"calls,omitempty"`
}

// Kit 测试编排器。
type Kit struct {
	cfg    *config.Config
	prober *hermesprobe.Prober
	bus    *tracelog.Bus
	clu    *cluster.Store   // 客户集群配置缓存（取号/解析；可为 nil）
	repo   model.Repository // 测试运行/呼叫记录落库（可为 nil=仅内存）
	orgs   *orgcfg.Store    // 当前机构接入配置（OpenAPI 服务地址/hermes-ws；可为 nil=未注入）
	orch   BizCaller        // 业务编排器（群呼/自动外呼任务触发；可为 nil）
	http   *http.Client

	mu   sync.Mutex
	runs []Run
}

// BizCaller 是 testkit 触发 Hermes 业务任务所需的最小接口（由 orchestrator 实现）。
// 用接口解耦，避免 testkit 硬依赖 orchestrator 的全部类型。
type BizCaller interface {
	CallCenterTask(req entity.CallCenterTaskReq) ([]byte, error)
	CallBotTask(name string, taskType int, numbers []string, robot, script string) ([]byte, error)
	AutoCall(templateCode string, numbers []string) ([]byte, error)
	OTP(to, templateCode string, params map[string]string) ([]byte, error)
}

func New(cfg *config.Config, prober *hermesprobe.Prober, bus *tracelog.Bus, clu *cluster.Store) *Kit {
	return &Kit{cfg: cfg, prober: prober, bus: bus, clu: clu, http: &http.Client{Timeout: 8 * time.Second}}
}

// SetRepo 注入持久化（main 启动时设置；落 mock_test_run / mock_call）。
func (k *Kit) SetRepo(repo model.Repository) { k.repo = repo }

// SetOrgs 注入机构配置（main 启动时设置）。
func (k *Kit) SetOrgs(orgs *orgcfg.Store) { k.orgs = orgs }

// SetBizCaller 注入业务编排器（main 启动时设置；解耦避免 import cycle）。
func (k *Kit) SetBizCaller(b BizCaller) { k.orch = b }

func dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (k *Kit) Recent() []Run {
	k.mu.Lock()
	out := make([]Run, len(k.runs))
	for i := range k.runs {
		out[i] = k.runs[len(k.runs)-1-i]
	}
	k.mu.Unlock()
	// 内存空（如容器重启后）时从 mock_test_run 回读历史，避免「切页/刷新看不到上次结果」。
	// 合并：内存优先（含 Calls），DB 补内存没有的 RunID（summary + steps，Calls 重启后不还原）。
	if k.repo == nil {
		return out
	}
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := k.repo.ListTestRuns(ctx, 100)
	if err != nil || len(rows) == 0 {
		return out
	}
	seen := make(map[string]bool, len(out))
	for _, r := range out {
		seen[r.ID] = true
	}
	for _, row := range rows {
		if seen[row.RunID] {
			continue
		}
		out = append(out, runFromRow(row))
	}
	return out
}

// runFromRow 从 mock_test_run 行还原 Run（用于重启后回读；不含 Calls 明细）。
func runFromRow(row cluster.TestRunRow) Run {
	r := Run{
		ID: row.RunID, Case: firstStr(row.CaseKind, row.CaseCode), OK: row.OK == 1,
		StartedAt: row.StartedAt.UTC().Format(time.RFC3339), DurationMs: int64(row.DurationMs),
		TraceID: row.TraceID,
	}
	if strings.TrimSpace(row.StepsJSON) != "" {
		_ = json.Unmarshal([]byte(row.StepsJSON), &r.Steps)
	}
	if strings.TrimSpace(row.ArtifactsJSON) != "" {
		_ = json.Unmarshal([]byte(row.ArtifactsJSON), &r.Artifacts)
	}
	return r
}

func (k *Kit) record(r Run) {
	k.mu.Lock()
	k.runs = append(k.runs, r)
	if len(k.runs) > 100 {
		k.runs = k.runs[len(k.runs)-100:]
	}
	k.mu.Unlock()
	// 异步落库（无 DB 时静默跳过）
	if k.clu != nil {
		go k.persist(r)
	}
}

// persist 把一次测试运行落到 mock_test_run。
func (k *Kit) persist(r Run) {
	steps, _ := json.Marshal(r.Steps)
	arts, _ := json.Marshal(r.Artifacts)
	ok := 0
	if r.OK {
		ok = 1
	}
	started, _ := time.Parse(time.RFC3339, r.StartedAt)
	ctx, cancel := dbCtx()
	defer cancel()
	_ = k.repo.CreateTestRun(ctx, &cluster.TestRunRow{
		RunID: r.ID, CaseKind: r.Case, OK: ok, DurationMs: int(r.DurationMs),
		TraceID: r.TraceID, StepsJSON: string(steps), ArtifactsJSON: string(arts),
		StartedAt: started,
	})
	k.persistCallRecords(r, started)
}

func (k *Kit) persistCallRecords(r Run, started time.Time) {
	if len(r.Calls) == 0 {
		return
	}
	steps, _ := json.Marshal(r.Steps)
	ctx, cancel := dbCtx()
	defer cancel()
	for _, c := range r.Calls {
		row := callRecordFromRunCall(r, c, started, string(steps))
		_ = k.repo.SaveCallRecord(ctx, &row)
	}
}

func callRecordFromRunCall(r Run, c CallView, started time.Time, stepsJSON string) cluster.CallRecordRow {
	scenario := firstStr(c.Scenario, r.Case)
	traceID := firstStr(c.TraceID, r.TraceID)
	recordID := "run:" + r.ID + ":" + scenario + ":" + c.Customer + ":" + c.Agent
	if traceID != "" {
		recordID = "trace:" + traceID
	} else if c.CallUUID != "" {
		recordID = "call:" + c.CallUUID
	}
	status := cluster.CallRecordStatusPending
	switch c.Status {
	case "CONNECTED", "OBSERVED":
		status = cluster.CallRecordStatusAnswered
	case "FAILED":
		status = cluster.CallRecordStatusFailed
	}
	last := started.Add(time.Duration(r.DurationMs) * time.Millisecond)
	detail := map[string]any{
		"callView":  c,
		"artifacts": r.Artifacts,
	}
	detailJSON, _ := json.Marshal(detail)
	row := cluster.CallRecordRow{
		RecordID:       recordID,
		Scenario:       scenario,
		Source:         "testkit",
		RunID:          firstStr(artifactString(r.Artifacts, "parentRunId"), r.ID),
		OrgCode:        artifactString(r.Artifacts, "orgCode"),
		TaskName:       taskNameFromRun(r),
		TaskCode:       firstStr(artifactString(r.Artifacts, "taskCode"), artifactString(r.Artifacts, "templateCode"), artifactString(r.Artifacts, "callUuid")),
		CustomerGroup:  artifactString(r.Artifacts, "customerGroup"),
		CustomerNumber: c.Customer,
		AgentGroupCode: firstStr(c.AgentGroup, artifactString(r.Artifacts, "agentGroup"), artifactString(r.Artifacts, "agentGroups")),
		AgentNumber:    c.Agent,
		Direction:      "HERMES_TO_MOCK",
		CallType:       r.Case,
		Status:         status,
		Result:         firstStr(c.Detail, status),
		TraceID:        traceID,
		CallUUID:       c.CallUUID,
		StartedAt:      started.UTC(),
		DurationMs:     c.DurationMs,
		LastEventAt:    last.UTC(),
		StepsJSON:      stepsJSON,
		DetailJSON:     string(detailJSON),
		LastSummary:    c.Detail,
	}
	if row.DurationMs == 0 {
		row.DurationMs = r.DurationMs
	}
	return row
}

func taskNameFromRun(r Run) string {
	if v := artifactString(r.Artifacts, "taskName"); v != "" {
		return v
	}
	switch r.Case {
	case "otp":
		return "OTP " + artifactString(r.Artifacts, "templateCode")
	case "autocall":
		return "自动外呼 " + artifactString(r.Artifacts, "templateCode")
	default:
		return r.Case
	}
}

func artifactString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		if ss, ok := m[key].([]string); ok && len(ss) > 0 {
			return strings.Join(ss, ",")
		}
		if xs, ok := m[key].([]any); ok && len(xs) > 0 {
			parts := make([]string, 0, len(xs))
			for _, x := range xs {
				if s, ok := x.(string); ok && s != "" {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, ",")
			}
		}
	}
	return ""
}

func firstStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// parseTaskCode 从 Hermes createAndImport 响应里尽力提取任务 code（兼容 data 为字符串 / 对象.code/.taskCode /
// 顶层 code/taskCode）。testkit 不依赖 orchestrator（BizCaller 接口解耦），故本地复刻提取逻辑。
func parseTaskCode(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
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
		if c, ok := d["code"].(string); ok && c != "" {
			return c
		}
		if c, ok := d["taskCode"].(string); ok && c != "" {
			return c
		}
	}
	if c, ok := m["code"].(string); ok && c != "" {
		return c
	}
	if c, ok := m["taskCode"].(string); ok && c != "" {
		return c
	}
	return ""
}

// CallCenterTaskParams call-center 群呼任务用例：建任务+导入客户号+启动，断言客户腿进 mock；
// 如指定 ObserveAgent，则断言该坐席工作台 WS 收到来电/调度通知。
type CallCenterTaskParams struct {
	OrgCode       string   `json:"orgCode"`
	Name          string   `json:"name"`
	CustomerGroup string   `json:"customerGroup"`
	CustomerLimit int      `json:"customerLimit"`
	Numbers       []string `json:"numbers"`      // 客户号（→mock 客户线路）
	AgentGroups   []string `json:"agentGroups"`  // 转接坐席组（真实 Hermes 技能组；与 AgentNumbers 二选一，仅取 1 个）
	AgentNumbers  []string `json:"agentNumbers"` // 指定坐席号（与 AgentGroups 二选一）
	ObserveAgent  string   `json:"observeAgent"` // 期望收到工作台 WS 通知的坐席号（空则只断客户腿）
	TTSCode       string   `json:"ttsCode"`
	TTSText       string   `json:"ttsText"`
	// 模式策略组合（对照 Hermes）：ModeStrategy=1→Proportion；=2→LossRate+HistoricalConnectionRate。
	ModeStrategy             int      `json:"modeStrategy"`
	Proportion               int      `json:"proportion"`
	LossRate                 int      `json:"lossRate"`
	HistoricalConnectionRate int      `json:"historicalConnectionRate"`
	SortMethod               int      `json:"sortMethod"`     // 1=优先首呼 2=优先重呼
	IsPriorityTask           bool     `json:"isPriorityTask"` // 优先任务
	IsVmHangup               bool     `json:"isVmHangup"`     // 语音信箱即挂
	MaxRedialTimes           int      `json:"maxRedialTimes"`
	RedialInterval           int      `json:"redialInterval"`
	BestRingDuration         int      `json:"bestRingDuration"`
	AgentMaxRingDuration     int      `json:"agentMaxRingDuration"`
	AssignDelaySeconds       int      `json:"assignDelaySeconds"`
	TransferType             string   `json:"transferType"`
	Description              string   `json:"description"`
	StartDate                string   `json:"startDate"`
	EndDate                  string   `json:"endDate"`
	DialTimePeriod           []string `json:"dialTimePeriod"`
	// LineType 线路类型（Hermes 7cbb285：任务期间仅用该 type 线路选号；空=默认 base）。
	LineType string `json:"lineType"`
	WaitSec  int    `json:"waitSec"` // 观测腿的最长秒（默认 90，预测式拨号按 Quartz 分钟级调度）
}

// toReq 把测试用例参数装配成 Hermes 群呼请求（号码已在调用前 resolve 好）。
func (p CallCenterTaskParams) toReq() entity.CallCenterTaskReq {
	return entity.CallCenterTaskReq{
		OrgCode: p.OrgCode, Name: p.Name, Numbers: p.Numbers,
		AgentGroupCodes: p.AgentGroups, AgentNumbers: p.AgentNumbers,
		TTSCode: p.TTSCode, TTSText: p.TTSText,
		ModeStrategy: p.ModeStrategy, Proportion: p.Proportion,
		LossRate: p.LossRate, HistoricalConnectionRate: p.HistoricalConnectionRate,
		SortMethod: p.SortMethod, IsPriorityTask: p.IsPriorityTask, IsVmHangup: p.IsVmHangup,
		MaxRedialTimes: p.MaxRedialTimes, RedialInterval: p.RedialInterval,
		BestRingDuration: p.BestRingDuration, AgentMaxRingDuration: p.AgentMaxRingDuration,
		AssignDelaySeconds: p.AssignDelaySeconds, TransferType: p.TransferType, Description: p.Description,
		StartDate: p.StartDate, EndDate: p.EndDate, DialTimePeriod: p.DialTimePeriod, LineType: p.LineType,
	}
}

// RunCallCenterTaskObserved 触发 call-center 群呼任务并观测链路：
// ① 经业务接口建任务+启动 ② 断言客户腿(任一导入号)进 mock ③ 可选断言坐席工作台 WS 通知。
// 全程真实：业务任务驱动 FS 预测式拨号 → mock 接客户腿 → Hermes 工作台通知坐席。断言基于链路，不查库。
func (k *Kit) RunCallCenterTaskObserved(p CallCenterTaskParams) Run {
	start := time.Now()
	r := Run{ID: uuid.NewString()[:8], Case: "callcenter-task", StartedAt: start.UTC().Format(time.RFC3339), Artifacts: map[string]any{}}
	if k.orch == nil {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "业务编排器未注入（先到「机构」页配置 call-center 地址）"})
		return k.finish(r, start)
	}
	if p.WaitSec <= 0 {
		p.WaitSec = 90
	}
	p.Numbers = k.resolveCustomerNumbers(p.CustomerGroup, p.Numbers, p.CustomerLimit, 20)
	if len(p.Numbers) == 0 {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "未提供客户号码"})
		return k.finish(r, start)
	}
	sess := k.bus.OpenSession("test", "群呼任务 "+p.Name)
	r.TraceID = sess
	r.Artifacts["numbers"] = len(p.Numbers)
	r.Artifacts["agentGroups"] = p.AgentGroups
	r.Artifacts["customerGroup"] = p.CustomerGroup
	r.Artifacts["taskName"] = p.Name
	r.Artifacts["orgCode"] = p.OrgCode

	// Step 1: 经业务接口建任务并启动
	k.bus.Emit(sess, "", tracelog.ChanFlow, tracelog.DirOut, "群呼任务",
		fmt.Sprintf("建群呼任务 %s 并启动：%d 客户号→坐席组 %v", p.Name, len(p.Numbers), p.AgentGroups), nil)
	out, err := k.orch.CallCenterTask(p.toReq())
	if err != nil {
		r.Steps = append(r.Steps, Step{Name: "创建群呼任务", OK: false, Detail: err.Error() + " " + clip(string(out), 200)})
		r.Calls = k.callViewsForCustomers(start, p.Numbers, "callcenter-task", p.ObserveAgent, strings.Join(p.AgentGroups, ","))
		return k.finish(r, start)
	}
	// 提取 Hermes 任务 code，暴露给前端做暂停/恢复/取消（createAndImport 后即自动拨号，无需 start）。
	if code := parseTaskCode(out); code != "" {
		r.Artifacts["taskCode"] = code
	}
	r.Steps = append(r.Steps, Step{Name: "创建群呼任务（即自动拨号）", OK: true, Detail: "Hermes 已受理：" + clip(string(out), 160)})

	// Step 2: 断言客户腿接通（任一导入号被预测式拨到 mock 且 INVITE 200 OK）
	if hit, ev := k.waitAnyLegInviteOK(start, p.Numbers, time.Duration(p.WaitSec)*time.Second); !ev.Answered {
		r.Steps = append(r.Steps, Step{Name: "断言 客户腿接通", OK: false,
			Detail: fmt.Sprintf("%ds 内未观测到任一客户号 %v 的 INVITE 200 OK（%s）", p.WaitSec, p.Numbers, ev.Detail)})
		r.Calls = k.callViewsForCustomers(start, p.Numbers, "callcenter-task", p.ObserveAgent, strings.Join(p.AgentGroups, ","))
		return k.finish(r, start)
	} else {
		r.Steps = append(r.Steps, Step{Name: "断言 客户腿接通", OK: true, Detail: hit + " " + ev.Detail})
	}

	// Step 3: 断言坐席腿接通（群呼客户接通后转接坐席）。坐席腿走 FS↔浏览器 jssip，不经 mock SIP，
	// 故由前端坐席软电话回存的 agent-inbound 记录观测。标 Optional：本地 RTP/转坐席链路常走不通，
	// 不计入 run 总体 ok（仅展示坐席侧是否接通），避免本地环境把整条 run 判失败。
	// 只做短观测（12s）拿即时快照；后续进展由群呼页前端轮询 agent-inbound 记录实时更新。
	seatAns, seatAgent, seatDetail := k.waitSeatTransferAnswered(start, p.AgentGroups, 12*time.Second)
	r.Steps = append(r.Steps, Step{Name: "断言 坐席腿接通", OK: seatAns, Optional: true, Detail: seatDetail})
	if seatAns {
		r.Artifacts["seatAnswered"] = seatAgent
	}

	r.Calls = k.callViewsForCustomers(start, p.Numbers, "callcenter-task", p.ObserveAgent, strings.Join(p.AgentGroups, ","))
	return k.finish(r, start)
}

// waitSeatTransferAnswered 在 start 之后观测「坐席被转接接通」：查前端坐席软电话回存的 agent-inbound 记录
// （scenario=agent-inbound、ANSWERED）。本地坐席腿不经 mock SIP，故只能靠前端回存的记录判定。
func (k *Kit) waitSeatTransferAnswered(start time.Time, agentGroups []string, timeout time.Duration) (bool, string, string) {
	if k.repo == nil {
		return false, "", "无持久化，无法观测坐席侧（需 DB）"
	}
	deadline := time.Now().Add(timeout)
	from := start.Add(-2 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := dbCtx()
		recs, _, err := k.repo.ListCallRecords(ctx, cluster.CallRecordFilter{Scenario: "agent-inbound", PageSize: 20})
		cancel()
		if err == nil {
			for _, rec := range recs {
				if rec.StartedAt.Before(from) {
					continue
				}
				if rec.Status == cluster.CallRecordStatusEnded || rec.Status == cluster.CallRecordStatusAnswered {
					return true, rec.AgentNumber, fmt.Sprintf("坐席 %s 接听了转接来电（agent-inbound 记录）", rec.AgentNumber)
				}
			}
		}
		time.Sleep(800 * time.Millisecond)
	}
	return false, "", "未观测到坐席被转接接通（坐席软电话需在线就绪；本地 RTP/转坐席链路常走不通——属已知限制，不计入 run 成败）"
}

// ---- 换线重试观测（Hermes 2026-06：47bf482 usedSet 按 lineCode / b90673c channelUuid 隔离）----

// InviteObservation 同一被叫号的一次 INVITE 观测（按 Call-ID 去重，重传合并）。
type InviteObservation struct {
	Session   string    `json:"session"`
	CallID    string    `json:"callId"`
	LineName  string    `json:"lineName"`  // INVITE 携带的 X-Line-Name 业务头（哪条线路打来的）
	FinalCode string    `json:"finalCode"` // 该 INVITE 的最终响应（200/486/503…；空=尚无最终响应）
	At        time.Time `json:"at"`
}

// collectInvitesForCallee 收集 start 之后该被叫号的全部 INVITE 观测序列（时间升序）。
// 换线重试断言的核心原语：每次（含重拨）INVITE 是独立 SIP Call-ID + 独立 X-Line-Name 证据。
// 注意：Event.CallID 是聚合键（业务 callUuid 优先）——重拨 callUuid 不变（b90673c 语义），
// 故去重必须用 Headers 里的真实 SIP Call-ID（每次新拨号一定换新）。
func (k *Kit) collectInvitesForCallee(start time.Time, number string) []InviteObservation {
	byCallID := map[string]*InviteObservation{}
	var order []string
	for _, s := range k.bus.Sessions() {
		if s.StartedAt.Before(start.Add(-time.Second)) {
			continue
		}
		for _, e := range s.Events {
			if e.Channel != tracelog.ChanSIP || !eventMatchesLeg(e, number) {
				continue
			}
			sipCallID := headerValue(e.Headers, "Call-ID", "i")
			switch {
			case e.Method == "INVITE" && e.Dir == tracelog.DirIn:
				key := firstStr(sipCallID, e.CallID, s.ID)
				if _, seen := byCallID[key]; !seen {
					byCallID[key] = &InviteObservation{Session: s.ID, CallID: key, LineName: headerValue(e.Headers, "X-Line-Name"), At: e.TS}
					order = append(order, key)
				}
			case e.Dir == tracelog.DirOut && isSIPInviteResponse(e.Headers):
				if code, ok := sipMethodStatusCode(e.Method); ok && code >= 200 {
					if obs, seen := byCallID[firstStr(sipCallID, e.CallID, s.ID)]; seen && obs.FinalCode == "" {
						obs.FinalCode = e.Method
					}
				}
			}
		}
	}
	out := make([]InviteObservation, 0, len(order))
	for _, key := range order {
		out = append(out, *byCallID[key])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// distinctLineNames 观测序列中互异的线路名（空 LineName 不计）。
func distinctLineNames(obs []InviteObservation) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range obs {
		n := strings.TrimSpace(o.LineName)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// allCallIDsDistinct 每次 INVITE 的 Call-ID 是否互异（b90673c：换线重拨必须用新 channelUuid，
// SIP 侧表现为每次独立 Call-ID；复用即旧 bug 回归的强信号）。
func allCallIDsDistinct(obs []InviteObservation) bool {
	seen := map[string]bool{}
	for _, o := range obs {
		if o.CallID == "" {
			continue
		}
		if seen[o.CallID] {
			return false
		}
		seen[o.CallID] = true
	}
	return true
}

// anyAnswered 是否有 INVITE 最终 200。
func anyAnswered(obs []InviteObservation) bool {
	for _, o := range obs {
		if o.FinalCode == "200" {
			return true
		}
	}
	return false
}

func headerValue(headers []tracelog.HeaderKV, names ...string) string {
	for _, name := range names {
		for _, h := range headers {
			if strings.EqualFold(h.Name, name) {
				return strings.TrimSpace(h.Value)
			}
		}
	}
	return ""
}

// RetrySwitchLineParams 换线重试场景：对一个被叫号发起群呼，断言「失败→重试→换线→最终接通」。
// 前置（cluster 配置）：≥2 条线路绑「拒接」客户组、≥1 条线路绑「接听」客户组，被叫号同时命中两边号段——
// Hermes 首呼落在拒接线即失败触发重试换线（47bf482），换到接听线后 200（按线路区分行为，无需有状态行为档）。
type RetrySwitchLineParams struct {
	OrgCode       string   `json:"orgCode"`
	Name          string   `json:"name"`
	Number        string   `json:"number"`        // 单个被叫号（与 CustomerGroup 二选一）
	CustomerGroup string   `json:"customerGroup"` // 从组取 1 个号
	AgentGroups   []string `json:"agentGroups"`
	TTSCode       string   `json:"ttsCode"`
	TTSText       string   `json:"ttsText"`
	LineType      string   `json:"lineType"` // 可选：锁 type 测「重试锁同 type 换线」（cc6251c）
	WaitSec       int      `json:"waitSec"`  // 默认 150（预测式分钟级调度 + 5s 重试窗）
}

// RunRetrySwitchLine 换线重试场景：群呼 1 个号 → 收集该号全部 INVITE 观测 → 断言
// ①发生重试（≥2 次 INVITE）②换线生效（X-Line-Name 互异，47bf482）③每次拨号独立 Call-ID（b90673c）④最终 200。
func (k *Kit) RunRetrySwitchLine(p RetrySwitchLineParams) Run {
	start := time.Now()
	r := Run{ID: uuid.NewString()[:8], Case: "retry-switch-line", StartedAt: start.UTC().Format(time.RFC3339), Artifacts: map[string]any{}}
	if k.orch == nil {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "业务编排器未注入（先到「机构」页配置 call-center 地址）"})
		return k.finish(r, start)
	}
	if p.WaitSec <= 0 {
		p.WaitSec = 150
	}
	if p.Name == "" {
		p.Name = "mock_retry_" + uuid.NewString()[:6]
	}
	numbers := k.resolveCustomerNumbers(p.CustomerGroup, compactLimit([]string{p.Number}, 1), 1, 1)
	if len(numbers) == 0 {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "未提供被叫号（number 或 customerGroup 取号）"})
		return k.finish(r, start)
	}
	number := numbers[0]
	sess := k.bus.OpenSession("test", "换线重试 → "+number)
	r.TraceID = sess
	r.Artifacts["customer"] = number
	r.Artifacts["taskName"] = p.Name
	r.Artifacts["orgCode"] = p.OrgCode
	r.Artifacts["lineType"] = p.LineType

	// Step 1: 群呼该号（预测式拨号驱动首呼；首呼落拒接线 → call-center 5s 窗重试换线）
	k.bus.Emit(sess, "", tracelog.ChanFlow, tracelog.DirOut, "换线重试",
		fmt.Sprintf("建群呼任务 %s（被叫 %s，lineType=%s）以触发失败重试换线", p.Name, number, orStr(p.LineType, "默认")), nil)
	out, err := k.orch.CallCenterTask(entity.CallCenterTaskReq{
		OrgCode: p.OrgCode, Name: p.Name, Numbers: []string{number},
		AgentGroupCodes: p.AgentGroups, TTSCode: p.TTSCode, TTSText: p.TTSText,
		ModeStrategy: 1, Proportion: 1, LineType: p.LineType,
	})
	if err != nil {
		r.Steps = append(r.Steps, Step{Name: "创建并启动群呼任务", OK: false, Detail: err.Error() + " " + clip(string(out), 200)})
		return k.finish(r, start)
	}
	r.Steps = append(r.Steps, Step{Name: "创建并启动群呼任务", OK: true, Detail: "Hermes 已受理：" + clip(string(out), 160)})

	// Step 2: 轮询收集 INVITE 观测，直到「≥2 次 + 换线 + 最终 200」或超时（超时后按已观测到的评估）
	deadline := time.Now().Add(time.Duration(p.WaitSec) * time.Second)
	var obs []InviteObservation
	for time.Now().Before(deadline) {
		obs = k.collectInvitesForCallee(start, number)
		if len(obs) >= 2 && len(distinctLineNames(obs)) >= 2 && anyAnswered(obs) {
			break
		}
		time.Sleep(time.Second)
	}
	lines := distinctLineNames(obs)
	seq := make([]map[string]string, 0, len(obs))
	for _, o := range obs {
		seq = append(seq, map[string]string{"lineName": o.LineName, "callId": o.CallID, "final": o.FinalCode})
	}
	r.Artifacts["invites"] = seq

	r.Steps = append(r.Steps, Step{Name: "断言 发生失败重试（≥2 次 INVITE）", OK: len(obs) >= 2,
		Detail: fmt.Sprintf("观测到 %d 次 INVITE（%v）", len(obs), summarizeInvites(obs))})
	r.Steps = append(r.Steps, Step{Name: "断言 重试已换线（X-Line-Name 互异）", OK: len(lines) >= 2,
		Detail: fmt.Sprintf("涉及线路 %v（47bf482：usedSet 按 lineCode 排除已用线）", lines)})
	r.Steps = append(r.Steps, Step{Name: "断言 每次拨号独立 Call-ID", OK: len(obs) > 0 && allCallIDsDistinct(obs),
		Detail: "b90673c：换线重拨用新 channelUuid，SIP 侧每次独立 Call-ID"})
	r.Steps = append(r.Steps, Step{Name: "断言 最终接通（INVITE 200 OK）", OK: anyAnswered(obs),
		Detail: "换到接听线路后 mock 返回 200"})
	return k.finish(r, start)
}

func summarizeInvites(obs []InviteObservation) []string {
	out := make([]string, 0, len(obs))
	for _, o := range obs {
		out = append(out, fmt.Sprintf("%s→%s", orStr(o.LineName, "?"), orStr(o.FinalCode, "…")))
	}
	return out
}

// AutoCallParams call-bot 自动外呼用例：对一批客户号发起自动外呼，断言机器人腿/客户腿进 mock。
type AutoCallParams struct {
	TemplateCode  string   `json:"templateCode"`
	CustomerGroup string   `json:"customerGroup"`
	CustomerLimit int      `json:"customerLimit"`
	Numbers       []string `json:"numbers"`
	WaitSec       int      `json:"waitSec"`
}

// RunAutoCallObserved 触发 call-bot 自动外呼并断言客户腿进 mock。
func (k *Kit) RunAutoCallObserved(p AutoCallParams) Run {
	start := time.Now()
	r := Run{ID: uuid.NewString()[:8], Case: "autocall", StartedAt: start.UTC().Format(time.RFC3339), Artifacts: map[string]any{}}
	if k.orch == nil {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "业务编排器未注入（先到「机构」页配置 call-bot 地址）"})
		return k.finish(r, start)
	}
	if p.WaitSec <= 0 {
		p.WaitSec = 20
	}
	p.Numbers = k.resolveCustomerNumbers(p.CustomerGroup, p.Numbers, p.CustomerLimit, 20)
	if len(p.Numbers) == 0 {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "未提供客户号码"})
		return k.finish(r, start)
	}
	sess := k.bus.OpenSession("test", "自动外呼 "+p.TemplateCode)
	r.TraceID = sess
	r.Artifacts["numbers"] = len(p.Numbers)
	r.Artifacts["customerGroup"] = p.CustomerGroup
	r.Artifacts["taskName"] = "自动外呼 " + p.TemplateCode
	r.Artifacts["templateCode"] = p.TemplateCode

	k.bus.Emit(sess, "", tracelog.ChanFlow, tracelog.DirOut, "自动外呼",
		fmt.Sprintf("call-bot 自动外呼 模板=%s：%d 客户号", p.TemplateCode, len(p.Numbers)), nil)
	out, err := k.orch.AutoCall(p.TemplateCode, p.Numbers)
	if err != nil {
		r.Steps = append(r.Steps, Step{Name: "发起自动外呼", OK: false, Detail: err.Error() + " " + clip(string(out), 200)})
		r.Calls = k.callViewsForCustomers(start, p.Numbers, "autocall", "", "")
		return k.finish(r, start)
	}
	r.Steps = append(r.Steps, Step{Name: "发起自动外呼", OK: true, Detail: "Hermes 已受理：" + clip(string(out), 160)})

	if hit, ev := k.waitAnyLegInviteOK(start, p.Numbers, time.Duration(p.WaitSec)*time.Second); !ev.Answered {
		r.Steps = append(r.Steps, Step{Name: "断言 客户腿接通", OK: false,
			Detail: fmt.Sprintf("%ds 内未观测到任一客户号 %v 的 INVITE 200 OK（%s）", p.WaitSec, p.Numbers, ev.Detail)})
		r.Calls = k.callViewsForCustomers(start, p.Numbers, "autocall", "", "")
		return k.finish(r, start)
	} else {
		r.Steps = append(r.Steps, Step{Name: "断言 客户腿接通", OK: true, Detail: hit + " " + ev.Detail})
	}
	r.Calls = k.callViewsForCustomers(start, p.Numbers, "autocall", "", "")
	r.Steps = append(r.Steps, Step{Name: "断言 客户腿进 mock", OK: true, Detail: "已观测到客户腿（机器人外呼到 mock 成立）"})
	return k.finish(r, start)
}

// CallBotTaskParams call-bot 任务用例：建机器人任务并导入客户号，断言客户腿进 mock。
type CallBotTaskParams struct {
	Name          string   `json:"name"`
	TaskType      int      `json:"taskType"` // 1=IVR 2=AI_CALL
	Robot         string   `json:"robotCode"`
	Script        string   `json:"salesScriptCode"`
	CustomerGroup string   `json:"customerGroup"`
	CustomerLimit int      `json:"customerLimit"`
	Numbers       []string `json:"numbers"`
	WaitSec       int      `json:"waitSec"`
}

// RunCallBotTaskObserved 触发 call-bot 任务并断言客户腿进 mock。
func (k *Kit) RunCallBotTaskObserved(p CallBotTaskParams) Run {
	start := time.Now()
	r := Run{ID: uuid.NewString()[:8], Case: "callbot-task", StartedAt: start.UTC().Format(time.RFC3339), Artifacts: map[string]any{}}
	if k.orch == nil {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "业务编排器未注入（先到「机构」页配置 call-bot 地址）"})
		return k.finish(r, start)
	}
	if p.WaitSec <= 0 {
		p.WaitSec = 60
	}
	if p.TaskType == 0 {
		p.TaskType = 2
	}
	p.Numbers = k.resolveCustomerNumbers(p.CustomerGroup, p.Numbers, p.CustomerLimit, 20)
	if len(p.Numbers) == 0 {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "未提供客户号码"})
		return k.finish(r, start)
	}
	sess := k.bus.OpenSession("test", "call-bot任务 "+p.Name)
	r.TraceID = sess
	r.Artifacts["numbers"] = len(p.Numbers)
	r.Artifacts["customerGroup"] = p.CustomerGroup
	r.Artifacts["taskName"] = p.Name
	r.Artifacts["taskCode"] = p.Robot

	k.bus.Emit(sess, "", tracelog.ChanFlow, tracelog.DirOut, "call-bot任务",
		fmt.Sprintf("建 call-bot 任务 %s：%d 客户号", p.Name, len(p.Numbers)), nil)
	out, err := k.orch.CallBotTask(p.Name, p.TaskType, p.Numbers, p.Robot, p.Script)
	if err != nil {
		r.Steps = append(r.Steps, Step{Name: "创建 call-bot 任务", OK: false, Detail: err.Error() + " " + clip(string(out), 200)})
		r.Calls = k.callViewsForCustomers(start, p.Numbers, "callbot-task", "", "")
		return k.finish(r, start)
	}
	r.Steps = append(r.Steps, Step{Name: "创建 call-bot 任务", OK: true, Detail: "Hermes 已受理：" + clip(string(out), 160)})

	if hit, ev := k.waitAnyLegInviteOK(start, p.Numbers, time.Duration(p.WaitSec)*time.Second); !ev.Answered {
		r.Steps = append(r.Steps, Step{Name: "断言 客户腿接通", OK: false,
			Detail: fmt.Sprintf("%ds 内未观测到任一客户号 %v 的 INVITE 200 OK（%s）", p.WaitSec, p.Numbers, ev.Detail)})
		r.Calls = k.callViewsForCustomers(start, p.Numbers, "callbot-task", "", "")
		return k.finish(r, start)
	} else {
		r.Steps = append(r.Steps, Step{Name: "断言 客户腿接通", OK: true, Detail: hit + " " + ev.Detail})
	}
	r.Calls = k.callViewsForCustomers(start, p.Numbers, "callbot-task", "", "")
	return k.finish(r, start)
}

// OTPParams 语音验证码用例：Hermes OTP 主动呼叫外部客户，mock 扮演客户接听。
type OTPParams struct {
	To            string            `json:"to"`
	CustomerGroup string            `json:"customerGroup,omitempty"`
	TemplateCode  string            `json:"templateCode"`
	Params        map[string]string `json:"params"`
	WaitSec       int               `json:"waitSec"`
}

type OTPBatchParams struct {
	CustomerGroup string            `json:"customerGroup"`
	CustomerLimit int               `json:"customerLimit"`
	Numbers       []string          `json:"numbers"`
	TemplateCode  string            `json:"templateCode"`
	Params        map[string]string `json:"params"`
	WaitSec       int               `json:"waitSec"`
	Concurrent    bool              `json:"concurrent"`
}

// RunOTPObserved 触发 OTP 语音验证码并断言客户腿进 mock。
func (k *Kit) RunOTPObserved(p OTPParams) Run {
	start := time.Now()
	r := Run{ID: uuid.NewString()[:8], Case: "otp", StartedAt: start.UTC().Format(time.RFC3339), Artifacts: map[string]any{}}
	if k.orch == nil {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "业务编排器未注入（先到「机构」页配置 otp 地址）"})
		return k.finish(r, start)
	}
	if p.WaitSec <= 0 {
		p.WaitSec = 30
	}
	if p.To == "" {
		r.Steps = append(r.Steps, Step{Name: "校验", OK: false, Detail: "未提供客户号码"})
		return k.finish(r, start)
	}
	sess := k.bus.OpenSession("test", "OTP 语音验证码 → "+p.To)
	r.TraceID = sess
	r.Artifacts["to"] = p.To
	r.Artifacts["customer"] = p.To
	r.Artifacts["customerGroup"] = p.CustomerGroup
	r.Artifacts["templateCode"] = p.TemplateCode
	r.Artifacts["taskName"] = "OTP " + p.TemplateCode

	k.bus.Emit(sess, "", tracelog.ChanFlow, tracelog.DirOut, "OTP语音验证码",
		fmt.Sprintf("hermes-otp 下发语音验证码 模板=%s 客户号=%s", p.TemplateCode, p.To), nil)
	out, err := k.orch.OTP(p.To, p.TemplateCode, p.Params)
	if err != nil {
		r.Steps = append(r.Steps, Step{Name: "下发 OTP 语音验证码", OK: false, Detail: err.Error() + " " + clip(string(out), 200)})
		r.Calls = k.callViewsForCustomers(start, []string{p.To}, "otp", "", "")
		return k.finish(r, start)
	}
	r.Steps = append(r.Steps, Step{Name: "下发 OTP 语音验证码", OK: true, Detail: "Hermes 已受理：" + clip(string(out), 160)})

	if _, ev := k.waitAnyLegInviteOK(start, []string{p.To}, time.Duration(p.WaitSec)*time.Second); !ev.Answered {
		r.Steps = append(r.Steps, Step{Name: "断言 客户腿接通", OK: false,
			Detail: fmt.Sprintf("%ds 内未观测到客户号 %s 的 INVITE 200 OK（%s）", p.WaitSec, p.To, ev.Detail)})
		r.Calls = k.callViewsForCustomers(start, []string{p.To}, "otp", "", "")
		return k.finish(r, start)
	}
	r.Calls = k.callViewsForCustomers(start, []string{p.To}, "otp", "", "")
	r.Steps = append(r.Steps, Step{Name: "断言 客户腿接通", OK: true, Detail: "客户腿已 INVITE 200（OTP 呼叫到 mock 接通）"})
	return k.finish(r, start)
}

// RunOTPBatchObserved 对一组 mock 客户逐个下发 OTP 语音验证码并聚合通话态。
func (k *Kit) RunOTPBatchObserved(p OTPBatchParams) ScenarioResult {
	start := time.Now()
	res := ScenarioResult{ID: uuid.NewString()[:8], GroupCode: p.CustomerGroup, StartedAt: start.UTC().Format(time.RFC3339)}
	numbers := k.resolveCustomerNumbers(p.CustomerGroup, p.Numbers, p.CustomerLimit, 10)
	if len(numbers) == 0 {
		res.DurMs = time.Since(start).Milliseconds()
		return res
	}
	res.Total = len(numbers)
	runOne := func(n string) Run {
		return k.RunOTPObserved(OTPParams{To: n, CustomerGroup: p.CustomerGroup, TemplateCode: p.TemplateCode, Params: p.Params, WaitSec: p.WaitSec})
	}
	if p.Concurrent {
		var wg sync.WaitGroup
		runs := make([]Run, len(numbers))
		for i, n := range numbers {
			wg.Add(1)
			go func(i int, n string) { defer wg.Done(); runs[i] = runOne(n) }(i, n)
		}
		wg.Wait()
		res.Runs = runs
	} else {
		for _, n := range numbers {
			res.Runs = append(res.Runs, runOne(n))
		}
	}
	for _, r := range res.Runs {
		if r.OK {
			res.Passed++
		} else {
			res.Failed++
		}
	}
	res.Calls = callsFromRuns(res.Runs)
	res.Metrics = computeMetrics(res.Runs, p.Concurrent)
	res.DurMs = time.Since(start).Milliseconds()
	return res
}

// waitAnyLegInviteOK 升级版断言：在一组号码里观测**任一号收到 INVITE 且返回 200 OK**才算接通，
// 而不是仅"腿出现"。返回命中的号码 + 证据；都没接通则返回最有信息量的一条证据（仅振铃/只看到INVITE/全无）。
// 修复"观测靠时间窗+号码子串匹配、撞号即误判 PASS"——INVITE 200 才是真接通证据。
func (k *Kit) waitAnyLegInviteOK(start time.Time, numbers []string, timeout time.Duration) (string, legInviteEvidence) {
	deadline := time.Now().Add(timeout)
	best := legInviteEvidence{Detail: fmt.Sprintf("未观测到 %v 任一号的 SIP INVITE", numbers)}
	bestNum := ""
	for time.Now().Before(deadline) {
		for _, s := range k.bus.Sessions() {
			if s.StartedAt.Before(start.Add(-time.Second)) {
				continue
			}
			for _, n := range numbers {
				ev := legInviteEvidenceFromSession(s, n)
				if ev.Answered {
					return n, ev
				}
				if ev.Seen {
					best, bestNum = ev, n
				}
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return bestNum, best
}

func (k *Kit) finish(r Run, start time.Time) Run {
	r.DurationMs = time.Since(start).Milliseconds()
	r.OK = true
	for _, s := range r.Steps {
		if s.Optional {
			continue // 参考性步骤（如坐席腿）不计入总体 ok
		}
		if !s.OK {
			r.OK = false
			break
		}
	}
	if len(r.Calls) == 0 {
		if c, ok := callViewFromRun(r); ok {
			r.Calls = []CallView{c}
		}
	}
	k.record(r)
	return r
}

func callViewFromRun(r Run) (CallView, bool) {
	customer, _ := r.Artifacts["customer"].(string)
	agent, _ := r.Artifacts["agent"].(string)
	agentGroup, _ := r.Artifacts["agentGroup"].(string)
	callUUID, _ := r.Artifacts["callUuid"].(string)
	if customer == "" && r.Case != "otp" {
		return CallView{}, false
	}
	status := "FAILED"
	if r.OK {
		status = "CONNECTED"
	}
	c := CallView{
		ID:            r.ID,
		Scenario:      r.Case,
		Customer:      customer,
		Agent:         agent,
		AgentGroup:    agentGroup,
		Status:        status,
		TraceID:       r.TraceID,
		CallUUID:      callUUID,
		DurationMs:    r.DurationMs,
		CustomerState: "未观测",
		AgentState:    "未观测",
	}
	for _, s := range r.Steps {
		phase := CallPhase{Name: s.Name, Detail: s.Detail}
		if s.OK {
			phase.Status = "ok"
		} else {
			phase.Status = "fail"
		}
		c.Phases = append(c.Phases, phase)
		if (strings.Contains(s.Name, "客户腿") || strings.Contains(s.Name, "A腿")) && s.OK {
			c.CustomerState = "已接入"
		}
		if (strings.Contains(s.Name, "坐席腿") || strings.Contains(s.Name, "B腿")) && s.OK {
			c.AgentState = "已接入"
		}
	}
	if c.CustomerState == "未观测" && r.OK {
		c.CustomerState = "已接入"
	}
	if c.Agent == "" {
		c.AgentState = ""
	}
	if !r.OK && len(r.Steps) > 0 {
		c.Detail = r.Steps[len(r.Steps)-1].Detail
	}
	return c, true
}

func callsFromRuns(runs []Run) []CallView {
	out := make([]CallView, 0, len(runs))
	for _, r := range runs {
		if len(r.Calls) > 0 {
			out = append(out, r.Calls...)
			continue
		}
		if c, ok := callViewFromRun(r); ok {
			out = append(out, c)
		}
	}
	return out
}

func (k *Kit) resolveCustomerNumbers(groupCode string, numbers []string, limit, def int) []string {
	out := make([]string, 0, len(numbers))
	for _, n := range numbers {
		n = strings.TrimSpace(n)
		if n != "" {
			out = append(out, n)
		}
	}
	if len(out) > 0 {
		return out
	}
	if limit <= 0 {
		limit = def
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	return k.groupNumbers(groupCode, limit)
}

func compactLimit(values []string, limit int) []string {
	if limit <= 0 {
		limit = len(values)
	}
	out := make([]string, 0, limit)
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (k *Kit) callViewsForCustomers(start time.Time, numbers []string, scenario, agent, agentGroup string) []CallView {
	out := make([]CallView, 0, len(numbers))
	for _, n := range numbers {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		c := CallView{
			ID:            scenario + "-" + n,
			Scenario:      scenario,
			Customer:      n,
			Agent:         agent,
			AgentGroup:    agentGroup,
			Status:        "PENDING",
			CustomerState: "等待呼入",
			AgentState:    "",
		}
		if agent != "" {
			c.AgentState = "等待通知"
		} else if agentGroup != "" {
			c.AgentState = "转接组 " + agentGroup
		}
		if s := k.sessionForCallee(start, n); s != nil {
			c.Status = "OBSERVED"
			c.CustomerState = "已接入"
			c.TraceID = s.ID
			c.Detail = "客户腿已进入 mock"
			c.Phases = append(c.Phases, CallPhase{Name: "客户腿进 mock", Status: "ok", Detail: "trace=" + s.ID})
		} else {
			c.Phases = append(c.Phases, CallPhase{Name: "客户腿进 mock", Status: "pending", Detail: "尚未观测到该客户号"})
		}
		out = append(out, c)
	}
	return out
}

type legInviteEvidence struct {
	Seen     bool
	Answered bool
	Failed   bool
	Session  string
	Detail   string
}

// waitLegInviteOK 只把 INVITE 的 200 OK 当成接通证据；INVITE/180/CANCEL/487 都不能算接通。
func (k *Kit) waitLegInviteOK(start time.Time, number string, timeout time.Duration) legInviteEvidence {
	deadline := time.Now().Add(timeout)
	best := legInviteEvidence{Detail: "未观测到 " + number + " 的 SIP INVITE"}
	for time.Now().Before(deadline) {
		for _, s := range k.bus.Sessions() {
			if s.StartedAt.Before(start.Add(-time.Second)) {
				continue
			}
			ev := legInviteEvidenceFromSession(s, number)
			if ev.Answered {
				return ev
			}
			if ev.Seen {
				best = ev
			}
			if ev.Failed {
				return ev
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	if best.Seen && !best.Answered && best.Detail == "" {
		best.Detail = number + " 只观测到 INVITE/振铃，未观测到 INVITE 200 OK"
	}
	return best
}

func legInviteEvidenceFromSession(s *tracelog.Session, number string) legInviteEvidence {
	out := legInviteEvidence{Session: s.ID}
	for _, e := range s.Events {
		if e.Channel != tracelog.ChanSIP || !eventMatchesLeg(e, number) {
			continue
		}
		switch {
		case e.Method == "INVITE" && e.Dir == tracelog.DirIn:
			out.Seen = true
			out.Detail = number + " 已收到 INVITE，等待 INVITE 200 OK"
		case e.Method == "180" || e.Method == "183":
			out.Seen = true
			out.Detail = number + " 仅振铃(" + e.Method + ")，未接通"
		case e.Method == "200" && isSIPInviteResponse(e.Headers):
			out.Seen = true
			out.Answered = true
			out.Detail = number + " 已返回 INVITE 200 OK"
			return out
		case e.Method == "CANCEL":
			out.Seen = true
			out.Failed = true
			out.Detail = number + " 被 CANCEL，未接通"
		case e.Method == "BYE" && headerContains(e.Headers, "Reason", "INCOMING_CALL_BARRED"):
			out.Seen = true
			out.Failed = true
			out.Detail = number + " 被挂断：INCOMING_CALL_BARRED"
		default:
			if code, ok := sipMethodStatusCode(e.Method); ok && code >= 400 {
				out.Seen = true
				out.Failed = true
				out.Detail = number + " INVITE 失败：" + e.Method
			}
		}
	}
	return out
}

func eventMatchesLeg(e tracelog.Event, number string) bool {
	if number == "" {
		return false
	}
	if e.Leg == number || e.Leg == "agent:"+number {
		return true
	}
	return headerContains(e.Headers, "To", "sip:"+number+"@")
}

func isSIPInviteResponse(headers []tracelog.HeaderKV) bool {
	return headerContains(headers, "CSeq", "INVITE")
}

func headerContains(headers []tracelog.HeaderKV, name, want string) bool {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) && strings.Contains(strings.ToUpper(h.Value), strings.ToUpper(want)) {
			return true
		}
	}
	return false
}

func sipMethodStatusCode(method string) (int, bool) {
	if len(method) != 3 {
		return 0, false
	}
	if method[0] < '0' || method[0] > '9' || method[1] < '0' || method[1] > '9' || method[2] < '0' || method[2] > '9' {
		return 0, false
	}
	return int(method[0]-'0')*100 + int(method[1]-'0')*10 + int(method[2]-'0'), true
}

func (k *Kit) sessionForCallee(start time.Time, callee string) *tracelog.Session {
	for _, s := range k.bus.Sessions() {
		if s.StartedAt.After(start.Add(-time.Second)) && sessionMatchesCallee(s, callee) {
			return s
		}
	}
	return nil
}

// isRejectCode 判断 SIP 方法字段是否为 4xx/5xx/6xx 拒接码。
func isRejectCode(method string) bool {
	if len(method) != 3 {
		return false
	}
	switch method[0] {
	case '4', '5', '6':
		return method[1] >= '0' && method[1] <= '9' && method[2] >= '0' && method[2] <= '9'
	}
	return false
}

// orStr 返回首个非空字符串。
func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// clip 截断字符串到 n 字节（响应体记入链路时防过长）。
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ScenarioResult 一次场景编排的汇总。
type ScenarioResult struct {
	ID        string          `json:"id"`
	GroupCode string          `json:"groupCode"`
	Total     int             `json:"total"`
	Passed    int             `json:"passed"`
	Failed    int             `json:"failed"`
	StartedAt string          `json:"startedAt"`
	DurMs     int64           `json:"durationMs"`
	Metrics   ScenarioMetrics `json:"metrics"`
	Runs      []Run           `json:"runs"`
	Calls     []CallView      `json:"calls,omitempty"`
}

// ScenarioMetrics 批量/压测聚合指标（每通耗时分布 + 通过率）。
type ScenarioMetrics struct {
	PassRate   int   `json:"passRate"`   // 通过率%
	AvgDurMs   int64 `json:"avgDurMs"`   // 平均每通耗时
	MinDurMs   int64 `json:"minDurMs"`   // 最快
	MaxDurMs   int64 `json:"maxDurMs"`   // 最慢
	P90DurMs   int64 `json:"p90DurMs"`   // P90 耗时
	Concurrent bool  `json:"concurrent"` // 是否并发触发
}

// computeMetrics 由逐通结果聚合压测指标（纯函数，便于单测）。
func computeMetrics(runs []Run, concurrent bool) ScenarioMetrics {
	m := ScenarioMetrics{Concurrent: concurrent}
	if len(runs) == 0 {
		return m
	}
	durs := make([]int64, 0, len(runs))
	var sum int64
	passed := 0
	for _, r := range runs {
		durs = append(durs, r.DurationMs)
		sum += r.DurationMs
		if r.OK {
			passed++
		}
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	m.PassRate = passed * 100 / len(runs)
	m.AvgDurMs = sum / int64(len(durs))
	m.MinDurMs = durs[0]
	m.MaxDurMs = durs[len(durs)-1]
	// P90：第 ceil(0.9*n) 个（1-based）→ 索引 min(n-1, ...)
	idx := (len(durs)*90 + 99) / 100 // ceil(0.9n)
	if idx > 0 {
		idx--
	}
	if idx >= len(durs) {
		idx = len(durs) - 1
	}
	m.P90DurMs = durs[idx]
	return m
}

// groupNumbers 从集群客户组按游标错开取号（无集群/未命中返回空）。
// 用 Store.TakeNumbers 让多次/并发测试不撞同一批号码（资源争夺修复）。
func (k *Kit) groupNumbers(groupCode string, limit int) []string {
	if k.clu == nil || groupCode == "" {
		return nil
	}
	return k.clu.TakeNumbers(groupCode, limit)
}

// sessionMatchesCallee 判断会话是否与某被叫号相关（呼入标题含该号，或事件 leg/Detail 命中）。
// test 会话标题通常也包含号码（例如“测试 line-call → 号码”），不能作为真实话路证据。
func sessionMatchesCallee(s *tracelog.Session, callee string) bool {
	if callee == "" {
		return false
	}
	if s.Kind != "test" && strings.Contains(s.Title, callee) {
		return true
	}
	for _, e := range s.Events {
		if e.Detail != nil && (e.Detail["callee"] == callee || e.Detail["leg"] == callee) {
			return true
		}
		if e.Leg == callee || e.Leg == "agent:"+callee {
			return true
		}
	}
	return false
}
