// Package behavior 定义 mock 客户被叫腿的「应答行为」类型：结果(Outcome)、故障(Fault)、
// IVR 脚本、整条规则(Rule)。这些是 sipagent 执行被叫行为的领域模型；具体行为由 cluster
// （号段客户组+个例+行为档，持久化在 hermes_mock 库）解析后转成 Rule。
//
// 历史上这里还有一套内存规则存储(Store)，已随 /api/rules、RulesPage 一并移除——
// 行为统一由 cluster 配置驱动（DB 为准）。
package behavior

// Outcome 通话结果类型。
type Outcome string

const (
	OutcomeAnswer      Outcome = "ANSWER"      // 接听（200 OK + 媒体）
	OutcomeReject      Outcome = "REJECT"      // 拒接（默认 486）
	OutcomeBusy        Outcome = "BUSY"        // 忙（486）
	OutcomeNoAnswer    Outcome = "NO_ANSWER"   // 振铃不接（超时后 480）
	OutcomeUnavailable Outcome = "UNAVAILABLE" // 不可用（默认 503）
	OutcomeBridge      Outcome = "BRIDGE"      // 历史值；mock 只演客户腿，解析时降级为 ANSWER
)

// Fault 故障注入类型（生产异常场景）。
type Fault string

const (
	FaultNone        Fault = ""
	FaultOneWayAudio Fault = "ONE_WAY_AUDIO" // 单向音频（只收不发）
	FaultNoRTP       Fault = "NO_RTP"        // 接听后不发 RTP（媒体超时）
	FaultHalfHangup  Fault = "HALF_HANGUP"   // 不发 BYE（半挂断）
	FaultNoResponse  Fault = "NO_RESPONSE"   // 收到 INVITE 完全不响应（SIP 超时，对端 408）
	FaultSlowAnswer  Fault = "SLOW_ANSWER"   // 拖很久才 200（慢应答）
	FaultAnswerDrop  Fault = "ANSWER_DROP"   // 200 应答后立即 BYE（接通即挂）
	FaultRtpLoss     Fault = "RTP_LOSS"      // 发媒体但按比例丢帧（丢包/抖动）
	FaultRtpReorder  Fault = "RTP_REORDER"   // 发媒体但小窗口内乱序（乱序/重排）
)

// RtpLossPercentDefault RTP 丢包故障默认丢帧比例（%）。
const RtpLossPercentDefault = 30

// SlowAnswerDelayMs 慢应答故障默认延迟（无 RingMs 配置时）。
const SlowAnswerDelayMs = 12000

// Rule 一条被叫行为：由 cluster 解析结果(clusterToRule)转换而来，sipagent 据此应答。
type Rule struct {
	Outcome    Outcome `json:"outcome"`
	RingMs     int     `json:"ringMs"`     // 振铃时长
	TalkMs     int     `json:"talkMs"`     // 接听后通话时长（到点挂断）
	HangupCode int     `json:"hangupCode"` // 拒接/不可用的 SIP 响应码（486/503/480）
	Playback   string  `json:"playback"`   // 接听后放音文件（AudioDir 下相对名）
	DTMF       string  `json:"dtmf"`       // 接听后发送的 DTMF 序列，如 "123456#"
	ExpectDTMF bool    `json:"expectDtmf"` // 接听后监听对端按键（IVR 交互观测）
	Fault      Fault   `json:"fault"`      // 故障注入
	// IVR 脚本化对话：非空时接听后按脚本「放音→收按键→分支」多轮（真实 RTP + RFC4733，非语义 ASR）。
	IVR []IVRStep `json:"ivr,omitempty"`
}

// IVRStep 一步脚本化 IVR：放一段提示音，等对端按键，按键映射决定下一步。
type IVRStep struct {
	ID       string            `json:"id"`       // 步骤标识（branch 目标）
	Prompt   string            `json:"prompt"`   // 提示音文件（AudioDir 下相对名）
	WaitMs   int               `json:"waitMs"`   // 等待按键时长（默认 5000）
	Branch   map[string]string `json:"branch"`   // 按键→下一步 ID（特殊目标 "HANGUP" 挂断）
	OnNoKey  string            `json:"onNoKey"`  // 超时未按键→下一步 ID（空=挂断）
	SendDTMF string            `json:"sendDtmf"` // 进入本步先发的 DTMF（模拟机器人侧按键，可选）
}
