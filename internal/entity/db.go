// Package entity 集中定义持久化对象（PO）与共享 DTO：
// db.go 放 GORM 实体（含 TableName 与领域方法），dto.go 放过滤器/分页等传输结构。
// 实体不依赖任何 internal 包，是依赖图的叶子。
//
// Schema 单源约定：**本文件的 gorm tag 是表结构的唯一权威**（启动 AutoMigrate 据此建表/补列）。
// deploy/ddl/hermes_mock.sql 是据此生成的只读快照，改 schema 先改这里再同步 DDL。
// 索引名约束：sqlite 的索引名是**全库全局唯一**（非表级），故每个 uniqueIndex/index 名必须全库不重复
// （如 uk_behavior_code / uk_group_code，不能都叫 uk_code），否则 AutoMigrate 在 sqlite 下报「index already exists」。
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
	ID           int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	Code         string    `json:"code" gorm:"column:code;size:64;uniqueIndex:uk_behavior_code"`
	Name         string    `json:"name" gorm:"column:name;size:128"`
	Outcome      string    `json:"outcome" gorm:"column:outcome;size:16"` // ANSWER/REJECT/BUSY/NO_ANSWER/UNAVAILABLE/BRIDGE
	RingMs       int       `json:"ringMs" gorm:"column:ring_ms"`
	TalkMs       int       `json:"talkMs" gorm:"column:talk_ms"`
	HangupCode   int       `json:"hangupCode" gorm:"column:hangup_code"`
	Playback     string    `json:"playback" gorm:"column:playback;size:128"`
	DTMF         string    `json:"dtmf" gorm:"column:dtmf;size:64"`
	ExpectDTMF   bool      `json:"expectDtmf" gorm:"column:expect_dtmf"`
	Fault        string    `json:"fault" gorm:"column:fault;size:24"`
	BridgeTarget string    `json:"bridgeTarget" gorm:"column:bridge_target;size:128"`
	IVRJson      string    `json:"ivrJson" gorm:"column:ivr_json;type:text"` // IVR 脚本（JSON 数组，[]behavior.IVRStep）；空=不用 IVR
	AnswerRatio  int       `json:"answerRatio" gorm:"column:answer_ratio"`   // 接通率%（批量随机）
	Remark       string    `json:"remark" gorm:"column:remark;size:255"`
	GmtCreate    time.Time `json:"-" gorm:"column:gmt_create;autoCreateTime"`
	GmtModified  time.Time `json:"-" gorm:"column:gmt_modified;autoUpdateTime"`
}

func (BehaviorProfile) TableName() string { return "mock_behavior_profile" }

// CustomerGroup 号段批量客户组（对应 mock_customer_group）。
type CustomerGroup struct {
	ID           int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	Code         string    `json:"code" gorm:"column:code;size:64;uniqueIndex:uk_group_code"`
	Name         string    `json:"name" gorm:"column:name;size:128"`
	NumberPrefix string    `json:"numberPrefix" gorm:"column:number_prefix;size:32"`
	NumberStart  int64     `json:"numberStart" gorm:"column:number_start"`
	Count        int       `json:"count" gorm:"column:count"`
	BehaviorCode string    `json:"behaviorCode" gorm:"column:behavior_code;size:64"`
	State        string    `json:"state" gorm:"column:state;size:16"` // ENABLED/DISABLED
	Remark       string    `json:"remark" gorm:"column:remark;size:255"`
	GmtCreate    time.Time `json:"-" gorm:"column:gmt_create;autoCreateTime"`
	GmtModified  time.Time `json:"-" gorm:"column:gmt_modified;autoUpdateTime"`
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
// 复合唯一 (group_code, number)：同一号码可在号段重叠的不同组各有一条个例（按端口/组区分）。
type CustomerOverride struct {
	ID           int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	GroupCode    string    `json:"groupCode" gorm:"column:group_code;size:64;uniqueIndex:uk_group_number,priority:1"`
	Number       string    `json:"number" gorm:"column:number;size:32;uniqueIndex:uk_group_number,priority:2"`
	BehaviorCode string    `json:"behaviorCode" gorm:"column:behavior_code;size:64"`
	State        string    `json:"state" gorm:"column:state;size:16"`
	Remark       string    `json:"remark" gorm:"column:remark;size:255"`
	GmtCreate    time.Time `json:"-" gorm:"column:gmt_create;autoCreateTime"`
	GmtModified  time.Time `json:"-" gorm:"column:gmt_modified;autoUpdateTime"`
}

func (CustomerOverride) TableName() string { return "mock_customer_override" }

// LineBinding 客户组↔mock SIP 入口端口绑定（对应 mock_line_binding）。
type LineBinding struct {
	ID          int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	ListenPort  int       `json:"listenPort" gorm:"column:listen_port;uniqueIndex:uk_port"`
	LineCode    string    `json:"lineCode" gorm:"column:line_code;size:64"`  // 可选：保留 Hermes 线路 code 仅作标识/兼容
	LineName    string    `json:"lineName" gorm:"column:line_name;size:128"` // 可选：FS 经 X-Line-Name 注入，主要用于观测
	GroupCode   string    `json:"groupCode" gorm:"column:group_code;size:64"`
	Enabled     int       `json:"enabled" gorm:"column:enabled"`
	Remark      string    `json:"remark" gorm:"column:remark;size:255"`
	GmtCreate   time.Time `json:"-" gorm:"column:gmt_create;autoCreateTime"`
	GmtModified time.Time `json:"-" gorm:"column:gmt_modified;autoUpdateTime"`
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

// MockCall 一通电话记录（聚合根，对应 mock_call）。
// 一通电话 = 一行：发起侧（testkit/坐席外呼）先写预期，被叫腿（calltrace.Tracker，按 call_uuid 主键）补齐状态。
// 跨场景/跨腿关联一律用 call_uuid（被叫腿从真实 X-CALL-UUID 提取）。SIP 报文级观测在 mock_trace_leg/event，不在本表。
type MockCall struct {
	ID             int64      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	RecordID       string     `json:"recordId" gorm:"column:record_id;size:96;uniqueIndex:uk_record"`
	Scenario       string     `json:"scenario" gorm:"column:scenario;size:32;index:idx_call_scenario_time"`
	Source         string     `json:"source" gorm:"column:source;size:24"`
	RunID          string     `json:"runId" gorm:"column:run_id;size:64;index:idx_call_run"`
	OrgCode        string     `json:"orgCode" gorm:"column:org_code;size:64;index:idx_call_org_time"`
	TaskName       string     `json:"taskName" gorm:"column:task_name;size:128;index:idx_call_task"`
	TaskCode       string     `json:"taskCode" gorm:"column:task_code;size:64;index:idx_call_task"`
	CustomerGroup  string     `json:"customerGroup" gorm:"column:customer_group;size:64;index:idx_call_customer"`
	CustomerNumber string     `json:"customerNumber" gorm:"column:customer_number;size:64;index:idx_call_customer"`
	AgentGroupCode string     `json:"agentGroupCode" gorm:"column:agent_group_code;size:64;index:idx_call_agent"`
	AgentNumber    string     `json:"agentNumber" gorm:"column:agent_number;size:64;index:idx_call_agent"`
	LineCode       string     `json:"lineCode" gorm:"column:line_code;size:64;index:idx_call_line"`
	LineAddress    string     `json:"lineAddress" gorm:"column:line_address;size:128;index:idx_call_line"`
	LineName       string     `json:"lineName" gorm:"column:line_name;size:128"`
	Direction      string     `json:"direction" gorm:"column:direction;size:32"`
	CallType       string     `json:"callType" gorm:"column:call_type;size:32"`
	ExpectOutcome  string     `json:"expectOutcome" gorm:"column:expect_outcome;size:24"` // 发起侧期望行为（断言用）
	Status         string     `json:"status" gorm:"column:status;size:24;index:idx_call_status_time"`
	Result         string     `json:"result" gorm:"column:result;size:64"`
	HangupCode     int        `json:"hangupCode" gorm:"column:hangup_code"`
	TraceID        string     `json:"traceId" gorm:"column:trace_id;size:64;index:idx_call_trace"`
	CallUUID       string     `json:"callUuid" gorm:"column:call_uuid;size:96;index:idx_call_uuid"`
	StartedAt      time.Time  `json:"startedAt" gorm:"column:started_at;index:idx_call_scenario_time;index:idx_call_status_time;index:idx_call_org_time"`
	AnsweredAt     *time.Time `json:"answeredAt,omitempty" gorm:"column:answered_at"`
	EndedAt        *time.Time `json:"endedAt,omitempty" gorm:"column:ended_at"`
	DurationMs     int64      `json:"durationMs" gorm:"column:duration_ms"`
	LastEventAt    time.Time  `json:"lastEventAt" gorm:"column:last_event_at"`
	StepsJSON      string     `json:"stepsJson" gorm:"column:steps_json;type:json"`
	DetailJSON     string     `json:"detailJson" gorm:"column:detail_json;type:json"`
	LastSummary    string     `json:"lastSummary" gorm:"column:last_summary;size:512"`
}

func (MockCall) TableName() string { return "mock_call" }

// ---- 测试运行历史 ----

// TestRun 对应 mock_test_run。
type TestRun struct {
	ID            int64     `gorm:"column:id;primaryKey;autoIncrement"`
	RunID         string    `gorm:"column:run_id;size:32;index:idx_run"`
	CaseCode      string    `gorm:"column:case_code;size:64;index:idx_case_time"`
	CaseKind      string    `gorm:"column:case_kind;size:32"`
	OK            int       `gorm:"column:ok"`
	DurationMs    int       `gorm:"column:duration_ms"`
	TraceID       string    `gorm:"column:trace_id;size:32"`
	StepsJSON     string    `gorm:"column:steps_json;type:json"`
	ArtifactsJSON string    `gorm:"column:artifacts_json;type:json"`
	StartedAt     time.Time `gorm:"column:started_at;index:idx_case_time"`
}

func (TestRun) TableName() string { return "mock_test_run" }

// ---- 通话链路（SIP/媒体/WS 时间线落库，单腿）----

// TraceLeg 一条单腿 SIP 对话的链路（对应 mock_trace_leg）。
// 写入侧严格单腿（一条 SIP Call-ID 一行）；跨腿的「一通业务通话含多腿」由读时按 CallUUID 归并装配
// （api 层 traceSessionFromEntity + 聚合），写入侧不做跨腿聚合（守 SCOPE 非目标）。
// Events 为关联事件，不入本表（查询时装配）。
type TraceLeg struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	SessionID string    `gorm:"column:session_id;size:64;uniqueIndex:uk_leg_session"` // = 单腿聚合键（业务 callUuid 优先，否则 SIP Call-ID）
	CallUUID  string    `gorm:"column:call_uuid;size:96;index:idx_leg_call_uuid"`     // 关联锚：同一通业务通话的多腿共享
	LegRole   string    `gorm:"column:leg_role;size:16"`                              // customer / agent
	Line      string    `gorm:"column:line;size:128"`                                 // 线路名/标识（观测用）
	Kind      string    `gorm:"column:kind;size:24"`
	Title     string    `gorm:"column:title;size:255"`
	StartedAt time.Time `gorm:"column:started_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`

	Events []TraceEvent `gorm:"-" json:"events,omitempty"`
}

func (TraceLeg) TableName() string { return "mock_trace_leg" }

// TraceEvent 对应 mock_trace_event（挂在单腿 session_id 下）。
type TraceEvent struct {
	ID          int64     `gorm:"column:id;primaryKey;autoIncrement"`
	SessionID   string    `gorm:"column:session_id;size:64;index:idx_event_session_seq"`
	Seq         int64     `gorm:"column:seq;index:idx_event_session_seq"`
	TS          time.Time `gorm:"column:ts"`
	Leg         string    `gorm:"column:leg;size:64"`
	Channel     string    `gorm:"column:channel;size:8"`
	Dir         string    `gorm:"column:dir;size:4"`
	Method      string    `gorm:"column:method;size:32"`
	Summary     string    `gorm:"column:summary;size:512"`
	HeadersJSON string    `gorm:"column:headers_json;type:json"`
	RawMessage  string    `gorm:"column:raw_message;type:mediumtext"`
}

func (TraceEvent) TableName() string { return "mock_trace_event" }

// ---- Hermes 回调（webhook 落库）----

// Callback 收到的一条 Hermes 回调（webhook），对应 mock_callback。
type Callback struct {
	ID          int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	Seq         int64     `json:"seq" gorm:"column:seq"`
	TS          time.Time `json:"ts" gorm:"column:ts;index:idx_cb_ts"`
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
	OrgCode               string    `json:"orgCode" gorm:"column:org_code;size:64;uniqueIndex:uk_org"`
	OrgName               string    `json:"orgName" gorm:"column:org_name;size:128"`
	Mode                  string    `json:"mode" gorm:"column:mode;size:16"` // gateway / direct
	GatewayURL            string    `json:"gatewayUrl" gorm:"column:gateway_url;size:256"`
	APIKey                string    `json:"apiKey" gorm:"column:api_key;size:128"`
	BasicURL              string    `json:"basicUrl" gorm:"column:basic_url;size:256"`
	CallCenterURL         string    `json:"callCenterUrl" gorm:"column:call_center_url;size:256"`
	CallBotURL            string    `json:"callBotUrl" gorm:"column:call_bot_url;size:256"`
	OTPURL                string    `json:"otpUrl" gorm:"column:otp_url;size:256;not null;default:''"`
	AgentWsURL            string    `json:"agentWsUrl" gorm:"column:agent_ws_url;size:256;not null;default:''"`
	UserCode              string    `json:"userCode" gorm:"column:user_code;size:64"`
	DefaultAgentGroupCode string    `json:"defaultAgentGroupCode" gorm:"column:default_agent_group_code;size:64;not null;default:''"`
	DefaultAgentRoleCode  string    `json:"defaultAgentRoleCode" gorm:"column:default_agent_role_code;size:64;not null;default:''"`
	DefaultDepCode        string    `json:"defaultDepCode" gorm:"column:default_dep_code;size:64;not null;default:''"`
	DefaultAgentPassword  string    `json:"defaultAgentPassword" gorm:"column:default_agent_password;size:128;not null;default:''"`
	Remark                string    `json:"remark" gorm:"column:remark;size:255"`
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
