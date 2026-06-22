import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Badge, Button, Card, Descriptions, Drawer, Empty, Segmented, Space, Switch, Table, Tag, Timeline, Tooltip, Typography } from 'antd'
import { CheckCircleTwoTone, CloseCircleTwoTone, MinusCircleTwoTone, ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import { JSONBlock, recordStatusColor, shortText, timeText } from './utils'
import { queryCallRecords, testRuns, type CallRecord, type TestRun } from '../../api'
import { usePolling } from '../../hooks/usePolling'

const { Text, Paragraph } = Typography

// 各通话场景页内嵌的「本场景记录」组件（替代被删的统一呼叫记录页，下沉到每页直接查）。
// scenario/scenarios 均为精确过滤；多场景（如坐席外呼+来电）显式传 scenarios。
// 两视图：通话记录(call-records，按 scenario) + 测试历史(test runs，按 caseKinds)。自动刷新默认关闭，
// 由用户按需开启（开关状态按场景持久化到 localStorage，切页重挂载不复位）。
export interface ScenarioRecordsProps {
  scenario: string // 主场景记录的精确过滤值
  scenarios?: string[] // 主视图多场景精确过滤；优先于 scenario
  caseKinds?: string[] // 测试历史按这些 run.case 过滤；为空则不显示测试历史视图
  title?: string
  // 可选的次视图（如坐席页的「原始被叫 sip」）：再给一个 scenario 过滤
  altLabel?: string
  altScenario?: string
  // 精确关联本次任务（群呼用）：传本次客户号 + 起始时间，则查 scenario 后按「客户号 ∈ 本次 && 时间 ≥ since」前端过滤
  filterCustomers?: string[]
  sinceMs?: number
}

type View = 'records' | 'runs' | 'alt'

const PAGE_SIZE = 10

export default function ScenarioRecords({ scenario, scenarios, caseKinds, title = '本场景记录', altLabel, altScenario, filterCustomers, sinceMs }: ScenarioRecordsProps) {
  const [view, setView] = useState<View>('records')
  const [records, setRecords] = useState<CallRecord[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [runs, setRuns] = useState<TestRun[]>([])
  const [loading, setLoading] = useState(false)
  // 自动刷新默认关闭；按场景记住用户选择（切页/切 Tab 重新挂载也不复位）。
  const autoKey = `hm.records.auto.${scenario}`
  const [auto, setAuto] = useState(() => {
    try { return localStorage.getItem(autoKey) === '1' } catch { return false }
  })
  useEffect(() => {
    try { localStorage.setItem(autoKey, auto ? '1' : '0') } catch { /* ignore */ }
  }, [autoKey, auto])
  const [detail, setDetail] = useState<CallRecord | null>(null)
  const pageRef = useRef(1)
  pageRef.current = page
  const rootRef = useRef<HTMLDivElement>(null)

  const activeScenario = view === 'alt' ? (altScenario || scenario) : scenario
  const scenariosKey = (scenarios || []).join('\u001f')
  const activeScenarios = useMemo(() => {
    if (view === 'alt' || !scenariosKey) return undefined
    return scenariosKey.split('\u001f').filter(Boolean)
  }, [view, scenariosKey])

  const clientFilter = !!filterCustomers?.length
  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      if (view === 'runs') {
        const all = await testRuns()
        setRuns(caseKinds?.length ? all.filter((r) => caseKinds.includes(r.case)) : all)
      } else if (clientFilter) {
        // 精确关联本次任务：查更大范围后按「客户号 ∈ 本次 && 时间 ≥ since」前端过滤（本次任务真实被叫腿）。前端分页。
        const r = await queryCallRecords({ scenario: activeScenarios?.length ? undefined : activeScenario, scenarios: activeScenarios, page: 1, pageSize: 200 })
        const set = new Set(filterCustomers)
        const since = sinceMs || 0
        const recs = (r.records || []).filter((x) => set.has(x.customerNumber) && (!since || new Date(x.startedAt).getTime() >= since - 5000))
        setRecords(recs)
        setTotal(recs.length)
      } else {
        const r = await queryCallRecords({ scenario: activeScenarios?.length ? undefined : activeScenario, scenarios: activeScenarios, page: pageRef.current, pageSize: PAGE_SIZE })
        setRecords(r.records || [])
        setTotal(r.total || 0)
      }
    } catch { /* ignore */ } finally {
      if (!silent) setLoading(false)
    }
  }, [view, activeScenario, activeScenarios, caseKinds, clientFilter, filterCustomers, sinceMs])

  useEffect(() => { void load() }, [load, page])
  // 自动刷新（静默，不闪 loading）。标签页隐藏或组件不可见（如常驻 display:none 的坐席软电话内）时跳过。
  usePolling(() => load(true), 4000, {
    enabled: auto,
    immediate: false,
    isVisible: () => rootRef.current?.offsetParent !== null,
  })

  const recordCols: ColumnsType<CallRecord> = [
    { title: '时间', dataIndex: 'startedAt', width: 158, render: (v) => <Text style={{ fontSize: 12 }}>{timeText(v)}</Text> },
    { title: '状态', dataIndex: 'status', width: 92, render: (v: string) => v ? <Tag color={recordStatusColor(v)}>{v}</Tag> : '-' },
    {
      title: '结果', dataIndex: 'result', ellipsis: true,
      render: (v: string, r) => {
        const bad = r.status === 'FAILED' || r.status === 'REJECTED'
        return v ? <Text type={bad ? 'danger' : undefined}>{v}</Text> : <Text type="secondary">{r.lastSummary || '-'}</Text>
      },
    },
    {
      title: '客户 / 坐席', width: 150,
      render: (_, r) => (
        <div style={{ fontSize: 12, lineHeight: 1.5 }}>
          {r.customerNumber ? <div><Text code>{r.customerNumber}</Text></div> : null}
          {r.agentNumber ? <div><Text type="secondary">坐席 {r.agentNumber}</Text></div> : null}
          {r.agentGroupCode ? <div><Text type="secondary">组 {r.agentGroupCode}</Text></div> : null}
          {!r.customerNumber && !r.agentNumber && !r.agentGroupCode ? '-' : null}
        </div>
      ),
    },
    { title: '时长', dataIndex: 'durationMs', width: 78, render: (v: number) => v ? <Text style={{ fontSize: 12 }}>{(v / 1000).toFixed(1)}s</Text> : '-' },
    {
      title: 'trace', dataIndex: 'traceId', width: 78,
      render: (v: string) => v ? <a href={`/trace?session=${encodeURIComponent(v)}`} target="_blank" rel="noreferrer">{shortText(v, 8)}</a> : '-',
    },
    { title: '', width: 56, render: (_, r) => <Button size="small" type="link" onClick={() => setDetail(r)}>详情</Button> },
  ]

  const runCols: ColumnsType<TestRun> = [
    { title: '时间', dataIndex: 'startedAt', width: 168, render: (v) => <Text style={{ fontSize: 12 }}>{timeText(v)}</Text> },
    { title: '用例', dataIndex: 'case', width: 140, render: (v: string) => <Tag color="geekblue">{v || '-'}</Tag> },
    { title: '结果', dataIndex: 'ok', width: 84, render: (ok: boolean) => <Tag color={ok ? 'success' : 'error'}>{ok ? '通过' : '失败'}</Tag> },
    { title: '耗时', dataIndex: 'durationMs', width: 84, render: (v: number) => v ? `${(v / 1000).toFixed(1)}s` : '-' },
    {
      title: '步骤断言',
      render: (_, r) => (
        <Space wrap size={4}>
          {(r.steps || []).map((s, i) => (
            <Tag key={i} color={s.optional && !s.ok ? 'default' : s.ok ? 'success' : 'error'} style={{ fontSize: 11 }}>
              {s.ok ? '✓' : s.optional ? '○' : '✗'} {s.name}
            </Tag>
          ))}
        </Space>
      ),
    },
    {
      title: 'trace', dataIndex: 'traceId', width: 78,
      render: (v?: string) => v ? <a href={`/trace?session=${encodeURIComponent(v)}`} target="_blank" rel="noreferrer">{shortText(v, 8)}</a> : '-',
    },
  ]

  const segOptions = [
    { label: '通话记录', value: 'records' as const },
    ...(altScenario ? [{ label: altLabel || '原始被叫', value: 'alt' as const }] : []),
    ...(caseKinds?.length ? [{ label: '测试历史', value: 'runs' as const }] : []),
  ]

  return (
    <div ref={rootRef}>
    <Card
      size="small"
      style={{ marginTop: 16 }}
      styles={{ body: { paddingTop: 12 } }}
      title={<Space><Text strong>{title}</Text>{view !== 'runs' && <Badge count={total} showZero overflowCount={9999} color="#1677ff" />}</Space>}
      extra={(
        <Space size={12}>
          {segOptions.length > 1 && (
            <Segmented size="small" value={view} onChange={(v) => { setView(v as View); setPage(1) }} options={segOptions} />
          )}
          <Tooltip title="每 4 秒自动刷新（预测式拨号/转接进展会随时间更新）">
            <Space size={4}><Text type="secondary" style={{ fontSize: 12 }}>自动</Text><Switch size="small" checked={auto} onChange={setAuto} /></Space>
          </Tooltip>
          <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>刷新</Button>
        </Space>
      )}
    >
      {view === 'runs' ? (
        <Table<TestRun>
          rowKey="id" size="small" loading={loading} dataSource={runs} columns={runCols}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无测试运行" /> }}
          pagination={{ pageSize: PAGE_SIZE, hideOnSinglePage: true, size: 'small' }}
        />
      ) : (
        <Table<CallRecord>
          rowKey="recordId" size="small" loading={loading} dataSource={records} columns={recordCols}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无通话记录——触发一次即可看到" /> }}
          pagination={clientFilter
            ? { pageSize: PAGE_SIZE, hideOnSinglePage: true, size: 'small' }
            : { current: page, pageSize: PAGE_SIZE, total, hideOnSinglePage: true, size: 'small', showSizeChanger: false }}
          onChange={clientFilter ? undefined : (p) => setPage(p.current || 1)}
        />
      )}

      <Drawer
        title={detail ? `通话详情 · ${detail.scenario}` : '通话详情'}
        width={680} open={!!detail} onClose={() => setDetail(null)} destroyOnClose
      >
        {detail && <RecordDetail r={detail} />}
      </Drawer>
    </Card>
    </div>
  )
}

// 通话详情抽屉内容：概览 Descriptions + 信令/媒体/回调摘要 Tag + 步骤断言时间线 + 原始 JSON。
function RecordDetail({ r }: { r: CallRecord }) {
  let steps: { name: string; ok: boolean; detail?: string; optional?: boolean }[] = []
  try { steps = JSON.parse(r.stepsJson || '[]') } catch { /* ignore */ }
  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Descriptions size="small" column={1} bordered
        items={[
          { key: 'status', label: '状态', children: <Tag color={recordStatusColor(r.status)}>{r.status}</Tag> },
          { key: 'result', label: '结果', children: r.result || r.lastSummary || '-' },
          { key: 'cust', label: '客户号', children: r.customerNumber ? <Text code>{r.customerNumber}</Text> : '-' },
          ...(r.agentNumber ? [{ key: 'agent', label: '坐席', children: r.agentNumber }] : []),
          ...(r.agentGroupCode ? [{ key: 'ag', label: '技能组', children: r.agentGroupCode }] : []),
          ...(r.taskName ? [{ key: 'task', label: '任务', children: `${r.taskName}${r.taskCode ? ' · ' + r.taskCode : ''}` }] : []),
          ...(r.lineCode || r.lineAddress ? [{ key: 'line', label: '线路', children: `${r.lineCode || ''} ${r.lineAddress || ''}` }] : []),
          { key: 'dir', label: '方向', children: r.direction || '-' },
          { key: 'dur', label: '时长', children: r.durationMs ? `${(r.durationMs / 1000).toFixed(1)}s` : '-' },
          { key: 'time', label: '开始/应答/结束', children: <div style={{ fontSize: 12 }}>{timeText(r.startedAt)}<br />{timeText(r.answeredAt)}<br />{timeText(r.endedAt)}</div> },
          ...(r.traceId ? [{ key: 'trace', label: 'trace', children: <a href={`/trace?session=${encodeURIComponent(r.traceId)}`} target="_blank" rel="noreferrer">{r.traceId}</a> }] : []),
          ...(r.callUuid ? [{ key: 'uuid', label: 'callUuid', children: <Text style={{ fontSize: 11 }}>{r.callUuid}</Text> }] : []),
        ]}
      />
      {steps.length > 0 && (
        <div>
          <Text strong>步骤断言</Text>
          <Timeline
            style={{ marginTop: 10 }}
            items={steps.map((s) => ({
              color: s.optional && !s.ok ? 'gray' : s.ok ? 'green' : 'red',
              dot: s.optional && !s.ok ? <MinusCircleTwoTone twoToneColor="#bfbfbf" /> : s.ok ? <CheckCircleTwoTone twoToneColor="#52c41a" /> : <CloseCircleTwoTone twoToneColor="#ff4d4f" />,
              children: (
                <div>
                  <Text>{s.name}{s.optional ? <Tag color="default" style={{ marginLeft: 6, fontSize: 11 }}>参考</Tag> : null}</Text>
                  {s.detail && <Paragraph type="secondary" style={{ fontSize: 12, margin: '2px 0 0' }}>{s.detail}</Paragraph>}
                </div>
              ),
            }))}
          />
        </div>
      )}
      {r.detailJson && (
        <div>
          <Text strong>明细 JSON</Text>
          <div style={{ marginTop: 8 }}><JSONBlock value={r.detailJson} /></div>
        </div>
      )}
    </Space>
  )
}
