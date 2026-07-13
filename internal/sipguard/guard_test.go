package sipguard

import (
	"testing"

	"hermes-mock/internal/config"
)

func mustNew(t *testing.T, cfg config.Config) *Guard {
	t.Helper()
	g, err := New(&cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func TestDeniedUA(t *testing.T) {
	g := mustNew(t, config.Config{SIPDenyUserAgents: "friendly-scanner, sipvicious ,sipcli"})
	cases := []struct {
		ua   string
		deny bool
	}{
		{"friendly-scanner", true},
		{"FRIENDLY-SCANNER v1", true}, // 大小写不敏感 + 子串
		{"sipvicious/0.3", true},
		{"SIPCLI 1.8", true},
		{"FreeSWITCH-mod_sofia/1.10.9", false}, // 自己人
		{"", false},                            // 空 UA 不算命中
	}
	for _, c := range cases {
		if got := g.DeniedUA(c.ua); got != c.deny {
			t.Errorf("DeniedUA(%q)=%v want %v", c.ua, got, c.deny)
		}
	}
	// 未配置指纹 → 全不命中
	g2 := mustNew(t, config.Config{})
	if g2.DeniedUA("friendly-scanner") {
		t.Error("空指纹配置不应命中任何 UA")
	}
}

func TestSourceAllowed_NoWhitelist(t *testing.T) {
	// 没配置白名单 → 不限制 IP
	g := mustNew(t, config.Config{})
	if g.Restricted() {
		t.Error("未配置白名单不应 restricted")
	}
	for _, addr := range []string{"1.2.3.4:5060", "8.8.8.8", "[::1]:5060"} {
		if !g.SourceAllowed(addr) {
			t.Errorf("未配置白名单应放行 %s", addr)
		}
	}
}

func TestSourceAllowed_Whitelist(t *testing.T) {
	g := mustNew(t, config.Config{SIPAllowedSources: "172.16.0.0/12,127.0.0.1"})
	if !g.SourceAllowed("172.16.7.27:5060") || !g.SourceAllowed("127.0.0.1") {
		t.Error("白名单内来源应放行")
	}
	if g.SourceAllowed("8.8.8.8:5060") {
		t.Error("白名单外来源应拒绝")
	}
}

func TestStrikeAutoBan(t *testing.T) {
	g := mustNew(t, config.Config{SIPBanThreshold: 3, SIPBanMinutes: 5})
	src := "203.0.113.9:31337"
	for i := 0; i < 2; i++ {
		g.Strike(src, "deny-ua")
		if !g.SourceAllowed(src) {
			t.Fatalf("第 %d 次违规未到阈值不应封禁", i+1)
		}
	}
	g.Strike(src, "deny-ua")
	if g.SourceAllowed(src) {
		t.Error("达到阈值应封禁")
	}
	if g.SourceAllowed("203.0.113.9") { // 同 IP 不同写法也应命中封禁
		t.Error("封禁按 IP 生效，与端口无关")
	}
	if !g.SourceAllowed("203.0.113.10:5060") {
		t.Error("其他 IP 不受影响")
	}
}

func TestStrikeDisabledAndTrustedExempt(t *testing.T) {
	// 阈值<=0 → 禁用封禁
	g := mustNew(t, config.Config{SIPBanThreshold: 0})
	for i := 0; i < 100; i++ {
		g.Strike("1.2.3.4:5060", "x")
	}
	if !g.SourceAllowed("1.2.3.4:5060") {
		t.Error("封禁禁用时不应封任何来源")
	}
	// 白名单内的可信来源永不被封禁（防集群缺号段误封自己的 FS）
	g2 := mustNew(t, config.Config{SIPAllowedSources: "172.16.0.0/12", SIPBanThreshold: 2, SIPBanMinutes: 5})
	for i := 0; i < 10; i++ {
		g2.Strike("172.16.7.27:5060", "unknown-callee")
	}
	if !g2.SourceAllowed("172.16.7.27:5060") {
		t.Error("白名单内来源不应被自动封禁")
	}
}
