// 后端 REST 封装（仅请求函数与请求参数内联类型；实体/响应类型统一在 ./types）。

export type * from './types'
import type {
  AudioFile, TraceSession, Overview,
  ManagedAgent, ManagedAgentFilter, AddAgentReq, UpdateAgentReq,
  TestRun, PreflightResp, BootstrapResult, ScenarioResult,
  CallRecordPage, CallRecordFilter, AgentCallRecord,
  BehaviorProfile, CustomerGroup, CustomerOverride, LineBinding,
  OrgConfig, OrgsResp, TtsVoice, AgentGroupAgg, CallbackRecord,
} from './types'

const base = '/api'

export async function listAudio(): Promise<AudioFile[]> {
  const r = await fetch(`${base}/audio`)
  if (!r.ok) throw new Error(`listAudio: ${r.status}`)
  return r.json()
}

export async function uploadAudio(file: File): Promise<{ name?: string }> {
  const fd = new FormData()
  fd.append('file', file)
  const r = await fetch(`${base}/audio`, { method: 'POST', body: fd })
  if (!r.ok) throw new Error(`uploadAudio: ${r.status}`)
  return r.json().catch(() => ({}))
}

// getTraceSessions 取会话**摘要**列表（不含 events，后端瘦身后只回 id/title/kind/callId/legs/eventCount）。
// 列表轮询用它；要事件梯形图走 getTraceSession(id) 单查，坐席软电话找自己那条走 findTraceSession(token)。
export async function getTraceSessions(): Promise<TraceSession[]> {
  const r = await fetch(`${base}/trace/sessions`)
  if (!r.ok) throw new Error(`traceSessions: ${r.status}`)
  return r.json()
}

// getTraceSession 取单条会话的**完整**轨迹（含 events），供 CallTracePage 选中后渲染梯形图。
export async function getTraceSession(id: string): Promise<TraceSession> {
  const r = await fetch(`${base}/trace/sessions/${encodeURIComponent(id)}`)
  if (!r.ok) throw new Error(`traceSession: ${r.status}`)
  return r.json()
}

// findTraceSession 让服务端按 token 子串匹配（复刻旧 sessionMatchesCall），回匹配到的完整 session（含 events）。
// 坐席软电话用 jssip callId 找「自己这通」对应的 trace，避免每张卡每轮拉全量列表再前端 find。
export async function findTraceSession(token: string): Promise<TraceSession[]> {
  const r = await fetch(`${base}/trace/sessions?match=${encodeURIComponent(token)}`)
  if (!r.ok) throw new Error(`findTraceSession: ${r.status}`)
  return r.json()
}

export async function getOverview(): Promise<Overview> {
  const r = await fetch(`${base}/hermes/overview`)
  if (!r.ok) throw new Error(`overview: ${r.status}`)
  return r.json()
}

export const listManagedAgents = (f?: ManagedAgentFilter) => {
  const q = new URLSearchParams()
  Object.entries(f || {}).forEach(([k, v]) => { if (v !== undefined && v !== '') q.set(k, String(v)) })
  return getJSON<{ agents: ManagedAgent[]; total: number }>(`/agents/managed${q.toString() ? `?${q}` : ''}`)
}
export const addManagedAgent = (req: AddAgentReq) => postJSON<ManagedAgent>('/agents/managed', req)
export const updateManagedAgent = async (req: UpdateAgentReq): Promise<{ ok: boolean }> => {
  const r = await fetch(`${base}/agents/managed`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(req),
  })
  if (!r.ok) { const e = await r.json().catch(() => ({})); throw new Error((e as { error?: string }).error || `update: ${r.status}`) }
  return r.json()
}
export const deleteManagedAgent = (number: string) => delJSON(`/agents/managed/${encodeURIComponent(number)}`)
export const setManagedAgentEnabled = (agentCodes: string[], enabled: boolean) =>
  postJSON<{ ok: boolean }>('/agents/managed/enabled', { agentCodes, enabled })
export const switchManagedAgentStatus = (number: string, status: string) =>
  postJSON<{ ok: boolean; number: string; status: string }>('/agents/managed/status', { number, status })

// ===== Hermes 重点通话场景：call-center 群呼 / call-bot 任务 / OTP / 线路呼叫 =====

export const getPreflight = () => getJSON<PreflightResp>('/tests/preflight')

export async function bootstrapDemo(p?: { provisionLine?: boolean; customerCount?: number; agentCount?: number }): Promise<{ ok?: boolean; result?: BootstrapResult; error?: string }> {
  const r = await fetch(`${base}/tests/bootstrap`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p || {}),
  })
  return r.json()
}


export async function runCallCenterTask(p: {
  orgCode?: string; name: string; customerGroup?: string; customerLimit?: number; numbers?: string[]; agentGroups?: string[]; agentGroupCodes?: string[]; agentNumbers?: string[]
  ttsCode?: string; ttsText?: string; observeAgent?: string
  modeStrategy?: number; proportion?: number; lossRate?: number; historicalConnectionRate?: number
  sortMethod?: number; isPriorityTask?: boolean; isVmHangup?: boolean
  maxRedialTimes?: number; redialInterval?: number; bestRingDuration?: number; agentMaxRingDuration?: number
  assignDelaySeconds?: number; transferType?: string; description?: string
  startDate?: string; endDate?: string; dialTimePeriod?: string[]; lineType?: string; waitSec?: number
}): Promise<TestRun> {
  const r = await fetch(`${base}/tests/callcenter-task`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p),
  })
  return r.json()
}

// 群呼任务生命周期管理（taskCode 来自 runCallCenterTask 结果的 artifacts.taskCode）。
// createAndImport 后即自动拨号；以下用于运行期暂停/恢复/取消/查状态。
export interface TaskActionResp { ok?: boolean; taskCode?: string; data?: unknown; error?: string }

const callCenterTaskAction = async (taskCode: string, action: 'pause' | 'resume' | 'cancel'): Promise<TaskActionResp> => {
  const r = await fetch(`${base}/tests/callcenter-task/${encodeURIComponent(taskCode)}/${action}`, { method: 'POST' })
  return r.json()
}
export const pauseCallCenterTask = (taskCode: string) => callCenterTaskAction(taskCode, 'pause')
export const resumeCallCenterTask = (taskCode: string) => callCenterTaskAction(taskCode, 'resume')
export const cancelCallCenterTask = (taskCode: string) => callCenterTaskAction(taskCode, 'cancel')
export const getCallCenterTaskStatus = async (taskCode: string): Promise<TaskActionResp> => {
  const r = await fetch(`${base}/tests/callcenter-task/${encodeURIComponent(taskCode)}/status`)
  return r.json()
}

export async function runCallBotTask(p: {
  name: string; taskType?: number; robotCode?: string; salesScriptCode?: string
  customerGroup?: string; customerLimit?: number; numbers?: string[]; waitSec?: number
}): Promise<TestRun> {
  const r = await fetch(`${base}/tests/callbot`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p),
  })
  return r.json()
}

export async function runAutoCall(p: {
  templateCode: string; customerGroup?: string; customerLimit?: number; numbers?: string[]; ttsVars?: Record<string, string>; waitSec?: number
}): Promise<TestRun> {
  const r = await fetch(`${base}/tests/autocall`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p),
  })
  return r.json()
}

// 测试运行历史（近期触发的业务测试 run）
export const testRuns = () => getJSON<TestRun[]>('/tests/runs')

export async function runOTPBatch(p: {
  customerGroup?: string; customerLimit?: number; numbers?: string[]; templateCode: string
  params?: Record<string, string>; waitSec?: number; concurrent?: boolean
}): Promise<ScenarioResult> {
  const r = await fetch(`${base}/tests/otp-batch`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p),
  })
  return r.json()
}

export const queryCallRecords = (f?: CallRecordFilter) => {
  const q = new URLSearchParams()
  Object.entries(f || {}).forEach(([k, v]) => {
    if (v !== undefined && v !== '') q.set(k, String(v))
  })
  return getJSON<CallRecordPage>(`/call-records${q.toString() ? `?${q}` : ''}`)
}

export const saveAgentCallRecord = (r: AgentCallRecord) =>
  postJSON<{ ok: boolean; recordId: string }>('/call-records', r)

async function getJSON<T>(path: string): Promise<T> {
  const r = await fetch(`${base}${path}`)
  if (!r.ok) {
    const e = await r.json().catch(() => ({}))
    throw new Error((e as { error?: string }).error || `${path}: ${r.status}`)
  }
  return r.json()
}
async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const r = await fetch(`${base}${path}`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  })
  if (!r.ok) {
    const e = await r.json().catch(() => ({}))
    throw new Error((e as { error?: string }).error || `${path}: ${r.status}`)
  }
  return r.json()
}

export const listProfiles = () => getJSON<BehaviorProfile[]>('/cluster/profiles')
export const upsertProfile = (p: BehaviorProfile) => postJSON<BehaviorProfile>('/cluster/profiles', p)
export const listGroups = () => getJSON<CustomerGroup[]>('/cluster/groups')
export const upsertGroup = (g: CustomerGroup) => postJSON<CustomerGroup>('/cluster/groups', g)
export const listOverrides = () => getJSON<CustomerOverride[]>('/cluster/overrides')
export const upsertOverride = (o: CustomerOverride) => postJSON<CustomerOverride>('/cluster/overrides', o)
export const listBindings = () => getJSON<LineBinding[]>('/cluster/bindings')
export const upsertBinding = (b: LineBinding) => postJSON<LineBinding>('/cluster/bindings', b)

// 删除（DELETE /cluster/{profiles|groups|overrides|bindings}/:key）
const delJSON = async (path: string): Promise<void> => {
  const r = await fetch(`${base}${path}`, { method: 'DELETE' })
  if (!r.ok) {
    const e = await r.json().catch(() => ({}))
    throw new Error((e as { error?: string }).error || `${path}: ${r.status}`)
  }
}
export const deleteProfile = (code: string) => delJSON(`/cluster/profiles/${encodeURIComponent(code)}`)
export const deleteGroup = (code: string) => delJSON(`/cluster/groups/${encodeURIComponent(code)}`)
export const deleteOverride = (number: string) => delJSON(`/cluster/overrides/${encodeURIComponent(number)}`)
export const deleteBinding = (listenPort: number) => delJSON(`/cluster/bindings/${encodeURIComponent(String(listenPort))}`)
export async function clusterResolve(number: string, listenPort?: number): Promise<{ matched: boolean; resolved?: { groupCode: string; profile: BehaviorProfile; disabled: boolean } }> {
  const q = new URLSearchParams({ number, ...(listenPort ? { listenPort: String(listenPort) } : {}) })
  return getJSON(`/cluster/resolve?${q}`)
}

// ===== 客户在线状态控制 =====
export const setGroupState = (code: string, state: string) =>
  postJSON<{ ok: boolean; code: string; state: string }>('/cluster/groups/state', { code, state })
export const setCustomerState = (number: string, groupCode: string, state: string) =>
  postJSON<{ ok: boolean; number: string; state: string }>('/cluster/customer/state', { number, groupCode, state })

export const listOrgs = () => getJSON<OrgsResp>('/orgs')
export const upsertOrg = (o: OrgConfig) => postJSON<OrgConfig>('/orgs', o)
export const deleteOrg = (orgCode: string) =>
  fetch(`/api/orgs/${encodeURIComponent(orgCode)}`, { method: 'DELETE' }).then((r) => r.json())
export const pingOrg = (orgCode?: string) =>
  postJSON<{ ok: boolean; msg?: string; error?: string }>('/orgs/ping', { orgCode })
export const setCurrentOrg = (orgCode: string) =>
  postJSON<{ ok: boolean; current: string }>('/orgs/current', { orgCode })

export const listOrgTts = () => getJSON<{ tts: TtsVoice[]; error?: string }>('/orgs/tts')
export const listOrgAgentGroups = () => getJSON<{ groups: AgentGroupAgg[]; error?: string }>('/orgs/agent-groups')

export const queryCallbacks = (f?: { source?: string; event?: string; orgCode?: string; callUuid?: string; keyword?: string }) => {
  const q = new URLSearchParams(Object.entries(f || {}).filter(([, v]) => v) as [string, string][])
  return getJSON<{ callbacks: CallbackRecord[] }>(`/callbacks?${q}`)
}
