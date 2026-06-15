import { useEffect, useState } from 'react'
import { Button, Col, Drawer, Form, Input, InputNumber, Row, Select, Tabs, Typography, message } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { ScenarioHeader } from '../components/scenario/ScenarioHeader'
import { CallRows, ResultBanner, callsOf, parseKV, parseList, type BannerVerdict } from '../components/scenario/utils'
import ScenarioRecords from '../components/scenario/ScenarioRecords'
import { InfoBanner } from '../components/layout/InfoBanner'
import { useScenarioMeta } from '../hooks/useScenarioMeta'
import { runAutoCall, runCallBotTask, testRuns } from '../api'
import type { TestRun } from '../api'

const { Text } = Typography
const { TextArea } = Input

const randSuffix = () => Math.random().toString(36).slice(2, 8)

// 把一次 run 的通话归纳成结论横幅参数。
function runVerdict(run: TestRun | null, label: string) {
  const cs = callsOf(run)
  const answered = cs.filter((c) => c.status === 'OBSERVED' || c.status === 'CONNECTED').length
  const failed = cs.filter((c) => c.status === 'FAILED').length
  const pending = cs.length - answered - failed
  const verdict: BannerVerdict = pending > 0 ? 'running' : failed > 0 ? 'fail' : answered > 0 ? 'success' : 'idle'
  const passRate = cs.length ? Math.round((answered / cs.length) * 100) : 0
  const title = pending > 0 ? `${label}进行中` : failed > 0 ? `${label}完成 · ${failed} 通未接通` : answered > 0 ? `${label}完成 · 已观测客户腿` : `${label}已受理`
  return { cs, answered, failed, pending, verdict, passRate, title }
}

export default function CallbotPage() {
  const { pf, currentOrg, customerOptions, reload } = useScenarioMeta()
  const [botForm] = Form.useForm()
  const [autoForm] = Form.useForm()
  const [running, setRunning] = useState<boolean>(false)
  const [botRun, setBotRun] = useState<TestRun | null>(null)
  const [autoRun, setAutoRun] = useState<TestRun | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [tab, setTab] = useState<'callbot' | 'autocall'>('callbot')
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
      setDrawerOpen(false)
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
      setDrawerOpen(false)
      message[r.ok ? 'success' : 'error'](r.ok ? 'autocall 已观测到客户腿' : 'autocall 未通过')
    } catch (e) {
      message.error(String(e))
    } finally {
      setRunning(false)
    }
  }

  const botFormUI = (
    <Form
      form={botForm}
      layout="vertical"
      initialValues={{ taskType: 2, customerLimit: 10, waitSec: 60, name: `mock_bot_${randSuffix()}` }}
    >
      <Row gutter={12}>
        <Col span={12}>
          <Form.Item name="name" label="任务名" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
        </Col>
        <Col span={12}>
          <Form.Item name="taskType" label="任务类型">
            <Select options={[{ value: 1, label: 'IVR' }, { value: 2, label: 'AI_CALL' }]} />
          </Form.Item>
        </Col>
        <Col span={12}>
          <Form.Item name="robotCode" label="机器人 code">
            <Input />
          </Form.Item>
        </Col>
        <Col span={12}>
          <Form.Item name="salesScriptCode" label="话术 code">
            <Input />
          </Form.Item>
        </Col>
        <Col span={8}>
          <Form.Item name="waitSec" label="等待秒">
            <InputNumber min={20} max={180} style={{ width: '100%' }} />
          </Form.Item>
        </Col>
        <Col span={16}>
          <Form.Item name="customerGroup" label="客户组">
            <Select allowClear options={customerOptions} />
          </Form.Item>
        </Col>
        <Col span={8}>
          <Form.Item name="customerLimit" label="取号数量">
            <InputNumber min={1} max={200} style={{ width: '100%' }} />
          </Form.Item>
        </Col>
        <Col span={24}>
          <Form.Item name="numbers" label="客户号码覆盖（可空；为空则从客户组取号）">
            <TextArea rows={3} />
          </Form.Item>
        </Col>
      </Row>
    </Form>
  )

  const autoFormUI = (
    <Form form={autoForm} layout="vertical" initialValues={{ customerLimit: 10, waitSec: 60 }}>
      <InfoBanner title="call-bot 模板自动外呼：按 TTS 模板对客户组/号码直接发起外呼（不建任务），mock 扮客户被叫应答。" />
      <Form.Item name="templateCode" label="模板 code" rules={[{ required: true }]}>
        <Input />
      </Form.Item>
      <Form.Item name="ttsVars" label="模板变量">
        <Input placeholder="name=张三,amount=100" />
      </Form.Item>
      <Row gutter={12}>
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
      </Row>
      <Form.Item name="waitSec" label="等待秒">
        <InputNumber min={20} max={180} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item name="numbers" label="客户号码覆盖（可空；为空则从客户组取号）">
        <TextArea rows={3} />
      </Form.Item>
    </Form>
  )

  const latest = [botRun, autoRun].filter(Boolean).sort((a, b) =>
    new Date(b!.startedAt).getTime() - new Date(a!.startedAt).getTime())[0] || null
  const latestLabel = latest && latest === autoRun ? '自动外呼' : 'call-bot 任务'
  const v = runVerdict(latest, latestLabel)

  return (
    <div className="page-container">
      <ScenarioHeader
        title="call-bot 机器人外呼"
        ready={pf?.autoCall}
        onReload={reload}
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => setDrawerOpen(true)}>新建 call-bot 任务</Button>}
      />

      <Drawer
        title={(
          <div>
            <div style={{ fontSize: 16, fontWeight: 500 }}>新建 call-bot 外呼</div>
            <div style={{ fontSize: 12, color: '#94a3b8', fontWeight: 400 }}>当前机构 {currentOrg}</div>
          </div>
        )}
        width={560}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        footer={(
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12 }}>
            <Button onClick={() => setDrawerOpen(false)}>取消</Button>
            <Button type="primary" loading={running} onClick={tab === 'callbot' ? runBot : runAuto}>
              {tab === 'callbot' ? '创建 call-bot 任务' : '发起模板自动外呼'}
            </Button>
          </div>
        )}
      >
        <Tabs
          activeKey={tab}
          onChange={(k) => setTab(k as 'callbot' | 'autocall')}
          items={[
            { key: 'callbot', label: 'call-bot 任务', children: botFormUI },
            { key: 'autocall', label: '自动外呼(模板)', children: autoFormUI },
          ]}
        />
      </Drawer>

      {latest ? (
        <>
          <ResultBanner
            verdict={v.verdict}
            title={v.title}
            sub={`run ${latest.id}`}
            metrics={[
              { label: '总通话', value: v.cs.length },
              { label: '已接通', value: v.answered, color: '#16a34a' },
              { label: '未接通', value: v.failed, color: v.failed ? '#dc2626' : undefined },
              { label: '通过率', value: `${v.passRate}%` },
            ]}
          />
          <Text type="secondary" style={{ fontSize: 12, display: 'block', margin: '10px 0' }}>
            展开任一通看协商编解码 / RTP 收发·丢包 / DTMF 采集 / 挂断码
          </Text>
          <CallRows calls={v.cs} />
        </>
      ) : (
        <ResultBanner verdict="idle" title="尚未发起 call-bot 外呼" sub="点右上「新建 call-bot 任务」填写表单，结果在此呈现" />
      )}

      <Tabs
        style={{ marginTop: 8 }}
        items={[
          { key: 'callbot', label: 'call-bot 任务记录', children: <ScenarioRecords scenario="callbot-task" caseKinds={['callbot-task']} title="call-bot 任务通话记录 / 测试历史" /> },
          { key: 'autocall', label: '自动外呼记录', children: <ScenarioRecords scenario="autocall" caseKinds={['autocall']} title="自动外呼通话记录 / 测试历史" /> },
        ]}
      />
    </div>
  )
}
