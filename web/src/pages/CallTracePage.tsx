import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { ReloadOutlined } from '@ant-design/icons'
import { Badge, Button, Card, Col, Collapse, Empty, Row, Segmented, Space, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { getTraceSessions, getTraceSession, type TraceEvent, type TraceSession } from '../api'
import { PageHeader } from '../components/layout/PageHeader'
import { InfoBanner } from '../components/layout/InfoBanner'
import { usePolling } from '../hooks/usePolling'

const { Text, Paragraph } = Typography
const TRACE_AUTO_REFRESH_KEY = 'hm.trace.auto'

const chanColor: Record<string, string> = {
  SIP: 'blue', WS: 'cyan', MEDIA: 'green', BRIDGE: 'orange', FLOW: 'default',
}
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

const legPalette = ['#1677ff', '#fa8c16', '#52c41a', '#722ed1', '#eb2f96', '#13c2c2']
const MOCK_PARTY = '__mock__'

interface Party {
  id: string
  label: string
  color: string
}

function shortID(v?: string) {
  if (!v) return ''
  return v.length > 16 ? `${v.slice(0, 12)}...` : v
}

function isSipResponse(method: string) {
  return /^\d{3}$/.test(method)
}

function partyLabel(id: string, role?: { customer?: string; displayCaller?: string }) {
  if (id === MOCK_PARTY) return 'mock UAS'
  if (role?.customer && id === role.customer) return `客户被叫 ${id}`
  if (role?.displayCaller && id === role.displayCaller) return `外显主叫 ${id}`
  if (/^agent:\d+/.test(id)) return id.replace('agent:', '坐席 ')
  if (/^\d/.test(id)) return `参与方 ${id}`
  if (id === 'customer') return 'mock客户腿'
  if (id === 'uac') return '主叫(UAC)'
  return id || '未知'
}

function legLabel(leg: string, role?: { customer?: string; displayCaller?: string }): string {
  if (!leg) return '-'
  if (role?.customer && leg === role.customer) return '客户被叫 ' + leg
  if (role?.displayCaller && leg === role.displayCaller) return '线路外显 ' + leg
  if (/^agent:\d+/.test(leg)) return leg.replace('agent:', '坐席 ')
  if (/^\d/.test(leg)) return '号 ' + leg
  if (leg === 'customer') return 'mock客户腿'
  if (leg === 'uac') return '主叫(UAC)'
  return leg
}

function normalizeParty(id?: string, role?: { customer?: string }) {
  const v = (id || '').trim()
  if (!v) return ''
  if (v === 'customer' && role?.customer) return role.customer
  return v
}

function eventParty(e: TraceEvent, role?: { customer?: string }) {
  return normalizeParty(e.leg, role)
}

function sipEndpoints(e: TraceEvent, role?: { customer?: string }) {
  if (e.channel !== 'SIP') return null
  const from = normalizeParty(sipUser(header(e.headers, 'From')), role)
  const to = normalizeParty(sipUser(header(e.headers, 'To')), role)
  if (from && to) {
    return isSipResponse(e.method) ? { from: to, to: from } : { from, to }
  }
  const p = eventParty(e, role)
  if (!p) return null
  return e.dir === 'OUT' ? { from: MOCK_PARTY, to: p } : { from: p, to: MOCK_PARTY }
}

function buildParties(events: TraceEvent[], legs: string[], role?: { customer?: string; displayCaller?: string }): Party[] {
  const ids: string[] = []
  const add = (id?: string) => {
    const p = normalizeParty(id, role)
    if (p && !ids.includes(p)) ids.push(p)
  }

  add(role?.displayCaller)
  add(role?.customer)
  events.forEach((e) => {
    const endpoints = sipEndpoints(e, role)
    add(endpoints?.from)
    add(endpoints?.to)
  })
  legs.forEach(add)
  events.forEach((e) => add(eventParty(e, role)))

  if (ids.length === 0) {
    ids.push(MOCK_PARTY)
  }
  if (ids.length === 1 && events.some((e) => e.channel === 'SIP')) {
    ids.unshift(MOCK_PARTY)
  }

  return ids.map((id, i) => ({ id, label: partyLabel(id, role), color: legPalette[i % legPalette.length] }))
}

function sipCallIDs(s?: TraceSession) {
  const ids = new Set<string>()
  s?.events?.forEach((e) => {
    const v = header(e.headers, 'Call-ID')
    if (v) ids.add(v)
  })
  return Array.from(ids)
}

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

function DetailTable({ detail }: { detail: Record<string, string> }) {
  return (
    <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
      <tbody>
        {Object.entries(detail).map(([k, v]) => (
          <tr key={k}>
            <td style={{ padding: '2px 8px', color: '#888', whiteSpace: 'nowrap', verticalAlign: 'top' }}>{k}</td>
            <td style={{ padding: '2px 8px', wordBreak: 'break-all', fontFamily: 'monospace' }}>{v}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function EventDetails({ e }: { e: TraceEvent }) {
  const hasReal = (e.headers && e.headers.length > 0) || e.raw
  const hasDetail = e.detail && Object.keys(e.detail).length > 0
  if (!hasReal && !hasDetail) return null

  return (
    <Collapse
      ghost
      size="small"
      items={[{
        key: 'r',
        label: (
          <Text type="secondary" style={{ fontSize: 12 }}>
            {hasReal ? `展开真实 SIP 报文（${e.headers?.length || 0} 个头${header(e.headers, 'Call-ID') ? ` · Call-ID ${shortID(header(e.headers, 'Call-ID'))}` : ''}）` : '展开事件明细'}
          </Text>
        ),
        children: (
          <div>
            {hasDetail && <DetailTable detail={e.detail || {}} />}
            {e.headers && e.headers.length > 0 && <HeaderTable headers={e.headers} />}
            {e.raw && (
              <Paragraph style={{ marginTop: 8, marginBottom: 0 }}>
                <pre className="trace-raw-message">{e.raw}</pre>
              </Paragraph>
            )}
          </div>
        ),
      }]}
    />
  )
}

function formatEventTime(ts: string) {
  const d = new Date(ts)
  return `${d.toLocaleTimeString()}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

function gridColumns(parties: Party[]) {
  return `96px repeat(${Math.max(parties.length, 1)}, minmax(150px, 1fr))`
}

function LadderLanes({ parties }: { parties: Party[] }) {
  return (
    <>
      {parties.map((p, i) => (
        <div
          key={p.id}
          className="trace-ladder-lane"
          style={{ gridColumn: i + 2, gridRow: '1 / span 2', ['--party-color' as string]: p.color } as CSSProperties}
        />
      ))}
    </>
  )
}

function EventNote({ e, parties, role }: { e: TraceEvent; parties: Party[]; role: { customer?: string; displayCaller?: string } }) {
  return (
    <div className="trace-ladder-row" style={{ gridTemplateColumns: gridColumns(parties) }}>
      <div className="trace-ladder-time">{formatEventTime(e.ts)}</div>
      <LadderLanes parties={parties} />
      <div className="trace-ladder-note" style={{ gridColumn: '2 / -1' }}>
        <div className="trace-ladder-message-main">
          <Tag color={chanColor[e.channel]}>{e.channel}</Tag>
          <Text strong>{e.method}</Text>
          {e.leg && <Tag color="default">{legLabel(e.leg, role)}</Tag>}
        </div>
        <div className="trace-ladder-summary">{e.summary}</div>
      </div>
      <div className="trace-ladder-details" style={{ gridColumn: '2 / -1' }}>
        <EventDetails e={e} />
      </div>
    </div>
  )
}

function LadderEventRow({ e, parties, role }: { e: TraceEvent; parties: Party[]; role: { customer?: string; displayCaller?: string } }) {
  const endpoints = sipEndpoints(e, role)
  const fromIndex = endpoints ? parties.findIndex((p) => p.id === endpoints.from) : -1
  const toIndex = endpoints ? parties.findIndex((p) => p.id === endpoints.to) : -1

  if (!endpoints || fromIndex < 0 || toIndex < 0 || fromIndex === toIndex) {
    return <EventNote e={e} parties={parties} role={role} />
  }

  const start = Math.min(fromIndex, toIndex) + 2
  const end = Math.max(fromIndex, toIndex) + 3
  const forward = fromIndex < toIndex
  const color = legPalette[Math.min(fromIndex, toIndex) % legPalette.length]

  return (
    <div className="trace-ladder-row" style={{ gridTemplateColumns: gridColumns(parties) }}>
      <div className="trace-ladder-time">{formatEventTime(e.ts)}</div>
      <LadderLanes parties={parties} />
      <div
        className={`trace-ladder-signal ${forward ? 'is-forward' : 'is-reverse'}`}
        style={{ gridColumn: `${start} / ${end}`, color }}
      >
        <span className="trace-ladder-arrow-line" />
        <div className="trace-ladder-message">
          <div className="trace-ladder-message-main">
            <Tag color={chanColor[e.channel]}>{e.channel}</Tag>
            <Text strong>{e.method}</Text>
            {header(e.headers, 'Call-ID') && <Tag color="default">Call-ID {shortID(header(e.headers, 'Call-ID'))}</Tag>}
          </div>
          <div className="trace-ladder-summary">{e.summary}</div>
        </div>
      </div>
      <div className="trace-ladder-details" style={{ gridColumn: '2 / -1' }}>
        <EventDetails e={e} />
      </div>
    </div>
  )
}

function TraceLadder({ events, parties, role }: { events: TraceEvent[]; parties: Party[]; role: { customer?: string; displayCaller?: string } }) {
  return (
    <div className="trace-ladder-wrap">
      <div className="trace-ladder-grid">
        <div className="trace-ladder-header" style={{ gridTemplateColumns: gridColumns(parties) }}>
          <div className="trace-ladder-time-head">时间</div>
          {parties.map((p) => (
            <div key={p.id} className="trace-ladder-party" style={{ ['--party-color' as string]: p.color } as CSSProperties}>
              <span className="trace-ladder-party-dot" />
              <span className="trace-ladder-party-name">{p.label}</span>
            </div>
          ))}
        </div>
        {events.map((e) => (
          <LadderEventRow key={e.seq} e={e} parties={parties} role={role} />
        ))}
      </div>
    </div>
  )
}

export default function CallTracePage() {
  const [sessions, setSessions] = useState<TraceSession[]>([])
  const [sel, setSel] = useState<string>('')
  const [detail, setDetail] = useState<TraceSession | undefined>() // 选中会话的完整轨迹（含 events，单查）
  const [legFilter, setLegFilter] = useState<string>('全部')
  // 自动刷新默认关闭；与其它记录页一致，用户打开后记住偏好。
  const [auto, setAuto] = useState(() => {
    try { return localStorage.getItem(TRACE_AUTO_REFRESH_KEY) === '1' } catch { return false }
  })
  const selRef = useRef(sel)
  selRef.current = sel

  useEffect(() => {
    try { localStorage.setItem(TRACE_AUTO_REFRESH_KEY, auto ? '1' : '0') } catch { /* ignore */ }
  }, [auto])

  // 列表只拉摘要（不含 events），轻量；标签页隐藏时由 usePolling 跳过。
  const load = useCallback(async () => {
    try {
      const ss = await getTraceSessions()
      setSessions(ss)
      const want = new URLSearchParams(window.location.search).get('session')
      setSel((cur) => cur || want || (ss[0]?.id ?? ''))
    } catch {
      /* ignore */
    }
  }, [])

  const loadDetail = useCallback(async (id: string) => {
    try {
      const d = await getTraceSession(id)
      if (selRef.current === id) setDetail(d)
    } catch {
      /* ignore */
    }
  }, [])

  const refresh = useCallback(async () => {
    await load()
    if (selRef.current) await loadDetail(selRef.current)
  }, [load, loadDetail])

  useEffect(() => { void load() }, [load])
  usePolling(load, 3000, { enabled: auto, immediate: false })

  // 选中会话 → 单查完整轨迹（含 events）。切换时先清空，避免梯形图闪上一通；自动刷新关闭时只查一次。
  useEffect(() => {
    if (!sel) { setDetail(undefined); return }
    setDetail(undefined)
    void loadDetail(sel)
  }, [sel, loadDetail])
  usePolling(() => {
    if (selRef.current) return loadDetail(selRef.current)
  }, 3000, { enabled: auto && !!sel, immediate: false })

  const current = sessions.find((s) => s.id === sel) // 列表摘要项（选中态/兜底标题）
  const role = callRole(detail)
  const legs = detail?.legs ?? current?.legs ?? []
  const shownEvents = (detail?.events ?? []).filter(
    (e) => legFilter === '全部' || e.leg === legFilter || (legFilter === '无腿' && !e.leg),
  )
  const parties = useMemo(() => buildParties(detail?.events ?? [], legs, role), [detail, legs, role.customer, role.displayCaller])
  const sipIDs = useMemo(() => sipCallIDs(detail), [detail])
  const multiLeg = legs.length > 1

  return (
    <div className="page-container">
      <PageHeader
        title="通话链路"
        status={{ tone: auto ? 'success' : 'neutral', text: auto ? '自动刷新开' : '自动刷新关' }}
        extra={(
          <Space size={12}>
            <Tooltip title="每 3 秒自动刷新会话列表与当前详情">
              <Space size={4}>
                <Text type="secondary" style={{ fontSize: 12 }}>自动</Text>
                <Switch size="small" checked={auto} onChange={setAuto} />
              </Space>
            </Tooltip>
            <Button icon={<ReloadOutlined />} onClick={() => void refresh()}>刷新</Button>
          </Space>
        )}
      />
      <InfoBanner title="按 SIP Call-ID 抓单腿真实报文 · 同一通业务多腿按 callUuid 归并">
        左侧选会话 → 右侧看事件梯形图（可展开真实 SIP 报文）。同一通业务通话的多腿在读时按 callUuid 归并装配（纯展示、不写回）。
      </InfoBanner>
      <Row gutter={[16, 16]}>
        <Col xs={24} xl={7}>
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
                { title: '事件', width: 50, render: (_: unknown, r: TraceSession) => r.eventCount ?? 0 },
              ]}
            />
          </Card>
        </Col>
        <Col xs={24} xl={17}>
          <Card
            title={detail ? traceTitle(detail) : current ? `通话链路 · ${current.title}` : '通话链路'}
            size="small"
            extra={detail && (
              <Text type="secondary" style={{ fontSize: 12 }}>{detail.events?.length ?? 0} 事件{detail.callId ? ` · ${shortID(detail.callId)}` : ''}</Text>
            )}
          >
            {!detail || (detail.events?.length ?? 0) === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="选择左侧会话查看事件梯形图（可展开真实 SIP 报文）" />
            ) : (
              <>
                <div className="trace-aggregate-strip">
                  <Space wrap size={4}>
                    <Tag color={multiLeg ? 'orange' : 'blue'}>业务会话聚合</Tag>
                    {detail.callId && <Text type="secondary" style={{ fontSize: 12 }}>业务ID {shortID(detail.callId)}</Text>}
                    <Text type="secondary" style={{ fontSize: 12 }}>SIP Call-ID {sipIDs.length || 0} 个</Text>
                    <Text type="secondary" style={{ fontSize: 12 }}>逻辑标签 {legs.length} 个</Text>
                    {parties.map((p) => (
                      <Tag key={p.id} color={p.color} style={{ color: '#fff', border: 'none' }}>{p.label}</Tag>
                    ))}
                  </Space>
                </div>
                {legs.length > 0 && (
                  <Segmented
                    size="small"
                    style={{ marginBottom: 12 }}
                    value={legFilter}
                    onChange={(v) => setLegFilter(String(v))}
                    options={['全部', ...legs.map((l) => ({ label: legLabel(l, role), value: l }))]}
                  />
                )}
                <TraceLadder events={shownEvents} parties={parties} role={role} />
              </>
            )}
          </Card>
        </Col>
      </Row>
    </div>
  )
}
