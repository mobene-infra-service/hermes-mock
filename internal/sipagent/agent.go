// Package sipagent 用 emiago/diago 实现 SIP UAS：接收 FreeSWITCH 的 INVITE，
// 按 behavior 规则自动应答/拒接/放音/发 DTMF/挂断，替代 dialplan mock 线路。
//
// diago 调用已对照 diago v0.28.0 / sipgo v1.4.0 校准，并通过编译与启动验证
// （参考 examples/dtmf、examples/playback）。所有 diago 适配集中在本文件底部
// 「diago 适配薄封装」，升级 diago 版本时只需在那里调整。
package sipagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/sirupsen/logrus"

	"hermes-mock/internal/behavior"
	"hermes-mock/internal/calltrace"
	"hermes-mock/internal/cluster"
	"hermes-mock/internal/config"
	"hermes-mock/internal/tracelog"
)

// Agent 是被测 Hermes 线路指向的可编程 SIP 被叫端（UAS）：接收 FreeSWITCH 的 INVITE，
// 按客户集群解析出的行为应答/拒接/放音/挂断。只演客户被叫腿——不主动呼出、不桥接、不注册坐席。
type Agent struct {
	cfg     *config.Config
	cluster *cluster.Store // 客户集群（号段组+个例+线路绑定+行为档），决定被叫行为
	tracker *calltrace.Tracker
	bus     *tracelog.Bus
	ua      *sipgo.UserAgent
	dg      *diago.Diago
}

// New 初始化入站 UAS（被叫端）：只监听一个端口接收 FreeSWITCH 发来的 INVITE。
func New(cfg *config.Config, clu *cluster.Store, tracker *calltrace.Tracker, bus *tracelog.Bus) (*Agent, error) {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("hermes-mock"))
	if err != nil {
		return nil, err
	}
	dg := diago.NewDiago(ua,
		diago.WithTransport(diago.Transport{
			Transport:    cfg.SIPTransport,
			BindHost:     cfg.SIPListenIP,
			BindPort:     cfg.SIPListenPort,
			ExternalHost: cfg.ExternalIP,
		}),
		diago.WithMediaConfig(diago.MediaConfig{Codecs: codecList(cfg.Codecs)}),
	)
	return &Agent{
		cfg: cfg, cluster: clu, tracker: tracker, bus: bus, ua: ua, dg: dg,
	}, nil
}

// Run 启动 UAS，阻塞处理入站 INVITE。
func (a *Agent) Run() error {
	ctx := context.Background()
	logrus.Infof("SIP agent serving %s:%d/%s", a.cfg.SIPListenIP, a.cfg.SIPListenPort, a.cfg.SIPTransport)
	return a.dg.Serve(ctx, func(in *diago.DialogServerSession) {
		a.handleInbound(in)
	})
}

// resolveRule 解析被叫的有效行为：用客户集群（号段组+个例+线路绑定+接通率）匹配，
// 命中即转成 behavior.Rule；未命中（或无集群）用内置默认行为兜底（应答+放音）。
func (a *Agent) resolveRule(callee, line string) behavior.Rule {
	if a.cluster != nil {
		var res *cluster.Resolved
		if line != "" {
			res = a.cluster.ResolveByLine(line, callee)
		} else {
			res = a.cluster.ResolveByNumber(callee)
		}
		if res != nil && res.Profile != nil {
			return clusterToRule(res)
		}
	}
	return a.defaultRule()
}

// defaultRule 未命中任何客户组/个例时的兜底行为：按 env 默认应答并放默认音频。
func (a *Agent) defaultRule() behavior.Rule {
	return behavior.Rule{
		Outcome:  behavior.OutcomeAnswer,
		RingMs:   a.cfg.DefaultRingMs,
		TalkMs:   a.cfg.DefaultTalkMs,
		Playback: a.cfg.DefaultPlayback,
	}
}

// clusterToRule 把集群解析结果转成 behavior.Rule。
// 处理：禁用组/个例 → 不可用 503；接通率 → 随机决定本通是否接通；
// BRIDGE（桥接到坐席/第二腿）已不支持——mock 只演客户腿，降级为普通接听。
func clusterToRule(res *cluster.Resolved) behavior.Rule {
	p := res.Profile
	r := behavior.Rule{
		Outcome:    behavior.Outcome(p.Outcome),
		RingMs:     p.RingMs,
		TalkMs:     p.TalkMs,
		HangupCode: p.HangupCode,
		Playback:   p.Playback,
		DTMF:       p.DTMF,
		ExpectDTMF: p.ExpectDTMF,
		Fault:      behavior.Fault(p.Fault),
	}
	// IVR 脚本（JSON）→ rule.IVR
	if p.IVRJson != "" {
		var steps []behavior.IVRStep
		if err := json.Unmarshal([]byte(p.IVRJson), &steps); err == nil {
			r.IVR = steps
		}
	}
	if r.Outcome == behavior.OutcomeBridge {
		r.Outcome = behavior.OutcomeAnswer // 桥接已移除，降级为接听
	}
	if res.Disabled {
		r.Outcome = behavior.OutcomeUnavailable
		r.HangupCode = 503
		return r
	}
	// 接通率：ANSWER 时按 answer_ratio 随机不接（模拟接通率）
	if r.Outcome == behavior.OutcomeAnswer && !cluster.RollAnswer(p.AnswerRatio) {
		r.Outcome = behavior.OutcomeNoAnswer // 振铃不接
	}
	return r
}

// handleInbound 处理一通入站通话：按行为规则决定应答/拒接/放音/DTMF/挂断。
func (a *Agent) handleInbound(in *diago.DialogServerSession) {
	callee := dialogCallee(in)
	caller := dialogCaller(in)
	line := dialogLine(in)
	rule := a.resolveRule(callee, line)
	log := logrus.WithFields(logrus.Fields{"callee": callee, "caller": caller, "outcome": rule.Outcome})
	log.Info("收到 INVITE")
	id := a.tracker.Start(callee, caller, string(rule.Outcome))

	// 通话链路观测：传输层 SIP tracer 已自动抓真实 INVITE/180/200/BYE。
	// 聚合键与 tracer 一致：优先用 Hermes 业务 callUuid，否则退回 SIP Call-ID。
	// 这里附加「mock 客户腿业务决策(FLOW)」，与真实报文同会话。
	leg := "customer"
	_, _, callID, bizUUID := sipReqInfo(in.InviteRequest)
	aggKey := bizUUID
	if aggKey == "" {
		aggKey = callID
	}
	sess := a.bus.EnsureByCallID(aggKey, "call", "呼入 "+caller+" → "+callee+" ("+leg+")")
	a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "命中",
		"被叫="+callee+" 主叫="+caller+" 规则="+string(rule.Outcome), map[string]string{"leg": leg})

	switch rule.Outcome {
	case behavior.OutcomeReject, behavior.OutcomeBusy:
		code := orDefault(rule.HangupCode, 486)
		rejectCall(in, code, "Busy Here")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "拒接(Busy Here) 码="+itoa(code), nil)
		a.tracker.Rejected(id, code)
		log.Infof("拒接 %d", code)
		return
	case behavior.OutcomeUnavailable:
		code := orDefault(rule.HangupCode, 503)
		rejectCall(in, code, "Service Unavailable")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "不可用(Service Unavailable) 码="+itoa(code), nil)
		a.tracker.Rejected(id, code)
		log.Infof("不可用 %d", code)
		return
	case behavior.OutcomeNoAnswer:
		ringing(in)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "振铃", nil)
		sleepMs(rule.RingMs)
		code := orDefault(rule.HangupCode, 480)
		rejectCall(in, code, "Temporarily Unavailable")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "振铃不接(Temporarily Unavailable) 码="+itoa(code), nil)
		a.tracker.Rejected(id, code)
		log.Info("振铃不接")
		return
	}

	// ---- 接听分支 ----
	// 故障注入（SIP 信令级，先于正常应答处理）：
	// NO_RESPONSE：收到 INVITE 完全不响应，让对端(FS)等到超时（模拟无应答/网络黑洞）。
	if rule.Fault == behavior.FaultNoResponse {
		log.Warn("故障注入 NO_RESPONSE：对 INVITE 不作任何响应，等待对端超时")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"NO_RESPONSE：收到 INVITE 不响应（压 SIP 超时/408）", map[string]string{"fault": "NO_RESPONSE"})
		holdUntilPeerHangup(in, 32*time.Second) // 阻塞持有，不发任何响应，直到对端放弃
		a.tracker.Rejected(id, 408)
		return
	}
	// SLOW_ANSWER：先 180，拖很久才 200（压 post-dial delay / 应答慢）。
	if rule.Fault == behavior.FaultSlowAnswer {
		ringing(in)
		delay := orDefault(rule.RingMs, behavior.SlowAnswerDelayMs)
		log.Warnf("故障注入 SLOW_ANSWER：延迟 %dms 才应答", delay)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"SLOW_ANSWER：180 后延迟 "+itoa(delay)+"ms 才 200（慢应答）", map[string]string{"fault": "SLOW_ANSWER"})
		sleepMs(delay)
	} else if rule.RingMs > 0 {
		ringing(in)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "振铃", nil)
		sleepMs(rule.RingMs)
	}
	if err := answer(in); err != nil {
		log.Errorf("answer: %v", err)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "失败", "应答失败: "+err.Error(), nil)
		a.tracker.Rejected(id, 500)
		return
	}
	a.tracker.Answered(id)
	a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "应答", "决定应答（媒体建立）", nil)
	log.Info("已接听")
	// 媒体观测：记录本腿协商到的编解码（真实取自 diago 媒体会话，非手写）。
	a.emitCodec(sess, leg, in)

	// ANSWER_DROP：200 应答后立即挂断（接通即挂，压计费/媒体竞态）。
	if rule.Fault == behavior.FaultAnswerDrop {
		log.Warn("故障注入 ANSWER_DROP：200 后立即 BYE")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"ANSWER_DROP：应答后立即挂断（接通即挂）", map[string]string{"fault": "ANSWER_DROP"})
		hangup(in)
		a.tracker.Ended(id)
		return
	}

	// 脚本化 IVR 对话（放音→收按键→分支，多轮）。优先于单次 Playback/DTMF；
	// 阻塞驱动整个通话窗口，结束后直接挂断。真实媒体 + RFC4733，不做语义 ASR。
	if len(rule.IVR) > 0 && rule.Fault == behavior.FaultNone {
		a.runIVR(in, sess, leg, rule.IVR, log)
		hangup(in)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "挂断", "IVR 脚本结束，挂断", nil)
		a.tracker.Ended(id)
		return
	}
	// 放预置音频（NO_RTP/单向只收/丢帧/乱序 故障：接听后不走普通放音）。
	// 默认行为：持续循环放音（预置 WAV 或合成拨号音），让 FS 录音/坐席/监听**真能听到声音**
	// 且媒体统计 tx>0。仅在媒体类故障/IVR/收发 DTMF 等占用 AudioWriter 时跳过。
	streamStop := make(chan struct{})
	streamingAudio := false
	mediaFault := rule.Fault == behavior.FaultNoRTP || rule.Fault == behavior.FaultOneWayAudio ||
		rule.Fault == behavior.FaultRtpLoss || rule.Fault == behavior.FaultRtpReorder
	if !mediaFault && rule.DTMF == "" && !rule.ExpectDTMF {
		streamingAudio = true
		go a.streamAudio(in, rule.Playback, streamStop, log)
		a.bus.Emit(sess, leg, tracelog.ChanBridge, tracelog.DirNA, "放音",
			"持续发送音频（"+audioSource(rule.Playback, a.cfg.DefaultPlayback)+"），对端可听", nil)
	}
	// RTP_LOSS 故障：发媒体但按比例丢帧（模拟丢包/抖动，媒体降质但不中断）。
	if rule.Fault == behavior.FaultRtpLoss {
		log.Warn("故障注入 RTP_LOSS：按比例丢帧发送")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"RTP_LOSS：发媒体但丢 "+itoa(behavior.RtpLossPercentDefault)+"% 帧（丢包/抖动）", map[string]string{"fault": "RTP_LOSS"})
		go a.lossySend(in, behavior.RtpLossPercentDefault, log)
	}
	// RTP_REORDER 故障：发媒体但小窗口内乱序（模拟乱序/重排，考验对端抖动缓冲）。
	if rule.Fault == behavior.FaultRtpReorder {
		log.Warn("故障注入 RTP_REORDER：小窗口乱序发送")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"RTP_REORDER：发媒体但小窗口内乱序（乱序/重排）", map[string]string{"fault": "RTP_REORDER"})
		go a.reorderSend(in, log)
	}
	// 单向音频故障：只收不发（读对端 RTP 但不写媒体）——媒体统计应显示 tx=0。
	if rule.Fault == behavior.FaultOneWayAudio {
		log.Warn("故障注入 ONE_WAY_AUDIO：只收不发（不写媒体）")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"ONE_WAY_AUDIO：只收不发（单向音频）", map[string]string{"fault": "ONE_WAY_AUDIO"})
		go a.drainInbound(in, log) // 持续读对端 RTP 以产生 rx 统计，但本侧不发
	}
	// 接听后发送 DTMF（覆盖 OTP 验证码按键 / IVR 选择）
	if rule.DTMF != "" {
		go a.sendDTMF(in, rule.DTMF, log)
	}
	// 接听后监听对端按键（IVR 交互观测：听放音→对端按键）。与发 DTMF 互斥（共用 AudioReader/Writer）。
	if rule.ExpectDTMF && rule.DTMF == "" && rule.Fault != behavior.FaultOneWayAudio {
		go a.recvDTMF(in, sess, leg, time.Duration(orDefault(rule.TalkMs, a.cfg.DefaultTalkMs))*time.Millisecond, log)
	}

	// 通话时长到点挂断；HALF_HANGUP 故障：本侧永不发 BYE。
	sleepMs(orDefault(rule.TalkMs, a.cfg.DefaultTalkMs))
	// 媒体观测：挂断前采样本腿 RTP 实际收发包/字节（区分真有媒体 vs NO_RTP 单向）。
	a.emitRTPStats(sess, leg, in)
	if rule.Fault == behavior.FaultHalfHangup {
		// 必须阻塞 handler 不返回——diago 在回调返回后会自动挂断
		// （diago.go serve: "Always try hanguping call"）。持有对话直到
		// 对端(FS) 挂断或安全上限，真正模拟「一方不挂」的生产半挂断。
		log.Warn("故障注入 HALF_HANGUP：保持通话、本侧不发 BYE")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入", "HALF_HANGUP：本侧不发 BYE", nil)
		holdUntilPeerHangup(in, 60*time.Second)
		if streamingAudio {
			close(streamStop)
		}
		a.tracker.Ended(id)
		return
	}
	if streamingAudio {
		close(streamStop)
	}
	hangup(in)
	a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "挂断", "通话时长到，挂断", nil)
	log.Info("挂断")
	a.tracker.Ended(id)
}

// emitCodec 把本腿协商到的编解码记入链路（真实取自 diago 媒体会话）。
func (a *Agent) emitCodec(sess, leg string, in *diago.DialogServerSession) {
	codec, ok := negotiatedCodec(in)
	if !ok {
		return
	}
	a.bus.Emit(sess, leg, tracelog.ChanBridge, tracelog.DirNA, "媒体协商",
		"编解码 "+codec.Name+"（pt="+itoa(int(codec.PayloadType))+" rate="+itoa(int(codec.SampleRate))+"）",
		map[string]string{"codec": codec.Name, "payloadType": itoa(int(codec.PayloadType)), "sampleRate": itoa(int(codec.SampleRate))})
}

// emitRTPStats 采样本腿 RTP 实际收发统计（真实取自 diago RTPSession），并入链路。
// 这是「自己亲历的媒体观测」——收/发包数都 >0 说明双向媒体真的通了；
// 收=0 或 发=0 即单向/NO_RTP 故障的客观证据（不是手写摘要）。
// 丢包率由公开字段推导：期望包数=末序号-首序号+1，丢=期望-实收（真实计算，非编造）。
func (a *Agent) emitRTPStats(sess, leg string, in *diago.DialogServerSession) {
	rs, ws, ok := rtpStats(in)
	if !ok {
		return
	}
	twoWay := rs.PacketsCount > 0 && ws.PacketsCount > 0
	flow := "双向媒体已建立"
	switch {
	case rs.PacketsCount == 0 && ws.PacketsCount == 0:
		flow = "无 RTP（媒体未流动）"
	case rs.PacketsCount == 0:
		flow = "单向：仅发不收（未收到对端 RTP）"
	case ws.PacketsCount == 0:
		flow = "单向：仅收不发"
	}
	lost, lossPct := rtpLoss(rs.FirstPktSequenceNumber, rs.LastSequenceNumber, rs.PacketsCount)
	detail := map[string]string{
		"rxPackets": u64(rs.PacketsCount), "rxBytes": u64(rs.OctetCount),
		"txPackets": u64(ws.PacketsCount), "txBytes": u64(ws.OctetCount),
		"twoWay": boolStr(twoWay),
		"rxSSRC": u64(uint64(rs.SSRC)), "txSSRC": u64(uint64(ws.SSRC)),
	}
	qual := ""
	if rs.PacketsCount > 0 {
		detail["rxLostPackets"] = itoa(lost)
		detail["rxLossPercent"] = itoa(lossPct)
		qual = "｜丢包 " + itoa(lost) + "(" + itoa(lossPct) + "%)"
	}
	if rs.RTT > 0 {
		detail["rttMs"] = itoa(int(rs.RTT.Milliseconds()))
		qual += "｜RTT " + itoa(int(rs.RTT.Milliseconds())) + "ms"
	}
	a.bus.Emit(sess, leg, tracelog.ChanBridge, tracelog.DirNA, "媒体统计",
		flow+"｜收 "+u64(rs.PacketsCount)+"包/"+u64(rs.OctetCount)+"字节，发 "+u64(ws.PacketsCount)+"包/"+u64(ws.OctetCount)+"字节"+qual,
		detail)
}

func (a *Agent) playback(in *diago.DialogServerSession, file string, log *logrus.Entry) {
	path := filepath.Join(a.cfg.AudioDir, file)
	if err := playFile(in, path); err != nil {
		log.Errorf("play %s: %v", path, err)
	}
}

// streamAudio 在整个通话窗口持续向对端发送音频，使 FS 录音/坐席/监听**能真正听到声音**，
// 且媒体统计 tx>0（真实双向媒体）。优先循环放预置 WAV；读不到则回退发 350+440Hz 拨号音。
// 必须持续发到 stop——单次 PlayFile 放完即静默会让"听不到声音"。
func (a *Agent) streamAudio(in *diago.DialogServerSession, file string, stop <-chan struct{}, log *logrus.Entry) {
	aw, err := in.AudioWriter()
	if err != nil {
		log.Errorf("stream audio writer: %v", err)
		return
	}
	frames := a.audioFrames(file) // 一组 20ms PCMU 帧（WAV 或合成音）
	if len(frames) == 0 {
		return
	}
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	idx := 0
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if _, err := aw.Write(frames[idx%len(frames)]); err != nil {
				return // 对端挂断/拆桥
			}
			idx++
		}
	}
}

// audioSource 描述当前放音来源（链路展示用）。
func audioSource(file, def string) string {
	if file == "" {
		file = def
	}
	if file == "" {
		return "合成拨号音"
	}
	return file
}

func (a *Agent) audioFrames(file string) [][]byte {
	if file == "" {
		file = a.cfg.DefaultPlayback
	}
	if file != "" {
		if frames := loadPCMUFrames(filepath.Join(a.cfg.AudioDir, file)); len(frames) > 0 {
			return frames
		}
	}
	return dialToneFrames() // 回退：合成可听拨号音（350+440Hz），保证一定有声音
}

func (a *Agent) sendDTMF(in *diago.DialogServerSession, digits string, log *logrus.Entry) {
	// diago 的 RTP DTMF 直接写 packetWriter，但需要 RTP 在流动。这里用静音帧
	// 驱动 RTP 时钟（PCMU 静音 = 0xFF），DTMFWriter 链式拦截：WriteDTMF 时会
	// 锁住静音写入并注入 RFC4733 telephone-event。与 PlaybackCreate 二选一。
	dtmfW := &diago.DTMFWriter{}
	aw, err := in.AudioWriter(diago.WithAudioWriterDTMF(dtmfW))
	if err != nil {
		log.Errorf("dtmf audio writer: %v", err)
		return
	}
	stop := make(chan struct{})
	go func() {
		silence := make([]byte, 160) // 20ms @ 8kHz PCMU
		for i := range silence {
			silence[i] = 0xFF
		}
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_, _ = aw.Write(silence)
			}
		}
	}()
	time.Sleep(400 * time.Millisecond) // 先发静音建立 RTP 流
	for _, d := range digits {
		if err := dtmfW.WriteDTMF(d); err != nil {
			log.Errorf("dtmf %c: %v", d, err)
			break
		}
		log.Infof("发送 DTMF %c", d)
		time.Sleep(300 * time.Millisecond)
	}
	close(stop)
}

// drainInbound 持续读对端 RTP 但本侧不写媒体——用于 ONE_WAY_AUDIO「只收不发」故障：
// 产生真实的 rx 统计（证明对端在发），而 tx 为 0（本侧静默），媒体观测据此判定单向。
func (a *Agent) drainInbound(in *diago.DialogServerSession, log *logrus.Entry) {
	r, err := in.AudioReader()
	if err != nil {
		log.Errorf("one-way audio reader: %v", err)
		return
	}
	buf := make([]byte, 320)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.Read(buf); err != nil {
			return // 对端停发/挂断
		}
	}
}

// lossySend 发送媒体但按 lossPct% 丢帧——模拟 RTP 丢包/抖动：媒体仍在流动（tx>0）
// 但有规律地跳过部分帧，对端听到卡顿/缺损。用确定性计数器决定丢哪帧（每 N 帧丢 1）。
func (a *Agent) lossySend(in *diago.DialogServerSession, lossPct int, log *logrus.Entry) {
	aw, err := in.AudioWriter()
	if err != nil {
		log.Errorf("rtp-loss audio writer: %v", err)
		return
	}
	if lossPct <= 0 {
		lossPct = behavior.RtpLossPercentDefault
	}
	if lossPct > 90 {
		lossPct = 90
	}
	// 每 step 帧丢 1 帧（step = 100/lossPct，近似丢包率）。
	step := 100 / lossPct
	if step < 2 {
		step = 2
	}
	silence := make([]byte, 160) // 20ms @ 8kHz PCMU
	for i := range silence {
		silence[i] = 0xFF
	}
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	deadline := time.Now().Add(60 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		<-t.C
		n++
		if n%step == 0 {
			continue // 丢这一帧
		}
		if _, err := aw.Write(silence); err != nil {
			return
		}
	}
}

// reorderSend 发送媒体但小窗口（2 帧）内乱序：缓冲一帧，与下一帧交换次序后写出，
// 模拟 RTP 乱序/重排（对端抖动缓冲需重排序）。媒体仍在流动（tx>0），但帧序被打乱。
func (a *Agent) reorderSend(in *diago.DialogServerSession, log *logrus.Entry) {
	aw, err := in.AudioWriter()
	if err != nil {
		log.Errorf("rtp-reorder audio writer: %v", err)
		return
	}
	// 用可区分的填充值标记不同帧（便于对端/抓包观察乱序），实际是静音级 payload。
	mk := func(b byte) []byte {
		f := make([]byte, 160)
		for i := range f {
			f[i] = b
		}
		return f
	}
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	deadline := time.Now().Add(60 * time.Second)
	var n int
	var held []byte // 暂存的前一帧（窗口）
	for time.Now().Before(deadline) {
		<-t.C
		cur := mk(byte(0xF0 + n%8))
		n++
		if held == nil {
			held = cur // 缓冲，下一拍交换写出
			continue
		}
		// 先写当前帧，再写被缓冲的前一帧 → 这两帧次序被交换（乱序）。
		if _, err := aw.Write(cur); err != nil {
			return
		}
		if _, err := aw.Write(held); err != nil {
			return
		}
		held = nil
	}
}

// recvDTMF 接听后监听对端按键（RFC4733），收到即记入链路——IVR 交互观测：
// 「放音→对端按键」中对端那一侧的真实 DTMF。dur 为监听时长。
func (a *Agent) recvDTMF(in *diago.DialogServerSession, sess, leg string, dur time.Duration, log *logrus.Entry) {
	dtmfR := &diago.DTMFReader{}
	if _, err := in.AudioReader(diago.WithAudioReaderDTMF(dtmfR)); err != nil {
		log.Errorf("dtmf reader: %v", err)
		return
	}
	err := dtmfR.Listen(func(d rune) error {
		log.Infof("收到对端 DTMF %c", d)
		a.bus.Emit(sess, leg, tracelog.ChanBridge, tracelog.DirIn, "DTMF",
			"收到对端按键 "+string(d), map[string]string{"digit": string(d)})
		return nil
	}, dur)
	if err != nil {
		log.Debugf("dtmf listen 结束: %v", err)
	}
}

// runIVR 执行脚本化 IVR 对话：从首步开始，每步「放提示音 → 等对端按键 → 按 branch 跳转」，
// 直到 HANGUP / 无下一步 / 步数上限。每步的放音、收键、跳转都记入链路（可观测多轮交互）。
// 真实媒体（放 WAV）+ RFC4733 收键，不做语义 ASR。
func (a *Agent) runIVR(in *diago.DialogServerSession, sess, leg string, steps []behavior.IVRStep, log *logrus.Entry) {
	byID := map[string]behavior.IVRStep{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	cur := steps[0]
	for i := 0; i < 20; i++ { // 步数上限，防脚本环
		a.bus.Emit(sess, leg, tracelog.ChanBridge, tracelog.DirOut, "IVR放音",
			"IVR 步骤["+cur.ID+"] 放提示音 "+cur.Prompt, map[string]string{"step": cur.ID, "prompt": cur.Prompt})
		if cur.Prompt != "" {
			a.playback(in, cur.Prompt, log) // 阻塞放完
		}
		// 进入本步可选先发 DTMF（模拟机器人侧按键）
		if cur.SendDTMF != "" {
			a.sendDTMF(in, cur.SendDTMF, log)
		}
		// 等对端按键
		wait := time.Duration(orDefault(cur.WaitMs, 5000)) * time.Millisecond
		key := a.listenOneDTMF(in, wait, log)
		next, hangupNow := nextIVRStep(cur, key)
		if key != "" {
			a.bus.Emit(sess, leg, tracelog.ChanBridge, tracelog.DirIn, "IVR按键",
				"IVR 步骤["+cur.ID+"] 收到按键 "+key+" → "+next, map[string]string{"step": cur.ID, "digit": key, "next": next})
		} else {
			a.bus.Emit(sess, leg, tracelog.ChanBridge, tracelog.DirNA, "IVR超时",
				"IVR 步骤["+cur.ID+"] 未按键 → "+next, map[string]string{"step": cur.ID, "next": next})
		}
		if hangupNow || next == "" {
			return
		}
		nxt, ok := byID[next]
		if !ok {
			a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "IVR结束", "下一步 "+next+" 不存在，结束", nil)
			return
		}
		cur = nxt
	}
	a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "IVR结束", "达到步数上限，结束", nil)
}

// nextIVRStep 纯函数：按收到的按键决定下一步 ID 与是否挂断。
// 按键命中 branch → 对应目标（"HANGUP" 表示挂断）；无键 → OnNoKey（空=挂断）。
func nextIVRStep(step behavior.IVRStep, key string) (next string, hangup bool) {
	if key != "" {
		if target, ok := step.Branch[key]; ok {
			if target == "HANGUP" {
				return "", true
			}
			return target, false
		}
		// 按了未映射的键：留在原步重试（返回自身）
		return step.ID, false
	}
	// 超时无键
	if step.OnNoKey == "" || step.OnNoKey == "HANGUP" {
		return "", true
	}
	return step.OnNoKey, false
}

// listenOneDTMF 监听一次对端按键，返回首个按键（超时返回空）。
func (a *Agent) listenOneDTMF(in *diago.DialogServerSession, wait time.Duration, log *logrus.Entry) string {
	dtmfR := &diago.DTMFReader{}
	if _, err := in.AudioReader(diago.WithAudioReaderDTMF(dtmfR)); err != nil {
		log.Errorf("ivr dtmf reader: %v", err)
		return ""
	}
	got := make(chan string, 1)
	go func() {
		_ = dtmfR.Listen(func(d rune) error {
			select {
			case got <- string(d):
			default:
			}
			return nil
		}, wait)
	}()
	select {
	case k := <-got:
		return k
	case <-time.After(wait):
		return ""
	}
}

func sleepMs(ms int) {
	if ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func itoa(v int) string { return strconv.Itoa(v) }

func u64(v uint64) string { return strconv.FormatUint(v, 10) }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ===========================================================================
// diago 适配薄封装：集中所有 diago 调用，按实际 diago 版本在此处统一校准。
// ===========================================================================

// sipReqInfo 从真实 SIP 请求提取：所有头（含 Hermes 注入的 X- 业务头）、原始报文、Call-ID。
// 这是「真实 SIP 报文观测」的核心——区别于手写摘要。req 为 nil 时返回空。
func sipReqInfo(req *sip.Request) (headers []tracelog.HeaderKV, raw, callID, bizUUID string) {
	if req == nil {
		return nil, "", "", ""
	}
	for _, h := range req.Headers() {
		name := h.Name()
		val := h.Value()
		headers = append(headers, tracelog.HeaderKV{Name: name, Value: val})
		if bizUUID == "" {
			switch strings.ToLower(name) {
			case "x-call-uuid", "x-callid", "x-jcallid", "x-call-id", "x-session-id", "x-session_id":
				bizUUID = val
			}
		}
	}
	raw = req.String()
	if cid := req.CallID(); cid != nil {
		callID = cid.Value()
	}
	return headers, raw, callID, bizUUID
}

// dialogCallee 取被叫号码。对照 diago v0.28.0
func dialogCallee(in *diago.DialogServerSession) string { return in.ToUser() }

// dialogCaller 取主叫号码。对照 diago v0.28.0
func dialogCaller(in *diago.DialogServerSession) string { return in.FromUser() }

// codecList 把配置的编解码名（PCMU,PCMA,opus）转成 diago 媒体编解码列表，供 SDP 协商。
// 未识别的名跳过；为空回退 PCMU+PCMA（电话网最稳）。
func codecList(spec string) []media.Codec {
	var out []media.Codec
	for _, name := range strings.Split(spec, ",") {
		switch strings.ToUpper(strings.TrimSpace(name)) {
		case "PCMU", "ULAW", "G711U":
			out = append(out, media.CodecAudioUlaw)
		case "PCMA", "ALAW", "G711A":
			out = append(out, media.CodecAudioAlaw)
		case "OPUS":
			out = append(out, media.CodecAudioOpus)
		}
	}
	if len(out) == 0 {
		out = []media.Codec{media.CodecAudioUlaw, media.CodecAudioAlaw}
	}
	return out
}

// negotiatedCodec 取本腿协商到的音频编解码（真实取自 diago 媒体会话）。 应答后才有媒体会话；无则返回 ok=false（不伪造）。对照 diago v0.28.0。
func negotiatedCodec(in *diago.DialogServerSession) (media.Codec, bool) {
	ms := in.MediaSession()
	if ms == nil {
		return media.Codec{}, false
	}
	return media.CodecAudioFromSession(ms), true
}

// rtpStats 取本腿 RTP 实际收发统计（真实取自 diago RTPSession）。
// 无 RTP 会话（未应答/无媒体）返回 ok=false。对照 diago v0.28.0。
func rtpStats(in *diago.DialogServerSession) (media.RTPReadStats, media.RTPWriteStats, bool) {
	rs := in.RTPSession()
	if rs == nil {
		return media.RTPReadStats{}, media.RTPWriteStats{}, false
	}
	return rs.ReadStats(), rs.WriteStats(), true
}

// rtpLoss 由 RTP 公开序号字段推导丢包：期望 = 末序号-首序号+1（处理 16bit 回绕），
// 丢 = 期望-实收，丢包率% = 丢/期望*100。纯函数，便于单测。received 为实收包数。
func rtpLoss(firstSeq, lastSeq uint16, received uint64) (lost, lossPercent int) {
	if received == 0 {
		return 0, 0
	}
	// 序号回绕：lastSeq < firstSeq 时加一个 16bit 周期。
	span := int(lastSeq) - int(firstSeq)
	if span < 0 {
		span += 1 << 16
	}
	expected := span + 1
	if expected <= 0 {
		return 0, 0
	}
	lost = expected - int(received)
	if lost < 0 {
		lost = 0 // 收到比期望多（重复/乱序计入）时不算丢
	}
	lossPercent = lost * 100 / expected
	return lost, lossPercent
}

// dialogLine 推断线路标识：从 INVITE 的 X-Line-Name 业务头取（FS 经 sip_h_x-line-name 注入，
// 对照 Hermes CommonConstant.CALL_X_LINE_NAME）。取不到回退空（走号码/默认匹配）。
// 有了它，cluster 的「线路→客户组绑定」(ResolveByLine) 才能真正生效。
func dialogLine(in *diago.DialogServerSession) string {
	if in == nil || in.InviteRequest == nil {
		return ""
	}
	for _, name := range []string{"X-Line-Name", "x-line-name"} {
		if h := in.InviteRequest.GetHeader(name); h != nil {
			if v := h.Value(); v != "" {
				return v
			}
		}
	}
	return ""
}

// ringing 回 180 Ringing。对照 diago v0.28.0
func ringing(in *diago.DialogServerSession) { _ = in.Ringing() }

// answer 回 200 OK 并建立媒体会话。对照 diago v0.28.0
func answer(in *diago.DialogServerSession) error { return in.Answer() }

// rejectCall 以指定 SIP 码拒接（如 486/503/480）。对照 diago v0.28.0
func rejectCall(in *diago.DialogServerSession, code int, reason string) {
	_ = in.Respond(code, reason, nil)
}

// hangup 主动挂断（发 BYE）。对照 diago v0.28.0
func hangup(in *diago.DialogServerSession) { _ = in.Hangup(context.Background()) }

// holdUntilPeerHangup 阻塞当前 Serve handler，直到对端挂断（dialog 上下文取消）或到达
// maxHold 上限，期间本侧不发 BYE——用于模拟「半挂断」故障。必须阻塞：diago 在 handler
// 返回后会自动挂断（diago.go serve: "Always try hanguping call"），返回即破坏该故障语义。
func holdUntilPeerHangup(in *diago.DialogServerSession, maxHold time.Duration) {
	t := time.NewTimer(maxHold)
	defer t.Stop()
	select {
	case <-in.Context().Done():
	case <-t.C:
	}
}

// playFile 接听后放一段预置音频（diago: PlaybackCreate().PlayFile）。对照 diago v0.28.0
func playFile(in *diago.DialogServerSession, path string) error {
	pb, err := in.PlaybackCreate()
	if err != nil {
		return err
	}
	_, err = pb.PlayFile(path)
	return err
}
