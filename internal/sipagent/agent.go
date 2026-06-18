// Package sipagent 用 emiago/diago 实现 SIP UAS：接收 FreeSWITCH 的 INVITE，
// 按 behavior 规则自动应答/拒接/放音/发 DTMF/挂断，替代 dialplan mock 线路。
//
// diago 调用已对照 diago v0.28.0 / sipgo v1.4.0 校准，并通过编译与启动验证
// （参考 examples/dtmf、examples/playback）。所有 diago 适配集中在本文件底部
// 「diago 适配薄封装」，升级 diago 版本时只需在那里调整。
package sipagent

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
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
	cfg        *config.Config
	listenPort int
	cluster    *cluster.Store // 客户集群（号段组+个例+端口绑定+行为档），决定被叫行为
	tracker    *calltrace.Tracker
	bus        *tracelog.Bus
	dg         *diago.Diago
}

// New 初始化入站 UAS（被叫端）：只监听一个端口接收 FreeSWITCH 发来的 INVITE。
func New(cfg *config.Config, clu *cluster.Store, tracker *calltrace.Tracker, bus *tracelog.Bus) (*Agent, error) {
	return NewOnPort(cfg, cfg.SIPListenPort, clu, tracker, bus)
}

// NewOnPort 初始化指定入口端口的入站 UAS。
func NewOnPort(cfg *config.Config, listenPort int, clu *cluster.Store, tracker *calltrace.Tracker, bus *tracelog.Bus) (*Agent, error) {
	uaOptions := []sipgo.UserAgentOption{sipgo.WithUserAgent("hermes-mock")}
	if cfg.SIPResponseToSource {
		uaOptions = append(uaOptions, sipgo.WithUserAgentTransportLayerOptions(
			sip.WithTransportLayerReadFilter(addTopViaSourceParams),
		))
	}
	ua, err := sipgo.NewUA(uaOptions...)
	if err != nil {
		return nil, err
	}
	dg := diago.NewDiago(ua,
		diago.WithTransport(diago.Transport{
			Transport:    cfg.SIPTransport,
			BindHost:     cfg.SIPListenIP,
			BindPort:     listenPort,
			ExternalHost: cfg.ExternalIP,
		}),
		diago.WithMediaConfig(diago.MediaConfig{Codecs: codecList(cfg.Codecs)}),
	)
	return &Agent{
		cfg: cfg, listenPort: listenPort, cluster: clu, tracker: tracker, bus: bus, dg: dg,
	}, nil
}

// Run 启动 UAS，阻塞处理入站 INVITE。
func (a *Agent) Run() error {
	ctx := context.Background()
	logrus.Infof("SIP agent serving %s:%d/%s", a.cfg.SIPListenIP, a.listenPort, a.cfg.SIPTransport)
	return a.dg.Serve(ctx, func(in *diago.DialogServerSession) {
		a.handleInbound(in)
	})
}

// resolveRule 解析被叫的有效行为。优先级与可见性（便于排查「绑了端口却不生效」）：
//  1. 入口端口有**启用绑定** → 绑定权威：只按绑定客户组(+组内/全局个例)解析，**不回退**按号串到别的组；
//     绑定命中端口但组/行为档缺失 → 明确 WARN + 默认兜底（而非悄悄按号匹配别的组）。
//  2. 端口无绑定 → 按号段/个例解析；命中即用。
//  3. 都未命中 → 默认兜底（应答+放音），并 WARN 提示该端口未绑定/号未命中。
func (a *Agent) resolveRule(callee string) behavior.Rule {
	if a.cluster == nil {
		return a.defaultRule()
	}
	if a.listenPort > 0 && a.cluster.HasBinding(a.listenPort) {
		res := a.cluster.ResolveByPort(a.listenPort, callee)
		if res != nil && res.Profile != nil {
			logrus.WithFields(logrus.Fields{"listenPort": a.listenPort, "callee": callee, "group": res.GroupCode, "outcome": res.Profile.Outcome, "source": "port-binding"}).Info("行为解析命中端口绑定")
			return clusterToRule(res)
		}
		logrus.WithFields(logrus.Fields{"listenPort": a.listenPort, "callee": callee}).Warn("端口已绑定但客户组或行为档缺失，回退默认兜底（检查该组是否存在、组 behaviorCode 是否指向有效行为档）")
		return a.defaultRule()
	}
	if res := a.cluster.ResolveByNumber(callee); res != nil && res.Profile != nil {
		logrus.WithFields(logrus.Fields{"listenPort": a.listenPort, "callee": callee, "group": res.GroupCode, "outcome": res.Profile.Outcome, "source": "number"}).Info("行为解析按号命中客户组")
		return clusterToRule(res)
	}
	if a.listenPort > 0 {
		logrus.WithFields(logrus.Fields{"listenPort": a.listenPort, "callee": callee}).Warn("入口端口未绑定且号未命中任何客户组，回退默认兜底（确认该端口已在 cluster 页绑定客户组、且该端口在 SIP_LISTEN_PORTS 中）")
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
	rule := a.resolveRule(callee)
	log := logrus.WithFields(logrus.Fields{"callee": callee, "caller": caller, "listenPort": a.listenPort, "line": line, "outcome": rule.Outcome})
	log.Info("收到 INVITE")
	logSIPRoute("INVITE 入站路由诊断", in, logrus.Fields{"listenPort": a.listenPort, "callee": callee, "caller": caller})

	// 聚合键：优先 Hermes 业务 callUuid，否则退回 SIP Call-ID。
	// 既作 trace 会话聚合键（与传输层 tracer 一致），又作 call_record 主键（被叫腿记录与 trace 同 call_uuid）。
	leg := callee
	callID, bizUUID := inviteAggKeys(in.InviteRequest)
	businessID := inviteBusinessID(in.InviteRequest) // X-JBusinessId/x-business_id：被叫腿关联到发起业务（坐席外呼/群呼任务）
	aggKey := bizUUID
	if aggKey == "" {
		aggKey = callID
	}
	id := a.tracker.Start(aggKey, businessID, callee, caller, string(rule.Outcome))

	// 通话链路观测：传输层 SIP tracer 已自动抓真实 INVITE/180/200/BYE。
	// 这里附加「mock 客户腿业务决策(FLOW)」，与真实报文同会话。
	sess := a.bus.EnsureByCallID(aggKey, "call", "呼入 "+caller+" → "+callee+" ("+leg+")")
	a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "命中",
		"被叫="+callee+" 主叫="+caller+" 入口端口="+itoa(a.listenPort)+" 规则="+string(rule.Outcome),
		map[string]string{"leg": leg, "role": "customer", "listenPort": itoa(a.listenPort), "line": line})

	switch rule.Outcome {
	case behavior.OutcomeReject, behavior.OutcomeBusy:
		code := orDefault(rule.HangupCode, 486)
		reason := reasonForCode(code, "Busy Here")
		rejectCall(in, code, reason, log)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "拒接("+reason+") 码="+itoa(code), nil)
		a.tracker.Rejected(id, code)
		log.WithField("reason", reason).Infof("被叫腿拒接 → 回 %d %s（应回送至发起腿；若发起方仍显示呼叫中，查 FS/call-center 是否透传/重拨）", code, reason)
		return
	case behavior.OutcomeUnavailable:
		code := orDefault(rule.HangupCode, 503)
		reason := reasonForCode(code, "Service Unavailable")
		rejectCall(in, code, reason, log)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "不可用("+reason+") 码="+itoa(code), nil)
		a.tracker.Rejected(id, code)
		log.WithField("reason", reason).Infof("被叫腿不可用 → 回 %d %s", code, reason)
		return
	case behavior.OutcomeNoAnswer:
		ringing(in, log)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "振铃", nil)
		sleepMs(orDefault(rule.RingMs, a.cfg.DefaultRingMs))
		code := orDefault(rule.HangupCode, 480)
		reason := reasonForCode(code, "Temporarily Unavailable")
		rejectCall(in, code, reason, log)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "振铃不接("+reason+") 码="+itoa(code), nil)
		a.tracker.Rejected(id, code)
		log.WithField("reason", reason).Infof("被叫腿振铃不接 → 回 %d %s", code, reason)
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
		ringing(in, log)
		delay := orDefault(rule.RingMs, behavior.SlowAnswerDelayMs)
		log.Warnf("故障注入 SLOW_ANSWER：延迟 %dms 才应答", delay)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"SLOW_ANSWER：180 后延迟 "+itoa(delay)+"ms 才 200（慢应答）", map[string]string{"fault": "SLOW_ANSWER"})
		sleepMs(delay)
	} else if rule.RingMs > 0 {
		ringing(in, log)
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "决策", "振铃", nil)
		sleepMs(rule.RingMs)
	}
	if err := answer(in, log); err != nil {
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
	// ---- 接听后的媒体动作 ----
	// 写流来源**至多一个**：diago 的 AudioWriter 多次调用返回同一底层 writer，普通 Write 仅 RLock、
	// 对并发写无保护，两个 goroutine 同时写会损坏 RTP。按优先级择一：媒体故障 > 发 DTMF > 常态放音。
	// 读流（监听对端 DTMF / 只收不发）与写流相互独立，可并存（diago 支持同时持 reader+writer）。
	streamStop := make(chan struct{})
	streamingAudio := false
	mediaFault := rule.Fault == behavior.FaultNoRTP || rule.Fault == behavior.FaultOneWayAudio ||
		rule.Fault == behavior.FaultRtpLoss || rule.Fault == behavior.FaultRtpReorder
	switch {
	case rule.Fault == behavior.FaultRtpLoss:
		// RTP_LOSS：发媒体但按比例丢帧（媒体降质但不中断）。
		log.Warn("故障注入 RTP_LOSS：按比例丢帧发送")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"RTP_LOSS：发媒体但丢 "+itoa(behavior.RtpLossPercentDefault)+"% 帧（丢包/抖动）", map[string]string{"fault": "RTP_LOSS"})
		go a.lossySend(in, behavior.RtpLossPercentDefault, log)
	case rule.Fault == behavior.FaultRtpReorder:
		// RTP_REORDER：发媒体但小窗口内乱序（考验对端抖动缓冲）。
		log.Warn("故障注入 RTP_REORDER：小窗口乱序发送")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"RTP_REORDER：发媒体但小窗口内乱序（乱序/重排）", map[string]string{"fault": "RTP_REORDER"})
		go a.reorderSend(in, log)
	case rule.Fault == behavior.FaultNoRTP:
		// NO_RTP：接听后完全不发媒体（压媒体超时）——不起任何写 goroutine。
		log.Warn("故障注入 NO_RTP：接听后不发 RTP")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"NO_RTP：接听后不发媒体（媒体超时）", map[string]string{"fault": "NO_RTP"})
	case rule.Fault == behavior.FaultOneWayAudio:
		// ONE_WAY_AUDIO：只收不发——写流留空，读流见下方 drainInbound。
		log.Warn("故障注入 ONE_WAY_AUDIO：只收不发（不写媒体）")
		a.bus.Emit(sess, leg, tracelog.ChanFlow, tracelog.DirNA, "故障注入",
			"ONE_WAY_AUDIO：只收不发（单向音频）", map[string]string{"fault": "ONE_WAY_AUDIO"})
	case rule.DTMF != "":
		// 发送 DTMF（OTP 验证码 / IVR 选择）。占用写流，与持续放音互斥。
		go a.sendDTMF(in, rule.DTMF, log)
	case !rule.ExpectDTMF:
		// 常态：持续放音，使 FS 录音/坐席/监听能听到声音且 tx>0。
		streamingAudio = true
		go a.streamAudio(in, rule.Playback, streamStop, log)
		a.bus.Emit(sess, leg, tracelog.ChanBridge, tracelog.DirNA, "放音",
			"持续发送音频（"+audioSource(rule.Playback, a.cfg.DefaultPlayback)+"），对端可听", nil)
	}
	// 同时配了媒体故障又配了 DTMF：写流唯一，DTMF 被忽略（避免双写损坏 RTP）。
	if rule.DTMF != "" && mediaFault {
		log.Warnf("媒体故障 %s 占用写流，本通忽略发送 DTMF=%q（避免双写）", rule.Fault, rule.DTMF)
	}
	// 读流：与上面的写流独立、可并存。
	if rule.Fault == behavior.FaultOneWayAudio {
		go a.drainInbound(in, log) // 只读对端 RTP 产生 rx 统计，本侧不发
	} else if rule.ExpectDTMF && rule.DTMF == "" {
		// 监听对端按键（IVR 交互观测：放音→对端按键）。
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
		if frames := cachedPCMUFrames(filepath.Join(a.cfg.AudioDir, file)); len(frames) > 0 {
			return frames
		}
	}
	return cachedDialToneFrames() // 回退：合成可听拨号音（350+440Hz），保证一定有声音
}

func (a *Agent) sendDTMF(in *diago.DialogServerSession, digits string, log *logrus.Entry) {
	// diago 的 RTP DTMF 直接写 packetWriter，但需要 RTP 在流动。这里用静音帧
	// 驱动 RTP 时钟（PCMU 静音 = 0xFF），DTMFWriter 链式拦截：WriteDTMF 时会
	// 锁住静音写入并注入 RFC4733 telephone-event。与 PlaybackCreate 二选一。
	// 静音 goroutine 的 aw.Write 与本函数的 WriteDTMF 并发安全：diago RTPDtmfWriter.mu 已序列化二者。
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

// ivrListenMaxDur 是 IVR 单个 DTMF 监听 goroutine 的存活上限（覆盖整个 IVR 窗口）；
// 实际多在通话挂断/AudioReader 出错时提前结束。
const ivrListenMaxDur = 5 * time.Minute

// runIVR 执行脚本化 IVR 对话：从首步开始，每步「放提示音 → 等对端按键 → 按 branch 跳转」，
// 直到 HANGUP / 无下一步 / 步数上限。每步的放音、收键、跳转都记入链路（可观测多轮交互）。
// 真实媒体（放 WAV）+ RFC4733 收键，不做语义 ASR。
func (a *Agent) runIVR(in *diago.DialogServerSession, sess, leg string, steps []behavior.IVRStep, log *logrus.Entry) {
	byID := map[string]behavior.IVRStep{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	// 只取一次 AudioReader+DTMFReader：diago 每次 AudioReader(WithAudioReaderDTMF) 都会新建拦截器
	// 并覆盖上一个，逐步重复获取会泄漏/失效。这里取一次、用单个 Listen 持续收键，各步只按本步
	// 超时从通道取键（type-ahead：放音期间按下的键会缓冲，下一步消费）。
	digits := a.ivrDTMFChannel(in, ivrListenMaxDur, log)

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
		key := waitDTMF(digits, wait)
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

// ivrDTMFChannel 取一次 AudioReader+DTMFReader，起单个 Listen goroutine 把收到的按键投递到
// 带缓冲通道，供各 IVR 步按本步超时取键。reader 取失败返回 nil 通道（waitDTMF 一律走超时）。
func (a *Agent) ivrDTMFChannel(in *diago.DialogServerSession, dur time.Duration, log *logrus.Entry) <-chan string {
	dtmfR := &diago.DTMFReader{}
	if _, err := in.AudioReader(diago.WithAudioReaderDTMF(dtmfR)); err != nil {
		log.Errorf("ivr dtmf reader: %v", err)
		return nil // nil 通道：waitDTMF 永远走超时分支，IVR 仍按 OnNoKey 推进
	}
	digits := make(chan string, 16)
	go func() {
		_ = dtmfR.Listen(func(d rune) error {
			select {
			case digits <- string(d):
			default: // 缓冲满则丢最新键，绝不阻塞 Listen
			}
			return nil
		}, dur)
	}()
	return digits
}

// waitDTMF 在 wait 内等一个按键，超时返回空。digits 为 nil 时永远超时（读 nil 通道阻塞）。
func waitDTMF(digits <-chan string, wait time.Duration) string {
	select {
	case k := <-digits:
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

// reasonForCode 给 SIP 失败码配标准 reason 短语（RFC 3261 §21 等）；未知码才用 fallback。
// 解决「自定义 HangupCode 时 reason 文案与码不符」（如配 500 仍写 "Busy Here"、603 仍写 "Busy Here"）。
func reasonForCode(code int, fallback string) string {
	switch code {
	// 4xx Request Failure
	case 400:
		return "Bad Request"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 408:
		return "Request Timeout"
	case 410:
		return "Gone"
	case 480:
		return "Temporarily Unavailable"
	case 484:
		return "Address Incomplete"
	case 486:
		return "Busy Here"
	case 487:
		return "Request Terminated"
	case 488:
		return "Not Acceptable Here"
	// 5xx Server Failure
	case 500:
		return "Server Internal Error"
	case 501:
		return "Not Implemented"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	case 504:
		return "Server Time-out"
	// 6xx Global Failure
	case 600:
		return "Busy Everywhere"
	case 603:
		return "Decline"
	case 604:
		return "Does Not Exist Anywhere"
	case 606:
		return "Not Acceptable"
	}
	return fallback
}

// ===========================================================================
// diago 适配薄封装：集中所有 diago 调用，按实际 diago 版本在此处统一校准。
// ===========================================================================

// inviteAggKeys 从入站 INVITE 取会话聚合所需的两个键：SIP Call-ID 与 Hermes 业务 callUuid。
// bizUUID 用与传输层 tracer 相同的 tracelog.BizUUIDFromHeaders 提取（同名集合 + 同优先级），
// 保证两条观测路径算出同一聚合键、同一通话不分裂。原始报文由 siptrace 在传输层另抓，这里不构建，
// 省去热路径上 req.String() 的无谓开销。req 为 nil 返回空。
func inviteAggKeys(req *sip.Request) (callID, bizUUID string) {
	if req == nil {
		return "", ""
	}
	hdrs := req.Headers()
	kv := make([]tracelog.HeaderKV, 0, len(hdrs))
	for _, h := range hdrs {
		kv = append(kv, tracelog.HeaderKV{Name: h.Name(), Value: h.Value()})
	}
	if cid := req.CallID(); cid != nil {
		callID = cid.Value()
	}
	return callID, tracelog.BizUUIDFromHeaders(kv)
}

// businessIDHeaders 业务 businessId 头名（小写，大小写不敏感匹配）。被叫腿据此关联到发起它的业务：
//   - X-JBusinessId：坐席外呼前端 jssip 注入（已抓包确认客户腿带，值=前端 param.businessId）。
//   - x-business_id：群呼/外呼任务后端 originate 可能注入（sip_h_x-business_id 经 FS 去前缀；待群呼实测）。
var businessIDHeaders = []string{"x-jbusinessid", "x-business_id", "x-business-id"}

// inviteBusinessID 从入站 INVITE 提取 Hermes businessId（X-JBusinessId / x-business_id，大小写不敏感）。
// 空表示该 INVITE 未带 businessId（如纯 sip-call 或群呼未设 NumberInfo.businessId）。
func inviteBusinessID(req *sip.Request) string {
	if req == nil {
		return ""
	}
	for _, h := range req.Headers() {
		ln := strings.ToLower(h.Name())
		for _, want := range businessIDHeaders {
			if ln == want && h.Value() != "" {
				return h.Value()
			}
		}
	}
	return ""
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
// 对照 Hermes CommonConstant.CALL_X_LINE_NAME）。它现在只用于观测/兼容预览；
// SIP 热路径按 mock 入口端口匹配客户组。
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
func ringing(in *diago.DialogServerSession, log *logrus.Entry) {
	logSIPRoute("SIP 响应发送前 180 Ringing", in, nil)
	if err := in.Ringing(); err != nil {
		logSIPResponseError(log, "180 Ringing", err, in, nil)
		return
	}
	logSIPRoute("SIP 响应发送成功 180 Ringing", in, nil)
}

// answer 回 200 OK 并建立媒体会话。对照 diago v0.28.0
func answer(in *diago.DialogServerSession, log *logrus.Entry) error {
	logSIPRoute("SIP 响应发送前 200 OK", in, nil)
	err := in.Answer()
	if err != nil {
		logSIPResponseError(log, "200 OK", err, in, nil)
		return err
	}
	logSIPRoute("SIP 响应发送成功 200 OK", in, nil)
	return nil
}

// rejectCall 以指定 SIP 码拒接（如 486/503/480/500）。对照 diago v0.28.0
// Respond 失败会致被叫腿没把终态码发出 → 发起腿永远收不到终态、卡在呼叫中，故落 Error 不吞。
func rejectCall(in *diago.DialogServerSession, code int, reason string, log *logrus.Entry) {
	extra := logrus.Fields{"code": code, "reason": reason}
	logSIPRoute("SIP 响应发送前 "+itoa(code)+" "+reason, in, extra)
	if err := in.Respond(code, reason, nil); err != nil {
		logSIPResponseError(log, itoa(code)+" "+reason, err, in, extra)
		return
	}
	logSIPRoute("SIP 响应发送成功 "+itoa(code)+" "+reason, in, extra)
}

func logSIPResponseError(log *logrus.Entry, response string, err error, in *diago.DialogServerSession, extra logrus.Fields) {
	fields := sipRouteFields(in)
	for k, v := range extra {
		fields[k] = v
	}
	fields["response"] = response
	if log == nil {
		logrus.WithFields(fields).Errorf("SIP 响应发送失败：%v", err)
		return
	}
	log.WithFields(fields).Errorf("SIP 响应发送失败：%v", err)
}

func logSIPRoute(message string, in *diago.DialogServerSession, extra logrus.Fields) {
	fields := sipRouteFields(in)
	for k, v := range extra {
		fields[k] = v
	}
	logrus.WithFields(fields).Info(message)
}

func sipRouteFields(in *diago.DialogServerSession) logrus.Fields {
	fields := logrus.Fields{}
	if in == nil || in.InviteRequest == nil {
		return fields
	}
	req := in.InviteRequest
	fields["sipMethod"] = req.Method
	fields["requestURI"] = req.Recipient.String()
	fields["transport"] = req.Transport()
	fields["reqSource"] = req.Source()
	fields["reqDestination"] = req.Destination()
	if cid := req.CallID(); cid != nil {
		fields["callID"] = cid.Value()
	}
	if cseq := req.CSeq(); cseq != nil {
		fields["cseq"] = cseq.Value()
	}
	if contact := req.Contact(); contact != nil {
		fields["contact"] = contact.Value()
	}
	if via := req.Via(); via != nil {
		fields["topVia"] = via.Value()
		fields["topViaHost"] = via.Host
		fields["topViaPort"] = via.Port
		fields["topViaTransport"] = via.Transport
		if via.Params != nil {
			if rport, ok := via.Params.Get("rport"); ok {
				fields["topViaRPort"] = rport
			}
			if received, ok := via.Params.Get("received"); ok {
				fields["topViaReceived"] = received
			}
		}
		fields["responseDestHint"] = responseDestinationHint(req, via)
		fields["responseDest"] = sip.NewResponseFromRequest(req, sip.StatusTrying, "Trying", nil).Destination()
	}
	if route := req.GetHeader("Record-Route"); route != nil {
		fields["recordRoute"] = route.Value()
	}
	return fields
}

func responseDestinationHint(req *sip.Request, via *sip.ViaHeader) string {
	if req == nil {
		return ""
	}
	if via == nil {
		return req.Source()
	}
	if via.Params != nil {
		if rport, ok := via.Params.Get("rport"); ok && rport == "" {
			return req.Source()
		}
		if rport, ok := via.Params.Get("rport"); ok && rport != "" {
			host := via.Host
			if received, ok := via.Params.Get("received"); ok && received != "" {
				host = received
			}
			return host + ":" + rport
		}
	}
	if via.Port > 0 {
		return via.Host + ":" + itoa(via.Port)
	}
	return via.Host + ":5060"
}

// addTopViaSourceParams 在原始 SIP 请求进入 parser 前，为顶层 Via 补 received/rport。
// 这样 sipgo/diago 后续仍走标准响应路径，但响应目的地址会解析到实际 UDP 包源。
func addTopViaSourceParams(info sip.TransportReadProps, data []byte) ([]byte, error) {
	if !strings.EqualFold(info.Transport, "udp") || info.RemoteAddr == nil {
		return data, nil
	}
	sourceHost, sourcePort, err := net.SplitHostPort(info.RemoteAddr.String())
	if err != nil || sourceHost == "" || sourcePort == "" {
		return data, nil
	}
	if len(data) == 0 || bytes.HasPrefix(data, []byte("SIP/2.0")) || !bytes.Contains(data, []byte("SIP/2.0")) {
		return data, nil
	}
	headerEnd := bytes.Index(data, []byte("\r\n\r\n"))
	separatorLen := 4
	if headerEnd < 0 {
		headerEnd = bytes.Index(data, []byte("\n\n"))
		separatorLen = 2
	}
	if headerEnd < 0 {
		return data, nil
	}

	header := data[:headerEnd]
	body := data[headerEnd:]
	lines := bytes.Split(header, []byte("\n"))
	for i := 1; i < len(lines); i++ {
		line := bytes.TrimRight(lines[i], "\r")
		if len(line) == 0 {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if !isViaHeaderLine(line) {
			continue
		}
		if viaHasParam(line, "rport") {
			return data, nil
		}
		addition := []byte(";rport=" + sourcePort + ";received=" + sourceHost)
		if bytes.HasSuffix(lines[i], []byte("\r")) {
			lines[i] = append(append([]byte{}, lines[i][:len(lines[i])-1]...), append(addition, '\r')...)
		} else {
			lines[i] = append(lines[i], addition...)
		}
		out := make([]byte, 0, len(data)+len(addition))
		out = append(out, bytes.Join(lines, []byte("\n"))...)
		out = append(out, body[:separatorLen]...)
		out = append(out, body[separatorLen:]...)
		return out, nil
	}
	return data, nil
}

func isViaHeaderLine(line []byte) bool {
	if len(line) < 2 {
		return false
	}
	if len(line) >= 4 && strings.EqualFold(string(line[:4]), "via:") {
		return true
	}
	return (line[0] == 'v' || line[0] == 'V') && line[1] == ':'
}

func viaHasParam(line []byte, param string) bool {
	for _, part := range bytes.Split(line, []byte(";"))[1:] {
		part = bytes.TrimSpace(part)
		if idx := bytes.IndexByte(part, '='); idx >= 0 {
			part = part[:idx]
		}
		if strings.EqualFold(string(part), param) {
			return true
		}
	}
	return false
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
