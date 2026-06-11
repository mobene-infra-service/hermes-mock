// 统一枚举选项。
// 与后端 internal/behavior 的 Outcome/Fault 一一对应。

// 被叫应答行为 outcome
export const OUTCOME_OPTIONS = [
  { value: 'ANSWER', label: 'ANSWER 接听' },
  { value: 'REJECT', label: 'REJECT 拒接' },
  { value: 'BUSY', label: 'BUSY 忙线' },
  { value: 'NO_ANSWER', label: 'NO_ANSWER 振铃不接' },
  { value: 'UNAVAILABLE', label: 'UNAVAILABLE 不可达' },
  { value: 'BRIDGE', label: 'BRIDGE 桥接' },
]

// 故障注入：与后端 behavior.Fault 一一对应（8 种 + 无）。
export const FAULT_OPTIONS: { value: string; label: string }[] = [
  { value: '', label: '(无)' },
  { value: 'ONE_WAY_AUDIO', label: 'ONE_WAY_AUDIO 单向音频（只收不发）' },
  { value: 'NO_RTP', label: 'NO_RTP 接听后不发 RTP（媒体超时）' },
  { value: 'HALF_HANGUP', label: 'HALF_HANGUP 半挂断（本侧不发 BYE）' },
  { value: 'NO_RESPONSE', label: 'NO_RESPONSE 收 INVITE 不响应（SIP 408）' },
  { value: 'SLOW_ANSWER', label: 'SLOW_ANSWER 慢应答（拖很久才 200）' },
  { value: 'ANSWER_DROP', label: 'ANSWER_DROP 接通即挂（200 后立即 BYE）' },
  { value: 'RTP_LOSS', label: 'RTP_LOSS 按比例丢帧（丢包/抖动）' },
  { value: 'RTP_REORDER', label: 'RTP_REORDER 小窗口乱序（乱序/重排）' },
]

// 线路类型（Hermes 2026-06 lineType 特性：base 预留默认，可自定义）
export const LINE_TYPE_OPTIONS = [
  { value: 'base' },
  { value: 'cat' },
  { value: 'pool' },
  { value: 'gsm' },
]
