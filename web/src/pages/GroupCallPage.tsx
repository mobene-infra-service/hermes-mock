import { useEffect, useState } from 'react'
import {
  AutoComplete, Button, Card, Col, Form, Input, InputNumber, Row, Select, Switch, Typography, message,
} from 'antd'
import { ScenarioHeader } from '../components/scenario/ScenarioHeader'
import { CallBoard, RunSteps, ScenarioSummary, callsOf, parseList } from '../components/scenario/utils'
import ScenarioRecords from '../components/scenario/ScenarioRecords'
import { useScenarioMeta } from '../hooks/useScenarioMeta'
import { LINE_TYPE_OPTIONS } from '../constants/enums'
import { queryCallRecords, runCallCenterTask, testRuns, type CallView, type TestRun } from '../api'

const { Text } = Typography

interface GroupCallForm {
  orgCode: string
  name: string
  customerGroup?: string
  customerLimit?: number
  agentGroups?: string[] | string
  proportion?: number
  ttsCode?: string
  ttsText?: string
  startDate?: string
  endDate?: string
  dialTimePeriod?: string
  lineType?: string
  autoStart?: boolean
  waitSec?: number
  observeAgent?: string
  numbers?: string
}

// call-center 群呼任务独立页：Hermes 业务侧创建并启动群呼任务，mock 扮客户被叫应答。
export default function GroupCallPage() {
  const {
    pf, currentOrg, customerOptions, hermesSkillOptions, ttsOptions,
    bootstrapping, bootstrap, reload,
  } = useScenarioMeta()
  const [form] = Form.useForm()
  const [ccRun, setCcRun] = useState<TestRun | null>(null)
  const [running, setRunning] = useState<boolean>(false)
  const [restored, setRestored] = useState(false) // ccRun 是否为切页前的历史结果（非本次新发起）
  const [liveCalls, setLiveCalls] = useState<CallView[] | null>(null) // 轮询刷新的实时通话状态（覆盖 run 快照）

  // 挂载时拉回最近一次群呼 run（切页/刷新回来仍能看到上次结果——run 已落库 mock_test_run）
  useEffect(() => {
    let alive = true
    testRuns().then((rs) => {
      if (!alive) return
      const last = rs.find((r) => r.case === 'callcenter-task')
      if (last) { setCcRun(last); setRestored(true) }
    }).catch(() => {})
    return () => { alive = false }
  }, [])

  // 群呼是预测式分批拨号：run 结束时只是「那一刻」快照，后续被拨到/转坐席的进展看不到。
  // 这里对当前 run 的客户号持续轮询后端记录（被叫腿 sip-inbound + 坐席腿 agent-inbound），实时更新卡片。
  useEffect(() => {
    const base = ccRun?.calls
    if (!base || !base.length) { setLiveCalls(null); return }
    const customers = Array.from(new Set(base.map((c) => c.customer).filter(Boolean)))
    if (!customers.length) { setLiveCalls(null); return }
    let alive = true
    const tick = async () => {
      try {
        // 拉被叫腿（客户接通）+ 坐席腿（转接接听）记录
        const [calleeRes, seatRes] = await Promise.all([
          queryCallRecords({ pageSize: 100 }),
          queryCallRecords({ scenario: 'agent-inbound', pageSize: 50 }),
        ])
        if (!alive) return
        const calleeRecs = calleeRes.records || []
        const seatRecs = seatRes.records || []
        const next: CallView[] = base.map((c) => {
          // 该客户号被叫腿是否接通（mock 收到并 ANSWERED/ENDED）
          const callee = calleeRecs.find((r) => r.customerNumber === c.customer && (r.scenario === 'sip-inbound' || r.source === 'mock' || r.source === 'sip'))
          // 是否有坐席被转接接听（按时间近似关联）
          const seat = seatRecs.find((r) => r.status === 'ENDED' || r.status === 'ANSWERED')
          const customerAns = callee && (callee.status === 'ANSWERED' || callee.status === 'ENDED' || (callee.signalSummary || '').includes('200'))
          let status = c.status
          let customerState = c.customerState
          let agentState = c.agentState
          if (customerAns) { status = 'OBSERVED'; customerState = '已接入' }
          if (seat) { status = 'CONNECTED'; agentState = `坐席 ${seat.agentNumber} 已接听` }
          return {
            ...c, status, customerState, agentState,
            traceId: callee?.traceId || c.traceId,
          }
        })
        setLiveCalls(next)
      } catch { /* ignore */ }
    }
    void tick()
    const t = setInterval(tick, 3000)
    return () => { alive = false; clearInterval(t) }
  }, [ccRun?.id])

  const runCC = async () => {
    let v: GroupCallForm
    try { v = await form.validateFields() } catch { return }
    setRunning(true)
    try {
      const agentGroups = Array.isArray(v.agentGroups) ? v.agentGroups : parseList(v.agentGroups)
      const r = await runCallCenterTask({
        ...v,
        numbers: parseList(v.numbers),
        agentGroups,
        dialTimePeriod: parseList(v.dialTimePeriod || '00:00-23:59'),
        lineType: (v.lineType || '').trim() || undefined, // 7cbb285：空=Hermes 默认 base
      })
      setCcRun(r)
      setRestored(false)
      message[r.ok ? 'success' : 'error'](r.ok ? '群呼任务已观测到客户腿' : '群呼任务未通过')
    } catch (e) {
      message.error(String(e))
    } finally {
      setRunning(false)
    }
  }

  const initialValues = {
    orgCode: currentOrg,
    name: `mock_cc_${Math.random().toString(36).slice(2, 8)}`,
    customerLimit: 10,
    proportion: 1,
    autoStart: true,
    waitSec: 90,
    dialTimePeriod: '00:00-23:59',
    startDate: new Date().toISOString().slice(0, 10),
    endDate: new Date(Date.now() + 7 * 864e5).toISOString().slice(0, 10),
  }

  return (
    <div className="page-container">
      <ScenarioHeader
        title="call-center 群呼任务"
        ready={pf?.callCenterTask}
        bootstrapping={bootstrapping}
        onBootstrap={bootstrap}
        onReload={reload}
      />

      <Row gutter={16}>
        <Col span={10}>
          <Card
            title="创建并启动群呼任务"
            extra={<Button type="primary" loading={running} onClick={runCC}>创建并启动</Button>}
          >
            <Form form={form} layout="vertical" initialValues={initialValues}>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="orgCode" label="组织码" rules={[{ required: true }]}>
                    <Input />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="name" label="任务名" rules={[{ required: true }]}>
                    <Input />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="customerGroup" label="客户组">
                    <Select allowClear options={customerOptions} />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="customerLimit" label="取号数量">
                    <InputNumber min={1} max={200} style={{ width: '100%' }} />
                  </Form.Item>
                </Col>
                <Col span={16}>
                  <Form.Item name="agentGroups" label="转接技能组" rules={[{ required: true }]}>
                    <Select mode="tags" options={hermesSkillOptions} />
                  </Form.Item>
                </Col>
                <Col span={8}>
                  <Form.Item name="proportion" label="拨号比例">
                    <InputNumber min={1} max={10} style={{ width: '100%' }} />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="ttsCode" label="TTS code" rules={[{ required: true }]}>
                    <AutoComplete options={ttsOptions} />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="ttsText" label="TTS 文本" rules={[{ required: true }]}>
                    <Input />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="startDate" label="开始日期">
                    <Input />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="endDate" label="结束日期">
                    <Input />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="dialTimePeriod" label="拨号时段">
                    <Input placeholder="00:00-23:59" />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item
                    name="lineType"
                    label="线路类型 lineType"
                    tooltip="Hermes 2026-06 特性：任务期间仅用该 type 线路选号（含重试换线锁同 type）。留空=默认 base"
                  >
                    <AutoComplete allowClear placeholder="留空=base" options={LINE_TYPE_OPTIONS} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="autoStart" label="自动启动" valuePropName="checked">
                    <Switch />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="waitSec" label="等待秒">
                    <InputNumber min={25} max={240} style={{ width: '100%' }} />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="observeAgent" label="期望WS坐席">
                    <Input placeholder="可空" />
                  </Form.Item>
                </Col>
                <Col span={24}>
                  <Form.Item name="numbers" label="客户号码覆盖（可空；为空则从客户组取号）">
                    <Input.TextArea rows={2} />
                  </Form.Item>
                </Col>
              </Row>
            </Form>
          </Card>
        </Col>

        <Col span={14}>
          {restored && ccRun && (
            <Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 8 }}>
              ↓ 上次群呼结果（run {ccRun.id} · {new Date(ccRun.startedAt).toLocaleString()}）——切页/刷新后从历史回填，点「创建并启动」发起新任务
            </Text>
          )}
          <ScenarioSummary run={ccRun} />
          {ccRun && (liveCalls?.length || callsOf(ccRun).length) ? (
            <Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 8 }}>
              通话状态每 3s 自动刷新（预测式分批拨号，客户腿/坐席腿接通进展实时更新）
            </Text>
          ) : null}
          <CallBoard calls={liveCalls || callsOf(ccRun)} />
          <RunSteps run={ccRun} />
        </Col>
      </Row>

      <ScenarioRecords scenario="callcenter-task" caseKinds={['callcenter-task']} title="群呼通话记录 / 测试历史" />
    </div>
  )
}
