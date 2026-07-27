import { useEffect, useMemo, useState } from 'react'
import {
  Alert, Button, Card, Col, Collapse, Drawer, Input, InputNumber, Modal, Row, Select, Space,
  Switch, Table, Tag, Typography, message,
} from 'antd'
import {
  DeleteOutlined, EditOutlined, MinusCircleOutlined, PlusOutlined, ReloadOutlined, SendOutlined,
} from '@ant-design/icons'
import {
  cancelSMSMockCallback, clearSMSMockMessages, createSMSMock, deleteSMSMock, enqueueSMSMockCallback,
  listSMSMockAttempts, listSMSMockMessages, listSMSMockProviders, listSMSMocks, updateSMSMock,
  type HTTPMockCondition, type HTTPMockConditionOperator, type HTTPMockConditionSource, type HTTPMockRule,
  type HTTPMockWeightedCase, type SMSMockCallbackAttempt, type SMSMockCaseSpec, type SMSMockEndpoint,
  type SMSMockEndpointConfig, type SMSMockMessage, type SMSMockProviderInfo,
} from '../api'
import { PageHeader } from '../components/layout/PageHeader'
import { InfoBanner } from '../components/layout/InfoBanner'

const { Text, Paragraph } = Typography
const { TextArea } = Input

const deepClone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T
const pretty = (value: unknown) => JSON.stringify(value, null, 2)
const providerKey = (provider: string, version: string) => `${provider}/${version}`
const terminalReceipt = new Set(['SUCCEEDED', 'FAILED', 'CANCELED', 'NOT_SCHEDULED'])

function materialize(value: unknown, variables: Record<string, string>): unknown {
  if (typeof value === 'string') {
    return Object.entries(variables).reduce((out, [name, replacement]) => out.split(`\${${name}}`).join(replacement), value)
  }
  if (Array.isArray(value)) return value.map((item) => materialize(item, variables))
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, materialize(item, variables)]))
  return value
}

const OPERATOR_OPTIONS: { value: HTTPMockConditionOperator; label: string }[] = [
  { value: 'EQ', label: '等于' }, { value: 'NE', label: '不等于' }, { value: 'IN', label: '属于集合' },
  { value: 'CONTAINS', label: '包含' }, { value: 'PREFIX', label: '前缀是' }, { value: 'EXISTS', label: '存在' },
]
const SOURCE_OPTIONS: { value: HTTPMockConditionSource; label: string }[] = [
  { value: 'jsonBody', label: '规范化消息字段' }, { value: 'query', label: 'Query 参数' },
  { value: 'header', label: 'Header' }, { value: 'rawBody', label: '原始厂商 Body' },
  { value: 'method', label: 'HTTP Method' },
]

function valueText(value: unknown) {
  if (value === undefined || value === null) return ''
  return typeof value === 'string' ? value : JSON.stringify(value)
}

function parseValue(raw: string): unknown {
  const value = raw.trim()
  if (!value) return ''
  if (/^[\[{"-]|^(true|false|null|\d)/.test(value)) {
    try { return JSON.parse(value) } catch { /* 普通字符串 */ }
  }
  return raw
}

function weightOf(items: HTTPMockWeightedCase[] | undefined, name: string) {
  return items?.find((item) => item.case === name)?.weight || 0
}

function setWeight(items: HTTPMockWeightedCase[] | undefined, name: string, weight: number) {
  const out = (items || []).filter((item) => item.case !== name)
  if (weight > 0) out.push({ case: name, weight })
  return out
}

function receiptColor(status: string) {
  if (status === 'SUCCEEDED') return 'green'
  if (status === 'FAILED') return 'red'
  if (status === 'DELIVERING') return 'blue'
  if (status === 'RETRY') return 'orange'
  if (status === 'CANCELED') return 'default'
  return 'gold'
}

function CaseEditor({ name, spec, onRename, onChange, onDelete, submitTemplateVariables, receiptTemplateVariables }: {
  name: string
  spec: SMSMockCaseSpec
  onRename: (next: string) => void
  onChange: (next: SMSMockCaseSpec) => void
  onDelete: () => void
  submitTemplateVariables: string[]
  receiptTemplateVariables: string[]
}) {
  const patchSubmit = (patch: Partial<SMSMockCaseSpec['submit']>) => onChange({ ...spec, submit: { ...spec.submit, ...patch } })
  const patchReceipt = (patch: Partial<SMSMockCaseSpec['receipt']>) => onChange({ ...spec, receipt: { ...spec.receipt, ...patch } })
  const changeSubmitAction = (action: NonNullable<SMSMockCaseSpec['submit']['action']>) => onChange({
    ...spec, submit: { ...spec.submit, action, ...(action === 'TIMEOUT' ? { result: 'ACCEPTED' as const } : {}) },
    receipt: action === 'TIMEOUT' ? { ...spec.receipt, enabled: false } : spec.receipt,
  })
  const changeSubmitResult = (result: NonNullable<SMSMockCaseSpec['submit']['result']>) => onChange({
    ...spec, submit: { ...spec.submit, result },
    receipt: result === 'REJECTED' || result === 'MALFORMED' ? { ...spec.receipt, enabled: false } : spec.receipt,
  })
  const submitAction = spec.submit.action || 'RESPOND'
  const submitResult = spec.submit.result || 'ACCEPTED'
  return <Card
    size="small"
    type="inner"
    title={<Input value={name} onChange={(event) => onRename(event.target.value)} style={{ width: 240 }} aria-label="Case 名称" />}
    extra={<Button danger type="text" icon={<DeleteOutlined />} onClick={onDelete}>删除</Button>}
  >
    <Row gutter={12}>
      <Col span={5}><Text type="secondary">提交动作</Text><Select value={submitAction} onChange={changeSubmitAction} options={[{ value: 'RESPOND', label: '返回响应' }, { value: 'TIMEOUT', label: '保持连接超时' }]} style={{ width: '100%', marginTop: 4 }} /></Col>
      <Col span={5}><Text type="secondary">提交结果</Text><Select disabled={submitAction === 'TIMEOUT'} value={submitResult} onChange={changeSubmitResult} options={[
        { value: 'ACCEPTED', label: 'Accepted' }, { value: 'REJECTED', label: 'Rejected' },
        { value: 'MALFORMED', label: '畸形响应' }, { value: 'CUSTOM', label: '自定义模板' },
      ]} style={{ width: '100%', marginTop: 4 }} /></Col>
      <Col span={4}><Text type="secondary">HTTP 状态</Text><InputNumber min={200} max={599} value={spec.submit.httpStatus || 200} onChange={(value) => patchSubmit({ httpStatus: value || 200 })} style={{ width: '100%', marginTop: 4 }} /></Col>
      <Col span={4}><Text type="secondary">响应延迟 ms</Text><InputNumber min={0} max={120000} value={spec.submit.delayMs || 0} onChange={(value) => patchSubmit({ delayMs: value || 0 })} style={{ width: '100%', marginTop: 4 }} /></Col>
      <Col span={6}><Text type="secondary">{submitAction === 'TIMEOUT' ? '保持连接 ms' : '计费 Parts'}</Text>{submitAction === 'TIMEOUT'
        ? <InputNumber min={0} max={120000} value={spec.submit.timeoutMs || 31000} onChange={(value) => patchSubmit({ timeoutMs: value || 0 })} style={{ width: '100%', marginTop: 4 }} />
        : <InputNumber min={1} max={255} value={spec.submit.parts || 1} onChange={(value) => patchSubmit({ parts: value || 1 })} style={{ width: '100%', marginTop: 4 }} />}</Col>
    </Row>
    {submitResult === 'REJECTED' && <Row gutter={12} style={{ marginTop: 12 }}>
      <Col span={6}><Text type="secondary">厂商错误码</Text><InputNumber value={spec.submit.errorCode || 10} onChange={(value) => patchSubmit({ errorCode: value || 0 })} style={{ width: '100%', marginTop: 4 }} /></Col>
      <Col span={18}><Text type="secondary">Details</Text><Input value={spec.submit.details || ''} onChange={(event) => patchSubmit({ details: event.target.value })} style={{ marginTop: 4 }} /></Col>
    </Row>}
    <Collapse ghost style={{ marginTop: 8 }} items={[{
      key: 'submit-template', label: '高级：覆盖厂商同步响应模板',
      children: <><Paragraph type="secondary">用于当前厂商协议的小版本字段调整；支持 {submitTemplateVariables.map((v) => `\${${v}}`).join('、')}。模板渲染后必须满足当前适配器契约。</Paragraph><TextArea rows={4} value={spec.submit.rawBodyTemplate || ''} onChange={(event) => patchSubmit({ rawBodyTemplate: event.target.value })} /></>,
    }]} />
    <Card size="small" style={{ marginTop: 8, background: '#fafcff' }} title={<Space><span>异步 DLR</span><Switch checked={spec.receipt.enabled} disabled={submitAction === 'TIMEOUT' || submitResult === 'REJECTED' || submitResult === 'MALFORMED'} onChange={(enabled) => patchReceipt({ enabled })} /></Space>}>
      {spec.receipt.enabled ? <>
        <Row gutter={12}>
          <Col span={4}><Text type="secondary">延迟 ms</Text><InputNumber min={0} max={604800000} value={spec.receipt.delayMs || 0} onChange={(value) => patchReceipt({ delayMs: value || 0 })} style={{ width: '100%', marginTop: 4 }} /></Col>
          <Col span={4}><Text type="secondary">状态 Code</Text><Input value={spec.receipt.statusCode || '2'} onChange={(event) => patchReceipt({ statusCode: event.target.value })} style={{ marginTop: 4 }} /></Col>
          <Col span={4}><Text type="secondary">错误 Code</Text><Input value={spec.receipt.errorCode ?? ''} placeholder="按厂商协议填写" onChange={(event) => patchReceipt({ errorCode: event.target.value })} style={{ marginTop: 4 }} /></Col>
          <Col span={5}><Text type="secondary">Operator</Text><Input value={spec.receipt.operator || 'MOCK'} onChange={(event) => patchReceipt({ operator: event.target.value })} style={{ marginTop: 4 }} /></Col>
          <Col span={3}><Text type="secondary">重复次数</Text><InputNumber min={1} max={20} value={spec.receipt.repeat || 1} onChange={(value) => patchReceipt({ repeat: value || 1 })} style={{ width: '100%', marginTop: 4 }} /></Col>
          <Col span={4}><Text type="secondary">重复间隔 ms</Text><InputNumber min={0} max={120000} value={spec.receipt.repeatIntervalMs || 0} onChange={(value) => patchReceipt({ repeatIntervalMs: value || 0 })} style={{ width: '100%', marginTop: 4 }} /></Col>
        </Row>
        <div style={{ marginTop: 10 }}><Text type="secondary">错误描述</Text><Input value={spec.receipt.errorDescription || ''} onChange={(event) => patchReceipt({ errorDescription: event.target.value })} style={{ marginTop: 4 }} /></div>
        <Collapse ghost items={[{
          key: 'receipt-template', label: '高级：覆盖 DLR 模板',
          children: <><Paragraph type="secondary">支持 {receiptTemplateVariables.map((v) => `\${${v}}`).join('、')}；必须保留 <Text code>{'${reference}'}</Text>，否则无法关联 Arke 短信记录。</Paragraph><TextArea rows={4} value={spec.receipt.rawBodyTemplate || ''} onChange={(event) => patchReceipt({ rawBodyTemplate: event.target.value })} /></>,
        }]} />
      </> : <Text type="secondary">提交响应完成后不发送 DLR，用于验证 Arke 长时间停留在“已发送”。</Text>}
    </Card>
  </Card>
}

function WeightedCases({ cases, value, onChange }: { cases: string[]; value?: HTTPMockWeightedCase[]; onChange: (next: HTTPMockWeightedCase[]) => void }) {
  return <Row gutter={[8, 8]}>{cases.map((name) => <Col span={8} key={name}>
    <Space.Compact style={{ width: '100%' }}><Input value={name} readOnly /><InputNumber aria-label={`${name} 权重`} min={0} max={1000000} value={weightOf(value, name)} onChange={(weight) => onChange(setWeight(value, name, weight || 0))} style={{ width: 90 }} /></Space.Compact>
  </Col>)}</Row>
}

function RuleEditor({ rule, index, caseNames, provider, onChange, onDelete }: {
  rule: HTTPMockRule
  index: number
  caseNames: string[]
  provider?: SMSMockProviderInfo
  onChange: (next: HTTPMockRule) => void
  onDelete: () => void
}) {
  const weighted = !!rule.weightedCases?.length
  const updateCondition = (conditionIndex: number, patch: Partial<HTTPMockCondition>) => {
    const conditions = [...rule.conditions]
    conditions[conditionIndex] = { ...conditions[conditionIndex], ...patch }
    onChange({ ...rule, conditions })
  }
  return <Card size="small" type="inner" title={`规则 ${index + 1}`} extra={<Button danger type="text" icon={<DeleteOutlined />} onClick={onDelete}>删除</Button>}>
    <Row gutter={12}>
      <Col span={10}><Text type="secondary">名称</Text><Input value={rule.name} onChange={(event) => onChange({ ...rule, name: event.target.value })} style={{ marginTop: 4 }} /></Col>
      <Col span={4}><Text type="secondary">优先级</Text><InputNumber value={rule.priority || 0} onChange={(value) => onChange({ ...rule, priority: value || 0 })} style={{ width: '100%', marginTop: 4 }} /></Col>
      <Col span={5}><Text type="secondary">命中结果</Text><Select value={weighted ? 'WEIGHTED' : 'FIXED'} onChange={(mode) => onChange(mode === 'WEIGHTED' ? { ...rule, case: undefined, weightedCases: [{ case: caseNames[0] || '', weight: 1 }] } : { ...rule, case: caseNames[0] || '', weightedCases: undefined })} options={[{ value: 'FIXED', label: '固定 Case' }, { value: 'WEIGHTED', label: '概率 Cases' }]} style={{ width: '100%', marginTop: 4 }} /></Col>
      <Col span={5}>{!weighted && <><Text type="secondary">Case</Text><Select value={rule.case} onChange={(value) => onChange({ ...rule, case: value })} options={caseNames.map((value) => ({ value, label: value }))} style={{ width: '100%', marginTop: 4 }} /></>}</Col>
    </Row>
    <Text strong style={{ display: 'block', marginTop: 12 }}>以下条件全部满足</Text>
    <Space direction="vertical" style={{ width: '100%', marginTop: 8 }}>
      {rule.conditions.map((condition, conditionIndex) => <Row gutter={8} key={conditionIndex} align="middle">
        <Col span={5}><Select value={condition.source} onChange={(source) => updateCondition(conditionIndex, { source, field: source === 'jsonBody' ? provider?.matchFields[0]?.path : '' })} options={SOURCE_OPTIONS} style={{ width: '100%' }} /></Col>
        <Col span={6}>{condition.source === 'jsonBody'
          ? <Select showSearch value={condition.field} onChange={(field) => updateCondition(conditionIndex, { field })} options={provider?.matchFields.map((field) => ({ value: field.path, label: field.label }))} style={{ width: '100%' }} />
          : <Input disabled={condition.source === 'method' || condition.source === 'rawBody'} value={condition.field || ''} placeholder="字段名" onChange={(event) => updateCondition(conditionIndex, { field: event.target.value })} />}</Col>
        <Col span={5}><Select value={condition.operator} onChange={(operator) => updateCondition(conditionIndex, { operator })} options={OPERATOR_OPTIONS} style={{ width: '100%' }} /></Col>
        <Col span={7}><Input disabled={condition.operator === 'EXISTS'} value={valueText(condition.value)} placeholder={'值或 JSON 数组 ["a","b"]'} onChange={(event) => updateCondition(conditionIndex, { value: parseValue(event.target.value) })} /></Col>
        <Col span={1}><Button danger type="text" icon={<MinusCircleOutlined />} onClick={() => onChange({ ...rule, conditions: rule.conditions.filter((_, i) => i !== conditionIndex) })} /></Col>
      </Row>)}
      <Button type="dashed" size="small" icon={<PlusOutlined />} onClick={() => onChange({ ...rule, conditions: [...rule.conditions, { source: 'jsonBody', field: provider?.matchFields[0]?.path || 'recipient', operator: 'EQ', value: '' }] })}>增加 AND 条件</Button>
    </Space>
    {weighted && <div style={{ marginTop: 12 }}><Text strong>命中后的概率池（0 表示不参与）</Text><div style={{ marginTop: 8 }}><WeightedCases cases={caseNames} value={rule.weightedCases} onChange={(weightedCases) => onChange(weightedCases.length ? { ...rule, weightedCases } : { ...rule, weightedCases: undefined, case: caseNames[0] || '' })} /></div></div>}
  </Card>
}

export default function SmsMockPage() {
  const [endpoints, setEndpoints] = useState<SMSMockEndpoint[]>([])
  const [providers, setProviders] = useState<SMSMockProviderInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [draft, setDraft] = useState<SMSMockEndpoint | null>(null)
  const [saving, setSaving] = useState(false)
  const [messageEndpoint, setMessageEndpoint] = useState<SMSMockEndpoint | null>(null)
  const [messages, setMessages] = useState<SMSMockMessage[]>([])
  const [messageLoading, setMessageLoading] = useState(false)
  const [keyword, setKeyword] = useState('')
  const [attemptMessage, setAttemptMessage] = useState<SMSMockMessage | null>(null)
  const [attempts, setAttempts] = useState<SMSMockCallbackAttempt[]>([])

  const load = async () => {
    setLoading(true)
    try {
      const [endpointResult, providerResult] = await Promise.all([listSMSMocks(), listSMSMockProviders()])
      setEndpoints(endpointResult.endpoints)
      setProviders(providerResult.providers)
    } catch (error) { message.error(String(error)) } finally { setLoading(false) }
  }
  useEffect(() => { void load() }, [])

  const currentProvider = useMemo(() => providers.find((item) => draft && providerKey(item.provider, item.protocolVersion) === providerKey(draft.provider, draft.protocolVersion)), [providers, draft])
  const caseNames = useMemo(() => draft ? Object.keys(draft.config.cases).sort() : [], [draft])

  const newEndpoint = () => {
    const provider = providers[0]
    if (!provider) { message.warning('后端未注册短信协议适配器'); return }
    setDraft({ name: `Arke ${provider.provider} Mock`, enabled: true, provider: provider.provider, protocolVersion: provider.protocolVersion, config: deepClone(provider.defaultConfig), remark: '' })
  }
  const editEndpoint = (endpoint: SMSMockEndpoint) => setDraft(deepClone(endpoint))
  const patchConfig = (patch: Partial<SMSMockEndpointConfig>) => draft && setDraft({ ...draft, config: { ...draft.config, ...patch } })

  const renameCase = (oldName: string, nextName: string) => {
    if (!draft || oldName === nextName) return
    if (nextName !== oldName && draft.config.cases[nextName]) { message.warning(`Case ${nextName} 已存在`); return }
    const cases = { ...draft.config.cases }
    const value = cases[oldName]
    delete cases[oldName]
    cases[nextName] = value
    const fixWeights = (items?: HTTPMockWeightedCase[]) => items?.map((item) => item.case === oldName ? { ...item, case: nextName } : item)
    const rules = (draft.config.rules || []).map((rule) => ({ ...rule, case: rule.case === oldName ? nextName : rule.case, weightedCases: fixWeights(rule.weightedCases) }))
    patchConfig({ cases, defaultCase: draft.config.defaultCase === oldName ? nextName : draft.config.defaultCase, defaultWeightedCases: fixWeights(draft.config.defaultWeightedCases), rules })
  }
  const updateCase = (name: string, spec: SMSMockCaseSpec) => draft && patchConfig({ cases: { ...draft.config.cases, [name]: spec } })
  const deleteCase = (name: string) => {
    if (!draft || caseNames.length <= 1) { message.warning('至少保留一个 Case'); return }
    const cases = { ...draft.config.cases }; delete cases[name]
    const nextDefault = draft.config.defaultCase === name ? Object.keys(cases)[0] : draft.config.defaultCase
    const defaultWeightedCases = draft.config.defaultWeightedCases?.filter((item) => item.case !== name)
    const rules = (draft.config.rules || []).filter((rule) => rule.case !== name).map((rule) => {
      const weightedCases = rule.weightedCases?.filter((item) => item.case !== name)
      return weightedCases?.length ? { ...rule, weightedCases } : { ...rule, weightedCases: undefined, case: nextDefault }
    })
    patchConfig({ cases, defaultCase: nextDefault, defaultWeightedCases, rules })
  }
  const addCase = () => {
    if (!draft) return
    let index = 1; while (draft.config.cases[`case-${index}`]) index++
    const base = currentProvider?.defaultConfig.cases['accepted-delivered'] || Object.values(draft.config.cases)[0]
    updateCase(`case-${index}`, deepClone(base))
  }
  const updateRule = (index: number, rule: HTTPMockRule) => {
    if (!draft) return
    const rules = [...(draft.config.rules || [])]; rules[index] = rule; patchConfig({ rules })
  }

  const save = async () => {
    if (!draft) return
    setSaving(true)
    try {
      if (!draft.name.trim()) throw new Error('名称必填')
      if (Object.values(draft.config.cases).some((spec) => spec.receipt.enabled) && !draft.config.callbackUrl.trim()) throw new Error('存在自动 DLR 时 Arke 回调 URL 必填')
      if (draft.id) await updateSMSMock(draft.id, draft)
      else await createSMSMock(draft)
      message.success('SMS Mock 已保存')
      setDraft(null)
      await load()
    } catch (error) { message.error(String(error)) } finally { setSaving(false) }
  }

  const removeEndpoint = (endpoint: SMSMockEndpoint) => Modal.confirm({
    title: `删除 ${endpoint.name}？`, content: 'Endpoint、短信消息和 DLR attempt 将一起删除。', okButtonProps: { danger: true },
    onOk: async () => { await deleteSMSMock(endpoint.id!); message.success('已删除'); await load() },
  })

  const showUsage = (endpoint: SMSMockEndpoint) => Modal.info({
    title: `Arke 厂商配置 · ${endpoint.name}`, width: 720,
    content: (() => {
      const capability = providers.find((item) => providerKey(item.provider, item.protocolVersion) === providerKey(endpoint.provider, endpoint.protocolVersion))
      const invokeUrl = endpoint.invokeUrl || endpoint.invokePath || ''
      const vendorName = capability?.hermesVendorName || endpoint.provider
      const config = materialize(capability?.hermesConfig || { url: '${invokeUrl}' }, { invokeUrl })
      return <><Paragraph>将 Hermes-Arke 的 <Text code>t_sms_vendor.vendor_name</Text> 配为 <Text code>{vendorName}</Text>，厂商配置 JSON：</Paragraph><pre style={{ background: '#0b1021', color: '#d6e0ff', padding: 12, borderRadius: 6, whiteSpace: 'pre-wrap' }}>{pretty(config)}</pre><Paragraph type="secondary">Callback URL 由本 Endpoint 主动调用，可参考 <Text code>{capability?.hermesCallbackHint || '当前厂商在 Hermes 中的公开回调地址'}</Text>。</Paragraph></>
    })(),
  })

  const loadMessages = async (endpoint = messageEndpoint, searchKeyword = keyword) => {
	if (!endpoint?.id) return
	setMessageLoading(true)
	try { setMessages((await listSMSMockMessages(endpoint.id, { keyword: searchKeyword, limit: 300 })).messages) }
	catch (error) { message.error(String(error)) } finally { setMessageLoading(false) }
  }
  const openMessages = async (endpoint: SMSMockEndpoint) => { setMessageEndpoint(endpoint); setKeyword(''); setMessages([]); await loadMessages(endpoint, '') }
  const clearMessages = () => messageEndpoint?.id && Modal.confirm({
    title: '清空当前 Endpoint 的短信记录？', content: '会同时删除 DLR attempt，不影响 Endpoint 配置。', okButtonProps: { danger: true },
    onOk: async () => { const result = await clearSMSMockMessages(messageEndpoint.id!); message.success(`已删除 ${result.deleted} 条`); await loadMessages() },
  })
  const triggerCallback = async (row: SMSMockMessage) => {
    try { await enqueueSMSMockCallback(row.id); message.success(terminalReceipt.has(row.receiptStatus) ? '已排队重发 DLR' : '已提前到立即投递'); await loadMessages() }
    catch (error) { message.error(String(error)) }
  }
  const cancelCallback = async (row: SMSMockMessage) => {
    try { await cancelSMSMockCallback(row.id); message.success('已取消待投 DLR'); await loadMessages() }
    catch (error) { message.error(String(error)) }
  }
  const showAttempts = async (row: SMSMockMessage) => {
    setAttemptMessage(row)
    setAttempts([])
    try { setAttempts((await listSMSMockAttempts(row.id)).attempts) } catch (error) { message.error(String(error)) }
  }

  return <div className="page-container">
    <PageHeader title="短信厂商 Mock" status={{ tone: 'info', text: '提交 + DLR' }} />
    <InfoBanner title="同一服务，独立状态机；协议可插拔">
      核心统一负责规则、概率、持久化延迟、重复、重试和手动投递；版本化 Adapter 只处理线协议与 Hermes 接入提示。协议升级可并存新版本，新增厂商无需复制调度器和页面。
    </InfoBanner>
    <Card title="Endpoint" extra={<Space><Button icon={<ReloadOutlined />} onClick={() => load()}>刷新</Button><Button type="primary" icon={<PlusOutlined />} onClick={newEndpoint}>新建短信 Mock</Button></Space>}>
      <Table rowKey="id" loading={loading} dataSource={endpoints} pagination={false} columns={[
        { title: '状态', width: 70, render: (_: unknown, row: SMSMockEndpoint) => <Tag color={row.enabled ? 'green' : 'default'}>{row.enabled ? '启用' : '禁用'}</Tag> },
        { title: '名称', dataIndex: 'name', width: 180, render: (value: string, row: SMSMockEndpoint) => <><div>{value}</div><Text type="secondary">{row.remark}</Text></> },
        { title: '协议', width: 150, render: (_: unknown, row: SMSMockEndpoint) => <Tag color="blue">{row.provider}/{row.protocolVersion}</Tag> },
        { title: '默认结果', width: 190, render: (_: unknown, row: SMSMockEndpoint) => row.config.defaultWeightedCases?.length ? <Tag color="purple">概率 {row.config.defaultWeightedCases.map((item) => `${item.case}:${item.weight}`).join(' / ')}</Tag> : <Tag>{row.config.defaultCase}</Tag> },
        { title: 'Arke DLR', dataIndex: ['config', 'callbackUrl'], ellipsis: true, render: (value: string) => <Text code copyable ellipsis>{value || '未配置'}</Text> },
        { title: '厂商提交 URL', render: (_: unknown, row: SMSMockEndpoint) => <Text code copyable ellipsis>{row.invokeUrl || row.invokePath}</Text> },
        { title: '操作', width: 300, render: (_: unknown, row: SMSMockEndpoint) => <Space size={4}><Button size="small" onClick={() => showUsage(row)}>接入</Button><Button size="small" onClick={() => openMessages(row)}>消息</Button><Button size="small" icon={<EditOutlined />} onClick={() => editEndpoint(row)}>编辑</Button><Button size="small" danger icon={<DeleteOutlined />} onClick={() => removeEndpoint(row)}>删除</Button></Space> },
      ]} />
    </Card>

    <Drawer title={draft?.id ? `编辑短信 Mock · ${draft.name}` : '新建短信 Mock'} width={1080} open={!!draft} onClose={() => setDraft(null)} footer={<div style={{ textAlign: 'right' }}><Space><Button onClick={() => setDraft(null)}>取消</Button><Button type="primary" loading={saving} onClick={save}>保存</Button></Space></div>}>
      {draft && <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Card size="small" title="1. Endpoint 与协议">
          <Row gutter={12}>
            <Col span={10}><Text type="secondary">名称</Text><Input value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} style={{ marginTop: 4 }} /></Col>
            <Col span={8}><Text type="secondary">厂商协议 / 版本</Text><Select disabled={!!draft.id} value={providerKey(draft.provider, draft.protocolVersion)} onChange={(key) => { const provider = providers.find((item) => providerKey(item.provider, item.protocolVersion) === key); if (provider) setDraft({ ...draft, provider: provider.provider, protocolVersion: provider.protocolVersion, config: { ...deepClone(provider.defaultConfig), callbackUrl: draft.config.callbackUrl } }) }} options={providers.map((provider) => ({ value: providerKey(provider.provider, provider.protocolVersion), label: provider.displayName }))} style={{ width: '100%', marginTop: 4 }} />{draft.id && <Text type="secondary">协议升级请新建 Endpoint，保留旧 token 与用例。</Text>}</Col>
            <Col span={3}><Text type="secondary">启用</Text><div style={{ marginTop: 8 }}><Switch checked={draft.enabled} onChange={(enabled) => setDraft({ ...draft, enabled })} /></div></Col>
            <Col span={3}><Text type="secondary">单次 Case 覆盖</Text><div style={{ marginTop: 8 }}><Switch checked={draft.config.allowCaseOverride} onChange={(allowCaseOverride) => patchConfig({ allowCaseOverride })} /></div></Col>
            <Col span={24} style={{ marginTop: 10 }}><Text type="secondary">备注</Text><Input value={draft.remark || ''} onChange={(event) => setDraft({ ...draft, remark: event.target.value })} style={{ marginTop: 4 }} /></Col>
          </Row>
        </Card>
        <Card size="small" title="2. Arke DLR 目标与失败重试">
          <Alert type="warning" showIcon message={currentProvider?.provider === 'CM' ? '这是厂商 DLR 回 Arke 的地址，不是 Arke 最终通知业务方的 URL；CM 成功回执的 errorCode 必须留空' : '这是厂商 DLR 回 Arke 的地址，不是 Arke 最终通知业务方的 URL'} style={{ marginBottom: 10 }} />
          <Row gutter={12}>
            <Col span={12}><Text type="secondary">Callback URL</Text><Input placeholder={currentProvider?.hermesCallbackHint || 'http://hermes-arke:8080/public/sms/callback?provider=...'} value={draft.config.callbackUrl} onChange={(event) => patchConfig({ callbackUrl: event.target.value })} style={{ marginTop: 4 }} /></Col>
            <Col span={4}><Text type="secondary">超时 ms</Text><InputNumber min={100} max={120000} value={draft.config.callbackTimeoutMs || 5000} onChange={(value) => patchConfig({ callbackTimeoutMs: value || 5000 })} style={{ width: '100%', marginTop: 4 }} /></Col>
            <Col span={4}><Text type="secondary">最大尝试</Text><InputNumber min={1} max={10} value={draft.config.callbackMaxAttempts || 3} onChange={(value) => patchConfig({ callbackMaxAttempts: value || 3 })} style={{ width: '100%', marginTop: 4 }} /></Col>
            <Col span={4}><Text type="secondary">退避基数 ms</Text><InputNumber min={100} max={120000} value={draft.config.callbackRetryBackoffMs || 1000} onChange={(value) => patchConfig({ callbackRetryBackoffMs: value || 1000 })} style={{ width: '100%', marginTop: 4 }} /></Col>
          </Row>
        </Card>
        <Card size="small" title="3. 默认结果与概率池">
          <Row gutter={12}><Col span={8}><Text type="secondary">未命中规则时固定 Case</Text><Select value={draft.config.defaultCase} onChange={(defaultCase) => patchConfig({ defaultCase })} options={caseNames.map((value) => ({ value, label: value }))} style={{ width: '100%', marginTop: 4 }} /></Col><Col span={16}><Text type="secondary">概率权重（任一项大于 0 后覆盖固定默认；0 表示不参与）</Text><div style={{ marginTop: 4 }}><WeightedCases cases={caseNames} value={draft.config.defaultWeightedCases} onChange={(defaultWeightedCases) => patchConfig({ defaultWeightedCases })} /></div></Col></Row>
        </Card>
        <Card size="small" title="4. 语义化 Cases" extra={<Button type="dashed" icon={<PlusOutlined />} onClick={addCase}>增加 Case</Button>}>
          <Space direction="vertical" style={{ width: '100%' }} size={12}>{caseNames.map((name) => <CaseEditor key={name} name={name} spec={draft.config.cases[name]} onRename={(next) => renameCase(name, next)} onChange={(next) => updateCase(name, next)} onDelete={() => deleteCase(name)} submitTemplateVariables={currentProvider?.submitTemplateVariables || []} receiptTemplateVariables={currentProvider?.receiptTemplateVariables || []} />)}</Space>
        </Card>
        <Card size="small" title="5. 消息匹配规则" extra={<Button type="dashed" icon={<PlusOutlined />} onClick={() => patchConfig({ rules: [...(draft.config.rules || []), { name: `rule-${(draft.config.rules || []).length + 1}`, priority: 10, conditions: [{ source: 'jsonBody', field: currentProvider?.matchFields[0]?.path || 'recipient', operator: 'EQ', value: '' }], case: draft.config.defaultCase }] })}>增加规则</Button>}>
          <Paragraph type="secondary">同一规则内为 AND，priority 越大越先匹配。固定 Case 和概率池二选一；显式 <Text code>?__mock_case=...</Text> 最后覆盖规则。</Paragraph>
          <Space direction="vertical" style={{ width: '100%' }} size={12}>{(draft.config.rules || []).map((rule, index) => <RuleEditor key={index} rule={rule} index={index} caseNames={caseNames} provider={currentProvider} onChange={(next) => updateRule(index, next)} onDelete={() => patchConfig({ rules: (draft.config.rules || []).filter((_, i) => i !== index) })} />)}</Space>
        </Card>
      </Space>}
    </Drawer>

    <Drawer title={messageEndpoint ? `短信消息 · ${messageEndpoint.name}` : '短信消息'} width={1180} open={!!messageEndpoint} onClose={() => setMessageEndpoint(null)}>
      <InfoBanner title="DLR 是持久化任务">
        Accepted 前已落库；重启会恢复未确认任务。自动重复的 deliveryNo 与网络重试 attemptNo 分开记录，终态记录随 OBSERVE_TTL_DAYS 清理，非终态不会被 TTL 误删。
      </InfoBanner>
      <Space wrap style={{ marginBottom: 12 }}><Input.Search allowClear value={keyword} onChange={(event) => setKeyword(event.target.value)} onSearch={() => loadMessages()} placeholder="reference / 号码 / 内容 / 错误" style={{ width: 280 }} /><Button icon={<ReloadOutlined />} onClick={() => loadMessages()}>刷新</Button><Button danger onClick={clearMessages}>清空记录</Button><Text type="secondary">最近 {messages.length} 条</Text></Space>
      <Table rowKey="id" size="small" loading={messageLoading} dataSource={messages} pagination={{ pageSize: 20 }} expandable={{ expandedRowRender: (row) => <pre style={{ fontSize: 11, background: '#0b1021', color: '#d6e0ff', padding: 12, borderRadius: 6, whiteSpace: 'pre-wrap' }}>{pretty({ request: { remote: row.remote, reference: row.reference, recipient: row.recipient, sender: row.sender, content: row.content, sanitizedBody: row.requestBody }, decision: { rule: row.matchedRule, case: row.selectedCase, mode: row.selectionMode, weight: row.totalWeight ? `${row.selectedWeight}/${row.totalWeight}` : undefined }, submit: { action: row.submitAction, state: row.submitState, httpStatus: row.submitHttpStatus, body: row.submitResponseBody }, receipt: { enabled: row.receiptEnabled, status: row.receiptStatus, dueAt: row.receiptDueAt, sent: `${row.receiptSentCount}/${row.receiptTargetCount}`, callback: { url: row.callbackUrl, method: row.callbackMethod, headers: row.callbackHeadersJson, body: row.callbackBody }, attempts: row.callbackAttempts, lastHttpStatus: row.callbackLastHttpStatus, lastResponse: row.callbackLastResponseBody, lastError: row.callbackLastError } })}</pre> }} columns={[
        { title: '收到时间', dataIndex: 'receivedAt', width: 165, render: (value: string) => new Date(value).toLocaleString() },
        { title: 'Reference', dataIndex: 'reference', width: 210, ellipsis: true, render: (value: string) => <Text code copyable ellipsis>{value}</Text> },
        { title: '号码 / Sender', width: 165, render: (_: unknown, row: SMSMockMessage) => <><div>{row.recipient}</div><Text type="secondary">{row.sender}</Text></> },
        { title: 'Case', width: 170, render: (_: unknown, row: SMSMockMessage) => <><Tag color={row.selectionMode.includes('WEIGHTED') ? 'purple' : 'blue'}>{row.selectedCase}</Tag><div><Text type="secondary">{row.matchedRule || row.selectionMode}</Text></div></> },
        { title: '提交', width: 120, render: (_: unknown, row: SMSMockMessage) => <><Tag color={row.submitState === 'RESPONDED' ? 'green' : row.submitState.includes('CANCEL') ? 'red' : 'orange'}>{row.submitState}</Tag><div>{row.submitAction} · {row.submitHttpStatus}</div></> },
        { title: 'DLR', width: 160, render: (_: unknown, row: SMSMockMessage) => <><Tag color={receiptColor(row.receiptStatus)}>{row.receiptStatus}</Tag><div>{row.receiptSentCount}/{row.receiptTargetCount} · 尝试 {row.callbackAttempts}</div>{row.callbackLastError && <Text type="danger" ellipsis>{row.callbackLastError}</Text>}</> },
        { title: '操作', width: 230, render: (_: unknown, row: SMSMockMessage) => <Space size={4}><Button size="small" onClick={() => showAttempts(row)}>Attempts</Button>{row.receiptEnabled && row.submitState === 'RESPONDED' && row.receiptStatus !== 'DELIVERING' && <Button size="small" type="primary" ghost icon={<SendOutlined />} onClick={() => triggerCallback(row)}>{terminalReceipt.has(row.receiptStatus) ? '重发' : '立即'}</Button>}{['WAITING_SUBMIT', 'PENDING', 'RETRY'].includes(row.receiptStatus) && <Button size="small" danger onClick={() => cancelCallback(row)}>取消</Button>}</Space> },
      ]} />
    </Drawer>

    <Modal title={attemptMessage ? `DLR Attempts · ${attemptMessage.reference}` : 'DLR Attempts'} open={!!attemptMessage} onCancel={() => setAttemptMessage(null)} footer={null} width={900}>
      <Table rowKey="id" size="small" dataSource={attempts} pagination={false} expandable={{ expandedRowRender: (row) => <pre style={{ whiteSpace: 'pre-wrap' }}>{pretty({ url: row.url, method: row.method, requestHeaders: row.requestHeadersJson, requestBody: row.requestBody, responseBody: row.responseBody, error: row.error })}</pre> }} columns={[
        { title: '时间', dataIndex: 'startedAt', width: 170, render: (value: string) => new Date(value).toLocaleString() },
        { title: 'Delivery', dataIndex: 'deliveryNo', width: 80 }, { title: 'Attempt', dataIndex: 'attemptNo', width: 80 },
        { title: '结果', width: 110, render: (_: unknown, row: SMSMockCallbackAttempt) => <Tag color={row.success ? 'green' : 'red'}>{row.success ? `HTTP ${row.httpStatus}` : row.httpStatus ? `HTTP ${row.httpStatus}` : '网络错误'}</Tag> },
        { title: '错误', dataIndex: 'error', ellipsis: true },
      ]} />
    </Modal>
  </div>
}
