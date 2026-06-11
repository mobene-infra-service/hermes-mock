import { useEffect, useState } from 'react'
import { Card, Table, Tag, Input, Space, Button, Typography, Alert, Drawer } from 'antd'
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons'
import { queryCallbacks, type CallbackRecord } from '../api'

const { Text, Paragraph } = Typography

// Hermes 回调：接收 Hermes 主动推来的 webhook（任务结果/自动外呼/CDR/会话等），带筛选查询。
// 回调地址需在 Hermes 侧(t_callback_address)配置指向 mock：http://<mock>/api/callbacks/<source>。
export default function CallbacksPage() {
  const [recs, setRecs] = useState<CallbackRecord[]>([])
  const [source, setSource] = useState('')
  const [event, setEvent] = useState('')
  const [callUuid, setCallUuid] = useState('')
  const [keyword, setKeyword] = useState('')
  const [detail, setDetail] = useState<CallbackRecord | null>(null)

  const load = async () => {
    try { const r = await queryCallbacks({ source, event, callUuid, keyword }); setRecs(r.callbacks || []) }
    catch { /* ignore */ }
  }
  useEffect(() => { load(); const t = setInterval(load, 4000); return () => clearInterval(t) }, [source, event, callUuid, keyword])

  return (
    <div className="page-container">
      <Alert
        type="info"
        style={{ marginBottom: 16 }}
        message="Hermes 回调接收（webhook）"
        description={<span>Hermes 主动回调推到 <Text code>POST /api/callbacks/&lt;source&gt;</Text>（如 callbot/autocall/cdr）。请在 Hermes 侧把回调地址配置为指向本 mock。带 callUuid 的回调会自动并入「通话链路」。下方可按来源/事件/callUuid/关键字筛选。</span>}
      />
      <Card title="回调记录" size="small"
        extra={
          <Space wrap>
            <Input size="small" placeholder="来源" allowClear value={source} onChange={(e) => setSource(e.target.value)} style={{ width: 110 }} />
            <Input size="small" placeholder="事件" allowClear value={event} onChange={(e) => setEvent(e.target.value)} style={{ width: 110 }} />
            <Input size="small" placeholder="callUuid" allowClear value={callUuid} onChange={(e) => setCallUuid(e.target.value)} style={{ width: 160 }} />
            <Input size="small" prefix={<SearchOutlined />} placeholder="关键字" allowClear value={keyword} onChange={(e) => setKeyword(e.target.value)} style={{ width: 130 }} />
            <Button size="small" icon={<ReloadOutlined />} onClick={load}>刷新</Button>
          </Space>
        }>
        <Table rowKey="seq" size="small" dataSource={recs} pagination={{ pageSize: 15 }}
          columns={[
            { title: '时间', dataIndex: 'ts', width: 100, render: (t: string) => new Date(t).toLocaleTimeString() },
            { title: '来源', dataIndex: 'source', width: 100, render: (v: string) => <Tag color="geekblue">{v}</Tag> },
            { title: '事件', dataIndex: 'event', render: (v: string) => v ? <Tag>{v}</Tag> : '-' },
            { title: '机构', dataIndex: 'orgCode', width: 90, render: (v: string) => v || '-' },
            { title: 'callUuid', dataIndex: 'callUuid', render: (v: string) => v ? <Text code style={{ fontSize: 11 }}>{v.slice(0, 16)}</Text> : '-' },
            { title: '来源IP', dataIndex: 'remote', width: 110 },
            { title: '', width: 60, render: (_: unknown, r: CallbackRecord) => <a onClick={() => setDetail(r)}>详情</a> },
          ]} />
      </Card>
      <Drawer title={detail ? `回调 #${detail.seq} · ${detail.source}` : ''} open={!!detail} onClose={() => setDetail(null)} width={520}>
        {detail && (
          <Paragraph>
            <pre style={{ fontSize: 12, background: '#0b1021', color: '#d6e0ff', padding: 10, borderRadius: 4, whiteSpace: 'pre-wrap', overflow: 'auto' }}>
              {JSON.stringify(detail.payload, null, 2)}
            </pre>
          </Paragraph>
        )}
      </Drawer>
    </div>
  )
}
