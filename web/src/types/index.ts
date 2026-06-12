// 全部 TypeScript 接口与类型定义。
// api.ts 会整体 re-export，页面既可 import from '../types' 也可沿用 from '../api'。

// ---- 总览模型 ----
export type CallState = 'RINGING' | 'ANSWERED' | 'ENDED' | 'REJECTED'

export interface Call {
  id: string
  callee: string
  caller: string
  outcome: string
  state: CallState
  hangupCode?: number
  startedAt: string
  answeredAt?: string
  endedAt?: string
}

export interface Stats {
  active: number
  total: number
  answered: number
  rejected: number
}

// ---- 媒体管理（预置 G.711 WAV）----
export interface AudioFile {
  name: string
  size: number
}

// ===== 通话链路可观测（SIP 信令 / ESL / WS / 桥接 时间线）=====
export type TraceChannel = 'SIP' | 'ESL' | 'WS' | 'BRIDGE' | 'FLOW'

export type TraceDir = 'IN' | 'OUT' | '-'

export interface TraceEvent {
  seq: number
  ts: string
  session: string
  leg: string
  channel: TraceChannel
  dir: TraceDir
  method: string
  summary: string
  detail?: Record<string, string>
  headers?: { name: string; value: string }[]
  raw?: string
  callId?: string
  src?: string
  dst?: string
}

export interface TraceSession {
  id: string
  title: string
  kind: string
  callId?: string
  startedAt: string
  updatedAt: string
  legs: string[]
  eventCount?: number // 列表摘要的事件数（瘦身后列表只回这个，不回 events）
  events?: TraceEvent[] // 仅单查 /trace/sessions/:id 或 ?match= 时返回
}

// ===== Hermes 栈服务健康（仅健康，不查业务库）=====
export interface ServiceHealth {
  name: string
  url: string
  up: boolean
  status: number
  latencyMs: number
  err?: string
}

export interface Overview {
  mock: { stats: Stats; active: Call[] }
  hermes: { health: ServiceHealth[] }
  trace: { sessions: TraceSession[] }
}

// ===== 真实 Hermes 坐席管理（经 OpenAPI：查/建/改/删/启停/切工作状态）=====
// 区别于上面的 hermes-ws 上线：这里直接 CRUD 当前机构 Hermes basic 的真实坐席（mock 只调 OpenAPI、不碰库）。
export interface ManagedAgent {
  agentCode?: string
  agentName?: string
  number?: string
  password?: string // Hermes 坐席分页接口回带的登录口令（软电话直接用，无需手填）
  orgCode?: string
  depCode?: string
  agentGroupCode?: string
  callProcessTime?: number
  remark?: string
  state?: unknown
  status?: unknown
}

export interface ManagedAgentFilter {
  pageNum?: number; pageSize?: number
  agentName?: string; number?: string; agentGroupCode?: string; depCode?: string; status?: string
}

export interface AddAgentReq {
  agentName?: string; password: string; agentGroupCode?: string; depCode?: string
  agentRoleCode?: string; phoneCode?: string; callProcessTime?: number; status?: number; remark?: string
}

export interface UpdateAgentReq {
  agentNumber: string; agentName?: string; depCode?: string; agentRoleCode?: string
  callProcessTime?: number; status?: string; agentGroupCode?: string
}

// ===== 针对性测试用例 =====
export interface TestStep {
  name: string
  ok: boolean
  detail: string
  optional?: boolean // 参考性断言（如环境受限的坐席腿），不计入 run 成败
}

export interface CallPhase {
  name: string
  status: 'ok' | 'pending' | 'fail'
  detail: string
}

export interface CallView {
  id: string
  scenario: string
  customer: string
  agent?: string
  agentGroup?: string
  status: 'CONNECTED' | 'OBSERVED' | 'PENDING' | 'FAILED'
  customerState: string
  agentState?: string
  traceId?: string
  callUuid?: string
  detail?: string
  durationMs?: number
  phases?: CallPhase[]
}

export interface TestRun {
  id: string
  case: string
  ok: boolean
  startedAt: string
  durationMs: number
  steps: TestStep[]
  traceId?: string
  artifacts?: Record<string, unknown>
  calls?: CallView[]
}

export interface PreflightCheck { name: string; status: 'OK' | 'WARN' | 'FAIL'; detail: string }

export interface PreflightReport { scenario: string; ready: boolean; checks: PreflightCheck[] }

export interface PreflightResp {
  callCenterTask: PreflightReport
  autoCall: PreflightReport
  otp: PreflightReport
}

export interface BootstrapResult {
  profileCode: string; customerGroup: string; agentGroup: string
  lineCode?: string; listenPort?: number; lineBinding: string; notes: string[]
}

export interface ScenarioMetrics {
  passRate: number
  avgDurMs: number
  minDurMs: number
  maxDurMs: number
  p90DurMs: number
  concurrent: boolean
}

export interface ScenarioResult {
  id: string
  groupCode: string
  total: number
  passed: number
  failed: number
  startedAt: string
  durationMs: number
  metrics: ScenarioMetrics
  runs: TestRun[]
  calls?: CallView[]
}

// ===== mock 自有呼叫记录（不从 Hermes 查询）=====
export interface CallRecord {
  id: number
  recordId: string
  scenario: string
  source: string
  runId: string
  orgCode: string
  taskName: string
  taskCode: string
  customerGroup: string
  customerNumber: string
  agentGroupCode: string
  agentNumber: string
  lineCode: string
  lineAddress: string
  lineName: string
  direction: string
  callType: string
  expectOutcome?: string
  status: string
  result: string
  hangupCode: number
  traceId: string
  callUuid: string
  startedAt: string
  answeredAt?: string
  endedAt?: string
  durationMs: number
  lastEventAt: string
  stepsJson: string
  detailJson: string
  lastSummary: string
}

export interface CallRecordPage {
  records: CallRecord[]
  total: number
  page: number
  pageSize: number
}

export interface CallRecordFilter {
  scenario?: string
  source?: string
  status?: string
  orgCode?: string
  runId?: string
  taskName?: string
  taskCode?: string
  customerGroup?: string
  customerNumber?: string
  agentGroupCode?: string
  agentNumber?: string
  lineCode?: string
  traceId?: string
  callUuid?: string
  keyword?: string
  startedFrom?: string
  startedTo?: string
  page?: number
  pageSize?: number
}

// 坐席软电话外呼结束回存「坐席侧」记录 + 断言（区别于 mock 被叫腿自动落的 sip-inbound）
export interface AgentCallRecord {
  callId: string; agentNumber: string; customer?: string
  expectOutcome?: string; expectFault?: string; expectDisabled?: boolean
  answered: boolean; endCause?: string; inbound?: boolean
  verdict?: string; verdictReason?: string
  traceId?: string; displayCaller?: string; startedAtMs?: number; answeredAtMs?: number; durationMs?: number
}

// ===== mock 客户配置（号段组 + 个例 + 端口绑定 + 行为档）=====
export interface BehaviorProfile {
  id?: number
  code: string
  name?: string
  outcome: string
  ringMs?: number
  talkMs?: number
  hangupCode?: number
  playback?: string
  dtmf?: string
  expectDtmf?: boolean
  fault?: string
  bridgeTarget?: string
  ivrJson?: string
  answerRatio?: number
  remark?: string
}

// IVRStep 一步脚本化 IVR（对应后端 behavior.IVRStep）：放音→等按键→分支。
export interface IVRStep {
  id: string
  prompt?: string
  waitMs?: number
  branch?: Record<string, string>
  onNoKey?: string
  sendDtmf?: string
}

export interface CustomerGroup {
  id?: number
  code: string
  name?: string
  numberPrefix?: string
  numberStart?: number
  count?: number
  behaviorCode?: string
  state?: string
  remark?: string
}

export interface CustomerOverride {
  id?: number
  groupCode?: string
  number: string
  behaviorCode?: string
  state?: string
  remark?: string
}

export interface LineBinding {
  id?: number
  listenPort: number
  lineCode?: string
  lineName?: string
  groupCode?: string
  enabled?: number
  remark?: string
}

// ===== Hermes 机构配置（OpenAPI 接入凭据；mock 只走 OpenAPI，不碰 Hermes 表）=====
export interface OrgConfig {
  id?: number
  orgCode: string
  orgName: string
  mode: 'direct' | 'gateway'
  gatewayUrl?: string
  apiKey?: string
  basicUrl?: string
  callCenterUrl?: string
  callBotUrl?: string
  agentWsUrl?: string
  otpUrl?: string
  userCode?: string
  defaultAgentGroupCode?: string
  defaultAgentRoleCode?: string
  defaultDepCode?: string
  defaultAgentPassword?: string
  remark?: string
}

export interface OrgsResp { orgs: OrgConfig[]; current: string }

// 群呼表单联动：真实 TTS 模板 + 技能组（从坐席聚合）
export interface TtsVoice { ttsCode: string; name: string; lang?: string }

export interface AgentGroupAgg { code: string; name?: string; count: number }

// Hermes 回调
export interface CallbackRecord { seq: number; ts: string; source: string; event: string; orgCode: string; callUuid: string; remote: string; payload: unknown }
