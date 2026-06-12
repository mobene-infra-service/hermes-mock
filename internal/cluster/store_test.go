package cluster

import "testing"

func TestContains(t *testing.T) {
	g := &CustomerGroup{NumberPrefix: "8613800", NumberStart: 1000, Count: 100}
	cases := map[string]bool{
		"86138001000": true,  // 起始
		"86138001099": true,  // 末位
		"86138001100": false, // 超界
		"86138000999": false, // 下界外
		"8613800":     false, // 无数字部分
		"13800001000": false, // 前缀不匹配
	}
	for num, want := range cases {
		if got := g.Contains(num); got != want {
			t.Errorf("Contains(%q)=%v want %v", num, got, want)
		}
	}
	// 无前缀按整号
	g2 := &CustomerGroup{NumberStart: 100, Count: 5}
	if !g2.Contains("102") || g2.Contains("105") {
		t.Error("无前缀号段比较错")
	}
}

func TestResolveByNumberGroupAndOverride(t *testing.T) {
	s := NewMemory()
	s.UpsertProfile(BehaviorProfile{Code: "answer", Outcome: "ANSWER", TalkMs: 5000, AnswerRatio: 100})
	s.UpsertProfile(BehaviorProfile{Code: "busy", Outcome: "BUSY", HangupCode: 486, AnswerRatio: 100})
	s.UpsertGroup(CustomerGroup{Code: "g1", NumberPrefix: "8613800", NumberStart: 0, Count: 1000, BehaviorCode: "answer", State: "ENABLED"})

	// 1) 组内号码 → 组行为 answer
	r := s.ResolveByNumber("86138000005")
	if r == nil || r.GroupCode != "g1" || r.Profile == nil || r.Profile.Outcome != "ANSWER" {
		t.Fatalf("组解析错: %+v", r)
	}
	if r.Disabled {
		t.Error("ENABLED 组不应 disabled")
	}

	// 2) 个例覆盖 → busy
	s.UpsertOverride(CustomerOverride{GroupCode: "g1", Number: "86138000005", BehaviorCode: "busy", State: "ENABLED"})
	r = s.ResolveByNumber("86138000005")
	if r.Profile == nil || r.Profile.Outcome != "BUSY" {
		t.Errorf("个例覆盖未生效: %+v", r.Profile)
	}

	// 3) 个例只改状态（行为档空 → 用组行为）
	s.UpsertOverride(CustomerOverride{GroupCode: "g1", Number: "86138000006", State: "DISABLED"})
	r = s.ResolveByNumber("86138000006")
	if !r.Disabled || r.Profile == nil || r.Profile.Outcome != "ANSWER" {
		t.Errorf("仅改状态的个例错: disabled=%v profile=%+v", r.Disabled, r.Profile)
	}

	// 4) 组外号码 → nil
	if r := s.ResolveByNumber("19900000000"); r != nil {
		t.Errorf("组外号码应 nil, got %+v", r)
	}
}

func TestResolveByPortBinding(t *testing.T) {
	s := NewMemory()
	s.UpsertProfile(BehaviorProfile{Code: "ans", Outcome: "ANSWER", AnswerRatio: 100})
	s.UpsertGroup(CustomerGroup{Code: "gA", NumberPrefix: "", NumberStart: 100, Count: 10, BehaviorCode: "ans", State: "ENABLED"})
	s.UpsertBinding(LineBinding{ListenPort: 5060, LineCode: "line_mock", GroupCode: "gA", Enabled: 1})

	// 号码命中组
	if r := s.ResolveByPort(5060, "105"); r == nil || r.GroupCode != "gA" {
		t.Errorf("按 listen_port 解析错: %+v", r)
	}
	// 号码不在组但线路绑了组 → 组行为兜底
	if r := s.ResolveByPort(5060, "999"); r == nil || r.Profile == nil || r.Profile.Outcome != "ANSWER" {
		t.Errorf("端口兜底解析错: %+v", r)
	}
}

func TestResolveByPortSeparatesSameNumberAcrossGroups(t *testing.T) {
	s := NewMemory()
	s.UpsertProfile(BehaviorProfile{Code: "ans", Outcome: "ANSWER", AnswerRatio: 100})
	s.UpsertProfile(BehaviorProfile{Code: "busy", Outcome: "BUSY", HangupCode: 486, AnswerRatio: 100})
	s.UpsertGroup(CustomerGroup{Code: "gA", NumberPrefix: "861", NumberStart: 100, Count: 10, BehaviorCode: "ans", State: "ENABLED"})
	s.UpsertGroup(CustomerGroup{Code: "gB", NumberPrefix: "861", NumberStart: 100, Count: 10, BehaviorCode: "busy", State: "ENABLED"})
	s.UpsertBinding(LineBinding{ListenPort: 5060, GroupCode: "gA", Enabled: 1})
	s.UpsertBinding(LineBinding{ListenPort: 5061, GroupCode: "gB", Enabled: 1})

	if r := s.ResolveByPort(5060, "861105"); r == nil || r.GroupCode != "gA" || r.Profile.Outcome != "ANSWER" {
		t.Errorf("5060 应命中 gA/ANSWER, got %+v", r)
	}
	if r := s.ResolveByPort(5061, "861105"); r == nil || r.GroupCode != "gB" || r.Profile.Outcome != "BUSY" {
		t.Errorf("5061 应命中 gB/BUSY, got %+v", r)
	}
}

// TestHasBindingAndBoundPorts 校验端口绑定的存在性判定（绑定权威解析、启动期对账的基础）：
// 只认**启用**绑定；disabled 绑定视为无绑定（运行时回退按号/默认，而非端口绑定行为）。
func TestHasBindingAndBoundPorts(t *testing.T) {
	s := NewMemory()
	s.UpsertProfile(BehaviorProfile{Code: "ans", Outcome: "ANSWER", AnswerRatio: 100})
	s.UpsertGroup(CustomerGroup{Code: "gA", NumberStart: 100, Count: 10, BehaviorCode: "ans", State: "ENABLED"})
	s.UpsertBinding(LineBinding{ListenPort: 5060, GroupCode: "gA", Enabled: 1})
	s.UpsertBinding(LineBinding{ListenPort: 5061, GroupCode: "gA", Enabled: 0}) // 禁用

	if !s.HasBinding(5060) {
		t.Error("5060 启用绑定应判定为有绑定")
	}
	if s.HasBinding(5061) {
		t.Error("5061 禁用绑定应判定为无绑定（回退按号）")
	}
	if s.HasBinding(5062) {
		t.Error("5062 无绑定")
	}
	bound := s.BoundPorts()
	if len(bound) != 1 || bound[0] != 5060 {
		t.Errorf("BoundPorts 应只含启用端口 [5060]，got %v", bound)
	}
}

func TestResolveByLineName(t *testing.T) {
	s := NewMemory()
	s.UpsertProfile(BehaviorProfile{Code: "ans", Outcome: "ANSWER", AnswerRatio: 100})
	s.UpsertGroup(CustomerGroup{Code: "gN", NumberStart: 100, Count: 10, BehaviorCode: "ans", State: "ENABLED"})
	// 绑定用线路名 "MOCK-CB-CN"；FS 注入的 X-Line-Name 是规范化后 "mockcbcn"
	s.UpsertBinding(LineBinding{ListenPort: 5060, LineCode: "lc1", LineName: "MOCK-CB-CN", GroupCode: "gN", Enabled: 1})

	// 用规范化后的线路名解析（模拟 dialogLine 取到的 X-Line-Name）
	if r := s.ResolveByLine("mockcbcn", "999"); r == nil || r.GroupCode != "gN" {
		t.Errorf("按规范化线路名解析错: %+v", r)
	}
	// 大小写/横杠不同也应命中
	if r := s.ResolveByLine("Mock-Cb-Cn", "999"); r == nil || r.GroupCode != "gN" {
		t.Errorf("线路名规范化匹配错: %+v", r)
	}
	// 不相关线路名不命中
	if r := s.ResolveByLine("otherline", "999"); r != nil {
		t.Errorf("无关线路名不应命中: %+v", r)
	}
}

func TestNormalizeLineName(t *testing.T) {
	cases := map[string]string{
		"MOCK-CB-CN": "mockcbcn",
		"Line-A":     "linea",
		" Foo ":      "foo",
		"abc":        "abc",
	}
	for in, want := range cases {
		if got := normalizeLineName(in); got != want {
			t.Errorf("normalizeLineName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRollAnswer(t *testing.T) {
	if !RollAnswer(100) {
		t.Error("100% 应恒接")
	}
	if RollAnswer(0) {
		t.Error("0% 应恒不接")
	}
	// 70% 多次：大致在范围内（不严格）
	hit := 0
	for i := 0; i < 1000; i++ {
		if RollAnswer(70) {
			hit++
		}
	}
	if hit < 600 || hit > 800 {
		t.Errorf("70%% 接通率偏差过大: %d/1000", hit)
	}
}

func TestNumbersExpand(t *testing.T) {
	g := &CustomerGroup{NumberPrefix: "861", NumberStart: 10, Count: 3}
	got := g.Numbers(0)
	want := []string{"86110", "86111", "86112"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Numbers[%d]=%q want %q", i, got[i], want[i])
		}
	}
}
