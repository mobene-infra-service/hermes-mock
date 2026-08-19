import { useEffect, useState } from 'react'
import {
  Alert, Button, Card, Col, Collapse, Descriptions, Drawer, Form, Input, InputNumber, Modal,
  Row, Select, Space, Switch, Table, Tag, Typography, message,
} from 'antd'
import {
  ArrowDownOutlined, ArrowUpOutlined, CopyOutlined, DeleteOutlined, EditOutlined, MinusCircleOutlined,
  PlusOutlined, ReloadOutlined,
} from '@ant-design/icons'
import {
  clearHTTPMockRequests, createHTTPMock, deleteHTTPMock, listHTTPMockRequests, listHTTPMocks, updateHTTPMock,
  type HTTPMockCondition, type HTTPMockConditionOperator, type HTTPMockConditionSource,
  type HTTPMockEndpoint, type HTTPMockEndpointConfig, type HTTPMockRequestRecord, type HTTPMockResponseSpec,
  type HTTPMockRule, type HTTPMockWeightedCase,
} from '../api'
import { PageHeader } from '../components/layout/PageHeader'
import { InfoBanner } from '../components/layout/InfoBanner'

const { Text, Paragraph } = Typography
const { TextArea } = Input

type ResultMode = 'FIXED' | 'WEIGHTED'

interface ResponseEditor {
  action: 'RESPOND' | 'TIMEOUT'
  status: number
  contentType: string
  body: string
  delayMs: number
  timeoutMs: number
  headersJson: string
}

interface CaseEditor extends ResponseEditor {
  name: string
}

interface ConditionEditor {
  source: HTTPMockConditionSource
  field?: string
  operator: HTTPMockConditionOperator
  valueText?: string
}

interface RuleEditor {
  name: string
  priority: number
  conditions: ConditionEditor[]
  resultMode: ResultMode
  case?: string
  weightedCases?: HTTPMockWeightedCase[]
}

interface EditorValues {
  name: string
  enabled: boolean
  allowedMethods: string[]
  overridePolicy: 'NONE' | 'CASE_ONLY' | 'FULL'
  defaultResponse: ResponseEditor
  defaultResultMode: ResultMode
  defaultWeightedCases: HTTPMockWeightedCase[]
  sequenceCases: string[]
  cases: CaseEditor[]
  rules: RuleEditor[]
  remark?: string
}

const DEFAULT_CASES: Record<string, HTTPMockResponseSpec> = {
  allow: { action: 'RESPOND', status: 200, contentType: 'text/plain; charset=utf-8', body: 'true' },
  deny: { action: 'RESPOND', status: 200, contentType: 'text/plain; charset=utf-8', body: 'false' },
  timeout: { action: 'TIMEOUT', status: 200, contentType: 'text/plain; charset=utf-8', timeoutMs: 5000 },
  error: { action: 'RESPOND', status: 500, contentType: 'text/plain; charset=utf-8', body: 'mock error' },
}

const SOURCE_LABELS: Record<HTTPMockConditionSource, string> = {
  method: '请求方法',
  query: 'Query 参数',
  header: '请求 Header',
  jsonBody: 'JSON Body 字段',
  rawBody: '原始 Body',
}

const OPERATOR_LABELS: Record<HTTPMockConditionOperator, string> = {
  EQ: '等于',
  NE: '不等于',
  IN: '属于集合',
  CONTAINS: '包含',
  PREFIX: '前缀是',
  EXISTS: '存在',
}

const SELECTION_LABELS: Record<string, string> = {
  DEFAULT: '默认响应',
  RULE_FIXED: '规则固定',
  DEFAULT_WEIGHTED: '默认概率',
  RULE_WEIGHTED: '规则概率',
  EXPLICIT_CASE: '调用方指定',
  SEQUENCE: '顺序响应',
}

const SOURCE_OPTIONS = Object.entries(SOURCE_LABELS).map(([value, label]) => ({ value, label }))
const OPERATOR_OPTIONS = Object.entries(OPERATOR_LABELS).map(([value, label]) => ({ value, label }))
const METHOD_OPTIONS = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'].map((value) => ({ value, label: value }))

const pretty = (value: unknown) => JSON.stringify(value, null, 2)

function parseJSON<T>(raw: string, fallback: T, label: string): T {
  if (!raw.trim()) return fallback
  try {
    return JSON.parse(raw) as T
  } catch (error) {
    throw new Error(label + ' JSON 非法：' + String(error))
  }
}

function safeJSON(raw: string) {
  try {
    return JSON.parse(raw)
  } catch {
    return raw
  }
}

function responseToEditor(response: HTTPMockResponseSpec = {}): ResponseEditor {
  return {
    action: response.action || 'RESPOND',
    status: response.status || 200,
    contentType: response.contentType || 'text/plain; charset=utf-8',
    body: response.body || '',
    delayMs: response.delayMs || 0,
    timeoutMs: response.timeoutMs || 5000,
    headersJson: pretty(response.headers || {}),
  }
}

function responseFromEditor(response: ResponseEditor, label: string): HTTPMockResponseSpec {
  return {
    action: response.action,
    status: response.status,
    contentType: response.contentType,
    headers: parseJSON<Record<string, string>>(response.headersJson || '', {}, label + ' Headers'),
    body: response.body || '',
    delayMs: response.delayMs || 0,
    timeoutMs: response.timeoutMs || 0,
  }
}

function conditionValueToText(value: unknown) {
  if (typeof value === 'string') return value
  if (value === undefined) return ''
  return JSON.stringify(value)
}

function conditionValueFromText(raw?: string) {
  const text = (raw || '').trim()
  if (!text) return ''
  try {
    return JSON.parse(text)
  } catch {
    return raw || ''
  }
}

function endpointToValues(endpoint?: HTTPMockEndpoint): EditorValues {
  const config = endpoint?.config
  const sourceCases = endpoint ? (config?.cases || {}) : DEFAULT_CASES
  const caseNames = Object.keys(sourceCases)
  const firstCase = caseNames[0] || ''
  const suggestedWeightedCases = caseNames.includes('allow') && caseNames.includes('deny')
    ? [{ case: 'allow', weight: 8 }, { case: 'deny', weight: 2 }]
    : caseNames.slice(0, 2).map((name) => ({ case: name, weight: 1 }))
  return {
    name: endpoint?.name || '拨打前确认',
    enabled: endpoint?.enabled ?? true,
    allowedMethods: config?.allowedMethods || ['POST'],
    overridePolicy: config?.overridePolicy || 'CASE_ONLY',
    defaultResponse: responseToEditor(config?.defaultResponse || { status: 200, body: 'true' }),
    defaultResultMode: config?.defaultWeightedCases?.length ? 'WEIGHTED' : 'FIXED',
    defaultWeightedCases: config?.defaultWeightedCases?.length
      ? config.defaultWeightedCases
      : suggestedWeightedCases,
    sequenceCases: config?.sequenceCases || [],
    cases: Object.entries(sourceCases).map(([name, response]) => ({ name, ...responseToEditor(response) })),
    rules: (config?.rules || []).map((rule) => ({
      name: rule.name,
      priority: rule.priority || 0,
      conditions: rule.conditions.map((condition) => ({
        source: condition.source,
        field: condition.field,
        operator: condition.operator,
        valueText: conditionValueToText(condition.value),
      })),
      resultMode: rule.weightedCases?.length ? 'WEIGHTED' : 'FIXED',
      case: rule.case || firstCase,
      weightedCases: rule.weightedCases?.length ? rule.weightedCases : [{ case: firstCase, weight: 1 }],
    })),
    remark: endpoint?.remark || '',
  }
}

function weightedSummary(items?: HTTPMockWeightedCase[]) {
  if (!items?.length) return ''
  const total = items.reduce((sum, item) => sum + (item.weight || 0), 0)
  if (total <= 0) return items.map((item) => item.case).join(' / ')
  return items.map((item) => {
    const percent = Math.round((item.weight / total) * 1000) / 10
    return item.case + ' ' + percent + '%'
  }).join(' / ')
}

function responseSummary(response: HTTPMockResponseSpec) {
  if (response.action === 'TIMEOUT') return 'TIMEOUT ' + (response.timeoutMs || 5000) + 'ms'
  const body = response.body || '(empty)'
  return String(response.status || 200) + ' · ' + (body.length > 40 ? body.slice(0, 40) + '…' : body)
}

function conditionSummary(condition: HTTPMockCondition) {
  const source = SOURCE_LABELS[condition.source]
  const field = condition.field ? ' [' + condition.field + ']' : ''
  const operator = OPERATOR_LABELS[condition.operator]
  const value = condition.operator === 'EXISTS' ? '' : ' ' + JSON.stringify(condition.value)
  return source + field + ' ' + operator + value
}

function ruleResultSummary(rule: HTTPMockRule) {
  if (rule.weightedCases?.length) return '按概率：' + weightedSummary(rule.weightedCases)
  return '固定 Case：' + (rule.case || '-')
}

function ResponseFields({ prefix, compact = false }: { prefix: Array<string | number>; compact?: boolean }) {
  const name = (field: string) => [...prefix, field]
  return (
    <Row gutter={12}>
      <Col span={6}><Form.Item name={name('action')} label="响应动作" rules={[{ required: true }]}><Select options={[{ value: 'RESPOND', label: '正常响应' }, { value: 'TIMEOUT', label: '保持连接/超时' }]} /></Form.Item></Col>
      <Col span={6}><Form.Item name={name('status')} label="状态码" rules={[{ required: true }]}><InputNumber min={200} max={599} style={{ width: '100%' }} /></Form.Item></Col>
      <Col span={6}><Form.Item name={name('delayMs')} label="响应延迟(ms)"><InputNumber min={0} max={30000} style={{ width: '100%' }} /></Form.Item></Col>
      <Col span={6}><Form.Item name={name('timeoutMs')} label="保持连接(ms)"><InputNumber min={0} max={30000} style={{ width: '100%' }} /></Form.Item></Col>
      <Col span={24}><Form.Item name={name('contentType')} label="Content-Type"><Input /></Form.Item></Col>
      <Col span={24}><Form.Item name={name('body')} label="原始响应体"><TextArea rows={compact ? 3 : 4} style={{ fontFamily: 'monospace' }} /></Form.Item></Col>
      <Col span={24}><Form.Item name={name('headersJson')} label="响应 Headers JSON"><TextArea rows={compact ? 2 : 3} style={{ fontFamily: 'monospace' }} /></Form.Item></Col>
    </Row>
  )
}

function WeightedCaseList({ name, caseOptions }: { name: string | Array<string | number>; caseOptions: Array<{ value: string; label: string }> }) {
  return (
    <Form.List name={name}>
      {(fields, { add, remove }) => (
        <Space direction="vertical" style={{ width: '100%' }} size={8}>
          {fields.map((field) => (
            <Row gutter={8} key={field.key} align="middle">
              <Col span={15}>
                <Form.Item name={[field.name, 'case']} rules={[{ required: true, message: '请选择 Case' }]} style={{ marginBottom: 0 }}>
                  <Select placeholder="选择命名 Case" options={caseOptions} />
                </Form.Item>
              </Col>
              <Col span={7}>
                <Form.Item name={[field.name, 'weight']} rules={[{ required: true, message: '请输入权重' }]} style={{ marginBottom: 0 }}>
                  <InputNumber min={1} max={1000000} addonBefore="权重" style={{ width: '100%' }} />
                </Form.Item>
              </Col>
              <Col span={2}><Button type="text" danger icon={<MinusCircleOutlined />} onClick={() => remove(field.name)} /></Col>
            </Row>
          ))}
          <Button type="dashed" icon={<PlusOutlined />} block onClick={() => add({ case: caseOptions[0]?.value || '', weight: 1 })}>增加概率结果</Button>
          <Text type="secondary">权重按相对比例计算，例如 8 / 2 等于 80% / 20%，不要求合计为 100。</Text>
        </Space>
      )}
    </Form.List>
  )
}

function CodeBlock({ text }: { text: string }) {
  return (
    <Paragraph copyable={{ text }} style={{ marginBottom: 0 }}>
      <pre style={{ margin: 0, padding: 12, borderRadius: 6, background: '#0b1021', color: '#d6e0ff', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{text}</pre>
    </Paragraph>
  )
}

function EndpointStrategy({ endpoint }: { endpoint: HTTPMockEndpoint }) {
  const rules = [...(endpoint.config.rules || [])].sort((left, right) => (right.priority || 0) - (left.priority || 0))
  const cases = endpoint.config.cases || {}
  return (
    <div style={{ padding: '4px 16px 12px' }}>
      <Alert
        type="info"
        showIcon
        message={endpoint.config.sequenceCases?.length
          ? '当前启用全局顺序响应；所有请求共享位置，显式 Case 不消耗顺序。'
          : '允许 Method 只负责准入；真正选择响应的是下面的请求条件。规则按优先级从高到低，第一条命中即停止。'}
        style={{ marginBottom: 12 }}
      />
      <Space direction="vertical" style={{ width: '100%' }} size={8}>
        {!!endpoint.config.sequenceCases?.length && (
          <Card size="small"><Text strong>顺序响应：</Text> {endpoint.config.sequenceCases.map((name, index) => <Tag key={`${index}-${name}`}>{index + 1}. {name}</Tag>)}</Card>
        )}
        {!endpoint.config.sequenceCases?.length && <>
        {rules.length === 0
          ? <Text type="secondary">没有参数规则，所有允许的请求都走“未命中规则时”的结果。</Text>
          : rules.map((rule, index) => (
            <Card size="small" key={rule.name + index}>
              <Space direction="vertical" size={4}>
                <Space><Tag color="blue">优先级 {rule.priority || 0}</Tag><Text strong>{rule.name}</Text></Space>
                <Text>当：{rule.conditions.map(conditionSummary).join(' 且 ')}</Text>
                <Text>返回：{ruleResultSummary(rule)}</Text>
              </Space>
            </Card>
          ))}
        <Card size="small">
          <Text strong>未命中任何规则时：</Text>{' '}
          {endpoint.config.defaultWeightedCases?.length
            ? <><Tag color="purple">概率</Tag><Text>{weightedSummary(endpoint.config.defaultWeightedCases)}</Text></>
            : <><Tag>固定</Tag><Text>{responseSummary(endpoint.config.defaultResponse)}</Text></>}
        </Card>
        </>}
        <div>
          <Text type="secondary">可用 Cases：</Text>{' '}
          {Object.entries(cases).map(([name, response]) => <Tag key={name}>{name} · {responseSummary(response)}</Tag>)}
        </div>
      </Space>
    </div>
  )
}

function UsageDrawer({ endpoint, onClose }: { endpoint: HTTPMockEndpoint | null; onClose: () => void }) {
  const url = endpoint?.invokeUrl || endpoint?.invokePath || '/mock/<token>'
  const policy = endpoint?.config.overridePolicy || 'CASE_ONLY'
  const caseNames = Object.keys(endpoint?.config.cases || {})
  const exampleCase = caseNames.includes('deny') ? 'deny' : (caseNames[0] || 'case-name')
  const plain = "curl -X POST '" + url + "'"
  const query = "curl -X POST '" + url + "?scene=deny'"
  const header = "curl -X POST '" + url + "' \\\n  -H 'X-Tenant: test-org'"
  const jsonBody = "curl -X POST '" + url + "' \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"number\":\"8613800000001\",\"scene\":\"deny\"}'"
  const explicitCase = "curl -X POST '" + url + "?__mock_case=" + exampleCase + "'"
  const explicitCaseHeader = "curl -X POST '" + url + "' \\\n  -H 'X-Mock-Case: " + exampleCase + "'"
  const fullOverride = "curl -X POST '" + url + "?__mock_status=503&__mock_delay_ms=1000&__mock_body=busy'"

  return (
    <Drawer title={endpoint ? '使用方式 · ' + endpoint.name : '使用方式'} width={820} open={!!endpoint} onClose={onClose}>
      {endpoint && (
        <Space direction="vertical" style={{ width: '100%' }} size={16}>
          <Card size="small" title="调用地址"><Text code copyable={{ text: url }}>{url}</Text></Card>
          <Alert
            type="info"
            showIcon
            message="决策顺序"
            description={endpoint.config.sequenceCases?.length
              ? '请求通过 Method 校验后按 Endpoint 全局顺序选择 Case；序列结束后保持最后一项。调用方显式 Case 优先且不消耗顺序，FULL 覆盖最后生效。'
              : '请求先通过允许 Method 校验，再按 priority 命中第一条参数规则；规则可固定返回一个 Case，也可按权重随机 Case。最后才应用调用方显式 Case 和 FULL 覆盖。'}
          />
          <Collapse
            defaultActiveKey={['plain', 'match']}
            items={[
              {
                key: 'plain',
                label: '1. 直接调用：走默认响应或默认概率池',
                children: <CodeBlock text={plain} />,
              },
              {
                key: 'match',
                label: '2. 根据请求参数分流：Query / Header / JSON Body / 原始 Body / Method',
                children: (
                  <Space direction="vertical" style={{ width: '100%' }}>
                    <Text>在编辑器的“请求匹配规则”里配置条件。一个规则内的条件是 AND，多条规则按优先级取第一条。</Text>
                    <CodeBlock text={query} />
                    <CodeBlock text={header} />
                    <CodeBlock text={jsonBody} />
                    <Descriptions bordered size="small" column={1} items={[
                      { key: 'method', label: 'method', children: '匹配 GET / POST 等请求方法；Allowed Methods 只是准入限制。' },
                      { key: 'query', label: 'query', children: '按 URL Query 参数名匹配，例如 scene=deny。' },
                      { key: 'header', label: 'header', children: '按请求 Header 匹配，例如 X-Tenant。' },
                      { key: 'jsonBody', label: 'jsonBody', children: '按点路径读取 JSON，例如 user.id、items.0.code。' },
                      { key: 'rawBody', label: 'rawBody', children: '直接匹配未经解析的原始请求体。' },
                      { key: 'operators', label: '操作符', children: 'EQ / NE / IN / CONTAINS / PREFIX / EXISTS。' },
                    ]} />
                  </Space>
                ),
              },
              {
                key: 'sequence',
                label: '确定性顺序响应',
                children: endpoint.config.sequenceCases?.length
                  ? <Text>{endpoint.config.sequenceCases.join(' → ')}；序列结束后保持最后一项，保存 Endpoint 或重启进程后回到第一项。</Text>
                  : <Text type="secondary">当前未配置顺序响应。</Text>,
              },
              {
                key: 'probability',
                label: '3. 根据概率返回不同结果',
                children: (
                  <Space direction="vertical" style={{ width: '100%' }}>
                    <Text>先定义 allow / deny / timeout / error 等命名 Case，再给“未命中规则时”或某条参数规则配置相对权重。每次请求独立随机选择，并在调用记录中保存命中的 Case、权重和选择方式。</Text>
                    <Text>当前未命中策略：{endpoint.config.defaultWeightedCases?.length ? weightedSummary(endpoint.config.defaultWeightedCases) : '固定默认响应'}</Text>
                  </Space>
                ),
              },
              {
                key: 'case',
                label: '4. 调用方强制选择命名 Case',
                children: policy === 'NONE'
                  ? <Alert type="warning" showIcon message="当前覆盖策略为 NONE，调用方 Case 参数会被忽略。" />
                  : <Space direction="vertical" style={{ width: '100%' }}><CodeBlock text={explicitCase} /><CodeBlock text={explicitCaseHeader} /></Space>,
              },
              {
                key: 'override',
                label: '5. 单次覆盖状态码、响应体、延迟或超时',
                children: policy === 'FULL'
                  ? <Space direction="vertical" style={{ width: '100%' }}><CodeBlock text={fullOverride} /><Text type="secondary">还支持 __mock_action、__mock_timeout_ms、__mock_content_type，以及对应 X-Mock-* Header。</Text></Space>
                  : <Alert type="warning" showIcon message={'当前策略为 ' + policy + '，只有 FULL 才允许单次覆盖响应字段。'} />,
              },
            ]}
          />
        </Space>
      )}
    </Drawer>
  )
}

export default function HttpMockPage() {
  const [endpoints, setEndpoints] = useState<HTTPMockEndpoint[]>([])
  const [loading, setLoading] = useState(false)
  const [editing, setEditing] = useState<HTTPMockEndpoint | null | undefined>(undefined)
  const [usageEndpoint, setUsageEndpoint] = useState<HTTPMockEndpoint | null>(null)
  const [form] = Form.useForm<EditorValues>()
  const watchedCases = Form.useWatch('cases', form) || []
  const caseOptions = watchedCases
    .map((item, index) => ({ value: (item?.name || '').trim(), label: (item?.name || '').trim() || 'Case ' + (index + 1) }))
    .filter((item) => item.value)
  const [requestEndpoint, setRequestEndpoint] = useState<HTTPMockEndpoint | null>(null)
  const [requests, setRequests] = useState<HTTPMockRequestRecord[]>([])
  const [requestLoading, setRequestLoading] = useState(false)
  const [keyword, setKeyword] = useState('')

  const load = async () => {
    setLoading(true)
    try {
      const result = await listHTTPMocks()
      setEndpoints(result.endpoints || [])
    } catch (error) {
      message.error(String(error))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void load() }, [])

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue(endpointToValues())
  }

  const openEdit = (endpoint: HTTPMockEndpoint) => {
    setEditing(endpoint)
    form.resetFields()
    form.setFieldsValue(endpointToValues(endpoint))
  }

  const closeEditor = () => setEditing(undefined)

  const save = async () => {
    let values: EditorValues
    try {
      values = await form.validateFields()
    } catch {
      return
    }
    try {
      const cases: Record<string, HTTPMockResponseSpec> = {}
      for (const item of values.cases || []) {
        const name = (item.name || '').trim()
        if (!name) throw new Error('Case 名称必填')
        if (cases[name]) throw new Error('Case 名称重复：' + name)
        cases[name] = responseFromEditor(item, 'Case ' + name)
      }
      if (values.defaultResultMode === 'WEIGHTED' && !values.defaultWeightedCases?.length) {
        throw new Error('默认概率结果至少配置一项')
      }
      const rules: HTTPMockRule[] = (values.rules || []).map((rule, index) => {
        const conditions: HTTPMockCondition[] = (rule.conditions || []).map((condition) => ({
          source: condition.source,
          field: condition.source === 'method' || condition.source === 'rawBody' ? undefined : condition.field,
          operator: condition.operator,
          value: condition.operator === 'EXISTS' ? undefined : conditionValueFromText(condition.valueText),
        }))
        if (rule.resultMode === 'WEIGHTED') {
          if (!rule.weightedCases?.length) throw new Error('规则 ' + (rule.name || index + 1) + ' 至少配置一个概率结果')
          return { name: rule.name, priority: rule.priority || 0, conditions, weightedCases: rule.weightedCases }
        }
        return { name: rule.name, priority: rule.priority || 0, conditions, case: rule.case }
      })
      const config: HTTPMockEndpointConfig = {
        allowedMethods: values.allowedMethods || [],
        overridePolicy: values.overridePolicy,
        defaultResponse: responseFromEditor(values.defaultResponse, '默认响应'),
        defaultWeightedCases: values.defaultResultMode === 'WEIGHTED' ? values.defaultWeightedCases : undefined,
        sequenceCases: values.sequenceCases?.length ? values.sequenceCases : undefined,
        cases,
        rules,
      }
      const payload: HTTPMockEndpoint = {
        name: values.name,
        enabled: values.enabled,
        config,
        remark: values.remark,
      }
      if (editing?.id) await updateHTTPMock(editing.id, payload)
      else await createHTTPMock(payload)
      message.success(editing?.id ? 'HTTP Mock 已更新' : 'HTTP Mock 已创建')
      closeEditor()
      await load()
    } catch (error) {
      message.error(String(error))
    }
  }

  const remove = (endpoint: HTTPMockEndpoint) => Modal.confirm({
    title: '删除 ' + endpoint.name + '？',
    content: 'Endpoint 及其全部调用记录会一起删除，无法恢复。',
    okText: '删除',
    okButtonProps: { danger: true },
    cancelText: '取消',
    onOk: async () => {
      if (endpoint.id) {
        await deleteHTTPMock(endpoint.id)
        await load()
      }
    },
  })

  const copyURL = async (url?: string) => {
    if (!url) return
    try {
      await navigator.clipboard.writeText(url)
      message.success('调用 URL 已复制')
    } catch {
      message.info(url)
    }
  }

  const loadRequests = async (endpoint = requestEndpoint) => {
    if (!endpoint?.id) return
    setRequestLoading(true)
    try {
      const result = await listHTTPMockRequests(endpoint.id, { keyword, limit: 300 })
      setRequests(result.requests || [])
    } catch (error) {
      message.error(String(error))
    } finally {
      setRequestLoading(false)
    }
  }

  const openRequests = async (endpoint: HTTPMockEndpoint) => {
    setRequestEndpoint(endpoint)
    setKeyword('')
    await loadRequests(endpoint)
  }

  const clearRequests = () => requestEndpoint?.id && Modal.confirm({
    title: '清空调用记录？',
    content: '仅清空该 Endpoint 的观测记录，配置不受影响。',
    okText: '清空',
    okButtonProps: { danger: true },
    cancelText: '取消',
    onOk: async () => {
      const result = await clearHTTPMockRequests(requestEndpoint.id!)
      message.success('已清理 ' + result.deleted + ' 条')
      await loadRequests()
    },
  })

  const editorOpen = editing !== undefined
  const defaultResultMode = Form.useWatch('defaultResultMode', form)

  return (
    <div className="page-container">
      <PageHeader
        title="通用 HTTP Mock"
        status={{ tone: 'info', text: endpoints.length + ' 个 Endpoint' }}
        extra={<Space><Button icon={<ReloadOutlined />} loading={loading} onClick={load}>刷新</Button><Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建 Endpoint</Button></Space>}
      />
      <InfoBanner title="请求条件决定结果，不只看 Method">
        <Text strong>允许 Method 只负责限制请求能否进入。</Text> 真正的响应分流可同时按 Query、Header、JSON Body 字段、原始 Body 和 Method 配置；命中后既能固定返回某个 Case，也能在多个 Case 间按权重随机。点击每行展开可直接看到完整决策。
      </InfoBanner>

      <Card size="small" title="Endpoint">
        <Table
          rowKey={(row) => String(row.id)}
          size="small"
          loading={loading}
          dataSource={endpoints}
          pagination={{ pageSize: 15 }}
          scroll={{ x: 1300 }}
          expandable={{ expandedRowRender: (row) => <EndpointStrategy endpoint={row} /> }}
          columns={[
            { title: '状态', width: 72, render: (_: unknown, row: HTTPMockEndpoint) => <Tag color={row.enabled ? 'green' : 'default'}>{row.enabled ? '启用' : '禁用'}</Tag> },
            { title: '名称', dataIndex: 'name', width: 160, render: (value: string, row: HTTPMockEndpoint) => <><div>{value}</div>{row.remark && <Text type="secondary" style={{ fontSize: 11 }}>{row.remark}</Text>}</> },
            { title: '允许 Method', width: 145, render: (_: unknown, row: HTTPMockEndpoint) => (row.config.allowedMethods?.length ? row.config.allowedMethods : ['ANY']).map((method) => <Tag key={method}>{method}</Tag>) },
            {
              title: '请求分流',
              width: 180,
              render: (_: unknown, row: HTTPMockEndpoint) => {
                if (row.config.sequenceCases?.length) return <><div>全局顺序响应</div><Tag color="cyan">{row.config.sequenceCases.join(' → ')}</Tag></>
                const rules = row.config.rules || []
                const sources = Array.from(new Set(rules.flatMap((rule) => rule.conditions.map((condition) => condition.source))))
                return <><div>{rules.length ? rules.length + ' 条参数规则' : '无参数规则'}</div>{sources.map((source) => <Tag key={source} color="blue">{SOURCE_LABELS[source]}</Tag>)}</>
              },
            },
            {
              title: '未命中规则时',
              width: 210,
              render: (_: unknown, row: HTTPMockEndpoint) => row.config.sequenceCases?.length
                ? <><Tag color="cyan">顺序</Tag><Text>{row.config.sequenceCases.join(' → ')}</Text></>
                : row.config.defaultWeightedCases?.length
                ? <><Tag color="purple">概率</Tag><Text>{weightedSummary(row.config.defaultWeightedCases)}</Text></>
                : <><Tag>固定</Tag><Text>{responseSummary(row.config.defaultResponse)}</Text></>,
            },
            { title: '调用地址', render: (_: unknown, row: HTTPMockEndpoint) => <Space><Text code copyable={{ text: row.invokeUrl }} style={{ fontSize: 11 }}>{row.invokeUrl || row.invokePath}</Text><Button type="text" size="small" icon={<CopyOutlined />} onClick={() => copyURL(row.invokeUrl)} /></Space> },
            { title: '操作', width: 260, render: (_: unknown, row: HTTPMockEndpoint) => <Space size={4}><Button size="small" onClick={() => setUsageEndpoint(row)}>用法</Button><Button size="small" onClick={() => openRequests(row)}>记录</Button><Button size="small" icon={<EditOutlined />} onClick={() => openEdit(row)}>编辑</Button><Button size="small" danger icon={<DeleteOutlined />} onClick={() => remove(row)}>删除</Button></Space> },
          ]}
        />
      </Card>

      <UsageDrawer endpoint={usageEndpoint} onClose={() => setUsageEndpoint(null)} />

      <Drawer
        title={editing?.id ? '编辑 HTTP Mock · ' + editing.name : '新建 HTTP Mock'}
        width={960}
        open={editorOpen}
        onClose={closeEditor}
        footer={<div style={{ textAlign: 'right' }}><Space><Button onClick={closeEditor}>取消</Button><Button type="primary" onClick={save}>保存</Button></Space></div>}
      >
        <Form form={form} layout="vertical">
          <Card size="small" title="1. Endpoint 基础信息" style={{ marginBottom: 16 }}>
            <Row gutter={12}>
              <Col span={15}><Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item></Col>
              <Col span={9}><Form.Item name="enabled" label="启用" valuePropName="checked"><Switch /></Form.Item></Col>
              <Col span={12}><Form.Item name="allowedMethods" label="允许 Method（仅准入，不负责选择结果）"><Select mode="multiple" allowClear options={METHOD_OPTIONS} /></Form.Item></Col>
              <Col span={12}><Form.Item name="overridePolicy" label="调用方覆盖权限"><Select options={[
                { value: 'NONE', label: 'NONE · 忽略调用方控制参数' },
                { value: 'CASE_ONLY', label: 'CASE_ONLY · 可强制选命名 Case' },
                { value: 'FULL', label: 'FULL · 可覆盖 Case / 状态 / Body / 延迟' },
              ]} /></Form.Item></Col>
              <Col span={24}><Form.Item name="remark" label="备注"><Input /></Form.Item></Col>
            </Row>
          </Card>

          <Card size="small" title="2. 固定默认响应" style={{ marginBottom: 16 }}>
            <Space wrap style={{ marginBottom: 12 }}>
              <Button size="small" onClick={() => form.setFieldValue('defaultResponse', responseToEditor({ status: 200, body: 'true' }))}>200 true</Button>
              <Button size="small" onClick={() => form.setFieldValue('defaultResponse', responseToEditor({ status: 200, body: 'false' }))}>200 false</Button>
              <Button size="small" onClick={() => form.setFieldValue('defaultResponse', responseToEditor({ status: 500, body: 'mock error' }))}>HTTP 500</Button>
              <Button size="small" onClick={() => form.setFieldValue('defaultResponse', responseToEditor({ action: 'TIMEOUT', status: 200, timeoutMs: 5000 }))}>超时 5s</Button>
            </Space>
            <ResponseFields prefix={['defaultResponse']} />
          </Card>

          <Card size="small" title="3. 命名响应 Cases" style={{ marginBottom: 16 }}>
            <Paragraph type="secondary">Case 是可复用的完整响应结果。规则、概率池和调用方的 __mock_case 都引用这里的名字。</Paragraph>
            <Form.List name="cases">
              {(fields, { add, remove }) => (
                <Space direction="vertical" style={{ width: '100%' }} size={12}>
                  {fields.map((field, index) => (
                    <Card
                      size="small"
                      type="inner"
                      key={field.key}
                      title={'Case ' + (index + 1)}
                      extra={<Button type="text" danger icon={<DeleteOutlined />} onClick={() => remove(field.name)}>删除</Button>}
                    >
                      <Form.Item name={[field.name, 'name']} label="Case 名称" rules={[{ required: true, message: '请输入 Case 名称' }]}>
                        <Input placeholder="例如 allow / deny / timeout / error" />
                      </Form.Item>
                      <ResponseFields prefix={[field.name]} compact />
                    </Card>
                  ))}
                  <Button type="dashed" icon={<PlusOutlined />} block onClick={() => add({ name: 'case_' + (fields.length + 1), ...responseToEditor({ status: 200, body: 'ok' }) })}>增加命名 Case</Button>
                </Space>
              )}
            </Form.List>
          </Card>

          <Card size="small" title="4. 未命中任何规则时" style={{ marginBottom: 16 }}>
            <Form.Item name="defaultResultMode" label="结果选择方式">
              <Select options={[
                { value: 'FIXED', label: '固定使用上面的默认响应' },
                { value: 'WEIGHTED', label: '在命名 Cases 中按权重随机' },
              ]} />
            </Form.Item>
            {defaultResultMode === 'WEIGHTED' && <WeightedCaseList name="defaultWeightedCases" caseOptions={caseOptions} />}
          </Card>

          <Card size="small" title="5. 顺序响应（可选）" style={{ marginBottom: 16 }}>
            <Alert type="info" showIcon style={{ marginBottom: 12 }} message="配置后优先于规则和默认结果；并发请求共享同一顺序，序列结束后保持最后一项。" />
            <Form.List name="sequenceCases">
              {(fields, { add, remove, move }) => (
                <Space direction="vertical" style={{ width: '100%' }} size={8}>
                  {fields.map((field, index) => (
                    <Row gutter={8} key={field.key} align="middle">
                      <Col span={2}><Tag>{index + 1}</Tag></Col>
                      <Col span={17}><Form.Item name={field.name} rules={[{ required: true, message: '请选择 Case' }]} style={{ marginBottom: 0 }}><Select options={caseOptions} /></Form.Item></Col>
                      <Col span={5}><Space size={2}>
                        <Button type="text" disabled={index === 0} icon={<ArrowUpOutlined />} onClick={() => move(index, index - 1)} />
                        <Button type="text" disabled={index === fields.length - 1} icon={<ArrowDownOutlined />} onClick={() => move(index, index + 1)} />
                        <Button type="text" danger icon={<MinusCircleOutlined />} onClick={() => remove(field.name)} />
                      </Space></Col>
                    </Row>
                  ))}
                  <Button type="dashed" icon={<PlusOutlined />} disabled={!caseOptions.length || fields.length >= 100} onClick={() => add(caseOptions[0]?.value)}>增加顺序项</Button>
                </Space>
              )}
            </Form.List>
          </Card>

          <Card size="small" title="6. 请求匹配规则" style={{ marginBottom: 16 }}>
            <Alert
              type="info"
              showIcon
              message="不只支持 Method"
              description="可按 Query、Header、JSON Body 点路径、原始 Body 和 Method 组合条件。同一规则内为 AND；priority 越大越先判断；第一条命中即停止。"
              style={{ marginBottom: 12 }}
            />
            <Form.List name="rules">
              {(fields, { add, remove }) => {
                const addRule = (source: HTTPMockConditionSource) => add({
                  name: 'rule-' + (fields.length + 1),
                  priority: 10,
                  resultMode: 'FIXED',
                  case: caseOptions[0]?.value || '',
                  weightedCases: [{ case: caseOptions[0]?.value || '', weight: 1 }],
                  conditions: [{
                    source,
                    field: source === 'query' ? 'scene' : source === 'header' ? 'X-Tenant' : source === 'jsonBody' ? 'number' : '',
                    operator: source === 'rawBody' ? 'CONTAINS' : 'EQ',
                    valueText: '',
                  }],
                })
                return (
                  <Space direction="vertical" style={{ width: '100%' }} size={12}>
                    {fields.map((field, index) => (
                      <Card
                        size="small"
                        type="inner"
                        key={field.key}
                        title={'规则 ' + (index + 1)}
                        extra={<Button type="text" danger icon={<DeleteOutlined />} onClick={() => remove(field.name)}>删除</Button>}
                      >
                        <Row gutter={12}>
                          <Col span={12}><Form.Item name={[field.name, 'name']} label="规则名称" rules={[{ required: true }]}><Input /></Form.Item></Col>
                          <Col span={5}><Form.Item name={[field.name, 'priority']} label="优先级"><InputNumber style={{ width: '100%' }} /></Form.Item></Col>
                          <Col span={7}><Form.Item name={[field.name, 'resultMode']} label="命中后"><Select options={[{ value: 'FIXED', label: '固定 Case' }, { value: 'WEIGHTED', label: '概率 Cases' }]} /></Form.Item></Col>
                        </Row>
                        <Text strong>当以下条件全部满足：</Text>
                        <Form.List name={[field.name, 'conditions']}>
                          {(conditionFields, conditionOps) => (
                            <Space direction="vertical" style={{ width: '100%', marginTop: 8, marginBottom: 12 }} size={8}>
                              {conditionFields.map((conditionField) => (
                                <Row gutter={8} key={conditionField.key} align="middle">
                                  <Col span={5}><Form.Item name={[conditionField.name, 'source']} rules={[{ required: true }]} style={{ marginBottom: 0 }}><Select options={SOURCE_OPTIONS} /></Form.Item></Col>
                                  <Col span={6}><Form.Item name={[conditionField.name, 'field']} style={{ marginBottom: 0 }}><Input placeholder="字段名；method/rawBody 留空" /></Form.Item></Col>
                                  <Col span={5}><Form.Item name={[conditionField.name, 'operator']} rules={[{ required: true }]} style={{ marginBottom: 0 }}><Select options={OPERATOR_OPTIONS} /></Form.Item></Col>
                                  <Col span={7}><Form.Item name={[conditionField.name, 'valueText']} style={{ marginBottom: 0 }}><Input placeholder={'值或 JSON，例如 ["a","b"]'} /></Form.Item></Col>
                                  <Col span={1}><Button type="text" danger icon={<MinusCircleOutlined />} onClick={() => conditionOps.remove(conditionField.name)} /></Col>
                                </Row>
                              ))}
                              <Button type="dashed" size="small" icon={<PlusOutlined />} onClick={() => conditionOps.add({ source: 'query', field: 'scene', operator: 'EQ', valueText: '' })}>增加 AND 条件</Button>
                            </Space>
                          )}
                        </Form.List>
                        <Text strong>返回结果：</Text>
                        <Form.Item noStyle shouldUpdate>
                          {({ getFieldValue }) => getFieldValue(['rules', field.name, 'resultMode']) === 'WEIGHTED'
                            ? <div style={{ marginTop: 8 }}><WeightedCaseList name={[field.name, 'weightedCases']} caseOptions={caseOptions} /></div>
                            : <Form.Item name={[field.name, 'case']} rules={[{ required: true, message: '请选择 Case' }]} style={{ marginTop: 8, marginBottom: 0 }}><Select placeholder="固定返回某个命名 Case" options={caseOptions} /></Form.Item>}
                        </Form.Item>
                      </Card>
                    ))}
                    <Space wrap>
                      <Button type="dashed" icon={<PlusOutlined />} onClick={() => addRule('query')}>新增 Query 规则</Button>
                      <Button type="dashed" icon={<PlusOutlined />} onClick={() => addRule('header')}>新增 Header 规则</Button>
                      <Button type="dashed" icon={<PlusOutlined />} onClick={() => addRule('jsonBody')}>新增 JSON Body 规则</Button>
                      <Button type="dashed" icon={<PlusOutlined />} onClick={() => addRule('rawBody')}>新增原始 Body 规则</Button>
                    </Space>
                  </Space>
                )
              }}
            </Form.List>
          </Card>
        </Form>
      </Drawer>

      <Drawer title={requestEndpoint ? '调用记录 · ' + requestEndpoint.name : '调用记录'} width={1040} open={!!requestEndpoint} onClose={() => setRequestEndpoint(null)}>
        <InfoBanner title="观测记录自动治理">
          记录不会阻塞实际响应；队列满时宁可丢记录也不改变 Mock 行为。历史记录随 OBSERVE_TTL_DAYS 周期清理，也可手工清空当前 Endpoint。
        </InfoBanner>
        <Space style={{ marginBottom: 12 }} wrap>
          <Input.Search allowClear placeholder="请求/响应关键字" value={keyword} onChange={(event) => setKeyword(event.target.value)} onSearch={() => loadRequests()} style={{ width: 240 }} />
          <Button icon={<ReloadOutlined />} loading={requestLoading} onClick={() => loadRequests()}>刷新</Button>
          <Button danger onClick={clearRequests}>清空记录</Button>
          <Text type="secondary">最近 {requests.length} 条</Text>
        </Space>
        <Table
          rowKey="id"
          size="small"
          loading={requestLoading}
          dataSource={requests}
          pagination={{ pageSize: 20 }}
          expandable={{ expandedRowRender: (row) => <Paragraph><pre style={{ fontSize: 11, background: '#0b1021', color: '#d6e0ff', padding: 12, borderRadius: 6, whiteSpace: 'pre-wrap' }}>{pretty({ request: { method: row.method, path: row.path, remote: row.remote, query: safeJSON(row.queryJson), headers: safeJSON(row.headersJson), body: row.requestBody }, decision: { matchedRule: row.matchedRule, selectedCase: row.selectedCase, selectionMode: row.selectionMode, selectedWeight: row.selectedWeight, totalWeight: row.totalWeight, overrides: safeJSON(row.overrideJson) }, response: { action: row.responseAction, status: row.responseStatus, headers: safeJSON(row.responseHeadersJson), body: row.responseBody, delayMs: row.delayMs }, durationMs: row.durationMs, clientCanceled: row.clientCanceled })}</pre></Paragraph> }}
          columns={[
            { title: '时间', dataIndex: 'receivedAt', width: 170, render: (value: string) => new Date(value).toLocaleString() },
            { title: 'Method', dataIndex: 'method', width: 82, render: (value: string) => <Tag>{value}</Tag> },
            {
              title: '命中方式',
              width: 210,
              render: (_: unknown, row: HTTPMockRequestRecord) => <><div><Tag color={row.selectionMode?.includes('WEIGHTED') ? 'purple' : 'blue'}>{SELECTION_LABELS[row.selectionMode] || row.selectionMode || '-'}</Tag>{row.matchedRule || '-'}</div><Text type="secondary">{row.selectedCase || 'default'}{row.totalWeight > 0 ? ' · 权重 ' + row.selectedWeight + '/' + row.totalWeight : ''}</Text></>,
            },
            { title: '请求体', dataIndex: 'requestBody', ellipsis: true, render: (value: string) => <Text code ellipsis>{value || '(empty)'}</Text> },
            { title: '响应', width: 180, render: (_: unknown, row: HTTPMockRequestRecord) => <Space size={4}><Tag color={row.responseStatus >= 400 ? 'red' : 'green'}>{row.responseStatus}</Tag><Text code ellipsis style={{ maxWidth: 95 }}>{row.responseAction === 'TIMEOUT' ? 'TIMEOUT ' + row.delayMs + 'ms' : row.responseBody || '(empty)'}</Text></Space> },
            { title: '耗时', width: 90, render: (_: unknown, row: HTTPMockRequestRecord) => <span>{row.durationMs}ms{row.clientCanceled ? ' · 取消' : ''}</span> },
          ]}
        />
      </Drawer>
    </div>
  )
}
