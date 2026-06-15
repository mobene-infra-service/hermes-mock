import { useEffect, useState } from 'react'
import { Button, Checkbox, Col, Drawer, Form, Input, InputNumber, Row, Select, Typography, message } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import {
  CallRows, ResultBanner, callsOf, parseKV, parseList, type BannerVerdict,
} from '../components/scenario/utils'
import ScenarioRecords from '../components/scenario/ScenarioRecords'
import { ScenarioHeader } from '../components/scenario/ScenarioHeader'
import { useScenarioMeta } from '../hooks/useScenarioMeta'
import { runOTPBatch, testRuns, type ScenarioResult, type TestRun } from '../api'

const { Text } = Typography
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
  const { pf, currentOrg, customerOptions, reload } = useScenarioMeta()
  const [form] = Form.useForm<OtpForm>()
  const [otpRun, setOtpRun] = useState<ScenarioResult | TestRun | null>(null)
  const [running, setRunning] = useState<boolean>(false)
  const [drawerOpen, setDrawerOpen] = useState(false)

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
      setDrawerOpen(false)
      message[r.failed ? 'warning' : 'success'](`OTP 完成：${r.passed}/${r.total}`)
    } catch (e) {
      message.error(String(e))
    } finally {
      setRunning(false)
    }
  }

  const cs = callsOf(otpRun)
  const answered = cs.filter((c) => c.status === 'OBSERVED' || c.status === 'CONNECTED').length
  const failed = cs.filter((c) => c.status === 'FAILED').length
  const pending = cs.length - answered - failed
  const verdict: BannerVerdict = pending > 0 ? 'running' : failed > 0 ? 'fail' : answered > 0 ? 'success' : 'idle'
  const passRate = cs.length ? Math.round((answered / cs.length) * 100) : 0
  const title = pending > 0 ? 'OTP 下发中' : failed > 0 ? `OTP 完成 · ${failed} 通未达` : answered > 0 ? 'OTP 完成 · 全部送达' : 'OTP 已受理'

  return (
    <div className="page-container">
      <ScenarioHeader
        title="OTP 语音验证码"
        ready={pf?.otp}
        onReload={reload}
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => setDrawerOpen(true)}>批量下发 OTP</Button>}
      />

      <Drawer
        title={(
          <div>
            <div style={{ fontSize: 16, fontWeight: 500 }}>批量下发 OTP</div>
            <div style={{ fontSize: 12, color: '#94a3b8', fontWeight: 400 }}>当前机构 {currentOrg}</div>
          </div>
        )}
        width={480}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        footer={(
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12 }}>
            <Button onClick={() => setDrawerOpen(false)}>取消</Button>
            <Button type="primary" loading={running} onClick={() => form.submit()}>批量下发</Button>
          </div>
        )}
      >
        <Form
          form={form}
          layout="vertical"
          initialValues={{ customerLimit: 5, waitSec: 30, concurrent: false }}
          onFinish={runOtp}
        >
          <Form.Item name="templateCode" label="模板编码" rules={[{ required: true, message: '请填写模板编码' }]}>
            <Input placeholder="OTP 模板编码" />
          </Form.Item>
          <Form.Item name="params" label="模板参数">
            <Input placeholder="code=1234" />
          </Form.Item>
          <Row gutter={12}>
            <Col span={12}>
              <Form.Item name="customerGroup" label="客户组">
                <Select allowClear placeholder="选择客户组" options={customerOptions} />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="customerLimit" label="客户上限">
                <InputNumber min={1} max={100} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="waitSec" label="等待秒数">
                <InputNumber min={10} max={120} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="concurrent" label="并发下发" valuePropName="checked">
                <Checkbox>并发执行</Checkbox>
              </Form.Item>
            </Col>
          </Row>
          <Form.Item name="numbers" label="指定号码（可选）">
            <TextArea rows={4} placeholder="多个号码用逗号、空格或换行分隔" />
          </Form.Item>
        </Form>
      </Drawer>

      {otpRun ? (
        <>
          <ResultBanner
            verdict={verdict}
            title={title}
            sub={`run ${otpRun.id}`}
            metrics={[
              { label: '总通话', value: cs.length },
              { label: '已送达', value: answered, color: '#16a34a' },
              { label: '未送达', value: failed, color: failed ? '#dc2626' : undefined },
              { label: '通过率', value: `${passRate}%` },
            ]}
          />
          <Text type="secondary" style={{ fontSize: 12, display: 'block', margin: '10px 0' }}>
            展开任一通看协商编解码 / RTP 收发·丢包 / DTMF 采集 / 挂断码
          </Text>
          <CallRows calls={cs} />
        </>
      ) : (
        <ResultBanner verdict="idle" title="尚未下发 OTP" sub="点右上「批量下发 OTP」填写表单，结果在此呈现" />
      )}

      <ScenarioRecords scenario="otp" caseKinds={['otp']} title="OTP 通话记录 / 测试历史" />
    </div>
  )
}
