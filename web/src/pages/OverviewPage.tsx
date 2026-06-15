import { useState } from 'react'
import { Card, Col, Row, Table, Empty, Spin } from 'antd'
import { Link } from 'react-router-dom'
import { getOverview, type Overview, type TraceSession } from '../api'
import { PageHeader } from '../components/layout/PageHeader'
import { usePolling } from '../hooks/usePolling'

const kindText: Record<string, string> = {
  'inbound-leg': '入站腿', outbound: '呼出', test: '测试', call: '通话',
}

// 状态彩点 pill（Figma：彩点 + 文案）。
function StatePill({ state }: { state: string }) {
  const color = state === 'ANSWERED' || state === 'ENDED' ? '#16a34a'
    : state === 'RINGING' ? '#2563eb'
    : state === 'REJECTED' ? '#dc2626' : '#94a3b8'
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
      <span style={{ width: 7, height: 7, borderRadius: '50%', background: color }} />
      <span style={{ fontSize: 13 }}>{state}</span>
    </span>
  )
}

// 统计卡（Figma StatCard）。
function StatCard({ label, value, color }: { label: string; value: number; color?: string }) {
  return (
    <div className="hm-stat-card">
      <div className="hm-stat-label">{label}</div>
      <div className="hm-stat-value" style={color ? { color } : undefined}>{value}</div>
    </div>
  )
}

export default function OverviewPage() {
  const [ov, setOv] = useState<Overview | null>(null)
  const [loading, setLoading] = useState(true)

  const load = async () => {
    try {
      setOv(await getOverview())
    } catch {
      /* ignore */
    } finally {
      setLoading(false)
    }
  }
  usePolling(load, 3000)

  if (loading && !ov) return <div style={{ padding: 48, textAlign: 'center' }}><Spin /></div>
  if (!ov) return <Empty style={{ marginTop: 80 }} description="无法获取概览" />

  const sessions = ov.trace.sessions || []
  const active = ov.mock.active || []

  return (
    <div className="page-container">
      <PageHeader
        title="总览"
        status={{ tone: 'success', text: '每 3s 自动刷新' }}
        onReload={load}
      />

      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col span={6}><StatCard label="活跃通话" value={ov.mock.stats.active} /></Col>
        <Col span={6}><StatCard label="累计接听" value={ov.mock.stats.answered} color="#16a34a" /></Col>
        <Col span={6}><StatCard label="拒接 / 失败" value={ov.mock.stats.rejected} color={ov.mock.stats.rejected ? '#dc2626' : undefined} /></Col>
        <Col span={6}><StatCard label="链路会话" value={sessions.length} /></Col>
      </Row>

      <Row gutter={16}>
        <Col span={9}>
          <Card
            title="活跃通话"
            extra={<span style={{ fontSize: 12, color: '#94a3b8' }}>实时 · mock 被叫腿</span>}
            styles={{ body: { padding: 0 } }}
          >
            {active.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无活跃通话" style={{ padding: 32 }} /> : (
              <Table rowKey="id" size="small" pagination={{ pageSize: 8, hideOnSinglePage: true }} dataSource={active}
                columns={[
                  { title: '被叫', dataIndex: 'callee', ellipsis: true },
                  { title: '状态', dataIndex: 'state', width: 120, render: (s: string) => <StatePill state={s} /> },
                  { title: '结果', dataIndex: 'outcome', width: 90, ellipsis: true, render: (o: string) => o || '-' },
                ]} />
            )}
          </Card>
        </Col>
        <Col span={15}>
          <Card
            title="近期通话链路"
            extra={<Link to="/trace" style={{ fontSize: 12, fontWeight: 500 }}>查看时间线 →</Link>}
            styles={{ body: { padding: 0 } }}
          >
            {sessions.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无链路会话（触发一通通话/测试）" style={{ padding: 32 }} /> : (
              <Table rowKey="id" size="small" pagination={{ pageSize: 6, hideOnSinglePage: true }} dataSource={sessions}
                columns={[
                  { title: '类型', dataIndex: 'kind', width: 80, render: (k: string) => <span style={{ fontSize: 12, color: '#64748b' }}>{kindText[k] || k}</span> },
                  { title: '会话', dataIndex: 'title', ellipsis: true },
                  { title: '腿', dataIndex: 'legs', width: 56, render: (l: string[]) => l?.length || 0 },
                  { title: '事件', width: 56, render: (_: unknown, r: TraceSession) => r.eventCount ?? 0 },
                  { title: '时间', dataIndex: 'startedAt', width: 96, render: (t: string) => <span style={{ fontSize: 12, color: '#64748b' }}>{new Date(t).toLocaleTimeString()}</span> },
                ]} />
            )}
          </Card>
        </Col>
      </Row>
    </div>
  )
}
