package sipagent

import (
	"bytes"
	"net"
	"testing"

	"github.com/emiago/sipgo/sip"

	"hermes-mock/internal/behavior"
)

func TestReasonForCode(t *testing.T) {
	cases := []struct {
		code     int
		fallback string
		want     string
	}{
		// 拒接类常用码：均应取标准短语，不再回退 fallback
		{486, "Busy Here", "Busy Here"},
		{503, "Service Unavailable", "Service Unavailable"},
		{480, "Temporarily Unavailable", "Temporarily Unavailable"},
		{603, "Busy Here", "Decline"}, // 自定义 603 不再误写 "Busy Here"
		{500, "Busy Here", "Server Internal Error"},
		{404, "x", "Not Found"},
		{487, "x", "Request Terminated"},
		{600, "x", "Busy Everywhere"},
		// 未知码 → fallback 原样返回
		{499, "保底短语", "保底短语"},
		{0, "fb", "fb"},
	}
	for _, c := range cases {
		if got := reasonForCode(c.code, c.fallback); got != c.want {
			t.Errorf("reasonForCode(%d,%q)=%q, want %q", c.code, c.fallback, got, c.want)
		}
	}
}

func TestNextIVRStep(t *testing.T) {
	step := behavior.IVRStep{
		ID:      "menu",
		Branch:  map[string]string{"1": "sales", "2": "support", "0": "HANGUP"},
		OnNoKey: "retry",
	}
	// 按键命中分支
	if n, h := nextIVRStep(step, "1"); n != "sales" || h {
		t.Errorf(`按 1 应→sales, got (%q,%v)`, n, h)
	}
	if n, h := nextIVRStep(step, "2"); n != "support" || h {
		t.Errorf(`按 2 应→support, got (%q,%v)`, n, h)
	}
	// 按键映射到 HANGUP
	if n, h := nextIVRStep(step, "0"); n != "" || !h {
		t.Errorf(`按 0 应挂断, got (%q,%v)`, n, h)
	}
	// 按了未映射键 → 留在原步重试
	if n, h := nextIVRStep(step, "9"); n != "menu" || h {
		t.Errorf(`按未映射键应留原步, got (%q,%v)`, n, h)
	}
	// 超时无键 → OnNoKey
	if n, h := nextIVRStep(step, ""); n != "retry" || h {
		t.Errorf(`无键应→retry, got (%q,%v)`, n, h)
	}
	// OnNoKey 为空 → 挂断
	step2 := behavior.IVRStep{ID: "x", Branch: map[string]string{"1": "y"}}
	if n, h := nextIVRStep(step2, ""); n != "" || !h {
		t.Errorf(`无 OnNoKey 应挂断, got (%q,%v)`, n, h)
	}
}

func TestCodecList(t *testing.T) {
	// 默认/空 → PCMU,PCMA
	if got := codecList(""); len(got) != 2 || got[0].Name != "PCMU" || got[1].Name != "PCMA" {
		t.Errorf("空配置应回退 PCMU,PCMA, got %v", got)
	}
	// 显式多编解码（含 opus）按序
	got := codecList("opus, PCMA , PCMU")
	if len(got) != 3 || got[0].Name != "opus" || got[1].Name != "PCMA" || got[2].Name != "PCMU" {
		t.Errorf("应按序解析 opus,PCMA,PCMU, got %v", got)
	}
	// 未识别名跳过，全未识别回退默认
	if got := codecList("bogus,xxx"); len(got) != 2 {
		t.Errorf("全未识别应回退默认 2 项, got %v", got)
	}
}

func TestRtpLoss(t *testing.T) {
	cases := []struct {
		name              string
		first, last       uint16
		received          uint64
		wantLost, wantPct int
	}{
		{"无丢包", 100, 199, 100, 0, 0},     // 期望 100，收 100
		{"丢10%", 100, 199, 90, 10, 10},   // 期望 100，收 90 → 丢 10
		{"全丢但收0按未流动", 100, 199, 0, 0, 0}, // received=0 视为无媒体，不算丢
		{"序号回绕", 65530, 9, 16, 0, 0},     // 65530..9 跨界=16 个，收 16 → 不丢
		{"回绕有丢", 65530, 9, 12, 4, 25},    // 期望 16，收 12 → 丢 4 = 25%
		{"收多于期望不算丢", 100, 109, 20, 0, 0}, // 期望 10 收 20（重复/乱序）→ 不算丢
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lost, pct := rtpLoss(c.first, c.last, c.received)
			if lost != c.wantLost || pct != c.wantPct {
				t.Errorf("rtpLoss(%d,%d,%d)=(%d,%d%%), want (%d,%d%%)",
					c.first, c.last, c.received, lost, pct, c.wantLost, c.wantPct)
			}
		})
	}
}

func TestAddTopViaSourceParams(t *testing.T) {
	source := &net.UDPAddr{IP: net.ParseIP("172.16.7.27"), Port: 5060}
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			name: "给请求顶层Via补rport",
			in: "INVITE sip:1000@example.com SIP/2.0\r\n" +
				"Via: SIP/2.0/UDP 47.251.74.116:5060;branch=z9hG4bK1\r\n" +
				"Via: SIP/2.0/UDP 172.16.7.27:5080;branch=z9hG4bK2\r\n" +
				"Content-Length: 0\r\n\r\n",
			want: "INVITE sip:1000@example.com SIP/2.0\r\n" +
				"Via: SIP/2.0/UDP 47.251.74.116:5060;branch=z9hG4bK1;rport=5060;received=172.16.7.27\r\n" +
				"Via: SIP/2.0/UDP 172.16.7.27:5080;branch=z9hG4bK2\r\n" +
				"Content-Length: 0\r\n\r\n",
			changed: true,
		},
		{
			name: "已有rport不重复补",
			in: "INVITE sip:1000@example.com SIP/2.0\r\n" +
				"Via: SIP/2.0/UDP 47.251.74.116:5060;branch=z9hG4bK1;rport\r\n" +
				"Content-Length: 0\r\n\r\n",
			changed: false,
		},
		{
			name: "SIP响应不改",
			in: "SIP/2.0 200 OK\r\n" +
				"Via: SIP/2.0/UDP 47.251.74.116:5060;branch=z9hG4bK1\r\n" +
				"Content-Length: 0\r\n\r\n",
			changed: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := addTopViaSourceParams(sip.TransportReadProps{Transport: "UDP", RemoteAddr: source}, []byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			want := c.want
			if want == "" {
				want = c.in
			}
			if string(got) != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
			if (bytes.Equal(got, []byte(c.in)) == false) != c.changed {
				t.Fatalf("changed=%v, want %v", !bytes.Equal(got, []byte(c.in)), c.changed)
			}
		})
	}
}

func TestAddTopViaSourceParamsSkipsNonUDP(t *testing.T) {
	in := []byte("INVITE sip:1000@example.com SIP/2.0\r\nVia: SIP/2.0/TCP 47.251.74.116:5060;branch=z\r\n\r\n")
	got, err := addTopViaSourceParams(sip.TransportReadProps{Transport: "TCP", RemoteAddr: &net.TCPAddr{IP: net.ParseIP("172.16.7.27"), Port: 5060}}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, in) {
		t.Fatalf("TCP 入站不应改写 Via, got %q", got)
	}
}
