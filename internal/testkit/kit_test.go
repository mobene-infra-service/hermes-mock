package testkit

import (
	"testing"
	"time"

	"hermes-mock/internal/config"
	"hermes-mock/internal/tracelog"
)

// sessionMatchesCallee：标题命中 / 事件 leg 命中 / Detail 命中 / agent: 前缀命中。
func TestSessionMatchesCallee(t *testing.T) {
	s := &tracelog.Session{
		Title: "呼入 8613 → 600 (customer)",
		Events: []tracelog.Event{
			{Leg: "agent:5002"},
			{Detail: map[string]string{"callee": "700"}},
		},
	}
	cases := map[string]bool{
		"600":  true,  // 标题命中
		"5002": true,  // 事件 leg agent:5002 命中
		"700":  true,  // Detail.callee 命中
		"123":  false, // 不命中
		"":     false, // 空号
	}
	for num, want := range cases {
		if got := sessionMatchesCallee(s, num); got != want {
			t.Errorf("sessionMatchesCallee(%q)=%v, want %v", num, got, want)
		}
	}
}

func TestLegInviteEvidenceRequiresInvite200(t *testing.T) {
	s := &tracelog.Session{ID: "s1", Events: []tracelog.Event{
		{Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirIn, Method: "INVITE"},
		{Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirOut, Method: "180"},
		{Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirIn, Method: "CANCEL"},
		{Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirOut, Method: "200", Headers: []tracelog.HeaderKV{{Name: "CSeq", Value: "1 CANCEL"}}},
		{Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirOut, Method: "487", Headers: []tracelog.HeaderKV{{Name: "CSeq", Value: "1 INVITE"}}},
	}}
	ev := legInviteEvidenceFromSession(s, "6203")
	if ev.Answered {
		t.Fatal("CANCEL 的 200 OK 不能算 INVITE 接通")
	}
	if !ev.Failed {
		t.Fatalf("CANCEL/487 应判失败, got %+v", ev)
	}
}

func TestLegInviteEvidenceAcceptsInvite200(t *testing.T) {
	s := &tracelog.Session{ID: "s1", Events: []tracelog.Event{
		{Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirIn, Method: "INVITE"},
		{Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirOut, Method: "180"},
		{Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirOut, Method: "200", Headers: []tracelog.HeaderKV{{Name: "CSeq", Value: "1 INVITE"}}},
	}}
	ev := legInviteEvidenceFromSession(s, "6203")
	if !ev.Answered || ev.Failed {
		t.Fatalf("INVITE 的 200 OK 应判接通, got %+v", ev)
	}
}

func TestIsRejectCode(t *testing.T) {
	for _, m := range []string{"486", "503", "480", "600"} {
		if !isRejectCode(m) {
			t.Errorf("%s 应为拒接码", m)
		}
	}
	for _, m := range []string{"200", "180", "INVITE", "BYE", "1xx"} {
		if isRejectCode(m) {
			t.Errorf("%s 不应为拒接码", m)
		}
	}
}

// computeMetrics 聚合压测指标：通过率/平均/最快/最慢/P90。
func TestComputeMetrics(t *testing.T) {
	runs := []Run{
		{OK: true, DurationMs: 100},
		{OK: true, DurationMs: 200},
		{OK: false, DurationMs: 300},
		{OK: true, DurationMs: 400},
		{OK: true, DurationMs: 1000}, // 慢尾
	}
	m := computeMetrics(runs, true)
	if m.PassRate != 80 { // 4/5
		t.Errorf("PassRate=%d, want 80", m.PassRate)
	}
	if m.MinDurMs != 100 || m.MaxDurMs != 1000 {
		t.Errorf("min/max=%d/%d, want 100/1000", m.MinDurMs, m.MaxDurMs)
	}
	if m.AvgDurMs != 400 { // (100+200+300+400+1000)/5=400
		t.Errorf("Avg=%d, want 400", m.AvgDurMs)
	}
	if m.P90DurMs != 1000 { // ceil(0.9*5)=5 → 第5个=1000
		t.Errorf("P90=%d, want 1000", m.P90DurMs)
	}
	if !m.Concurrent {
		t.Error("Concurrent 应为 true")
	}
	// 空输入不 panic
	if e := computeMetrics(nil, false); e.PassRate != 0 || e.AvgDurMs != 0 {
		t.Errorf("空输入应全 0, got %+v", e)
	}
}

// fakeBiz 模拟业务编排器：记录调用 + 触发后把「客户腿」注入 bus（模拟 FS 拨号到 mock）。
type fakeBiz struct {
	bus       *tracelog.Bus
	injectLeg string
	ccCalled  bool
	cbCalled  bool
	acCalled  bool
	otpCalled bool
}

func (f *fakeBiz) CallCenterTask(orgCode, name string, numbers, agentGroups []string, ttsCode, ttsText string, proportion int, startDate, endDate string, dialTimePeriod []string, lineType string, autoStart bool) ([]byte, error) {
	f.ccCalled = true
	f.inject(numbers)
	return []byte(`{"data":"TASK1"}`), nil
}
func (f *fakeBiz) CallBotTask(name string, taskType int, numbers []string, robot, script string) ([]byte, error) {
	f.cbCalled = true
	f.inject(numbers)
	return []byte(`{"data":"BOT1"}`), nil
}
func (f *fakeBiz) AutoCall(templateCode string, numbers []string) ([]byte, error) {
	f.acCalled = true
	f.inject(numbers)
	return []byte(`{"ok":true}`), nil
}
func (f *fakeBiz) OTP(to, templateCode string, params map[string]string) ([]byte, error) {
	f.otpCalled = true
	f.inject([]string{to})
	return []byte(`{"ok":true}`), nil
}
func (f *fakeBiz) inject(numbers []string) {
	if f.injectLeg == "" || len(numbers) == 0 {
		return
	}
	// 把客户腿注入：标题含被叫号，模拟 FS 拨号到 mock 后的入站腿会话；
	// 含 INVITE + 200 OK，模拟 mock 被叫腿应答接通（新断言 waitAnyLegInviteOK 要求 INVITE 200）。
	sid := f.bus.OpenSession("call", "呼入 → "+numbers[0])
	to := []tracelog.HeaderKV{{Name: "To", Value: "<sip:" + numbers[0] + "@mock>"}}
	f.bus.EmitSIP(sid, numbers[0], tracelog.DirIn, "INVITE", "收自 FS",
		to, "INVITE", "cid-"+numbers[0], "fs:5080", "mock:5060")
	f.bus.EmitSIP(sid, numbers[0], tracelog.DirOut, "200", "mock 应答接通",
		[]tracelog.HeaderKV{{Name: "CSeq", Value: "1 INVITE"}}, "200", "cid-"+numbers[0], "mock:5060", "fs:5080")
}

// finish: Optional 步骤即使失败也不拉低 run 总体 ok（坐席腿本地走不通，不应判整条 run 失败）。
func TestFinishOptionalStepDoesNotFailRun(t *testing.T) {
	k := New(&config.Config{}, nil, tracelog.New(), nil)
	// 必做步骤全通过 + 一个失败的 Optional 步骤 → run 应仍 OK
	r := Run{Steps: []Step{
		{Name: "创建并启动", OK: true},
		{Name: "客户腿接通", OK: true},
		{Name: "坐席腿接通", OK: false, Optional: true},
	}}
	out := k.finish(r, time.Now())
	if !out.OK {
		t.Errorf("Optional 步骤失败不应判 run 失败, got OK=%v steps=%+v", out.OK, out.Steps)
	}
	// 必做步骤失败 → run 必须失败（Optional 不掩盖必做失败）
	r2 := Run{Steps: []Step{
		{Name: "客户腿接通", OK: false},
		{Name: "坐席腿接通", OK: true, Optional: true},
	}}
	if k.finish(r2, time.Now()).OK {
		t.Error("必做步骤失败时 run 应失败")
	}
}

// 群呼任务用例：触发业务接口 + 观测到客户腿进 mock → 用例通过。
func TestRunCallCenterTaskObserved(t *testing.T) {
	bus := tracelog.New()
	k := New(&config.Config{}, nil, bus, nil)
	fb := &fakeBiz{bus: bus, injectLeg: "8613800000001"}
	k.SetBizCaller(fb)
	r := k.RunCallCenterTaskObserved(CallCenterTaskParams{
		OrgCode: "ORG1", Name: "t1", Numbers: []string{"8613800000001"}, AgentGroups: []string{"agA"}, WaitSec: 3,
	})
	if !fb.ccCalled {
		t.Error("应调用业务群呼接口")
	}
	if !r.OK {
		t.Errorf("观测到客户腿应通过, steps=%+v", r.Steps)
	}
}

// 未注入业务编排器时应明确失败（不静默通过）。
func TestRunCallCenterTaskNoBiz(t *testing.T) {
	k := New(&config.Config{}, nil, tracelog.New(), nil)
	r := k.RunCallCenterTaskObserved(CallCenterTaskParams{Name: "t", Numbers: []string{"x"}})
	if r.OK {
		t.Error("未注入编排器应失败")
	}
}

// 自动外呼用例：触发 + 观测客户腿。
func TestRunAutoCallObserved(t *testing.T) {
	bus := tracelog.New()
	k := New(&config.Config{}, nil, bus, nil)
	fb := &fakeBiz{bus: bus, injectLeg: "8613900000002"}
	k.SetBizCaller(fb)
	r := k.RunAutoCallObserved(AutoCallParams{TemplateCode: "TPL", Numbers: []string{"8613900000002"}, WaitSec: 3})
	if !fb.acCalled {
		t.Error("应调用自动外呼接口")
	}
	if !r.OK {
		t.Errorf("观测到客户腿应通过, steps=%+v", r.Steps)
	}
}

// call-bot 任务用例：创建任务 + 观测客户腿。
func TestRunCallBotTaskObserved(t *testing.T) {
	bus := tracelog.New()
	k := New(&config.Config{}, nil, bus, nil)
	fb := &fakeBiz{bus: bus, injectLeg: "8613900000004"}
	k.SetBizCaller(fb)
	r := k.RunCallBotTaskObserved(CallBotTaskParams{Name: "bot", TaskType: 2, Numbers: []string{"8613900000004"}, WaitSec: 3})
	if !fb.cbCalled {
		t.Error("应调用 call-bot 任务接口")
	}
	if !r.OK || len(r.Calls) == 0 {
		t.Errorf("观测到客户腿应通过并返回 calls, run=%+v", r)
	}
}

// OTP 用例：Hermes 下发语音验证码 + mock 观测客户腿。
func TestRunOTPObserved(t *testing.T) {
	bus := tracelog.New()
	k := New(&config.Config{}, nil, bus, nil)
	fb := &fakeBiz{bus: bus, injectLeg: "8613900000003"}
	k.SetBizCaller(fb)
	r := k.RunOTPObserved(OTPParams{To: "8613900000003", TemplateCode: "OTP", WaitSec: 3})
	if !fb.otpCalled {
		t.Error("应调用 OTP 接口")
	}
	if !r.OK {
		t.Errorf("观测到客户腿应通过, steps=%+v", r.Steps)
	}
}

// OTP 批量用例：客户组/号码展开后逐通聚合。
func TestRunOTPBatchObserved(t *testing.T) {
	bus := tracelog.New()
	k := New(&config.Config{}, nil, bus, nil)
	fb := &fakeBiz{bus: bus, injectLeg: "8613900000005"}
	k.SetBizCaller(fb)
	r := k.RunOTPBatchObserved(OTPBatchParams{Numbers: []string{"8613900000005"}, TemplateCode: "OTP", WaitSec: 3})
	if r.Total != 1 || r.Passed != 1 || len(r.Calls) != 1 {
		t.Errorf("批量 OTP 聚合错误: %+v", r)
	}
}

// ---- 换线重试观测原语（47bf482 / b90673c）----

// injectInvite 注入一次完整的 INVITE 观测：带 X-Line-Name + SIP Call-ID 的入站 INVITE 与最终响应。
func injectInvite(bus *tracelog.Bus, number, lineName, sipCallID, finalCode string) {
	sid := bus.OpenSession("call", "呼入 → "+number)
	bus.EmitSIP(sid, number, tracelog.DirIn, "INVITE", "收自 FS",
		[]tracelog.HeaderKV{
			{Name: "To", Value: "<sip:" + number + "@mock>"},
			{Name: "Call-ID", Value: sipCallID},
			{Name: "X-Line-Name", Value: lineName},
		}, "INVITE", "biz-"+number, "fs:5080", "mock:5060")
	bus.EmitSIP(sid, number, tracelog.DirOut, finalCode, "mock 最终响应",
		[]tracelog.HeaderKV{
			{Name: "CSeq", Value: "1 INVITE"},
			{Name: "Call-ID", Value: sipCallID},
		}, finalCode, "biz-"+number, "mock:5060", "fs:5080")
}

// collectInvitesForCallee：按 SIP Call-ID 去重聚合（业务 callUuid 复用时仍须分开）、
// 提取 X-Line-Name 与最终响应码、时间升序。
func TestCollectInvitesForCallee(t *testing.T) {
	bus := tracelog.New()
	k := New(&config.Config{}, nil, bus, nil)
	start := time.Now().Add(-time.Second)
	num := "8613800000077"
	injectInvite(bus, num, "line-base-a", "cid-1", "486") // 首呼：拒接线失败
	injectInvite(bus, num, "line-base-c", "cid-2", "200") // 重拨：换线接通
	obs := k.collectInvitesForCallee(start, num)
	if len(obs) != 2 {
		t.Fatalf("应观测到 2 次 INVITE, got %d: %+v", len(obs), obs)
	}
	if obs[0].LineName != "line-base-a" || obs[0].FinalCode != "486" {
		t.Errorf("首呼观测错误: %+v", obs[0])
	}
	if obs[1].LineName != "line-base-c" || obs[1].FinalCode != "200" {
		t.Errorf("重拨观测错误: %+v", obs[1])
	}
	if lines := distinctLineNames(obs); len(lines) != 2 {
		t.Errorf("应观测到 2 条互异线路, got %v", lines)
	}
	if !allCallIDsDistinct(obs) {
		t.Error("两次拨号 Call-ID 互异应判 true")
	}
	if !anyAnswered(obs) {
		t.Error("有 200 应判接通")
	}
}

// 同一线路重拨（修复前的 bug 形态）：线路名不互异 → distinctLineNames 只有 1 条。
func TestCollectInvitesSameLineNotDistinct(t *testing.T) {
	bus := tracelog.New()
	k := New(&config.Config{}, nil, bus, nil)
	start := time.Now().Add(-time.Second)
	num := "8613800000078"
	injectInvite(bus, num, "line-base-a", "cid-x1", "486")
	injectInvite(bus, num, "line-base-a", "cid-x2", "486") // 一线多号：重拨仍走同线（47bf482 修复前）
	obs := k.collectInvitesForCallee(start, num)
	if len(obs) != 2 {
		t.Fatalf("应观测到 2 次 INVITE, got %d", len(obs))
	}
	if lines := distinctLineNames(obs); len(lines) != 1 {
		t.Errorf("同线重拨线路应只有 1 条, got %v", lines)
	}
	if anyAnswered(obs) {
		t.Error("全 486 不应判接通")
	}
}

// Call-ID 复用（b90673c 修复前的回归形态）：allCallIDsDistinct 应为 false。
func TestAllCallIDsDistinctDetectsReuse(t *testing.T) {
	obs := []InviteObservation{{CallID: "same"}, {CallID: "same"}}
	if allCallIDsDistinct(obs) {
		t.Error("Call-ID 复用应判 false")
	}
}
