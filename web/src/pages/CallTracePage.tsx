import { useEffect, useState } from 'react'
import { Card, Table, Tag, Timeline, Empty, Typography, Row, Col, Badge, Collapse, Segmented, Space } from 'antd'
import { getTraceSessions, type TraceSession, type TraceEvent } from '../api'

const { Text, Paragraph } = Typography

const chanColor: Record<string, string> = {
  SIP: 'blue', WS: 'cyan', MEDIA: 'green', BRIDGE: 'orange', FLOW: 'default',
}
const dirArrow: Record<string, string> = { IN: '◀', OUT: '▶', '-': '·' }
const kindText: Record<string, string> = {
  'inbound-leg': '入站腿', outbound: '呼出', test: '测试',
  call: '通话', 'sip-call': 'SIP通话',
}

function sipUser(v?: string) {
  const m = (v || '').match(/sip:([^@>]+)/i)
  return m?.[1] || ''
}
function header(headers: { name: string; value: string }[] | undefined, name: string) {
  return headers?.find((h) => h.name.toLowerCase() === name.toLowerCase())?.value || ''
}
function callRole(s?: TraceSession) {
  const inv = s?.events?.find((e) => e.channel === 'SIP' && e.method === 'INVITE')
  const customer = sipUser(header(inv?.headers, 'To'))
  const displayCaller = sipUser(header(inv?.headers, 'From'))
  const sessionId = header(inv?.headers, 'x-session-id')
  const jCallId = header(inv?.headers, 'X-JCallId')
  return { customer, displayCaller, sessionId, jCallId }
}
function traceTitle(s?: TraceSession) {
  if (!s) return '通话链路'
  const r = callRole(s)
  if (r.customer || r.displayCaller) {
    return `客户腿：${r.customer || '-'} ｜ 外显主叫：${r.displayCaller || '-'}${r.sessionId ? ' ｜ 坐席外呼' : ''}`
  }
  return `通话链路 · ${s.title}`
}

// 腿配色：客户腿/其它各一色，便于在「一通业务通话」里区分多腿。
const legPalette = ['#1677ff', '#fa8c16', '#52c41a', '#722ed1', '#eb2f96', '#13c2c2']
function legColor(leg: string, legs: string[]): string {
  const i = legs.indexOf(leg)
  return i >= 0 ? legPalette[i % legPalette.length] : '#8c8c8c'
}
function legLabel(leg: string, role?: { customer?: string; displayCaller?: string }): string {
  if (!leg) return '—'
  if (role?.customer && leg === role.customer) return '客户被叫 ' + leg
  if (role?.displayCaller && leg === role.displayCaller) return '线路外显 ' + leg
  if (/^agent:\d+/.test(leg)) return leg.replace('agent:', '坐席 ')
  if (/^\d/.test(leg)) return '号 ' + leg
  if (leg === 'customer') return 'mock客户腿'
  if (leg === 'uac') return '主叫(UAC)'
  return leg
}

// SIP 头里属于 Hermes 业务的（高亮展示）
function isBizHeader(name: string): boolean {
  const n = name.toLowerCase()
  return n.startsWith('x-') || n.includes('call') || n.includes('session') || n.includes('origination')
}

function headerRole(name: string, value: string) {
  const n = name.toLowerCase()
  if (n === 'to') return '客户被叫号'
  if (n === 'from' || n === 'remote-party-id') return '线路外显主叫号'
  if (n === 'x-jcallid') return '坐席外呼 callId'
  if (n === 'x-session-id') return '坐席外呼 sessionId'
  if (n === 'x-line-name') return 'Hermes 线路'
  if (/sip:\d+@/.test(value)) return '号码'
  return ''
}

function HeaderTable({ headers }: { headers: { name: string; value: string }[] }) {
  // 业务头排前面
  const sorted = [...headers].sort((a, b) => Number(isBizHeader(b.name)) - Number(isBizHeader(a.name)))
  return (
    <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
      <tbody>
        {sorted.map((h, i) => {
          const role = headerRole(h.name, h.value)
          return (
            <tr key={i} style={{ background: isBizHeader(h.name) || role ? '#fffbe6' : undefined }}>
              <td style={{ padding: '2px 8px', color: isBizHeader(h.name) || role ? '#d46b08' : '#888', fontWeight: isBizHeader(h.name) || role ? 600 : 400, whiteSpace: 'nowrap', verticalAlign: 'top' }}>
                {h.name}{role && <Tag color="gold" style={{ marginLeft: 6 }}>{role}</Tag>}
              </td>
              <td style={{ padding: '2px 8px', wordBreak: 'break-all', fontFamily: 'monospace' }}>{h.value}</td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}

function eventNode(e: TraceEvent, legs: string[], role?: { customer?: string; displayCaller?: string }) {
  const hasReal = (e.headers && e.headers.length > 0) || e.raw
  const lc = legColor(e.leg, legs)
  return {
    color: chanColor[e.channel] || 'gray',
    label: (
      <Text type="secondary" style={{ fontSize: 12 }}>
        {new Date(e.ts).toLocaleTimeString()}.{String(new Date(e.ts).getMilliseconds()).padStart(3, '0')}
      </Text>
    ),
    children: (
      <div style={{ borderLeft: e.leg ? `3px solid ${lc}` : undefined, paddingLeft: e.leg ? 8 : 0 }}>
        <Tag color={chanColor[e.channel]} style={{ marginRight: 6 }}>{e.channel}</Tag>
        <Text strong style={{ marginRight: 6 }}>{dirArrow[e.dir]} {e.method}</Text>
        {e.leg && <Tag color={lc} style={{ color: '#fff', border: 'none' }}>{legLabel(e.leg, role)}</Tag>}
        <div style={{ fontSize: 13, color: '#555', marginTop: 2 }}>{e.summary}</div>
        {hasReal && (
          <Collapse
            ghost
            size="small"
            style={{ marginTop: 4 }}
            items={[{
              key: 'r',
              label: <Text type="secondary" style={{ fontSize: 12 }}>展开真实 SIP 报文（{e.headers?.length || 0} 个头{e.callId ? ` · Call-ID ${e.callId.slice(0, 16)}…` : ''}）</Text>,
              children: (
                <div>
                  {e.headers && e.headers.length > 0 && <HeaderTable headers={e.headers} />}
                  {e.raw && (
                    <Paragraph style={{ marginTop: 8, marginBottom: 0 }}>
                      <pre style={{ fontSize: 11, background: '#0b1021', color: '#d6e0ff', padding: 10, borderRadius: 4, maxHeight: 280, overflow: 'auto', whiteSpace: 'pre-wrap' }}>{e.raw}</pre>
                    </Paragraph>
                  )}
                </div>
              ),
            }]}
          />
        )}
        {!hasReal && e.detail && Object.keys(e.detail).length > 0 && (
          <div style={{ fontSize: 11, color: '#999' }}>
            {Object.entries(e.detail).map(([k, v]) => `${k}=${v}`).join('  ')}
          </div>
        )}
      </div>
    ),
  }
}

export default function CallTracePage() {
  const [sessions, setSessions] = useState<TraceSession[]>([])
  const [sel, setSel] = useState<string>('')
  const [legFilter, setLegFilter] = useState<string>('全部')

  const load = async () => {
    try {
      const ss = await getTraceSessions()
      setSessions(ss)
      // 支持 ?session=<id> 直达，便于从外部调试链接打开指定链路。
      const want = new URLSearchParams(window.location.search).get('session')
      setSel((cur) => cur || want || (ss[0]?.id ?? ''))
    } catch {
      /* ignore */
    }
  }
  useEffect(() => {
    load()
    const t = setInterval(load, 2000)
    return () => clearInterval(t)
  }, [])

  const current = sessions.find((s) => s.id === sel)
  const role = callRole(current)
  const legs = current?.legs ?? []
  const multiLeg = legs.length > 1
  const shownEvents = (current?.events ?? []).filter(
    (e) => legFilter === '全部' || e.leg === legFilter || (legFilter === '无腿' && !e.leg),
  )

  return (
    <div className="page-container">
      <Row gutter={16}>
        <Col span={9}>
          <Card title="通话链路会话" size="small" styles={{ body: { padding: 0 } }}>
            <Table
              rowKey="id"
              size="small"
              dataSource={sessions}
              pagination={{ pageSize: 12 }}
              onRow={(r) => ({ onClick: () => { setSel(r.id); setLegFilter('全部') }, style: { cursor: 'pointer', background: r.id === sel ? '#e6f4ff' : undefined } })}
              columns={[
                { title: '类型', dataIndex: 'kind', width: 70, render: (k: string) => <Tag>{kindText[k] || k}</Tag> },
                { title: '会话', dataIndex: 'title', ellipsis: true },
                { title: '腿', dataIndex: 'legs', width: 56, render: (l: string[]) => <Badge count={l?.length || 0} showZero color={l?.length > 1 ? '#fa8c16' : '#1677ff'} /> },
                { title: '事件', width: 50, render: (_: unknown, r: TraceSession) => r.events?.length || 0 },
              ]}
            />
          </Card>
        </Col>
        <Col span={15}>
          <Card
            title={current ? traceTitle(current) : '通话链路'}
            size="small"
            extra={current && (
              <Text type="secondary" style={{ fontSize: 12 }}>{current.events.length} 事件{current.callId ? ` · ${current.callId.slice(0, 12)}…` : ''}</Text>
            )}
          >
            {!current || current.events.length === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="选择左侧会话查看事件时间线（可展开真实 SIP 报文）" />
            ) : (
              <>
                {multiLeg && (
                  <div style={{ marginBottom: 12, padding: '8px 12px', background: '#fff7e6', border: '1px solid #ffd591', borderRadius: 6 }}>
                    <Space wrap size={4}>
                      <Tag color="orange">多腿合并</Tag>
                      <Text type="secondary" style={{ fontSize: 12 }}>本通业务通话含 {legs.length} 条腿：</Text>
                      {legs.map((l) => (
                        <Tag key={l} color={legColor(l, legs)} style={{ color: '#fff', border: 'none' }}>{legLabel(l, role)}</Tag>
                      ))}
                    </Space>
                  </div>
                )}
                {legs.length > 0 && (
                  <Segmented
                    size="small"
                    style={{ marginBottom: 12 }}
                    value={legFilter}
                    onChange={(v) => setLegFilter(String(v))}
                    options={['全部', ...legs.map((l) => ({ label: legLabel(l, role), value: l }))]}
                  />
                )}
                <Timeline mode="left" style={{ marginTop: 4 }} items={shownEvents.map((e) => eventNode(e, legs, role))} />
              </>
            )}
          </Card>
        </Col>
      </Row>
    </div>
  )
}
