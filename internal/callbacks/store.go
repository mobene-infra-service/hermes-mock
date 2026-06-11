// Package callbacks 接收 Hermes 主动回调（任务结果/自动外呼/CDR/会话推送等 webhook），
// 经 Repository 落库到 mock_callback，并按 callUuid 关联进通话链路，供「回调」页查询。
// 回调地址需在 Hermes 侧(t_callback_address)配置指向 mock，这里只负责接收+展示（不写 Hermes 表）。
package callbacks

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"hermes-mock/internal/entity"
	"hermes-mock/internal/model"
)

// Record 一条收到的回调。
type Record struct {
	Seq      int64           `json:"seq"`
	TS       time.Time       `json:"ts"`
	Source   string          `json:"source"`
	Event    string          `json:"event"`
	OrgCode  string          `json:"orgCode"`
	CallUUID string          `json:"callUuid"`
	Remote   string          `json:"remote"`
	Payload  json.RawMessage `json:"payload"`
}

// Store 回调记录存储：经 Repository 落 mock_callback；repo=nil 仅单测（内存环）。
type Store struct {
	repo model.Repository
	mu   sync.Mutex
	seq  int64
	// 内存回退
	recs []Record
	max  int
}

// New 创建 Store；运行时 repo 必为已初始化的 Repository；nil 仅单测（内存环）。
func New(repo model.Repository) *Store { return &Store{repo: repo, max: 1000} }

func (s *Store) persistent() bool { return s.repo != nil }

// Record 记录一条回调，返回提取出的关键字段。
func (s *Store) Record(source, remote string, payload []byte) Record {
	event, org, callUUID := extract(payload)
	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	r := Record{
		Seq: seq, TS: time.Now(), Source: source, Event: event,
		OrgCode: org, CallUUID: callUUID, Remote: remote, Payload: json.RawMessage(payload),
	}
	if s.persistent() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repo.CreateCallback(ctx, &entity.Callback{
			Seq: seq, TS: r.TS, Source: source, Event: event,
			OrgCode: org, CallUUID: callUUID, Remote: remote, PayloadJSON: string(payload),
		})
		return r
	}
	s.mu.Lock()
	s.recs = append(s.recs, r)
	if len(s.recs) > s.max {
		s.recs = s.recs[len(s.recs)-s.max:]
	}
	s.mu.Unlock()
	return r
}

// Filter 查询筛选条件。
type Filter struct {
	Source   string
	Event    string
	OrgCode  string
	CallUUID string
	Keyword  string // 在原始 payload 里模糊匹配
	Limit    int
}

// Query 按条件倒序返回回调记录（新→旧）。
func (s *Store) Query(f Filter) []Record {
	if s.persistent() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, _ := s.repo.ListCallbacks(ctx, entity.CallbackFilter{
			Source: f.Source, Event: f.Event, OrgCode: f.OrgCode,
			CallUUID: f.CallUUID, Keyword: f.Keyword, Limit: f.Limit,
		})
		out := make([]Record, 0, len(rows))
		for _, row := range rows {
			out = append(out, Record{
				Seq: row.Seq, TS: row.TS, Source: row.Source, Event: row.Event,
				OrgCode: row.OrgCode, CallUUID: row.CallUUID, Remote: row.Remote,
				Payload: json.RawMessage(row.PayloadJSON),
			})
		}
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	out := make([]Record, 0, limit)
	for i := len(s.recs) - 1; i >= 0 && len(out) < limit; i-- {
		r := s.recs[i]
		if f.Source != "" && r.Source != f.Source {
			continue
		}
		if f.Event != "" && r.Event != f.Event {
			continue
		}
		if f.OrgCode != "" && r.OrgCode != f.OrgCode {
			continue
		}
		if f.CallUUID != "" && r.CallUUID != f.CallUUID {
			continue
		}
		if f.Keyword != "" && !containsFold(string(r.Payload), f.Keyword) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// extract 从回调 payload 里尽力提取 event/orgCode/callUuid（兼容顶层与嵌套 data）。
func extract(payload []byte) (event, org, callUUID string) {
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return "", "", ""
	}
	pick := func(src map[string]any) {
		for _, k := range []string{"event", "eventType", "type", "action"} {
			if event == "" {
				if v, ok := src[k].(string); ok {
					event = v
				}
			}
		}
		for _, k := range []string{"orgCode", "org_code"} {
			if org == "" {
				if v, ok := src[k].(string); ok {
					org = v
				}
			}
		}
		for _, k := range []string{"callUuid", "call_uuid", "callId", "uuid"} {
			if callUUID == "" {
				if v, ok := src[k].(string); ok {
					callUUID = v
				}
			}
		}
	}
	pick(m)
	if d, ok := m["data"].(map[string]any); ok {
		pick(d)
	}
	return event, org, callUUID
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 || indexFold(s, sub) >= 0
}

// indexFold 简易大小写无关子串查找。
func indexFold(s, sub string) int {
	ls, lsub := len(s), len(sub)
	for i := 0; i+lsub <= ls; i++ {
		match := true
		for j := 0; j < lsub; j++ {
			if lower(s[i+j]) != lower(sub[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func lower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 32
	}
	return b
}
