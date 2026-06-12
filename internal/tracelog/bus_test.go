package tracelog

import (
	"encoding/json"
	"testing"
)

func TestEnsureByCallIDAggregates(t *testing.T) {
	b := New()
	// 客户腿与坐席腿是两条 SIP 会话，但 Hermes 用同一 Call-ID（或业务 callUuid）关联。
	// 这里验证：同一 Call-ID 的报文聚合到同一会话。
	cid := "call-abc@fs"
	s1 := b.EnsureByCallID(cid, "call", "INVITE 客户腿")
	b.EmitSIP(s1, "customer", DirIn, "INVITE", "收到 INVITE", nil, "raw1", cid, "1.1.1.1:5060", "2.2.2.2:5060")

	// 同 Call-ID 的 200 OK（响应）应落到同一会话
	s2 := b.EnsureByCallID(cid, "call", "200 OK")
	if s1 != s2 {
		t.Fatalf("同 Call-ID 应聚合到一个会话: s1=%s s2=%s", s1, s2)
	}
	b.EmitSIP(s2, "customer", DirOut, "200", "应答", nil, "raw2", cid, "2.2.2.2:5060", "1.1.1.1:5060")

	sess := b.Session(s1)
	if sess == nil || len(sess.Events) != 2 {
		t.Fatalf("会话应含 2 个事件, got %v", sess)
	}
	if sess.CallID != cid {
		t.Errorf("会话 CallID=%q, want %q", sess.CallID, cid)
	}

	// 不同 Call-ID 开新会话
	s3 := b.EnsureByCallID("other-cid@fs", "call", "另一通")
	if s3 == s1 {
		t.Error("不同 Call-ID 不应聚合")
	}
	if got := b.SessionByCallID(cid); got != s1 {
		t.Errorf("SessionByCallID=%q, want %q", got, s1)
	}
}

func TestEmitSIPCarriesRealMessage(t *testing.T) {
	b := New()
	s := b.OpenSession("call", "t")
	hdrs := []HeaderKV{{Name: "X-CALL-UUID", Value: "abc"}, {Name: "From", Value: "<sip:x@y>"}}
	b.EmitSIP(s, "customer", DirIn, "INVITE", "收到", hdrs, "INVITE sip:...\r\n", "cid1", "1.1.1.1:5060", "2.2.2.2:5060")
	sess := b.Session(s)
	if len(sess.Events) != 1 {
		t.Fatal("want 1 event")
	}
	e := sess.Events[0]
	if len(e.Headers) != 2 || e.Headers[0].Name != "X-CALL-UUID" {
		t.Errorf("headers 未携带: %+v", e.Headers)
	}
	if e.Raw == "" || e.CallID != "cid1" {
		t.Errorf("raw/callID 未携带: raw=%q callID=%q", e.Raw, e.CallID)
	}
}

// TestSessionsSnapshotRaceFree 回归：Sessions()/Session() 必须返回深拷贝快照，使读取侧脱锁后
// 遍历/Marshal 不与并发 emit 的 append 竞争。`go test -race` 下，修复前会报 data race。
func TestSessionsSnapshotRaceFree(t *testing.T) {
	b := New()
	const cid = "race-cid@fs"
	sess := b.EnsureByCallID(cid, "call", "race")
	done := make(chan struct{})
	go func() { // writer：持续追加事件到同一会话
		for i := 0; i < 3000; i++ {
			b.Emit(sess, "leg", ChanFlow, DirNA, "m", "s", map[string]string{"i": "x"})
		}
		close(done)
	}()
	for { // reader：并发取快照并 Marshal（与 append 同时进行）
		select {
		case <-done:
			return
		default:
			for _, s := range b.Sessions() {
				if _, err := json.Marshal(s); err != nil {
					t.Errorf("marshal sessions: %v", err)
				}
			}
			if s := b.Session(sess); s != nil {
				if _, err := json.Marshal(s); err != nil {
					t.Errorf("marshal session: %v", err)
				}
			}
		}
	}
}

// TestBizUUIDFromHeadersPriority 验证业务 callUuid 提取优先级：
// 真 callUuid / x-session-id 族同权优先，x-jcallid(businessId) 仅最末兜底——
// 保证 siptrace 与 sipagent 两路对同一 INVITE 取同一聚合键，且不误把 businessId 当 callUuid。
func TestBizUUIDFromHeadersPriority(t *testing.T) {
	// x-session-id 是 Hermes 通用 callUuid 载体，应被优先取（不被 x-jcallid=businessId 抢占）
	got := BizUUIDFromHeaders([]HeaderKV{
		{Name: "X-JCallId", Value: "BIZ1"},
		{Name: "X-SESSION-ID", Value: "CCMDLsess1"},
	})
	if got != "CCMDLsess1" {
		t.Errorf("x-session-id 应优先于 x-jcallid: got %q want CCMDLsess1", got)
	}
	// 真 callUuid 头同样优先于 x-jcallid
	got = BizUUIDFromHeaders([]HeaderKV{
		{Name: "x-jcallid", Value: "BIZ2"},
		{Name: "X-CALL-UUID", Value: "CALL2"},
	})
	if got != "CALL2" {
		t.Errorf("x-call-uuid 应优先于 x-jcallid: got %q want CALL2", got)
	}
	// 只有 x-jcallid 时才退回它（businessId 兜底）
	if got := BizUUIDFromHeaders([]HeaderKV{{Name: "X-JCallId", Value: "BIZ3"}}); got != "BIZ3" {
		t.Errorf("兜底退回 x-jcallid: got %q want BIZ3", got)
	}
	// 只有 session-id 时取 session-id
	if got := BizUUIDFromHeaders([]HeaderKV{{Name: "x-session-id", Value: "S2"}}); got != "S2" {
		t.Errorf("退回 session-id: got %q want S2", got)
	}
	// 大小写不敏感 + 空值跳过（取下一个非空 call-uuid 族头）
	got = BizUUIDFromHeaders([]HeaderKV{{Name: "X-Call-Uuid", Value: ""}, {Name: "x-callid", Value: "CID2"}})
	if got != "CID2" {
		t.Errorf("大小写/空值: got %q want CID2", got)
	}
	// 无业务头 → 空
	if got := BizUUIDFromHeaders([]HeaderKV{{Name: "From", Value: "x"}}); got != "" {
		t.Errorf("无业务头: got %q want empty", got)
	}
}
