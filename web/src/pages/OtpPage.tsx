import { useEffect, useState } from 'react'
import { Button, Card, Checkbox, Col, Form, Input, InputNumber, Row, Select, message } from 'antd'
import {
  CallBoard, ScenarioSummary, callsOf, parseKV, parseList,
} from '../components/scenario/utils'
import ScenarioRecords from '../components/scenario/ScenarioRecords'
import { ScenarioHeader } from '../components/scenario/ScenarioHeader'
import { useScenarioMeta } from '../hooks/useScenarioMeta'
import { runOTPBatch, testRuns, type ScenarioResult, type TestRun } from '../api'

const { TextArea } = Input

interface OtpForm {
  templateCode: string
  params?: string
  customerGroup?: string
  customerLimit?: number
  waitSec?: number
  concurrent?: boolean
  numbers?: string
}

export default function OtpPage() {
  const { pf, customerOptions, bootstrapping, bootstrap, reload } = useScenarioMeta()
  const [form] = Form.useForm<OtpForm>()
  const [otpRun, setOtpRun] = useState<ScenarioResult | TestRun | null>(null)
  const [running, setRunning] = useState<boolean>(false)

  // 挂载时拉回最近一次 otp run（切页/刷新回来仍能看到上次结果——已落库）
  useEffect(() => {
    let alive = true
    testRuns().then((rs) => {
      if (!alive) return
      const last = rs.find((r) => r.case === 'otp')
      if (last) setOtpRun(last)
    }).catch(() => {})
    return () => { alive = false }
  }, [])

  const runOtp = async (v: OtpForm) => {
    setRunning(true)
    try {
      const r = await runOTPBatch({
        templateCode: v.templateCode,
        customerGroup: v.customerGroup,
        customerLimit: v.customerLimit,
        waitSec: v.waitSec,
        concurrent: v.concurrent,
        numbers: parseList(v.numbers),
        params: parseKV(v.params),
      })
      setOtpRun(r)
      message[r.failed ? 'warning' : 'success'](`OTP 完成：${r.passed}/${r.total}`)
    } catch (e) {
      message.error(String(e))
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="page-container">
      <ScenarioHeader
        title="OTP 语音验证码"
        ready={pf?.otp}
        bootstrapping={bootstrapping}
        onBootstrap={bootstrap}
        onReload={reload}
      />
      <Row gutter={16}>
        <Col span={9}>
          <Card
            title="批量下发 OTP"
            extra={
              <Button type="primary" loading={running} onClick={() => form.submit()}>
                批量下发
              </Button>
            }
          >
            <Form
              form={form}
              layout="vertical"
              initialValues={{ customerLimit: 5, waitSec: 30, concurrent: false }}
              onFinish={runOtp}
            >
              <Form.Item
                name="templateCode"
                label="模板编码"
                rules={[{ required: true, message: '请填写模板编码' }]}
              >
                <Input placeholder="OTP 模板编码" />
              </Form.Item>
              <Form.Item name="params" label="模板参数">
                <Input placeholder="code=1234" />
              </Form.Item>
              <Form.Item name="customerGroup" label="客户组">
                <Select allowClear placeholder="选择客户组" options={customerOptions} />
              </Form.Item>
              <Form.Item name="customerLimit" label="客户上限">
                <InputNumber min={1} max={100} style={{ width: '100%' }} />
              </Form.Item>
              <Form.Item name="waitSec" label="等待秒数">
                <InputNumber min={10} max={120} style={{ width: '100%' }} />
              </Form.Item>
              <Form.Item name="concurrent" label="并发下发" valuePropName="checked">
                <Checkbox>并发执行</Checkbox>
              </Form.Item>
              <Form.Item name="numbers" label="指定号码（可选）">
                <TextArea rows={4} placeholder="多个号码用逗号、空格或换行分隔" />
              </Form.Item>
            </Form>
          </Card>
        </Col>
        <Col span={15}>
          <ScenarioSummary run={otpRun} />
          <CallBoard calls={callsOf(otpRun)} />
        </Col>
      </Row>

      <ScenarioRecords scenario="otp" caseKinds={['otp']} title="OTP 通话记录 / 测试历史" />
    </div>
  )
}
