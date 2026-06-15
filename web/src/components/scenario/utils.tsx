import { type ReactNode, useEffect, useState } from 'react'
import { Col, Progress, Row, Space, Spin, Statistic, Steps, Tag, Tooltip, Typography } from 'antd'
import { CaretRightOutlined, CheckCircleTwoTone, CloseCircleTwoTone, MinusCircleTwoTone } from '@ant-design/icons'
import { getTraceSession } from '../../api'
import type { CallView, PreflightReport, ScenarioResult, TestRun, TraceEvent } from '../../api'

const { Text, Paragraph } = Typography

export type RunLike = TestRun | ScenarioResult

// ===== 解析工具 =====
export function parseList(s?: string): string[] {
  if (!s) return []
  return s.split(/[,，\s]+/).map((x) => x.trim()).filter(Boolean)
}

export function parseKV(s?: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  parseList(s).forEach((item) => {
    const i = item.indexOf('=')
    if (i > 0) out[item.slice(0, i).trim()] = item.slice(i + 1).trim()
  })
  return Object.keys(out).length ? out : undefined
}

export function statusColor(status?: string) {
  switch (status) {
    case 'CONNECTED':
    case 'OBSERVED':
      return '#52c41a'
    case 'FAILED':
      return '#ff4d4f'
    default:
      return '#faad14'
  }
}
export function statusText(status?: string) {
  switch (status) {
    case 'CONNECTED': return '已接通'
    case 'OBSERVED': return '已观测'
    case 'FAILED': return '失败'
    default: return '等待'
  }
}
export function phaseTag(status?: string) {
  if (status === 'ok') return 'success'
  if (status === 'fail') return 'error'
  return 'warning'
}
export function recordStatusColor(status?: string) {
  if (status === 'ENDED' || status === 'ANSWERED') return 'success'
  if (status === 'REJECTED' || status === 'FAILED') return 'error'
  if (status === 'RINGING') return 'processing'
  return 'warning'
}
export function shortText(s?: string, n = 18) {
  if (!s) return '-'
  return s.length > n ? `${s.slice(0, n)}...` : s
}
export function timeText(s?: string) {
  if (!s) return '-'
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString()
}
export function callsOf(run?: RunLike | null): CallView[] {
  if (!run) return []
  return run.calls || []
}
function resultTotals(run?: RunLike | null) {
  if (!run) return { total: 0, passed: 0, failed: 0, passRate: 0, durationMs: 0 }
  if ('runs' in run) {
    return { total: run.total, passed: run.passed, failed: run.failed, passRate: run.metrics?.passRate || 0, durationMs: run.durationMs }
  }
  const total = run.calls?.length || 1
  const passed = run.ok ? total : 0
  return { total, passed, failed: total - passed, passRate: run.ok ? 100 : 0, durationMs: run.durationMs }
}

// ===== 就绪标签（preflight）=====
export function ReadyLabel({ report }: { report?: PreflightReport }) {
  if (!report) return null
  const fails = report.checks.filter((c) => c.status === 'FAIL').length
  const warns = report.checks.filter((c) => c.status === 'WARN').length
  const detail = report.checks.map((c) => `${c.status} ${c.name}: ${c.detail}`).join('\n')
  const label = report.ready ? (warns ? `就绪(${warns} 提示)` : '就绪') : `未就绪(${fails} 项缺失)`
  return (
    <Tooltip title={detail}>
      <Tag color={report.ready ? (warns ? 'warning' : 'success') : 'error'} style={{ cursor: 'help' }}>{label}</Tag>
    </Tooltip>
  )
}

// ===== JSON 代码块 =====
export function JSONBlock({ value }: { value?: string }) {
  if (!value) return null
  let text = value
  try { text = JSON.stringify(JSON.parse(value), null, 2) } catch { /* raw */ }
  return <pre style={{ margin: 0, padding: 10, background: '#0b1021', color: '#d6e0ff', borderRadius: 4, fontSize: 12, maxHeight: 260, overflow: 'auto' }}>{text}</pre>
}

// ===== 场景结果汇总卡 =====
export function ScenarioSummary({ run }: { run?: RunLike | null }) {
  if (!run) return null
  const t = resultTotals(run)
  return (
    <div style={{ marginTop: 16, padding: 12, border: '1px solid #f0f0f0', borderRadius: 6, background: '#fff' }}>
      <Row gutter={16} align="middle">
        <Col span={5}><Statistic title="总通话" value={t.total} /></Col>
        <Col span={5}><Statistic title="通过" value={t.passed} valueStyle={{ color: '#3f8600' }} /></Col>
        <Col span={5}><Statistic title="失败" value={t.failed} valueStyle={{ color: t.failed ? '#cf1322' : undefined }} /></Col>
        <Col span={5}><Statistic title="耗时(ms)" value={t.durationMs} /></Col>
        <Col span={4}><Progress type="circle" percent={t.passRate} size={64} /></Col>
      </Row>
    </div>
  )
}

// ===== 每通通话双侧卡片墙 =====
export function CallBoard({ calls }: { calls: CallView[] }) {
  if (!calls.length) return null
  return (
    <div style={{ marginTop: 12, display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(360px, 1fr))', gap: 12 }}>
      {calls.map((c, i) => (
        <div key={`${c.id}-${i}`} style={{ border: `1px solid ${statusColor(c.status)}`, borderRadius: 6, background: '#fff', padding: 12 }}>
          <Space style={{ width: '100%', justifyContent: 'space-between' }}>
            <Tag color={c.status === 'FAILED' ? 'error' : c.status === 'PENDING' ? 'warning' : 'success'}>{statusText(c.status)}</Tag>
            {c.traceId && <a href={`/trace?session=${encodeURIComponent(c.traceId)}`}>trace {c.traceId}</a>}
          </Space>
          <div style={{ marginTop: 12, display: 'grid', gridTemplateColumns: '1fr 34px 1fr', alignItems: 'center', gap: 8 }}>
            <div style={{ border: '1px solid #d9d9d9', borderRadius: 6, padding: 10, minHeight: 78 }}>
              <Text type="secondary">Hermes 业务侧</Text>
              <div style={{ fontSize: 18, fontWeight: 600, marginTop: 4 }}>{c.agent || c.agentGroup || '-'}</div>
              <Text type="secondary">{c.agentState || '由 Hermes 调度'}</Text>
            </div>
            <div style={{ textAlign: 'center', color: '#8c8c8c' }}>⇄</div>
            <div style={{ border: '1px solid #d9d9d9', borderRadius: 6, padding: 10, minHeight: 78 }}>
              <Text type="secondary">mock 客户被叫</Text>
              <div style={{ fontSize: 18, fontWeight: 600, marginTop: 4 }}>{c.customer || '-'}</div>
              <Text type="secondary">{c.customerState || '等待呼入'}</Text>
            </div>
          </div>
          {!!c.phases?.length && (
            <div style={{ marginTop: 10 }}>
              {c.phases.map((p, idx) => (
                <Tag key={idx} color={phaseTag(p.status)} style={{ marginBottom: 4 }}>{p.name}</Tag>
              ))}
            </div>
          )}
          {c.detail && <Paragraph type="secondary" style={{ fontSize: 12, marginTop: 8, marginBottom: 0 }}>{c.detail}</Paragraph>}
        </div>
      ))}
    </div>
  )
}

// ===== 测试步骤断言时间线 =====
export function RunSteps({ run }: { run?: TestRun | null }) {
  if (!run?.steps?.length) return null
  return (
    <Steps
      direction="vertical"
      size="small"
      style={{ marginTop: 12 }}
      items={run.steps.map((s) => {
        // 可选步骤（如坐席腿）：未通过时不标红 error，用中性「参考/未观测」呈现
        if (s.optional && !s.ok) {
          return {
            status: 'wait' as const,
            title: <>{s.name} <Tag color="default" style={{ fontSize: 11 }}>参考·不计成败</Tag></>,
            description: <Text type="secondary" style={{ fontSize: 12 }}>{s.detail}</Text>,
            icon: <MinusCircleTwoTone twoToneColor="#bfbfbf" />,
          }
        }
        return {
          status: s.ok ? 'finish' : 'error' as const,
          title: s.optional ? <>{s.name} <Tag color="green" style={{ fontSize: 11 }}>参考</Tag></> : s.name,
          description: <Text type={s.ok ? 'secondary' : 'danger'} style={{ fontSize: 12 }}>{s.detail}</Text>,
          icon: s.ok ? <CheckCircleTwoTone twoToneColor="#52c41a" /> : <CloseCircleTwoTone twoToneColor="#ff4d4f" />,
        }
      })}
    />
  )
}

// ============================================================================
// 统一通话结果术语（一处定义，全局用）——根除 CONNECTED/OBSERVED/ANSWERED/ENDED…
// 三套状态词混用。把后端各来源状态归一成 4 个可读结局。
// ============================================================================
export type CallOutcomeKind = 'answered' | 'failed' | 'ringing' | 'pending'

// callOutcome 把 CallView.status 或 CallRecord.status 统一成一个结局描述。
export function callOutcome(status?: string): { kind: CallOutcomeKind; text: string; color: string; icon: string } {
  switch (status) {
    case 'CONNECTED':
    case 'OBSERVED':
    case 'ANSWERED':
    case 'ENDED':
      return { kind: 'answered', text: '已接通', color: '#52c41a', icon: '✅' }
    case 'FAILED':
    case 'REJECTED':
      return { kind: 'failed', text: '未接通', color: '#ff4d4f', icon: '❌' }
    case 'RINGING':
      return { kind: 'ringing', text: '振铃中', color: '#1677ff', icon: '📳' }
    default:
      return { kind: 'pending', text: '等待呼入', color: '#faad14', icon: '⏳' }
  }
}

// ============================================================================
// 结论横幅：一次运行最顶部的「成/败/进行中」单一答案 + 关键数字 + 右侧操作槽。
// ============================================================================
export type BannerVerdict = 'success' | 'fail' | 'running' | 'idle'
export interface BannerMetric { label: string; value: number | string; color?: string }

export function ResultBanner({
  verdict, title, sub, metrics, extra,
}: {
  verdict: BannerVerdict
  title: string
  sub?: ReactNode
  metrics?: BannerMetric[]
  extra?: ReactNode
}) {
  const icon = verdict === 'success' ? '✅' : verdict === 'fail' ? '❌' : verdict === 'running' ? '🟢' : '○'
  return (
    <div className={`result-banner is-${verdict}`}>
      <div className="result-banner-verdict">
        <span className="rb-icon">{icon}</span>
        <span>{title}</span>
        {sub ? <span className="result-banner-sub">{sub}</span> : null}
      </div>
      <Space size={18} wrap>
        {metrics?.length ? (
          <div className="result-banner-metrics">
            {metrics.map((m) => (
              <div className="rb-metric" key={m.label}>
                <div className="rb-metric-num" style={{ color: m.color }}>{m.value}</div>
                <div className="rb-metric-label">{m.label}</div>
              </div>
            ))}
          </div>
        ) : null}
        {extra}
      </Space>
    </div>
  )
}

// ============================================================================
// 通话证据：从 trace events 提取 mock 被叫腿亲历的硬证据（编解码 / RTP 收发·丢包 /
// DTMF / 故障注入 / 挂断）。后端 sipagent 已采集进 ChanBridge(MEDIA/BRIDGE) detail，
// 这里汇总展示——把「测试浅」（只验接通）补成「接通后行为对不对」。
// ============================================================================
export interface CallEvidenceData {
  codec?: string
  media?: string      // 双向/单向 + 收发包
  loss?: string       // 丢包率
  dtmf?: string[]     // 收到的按键
  fault?: string      // 注入的故障
  hangup?: string     // 挂断 summary
  empty: boolean
}

// extractEvidence 从一段 trace events 里抽取证据（纯函数，便于复用/测试）。
export function extractEvidence(events: TraceEvent[]): CallEvidenceData {
  const out: CallEvidenceData = { dtmf: [], empty: true }
  for (const e of events) {
    const d = e.detail || {}
    if (e.method === '媒体协商' && d.codec) {
      out.codec = `${d.codec}${d.payloadType ? ` (pt=${d.payloadType})` : ''}`
    } else if (e.method === '媒体统计') {
      out.media = d.twoWay === 'true'
        ? `双向 · 收${d.rxPackets || '0'}/发${d.txPackets || '0'}包`
        : `单向 · 收${d.rxPackets || '0'}/发${d.txPackets || '0'}包`
      if (d.rxLossPercent !== undefined) out.loss = `丢包 ${d.rxLossPercent}%`
    } else if (e.method === 'DTMF' && d.digit) {
      out.dtmf!.push(d.digit)
    } else if (e.method === '故障注入') {
      out.fault = d.fault || e.summary
    } else if (e.method === '挂断' || e.method === '决策') {
      if (e.summary && (e.method === '挂断' || /拒接|不可用|振铃不接/.test(e.summary))) out.hangup = e.summary
    }
  }
  out.empty = !out.codec && !out.media && !out.dtmf!.length && !out.fault && !out.hangup
  return out
}

// CallEvidence 拉取某通的 trace（按 traceId）并展示证据格。无 traceId / 拉取中 / 无证据各有占位。
export function CallEvidence({ traceId, hangupCode }: { traceId?: string; hangupCode?: number }) {
  const [ev, setEv] = useState<CallEvidenceData | null>(null)
  const [loading, setLoading] = useState(false)
  useEffect(() => {
    if (!traceId) { setEv(null); return }
    let alive = true
    setLoading(true)
    getTraceSession(traceId)
      .then((s) => { if (alive) setEv(extractEvidence(s.events || [])) })
      .catch(() => { if (alive) setEv(null) })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [traceId])

  if (!traceId) return <Text type="secondary" style={{ fontSize: 12 }}>无关联链路（尚未观测到被叫腿）</Text>
  if (loading && !ev) return <Spin size="small" />
  if (!ev || ev.empty) {
    return (
      <div className="evidence-grid">
        {hangupCode ? <Cell label="挂断码" value={String(hangupCode)} /> : null}
        <Cell label="媒体证据" value="—" />
      </div>
    )
  }
  return (
    <div className="evidence-grid">
      {ev.codec && <Cell label="协商编解码" value={ev.codec} />}
      {ev.media && <Cell label="RTP 媒体" value={ev.media} />}
      {ev.loss && <Cell label="丢包" value={ev.loss} />}
      {ev.dtmf?.length ? <Cell label="收到 DTMF" value={ev.dtmf.join(' ')} /> : null}
      {ev.fault && <Cell label="故障注入" value={ev.fault} />}
      {(ev.hangup || hangupCode) && <Cell label="挂断" value={ev.hangup || String(hangupCode)} />}
    </div>
  )
}

function Cell({ label, value }: { label: string; value: string }) {
  return (
    <div className="evidence-cell">
      <div className="evidence-cell-label">{label}</div>
      <div className="evidence-cell-value">{value}</div>
    </div>
  )
}

// ============================================================================
// 通话明细行：可展开。收起=结局+号码+证据摘要一行；展开=Hermes侧↔mock侧 + 证据格。
// ============================================================================

// 结局状态彩点 pill（Figma StatusTag：浅底 + 彩点 + 文案）。
function OutcomePill({ status }: { status?: string }) {
  const o = callOutcome(status)
  const tone = o.kind === 'answered' ? 'success' : o.kind === 'failed' ? 'danger' : o.kind === 'ringing' ? 'info' : 'warning'
  return <span className={`hm-status-pill is-${tone}`}><span className="hm-status-dot" />{o.text}</span>
}

export function CallRow({ call }: { call: CallView }) {
  const [open, setOpen] = useState(false)
  return (
    <div className={`call-row${open ? ' is-open' : ''}`}>
      <div className="call-row-head" onClick={() => setOpen((v) => !v)}>
        <CaretRightOutlined className={`call-row-caret${open ? ' is-open' : ''}`} />
        <OutcomePill status={call.status} />
        <span className="call-row-number">{call.customer || '-'}</span>
        <span className="call-row-spacer" />
        <span className="call-row-evidence-inline">
          {call.durationMs ? <span>{(call.durationMs / 1000).toFixed(1)}s</span> : <span>—</span>}
          {call.agent ? <span>坐席 {call.agent}</span> : call.agentGroup ? <span>组 {call.agentGroup}</span> : null}
          {call.detail ? <Text type="secondary" style={{ fontSize: 12 }}>{shortText(call.detail, 24)}</Text> : null}
          {call.traceId ? (
            <a href={`/trace?session=${encodeURIComponent(call.traceId)}`} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>trace ↗</a>
          ) : null}
        </span>
      </div>
      {open && (
        <div className="call-row-body">
          <div className="leg-view">
            <div className="leg-card">
              <div className="leg-card-label">Hermes 业务侧</div>
              <div className="leg-card-value">{call.agent || call.agentGroup || '由 Hermes 调度'}{call.agentState ? ` · ${call.agentState}` : ''}</div>
            </div>
            <div className="leg-arrow">⇄</div>
            <div className="leg-card">
              <div className="leg-card-label">mock 客户被叫</div>
              <div className="leg-card-value">{call.customer || '-'}{call.customerState ? ` · ${call.customerState}` : ''}</div>
            </div>
          </div>
          <CallEvidence traceId={call.traceId} />
        </div>
      )}
    </div>
  )
}

// CallRows 一组通话明细行（替代 CallBoard 的卡片墙；空态返回 null）。
export function CallRows({ calls }: { calls: CallView[] }) {
  if (!calls.length) return null
  return <div className="call-list-wrap">{calls.map((c, i) => <CallRow key={`${c.id}-${i}`} call={c} />)}</div>
}
