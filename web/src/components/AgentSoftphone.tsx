import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState, type ReactNode } from 'react'
import { Alert, Button, Card, Col, Collapse, Descriptions, Input, InputNumber, Modal, Progress, Radio, Row, Select, Space, Switch, Table, Tag, Timeline, Tooltip, Typography, message } from 'antd'
import { CaretRightOutlined, PhoneOutlined, PoweroffOutlined, ThunderboltOutlined } from '@ant-design/icons'
import SipCall, { SipState } from '../sip'
import SipController, { type CloseInfo } from '../sip/controller'
import { markSipReady } from '../sip/request'
import { setReadyAgents } from '../hooks/useReadyAgents'
import ScenarioRecords from './scenario/ScenarioRecords'
import { PageHeader } from './layout/PageHeader'
import { InfoBanner } from './layout/InfoBanner'
import { clusterResolve, findTraceSession, listManagedAgents, listGroups, listOrgs, saveAgentCallRecord, type BehaviorProfile, type TraceEvent, type TraceSession, type ManagedAgent, type CustomerGroup } from '../api'

const { Text, Paragraph } = Typography

// 卡片对外暴露的命令接口（容器经 ref 调用，实现批量并发派号）。
export interface CardHandle {
  number: string
  isReady: () => boolean // 已连接+注册+空闲，可接受派号
  enqueue: (nums: string[]) => void // 塞入号码队列并启动顺序拨号
  getProgress: () => { total: number; done: number; running: boolean }
  stop: () => void
  connect: () => void
  disconnect: () => void
  setWork: (key: WorkKey) => void
  setRule: (rule: AnswerRule) => void // 应用统一接听规则到本卡
  setOutputMuted: (muted: boolean) => void // 仅静浏览器本地播放，避免并发压测噪音
  getSnapshot: () => CardSnapshot
}
// 卡片状态快照（上报父层做汇总 + 批量操作判断）。
export interface CardSnapshot { connected: boolean; registered: boolean; sipReady: boolean; status: number; phase: CallPhase }
// 全页坐席汇总（供常驻 dock 折叠条显示在线坐席数）
export interface AgentSummary { total: number; connected: number; registered: number; ready: number; incall: number }

type WorkKey = 'idle' | 'resting' | 'busy' | 'autoOut'

const AGENT_STATUS: Record<number, { label: string; color: string }> = {
  1: { label: '离线', color: 'default' },
  2: { label: '在线/空闲', color: 'green' },
  3: { label: '响铃中', color: 'orange' },
  4: { label: '通话中', color: 'blue' },
  5: { label: '呼叫中', color: 'orange' },
  6: { label: '小休', color: 'gold' },
  7: { label: '忙碌', color: 'volcano' },
  8: { label: '整理中', color: 'cyan' },
  9: { label: '自动外呼', color: 'purple' },
}
// 坐席工作态切换（前端按钮 → call-center switchStatus）
const WORK_ACTIONS: { key: WorkKey; label: string }[] = [
  { key: 'idle', label: '示闲' },
  { key: 'resting', label: '小休' },
  { key: 'busy', label: '示忙' },
  { key: 'autoOut', label: '自动外呼' },
]

// 连接阶段（细化单一 connecting 布尔，让卡住时能看出卡在哪一步）。
type ConnPhase = 'offline' | 'connecting' | 'logging' | 'registering' | 'ready' | 'failed'
const CONN_PHASE: Record<ConnPhase, { label: string; color: string }> = {
  offline: { label: '未连接', color: 'default' },
  connecting: { label: '连接 WS…', color: 'processing' },
  logging: { label: '登录中…', color: 'processing' },
  registering: { label: '注册 SIP…', color: 'processing' },
  ready: { label: '就绪', color: 'green' },
  failed: { label: '失败', color: 'red' },
}

type CallPhase = 'idle' | 'calling' | 'incall'
type CallStatus = '待外呼' | '呼叫中' | '振铃(被叫)' | '已接通' | '已结束' | '失败'

// 期望行为档（取号时经 clusterResolve 解析，结束时据此断言坐席侧实际结果）。
interface Expectation { groupCode?: string; profile?: BehaviorProfile; disabled?: boolean }
type AssertVerdict = 'pass' | 'fail' | 'unknown'

interface CurrentCall {
  id: string; sessionId: string; agent: string; customer: string; displayCaller?: string; inbound?: boolean
  status: CallStatus; startedAt: number; answeredAt?: number; endedAt?: number; endCause?: string; endCode?: number; traceId?: string
  expect?: Expectation; verdict?: AssertVerdict; verdictReason?: string
}

// 接听规则（坐席被叫，如群呼转接来电时自动响应）
interface AnswerRule {
  enabled: boolean
  ringSec: number      // 振铃多少秒后触发动作
  action: 'answer' | 'reject' // 自动接听 / 自动拒接
  probability: number  // 命中概率 %（0-100）
  talkSec: number      // answer 后自动挂断时长（0=不自动挂）
}
const DEFAULT_RULE: AnswerRule = { enabled: true, ringSec: 2, action: 'answer', probability: 100, talkSec: 0 }
const DEFAULT_DISPATCH_GAP_MS = 500

const traceDir: Record<string, string> = { IN: '入', OUT: '出', '-': '·' }
function msText(ms?: number) { return !ms || ms < 0 ? '-' : `${(ms / 1000).toFixed(1)}s` }
// mm:ss 计时（紧凑卡片通话/振铃时长展示，对齐 Figma「00:42」）
function mmss(ms?: number) {
  if (!ms || ms < 0) return '00:00'
  const s = Math.floor(ms / 1000)
  return `${String(Math.floor(s / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`
}
// wsBrief 把 hermes-ws 推送内容压成短字符串（截断），用于日志展示。
function wsBrief(v: unknown): string {
  try { const s = typeof v === 'string' ? v : JSON.stringify(v); return s.length > 120 ? `${s.slice(0, 120)}…` : s } catch { return String(v) }
}
function shortId(s?: string) { return s ? (s.length > 18 ? `${s.slice(0, 18)}…` : s) : '-' }
function hdr(headers: { name: string; value: string }[] | undefined, name: string) {
  return headers?.find((h) => h.name.toLowerCase() === name.toLowerCase())?.value || ''
}
function sipUser(v?: string) { const m = (v || '').match(/sip:([^@>]+)/i); return m?.[1] || '' }
function findDisplayCaller(s?: TraceSession) {
  const inv = s?.events?.find((e) => e.channel === 'SIP' && e.method === 'INVITE'); return sipUser(hdr(inv?.headers, 'From')) || undefined
}
function compactEvents(s?: TraceSession): TraceEvent[] {
  return (s?.events || []).filter((e) => e.channel === 'SIP' || e.channel === 'FLOW' || e.channel === 'BRIDGE').slice(0, 8)
}
function parseNumbers(s?: string): string[] {
  if (!s) return []; return Array.from(new Set(s.split(/[,，\s]+/).map((x) => x.trim()).filter(Boolean)))
}
function sanitizeDtmf(s: string): string { return (s || '').replace(/[^0-9*#abcdABCD]/g, '').toUpperCase() }
function outcomeLabel(o?: string): string {
  return ({ ANSWER: '接听', REJECT: '拒接', BUSY: '忙线', NO_ANSWER: '振铃不接', UNAVAILABLE: '不可达', BRIDGE: '桥接' } as Record<string, string>)[o || ''] || o || '?'
}

// assertCall 据期望行为档判定坐席侧实际 SIP 结果是否符合预期。
// 坐席腿是 call-center 经 FS bridge 上来的：客户(mock)按行为档应答 → 坐席侧观察到接通/挂断。
function assertCall(c: CurrentCall): { verdict: AssertVerdict; reason: string } {
  const o = c.expect?.profile?.outcome
  if (c.expect?.disabled) {
    // 客户组/个例被禁用 → mock 回 503 拒接，坐席侧应为未接通
    return c.status === '失败' ? { verdict: 'pass', reason: '客户已禁用，坐席侧未接通 ✓' } : { verdict: 'fail', reason: '客户已禁用却接通了' }
  }
  if (!o) return { verdict: 'unknown', reason: '无期望行为档' }
  const answered = c.status === '已结束' || c.status === '已接通'
  switch (o) {
    case 'ANSWER':
    case 'BRIDGE':
      return answered ? { verdict: 'pass', reason: `期望${outcomeLabel(o)}，坐席侧已接通 ✓` } : { verdict: 'fail', reason: `期望${outcomeLabel(o)}却未接通(${c.endCause || '-'})` }
    case 'REJECT':
    case 'BUSY':
    case 'NO_ANSWER':
    case 'UNAVAILABLE':
      return !answered ? { verdict: 'pass', reason: `期望${outcomeLabel(o)}，坐席侧未接通 ✓` } : { verdict: 'fail', reason: `期望${outcomeLabel(o)}却接通了` }
    default:
      return { verdict: 'unknown', reason: `未知 outcome ${o}` }
  }
}

// 单坐席卡片：各持独立 SipController + SipCall 实例（多坐席同页互不干扰）。
// forwardRef 暴露 CardHandle 供容器批量并发派号 + 批量连接/工作态。
const SoftphoneCard = forwardRef<CardHandle, {
  agent: ManagedAgent; password: string; groups: CustomerGroup[]; collapsed: boolean; initialRule?: AnswerRule
  dispatchGapMs: number; outputMuted: boolean
  onRemove: (number: string) => void; onSnapshot: (number: string, s: CardSnapshot) => void
}>(function SoftphoneCard({ agent, password, groups, collapsed, initialRule, dispatchGapMs, outputMuted, onRemove, onSnapshot }, ref) {
  const num = agent.number || ''
  const [connected, setConnected] = useState(false)
  const [registered, setRegistered] = useState(false)
  const [sipReady, setSipReady] = useState(false)
  const [connPhase, setConnPhase] = useState<ConnPhase>('offline')
  const [agentStatus, setAgentStatus] = useState(1)
  const [phase, setPhase] = useState<CallPhase>('idle')
  const [peer, setPeer] = useState('')
  const [muted, setMuted] = useState(false)
  const [held, setHeld] = useState(false)
  const [dtmf, setDtmf] = useState('')
  const [logs, setLogs] = useState<string[]>([])
  const [currentCall, setCurrentCall] = useState<CurrentCall | null>(null)
  const [trace, setTrace] = useState<TraceSession | undefined>()
  const [callees, setCallees] = useState('')
  const [takeCount, setTakeCount] = useState(3)
  const [lineType, setLineType] = useState('') // 外呼线路类型（X-JLineType 头；空=不发，Hermes 默认 base）
  const [rule, setRule] = useState<AnswerRule>(initialRule || DEFAULT_RULE)
  const [, setNowTick] = useState(0) // 通话/振铃中每秒触发重渲，让 MiniCall mm:ss 走字
  const ctrlRef = useRef<SipController | null>(null)
  const callRef = useRef<SipCall | null>(null)
  const cardRootRef = useRef<HTMLDivElement>(null) // 卡片根 DOM：判可见性（常驻软电话切到别页 display:none 时 offsetParent 为 null）
  const sipReadyTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const queueRef = useRef<{ list: string[]; idx: number; running: boolean }>({ list: [], idx: 0, running: false })
  const expectRef = useRef<Map<string, Expectation>>(new Map()) // 被叫号 → 期望行为档（取号时解析）
  const lineTypeRef = useRef('') // lineType 镜像（dialNext 定时器闭包里读最新值）
  const dispatchGapMsRef = useRef(DEFAULT_DISPATCH_GAP_MS) // 批量队列呼间隔，避免定时器闭包读旧值
  const ruleRef = useRef<AnswerRule>(DEFAULT_RULE)
  const ringTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const talkTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const ruleAutoAnsweredRef = useRef(false) // 本通是否由接听规则自动接听（决定 IN_CALL 后是否排自动挂断）
  const talkSecRef = useRef(0) // 规则自动接听命中那刻的 talkSec 快照（保证整通参数一致）
  const dialTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null) // 批量队列推进定时器（断开时清，避免越权拨号）
  const destroyedRef = useRef(false) // 卡片已断开/卸载哨兵，阻止异步回调在销毁后建实例/重建定时器
  // state 镜像 ref：供 imperative handle（容器调用）/ 冻结闭包（onSipState）读最新值，避免闭包旧值
  const registeredRef = useRef(false)
  const phaseRef = useRef<CallPhase>('idle')
  const connPhaseRef = useRef<ConnPhase>('offline')
  useEffect(() => { ruleRef.current = rule }, [rule])
  useEffect(() => { lineTypeRef.current = lineType.trim() }, [lineType])
  useEffect(() => { dispatchGapMsRef.current = Math.max(0, dispatchGapMs || 0) }, [dispatchGapMs])
  useEffect(() => { callRef.current?.setOutputMuted(outputMuted) }, [outputMuted])
  useEffect(() => { registeredRef.current = registered }, [registered])
  useEffect(() => { phaseRef.current = phase }, [phase])
  useEffect(() => { connPhaseRef.current = connPhase }, [connPhase])
  // 通话/振铃进行中：每秒重渲让 MiniCall 的 mm:ss 走字（隐藏标签页跳过；空闲不开计时）
  useEffect(() => {
    if (phase === 'idle' || !currentCall || currentCall.endedAt) return
    const t = setInterval(() => { if (!document.hidden) setNowTick((n) => n + 1) }, 1000)
    return () => clearInterval(t)
  }, [phase, currentCall?.id, currentCall?.endedAt])
  // 向父层上报状态快照（汇总 + 批量操作的就绪判断）
  useEffect(() => { onSnapshot(num, { connected, registered, sipReady, status: agentStatus, phase }) },
    [num, connected, registered, sipReady, agentStatus, phase]) // eslint-disable-line

  const pushLog = (s: string) => setLogs((l) => [`${new Date().toLocaleTimeString()} ${s}`, ...l].slice(0, 40))

  const clearRuleTimers = () => {
    if (ringTimerRef.current) { clearTimeout(ringTimerRef.current); ringTimerRef.current = null }
    if (talkTimerRef.current) { clearTimeout(talkTimerRef.current); talkTimerRef.current = null }
  }
  const clearSipReadyTimer = () => {
    if (sipReadyTimer.current) { clearTimeout(sipReadyTimer.current); sipReadyTimer.current = null }
  }

  const refreshTrace = async (call: CurrentCall) => {
    const token = call.id || call.sessionId
    if (!token) return
    try {
      // 服务端按 token（jssip callId）子串匹配，回完整单条（含 events）；不再每张卡每轮拉全量列表再前端 find。
      const hit = (await findTraceSession(token))[0]
      if (hit) {
        const dc = findDisplayCaller(hit)
        setTrace(hit)
        setCurrentCall((c) => c && c.id === call.id ? { ...c, traceId: hit.id, displayCaller: dc || c.displayCaller } : c)
      }
    } catch { /* ignore */ }
  }
  useEffect(() => {
    if (!currentCall || currentCall.status === '待外呼') return
    const c = currentCall
    void refreshTrace(c)
    // 终态（已结束/失败）：trace 已基本定型，补刷几次即停。非终态：设上限（~40 次≈100s），不再 Infinity 永久空转。
    const terminal = c.status === '已结束' || c.status === '失败'
    let left = terminal ? 3 : 40
    const t = setInterval(() => {
      // 常驻软电话切到别页时容器 display:none → offsetParent 为 null，跳过（不递减不轮询，回到本页继续）。
      if (document.hidden || cardRootRef.current?.offsetParent === null) return
      if (left-- <= 0) { clearInterval(t); return }
      void refreshTrace(c)
    }, 2500)
    return () => clearInterval(t)
  }, [currentCall?.id, currentCall?.status])

  const readyAndOnline = async () => {
    try {
      const ok = await markSipReady(num, password)
      setSipReady(ok)
      if (ok) pushLog('SIP-ready 已标记')
      else { pushLog('SIP-ready 失败：call-center 不认坐席就绪，无法被分配/外呼'); message.error(`坐席 ${num} SIP-ready 失败（无法被外呼分配）`) }
    } catch { setSipReady(false); pushLog('SIP-ready 异常'); message.error(`坐席 ${num} SIP-ready 异常`) }
    if (destroyedRef.current) return // await 期间已断开/卸载：不再重建定时器（避免泄漏）
    // 函数式更新：若 await 期间 REGISTER_FAILED 已置 'failed'，不要覆盖回 'ready'（闭包旧值会失效，故用 prev）
    setConnPhase((prev) => prev === 'failed' ? prev : 'ready')
    clearSipReadyTimer()
    // 多坐席同时连接时固定 interval 会相位对齐、每 30s 同秒爆发 N 次保活请求；改用自调度 setTimeout +
    // 随机首延迟(0–30s) 把各卡心跳在 30s 窗口内均匀错开。周期仍 30s（< call-center SIP onlineSipClient 的 45s TTL）。
    const SIP_KEEPALIVE_MS = 30000
    const keepalive = () => {
      if (destroyedRef.current) return
      markSipReady(num, password).then(setSipReady).catch(() => setSipReady(false))
      sipReadyTimer.current = setTimeout(keepalive, SIP_KEEPALIVE_MS)
    }
    sipReadyTimer.current = setTimeout(keepalive, Math.floor(Math.random() * SIP_KEEPALIVE_MS))
    try { await callRef.current?.setIdle(); pushLog('已切在线/空闲') } catch { /* ignore */ }
  }

  // 被叫来电按接听规则自动响应（群呼转接等）。命中自动接听时只 answer()，
  // 「通话 N 秒后自动挂断」改到 IN_CALL（接通）后再排程——否则 IN_CALL 的 clearRuleTimers 会先把它清掉。
  const applyAnswerRule = () => {
    const r = ruleRef.current
    if (!r.enabled) return
    clearRuleTimers()
    ruleAutoAnsweredRef.current = false
    ringTimerRef.current = setTimeout(() => {
      const hit = Math.random() * 100 < r.probability
      if (!hit) { pushLog(`接听规则未命中(${r.probability}%)，不动作`); return }
      if (r.action === 'reject') {
        pushLog('接听规则：自动拒接'); try { callRef.current?.hangup() } catch { /* ignore */ }
      } else {
        ruleAutoAnsweredRef.current = true
        talkSecRef.current = r.talkSec // 命中那刻快照 talkSec，IN_CALL 后据此排自动挂断（整通参数一致）
        pushLog('接听规则：自动接听'); try { callRef.current?.answer() } catch { /* ignore */ }
      }
    }, Math.max(0, r.ringSec) * 1000)
  }

  const dialNext = () => {
    const q = queueRef.current
    if (destroyedRef.current || !q.running) return
    if (q.idx >= q.list.length) { q.running = false; pushLog(`本卡批量完成（${q.list.length}）`); return }
    const customer = q.list[q.idx]
    try {
      const callId = callRef.current?.call(customer, lineTypeRef.current ? { lineType: lineTypeRef.current } : {})
      if (callId) {
        const call: CurrentCall = { id: callId, sessionId: `CCMDL${callId}`, agent: num, customer, status: '呼叫中', startedAt: Date.now(), expect: expectRef.current.get(customer) }
        setCurrentCall(call); setTrace(undefined); void refreshTrace(call)
        pushLog(`外呼 ${customer}（${q.idx + 1}/${q.list.length}）`)
      } else {
        // call() 未注册时返回空串、不触发 CALL_END，队列会卡死——主动跳过继续下一个
        pushLog(`外呼 ${customer} 未发起（坐席未就绪），跳过`); q.idx += 1; dialTimerRef.current = setTimeout(dialNext, dispatchGapMsRef.current)
      }
    } catch (e) { pushLog(`外呼 ${customer} 失败：${String(e)}`); q.idx += 1; dialTimerRef.current = setTimeout(dialNext, dispatchGapMsRef.current) }
  }

  const onSipState = (event: string, data: unknown) => {
    pushLog(`SIP ${event}`)
    switch (event) {
      case SipState.CONNECTED: if (connPhaseRef.current === 'connecting') setConnPhase('logging'); break
      case SipState.REGISTERED: setRegistered(true); setConnPhase('registering'); void readyAndOnline(); break
      case SipState.UNREGISTERED:
        setRegistered(false); setSipReady(false); clearSipReadyTimer(); break
      case SipState.REGISTER_FAILED: {
        setRegistered(false); setSipReady(false); setConnPhase('failed'); clearSipReadyTimer()
        const msg = (data as { msg?: string })?.msg || 'SIP 注册失败'
        const cfgLike = /WebRTC|callCenterUrl|gatewayUrl|agent-workbench|响应不是 JSON|返回了 HTML|HTTP/i.test(msg)
        pushLog(msg)
        message.error(`${num}: ${msg}${cfgLike ? '' : '（请确认该分机已在 FreeSWITCH directory 配置）'}`)
        break
      }
      case SipState.OUTGOING_CALL: {
        const d = data as { otherLegNumber?: string }
        setPeer(d?.otherLegNumber || ''); setPhase('calling')
        setCurrentCall((c) => c ? { ...c, status: '呼叫中' } : c); break
      }
      case SipState.INCOMING_CALL: {
        const d = data as { otherLegNumber?: string; callId?: string }
        setPeer(d?.otherLegNumber || ''); setPhase('calling')
        const inb: CurrentCall = { id: d?.callId || `in-${Date.now()}`, sessionId: d?.callId || '', agent: num, customer: d?.otherLegNumber || '', inbound: true, status: '振铃(被叫)', startedAt: Date.now() }
        setCurrentCall(inb); setTrace(undefined); void refreshTrace(inb)
        applyAnswerRule() // 被叫来电 → 按规则自动接/拒
        break
      }
      case SipState.IN_CALL:
        clearRuleTimers() // 已接通 → 取消待触发的振铃定时器
        // 接通后若本通是规则自动接听且配了 talkSec，再排「通话 N 秒自动挂断」（读命中那刻的快照值）
        if (ruleAutoAnsweredRef.current && talkSecRef.current > 0) {
          const sec = talkSecRef.current
          talkTimerRef.current = setTimeout(() => { pushLog(`接听规则：通话 ${sec}s 后自动挂断`); try { callRef.current?.hangup() } catch { /* ignore */ } }, sec * 1000)
        }
        ruleAutoAnsweredRef.current = false
        setPhase('incall'); setMuted(false); setHeld(false)
        setCurrentCall((c) => c ? { ...c, status: '已接通', answeredAt: Date.now() } : c); break
      case SipState.CALL_END: {
        const d = data as { cause?: string; answered?: boolean; code?: number }
        clearRuleTimers(); ruleAutoAnsweredRef.current = false
        setPhase('idle'); setPeer(''); setMuted(false); setHeld(false)
        setCurrentCall((c) => {
          if (!c) return c
          const ended: CurrentCall = { ...c, status: d?.answered ? '已结束' : '失败', endedAt: Date.now(), endCause: d?.cause, endCode: d?.code }
          const durationMs = (ended.endedAt || Date.now()) - (ended.answeredAt || ended.startedAt)
          if (!ended.inbound) {
            if (ended.expect) {
              const a = assertCall(ended); ended.verdict = a.verdict; ended.verdictReason = a.reason
              pushLog(`断言：${a.reason}`)
            }
            // 回存坐席侧记录 + 断言到后端（坐席外呼可在「呼叫记录」页查到、可回归）
            saveAgentCallRecord({
              callId: ended.id, agentNumber: num, customer: ended.customer,
              expectOutcome: ended.expect?.profile?.outcome, expectFault: ended.expect?.profile?.fault,
              expectDisabled: ended.expect?.disabled, answered: !!d?.answered, endCause: ended.endCause,
              verdict: ended.verdict, verdictReason: ended.verdictReason,
              traceId: ended.traceId, displayCaller: ended.displayCaller,
              startedAtMs: ended.startedAt, answeredAtMs: ended.answeredAt, durationMs,
            }).catch(() => { /* 记录失败不影响通话 */ })
          } else {
            // 被叫来电（群呼/转接进来的坐席腿）：回存「坐席接听」记录，供群呼 run 断言坐席侧是否接通。
            saveAgentCallRecord({
              callId: ended.id, agentNumber: num, customer: ended.customer || ended.displayCaller,
              answered: !!d?.answered, endCause: ended.endCause, inbound: true,
              traceId: ended.traceId, displayCaller: ended.displayCaller,
              startedAtMs: ended.startedAt, answeredAtMs: ended.answeredAt, durationMs,
            }).catch(() => { /* ignore */ })
          }
          return ended
        })
        const q = queueRef.current
        if (q.running) { q.idx += 1; dialTimerRef.current = setTimeout(dialNext, dispatchGapMsRef.current) }
        break
      }
      case SipState.HOLD: setHeld(true); break
      case SipState.UNHOLD: setHeld(false); break
      case SipState.MUTE: setMuted(true); break
      case SipState.UNMUTE: setMuted(false); break
      case SipState.MIC_ERROR:
      case SipState.ERROR: message.warning((data as { msg?: string })?.msg || event); break
    }
  }

  const connect = () => {
    if (ctrlRef.current) return // 已在连接/已连接，避免重复建实例
    destroyedRef.current = false // 重新连接：清哨兵
    setConnPhase('connecting'); pushLog(`为坐席 ${num} 建立软电话 …`)
    try {
      ctrlRef.current = new SipController({
        proto: location.protocol === 'https:',
        host: location.hostname,
        port: location.port || (location.protocol === 'https:' ? '443' : '80'),
        username: num, password,
        statusListener: (s: number) => setAgentStatus(s),
        // hermes-ws 推送的非状态消息：有业务价值的进卡片日志，噪音进 console.debug（避免刷前端 UI）。
        groupCallNotify: (v) => pushLog('群呼进度：' + wsBrief(v)),
        callLinkInfo: (v) => {
          // currentCallUuid：hermes 主动下发本通业务 callUuid/callType/客户号——联调时确认 hermes 侧 callId、为关联断言铺路。
          const info = (v || {}) as { callUuid?: string; callType?: string; customerNumber?: string; number?: string }
          pushLog(`hermes 通话下发：callUuid=${info.callUuid || '?'} type=${info.callType || '?'}${info.customerNumber || info.number ? ' 客户=' + (info.customerNumber || info.number) : ''}`)
        },
        callbackInfo: (v) => console.debug('[ws] numberInfo', v),
        otherEvent: (v) => console.debug('[ws] other', v),
        kick: (msg?: string) => {
          message.warning(`坐席 ${num} 被踢下线${msg ? `：${msg}` : '（同号在别处登录）'}`); disconnect()
        },
        onOpenHook: () => { setConnPhase('logging') },
        loginHook: () => {
          // 竞态保护：若 WS 登录回调到来时卡片已断开/卸载，立即销毁刚建的实例，避免幽灵注册
          if (destroyedRef.current) { try { ctrlRef.current?.destroy() } catch { /* ignore */ } return }
          // 去重保护：loginHook 现仅首次 auth 触发（controller 门闸），这里再防御一次
          if (callRef.current) return
          setConnected(true); setConnPhase('registering'); pushLog('工作台 WS 已登录，开始 SIP 注册')
          callRef.current = new SipCall({
            host: location.hostname, port: location.port, proto: false,
            extNo: num, extPwd: password, checkMic: true, autoRegister: true,
            stateEventListener: onSipState, sipController: ctrlRef.current!,
          })
          callRef.current.setOutputMuted(outputMuted)
          if (destroyedRef.current) { try { callRef.current.destroy() } catch { /* ignore */ }; callRef.current = null }
        },
        onCloseHook: (info?: CloseInfo) => {
          setConnected(false); setRegistered(false); setSipReady(false)
          if (!destroyedRef.current) {
            // 非主动断开：WS 异常关闭。清理实例引用，让「连接」按钮可重试（否则 connect 的 ctrlRef 守卫会挡住重连）。
            clearSipReadyTimer()
            if (dialTimerRef.current) { clearTimeout(dialTimerRef.current); dialTimerRef.current = null }
            queueRef.current.running = false
            try { callRef.current?.destroy() } catch { /* ignore */ }
            callRef.current = null; ctrlRef.current = null
            setConnPhase('failed'); setAgentStatus(1); setPhase('idle')
            const reason = info?.reason || `WS 关闭(${info?.code ?? '?'})`
            message.warning(`坐席 ${num} 连接断开：${reason}`); pushLog(`连接断开：${reason}`)
          }
        },
      })
    } catch (e) { setConnPhase('failed'); message.error(String(e)) }
  }

  const disconnect = () => {
    destroyedRef.current = true // 置哨兵：阻止在途异步回调建实例/重建定时器
    clearSipReadyTimer()
    clearRuleTimers()
    if (dialTimerRef.current) { clearTimeout(dialTimerRef.current); dialTimerRef.current = null }
    queueRef.current.running = false
    expectRef.current.clear() // 释放期望行为档缓存（断开即清，下次外呼重新解析）
    try { callRef.current?.destroy(); ctrlRef.current?.destroy() } catch { /* ignore */ }
    callRef.current = null; ctrlRef.current = null
    setConnected(false); setRegistered(false); setSipReady(false); setAgentStatus(1); setPhase('idle'); setPeer(''); setConnPhase('offline')
    pushLog('已断开')
  }
  useEffect(() => () => { disconnect() }, []) // eslint-disable-line

  const switchWork = async (key: WorkKey) => {
    const c = callRef.current
    if (!c) { message.warning('请先连接'); return }
    try {
      if (key === 'idle') await c.setIdle()
      else if (key === 'resting') await c.setResting()
      else if (key === 'busy') await c.setBusy()
      else await c.setAutoOut()
      pushLog(`工作态 → ${key}`)
    } catch (e) { message.error(String(e)) }
  }

  const startCall = async () => {
    if (!registered) { message.warning('坐席未注册'); return }
    const nums = parseNumbers(callees)
    if (!nums.length) { message.warning('请输入被叫号'); return }
    if (phase !== 'idle') { message.warning('当前有通话进行中'); return }
    await resolveExpectations(nums) // 先解析期望行为档，确保首呼也带上 expect（用于结束断言）
    if (destroyedRef.current || phaseRef.current !== 'idle') return // await 期间状态可能变化
    if (nums.length > 1) {
      queueRef.current = { list: nums, idx: 0, running: true }
      pushLog(`顺序外呼 ${nums.length} 个`); dialNext()
    } else {
      // 单呼不进批量队列（否则会在「批量并发派号」总进度面板留下 0/1 残影）
      queueRef.current = { list: [], idx: 0, running: false }
      try {
        const callId = callRef.current?.call(nums[0], lineTypeRef.current ? { lineType: lineTypeRef.current } : {})
        if (callId) {
          const call: CurrentCall = { id: callId, sessionId: `CCMDL${callId}`, agent: num, customer: nums[0], status: '呼叫中', startedAt: Date.now(), expect: expectRef.current.get(nums[0]) }
          setCurrentCall(call); setTrace(undefined); void refreshTrace(call)
        } else message.warning('坐席未就绪，外呼未发起')
        pushLog(`外呼 ${nums[0]}`)
      } catch (e) { message.error(String(e)) }
    }
  }
  const answer = () => { ruleAutoAnsweredRef.current = false; try { callRef.current?.answer() } catch { /* ignore */ } }
  const hangup = () => { try { callRef.current?.hangup() } catch { /* ignore */ } }
  const toggleMute = () => { try { muted ? callRef.current?.unmute() : callRef.current?.mute() } catch { /* ignore */ } }
  const toggleHold = () => { try { held ? callRef.current?.unhold() : callRef.current?.hold() } catch { /* ignore */ } }
  const sendDtmf = () => {
    const tone = sanitizeDtmf(dtmf)
    if (!tone) { message.warning('请输入有效 DTMF（0-9 * #）'); return }
    try { callRef.current?.sendDtmf(tone); pushLog(`发送 DTMF ${tone}`); message.success(`已发送 ${tone}`); setDtmf('') } catch { /* ignore */ }
  }
  // 取号：拼号 + 经 clusterResolve 解析每个号命中的行为档（期望 outcome）
  const resolveExpectations = async (nums: string[]) => {
    await Promise.all(nums.map(async (n) => {
      if (expectRef.current.has(n)) return
      try {
        const r = await clusterResolve(n)
        if (r.matched && r.resolved) expectRef.current.set(n, { groupCode: r.resolved.groupCode, profile: r.resolved.profile, disabled: r.resolved.disabled })
      } catch { /* ignore */ }
    }))
  }
  const fillFromGroup = (groupCode: string) => {
    const g = groups.find((x) => x.code === groupCode); if (!g) return
    const start = Number(g.numberStart || 0); const count = Math.min(Math.max(1, takeCount), g.count || 1)
    const list: string[] = []; for (let i = 0; i < count; i++) list.push(`${g.numberPrefix || ''}${start + i}`)
    setCallees(list.join(','))
    void resolveExpectations(list)
  }

  // 暴露命令接口给容器（批量并发派号 + 批量连接/工作态）。用 ref 镜像读最新 registered/phase。
  useImperativeHandle(ref, () => ({
    number: num,
    isReady: () => registeredRef.current && phaseRef.current === 'idle' && !queueRef.current.running,
    enqueue: (nums: string[]) => {
      if (!registeredRef.current || !nums.length) return
      queueRef.current = { list: nums, idx: 0, running: true }
      pushLog(`收到派号 ${nums.length} 个，开始外呼`)
      // 先解析期望行为档再拨（首个号也能带 expect 断言）；解析失败也照常拨
      resolveExpectations(nums).finally(() => { if (!destroyedRef.current) dialNext() })
    },
    getProgress: () => ({ total: queueRef.current.list.length, done: queueRef.current.idx, running: queueRef.current.running }),
    stop: () => { queueRef.current.running = false },
    connect, disconnect, setWork: (k: WorkKey) => { void switchWork(k) },
    setRule: (r: AnswerRule) => setRule(r),
    setOutputMuted: (muted: boolean) => { callRef.current?.setOutputMuted(muted) },
    getSnapshot: () => ({ connected, registered, sipReady, status: agentStatus, phase }),
  }), [num, connected, registered, sipReady, agentStatus, phase]) // eslint-disable-line

  const st = AGENT_STATUS[agentStatus] || AGENT_STATUS[1]
  const cp = CONN_PHASE[connPhase]
  const incomingRinging = phase === 'calling' && currentCall?.inbound
  const verdictTag = currentCall?.verdict === 'pass'
    ? <Tooltip title={currentCall.verdictReason}><Tag color="green">断言 ✓</Tag></Tooltip>
    : currentCall?.verdict === 'fail'
      ? <Tooltip title={currentCall.verdictReason}><Tag color="red">断言 ✗</Tag></Tooltip> : null

  const statusTags = (
    <Space size={4} wrap>
      <Tag color={connected ? 'green' : 'default'}>WS {connected ? '在线' : '离线'}</Tag>
      <Tag color={registered ? 'green' : 'default'}>SIP {registered ? '已注册' : '未注册'}</Tag>
      <Tooltip title="call-center 是否认为坐席就绪（可被分配/外呼）。失败=无法被群呼/派号选中">
        <Tag color={sipReady ? 'green' : connected ? 'red' : 'default'}>就绪 {sipReady ? '是' : '否'}</Tag>
      </Tooltip>
      {connPhase !== 'offline' && connPhase !== 'ready' && <Tag color={cp.color}>{cp.label}</Tag>}
      <Tag color={st.color}>{st.label}</Tag>
    </Space>
  )
  const headExtra = (
    <Space wrap>
      {statusTags}
      {incomingRinging && <Button size="small" type="primary" onClick={answer}>接听</Button>}
      {!connected && connPhase !== 'connecting' && connPhase !== 'logging' && connPhase !== 'registering'
        ? <Button size="small" type="primary" icon={<PoweroffOutlined />} onClick={connect}>连接</Button>
        : !connected
          ? <Button size="small" type="primary" icon={<PoweroffOutlined />} loading onClick={() => {}}>连接中</Button>
          : <Button size="small" danger icon={<PoweroffOutlined />} onClick={disconnect}>断开</Button>}
      <Button size="small" onClick={() => onRemove(num)}>移除</Button>
    </Space>
  )

  // ===== 紧凑卡片（Figma AgentCard 11:442）派生：head 药丸 / MiniCall / ActionRow 主按钮 =====
  // head 状态药丸：通话中(蓝) / 振铃中(蓝) / 就绪(绿) / 在线(绿) / 连接中(琥珀) / 未连接(灰)
  const pill: { tone: string; label: string } = (() => {
    if (phase === 'incall') return { tone: 'incall', label: '通话中' }
    if (phase === 'calling') return { tone: 'incall', label: incomingRinging ? '来电振铃' : '振铃中' }
    if (connPhase === 'connecting' || connPhase === 'logging' || connPhase === 'registering') return { tone: 'conn', label: cp.label }
    if (sipReady) return { tone: 'ready', label: '就绪' }
    if (connected) return { tone: 'online', label: registered ? '在线' : 'WS在线' }
    if (connPhase === 'failed') return { tone: 'off', label: '失败' }
    return { tone: 'off', label: '未连接' }
  })()
  // MiniCall 内容：通话/振铃时蓝底显当前通话；空闲/离线时灰底显就绪态 + 接听规则摘要
  const ruleBrief = rule.enabled
    ? `接听规则：振铃 ${rule.ringSec}s→${rule.action === 'answer' ? '接听' : '拒接'}${rule.probability < 100 ? `（${rule.probability}%）` : ''}`
    : '接听规则：未启用'
  const elapsed = currentCall ? (currentCall.endedAt || Date.now()) - currentCall.startedAt : 0
  const mini: { tone: 'incall' | 'idle'; off?: boolean; main: ReactNode; sub: ReactNode } = (() => {
    if (currentCall && phase !== 'idle') {
      const ringing = phase === 'calling'
      const dirArrow = currentCall.inbound ? '↘ 来电' : '⇄ 客户'
      const q = queueRef.current
      const dispatch = q.running && q.list.length > 1 ? ` · 派号 ${Math.min(q.idx + 1, q.list.length)}/${q.list.length}` : ''
      const main = <>{dirArrow} {currentCall.customer || currentCall.displayCaller || '-'} · {ringing ? '振铃 ' : ''}{mmss(elapsed)}</>
      const sub = (
        <>
          {ringing ? '等待被叫应答' : 'PCMU 双向'}{dispatch}
          {currentCall.traceId
            ? <> · <a href={`/trace?session=${encodeURIComponent(currentCall.traceId)}`} target="_blank" rel="noopener noreferrer">trace ↗</a></>
            : ' · trace 匹配中'}
        </>
      )
      return { tone: 'incall', main, sub }
    }
    if (!connected) {
      return { tone: 'idle', off: true, main: '离线 · 点「连接」上线', sub: '未注册 FreeSWITCH，无法接派号' }
    }
    return { tone: 'idle', main: '空闲 · 等待派号或外呼', sub: <>{sipReady ? '已就绪' : '注册中'} · {ruleBrief}</> }
  })()
  // ActionRow 主按钮：振铃→接听 / 通话→挂断 / 未连→连接 / 空闲→外呼
  const primaryAction: ReactNode = (() => {
    if (incomingRinging) return <Button size="small" type="primary" onClick={answer}>接听</Button>
    if (phase !== 'idle') return <Button size="small" danger onClick={hangup}>挂断</Button>
    if (!connected) {
      const connecting = connPhase === 'connecting' || connPhase === 'logging' || connPhase === 'registering'
      return <Button size="small" type="primary" loading={connecting} onClick={connect} style={{ background: '#16a34a', borderColor: '#16a34a' }}>{connecting ? '连接中' : '连接'}</Button>
    }
    return <Button size="small" type="primary" disabled={!registered} onClick={startCall}>外呼</Button>
  })()

  const body = (
    <>
      {/* 连接步骤条（Figma ConnSteps）：连接 WS → 登录 → 注册 SIP → 就绪 */}
      <div className="hm-conn-steps">
        {[
          { label: '连接 WS', done: connected },
          { label: '登录', done: connected },
          { label: '注册 SIP', done: registered },
          { label: '就绪', done: sipReady },
        ].map((s, i) => (
          <span key={s.label} style={{ display: 'inline-flex', alignItems: 'center' }}>
            {i > 0 && <span className="hm-conn-step-sep">·</span>}
            <span className={`hm-conn-step ${s.done ? 'is-done' : 'is-pending'}`}>{s.done ? '✓' : '○'} {s.label}</span>
          </span>
        ))}
      </div>
      <Row gutter={12}>
      <Col xs={24} md={12}>
        {/* 工作态切换 */}
        <Space wrap style={{ marginBottom: 8 }}>
          <Text type="secondary" style={{ fontSize: 12 }}>工作态:</Text>
          {WORK_ACTIONS.map((w) => (
            <Button key={w.key} size="small" disabled={!connected} onClick={() => switchWork(w.key)}>{w.label}</Button>
          ))}
        </Space>
        {/* 外呼 */}
        <Space.Compact style={{ width: '100%', marginBottom: 8 }}>
          <Tooltip title="按客户组号段取号，并解析每个号命中的行为档（用于通话结束断言）">
            <Select style={{ width: 130 }} allowClear placeholder="客户组取号" size="small"
              options={groups.map((g) => ({ value: g.code, label: `${g.code}（${g.count || 0}）` }))}
              onChange={(v) => { if (v) fillFromGroup(v) }} />
          </Tooltip>
          <Tooltip title="客户组取号数量"><InputNumber size="small" style={{ width: 60 }} min={1} max={50} value={takeCount} onChange={(v) => setTakeCount(v ?? 1)} /></Tooltip>
          <Tooltip title="线路类型（发 X-JLineType 头，call-center 仅用该 type 线路选号；留空=默认 base）">
            <Input size="small" style={{ width: 90 }} placeholder="lineType" value={lineType} onChange={(e) => setLineType(e.target.value)} disabled={!registered} />
          </Tooltip>
          <Input size="small" placeholder="被叫号(多个逗号分隔)" value={callees} onChange={(e) => setCallees(e.target.value)} disabled={!registered} />
        </Space.Compact>
        <Space wrap style={{ marginBottom: 8 }}>
          <Tooltip title={!registered ? '坐席未注册，无法外呼' : phase !== 'idle' ? '当前有通话进行中' : ''}>
            <Button size="small" type="primary" icon={<PhoneOutlined />} disabled={!registered || phase !== 'idle'} onClick={startCall}>外呼</Button>
          </Tooltip>
          {incomingRinging && <Button size="small" type="primary" onClick={answer}>接听</Button>}
          <Button size="small" danger disabled={phase === 'idle'} onClick={hangup}>挂断</Button>
          <Button size="small" disabled={phase !== 'incall'} onClick={toggleMute}>{muted ? '取消静音' : '静音'}</Button>
          <Button size="small" disabled={phase !== 'incall'} onClick={toggleHold}>{held ? '恢复' : '保持'}</Button>
          {phase !== 'idle' && <Tag color={phase === 'incall' ? 'blue' : 'orange'}>{currentCall?.status}{peer ? ' · ' + peer : ''}</Tag>}
        </Space>
        {phase === 'incall' && (
          <Space.Compact style={{ marginBottom: 8 }}>
            <Input size="small" style={{ width: 120 }} placeholder="DTMF 如 1#" value={dtmf}
              onChange={(e) => setDtmf(sanitizeDtmf(e.target.value))} onPressEnter={sendDtmf} maxLength={32} />
            <Button size="small" onClick={sendDtmf}>发送</Button>
          </Space.Compact>
        )}
        {/* 接听规则（被叫，如群呼转接来电时自动响应） */}
        <div style={{ background: '#fafafa', padding: 8, borderRadius: 4, marginBottom: 8 }}>
          <Space wrap size={6}>
            <Tooltip title="仅对坐席「被叫来电」生效（如群呼/转接时坐席侧收到 INVITE）。改参数对当前正在响铃的这通不生效，下一通生效。">
              <Text type="secondary" style={{ fontSize: 12 }}>接听规则(被叫) ⓘ:</Text>
            </Tooltip>
            <Switch size="small" checked={rule.enabled} onChange={(v) => setRule((r) => ({ ...r, enabled: v }))} />
            {rule.enabled && <>
              <Select size="small" style={{ width: 90 }} value={rule.action} onChange={(v) => setRule((r) => ({ ...r, action: v }))}
                options={[{ value: 'answer', label: '自动接听' }, { value: 'reject', label: '自动拒接' }]} />
              <span style={{ fontSize: 12 }}>振铃<InputNumber size="small" style={{ width: 56 }} min={0} max={60} value={rule.ringSec} onChange={(v) => setRule((r) => ({ ...r, ringSec: v ?? 0 }))} />s</span>
              <span style={{ fontSize: 12 }}>概率<InputNumber size="small" style={{ width: 60 }} min={0} max={100} value={rule.probability} onChange={(v) => setRule((r) => ({ ...r, probability: v ?? 100 }))} />%</span>
              {rule.action === 'answer' && <span style={{ fontSize: 12 }}>通话<InputNumber size="small" style={{ width: 56 }} min={0} max={600} value={rule.talkSec} onChange={(v) => setRule((r) => ({ ...r, talkSec: v ?? 0 }))} />s</span>}
            </>}
          </Space>
        </div>
        {currentCall && (
          <Descriptions size="small" column={1}>
            <Descriptions.Item label="方向">{currentCall.inbound ? <>被叫(来电) <Text type="secondary" style={{ fontSize: 12 }}>· 不参与外呼断言</Text></> : '主叫(外呼)'}</Descriptions.Item>
            <Descriptions.Item label="对端号"><Text code>{currentCall.customer}</Text></Descriptions.Item>
            {currentCall.expect?.profile && (
              <Descriptions.Item label="期望">
                <Tag>{outcomeLabel(currentCall.expect.profile.outcome)}</Tag>
                {currentCall.expect.profile.fault && <Tag color="volcano">{currentCall.expect.profile.fault}</Tag>}
                {currentCall.expect.disabled && <Tag color="red">已禁用</Tag>}
                {typeof currentCall.expect.profile.answerRatio === 'number' && currentCall.expect.profile.answerRatio < 100 && <Text type="secondary" style={{ fontSize: 12 }}>接通率{currentCall.expect.profile.answerRatio}%</Text>}
              </Descriptions.Item>
            )}
            <Descriptions.Item label="外显主叫">{currentCall.displayCaller ? <Text code>{currentCall.displayCaller}</Text> : <Text type="secondary">-</Text>}</Descriptions.Item>
            <Descriptions.Item label="耗时">{msText((currentCall.endedAt || Date.now()) - currentCall.startedAt)}{currentCall.endCause ? ` · ${currentCall.endCause}` : ''}</Descriptions.Item>
            {currentCall.verdict && currentCall.verdict !== 'unknown' && (
              <Descriptions.Item label="断言">{verdictTag}<Text type={currentCall.verdict === 'fail' ? 'danger' : 'secondary'} style={{ fontSize: 12 }}> {currentCall.verdictReason}</Text></Descriptions.Item>
            )}
            <Descriptions.Item label="Trace">{currentCall.traceId ? <a href={`/trace?session=${encodeURIComponent(currentCall.traceId)}`} target="_blank" rel="noopener noreferrer">{shortId(currentCall.traceId)}</a> : <Text type="secondary">匹配中</Text>}</Descriptions.Item>
          </Descriptions>
        )}
      </Col>
      <Col xs={24} md={12}>
        <Collapse size="small" ghost items={[{
          key: 'tracelog',
          label: <Text type="secondary" style={{ fontSize: 12 }}>链路 / 日志</Text>,
          children: (<>
            {trace && (
              <Timeline style={{ maxHeight: 180, overflow: 'auto' }} items={compactEvents(trace).map((e) => ({
                color: e.channel === 'SIP' ? 'blue' : e.channel === 'BRIDGE' ? 'orange' : 'gray',
                children: <div style={{ fontSize: 12 }}><Tag>{e.channel}</Tag>{traceDir[e.dir] || e.dir} {e.method} <Text type="secondary">{e.summary}</Text></div>,
              }))} />
            )}
            <Paragraph style={{ margin: 0, maxHeight: 140, overflow: 'auto', fontSize: 11, fontFamily: 'monospace' }}>
              {logs.length ? logs.map((l, i) => <div key={i}>{l}</div>) : <Text type="secondary">暂无事件</Text>}
            </Paragraph>
          </>),
        }]} />
      </Col>
    </Row>
    </>
  )

  const title = (
    <Space wrap size={4}>
      <Text strong>坐席 {num}</Text>
      {agent.agentName && <Text type="secondary">{agent.agentName}</Text>}
      {phase !== 'idle' && <Tag color={phase === 'incall' ? 'blue' : 'orange'}>{currentCall?.status}{currentCall?.customer ? ' · ' + currentCall.customer : ''}</Tag>}
      {verdictTag}
    </Space>
  )

  if (collapsed) {
    // Figma 紧凑卡片：head（坐席号 + 状态药丸 + ✕）· MiniCall（状态色摘要）· ActionRow（被叫号 + 主按钮）
    return (
      <div ref={cardRootRef} className={`hm-agent-card${currentCall?.verdict === 'fail' ? ' is-fail' : ''}`}>
        <div className="hm-agent-head">
          <div className="hm-agent-head-left">
            <span className="hm-agent-name">坐席 {num}</span>
            {agent.agentName && <span className="hm-agent-subname">{agent.agentName}</span>}
            <span className={`hm-agent-pill tone-${pill.tone}`}><span className="dot" />{pill.label}</span>
            {verdictTag}
          </div>
          <button className="hm-agent-remove" title="移除" onClick={() => onRemove(num)}>✕</button>
        </div>
        <div className="hm-agent-body">
          <div className={`hm-minicall tone-${mini.tone}${mini.off ? ' is-off' : ''}`}>
            <div className="hm-minicall-main">{mini.main}</div>
            <div className="hm-minicall-sub">{mini.sub}</div>
          </div>
          <Space.Compact style={{ width: '100%' }}>
            <Input size="small" placeholder="被叫客户号码" value={callees} disabled={!registered || phase !== 'idle'}
              onChange={(e) => setCallees(e.target.value)} onPressEnter={() => { if (registered && phase === 'idle') void startCall() }} />
            {primaryAction}
          </Space.Compact>
        </div>
      </div>
    )
  }
  return (
    <div ref={cardRootRef}>
      <Card size="small" style={{ ...(currentCall?.verdict === 'fail' ? { borderColor: '#ff4d4f' } : {}) }} title={title} extra={headExtra}>{body}</Card>
    </div>
  )
})

export default function AgentSoftphone({ onSummary }: { onSummary?: (s: AgentSummary) => void } = {}) {
  const [agents, setAgents] = useState<ManagedAgent[]>([])
  const [groups, setGroups] = useState<CustomerGroup[]>([])
  const [picked, setPicked] = useState<string[]>([])
  const [active, setActive] = useState<{ agent: ManagedAgent; password: string }[]>([]) // 口令随卡片快照
  const [orgPwd, setOrgPwd] = useState('') // 当前机构「新坐席默认口令」（坐席接口未回带口令时兜底）
  const [loadingMeta, setLoadingMeta] = useState(false)
  const [collapsed, setCollapsed] = useState(false) // 卡片默认展开，便于直接查看链路/日志与接听规则
  // 选择区筛选（搜索 + 技能组 + 状态）（B）
  const [agentQuery, setAgentQuery] = useState('')
  const [filterGroup, setFilterGroup] = useState<string | undefined>()
  const [filterStatus, setFilterStatus] = useState<number | undefined>()
  // 批量并发派号
  const cardRefs = useRef<Map<string, CardHandle | null>>(new Map())
  const [dispatchGroup, setDispatchGroup] = useState<string | undefined>()
  const [dispatchLimit, setDispatchLimit] = useState(6)
  const [dispatchGapMs, setDispatchGapMs] = useState(DEFAULT_DISPATCH_GAP_MS)
  const [dispatchNumbers, setDispatchNumbers] = useState('')
  const [dispatchScope, setDispatchScope] = useState<'all' | 'picked'>('all')
  const [dispatchPicked, setDispatchPicked] = useState<string[]>([])
  const [progress, setProgress] = useState<Record<string, { total: number; done: number; running: boolean }>>({})
  const [snapshots, setSnapshots] = useState<Record<string, CardSnapshot>>({})
  const [agentsTotal, setAgentsTotal] = useState(0)
  const [outputMuted, setOutputMuted] = useState(false)
  // 统一接听规则（默认套用到新加入坐席；可一键应用到全部）
  const [unifiedRule, setUnifiedRule] = useState<AnswerRule>(DEFAULT_RULE)

  const loadMeta = () => {
    setLoadingMeta(true)
    Promise.allSettled([
      listManagedAgents({ pageNum: 1, pageSize: 200 }).then((r) => { setAgents(r.agents || []); setAgentsTotal(r.total || (r.agents || []).length) }),
      listGroups().then(setGroups),
      listOrgs().then((r) => setOrgPwd(r.orgs.find((o) => o.orgCode === r.current)?.defaultAgentPassword || '')),
    ]).then((rs) => {
      const rejected = rs.find((r): r is PromiseRejectedResult => r.status === 'rejected')
      if (rejected) {
        const reason = rejected.reason instanceof Error ? rejected.reason.message : String(rejected.reason)
        message.warning(`加载坐席/客户组失败：${reason}`)
      }
    }).finally(() => setLoadingMeta(false))
  }
  useEffect(() => { loadMeta() }, [])

  // 1s 轮询聚合各卡进度（批量派号期间）。仅在内容变化时 setState，避免无派号时每秒空转重渲染。
  // 标签页隐藏时跳过（纯本地聚合，无网络，但隐藏时无人看，省渲染）。
  useEffect(() => {
    const t = setInterval(() => {
      if (document.hidden) return
      const p: Record<string, { total: number; done: number; running: boolean }> = {}
      cardRefs.current.forEach((h, n) => { if (h) { const g = h.getProgress(); if (g.total > 0 || g.running) p[n] = g } })
      setProgress((prev) => {
        const keys = Object.keys(p), pkeys = Object.keys(prev)
        if (keys.length === pkeys.length && keys.every((k) => prev[k] && prev[k].total === p[k].total && prev[k].done === p[k].done && prev[k].running === p[k].running)) return prev
        return p
      })
    }, 1000)
    return () => clearInterval(t)
  }, [])

  const onSnapshot = (number: string, s: CardSnapshot) => setSnapshots((cur) => ({ ...cur, [number]: s }))

  const addCards = () => {
    const toAdd = agents.filter((a) => picked.includes(a.number || '') && !active.some((x) => x.agent.number === a.number))
    if (!toAdd.length) { message.warning('请选择尚未加入的坐席'); return }
    // 口令优先取坐席接口回带的真实密码；接口未回带时回退机构「新坐席默认口令」。
    setActive((cur) => [...cur, ...toAdd.map((a) => ({ agent: a, password: a.password || orgPwd }))])
    setPicked([])
    message.success(`已加入 ${toAdd.length} 个坐席卡片`)
  }
  const removeCard = (number: string) => {
    cardRefs.current.delete(number)
    setActive((cur) => cur.filter((a) => a.agent.number !== number))
    setSnapshots((cur) => { const n = { ...cur }; delete n[number]; return n })
  }
  // 解析派号号码：客户组取号 + 手填，合并去重
  const resolveDispatchNumbers = (): string[] => {
    const manual = parseNumbers(dispatchNumbers)
    const fromGroup: string[] = []
    if (dispatchGroup) {
      const g = groups.find((x) => x.code === dispatchGroup)
      if (g) {
        const start = Number(g.numberStart || 0)
        const count = Math.min(dispatchLimit || 1, g.count || 1)
        for (let i = 0; i < count; i++) fromGroup.push(`${g.numberPrefix || ''}${start + i}`)
      }
    }
    return Array.from(new Set([...fromGroup, ...manual]))
  }

  // 一键并发派号：轮询分发给就绪坐席卡片，各卡同时启动
  const dispatch = () => {
    const numbers = resolveDispatchNumbers()
    if (!numbers.length) { message.warning('请选择客户组取号或手填被叫号'); return }
    const candidates = active
      .map((a) => a.agent.number || '')
      .filter((n) => dispatchScope === 'all' || dispatchPicked.includes(n))
    const ready = candidates.filter((n) => cardRefs.current.get(n)?.isReady())
    if (!ready.length) { message.warning('无就绪坐席（需已连接+注册+空闲）'); return }
    const buckets: Record<string, string[]> = {}
    numbers.forEach((nbr, i) => {
      const owner = ready[i % ready.length]
      ;(buckets[owner] ||= []).push(nbr)
    })
    ready.forEach((n) => { const list = buckets[n]; if (list?.length) cardRefs.current.get(n)?.enqueue(list) })
    message.success(`已向 ${ready.length} 个坐席派发 ${numbers.length} 个号码（轮询）`)
  }
  const stopAll = () => {
    cardRefs.current.forEach((h) => h?.stop())
    message.info('已停止全部批量派号（当前通话不中断）')
  }
  const toggleOutputMuted = () => {
    setOutputMuted((muted) => {
      const next = !muted
      cardRefs.current.forEach((h) => h?.setOutputMuted(next))
      message.info(next ? '已静音全部坐席本地播放' : '已取消全部坐席本地播放静音')
      return next
    })
  }

  // 批量连接/断开/示闲（反馈实际作用张数）
  const connectAll = () => {
    let n = 0; cardRefs.current.forEach((h) => { if (h && !h.getSnapshot().connected) { h.connect(); n++ } })
    message.info(n ? `正在连接 ${n} 个坐席` : '所有坐席已连接')
  }
  const doDisconnectAll = () => {
    let n = 0; cardRefs.current.forEach((h) => { if (h?.getSnapshot().connected) { h.disconnect(); n++ } else h?.disconnect() })
    message.info(`已断开 ${n} 个已连坐席`)
  }
  const disconnectAll = () => {
    const incall = Object.values(snapshots).filter((s) => s.phase === 'incall').length
    if (incall > 0) {
      Modal.confirm({ title: '确认全部断开？', content: `当前有 ${incall} 个坐席正在通话中，断开会立即中断这些通话。`, okText: '断开', okButtonProps: { danger: true }, cancelText: '取消', onOk: doDisconnectAll })
    } else doDisconnectAll()
  }
  const idleAll = () => {
    let n = 0; cardRefs.current.forEach((h) => { if (h?.getSnapshot().connected) { h.setWork('idle'); n++ } })
    message.info(n ? `已对 ${n} 个在线坐席示闲` : '无在线坐席')
  }
  // 把统一接听规则应用到全部已加入坐席卡片
  const applyRuleToAll = () => {
    let n = 0; cardRefs.current.forEach((h) => { if (h) { h.setRule(unifiedRule); n++ } })
    message.success(n ? `已把接听规则应用到 ${n} 个坐席` : '尚无坐席卡片')
  }

  const previewNumbers = useMemo(() => resolveDispatchNumbers(), [dispatchGroup, dispatchLimit, dispatchNumbers, groups]) // eslint-disable-line
  const totalProg = useMemo(() => {
    const vals = Object.values(progress)
    return { total: vals.reduce((s, v) => s + v.total, 0), done: vals.reduce((s, v) => s + v.done, 0), running: vals.some((v) => v.running) }
  }, [progress])
  // 状态汇总
  const summary = useMemo(() => {
    const vals = Object.values(snapshots)
    return {
      total: active.length,
      connected: vals.filter((s) => s.connected).length,
      registered: vals.filter((s) => s.registered).length,
      ready: vals.filter((s) => s.sipReady).length,
      incall: vals.filter((s) => s.phase === 'incall').length,
    }
  }, [snapshots, active.length])
  useEffect(() => { onSummary?.(summary) }, [summary]) // eslint-disable-line

  // 广播「已就绪（sipReady）坐席号」给跨页 store——群呼页坐席分配下拉据此标记/排序（已就绪排前）。
  useEffect(() => {
    setReadyAgents(Object.entries(snapshots).filter(([, s]) => s.sipReady).map(([num]) => num))
  }, [snapshots])

  // 技能组筛选下拉选项（从当前坐席 agentGroupCode 去重）
  const groupFilterOptions = useMemo(() => {
    const s = new Set<string>()
    agents.forEach((a) => { if (a.agentGroupCode) s.add(a.agentGroupCode) })
    return Array.from(s).map((c) => ({ value: c, label: c }))
  }, [agents])
  // 选择区表格数据：按 搜索词 + 技能组 + 状态 过滤
  const filteredAgents = useMemo(() => {
    const q = agentQuery.trim().toLowerCase()
    return agents.filter((a) => {
      if (filterGroup && a.agentGroupCode !== filterGroup) return false
      if (filterStatus != null) {
        const stN = typeof a.status === 'number' ? a.status : Number(a.status)
        if (stN !== filterStatus) return false
      }
      if (q && !`${a.number || ''} ${a.agentName || ''}`.toLowerCase().includes(q)) return false
      return true
    })
  }, [agents, agentQuery, filterGroup, filterStatus])
  // 客户组取号被 count 截断时的提示（dispatchLimit 远大于组容量）
  const dispatchTruncated = useMemo(() => {
    if (!dispatchGroup) return 0
    const g = groups.find((x) => x.code === dispatchGroup)
    if (!g) return 0
    return (dispatchLimit || 0) > (g.count || 0) ? (g.count || 0) : 0
  }, [dispatchGroup, dispatchLimit, groups])

  return (
    <div className="page-container">
      <PageHeader
        title="坐席外呼（浏览器软电话）"
        status={{ tone: summary.ready > 0 ? 'success' : 'neutral', text: summary.ready > 0 ? `就绪 ${summary.ready}` : '未就绪' }}
        onReload={loadMeta}
      />
      <InfoBanner title="坐席浏览器 jssip 软电话 · 走真实 Hermes 工作台链路">
        坐席在浏览器用 jssip 软电话登录真实 hermes-ws 工作台并经 call-center 选线，bridge 到 mock 扮演的客户被叫腿；坐席不在 mock 后端模拟。勾选坐席→加入卡片→各卡独立连接→各自外呼或用「批量并发派号」一组坐席同时拨一批客户。
      </InfoBanner>

      {/* 选择区：搜索 + 技能组/状态筛选 + 表格多选（表头全选作用于当前筛选结果）；可折叠默认展开，加入后可收起省空间 */}
      <Collapse size="small" defaultActiveKey={['pick']} style={{ marginBottom: 12 }}
        expandIcon={({ isActive }) => <CaretRightOutlined rotate={isActive ? 90 : 0} />}
        items={[{
          key: 'pick',
          label: <Space><Text strong>选择坐席加入</Text>{picked.length ? <Text type="secondary" style={{ fontSize: 12 }}>已选 {picked.length}</Text> : null}</Space>,
          children: (<>
            <Space wrap style={{ marginBottom: 8 }}>
              <Input allowClear style={{ width: 200 }} placeholder="搜索坐席号 / 坐席名" value={agentQuery} onChange={(e) => setAgentQuery(e.target.value)} />
              <Select allowClear style={{ width: 160 }} placeholder="技能组筛选" value={filterGroup} onChange={setFilterGroup} options={groupFilterOptions} />
              <Select allowClear style={{ width: 140 }} placeholder="状态筛选" value={filterStatus} onChange={setFilterStatus}
                options={Object.entries(AGENT_STATUS).map(([k, v]) => ({ value: Number(k), label: v.label }))} />
              <Button type="primary" onClick={addCards} disabled={!picked.length}>加入卡片{picked.length ? `（${picked.length}）` : ''}</Button>
              <Button onClick={loadMeta} loading={loadingMeta}>刷新坐席</Button>
              {agentsTotal > agents.length && <Tooltip title="坐席数超过 200，仅展示前 200，请用搜索/筛选精确匹配"><Tag color="warning">仅显示 {agents.length}/{agentsTotal}</Tag></Tooltip>}
            </Space>
            <Table<ManagedAgent>
              size="small" rowKey={(a) => a.number || ''} pagination={false} scroll={{ y: 220 }}
              dataSource={filteredAgents}
              rowSelection={{ selectedRowKeys: picked, onChange: (keys) => setPicked(keys as string[]) }}
              columns={[
                { title: '坐席号', dataIndex: 'number', width: 90 },
                { title: '坐席名', dataIndex: 'agentName', width: 120, render: (v) => v || <Text type="secondary">-</Text> },
                { title: '技能组', dataIndex: 'agentGroupCode', width: 130, render: (v) => v || <Text type="secondary">-</Text> },
                { title: '状态', dataIndex: 'status', width: 110, render: (v) => { const n = typeof v === 'number' ? v : Number(v); const m = AGENT_STATUS[n]; return m ? <Tag color={m.color}>{m.label}</Tag> : <Text type="secondary">-</Text> } },
              ]}
            />
          </>),
        }]} />

      {/* 批量操作 + 汇总（高频查看，吸顶常驻；仅在已有卡片时显示） */}
      {active.length > 0 && (
        <Card size="small" style={{ marginBottom: 12, position: 'sticky', top: 0, zIndex: 10 }}>
          <Space size={6} wrap>
            <Button size="small" onClick={connectAll}>全部连接</Button>
            <Button size="small" onClick={disconnectAll}>全部断开</Button>
            <Button size="small" onClick={idleAll}>全部示闲</Button>
            <Button size="small" type={outputMuted ? 'primary' : 'default'} onClick={toggleOutputMuted}>{outputMuted ? '取消静音' : '一键静音'}</Button>
            <Switch size="small" checkedChildren="折叠" unCheckedChildren="展开" checked={collapsed} onChange={setCollapsed} />
            <Text type="secondary" style={{ fontSize: 12 }}>汇总:</Text>
            <Tag>卡片 {summary.total}</Tag>
            <Tag color="green">WS在线 {summary.connected}</Tag>
            <Tag color="green">已注册 {summary.registered}</Tag>
            <Tooltip title="call-center 认为就绪（WS 在线 + SIP-ready）的坐席数。少于 WS在线数=有坐席 SIP-ready 失败，无法被群呼/派号选中">
              <Tag color={summary.ready < summary.connected ? 'orange' : 'green'}>就绪 {summary.ready}</Tag>
            </Tooltip>
            <Tag color="blue">通话中 {summary.incall}</Tag>
          </Space>
        </Card>
      )}

      {/* 批量并发派号面板 */}
      {active.length > 0 && (
        <Collapse defaultActiveKey={['dispatch']} style={{ marginBottom: 12 }} expandIcon={({ isActive }) => <CaretRightOutlined rotate={isActive ? 90 : 0} />}
          items={[{
            key: 'dispatch',
            label: <Space><ThunderboltOutlined /><Text strong>批量并发派号</Text><Text type="secondary" style={{ fontSize: 12 }}>一组坐席轮询分发、同时外呼一批客户</Text></Space>,
            children: (
              <>
                <Space wrap align="center">
                  <Space.Compact>
                    <Select style={{ width: 160 }} allowClear placeholder="客户组取号" value={dispatchGroup} onChange={setDispatchGroup}
                      options={groups.map((g) => ({ value: g.code, label: `${g.code}（${g.count || 0}）` }))} />
                    <Tooltip title="从客户组取多少个号"><InputNumber style={{ width: 80 }} min={1} max={500} value={dispatchLimit} onChange={(v) => setDispatchLimit(v ?? 1)} /></Tooltip>
                    <Tooltip title="同一坐席队列里，上一通结束后等待多久再拨下一通；坐席之间仍并发。">
                      <InputNumber style={{ width: 120 }} min={0} max={5000} step={100} addonAfter="ms" value={dispatchGapMs} onChange={(v) => setDispatchGapMs(v ?? DEFAULT_DISPATCH_GAP_MS)} />
                    </Tooltip>
                  </Space.Compact>
                  <Input style={{ width: 240 }} placeholder="或手填被叫号(逗号分隔)" value={dispatchNumbers} onChange={(e) => setDispatchNumbers(e.target.value)} />
                  <Radio.Group value={dispatchScope} onChange={(e) => setDispatchScope(e.target.value)} optionType="button" size="small"
                    options={[{ value: 'all', label: '全部就绪坐席' }, { value: 'picked', label: '指定坐席' }]} />
                  {dispatchScope === 'picked' && (
                    <Select mode="multiple" style={{ minWidth: 200 }} placeholder="选参与坐席" value={dispatchPicked} onChange={setDispatchPicked}
                      options={active.map((a) => ({ value: a.agent.number || '', label: a.agent.number || '' }))} />
                  )}
                  <Button type="primary" icon={<ThunderboltOutlined />} onClick={dispatch}>一键并发派号</Button>
                  <Button danger onClick={stopAll}>全部停止</Button>
                  <Text type="secondary">待派 {previewNumbers.length} 号</Text>
                  {dispatchTruncated > 0 && <Text type="warning" style={{ fontSize: 12 }}>该组仅 {dispatchTruncated} 个，已按组容量取号</Text>}
                </Space>
                {totalProg.total > 0 && (
                  <div style={{ marginTop: 10 }}>
                    <Space wrap>
                      <Text>总进度 {totalProg.done}/{totalProg.total}</Text>
                      <Progress percent={totalProg.total ? Math.round((totalProg.done / totalProg.total) * 100) : 0} style={{ width: 200 }} size="small" status={totalProg.running ? 'active' : 'normal'} />
                      {Object.entries(progress).filter(([, v]) => v.total > 0).map(([n, v]) => (
                        <Tag key={n} color={v.running ? 'processing' : 'default'}>{n} {v.done}/{v.total}</Tag>
                      ))}
                    </Space>
                  </div>
                )}
              </>
            ),
          }]} />
      )}

      {/* 统一接听规则面板（Figma AnswerRulePanel）：坐席被群呼/转接来电时自动接/拒 · 新加入坐席默认套用 */}
      {active.length > 0 && (
        <div className="hm-rule-panel">
          <div className="hm-rule-head">
            <div>
              <div className="hm-rule-title">📞 接听规则（统一默认）</div>
              <div className="hm-rule-sub">坐席被群呼/转接来电时自动接/拒 · 新加入坐席默认套用此规则</div>
            </div>
            <Button size="small" style={{ background: '#d97706', borderColor: '#d97706', color: '#fff' }} onClick={applyRuleToAll}>↡ 应用到全部坐席</Button>
          </div>
          <div className="hm-rule-controls">
            <div className="hm-rule-ctl">
              <Switch size="small" checked={unifiedRule.enabled} onChange={(v) => setUnifiedRule((r) => ({ ...r, enabled: v }))} />
              <span className="hm-rule-ctl-label">启用规则</span>
            </div>
            <div className="hm-rule-ctl">
              <span>振铃</span>
              <InputNumber size="small" style={{ width: 64 }} min={0} max={60} value={unifiedRule.ringSec} onChange={(v) => setUnifiedRule((r) => ({ ...r, ringSec: v ?? 0 }))} />
              <span>秒后触发</span>
            </div>
            <div className="hm-rule-ctl">
              <span>动作</span>
              <Radio.Group size="small" optionType="button" buttonStyle="solid" value={unifiedRule.action} onChange={(e) => setUnifiedRule((r) => ({ ...r, action: e.target.value }))}
                options={[{ value: 'answer', label: '自动接听' }, { value: 'reject', label: '自动拒接' }]} />
            </div>
            <div className="hm-rule-ctl">
              <span>命中概率</span>
              <InputNumber size="small" style={{ width: 70 }} min={0} max={100} value={unifiedRule.probability} onChange={(v) => setUnifiedRule((r) => ({ ...r, probability: v ?? 100 }))} addonAfter="%" />
              <span style={{ fontSize: 12 }}>（按概率随机接/拒，模拟真实坐席）</span>
            </div>
          </div>
        </div>
      )}

      {active.length === 0
        ? <Alert type="info" showIcon message="开始：① 上方表格勾选坐席 → ② 加入卡片 → ③ 卡片内点「连接」（或顶部「全部连接」）→ ④ 外呼 / 批量并发派号。每个坐席一张卡片，各自独立在线。" />
        : (
          <Row gutter={[12, 12]}>
            {active.map((a) => (
              <Col key={a.agent.number} xs={24} {...(collapsed ? { sm: 12, lg: 8 } : {})}>
                <SoftphoneCard ref={(h) => { cardRefs.current.set(a.agent.number || '', h) }}
                  agent={a.agent} password={a.password} groups={groups} collapsed={collapsed} initialRule={unifiedRule}
                  dispatchGapMs={dispatchGapMs} outputMuted={outputMuted} onRemove={removeCard} onSnapshot={onSnapshot} />
              </Col>
            ))}
          </Row>
        )}

      <ScenarioRecords scenario="agent" title="坐席外呼记录（外呼 + 被转接来电）" />
    </div>
  )
}
