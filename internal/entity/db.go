// Package entity 集中定义持久化对象（PO）与共享 DTO：
// db.go 放 GORM 实体（含 TableName 与领域方法），dto.go 放过滤器/分页等传输结构。
// 实体不依赖任何 internal 包，是依赖图的叶子。
package entity

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---- 客户集群配置（mock 的核心抽象：可批量编排的虚拟客户）----

// BehaviorProfile 可复用的应答行为档（对应 mock_behavior_profile）。
type BehaviorProfile struct {
	ID           int64  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	Code         string `json:"code" gorm:"column:code"`
	Name         string `json:"name" gorm:"column:name"`
	Outcome      string `json:"outcome" gorm:"column:outcome"` // ANSWER/REJECT/BUSY/NO_ANSWER/UNAVAILABLE/BRIDGE
	RingMs       int    `json:"ringMs" gorm:"column:ring_ms"`
	TalkMs       int    `json:"talkMs" gorm:"column:talk_ms"`
	HangupCode   int    `json:"hangupCode" gorm:"column:hangup_code"`
	Playback     string `json:"playback" gorm:"column:playback"`
	DTMF         string `json:"dtmf" gorm:"column:dtmf"`
	ExpectDTMF   bool   `json:"expectDtmf" gorm:"column:expect_dtmf"`
	Fault        string `json:"fault" gorm:"column:fault"`
	BridgeTarget string `json:"bridgeTarget" gorm:"column:bridge_target"`
	IVRJson      string `json:"ivrJson" gorm:"column:ivr_json"`         // IVR 脚本（JSON 数组，[]behavior.IVRStep）；空=不用 IVR
	AnswerRatio  int    `json:"answerRatio" gorm:"column:answer_ratio"` // 接通率%（批量随机）
	Remark       string `json:"remark" gorm:"column:remark"`
}

func (BehaviorProfile) TableName() string { return "mock_behavior_profile" }

// CustomerGroup 号段批量客户组（对应 mock_customer_group）。
type CustomerGroup struct {
	ID           int64  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	Code         string `json:"code" gorm:"column:code"`
	Name         string `json:"name" gorm:"column:name"`
	NumberPrefix string `json:"numberPrefix" gorm:"column:number_prefix"`
	NumberStart  int64  `json:"numberStart" gorm:"column:number_start"`
	Count        int    `json:"count" gorm:"column:count"`
	BehaviorCode string `json:"behaviorCode" gorm:"column:behavior_code"`
	State        string `json:"state" gorm:"column:state"` // ENABLED/DISABLED
	Remark       string `json:"remark" gorm:"column:remark"`
}

func (CustomerGroup) TableName() string { return "mock_customer_group" }

// Contains 判断某号码是否落在本组号段内。
func (g *CustomerGroup) Contains(number string) bool {
	if g.NumberPrefix != "" && !strings.HasPrefix(number, g.NumberPrefix) {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(number, g.NumberPrefix), 10, 64)
	if err != nil {
		// 前缀外无数字部分：仅当起始也匹配整号时算命中
		full, e2 := strconv.ParseInt(number, 10, 64)
		if e2 != nil {
			return false
		}
		return full >= g.NumberStart && full < g.NumberStart+int64(g.Count)
	}
	return n >= g.NumberStart && n < g.NumberStart+int64(g.Count)
}

// Numbers 展开号段为具体号码列表（上限保护）。
func (g *CustomerGroup) Numbers(limit int) []string {
	if limit <= 0 || limit > g.Count {
		limit = g.Count
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, g.NumberPrefix+strconv.FormatInt(g.NumberStart+int64(i), 10))
	}
	return out
}

// NumbersFrom 从号段内 offset 处环绕取 limit 个号（用于多次测试错开取号、避免撞号）。
func (g *CustomerGroup) NumbersFrom(offset, limit int) []string {
	if g.Count <= 0 {
		return nil
	}
	if limit <= 0 || limit > g.Count {
		limit = g.Count
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (offset + i) % g.Count
		out = append(out, g.NumberPrefix+strconv.FormatInt(g.NumberStart+int64(idx), 10))
	}
	return out
}

// CustomerOverride 组内个例覆盖（对应 mock_customer_override）。
type CustomerOverride struct {
	ID           int64  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	GroupCode    string `json:"groupCode" gorm:"column:group_code"`
	Number       string `json:"number" gorm:"column:number"`
	BehaviorCode string `json:"behaviorCode" gorm:"column:behavior_code"`
	State        string `json:"state" gorm:"column:state"`
	Remark       string `json:"remark" gorm:"column:remark"`
}

func (CustomerOverride) TableName() string { return "mock_customer_override" }

// LineBinding 客户组↔线路绑定（对应 mock_line_binding）。
type LineBinding struct {
	ID          int64  `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	LineCode    string `json:"lineCode" gorm:"column:line_code"`
	LineAddress string `json:"lineAddress" gorm:"column:line_address"`
	LineName    string `json:"lineName" gorm:"column:line_name"` // 线路名（FS 经 X-Line-Name 注入，规范化后匹配）
	GroupCode   string `json:"groupCode" gorm:"column:group_code"`
	Enabled     int    `json:"enabled" gorm:"column:enabled"`
	Remark      string `json:"remark" gorm:"column:remark"`
}

func (LineBinding) TableName() string { return "mock_line_binding" }

// ---- 通话记录（mock 事实表）----

const (
	CallRecordStatusPending  = "PENDING"
	CallRecordStatusRinging  = "RINGING"
	CallRecordStatusAnswered = "ANSWERED"
	CallRecordStatusEnded    = "ENDED"
	CallRecordStatusRejected = "REJECTED"
	CallRecordStatusFailed   = "FAILED"
)

// CallRecord 一通电话记录（对应 mock_call_record）。
// 任务发起时先写预期记录，真实 SIP/媒体/WS 事件到达后再补齐状态和链路字段。
type CallRecord struct {
	ID              int64      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	RecordID        string     `json:"recordId" gorm:"column:record_id;size:96;uniqueIndex:uk_record"`
	Scenario        string     `json:"scenario" gorm:"column:scenario;size:32;index:idx_scenario_time"`
	Source          string     `json:"source" gorm:"column:source;size:24"`
	RunID           string     `json:"runId" gorm:"column:run_id;size:64;index:idx_run"`
	OrgCode         string     `json:"orgCode" gorm:"column:org_code;size:64;index:idx_org_time"`
	TaskName        string     `json:"taskName" gorm:"column:task_name;size:128;index:idx_task"`
	TaskCode        string     `json:"taskCode" gorm:"column:task_code;size:64;index:idx_task"`
	CustomerGroup   string     `json:"customerGroup" gorm:"column:customer_group;size:64;index:idx_customer"`
	CustomerNumber  string     `json:"customerNumber" gorm:"column:customer_number;size:64;index:idx_customer"`
	AgentGroupCode  string     `json:"agentGroupCode" gorm:"column:agent_group_code;size:64;index:idx_agent"`
	AgentNumber     string     `json:"agentNumber" gorm:"column:agent_number;size:64;index:idx_agent"`
	LineCode        string     `json:"lineCode" gorm:"column:line_code;size:64;index:idx_line"`
	LineAddress     string     `json:"lineAddress" gorm:"column:line_address;size:128;index:idx_line"`
	LineName        string     `json:"lineName" gorm:"column:line_name;size:128"`
	Direction       string     `json:"direction" gorm:"column:direction;size:32"`
	CallType        string     `json:"callType" gorm:"column:call_type;size:32"`
	Status          string     `json:"status" gorm:"column:status;size:24;index:idx_status_time"`
	Result          string     `json:"result" gorm:"column:result;size:64"`
	HangupCode      int        `json:"hangupCode" gorm:"column:hangup_code"`
	TraceID         string     `json:"traceId" gorm:"column:trace_id;size:64;index:idx_trace"`
	CallUUID        string     `json:"callUuid" gorm:"column:call_uuid;size:96;index:idx_call_uuid"`
	SIPCallID       string     `json:"sipCallId" gorm:"column:sip_call_id;size:128;index:idx_sip_call"`
	StartedAt       time.Time  `json:"startedAt" gorm:"column:started_at;index:idx_scenario_time;index:idx_status_time;index:idx_org_time"`
	AnsweredAt      *time.Time `json:"answeredAt,omitempty" gorm:"column:answered_at"`
	EndedAt         *time.Time `json:"endedAt,omitempty" gorm:"column:ended_at"`
	DurationMs      int64      `json:"durationMs" gorm:"column:duration_ms"`
	LastEventAt     time.Time  `json:"lastEventAt" gorm:"column:last_event_at"`
	StepsJSON       string     `json:"stepsJson" gorm:"column:steps_json;type:json"`
	DetailJSON      string     `json:"detailJson" gorm:"column:detail_json;type:json"`
	LastSummary     string     `json:"lastSummary" gorm:"column:last_summary;size:512"`
	SignalSummary   string     `json:"signalSummary" gorm:"column:signal_summary;size:512"`
	MediaSummary    string     `json:"mediaSummary" gorm:"column:media_summary;size:512"`
	CallbackSummary string     `json:"callbackSummary" gorm:"column:callback_summary;size:512"`
}

func (CallRecord) TableName() string { return "mock_call_record" }

// ---- 测试运行历史 ----

// TestRun 对应 mock_test_run。
type TestRun struct {
	ID            int64     `gorm:"column:id;primaryKey;autoIncrement"`
	RunID         string    `gorm:"column:run_id"`
	CaseCode      string    `gorm:"column:case_code"`
	CaseKind      string    `gorm:"column:case_kind"`
	OK            int       `gorm:"column:ok"`
	DurationMs    int       `gorm:"column:duration_ms"`
	TraceID       string    `gorm:"column:trace_id"`
	StepsJSON     string    `gorm:"column:steps_json"`
	ArtifactsJSON string    `gorm:"column:artifacts_json"`
	StartedAt     time.Time `gorm:"column:started_at"`
}

func (TestRun) TableName() string { return "mock_test_run" }

// ---- 通话链路（SIP/媒体/WS 时间线落库）----

// TraceSession 对应 mock_trace_session。Events 为关联事件，不入本表（查询时装配）。
type TraceSession struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	SessionID string    `gorm:"column:session_id"`
	CallUUID  string    `gorm:"column:call_uuid"`
	Kind      string    `gorm:"column:kind"`
	Title     string    `gorm:"column:title"`
	Legs      string    `gorm:"column:legs"`
	StartedAt time.Time `gorm:"column:started_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`

	Events []TraceEvent `gorm:"-" json:"events,omitempty"`
}

func (TraceSession) TableName() string { return "mock_trace_session" }

// TraceEvent 对应 mock_trace_event。
type TraceEvent struct {
	ID          int64     `gorm:"column:id;primaryKey;autoIncrement"`
	SessionID   string    `gorm:"column:session_id"`
	Seq         int64     `gorm:"column:seq"`
	TS          time.Time `gorm:"column:ts"`
	Leg         string    `gorm:"column:leg"`
	Channel     string    `gorm:"column:channel"`
	Dir         string    `gorm:"column:dir"`
	Method      string    `gorm:"column:method"`
	Summary     string    `gorm:"column:summary"`
	HeadersJSON string    `gorm:"column:headers_json"`
	RawMessage  string    `gorm:"column:raw_message"`
}

func (TraceEvent) TableName() string { return "mock_trace_event" }

// ---- Hermes 回调（webhook 落库）----

// Callback 收到的一条 Hermes 回调（webhook），对应 mock_callback。
type Callback struct {
	ID          int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	Seq         int64     `json:"seq" gorm:"column:seq"`
	TS          time.Time `json:"ts" gorm:"column:ts"`
	Source      string    `json:"source" gorm:"column:source;size:32;index:idx_cb_source_event"`
	Event       string    `json:"event" gorm:"column:event;size:64;index:idx_cb_source_event"`
	OrgCode     string    `json:"orgCode" gorm:"column:org_code;size:64"`
	CallUUID    string    `json:"callUuid" gorm:"column:call_uuid;size:96;index:idx_cb_call_uuid"`
	Remote      string    `json:"remote" gorm:"column:remote;size:64"`
	PayloadJSON string    `json:"payloadJson" gorm:"column:payload_json;type:mediumtext"`
}

func (Callback) TableName() string { return "mock_callback" }

// ---- 机构 OpenAPI 接入配置 ----

// OrgConfig 一个机构的接入配置（对应 mock_org_config）。
type OrgConfig struct {
	ID                    int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	OrgCode               string    `json:"orgCode" gorm:"column:org_code"`
	OrgName               string    `json:"orgName" gorm:"column:org_name"`
	Mode                  string    `json:"mode" gorm:"column:mode"` // gateway / direct
	GatewayURL            string    `json:"gatewayUrl" gorm:"column:gateway_url"`
	APIKey                string    `json:"apiKey" gorm:"column:api_key"`
	BasicURL              string    `json:"basicUrl" gorm:"column:basic_url"`
	CallCenterURL         string    `json:"callCenterUrl" gorm:"column:call_center_url"`
	CallBotURL            string    `json:"callBotUrl" gorm:"column:call_bot_url"`
	OTPURL                string    `json:"otpUrl" gorm:"column:otp_url;not null;default:''"`
	AgentWsURL            string    `json:"agentWsUrl" gorm:"column:agent_ws_url;not null;default:''"`
	UserCode              string    `json:"userCode" gorm:"column:user_code"`
	DefaultAgentGroupCode string    `json:"defaultAgentGroupCode" gorm:"column:default_agent_group_code;not null;default:''"`
	DefaultAgentRoleCode  string    `json:"defaultAgentRoleCode" gorm:"column:default_agent_role_code;not null;default:''"`
	DefaultDepCode        string    `json:"defaultDepCode" gorm:"column:default_dep_code;not null;default:''"`
	DefaultAgentPassword  string    `json:"defaultAgentPassword" gorm:"column:default_agent_password;not null;default:''"`
	Remark                string    `json:"remark" gorm:"column:remark"`
	GmtModified           time.Time `json:"-" gorm:"column:gmt_modified"`
}

func (OrgConfig) TableName() string { return "mock_org_config" }

// AgentWSHost 返回该机构的 hermes-ws 工作台地址（host:port）。
// 显式 agentWsUrl 优先；未配时按本地约定从 direct 服务地址推导：
//   - http://127.0.0.1:8091 -> 127.0.0.1:18081
//   - http://hermes-call-center:8080 -> hermes-ws:8081
func (o OrgConfig) AgentWSHost() string {
	if h := strings.TrimSpace(o.AgentWsURL); h != "" {
		return h
	}
	if h := InferAgentWSHost(o.CallCenterURL); h != "" {
		return h
	}
	return InferAgentWSHost(o.BasicURL)
}

// InferAgentWSHost 从本地 Hermes 服务地址推导 hermes-ws 工作台地址。
func InferAgentWSHost(serviceURL string) string {
	serviceURL = strings.TrimSpace(serviceURL)
	if serviceURL == "" {
		return ""
	}
	raw := serviceURL
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		return ""
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return host + ":18081"
	}
	switch {
	case strings.Contains(host, "call-center"):
		host = strings.Replace(host, "call-center", "ws", 1)
	case strings.Contains(host, "basic"):
		host = strings.Replace(host, "basic", "ws", 1)
	case strings.HasPrefix(host, "hermes-"):
		host = "hermes-ws"
	default:
		host = "hermes-ws"
	}
	return host + ":8081"
}
