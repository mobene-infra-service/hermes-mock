// Package calltrace 记录 agent 处理的每通被叫通话（活跃 + 最近 + 统计），供监控页与断言查询。
// 持久化经 model.Repository 落 mock_call（持久、重启不丢）；repo=nil 仅单测（内存）。
package calltrace

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"

	"hermes-mock/internal/entity"
	"hermes-mock/internal/model"
)

type State string

const (
	StateRinging  State = "RINGING"
	StateAnswered State = "ANSWERED"
	StateEnded    State = "ENDED"
	StateRejected State = "REJECTED"
)

// Call 一通通话的观测记录（前端监控页模型）。
type Call struct {
	ID         string     `json:"id"`
	Callee     string     `json:"callee"`
	Caller     string     `json:"caller"`
	Outcome    string     `json:"outcome"`
	State      State      `json:"state"`
	HangupCode int        `json:"hangupCode,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	AnsweredAt *time.Time `json:"answeredAt,omitempty"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
}

// Tracker 通话跟踪：落 mock_call（scenario=sip-inbound）；repo=nil 仅单测（内存）。
type Tracker struct {
	repo model.Repository
	now  func() time.Time

	// 内存回退（单测）
	mu        sync.RWMutex
	active    map[string]*Call
	recent    []*Call
	maxRecent int
}

// New 创建 Tracker；运行时 repo 必为已初始化的 Repository；nil 仅单测（内存）。
func New(repo model.Repository) *Tracker {
	return &Tracker{repo: repo, now: time.Now, active: map[string]*Call{}, maxRecent: 200}
}

func (t *Tracker) persistent() bool { return t.repo != nil }

func (t *Tracker) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (t *Tracker) save(row entity.MockCall) {
	ctx, cancel := t.ctx()
	defer cancel()
	_ = t.repo.SaveCallRecord(ctx, &row)
}

// Start 登记一通新通话（被叫腿 RINGING），返回 record id。
// callUUID 为 Hermes 业务 callUuid（被叫腿 INVITE 的 X-CALL-UUID/x-session-id 提取，由 sipagent 传入）：
// 用作 record_id 主键，使本记录与同一通话的 trace 会话（同 call_uuid）天然关联，
// 也让 INVITE 重传/同一通话多次落库幂等合并到一行（根除「一通电话多条记录」）。空则回退随机 uuid。
// businessID 为被叫腿 INVITE 的 X-JBusinessId/x-business_id（发起业务的 businessId，可空）：存 task_code，
// 让前端把被叫腿精确关联到发起它的业务（坐席外呼/群呼任务）。
func (t *Tracker) Start(callUUID, businessID, callee, caller, outcome string) string {
	id := callUUID
	if id == "" {
		id = uuid.NewString()
	}
	now := t.now()
	if t.persistent() {
		detail, _ := json.Marshal(map[string]string{"caller": caller})
		t.save(entity.MockCall{
			RecordID: id, Scenario: "sip-inbound", Source: "sip",
			CustomerNumber: callee, Result: outcome,
			Status:    entity.CallRecordStatusRinging,
			Direction: "HERMES_TO_MOCK", StartedAt: now, LastEventAt: now,
			CallUUID:   callUUID,
			TaskCode:   businessID, // 关联键：发起业务的 businessId（X-JBusinessId/x-business_id）
			DetailJSON: string(detail),
		})
		return id
	}
	t.mu.Lock()
	t.active[id] = &Call{ID: id, Callee: callee, Caller: caller, Outcome: outcome, State: StateRinging, StartedAt: now}
	t.mu.Unlock()
	return id
}

// Answered 标记接听。
func (t *Tracker) Answered(id string) {
	if t.persistent() {
		now := t.now()
		t.save(entity.MockCall{
			RecordID: id, Status: entity.CallRecordStatusAnswered, AnsweredAt: &now, LastEventAt: now,
		})
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.active[id]; c != nil {
		now := t.now()
		c.AnsweredAt = &now
		c.State = StateAnswered
	}
}

// Rejected 标记拒接/失败并归档。
func (t *Tracker) Rejected(id string, code int) {
	if t.persistent() {
		now := t.now()
		t.save(entity.MockCall{
			RecordID: id, Status: entity.CallRecordStatusRejected, HangupCode: code, EndedAt: &now, LastEventAt: now,
		})
		return
	}
	t.finish(id, StateRejected, code)
}

// Ended 标记正常结束并归档。
func (t *Tracker) Ended(id string) {
	if t.persistent() {
		now := t.now()
		t.save(entity.MockCall{
			RecordID: id, Status: entity.CallRecordStatusEnded, EndedAt: &now, LastEventAt: now,
		})
		return
	}
	t.finish(id, StateEnded, 0)
}

func (t *Tracker) finish(id string, st State, code int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.active[id]
	if c == nil {
		return
	}
	now := t.now()
	c.EndedAt = &now
	c.State = st
	if code != 0 {
		c.HangupCode = code
	}
	delete(t.active, id)
	t.recent = append(t.recent, c)
	if len(t.recent) > t.maxRecent {
		t.recent = t.recent[len(t.recent)-t.maxRecent:]
	}
}

// Active 返回当前活跃通话（RINGING/ANSWERED）。DB 模式只看最近 1 小时，避免历史残留。
func (t *Tracker) Active() []*Call {
	if t.persistent() {
		out := []*Call{}
		cutoff := t.now().Add(-time.Hour)
		for _, r := range t.recentRows() {
			if (r.Status == entity.CallRecordStatusRinging || r.Status == entity.CallRecordStatusAnswered) && r.StartedAt.After(cutoff) {
				out = append(out, rowToCall(r))
			}
		}
		return out
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*Call, 0, len(t.active))
	for _, c := range t.active {
		out = append(out, c)
	}
	return out
}

// Recent 返回最近已结束通话（新→旧）。
func (t *Tracker) Recent() []*Call {
	if t.persistent() {
		out := []*Call{}
		for _, r := range t.recentRows() {
			if r.Status == entity.CallRecordStatusEnded || r.Status == entity.CallRecordStatusRejected {
				out = append(out, rowToCall(r))
			}
		}
		return out
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*Call, 0, len(t.recent))
	for i := len(t.recent) - 1; i >= 0; i-- {
		out = append(out, t.recent[i])
	}
	return out
}

// Stats 简单计数。
type Stats struct {
	Active   int `json:"active"`
	Total    int `json:"total"`
	Answered int `json:"answered"`
	Rejected int `json:"rejected"`
}

func (t *Tracker) Stats() Stats {
	if t.persistent() {
		s := Stats{}
		cutoff := t.now().Add(-time.Hour)
		for _, r := range t.recentRows() {
			switch r.Status {
			case entity.CallRecordStatusRinging, entity.CallRecordStatusAnswered:
				if r.StartedAt.After(cutoff) {
					s.Active++
				}
			case entity.CallRecordStatusEnded:
				s.Answered++
				s.Total++
			case entity.CallRecordStatusRejected, entity.CallRecordStatusFailed:
				s.Rejected++
				s.Total++
			}
		}
		return s
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := Stats{Active: len(t.active), Total: len(t.recent)}
	for _, c := range t.recent {
		switch c.State {
		case StateEnded:
			s.Answered++
		case StateRejected:
			s.Rejected++
		}
	}
	return s
}

// recentRows 取最近的 sip-inbound 记录（DB 模式）。
func (t *Tracker) recentRows() []entity.MockCall {
	ctx, cancel := t.ctx()
	defer cancel()
	rows, _, err := t.repo.ListCallRecords(ctx, entity.CallRecordFilter{Scenario: "sip-inbound", PageSize: 200})
	if err != nil {
		return nil
	}
	return rows
}

// rowToCall 把落库的呼叫记录映射回监控页模型（caller 从 detail_json 还原）。
func rowToCall(r entity.MockCall) *Call {
	caller := ""
	if r.DetailJSON != "" {
		var m map[string]string
		if json.Unmarshal([]byte(r.DetailJSON), &m) == nil {
			caller = m["caller"]
		}
	}
	return &Call{
		ID: r.RecordID, Callee: r.CustomerNumber, Caller: caller, Outcome: r.Result,
		State: State(r.Status), HangupCode: r.HangupCode,
		StartedAt: r.StartedAt, AnsweredAt: r.AnsweredAt, EndedAt: r.EndedAt,
	}
}
