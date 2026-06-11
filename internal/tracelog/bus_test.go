package tracelog

import "testing"

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
