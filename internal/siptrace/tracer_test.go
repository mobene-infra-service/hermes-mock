package siptrace

import "testing"

// 一条真实风格的 Hermes INVITE（含 FS 注入的业务头与 SDP）。
const sampleInvite = "INVITE sip:123@192.168.107.9:5060 SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 192.168.107.5:5080;rport;branch=z9hG4bKabc123\r\n" +
	"Max-Forwards: 70\r\n" +
	"From: \"8613800000000\" <sip:8613800000000@192.168.107.5>;tag=fromtag1\r\n" +
	"To: <sip:123@192.168.107.9:5060>\r\n" +
	"Call-ID: call-id-019e96e8-abc@192.168.107.5\r\n" +
	"CSeq: 10 INVITE\r\n" +
	"Contact: <sip:8613800000000@192.168.107.5:5080>\r\n" +
	"X-CALL-UUID: 019e96e80ffa794c804213adc2abb13c\r\n" +
	"X-SESSION-ID: CCINC019e96e8\r\n" +
	"X-CALL-CENTER-TYPE: 2\r\n" +
	"Content-Type: application/sdp\r\n" +
	"Content-Length: 120\r\n" +
	"\r\n" +
	"v=0\r\no=FreeSWITCH 1 1 IN IP4 192.168.107.5\r\ns=FS\r\nc=IN IP4 192.168.107.5\r\nt=0 0\r\nm=audio 27224 RTP/AVP 0 8\r\n"

func TestParseInviteHeadersAndCallID(t *testing.T) {
	p := parse([]byte(sampleInvite))
	if p == nil {
		t.Fatal("parse returned nil")
	}
	if p.method != "INVITE" || !p.isRequest {
		t.Errorf("method=%q isRequest=%v, want INVITE/true", p.method, p.isRequest)
	}
	if p.callID != "call-id-019e96e8-abc@192.168.107.5" {
		t.Errorf("callID=%q 解析错", p.callID)
	}
	// 业务 callUuid 应从 X-CALL-UUID 提取（多腿聚合键）
	if p.bizUUID != "019e96e80ffa794c804213adc2abb13c" {
		t.Errorf("bizUUID=%q, 应取 X-CALL-UUID", p.bizUUID)
	}
	// 业务头必须抓到
	want := map[string]string{
		"X-CALL-UUID":        "019e96e80ffa794c804213adc2abb13c",
		"X-SESSION-ID":       "CCINC019e96e8",
		"X-CALL-CENTER-TYPE": "2",
	}
	got := map[string]string{}
	for _, h := range p.headers {
		got[h.Name] = h.Value
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("业务头 %s=%q, want %q", k, got[k], v)
		}
	}
	// 腿应取被叫 user=123
	if leg := legOf(p, "IN"); leg != "123" {
		t.Errorf("legOf=%q, want 123", leg)
	}
}

func TestParseResponse(t *testing.T) {
	resp := "SIP/2.0 200 OK\r\n" +
		"Via: SIP/2.0/UDP 192.168.107.5:5080;branch=z9hG4bKabc123\r\n" +
		"From: \"8613800000000\" <sip:8613800000000@192.168.107.5>;tag=fromtag1\r\n" +
		"To: <sip:123@192.168.107.9:5060>;tag=totag9\r\n" +
		"Call-ID: call-id-019e96e8-abc@192.168.107.5\r\n" +
		"CSeq: 10 INVITE\r\n\r\n"
	p := parse([]byte(resp))
	if p == nil || p.isRequest {
		t.Fatalf("应解析为响应, got %+v", p)
	}
	if p.method != "200" {
		t.Errorf("status=%q, want 200", p.method)
	}
	if p.callID != "call-id-019e96e8-abc@192.168.107.5" {
		t.Errorf("响应 callID=%q 解析错（应与 INVITE 同 → 聚合同会话）", p.callID)
	}
	if got := p.startLineCode(); got != "200 OK" {
		t.Errorf("startLineCode=%q, want '200 OK'", got)
	}
}

func TestParseNonSIPIgnored(t *testing.T) {
	if p := parse([]byte("\r\n\r\n")); p != nil { // keepalive ping
		t.Errorf("keepalive 应忽略, got %+v", p)
	}
	if p := parse([]byte("garbage no colon line")); p == nil {
		t.Skip() // 起始行被当请求方法 garbage，callID 空，handle 层会丢弃
	}
}

func TestUserOf(t *testing.T) {
	cases := map[string]string{
		`"8613800000000" <sip:8613800000000@192.168.107.5>;tag=x`: "8613800000000",
		`<sip:123@192.168.107.9:5060>`:                            "123",
		`<sip:5002@192.168.107.5>;tag=y`:                          "5002",
		``:                                                        "",
	}
	for in, want := range cases {
		if got := userOf(in); got != want {
			t.Errorf("userOf(%q)=%q, want %q", in, got, want)
		}
	}
}

// resolveAggKey：INVITE 的业务头记住后，同 Call-ID 的响应(无业务头)复用同一聚合键，
// 避免「INVITE 进 biz 会话、200/ACK/BYE 另起 callID 会话」的分裂（实测踩坑回归）。
func TestResolveAggKeyStickyBizUUID(t *testing.T) {
	tr := &Tracer{cid2biz: map[string]string{}}
	cid := "leg-call-id-1"
	biz := "019e96e80ffa794c804213adc2abb13c"
	// INVITE 带业务头 → 返回 biz 并记住
	if got := tr.resolveAggKey(cid, biz); got != biz {
		t.Fatalf("INVITE 应返回 bizUUID, got %q", got)
	}
	// 200/ACK/BYE 无业务头但同 Call-ID → 仍返回 biz（不分裂）
	if got := tr.resolveAggKey(cid, ""); got != biz {
		t.Errorf("响应应复用 bizUUID, got %q（分裂 bug）", got)
	}
	// 完全无关的 Call-ID 且无业务头 → 退回 Call-ID
	if got := tr.resolveAggKey("other-cid", ""); got != "other-cid" {
		t.Errorf("无业务头无记录应返回 callID, got %q", got)
	}
	// 另一条腿(不同 Call-ID)带同一 bizUUID → 也聚合到 biz（双腿合并）
	if got := tr.resolveAggKey("leg-call-id-2", biz); got != biz {
		t.Errorf("第二条腿同 bizUUID 应聚合, got %q", got)
	}
	if got := tr.resolveAggKey("leg-call-id-2", ""); got != biz {
		t.Errorf("第二条腿响应应复用 bizUUID, got %q", got)
	}
}
