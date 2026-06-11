import { useEffect, useState } from 'react'
import { Alert, Button, Card, Col, Form, Input, InputNumber, Row, Select, Tabs, message } from 'antd'
import { ScenarioHeader } from '../components/scenario/ScenarioHeader'
import { CallBoard, RunSteps, ScenarioSummary, callsOf, parseKV, parseList } from '../components/scenario/utils'
import ScenarioRecords from '../components/scenario/ScenarioRecords'
import { useScenarioMeta } from '../hooks/useScenarioMeta'
import { runAutoCall, runCallBotTask, testRuns } from '../api'
import type { TestRun } from '../api'

const { TextArea } = Input

const randSuffix = () => Math.random().toString(36).slice(2, 8)

export default function CallbotPage() {
  const { pf, customerOptions, bootstrapping, bootstrap, reload } = useScenarioMeta()
  const [botForm] = Form.useForm()
  const [autoForm] = Form.useForm()
  const [running, setRunning] = useState<boolean>(false)
  const [botRun, setBotRun] = useState<TestRun | null>(null)
  const [autoRun, setAutoRun] = useState<TestRun | null>(null)

  // 挂载时拉回最近一次 call-bot / autocall run（切页/刷新回来仍能看到上次结果——已落库）
  useEffect(() => {
    let alive = true
    testRuns().then((rs) => {
      if (!alive) return
      const bot = rs.find((r) => r.case === 'callbot-task')
      const auto = rs.find((r) => r.case === 'autocall')
      if (bot) setBotRun(bot)
      if (auto) setAutoRun(auto)
    }).catch(() => {})
    return () => { alive = false }
  }, [])

  const runBot = async () => {
    let v: {
      name: string; taskType?: number; robotCode?: string; salesScriptCode?: string
      waitSec?: number; customerGroup?: string; customerLimit?: number; numbers?: string
    }
    try {
      v = await botForm.validateFields()
    } catch {
      return
    }
    setRunning(true)
    try {
      const r = await runCallBotTask({ ...v, numbers: parseList(v.numbers) })
      setBotRun(r)
      message[r.ok ? 'success' : 'error'](r.ok ? 'call-bot 任务已观测到客户腿' : 'call-bot 任务未通过')
    } catch (e) {
      message.error(String(e))
    } finally {
      setRunning(false)
    }
  }

  const runAuto = async () => {
    let v: {
      templateCode: string; ttsVars?: string; customerGroup?: string
      customerLimit?: number; waitSec?: number; numbers?: string
    }
    try {
      v = await autoForm.validateFields()
    } catch {
      return
    }
    setRunning(true)
    try {
      const r = await runAutoCall({ ...v, numbers: parseList(v.numbers), ttsVars: parseKV(v.ttsVars) })
      setAutoRun(r)
      message[r.ok ? 'success' : 'error'](r.ok ? 'autocall 已观测到客户腿' : 'autocall 未通过')
    } catch (e) {
      message.error(String(e))
    } finally {
      setRunning(false)
    }
  }

  const botTab = (
    <Row gutter={16}>
      <Col span={9}>
        <Card
          title="call-bot 任务配置"
          extra={<Button type="primary" loading={running} onClick={runBot}>创建 call-bot 任务</Button>}
        >
          <Form
            form={botForm}
            layout="vertical"
            initialValues={{ taskType: 2, customerLimit: 10, waitSec: 60, name: `mock_bot_${randSuffix()}` }}
          >
            <Form.Item name="name" label="任务名" rules={[{ required: true }]}>
              <Input />
            </Form.Item>
            <Form.Item name="taskType" label="任务类型">
              <Select options={[{ value: 1, label: 'IVR' }, { value: 2, label: 'AI_CALL' }]} />
            </Form.Item>
            <Form.Item name="robotCode" label="机器人 code">
              <Input />
            </Form.Item>
            <Form.Item name="salesScriptCode" label="话术 code">
              <Input />
            </Form.Item>
            <Form.Item name="waitSec" label="等待秒">
              <InputNumber min={20} max={180} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="customerGroup" label="客户组">
              <Select allowClear options={customerOptions} />
            </Form.Item>
            <Form.Item name="customerLimit" label="取号数量">
              <InputNumber min={1} max={200} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="numbers" label="客户号码覆盖（可空；为空则从客户组取号）">
              <TextArea rows={3} />
            </Form.Item>
          </Form>
        </Card>
      </Col>
      <Col span={15}>
        <ScenarioSummary run={botRun} />
        <CallBoard calls={callsOf(botRun)} />
        <RunSteps run={botRun} />
      </Col>
    </Row>
  )

  const botRecords = <ScenarioRecords scenario="callbot-task" caseKinds={['callbot-task']} title="call-bot 任务通话记录 / 测试历史" />
  const autoRecords = <ScenarioRecords scenario="autocall" caseKinds={['autocall']} title="自动外呼通话记录 / 测试历史" />

  const autoTab = (
    <Row gutter={16}>
      <Col span={9}>
        <Card
          title="自动外呼(模板)配置"
          extra={<Button type="primary" loading={running} onClick={runAuto}>发起模板自动外呼</Button>}
        >
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 12 }}
            message="call-bot 模板自动外呼：按 TTS 模板对客户组/号码直接发起外呼（不建任务），mock 扮客户被叫应答。"
          />
          <Form
            form={autoForm}
            layout="vertical"
            initialValues={{ customerLimit: 10, waitSec: 60 }}
          >
            <Form.Item name="templateCode" label="模板 code" rules={[{ required: true }]}>
              <Input />
            </Form.Item>
            <Form.Item name="ttsVars" label="模板变量">
              <Input placeholder="name=张三,amount=100" />
            </Form.Item>
            <Form.Item name="customerGroup" label="客户组">
              <Select allowClear options={customerOptions} />
            </Form.Item>
            <Form.Item name="customerLimit" label="取号数量">
              <InputNumber min={1} max={200} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="waitSec" label="等待秒">
              <InputNumber min={20} max={180} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="numbers" label="客户号码覆盖（可空；为空则从客户组取号）">
              <TextArea rows={3} />
            </Form.Item>
          </Form>
        </Card>
      </Col>
      <Col span={15}>
        <ScenarioSummary run={autoRun} />
        <CallBoard calls={callsOf(autoRun)} />
        <RunSteps run={autoRun} />
      </Col>
    </Row>
  )

  return (
    <div className="page-container">
      <ScenarioHeader
        title="call-bot 机器人外呼"
        ready={pf?.autoCall}
        bootstrapping={bootstrapping}
        onBootstrap={bootstrap}
        onReload={reload}
      />
      <Tabs
        items={[
          { key: 'callbot', label: 'call-bot 任务', children: <>{botTab}{botRecords}</> },
          { key: 'autocall', label: '自动外呼(模板)', children: <>{autoTab}{autoRecords}</> },
        ]}
      />
    </div>
  )
}
