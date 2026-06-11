import { Col, Progress, Row, Space, Statistic, Steps, Tag, Tooltip, Typography } from 'antd'
import { CheckCircleTwoTone, CloseCircleTwoTone, MinusCircleTwoTone } from '@ant-design/icons'
import type { CallView, PreflightReport, ScenarioResult, TestRun } from '../../api'

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
