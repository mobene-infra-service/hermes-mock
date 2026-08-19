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

// ===== 通用 HTTP Mock =====
export type HTTPMockAction = 'RESPOND' | 'TIMEOUT'
export type HTTPMockOverridePolicy = 'NONE' | 'CASE_ONLY' | 'FULL'
export type HTTPMockConditionSource = 'method' | 'query' | 'header' | 'jsonBody' | 'rawBody'
export type HTTPMockConditionOperator = 'EQ' | 'NE' | 'IN' | 'CONTAINS' | 'PREFIX' | 'EXISTS'

export interface HTTPMockResponseSpec {
  action?: HTTPMockAction
  status?: number
  contentType?: string
  headers?: Record<string, string>
  body?: string
  delayMs?: number
  timeoutMs?: number
}

export interface HTTPMockCondition {
  source: HTTPMockConditionSource
  field?: string
  operator: HTTPMockConditionOperator
  value?: unknown
}

export interface HTTPMockWeightedCase {
  case: string
  weight: number
}

export interface HTTPMockRule {
  name: string
  priority?: number
  conditions: HTTPMockCondition[]
  case?: string
  weightedCases?: HTTPMockWeightedCase[]
}

export interface HTTPMockEndpointConfig {
  allowedMethods?: string[]
  overridePolicy?: HTTPMockOverridePolicy
  defaultResponse: HTTPMockResponseSpec
  defaultWeightedCases?: HTTPMockWeightedCase[]
  sequenceCases?: string[]
  cases?: Record<string, HTTPMockResponseSpec>
  rules?: HTTPMockRule[]
}

export interface HTTPMockEndpoint {
  id?: number
  token?: string
  name: string
  enabled: boolean
  config: HTTPMockEndpointConfig
  remark?: string
  gmtCreate?: string
  gmtModified?: string
  invokePath?: string
  invokeUrl?: string
}

export interface HTTPMockRequestRecord {
  id: number
  endpointId: number
  token: string
  receivedAt: string
  remote: string
  method: string
  path: string
  queryJson: string
  headersJson: string
  requestBody: string
  matchedRule: string
  selectedCase: string
  selectionMode: string
  selectedWeight: number
  totalWeight: number
  overrideJson: string
  responseAction: string
  responseStatus: number
  responseHeadersJson: string
  responseBody: string
  delayMs: number
  durationMs: number
  clientCanceled: boolean
}

// ===== 可插拔短信厂商 Mock =====
export type SMSMockSubmitAction = 'RESPOND' | 'TIMEOUT'
export type SMSMockSubmitResult = 'ACCEPTED' | 'REJECTED' | 'MALFORMED' | 'CUSTOM'

export interface SMSMockSubmissionSpec {
  action?: SMSMockSubmitAction
  result?: SMSMockSubmitResult
  httpStatus?: number
  delayMs?: number
  timeoutMs?: number
  errorCode?: number
  details?: string
  parts?: number
  rawBodyTemplate?: string
}

export interface SMSMockReceiptSpec {
  enabled: boolean
  delayMs?: number
  statusCode?: string
  errorCode?: string
  errorDescription?: string
  operator?: string
  repeat?: number
  repeatIntervalMs?: number
  rawBodyTemplate?: string
}

export interface SMSMockCaseSpec {
  submit: SMSMockSubmissionSpec
  receipt: SMSMockReceiptSpec
}

export interface SMSMockEndpointConfig {
  callbackUrl: string
  callbackTimeoutMs?: number
  callbackMaxAttempts?: number
  callbackRetryBackoffMs?: number
  allowCaseOverride: boolean
  defaultCase: string
  defaultWeightedCases?: HTTPMockWeightedCase[]
  cases: Record<string, SMSMockCaseSpec>
  rules?: HTTPMockRule[]
}

export interface SMSMockEndpoint {
  id?: number
  token?: string
  name: string
  enabled: boolean
  provider: string
  protocolVersion: string
  config: SMSMockEndpointConfig
  remark?: string
  gmtCreate?: string
  gmtModified?: string
  invokePath?: string
  invokeUrl?: string
}

export interface SMSMockProviderField {
  path: string
  label: string
  description?: string
}

export interface SMSMockProviderInfo {
  provider: string
  protocolVersion: string
  displayName: string
  hermesVendorName: string
  hermesConfig: Record<string, unknown>
  hermesCallbackHint: string
  submitContentType: string
  receiptContentType: string
  matchFields: SMSMockProviderField[]
  submitTemplateVariables: string[]
  receiptTemplateVariables: string[]
  defaultConfig: SMSMockEndpointConfig
}

export interface SMSMockMessage {
  id: number
  endpointId: number
  token: string
  provider: string
  protocolVersion: string
  reference: string
  recipient: string
  sender: string
  content: string
  receivedAt: string
  remote: string
  requestBody: string
  matchedRule: string
  selectedCase: string
  selectionMode: string
  selectedWeight: number
  totalWeight: number
  submitAction: string
  submitState: string
  submitHttpStatus: number
  submitResponseBody: string
  submitCompletedAt?: string
  receiptEnabled: boolean
  receiptStatus: string
  receiptDelayMs: number
  receiptDueAt?: string
  receiptTargetCount: number
  receiptSentCount: number
  receiptRepeatIntervalMs: number
  callbackUrl: string
  callbackMethod: string
  callbackHeadersJson: string
  callbackBody: string
  callbackTimeoutMs: number
  callbackMaxAttempts: number
  callbackRetryBackoffMs: number
  callbackClaimedAt?: string
  callbackCurrentAttempt: number
  callbackAttempts: number
  callbackLastHttpStatus: number
  callbackLastResponseBody: string
  callbackLastError: string
  callbackCompletedAt?: string
  gmtModified: string
}

export interface SMSMockCallbackAttempt {
  id: number
  messageId: number
  reference: string
  deliveryNo: number
  attemptNo: number
  startedAt: string
  completedAt?: string
  url: string
  method: string
  requestHeadersJson: string
  requestBody: string
  httpStatus: number
  responseBody: string
  error: string
  success: boolean
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

export interface Overview {
  mock: { stats: Stats; active: Call[] }
  trace: { sessions: TraceSession[] }
}

// ===== 真实 Hermes 坐席管理（经 OpenAPI：查/建/改/删/启停/切工作状态）=====
// 这里直接 CRUD 当前机构 Hermes basic 的真实坐席（mock 只调 OpenAPI、不碰库）；坐席上线/外呼走前端 jssip 软电话。
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
  scenarios?: string[]
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
  stratflowUrl?: string
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

// ---- 策略流应用层 mock 编排（对接 hermes-stratflow /openapi/mock）----
// 这是「应用层 mock（stratflow 合成回执）」的编排台，与 SIP 被叫腿正交；测策略图分支逻辑时不产真实 SIP。
export type SfDispatchMode = 'REAL' | 'MOCK' | 'PAUSED'
export interface SfGateView {
  master: boolean
  mode?: SfDispatchMode
  global: boolean
  schemeModes?: Record<string, SfDispatchMode>
  schemes: Record<string, boolean>
  deliveryPaused: boolean
  receiptWindowSec: number
}
export interface SfMockCaseResult {
  type: 'CALL' | 'SMS'
  status: string
  terminalAttemptNo?: number | null
  retryRingStatus?: string | null
  ringStatus?: string | null
  intention?: string | null
  talkDurationSec?: number | null
  failureReason?: string | null
  errorCode?: string | null
  errorDesc?: string | null
  partCount?: number | null
  callbackOverrides?: Record<string, unknown>
}
export interface SfAPIPushFieldOption { value: unknown; i18nKey: string; fallbackLabel: string }
export interface SfAPIPushFieldDefinition {
  path: string; type: string; i18nKey: string; fallbackLabel: string
  required: boolean; sensitive: boolean; defaultSelected: boolean; sample: unknown
  options: SfAPIPushFieldOption[]
}
export interface SfMockCase { key: string; name: string; delayMs: number; result: SfMockCaseResult }
export interface SfWeightedCase { caseKey: string; weight: number }
export interface SfSelection { mode: 'FIXED' | 'WEIGHTED'; caseKey?: string | null; choices?: SfWeightedCase[] }
export interface SfMatchCondition { key: string; type: string; itemType?: string | null; op: string; value?: unknown }
export interface SfSelectionRule { name: string; priority: number; conditions: SfMatchCondition[]; selection: SfSelection }
export interface SfNodeConfig { forcedCaseKey?: string | null; cases: SfMockCase[]; defaultSelection: SfSelection; rules: SfSelectionRule[] }
export interface SfResultSchema { type: 'CALL' | 'SMS'; statuses: string[]; ringStatuses: string[]; intentions: string[]; maxAttemptNo?: number | null; retryStepGapMs?: number | null }
export interface SfMatchField { key: string; type: string; itemType?: string | null }
export interface SfMatchSchema { fields: SfMatchField[]; operators: Record<string, string[]> }
export interface SfCasePreview { steps: SfMockStep[]; actionFinal: string; nodePort: string; expectedVars: Record<string, unknown>; dynamicVars: string[] }
export interface SfNode {
  nodeId: string
  type: string
  channel?: string | null
  configured: boolean
  resultSchema: SfResultSchema
  matchSchema: SfMatchSchema
  callbackFields: SfAPIPushFieldDefinition[]
  config: SfNodeConfig
  previews: Record<string, SfCasePreview>
}
export interface SfWorkflow { defCode: string; name: string; status: number; orgEnabled: boolean }
export interface SfWorkflowDetail extends SfWorkflow { versionCode: string }
export interface SfCollection { code: string; name: string; status: number; boundPlanCount: number; fieldCount: number; entryCount: number }
export interface SfField {
  key: string
  displayName: string
  dataType: string
  required: boolean
  sort: number
  format?: string | null
  options?: string[] | null
  itemType?: string | null
  maxLen?: number | null
  scale?: number | null
}
export interface SfBinding { defCode: string; defName: string; status: number }
export interface SfVersionRun {
  code: string
  collectionCode: string
  defCode: string
  defName?: string | null
  bindingStatus: number
  versionCode: string
  versionNo: number
  result: number
  status: number
  numberCount: number
  reachedEndCount: number
  terminalCount: number
  expiredCount: number
  canceledCount: number
  firstConsumedAt?: unknown
  terminalAt?: unknown
  anomalyFlags?: string[] | null
  createdAt?: unknown
  updatedAt?: unknown
  failFields?: string[] | null
  contractFailure?: unknown
  localCancelPending: boolean
  executionCount: number
  unsettledExecutionCount: number
}
export interface SfVersionRunPage { records: SfVersionRun[]; total: number; size: number; current: number; pages: number }
export interface SfRun { code: string; collectionCode: string; defCode: string; defName: string; versionCode: string; status: number; numberCount: number; reachedEndCount: number; terminalCount: number; expiredCount: number; canceledCount: number }
export interface SfRunNode { nodeId: string; inflow: number; processed: number; processing: number; edgeFlow: Record<string, number> }
export interface SfRunProgress { run: SfRun; nodes: SfRunNode[]; callDispatchedCount: number; smsDispatchedCount: number }
export interface SfVersionRunProgress { run: SfVersionRun; window: { start: string; end: string }; nodes: SfRunNode[]; callDispatchedCount: number; smsDispatchedCount: number }
export interface SfMockStep { delayMs: number; status: string; failureReason?: string | null; data?: Record<string, unknown> }
export type SfPlanStatus = 'PENDING' | 'DEAD' | 'DONE'
export interface SfActionPlan { actionCode: string; runCode: string; nodeId: string; entryCode: string; channel: string; outcomeKey: string; outcomeLabel?: string | null; selectionMode?: string | null; matchedRule?: string | null; selectedWeight?: number | null; totalWeight?: number | null; baseMs?: number | null; idx?: number | null; steps?: SfMockStep[] | null; status: SfPlanStatus; nextDueAt: string; retryCount: number; lastError?: string | null }
export interface SfDecision {
  actionCode: string; runCode: string; nodeId: string; entryCode: string; channel: string
  caseKey: string; caseName?: string | null; selectionMode?: string | null; matchedRule?: string | null
  selectedWeight?: number | null; totalWeight?: number | null; status: SfPlanStatus; noReceipt: boolean
  expectedVars: Record<string, unknown>; expectedPort?: string | null
  actualVars: Record<string, unknown>; actualPort?: string | null; routed: boolean
  selectedAt?: string | null; completedAt?: string | null; lastError?: string | null
}
export interface SfDecisionPage { records: SfDecision[]; total: number; pageNo: number; pageSize: number }
export interface SfImportPlan { defCode: string; versionCode: string; result: number; runCode: string; failFields?: string[] | null }
export interface SfImportFieldError { fieldKey: string; reason: string; itemIndex?: number | null; maxLength?: number | null; actualLength?: number | null; maxScale?: number | null; actualScale?: number | null }
export interface SfImportRowError { rowNo: number; errors: SfImportFieldError[] }
export interface SfImportFailureData { total: number; success: number; fail: number; errors: SfImportRowError[]; errorsTruncated: boolean }
export interface SfImportResult { code: string; batchCode: string; collectionCode: string; status: number; total: number; success: number; fail: number; plans: SfImportPlan[]; errors: SfImportRowError[]; errorsTruncated: boolean }
export interface SfImportRow {
  phone: string
  businessId?: string | null
  ticketId?: string | null
  orderId?: string | null
  userId?: string | null
  bizFields?: Record<string, unknown>
}
// import result 码：1=已生成 run / 2=字段契约失败(failFields) / 3=无可用绑定
export const SF_IMPORT_RUN_CREATED = 1
export const SF_IMPORT_FIELD_FAIL = 2
export const SF_IMPORT_NO_BINDING = 3
