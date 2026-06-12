// Package tracelog 是 hermes-mock 的「通话链路事件总线」：把一通通话涉及的
// SIP 信令、ESL 事件、WebSocket 消息、双腿桥接动作统一记成带时间线的事件流，
// 按会话(session)聚合，供前端做「通话链路可观测」。
//
// 这是观测的核心：不是看服务健康，而是看一通通话「发生了什么」。
package tracelog

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Channel 事件来自哪条观测通道。
type Channel string

const (
	ChanSIP    Channel = "SIP"    // SIP 信令（INVITE/180/200/ACK/BYE…）
	ChanESL    Channel = "ESL"    // FreeSWITCH ESL 事件（CHANNEL_*）
	ChanWS     Channel = "WS"     // WebSocket 坐席消息
	ChanBridge Channel = "BRIDGE" // 双腿桥接 / 媒体
	ChanFlow   Channel = "FLOW"   // 编排流程（mock 发起/断言等）
)

// Dir 事件方向（相对 mock）。
type Dir string

const (
	DirIn  Dir = "IN"  // 收到
	DirOut Dir = "OUT" // 发出
	DirNA  Dir = "-"   // 不适用
)

// Event 一条链路事件。
type Event struct {
	Seq     int64             `json:"seq"`
	TS      time.Time         `json:"ts"`
	Session string            `json:"session"` // 会话 ID（一通通话/一次测试）
	Leg     string            `json:"leg"`     // mock 侧腿标识：客户号 / agent:分机 / ""（角色名放 Detail）
	Channel Channel           `json:"channel"`
	Dir     Dir               `json:"dir"`
	Method  string            `json:"method"`  // INVITE / 200 / CHANNEL_ANSWER / agent-login …
	Summary string            `json:"summary"` // 人类可读摘要
	Detail  map[string]string `json:"detail,omitempty"`
	// ---- 真实 SIP 报文观测（区别于手写摘要）----
	Headers []HeaderKV `json:"headers,omitempty"` // 结构化 SIP 头（含 X- 业务头）
	Raw     string     `json:"raw,omitempty"`     // 原始 SIP 报文 req.String()
	CallID  string     `json:"callId,omitempty"`  // SIP Call-ID
	// ---- 端点（时序梯形图用：消息从 Src 流向 Dst）----
	Src string `json:"src,omitempty"` // 源 IP:port
	Dst string `json:"dst,omitempty"` // 目的 IP:port
}

// HeaderKV 一条 SIP 头（保留顺序与重复头）。
type HeaderKV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// 业务 callUuid 头名（FreeSWITCH 经 sip_h_X- 注入）。提取优先级按 Hermes 真实链路证据排定：
//   - `x-session-id`（值形如 CCMDL{uuid}）是**所有被叫场景的通用 callUuid 载体**——hermes-common
//     `EslEventConstant.SID = "variable_sip_h_x-session-id"`，群呼/callbot/otp/坐席外呼共用；
//     坐席 jssip 外呼也注 `x-session-id:CCMDL{uuid}`，坐席侧 saveCallRecord 写的 `CCMDL+callId` 与之对齐。
//     证据：docs/hermes/{agent-outbound-call,otp}.md。
//   - `x-call-uuid`/`x-callid`/`x-call-id`：真 callUuid 头（若链路确实带），与 session-id 同权优先。
//   - `x-jcallid`(=X-JCallId)：**坐席外呼里传的是 businessId，不是 callUuid**（agent-outbound-call.md:80），
//     故降为**最末兜底**，仅在前两族都取不到时才用——避免错把 businessId 当 callUuid 致两腿关联不上。
//
// 同一通话的被叫腿与坐席腿用同一 callUuid 关联，多条报文也据此聚合，必须稳定取同一个值。
var (
	bizUUIDPrimary  = []string{"x-call-uuid", "x-callid", "x-call-id", "x-session-id", "x-session_id"}
	bizUUIDFallback = []string{"x-jcallid"}
)

// BizUUIDFromHeaders 按优先级从 SIP 头提取 Hermes 业务 callUuid（会话聚合键）：
// 先取真 callUuid / session-id 族任一非空值，都没有才退到 x-jcallid(businessId)；大小写不敏感。
//
// siptrace（传输层抓真包）与 sipagent（业务 handler）**共用本函数**，保证「同一 INVITE →
// 同一聚合键」——否则同一通话的 SIP 报文事件与 mock 决策事件会分裂成两个会话。
func BizUUIDFromHeaders(headers []HeaderKV) string {
	if v := firstHeaderValue(headers, bizUUIDPrimary); v != "" {
		return v
	}
	return firstHeaderValue(headers, bizUUIDFallback)
}

// firstHeaderValue 返回 headers 中首个名在 names（小写）内且值非空的头值。
func firstHeaderValue(headers []HeaderKV, names []string) string {
	for _, h := range headers {
		ln := strings.ToLower(h.Name)
		for _, n := range names {
			if ln == n && h.Value != "" {
				return h.Value
			}
		}
	}
	return ""
}

// Session 一通通话/一次测试的完整轨迹。
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`  // 如 "外呼 8613... → 坐席 5002"
	Kind      string    `json:"kind"`   // outbound / inbound / agent-reg / test
	CallID    string    `json:"callId"` // SIP Call-ID（从首条 SIP 事件回填，多腿聚合用）
	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Legs      []string  `json:"legs"` // 涉及的腿
	Events    []Event   `json:"events"`
}

// Bus 事件总线：会话聚合 + 全局事件流（环形）。
type Bus struct {
	mu       sync.RWMutex
	seq      int64
	sessions map[string]*Session
	order    []string // 会话顺序（新建在后）
	maxSess  int
	now      func() time.Time
}

func New() *Bus {
	return &Bus{sessions: map[string]*Session{}, maxSess: 200, now: time.Now}
}

// OpenSession 新建一个会话，返回 ID。
func (b *Bus) OpenSession(kind, title string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.openLocked(kind, title, "")
}

// openLocked 在已持锁下新建会话。
func (b *Bus) openLocked(kind, title, callID string) string {
	id := uuid.NewString()[:8]
	now := b.now()
	b.sessions[id] = &Session{ID: id, Title: title, Kind: kind, CallID: callID, StartedAt: now, UpdatedAt: now}
	b.order = append(b.order, id)
	if len(b.order) > b.maxSess {
		old := b.order[0]
		b.order = b.order[1:]
		delete(b.sessions, old)
	}
	return id
}

// Emit 向某会话追加一条事件（会话不存在则忽略）。
func (b *Bus) Emit(session string, leg string, ch Channel, dir Dir, method, summary string, detail map[string]string) {
	b.emit(Event{Session: session, Leg: leg, Channel: ch, Dir: dir, Method: method, Summary: summary, Detail: detail})
}

// EmitSIP 追加一条**真实 SIP 报文**事件（带结构化头 + 原始报文 + Call-ID + 端点）。
func (b *Bus) EmitSIP(session, leg string, dir Dir, method, summary string, headers []HeaderKV, raw, callID, src, dst string) {
	b.emit(Event{
		Session: session, Leg: leg, Channel: ChanSIP, Dir: dir, Method: method,
		Summary: summary, Headers: headers, Raw: raw, CallID: callID, Src: src, Dst: dst,
	})
}

func (b *Bus) emit(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[e.Session]
	if s == nil {
		return
	}
	b.seq++
	now := b.now()
	e.Seq = b.seq
	e.TS = now
	s.Events = append(s.Events, e)
	s.UpdatedAt = now
	if e.Leg != "" && !contains(s.Legs, e.Leg) {
		s.Legs = append(s.Legs, e.Leg)
	}
	// 从 SIP Call-ID 回填会话级 callId（多腿聚合用）
	if e.CallID != "" && s.CallID == "" {
		s.CallID = e.CallID
	}
}

// Sessions 返回最近会话（新→旧）的**读快照**（深拷贝，脱锁后可安全 Marshal）。
func (b *Bus) Sessions() []*Session {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*Session, 0, len(b.order))
	for i := len(b.order) - 1; i >= 0; i-- {
		out = append(out, cloneSession(b.sessions[b.order[i]]))
	}
	return out
}

// EnsureByCallID 按 SIP Call-ID 找到已有会话，没有则用给定 kind/title 新建。
// 供传输层 SIP tracer 把同一 Call-ID 的所有报文（INVITE/180/200/ACK/BYE）聚到一个会话。
func (b *Bus) EnsureByCallID(callID, kind, title string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if callID != "" {
		for _, id := range b.order {
			if s := b.sessions[id]; s != nil && s.CallID == callID {
				return id
			}
		}
	}
	return b.openLocked(kind, title, callID)
}

// SessionByCallID 返回某 Call-ID 对应会话 id（无则空）。
func (b *Bus) SessionByCallID(callID string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, id := range b.order {
		if s := b.sessions[id]; s != nil && s.CallID == callID {
			return id
		}
	}
	return ""
}

// Session 取单个会话的**读快照**（含全部事件，深拷贝）。无则返回 nil。
func (b *Bus) Session(id string) *Session {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return cloneSession(b.sessions[id])
}

// cloneSession 返回 Session 的读快照：复制结构体并一级复制 Events/Legs 切片。
//
// 不变量：Event 一旦经 emit 追加即不可变（emit 只 append 新 Event，从不回改旧值），
// 因此 Event.Detail/Headers 共享引用对并发读是安全的；只需复制会被 append 改写的
// Events/Legs 切片头与底层数组，读取侧脱锁后即可安全遍历/Marshal，不与 emit 竞争。
func cloneSession(s *Session) *Session {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Events = append([]Event(nil), s.Events...)
	cp.Legs = append([]string(nil), s.Legs...)
	return &cp
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
