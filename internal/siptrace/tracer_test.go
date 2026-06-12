package siptrace

import (
	"strconv"
	"testing"
)

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

func TestResolveLegSticksToInviteCallee(t *testing.T) {
	tr := &Tracer{cid2leg: map[string]string{}}
	invite := parse([]byte(
		"INVITE sip:8613800100009@10.24.140.92:5060 SIP/2.0\r\n" +
			"From: \"1222\" <sip:1222@172.16.7.27>;tag=caller\r\n" +
			"To: <sip:8613800100009@10.24.140.92:5060>\r\n" +
			"Call-ID: same-dialog\r\n\r\n",
	))
	if got := tr.resolveLeg(invite, "IN"); got != "8613800100009" {
		t.Fatalf("INVITE leg=%q, want callee", got)
	}

	bye := parse([]byte(
		"BYE sip:1222@172.16.7.27 SIP/2.0\r\n" +
			"From: <sip:8613800100009@10.24.140.92:5060>;tag=callee\r\n" +
			"To: \"1222\" <sip:1222@172.16.7.27>;tag=caller\r\n" +
			"Call-ID: same-dialog\r\n\r\n",
	))
	if got := tr.resolveLeg(bye, "OUT"); got != "8613800100009" {
		t.Fatalf("BYE leg=%q, want sticky callee（不能误标成 1222）", got)
	}

	byeOK := parse([]byte(
		"SIP/2.0 200 OK\r\n" +
			"From: <sip:8613800100009@10.24.140.92:5060>;tag=callee\r\n" +
			"To: \"1222\" <sip:1222@172.16.7.27>;tag=caller\r\n" +
			"Call-ID: same-dialog\r\n\r\n",
	))
	if got := tr.resolveLeg(byeOK, "IN"); got != "8613800100009" {
		t.Fatalf("BYE 200 leg=%q, want sticky callee", got)
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

// parse() 应按优先级取 call-uuid 族，即使 X-SESSION-ID 排在 X-CALL-UUID 之前——
// 与 sipagent 共用 tracelog.BizUUIDFromHeaders，保证两路聚合键一致。
// TestParseBizUUIDIgnoresJCallId 验证：被叫腿 INVITE 同时带 X-JCallId(businessId) 与
// x-session-id(=CCMDL{uuid}=真 callUuid) 时，提取必须取 x-session-id 而非 X-JCallId——
// 否则坐席外呼两腿（坐席侧记录 CCMDL{uuid} vs 被叫腿）关联不上。这是 BizUUID 优先级修复的回归守卫。
func TestParseBizUUIDIgnoresJCallId(t *testing.T) {
	msg := "INVITE sip:123@h SIP/2.0\r\n" +
		"From: <sip:a@h>;tag=1\r\n" +
		"To: <sip:123@h>\r\n" +
		"Call-ID: cid-x\r\n" +
		"X-JCallId: BIZ\r\n" + // businessId，非 callUuid，不该被取
		"x-session-id: CCMDLuuid\r\n" + // Hermes 通用 callUuid 载体
		"\r\n"
	p := parse([]byte(msg))
	if p == nil {
		t.Fatal("parse nil")
	}
	if p.bizUUID != "CCMDLuuid" {
		t.Errorf("bizUUID=%q, 应取 x-session-id(CCMDLuuid) 而非 X-JCallId(businessId)", p.bizUUID)
	}
}

// resolveAggKey/resolveLeg 写入的 cid 映射必须有容量上限：灌入 > maxCID 个 Call-ID 后
// 映射不超限、最旧被淘汰、最新仍可解析（堵内存泄漏的回归）。
func TestRememberCIDEvictsOldest(t *testing.T) {
	tr := &Tracer{cid2biz: map[string]string{}, cid2leg: map[string]string{}}
	for i := 0; i < maxCID+100; i++ {
		tr.resolveAggKey("cid-"+strconv.Itoa(i), "biz-"+strconv.Itoa(i))
	}
	if len(tr.cid2biz) > maxCID || len(tr.cidOrder) > maxCID {
		t.Errorf("超上限: cid2biz=%d cidOrder=%d max=%d", len(tr.cid2biz), len(tr.cidOrder), maxCID)
	}
	if _, ok := tr.cid2biz["cid-0"]; ok {
		t.Error("最旧 cid-0 应被淘汰")
	}
	last := "cid-" + strconv.Itoa(maxCID+99)
	if got := tr.resolveAggKey(last, ""); got != "biz-"+strconv.Itoa(maxCID+99) {
		t.Errorf("最新 %s 应仍解析到 bizUUID, got %q", last, got)
	}
}
