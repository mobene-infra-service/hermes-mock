import { useEffect, useMemo, useState } from 'react'
import {
  AutoComplete, Button, Col, Collapse, Drawer, Form, Input, InputNumber, Radio, Row, Select, Space, Switch, Tag, Typography, message,
} from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { ScenarioHeader } from '../components/scenario/ScenarioHeader'
import { CallRows, ResultBanner, callsOf, parseList, type BannerVerdict } from '../components/scenario/utils'
import ScenarioRecords from '../components/scenario/ScenarioRecords'
import { useScenarioMeta } from '../hooks/useScenarioMeta'
import { useReadyAgents } from '../hooks/useReadyAgents'
import { usePolling } from '../hooks/usePolling'
import { LINE_TYPE_OPTIONS, MODE_STRATEGY_OPTIONS, SORT_METHOD_OPTIONS, TRANSFER_TYPE_OPTIONS } from '../constants/enums'
import {
  cancelCallCenterTask, getCallCenterTaskStatus, listManagedAgents, pauseCallCenterTask, queryCallRecords,
  resumeCallCenterTask, runCallCenterTask, testRuns, type CallView, type ManagedAgent, type TestRun,
} from '../api'

const { Text } = Typography

interface GroupCallForm {
  name: string
  customerGroup?: string
  customerLimit?: number
  agentGroup?: string // 转接技能组（单选；Hermes @Size(max=1)）
  agentNumbers?: string[] // 指定坐席号（与技能组二选一）
  modeStrategy?: number // 1=比例 2=PID
  proportion?: number // modeStrategy=1 时必填
  lossRate?: number // modeStrategy=2 时必填
  historicalConnectionRate?: number // modeStrategy=2 时必填
  sortMethod?: number
  transferType?: string
  isPriorityTask?: boolean
  isVmHangup?: boolean
  maxRedialTimes?: number
  redialInterval?: number
  bestRingDuration?: number
  agentMaxRingDuration?: number
  assignDelaySeconds?: number
  description?: string
  ttsCode?: string
  ttsText?: string
  startDate?: string
  endDate?: string
  dialTimePeriod?: string
  lineType?: string
  waitSec?: number
  observeAgent?: string
  numbers?: string
}

// recordToCallStatus 把被叫腿记录的 status 映射成 CallView.status（统一术语见 utils.callOutcome）。
function recordToCallStatus(s?: string): CallView['status'] {
  if (s === 'ANSWERED' || s === 'ENDED') return 'OBSERVED'
  if (s === 'REJECTED' || s === 'FAILED') return 'FAILED'
  return 'PENDING'
}

// call-center 群呼任务独立页：Hermes 业务侧创建并启动群呼任务，mock 扮客户被叫应答。
export default function GroupCallPage() {
  const {
    pf, currentOrg, customerOptions, hermesSkillOptions, ttsOptions,
    reload,
  } = useScenarioMeta()
  const [form] = Form.useForm()
  const [ccRun, setCcRun] = useState<TestRun | null>(null)
  const [running, setRunning] = useState<boolean>(false)
  const [restored, setRestored] = useState(false) // ccRun 是否为切页前的历史结果（非本次新发起）
  const [liveCalls, setLiveCalls] = useState<CallView[] | null>(null) // 轮询刷新的实时通话状态（覆盖 run 快照）
  const [taskBusy, setTaskBusy] = useState(false) // 暂停/恢复/取消任务请求中
  const taskCode = (ccRun?.artifacts?.taskCode as string | undefined) || '' // Hermes 任务 code（暂停/取消用）
  const [assignMode, setAssignMode] = useState<'group' | 'numbers'>('group') // 坐席分配二选一
  const [drawerOpen, setDrawerOpen] = useState(false) // 新建任务配置抽屉
  const [managedAgents, setManagedAgents] = useState<ManagedAgent[]>([]) // 坐席号数据源
  const readyAgents = useReadyAgents() // 前端软电话已就绪（sipReady）坐席号——已就绪默认排前
  const agentNumberOptions = useMemo(() => {
    const readySet = new Set(readyAgents)
    return managedAgents
      .filter((a) => a.number)
      .map((a) => ({
        value: a.number as string,
        label: `${a.number}${a.agentName ? ' · ' + a.agentName : ''}`, // 仅号码+姓名（供搜索）；就绪标记走 optionRender
        ready: readySet.has(a.number as string),
      }))
      .sort((x, y) => Number(y.ready) - Number(x.ready)) // 已就绪排前
  }, [managedAgents, readyAgents])

  // 加载真实坐席号（指定坐席分配方式用）
  useEffect(() => {
    listManagedAgents({ pageSize: 500 }).then((r) => setManagedAgents(r.agents || [])).catch(() => { /* ignore */ })
  }, [])

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
  // 这里对当前 run 的客户号轮询后端「被叫腿」记录（scenario=sip-inbound），实时更新明细行。
  // 关联一律按 customerNumber 命中 + startedAt≥since 收窄（避免历史撞号）——坐席腿 mock 侧基本不可见
  // （坐席在真实 Hermes / 前端 jssip），故不再用「任意一条 agent-inbound ENDED」张冠李戴地标到每个客户卡。
  const liveBase = ccRun?.calls || []
  const viewCalls = liveCalls || liveBase
  const pendingCount = viewCalls.filter((c) => c.status !== 'OBSERVED' && c.status !== 'CONNECTED' && c.status !== 'FAILED').length
  const [pollTicks, setPollTicks] = useState(0)
  // run 切换：清实时态 + 重置轮询计数（新 run 重新观测）。
  useEffect(() => { setLiveCalls(null); setPollTicks(0) }, [ccRun?.id])
  // 轮询条件：有取号 && (还没拉到实时态 || 还有等待中) && 未超兜底上限(~5min)。
  // 终态（全部接通/未接通）即停；标签页隐藏由 usePolling 自动跳过——杜绝群呼结束后仍每 3s 拉 200 条。
  const livePollEnabled = liveBase.length > 0 && (liveCalls === null || pendingCount > 0) && pollTicks < 100
  usePolling(async () => {
    setPollTicks((n) => n + 1)
    const since = ccRun ? new Date(ccRun.startedAt).getTime() - 5000 : 0
    try {
      // 只拉被叫腿（mock 自己亲历的 sip-inbound）。按客户号 + 起始时间精确关联本次任务的真实通话。
      const res = await queryCallRecords({ scenario: 'sip-inbound', pageSize: 200 })
      const recs = (res.records || []).filter((r) => !since || new Date(r.startedAt).getTime() >= since)
      const next: CallView[] = liveBase.map((c) => {
        // 命中本次该客户号的被叫腿记录（取最近一条）——这是 mock 唯一可信的接通证据。
        const rec = recs
          .filter((r) => r.customerNumber === c.customer)
          .sort((a, b) => new Date(b.startedAt).getTime() - new Date(a.startedAt).getTime())[0]
        if (!rec) return c // 尚未观测到该号被叫腿 → 维持原占位（等待呼入）
        const o = recordToCallStatus(rec.status)
        return {
          ...c,
          status: o,
          customerState: o === 'OBSERVED' || o === 'CONNECTED' ? '已接入' : o === 'FAILED' ? `未接通（${rec.hangupCode || rec.result || '失败'}）` : c.customerState,
          traceId: rec.traceId || c.traceId,
          callUuid: rec.callUuid || c.callUuid,
          durationMs: rec.durationMs || c.durationMs,
          detail: rec.result || rec.lastSummary || c.detail,
        }
      })
      setLiveCalls(next)
    } catch { /* ignore */ }
  }, 3000, { enabled: livePollEnabled })

  const runCC = async () => {
    let v: GroupCallForm
    try { v = await form.validateFields() } catch { return }
    // 坐席分配二选一校验
    if (assignMode === 'group' ? !v.agentGroup : !(v.agentNumbers?.length)) {
      message.error(assignMode === 'group' ? '请选择转接技能组' : '请选择至少一个坐席号')
      return
    }
    setRunning(true)
    try {
      const r = await runCallCenterTask({
        ...v,
        orgCode: currentOrg, // 用当前机构（orgCode 不进请求体，由凭据头注入）
        numbers: parseList(v.numbers),
        agentGroups: assignMode === 'group' && v.agentGroup ? [v.agentGroup] : [],
        agentNumbers: assignMode === 'numbers' ? (v.agentNumbers || []) : [],
        dialTimePeriod: parseList(v.dialTimePeriod || '00:00-23:59'),
        lineType: (v.lineType || '').trim() || undefined, // 7cbb285：空=Hermes 默认 base
      })
      setCcRun(r)
      setRestored(false)
      if (r.ok) setDrawerOpen(false)
      message[r.ok ? 'success' : 'error'](r.ok ? '群呼任务已创建并自动拨号，下方实时观测客户腿/坐席腿进展' : '群呼任务创建失败')
    } catch (e) {
      message.error(String(e))
    } finally {
      setRunning(false)
    }
  }

  // 群呼任务生命周期：暂停/恢复/取消（createAndImport 后即自动拨号，运行期可经此控制）。
  const taskAction = async (action: 'pause' | 'resume' | 'cancel') => {
    if (!taskCode) return
    setTaskBusy(true)
    try {
      const fn = action === 'pause' ? pauseCallCenterTask : action === 'resume' ? resumeCallCenterTask : cancelCallCenterTask
      const res = await fn(taskCode)
      const label = action === 'pause' ? '暂停' : action === 'resume' ? '恢复' : '取消'
      if (res.error) message.error(`${label}任务失败：${res.error}`)
      else message.success(`已${label}任务 ${taskCode}`)
    } catch (e) {
      message.error(String(e))
    } finally {
      setTaskBusy(false)
    }
  }
  const taskStatus = async () => {
    if (!taskCode) return
    try {
      const res = await getCallCenterTaskStatus(taskCode)
      message.info(`任务状态：${JSON.stringify(res.data ?? res.error ?? res)}`)
    } catch (e) {
      message.error(String(e))
    }
  }

  const initialValues = {
    name: `mock_cc_${Math.random().toString(36).slice(2, 8)}`,
    customerLimit: 10,
    modeStrategy: 1,
    proportion: 1,
    sortMethod: 1,
    isPriorityTask: false,
    isVmHangup: true,
    bestRingDuration: 40,
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
        onReload={reload}
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => setDrawerOpen(true)}>新建群呼任务</Button>}
      />

      <Drawer
        title={(
          <div>
            <div style={{ fontSize: 16, fontWeight: 500 }}>新建群呼任务</div>
            <div style={{ fontSize: 12, color: '#94a3b8', fontWeight: 400 }}>建任务即自动拨号 · 当前机构 {currentOrg}</div>
          </div>
        )}
        width={560}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        footer={(
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12 }}>
            <Button onClick={() => setDrawerOpen(false)}>取消</Button>
            <Button type="primary" loading={running} onClick={runCC}>创建任务并自动拨号</Button>
          </div>
        )}
      >
        <Form form={form} layout="vertical" initialValues={initialValues}>
              <Row gutter={12}>
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
                <Col span={24}>
                  <Form.Item
                    label="坐席分配（二选一）"
                    tooltip="Hermes 群呼任务：转接技能组（@Size max=1，单选）或指定坐席号列表（max 500），二选一。"
                    style={{ marginBottom: 8 }}
                  >
                    <Radio.Group
                      value={assignMode}
                      onChange={(e) => setAssignMode(e.target.value)}
                      optionType="button"
                      buttonStyle="solid"
                      size="small"
                    >
                      <Radio.Button value="group">转接技能组</Radio.Button>
                      <Radio.Button value="numbers">指定坐席号</Radio.Button>
                    </Radio.Group>
                  </Form.Item>
                </Col>
                {assignMode === 'group' ? (
                  <Col span={16}>
                    <Form.Item name="agentGroup" label="转接技能组（单选）">
                      <Select allowClear showSearch optionFilterProp="label" options={hermesSkillOptions} placeholder="选 1 个技能组" />
                    </Form.Item>
                  </Col>
                ) : (
                  <Col span={16}>
                    <Form.Item
                      name="agentNumbers"
                      label="指定坐席号（可多选）"
                      tooltip="标「就绪」= 该坐席已在「坐席外呼」页软电话上线（sipReady），才能被群呼转接接听；已就绪默认排前。"
                    >
                      <Select
                        mode="multiple"
                        allowClear
                        showSearch
                        optionFilterProp="label"
                        options={agentNumberOptions}
                        optionRender={(opt) => (
                          <Space>
                            <span>{opt.label}</span>
                            {(opt.data as { ready?: boolean }).ready
                              ? <Tag color="green" style={{ marginInlineEnd: 0 }}>就绪</Tag>
                              : <Tag style={{ marginInlineEnd: 0 }}>未就绪</Tag>}
                          </Space>
                        )}
                        placeholder="选坐席号（绿标=软电话已就绪）"
                        maxTagCount="responsive"
                      />
                    </Form.Item>
                  </Col>
                )}
                <Col span={8}>
                  <Form.Item
                    name="modeStrategy"
                    label="模式策略"
                    tooltip="对照 Hermes：1=比例（每条接通线路同时外呼 N 路）；2=PID（按期望呼损率 + 历史接通率动态调速）。"
                  >
                    <Select options={MODE_STRATEGY_OPTIONS} />
                  </Form.Item>
                </Col>
                <Col span={16}>
                  <Form.Item noStyle shouldUpdate={(prev, cur) => prev.modeStrategy !== cur.modeStrategy}>
                    {({ getFieldValue }) => getFieldValue('modeStrategy') === 2 ? (
                      <Row gutter={12}>
                        <Col span={12}>
                          <Form.Item name="lossRate" label="期望呼损率 %" rules={[{ required: true }]}>
                            <InputNumber min={0} max={99} style={{ width: '100%' }} />
                          </Form.Item>
                        </Col>
                        <Col span={12}>
                          <Form.Item name="historicalConnectionRate" label="历史接通率 %" rules={[{ required: true }]}>
                            <InputNumber min={1} max={100} style={{ width: '100%' }} />
                          </Form.Item>
                        </Col>
                      </Row>
                    ) : (
                      <Form.Item name="proportion" label="拨号比例" rules={[{ required: true }]}>
                        <InputNumber min={1} max={10} style={{ width: '100%' }} />
                      </Form.Item>
                    )}
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="ttsCode" label="TTS 音色" rules={[{ required: true }]}>
                    <Select
                      showSearch
                      allowClear
                      optionFilterProp="label"
                      filterOption={(input, option) => {
                        const s = input.toLowerCase()
                        const o = option as { label?: string; code?: string }
                        return (o?.label ?? '').toLowerCase().includes(s) || (o?.code ?? '').toLowerCase().includes(s)
                      }}
                      options={ttsOptions}
                      placeholder="选 TTS 音色（显示名称，可搜名称/code）"
                    />
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
                <Col span={12}>
                  <Form.Item name="waitSec" label="等待秒">
                    <InputNumber min={25} max={240} style={{ width: '100%' }} />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item
                    name="observeAgent"
                    label="期望WS坐席（观测，可空）"
                    tooltip="mock 侧断言参数，不进 Hermes 请求体：填一个坐席号则额外断言「该坐席工作台 WS 收到来电/调度通知」；留空只断客户腿接通。"
                  >
                    <Input placeholder="可空，仅用于断言坐席工作台收到通知" />
                  </Form.Item>
                </Col>
                <Col span={24}>
                  <Collapse
                    size="small"
                    ghost
                    items={[{
                      key: 'adv',
                      label: '高级参数（重拨 / 响铃 / 转接 / 优先级，留空走 Hermes 默认）',
                      children: (
                        <Row gutter={12}>
                          <Col span={8}>
                            <Form.Item name="sortMethod" label="排序方式">
                              <Select options={SORT_METHOD_OPTIONS} />
                            </Form.Item>
                          </Col>
                          <Col span={8}>
                            <Form.Item name="transferType" label="转接类型">
                              <Select allowClear options={TRANSFER_TYPE_OPTIONS} placeholder="（默认）" />
                            </Form.Item>
                          </Col>
                          <Col span={8}>
                            <Form.Item
                              name="assignDelaySeconds"
                              label="分配延迟(秒)"
                              tooltip="客户接听后等待指定时长再分配给坐席，0-60。"
                            >
                              <InputNumber min={0} max={60} style={{ width: '100%' }} />
                            </Form.Item>
                          </Col>
                          <Col span={8}>
                            <Form.Item name="maxRedialTimes" label="最大重拨次数" tooltip="1-5；留空=Hermes 默认">
                              <InputNumber min={1} max={5} style={{ width: '100%' }} />
                            </Form.Item>
                          </Col>
                          <Col span={8}>
                            <Form.Item name="redialInterval" label="重拨间隔(分)">
                              <InputNumber min={0} max={60} style={{ width: '100%' }} />
                            </Form.Item>
                          </Col>
                          <Col span={8}>
                            <Form.Item name="bestRingDuration" label="最佳响铃(秒)" tooltip="10-60，默认 40">
                              <InputNumber min={10} max={60} style={{ width: '100%' }} />
                            </Form.Item>
                          </Col>
                          <Col span={8}>
                            <Form.Item name="agentMaxRingDuration" label="坐席最大响铃(秒)" tooltip="1-60；留空=不限">
                              <InputNumber min={1} max={60} style={{ width: '100%' }} />
                            </Form.Item>
                          </Col>
                          <Col span={8}>
                            <Form.Item name="isPriorityTask" label="优先任务" valuePropName="checked">
                              <Switch />
                            </Form.Item>
                          </Col>
                          <Col span={8}>
                            <Form.Item
                              name="isVmHangup"
                              label="语音信箱即挂"
                              valuePropName="checked"
                              tooltip="检测到语音信箱即挂断（Hermes 默认开）"
                            >
                              <Switch />
                            </Form.Item>
                          </Col>
                          <Col span={24}>
                            <Form.Item name="description" label="任务描述">
                              <Input.TextArea rows={1} maxLength={300} />
                            </Form.Item>
                          </Col>
                        </Row>
                      ),
                    }]}
                  />
                </Col>
                <Col span={24}>
                  <Form.Item name="numbers" label="客户号码覆盖（可空；为空则从客户组取号）">
                    <Input.TextArea rows={2} />
                  </Form.Item>
                </Col>
              </Row>
            </Form>
      </Drawer>

      {ccRun ? (() => {
        // 群呼是异步预测式分批拨号，不是「同步用例出 N 个通过/失败」。结论与统计基于真实被叫腿
        // （liveCalls 轮询 sip-inbound 精确匹配本次客户号），而非 run.ok×占位数。
        const cs = liveCalls || callsOf(ccRun)
        const answered = cs.filter((c) => c.status === 'OBSERVED' || c.status === 'CONNECTED').length
        const failed = cs.filter((c) => c.status === 'FAILED').length
        const pending = cs.length - answered - failed
        // 结论：还有等待中→进行中；全部有结果且无失败→成功；有失败→失败。
        const verdict: BannerVerdict = pending > 0 ? 'running' : failed > 0 ? 'fail' : answered > 0 ? 'success' : 'idle'
        const title = pending > 0 ? '群呼进行中' : failed > 0 ? `群呼完成 · ${failed} 通未接通` : answered > 0 ? '群呼完成 · 全部接通' : '群呼已受理'
        return (
          <>
            <ResultBanner
              verdict={verdict}
              title={title}
              sub={restored ? `上次结果 · run ${ccRun.id} · ${new Date(ccRun.startedAt).toLocaleString()}` : `run ${ccRun.id}`}
              metrics={[
                { label: '取号', value: cs.length },
                { label: '已接通', value: answered, color: '#16a34a' },
                { label: '未接通', value: failed, color: failed ? '#dc2626' : undefined },
                { label: '等待中', value: pending, color: pending ? '#d97706' : undefined },
              ]}
              extra={taskCode ? (
                <Space size={4} wrap>
                  <Button size="small" loading={taskBusy} onClick={() => taskAction('pause')}>暂停</Button>
                  <Button size="small" loading={taskBusy} onClick={() => taskAction('resume')}>恢复</Button>
                  <Button size="small" danger loading={taskBusy} onClick={() => taskAction('cancel')}>取消</Button>
                  <Button size="small" type="link" onClick={taskStatus}>状态</Button>
                </Space>
              ) : undefined}
            />
            <Text type="secondary" style={{ fontSize: 12, display: 'block', margin: '10px 0' }}>
              {taskCode ? <>任务 <Text code copyable style={{ fontSize: 12 }}>{taskCode}</Text> · </> : null}
              预测式分批拨号{pending > 0 ? '，每 3s 刷新被叫腿进展' : '（已全部出结果，停止刷新）'}（展开任一通看协商编解码 / RTP 收发·丢包 / DTMF / 挂断码）
            </Text>
            <div style={{ maxHeight: 560, overflowY: 'auto' }}>
              <CallRows calls={cs} />
            </div>
          </>
        )
      })() : (
        <ResultBanner verdict="idle" title="尚未发起群呼" sub="点右上「新建群呼任务」填写表单，结果在此实时呈现" />
      )}

      <ScenarioRecords
        scenario="sip-inbound"
        filterCustomers={(ccRun?.artifacts?.customerNumbers as string[] | undefined) || undefined}
        sinceMs={ccRun ? new Date(ccRun.startedAt).getTime() : undefined}
        caseKinds={['callcenter-task']}
        title="本次群呼·被叫腿通话记录 / 测试历史"
      />
    </div>
  )
}
