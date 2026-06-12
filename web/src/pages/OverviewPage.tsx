import { useEffect, useState } from 'react'
import { Card, Col, Row, Statistic, Tag, Table, Badge, Empty, Spin } from 'antd'
import { Link } from 'react-router-dom'
import { getOverview, type Overview, type TraceSession } from '../api'

const kindText: Record<string, string> = {
  'inbound-leg': '入站腿', outbound: '呼出', test: '测试', call: '通话',
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
  useEffect(() => {
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [])

  if (loading && !ov) return <div style={{ padding: 48, textAlign: 'center' }}><Spin /></div>
  if (!ov) return <Empty style={{ marginTop: 80 }} description="无法获取概览" />

  const sessions = ov.trace.sessions || []
  const active = ov.mock.active || []

  return (
    <div className="page-container">
      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col span={6}><Card><Statistic title="活跃通话" value={ov.mock.stats.active} /></Card></Col>
        <Col span={6}><Card><Statistic title="累计接听" value={ov.mock.stats.answered} /></Card></Col>
        <Col span={6}><Card><Statistic title="拒接/失败" value={ov.mock.stats.rejected} valueStyle={{ color: '#cf1322' }} /></Card></Col>
        <Col span={6}><Card><Statistic title="链路会话" value={sessions.length} /></Card></Col>
      </Row>

      <Row gutter={16}>
        <Col span={9}>
          <Card title="活跃通话" size="small" styles={{ body: { padding: 0 } }}>
            {active.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无活跃通话" style={{ padding: 24 }} /> : (
              <Table rowKey="id" size="small" pagination={{ pageSize: 8 }} dataSource={active}
                columns={[
                  { title: '被叫', dataIndex: 'callee', ellipsis: true },
                  { title: '状态', dataIndex: 'state', width: 90, render: (s: string) => <Tag color={s === 'ANSWERED' ? 'green' : s === 'RINGING' ? 'processing' : s === 'REJECTED' ? 'red' : 'default'}>{s}</Tag> },
                  { title: '结果', dataIndex: 'outcome', width: 90, ellipsis: true, render: (o: string) => o || '-' },
                ]} />
            )}
          </Card>
        </Col>
        <Col span={15}>
          <Card title={<span>近期通话链路 <Link to="/trace" style={{ fontSize: 12, fontWeight: 400 }}>查看时间线 →</Link></span>} size="small">
            {sessions.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无链路会话（触发一通通话/测试）" /> : (
              <Table rowKey="id" size="small" pagination={{ pageSize: 6 }} dataSource={sessions}
                columns={[
                  { title: '类型', dataIndex: 'kind', width: 72, render: (k: string) => <Tag>{kindText[k] || k}</Tag> },
                  { title: '会话', dataIndex: 'title', ellipsis: true },
                  { title: '腿', dataIndex: 'legs', width: 56, render: (l: string[]) => <Badge count={l?.length || 0} showZero color="#1677ff" /> },
                  { title: '事件', width: 50, render: (_: unknown, r: TraceSession) => r.eventCount ?? 0 },
                  { title: '时间', dataIndex: 'startedAt', width: 90, render: (t: string) => new Date(t).toLocaleTimeString() },
                ]} />
            )}
          </Card>
        </Col>
      </Row>
    </div>
  )
}
