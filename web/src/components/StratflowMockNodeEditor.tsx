import { Alert, AutoComplete, Button, Card, Collapse, Descriptions, Divider, Input, InputNumber, Select, Space, Table, Tag, Typography } from 'antd'
import { CopyOutlined, DeleteOutlined, PlusOutlined, SaveOutlined, UndoOutlined } from '@ant-design/icons'
import type {
  SfMatchCondition, SfMockCase, SfMockCaseResult, SfNode, SfNodeConfig, SfSelection,
} from '../types'

const { Text } = Typography

const STATUS_LABEL: Record<string, string> = {
  CONNECTED: '接通', NOT_CONNECTED: '未接通/重拨耗尽', CANCELLED: '已取消', NOT_DIALED: '未拨打', NO_RECEIPT: '超时无回执',
  DELIVERED: '送达', FAILED: '失败',
}
const RING_LABEL: Record<string, string> = {
  busy: '忙线中', out_area_or_offline: '不在服务区/关机', wrong_number: '空号', failed: '呼叫失败',
  hold_line: '呼叫等待', rejected: '拒接', no_answer: '无应答', answered: '已接通', normal: '正常',
}

function uniqueKey(cases: SfMockCase[], seed = 'custom_case') {
  const used = new Set(cases.map((item) => item.key))
  if (!used.has(seed)) return seed
  for (let i = 2; ; i++) if (!used.has(`${seed}_${i}`)) return `${seed}_${i}`
}

function cloneConfig(config: SfNodeConfig): SfNodeConfig {
  return JSON.parse(JSON.stringify(config)) as SfNodeConfig
}

function selectionWithCases(selection: SfSelection, valid: Set<string>, fallback?: string): SfSelection {
  if (selection.mode === 'FIXED') {
    return { mode: 'FIXED', caseKey: selection.caseKey && valid.has(selection.caseKey) ? selection.caseKey : fallback }
  }
  const choices = (selection.choices || []).filter((choice) => valid.has(choice.caseKey))
  return { mode: 'WEIGHTED', choices: choices.length ? choices : (fallback ? [{ caseKey: fallback, weight: 1 }] : []) }
}

function defaultResult(node: SfNode): SfMockCaseResult {
  if (node.resultSchema.type === 'CALL') {
    return { type: 'CALL', status: 'CONNECTED', terminalAttemptNo: 1, retryRingStatus: 'no_answer', intention: 'A', talkDurationSec: 30 }
  }
  return { type: 'SMS', status: 'DELIVERED', partCount: 1 }
}

function resetForStatus(result: SfMockCaseResult, status: string): SfMockCaseResult {
  if (result.type === 'CALL') {
    if (status === 'CONNECTED') return { type: 'CALL', status, terminalAttemptNo: 1, retryRingStatus: 'no_answer', intention: 'A', talkDurationSec: 30 }
    if (status === 'NOT_CONNECTED') return { type: 'CALL', status, retryRingStatus: 'no_answer', ringStatus: 'no_answer' }
    if (status === 'CANCELLED') return { type: 'CALL', status, terminalAttemptNo: 1, retryRingStatus: 'no_answer' }
    if (status === 'NOT_DIALED') return { type: 'CALL', status, terminalAttemptNo: 1 }
    return { type: 'CALL', status }
  }
  if (status === 'DELIVERED') return { type: 'SMS', status, partCount: 1 }
  if (status === 'FAILED') return { type: 'SMS', status, errorCode: 'MOCK_FAILED', errorDesc: 'Mock 短信失败' }
  return { type: 'SMS', status }
}

function json(value: unknown) {
  return JSON.stringify(value ?? {}, null, 2)
}

function SelectionEditor({ value, cases, onChange }: { value: SfSelection; cases: SfMockCase[]; onChange: (value: SfSelection) => void }) {
  const options = cases.map((item) => ({ value: item.key, label: `${item.name}（${item.key}）` }))
  const choices = value.choices || []
  const usedCaseKeys = new Set(choices.map((item) => item.caseKey))
  const availableOptions = options.filter((option) => !usedCaseKeys.has(option.value))
  return (
    <Space direction="vertical" style={{ width: '100%' }} size={6}>
      <Space wrap>
        <Select style={{ width: 120 }} value={value.mode} options={[{ value: 'FIXED', label: '固定 Case' }, { value: 'WEIGHTED', label: '按权重随机' }]}
          onChange={(mode: 'FIXED' | 'WEIGHTED') => onChange(mode === 'FIXED'
            ? { mode, caseKey: value.caseKey || choices[0]?.caseKey || cases[0]?.key }
            : { mode, choices: choices.length ? choices : (value.caseKey ? [{ caseKey: value.caseKey, weight: 1 }] : cases.slice(0, 1).map((item) => ({ caseKey: item.key, weight: 1 }))) })} />
        {value.mode === 'FIXED' && <Select style={{ width: 300 }} value={value.caseKey || undefined} options={options} onChange={(caseKey) => onChange({ mode: 'FIXED', caseKey })} />}
      </Space>
      {value.mode === 'WEIGHTED' && (
        <Space direction="vertical" style={{ width: '100%' }} size={4}>
          {choices.map((choice, index) => (
            <Space key={`${choice.caseKey}-${index}`}>
              <Text>Case</Text>
              <Select showSearch optionFilterProp="label" style={{ width: 320 }} value={choice.caseKey} options={options.map((option) => ({
                ...option,
                disabled: choices.some((item, i) => i !== index && item.caseKey === option.value),
              }))} onChange={(caseKey) => {
                const next = [...choices]; next[index] = { ...choice, caseKey }; onChange({ mode: 'WEIGHTED', choices: next })
              }} />
              <Text>权重</Text>
              <InputNumber min={1} max={1_000_000} value={choice.weight} onChange={(weight) => {
                const next = [...choices]; next[index] = { ...choice, weight: Number(weight) || 1 }; onChange({ mode: 'WEIGHTED', choices: next })
              }} />
              <Button size="small" danger disabled={choices.length <= 1} icon={<DeleteOutlined />} onClick={() => onChange({ mode: 'WEIGHTED', choices: choices.filter((_, i) => i !== index) })} />
            </Space>
          ))}
          <Space wrap>
            <Text type="secondary">添加已有 Case</Text>
            <Select showSearch optionFilterProp="label" style={{ width: 320 }} value={undefined}
              placeholder={availableOptions.length ? '选择命名 Case 加入概率池' : '所有 Case 均已加入'}
              disabled={availableOptions.length === 0} options={availableOptions}
              onChange={(caseKey?: string) => {
                if (!caseKey) return
                onChange({ mode: 'WEIGHTED', choices: [...choices, { caseKey, weight: 1 }] })
              }} />
          </Space>
        </Space>
      )}
    </Space>
  )
}

function parseScalar(type: string, value: string): unknown {
  if (type === 'int' || type === 'float' || type === 'number') return value === '' ? undefined : Number(value)
  if (type === 'bool') return value === 'true'
  return value
}

function initialConditionValue(type: string, op: string): unknown {
  if (op === 'isEmpty' || op === 'notEmpty') return undefined
  if (type === 'bool') return true
  if (op === 'between') return {}
  if (['anyOf', 'notIn', 'in'].includes(op) || type === 'array') return []
  return ''
}

function ConditionValue({ condition, onChange }: { condition: SfMatchCondition; onChange: (value: unknown) => void }) {
  if (condition.op === 'isEmpty' || condition.op === 'notEmpty') return <Text type="secondary">无需比较值</Text>
  const valueType = condition.type === 'array' ? (condition.itemType || 'string') : condition.type
  if (condition.op === 'between') {
    const range = (condition.value || {}) as { min?: unknown; max?: unknown }
    return <Space><Input style={{ width: 120 }} placeholder="最小值" value={String(range.min ?? '')} onChange={(e) => onChange({ ...range, min: parseScalar(valueType, e.target.value) })} /><Input style={{ width: 120 }} placeholder="最大值" value={String(range.max ?? '')} onChange={(e) => onChange({ ...range, max: parseScalar(valueType, e.target.value) })} /></Space>
  }
  if (['anyOf', 'notIn', 'in'].includes(condition.op) || condition.type === 'array') {
    const values = Array.isArray(condition.value) ? condition.value.map(String) : []
    return <Select mode="tags" tokenSeparators={[',']} style={{ minWidth: 240 }} value={values} onChange={(items) => onChange(items.map((item) => parseScalar(valueType, item)))} />
  }
  if (condition.type === 'bool') return <Select style={{ width: 120 }} value={String(condition.value ?? 'true')} options={[{ value: 'true', label: 'true' }, { value: 'false', label: 'false' }]} onChange={(v) => onChange(v === 'true')} />
  return <Input style={{ width: 240 }} value={String(condition.value ?? '')} onChange={(e) => onChange(parseScalar(valueType, e.target.value))} />
}

export function StratflowMockNodeEditor({ node, value, disabled, onChange, onSave, onReset }: {
  node: SfNode; value: SfNodeConfig; disabled?: boolean
  onChange: (value: SfNodeConfig) => void; onSave: () => void; onReset: () => void
}) {
  if (!Array.isArray(value?.cases) || !value.defaultSelection || !Array.isArray(value.rules)
    || !node.resultSchema || !Array.isArray(node.resultSchema.statuses)
    || !Array.isArray(node.resultSchema.ringStatuses) || !Array.isArray(node.resultSchema.intentions)
    || !node.matchSchema || !Array.isArray(node.matchSchema.fields) || !node.matchSchema.operators
    || !node.previews) {
    return <Alert type="error" showIcon message="Hermes Mock 配置协议不兼容"
      description="当前 Hermes 后端未返回类型化 cases/defaultSelection/schema/previews。请先部署配套 Hermes 后端并清理旧 sf:mock:cfg:* 配置；本节点已停止编辑以避免页面白屏。" />
  }
  const update = (fn: (next: SfNodeConfig) => void) => { const next = cloneConfig(value); fn(next); onChange(next) }
  const caseOptions = value.cases.map((item) => ({ value: item.key, label: `${item.name}（${item.key}）` }))
  const fieldOptions = node.matchSchema.fields.map((field) => ({ value: field.key, label: `${field.key} · ${field.type}` }))

  const deleteCase = (index: number) => update((next) => {
    if (next.cases.length <= 1) return
    const removed = next.cases[index].key
    next.cases.splice(index, 1)
    const valid = new Set(next.cases.map((item) => item.key)); const fallback = next.cases[0]?.key
    if (next.forcedCaseKey === removed) next.forcedCaseKey = null
    next.defaultSelection = selectionWithCases(next.defaultSelection, valid, fallback)
    next.rules = next.rules.map((rule) => ({ ...rule, selection: selectionWithCases(rule.selection, valid, fallback) }))
  })

  const addCondition = (ruleIndex: number) => update((next) => {
    const first = node.matchSchema.fields[0]
    const type = first?.type || 'string'
    const op = node.matchSchema.operators[type]?.[0] || 'eq'
    next.rules[ruleIndex].conditions.push({ key: first?.key || 'mock_segment', type, itemType: first?.itemType, op, value: initialConditionValue(type, op) })
  })

  return (
    <Space direction="vertical" style={{ width: '100%' }} size={12}>
      <Alert type="info" showIcon
        message={node.configured ? '当前使用已保存的节点配置' : '当前展示 Hermes 默认模板'}
        description={node.configured
          ? '命名 Cases、规则和概率池来自该节点已保存配置；保存会整体覆盖，重置会删除自定义配置并回到 Hermes 默认模板。'
          : '这些命名 Cases 由 Hermes 按 CALL/SMS 节点类型生成，不是 Redis 残留；编辑或新增 Case 后保存，才会成为该节点的自定义配置。概率池只从下方命名 Cases 中选择。'} />
      <Card size="small" title="强制覆盖与默认选择">
        <Space direction="vertical" style={{ width: '100%' }}>
          <Space wrap><Text>临时强制 Case：</Text><Select allowClear style={{ width: 320 }} placeholder="不强制：先规则，后默认选择" value={value.forcedCaseKey || undefined} options={caseOptions} onChange={(forcedCaseKey) => update((next) => { next.forcedCaseKey = forcedCaseKey || null })} /></Space>
          <Divider style={{ margin: '6px 0' }} />
          <Text strong>未命中名单规则时</Text>
          <SelectionEditor value={value.defaultSelection} cases={value.cases} onChange={(selection) => update((next) => { next.defaultSelection = selection })} />
        </Space>
      </Card>

      <Card size="small" title="命名 Cases（固定选择与概率池的候选结果）" extra={<Button size="small" disabled={value.cases.length >= 100} icon={<PlusOutlined />} onClick={() => update((next) => {
        const key = uniqueKey(next.cases); next.cases.push({ key, name: '自定义 Case', delayMs: 0, result: defaultResult(node) })
      })}>新增 Case</Button>}>
        <Collapse items={value.cases.map((mockCase, index) => {
          const result = mockCase.result
          const preview = node.previews[mockCase.key]
          const nonAnsweredRings = node.resultSchema.ringStatuses.filter((item) => item !== 'answered')
          const updateResult = (patch: Partial<SfMockCaseResult>) => update((next) => { next.cases[index].result = { ...next.cases[index].result, ...patch } })
          return {
            // 编辑 key 时 Collapse identity 必须稳定，否则每敲一个字符都会卸载当前面板并丢焦点。
            key: `case-${index}`,
            label: <Space><Text strong>{mockCase.name}</Text><Text code>{mockCase.key}</Text><Tag>{STATUS_LABEL[result.status] || result.status}</Tag>{mockCase.delayMs > 0 && <Tag color="blue">延迟 {mockCase.delayMs}ms</Tag>}</Space>,
            children: <Space direction="vertical" style={{ width: '100%' }} size={10}>
              <Space wrap>
                <Text>key</Text><Input style={{ width: 190 }} value={mockCase.key} onChange={(e) => update((next) => {
                  const oldKey = next.cases[index].key; const newKey = e.target.value; next.cases[index].key = newKey
                  if (next.forcedCaseKey === oldKey) next.forcedCaseKey = newKey
                  const rename = (selection: SfSelection) => {
                    if (selection.caseKey === oldKey) selection.caseKey = newKey
                    selection.choices = selection.choices?.map((choice) => choice.caseKey === oldKey ? { ...choice, caseKey: newKey } : choice)
                  }
                  rename(next.defaultSelection); next.rules.forEach((rule) => rename(rule.selection))
                })} />
                <Text>名称</Text><Input style={{ width: 180 }} value={mockCase.name} onChange={(e) => update((next) => { next.cases[index].name = e.target.value })} />
                <Text>结果</Text><Select style={{ width: 210 }} value={result.status} options={node.resultSchema.statuses.map((status) => ({ value: status, label: STATUS_LABEL[status] || status }))} onChange={(status) => update((next) => { next.cases[index].result = resetForStatus(result, status); if (status === 'NO_RECEIPT') next.cases[index].delayMs = 0 })} />
                <Text>首步延迟(ms)</Text><InputNumber min={0} max={86_400_000} disabled={result.status === 'NO_RECEIPT'} value={mockCase.delayMs} onChange={(delayMs) => update((next) => { next.cases[index].delayMs = Number(delayMs) || 0 })} />
              </Space>

              {result.type === 'CALL' && result.status === 'CONNECTED' && <Space wrap>
                <Text>终态拨次</Text><InputNumber min={1} max={node.resultSchema.maxAttemptNo || 1} value={result.terminalAttemptNo || 1} onChange={(v) => updateResult({ terminalAttemptNo: Number(v) || 1 })} />
                <Text>前置拨次振铃</Text><Select style={{ width: 210 }} value={result.retryRingStatus || 'no_answer'} options={nonAnsweredRings.map((item) => ({ value: item, label: `${RING_LABEL[item] || item}（${item}）` }))} onChange={(retryRingStatus) => updateResult({ retryRingStatus })} />
                <Text>意向</Text><Select allowClear style={{ width: 100 }} value={result.intention || undefined} options={node.resultSchema.intentions.map((item) => ({ value: item }))} onChange={(intention) => updateResult({ intention: intention || null })} />
                <Text>通话秒数</Text><InputNumber min={0} max={86400} value={result.talkDurationSec ?? 30} onChange={(v) => updateResult({ talkDurationSec: Number(v) || 0 })} />
              </Space>}
              {result.type === 'CALL' && result.status === 'NOT_CONNECTED' && <Space wrap>
                <Text>终态拨次</Text><Tag>{node.resultSchema.maxAttemptNo}（按节点重拨耗尽）</Tag>
                <Text>终态振铃</Text><Select style={{ width: 220 }} value={result.ringStatus || 'no_answer'} options={nonAnsweredRings.map((item) => ({ value: item, label: `${RING_LABEL[item] || item}（${item}）` }))} onChange={(ringStatus) => updateResult({ ringStatus })} />
                <Text>前置拨次振铃</Text><Select style={{ width: 220 }} value={result.retryRingStatus || 'no_answer'} options={nonAnsweredRings.map((item) => ({ value: item, label: `${RING_LABEL[item] || item}（${item}）` }))} onChange={(retryRingStatus) => updateResult({ retryRingStatus })} />
                <Text>失败原因</Text><Input style={{ width: 220 }} value={result.failureReason || ''} onChange={(e) => updateResult({ failureReason: e.target.value || null })} />
              </Space>}
              {result.type === 'CALL' && result.status === 'CANCELLED' && <Space wrap>
                <Text>终态拨次</Text><InputNumber min={1} max={node.resultSchema.maxAttemptNo || 1} value={result.terminalAttemptNo || 1} onChange={(v) => updateResult({ terminalAttemptNo: Number(v) || 1 })} />
                <Text>前置拨次振铃</Text><Select style={{ width: 220 }} value={result.retryRingStatus || 'no_answer'} options={nonAnsweredRings.map((item) => ({ value: item, label: `${RING_LABEL[item] || item}（${item}）` }))} onChange={(retryRingStatus) => updateResult({ retryRingStatus })} />
                <Text>原因</Text><Input style={{ width: 220 }} value={result.failureReason || ''} onChange={(e) => updateResult({ failureReason: e.target.value || null })} />
              </Space>}
              {result.type === 'CALL' && result.status === 'NOT_DIALED' && <Space><Text>原因</Text><Input style={{ width: 260 }} value={result.failureReason || ''} onChange={(e) => updateResult({ failureReason: e.target.value || null })} /></Space>}
              {result.type === 'SMS' && result.status === 'DELIVERED' && <Space><Text>计费条数</Text><InputNumber min={0} max={1000} value={result.partCount ?? 1} onChange={(v) => updateResult({ partCount: Number(v) || 0 })} /></Space>}
              {result.type === 'SMS' && result.status === 'FAILED' && <Space wrap>
                <Text>errorCode</Text><Input style={{ width: 180 }} value={result.errorCode || ''} onChange={(e) => updateResult({ errorCode: e.target.value || null })} />
                <Text>errorDesc</Text><Input style={{ width: 220 }} value={result.errorDesc || ''} onChange={(e) => updateResult({ errorDesc: e.target.value || null })} />
                <Text>计费条数</Text><InputNumber min={0} max={1000} value={result.partCount ?? undefined} onChange={(v) => updateResult({ partCount: v == null ? null : Number(v) })} />
              </Space>}

              <Space><Button size="small" icon={<CopyOutlined />} onClick={() => update((next) => {
                const copied = JSON.parse(JSON.stringify(next.cases[index])) as SfMockCase; copied.key = uniqueKey(next.cases, `${copied.key}_copy`); copied.name += '（复制）'; next.cases.splice(index + 1, 0, copied)
              })}>复制</Button><Button size="small" danger disabled={value.cases.length <= 1} icon={<DeleteOutlined />} onClick={() => deleteCase(index)}>删除</Button></Space>

              {preview ? <Card size="small" type="inner" title="服务端编译预览（本地修改需保存后刷新）">
                <Descriptions size="small" column={3} items={[{ key: 'final', label: '动作终态', children: preview.actionFinal }, { key: 'port', label: '触达节点出口', children: preview.nodePort }, { key: 'vars', label: '预期变量', children: <Text code>{json(preview.expectedVars)}</Text> }]} />
                <Table size="small" rowKey={(_, i) => String(i)} pagination={false} dataSource={preview.steps} columns={[{ title: '偏移(ms)', dataIndex: 'delayMs', width: 90 }, { title: 'status', dataIndex: 'status', width: 150 }, { title: 'failureReason', dataIndex: 'failureReason', width: 180 }, { title: 'data', dataIndex: 'data', render: (data) => <Text code>{json(data)}</Text> }]} />
                {preview.dynamicVars?.length > 0 && <Text type="secondary">运行时动态变量：{preview.dynamicVars.join('、')}</Text>}
              </Card> : <Text type="secondary">新增或修改的 Case 保存后由 Hermes 编译并返回预览。</Text>}
            </Space>,
          }
        })} />
      </Card>

      <Card size="small" title="按名单 bizFields 选择结果" extra={<Button size="small" disabled={value.rules.length >= 100} icon={<PlusOutlined />} onClick={() => update((next) => {
        const first = node.matchSchema.fields[0]
        const type = first?.type || 'string'
        const op = node.matchSchema.operators[type]?.[0] || 'eq'
        const fallback = next.cases[0]?.key; next.rules.push({ name: `规则${next.rules.length + 1}`, priority: 0, conditions: [{ key: first?.key || 'mock_segment', type, itemType: first?.itemType, op, value: initialConditionValue(type, op) }], selection: { mode: 'FIXED', caseKey: fallback } })
      })}>新增规则</Button>}>
        {value.rules.length === 0 ? <Text type="secondary">无规则：全部走上方默认选择。规则在触达结果产生前选择 Case，不是画布 Condition。</Text> : <Collapse items={value.rules.map((rule, ruleIndex) => ({
          // 规则名本身可编辑，不能拿它当 Collapse identity。
          key: `rule-${ruleIndex}`,
          label: <Space><Text strong>{rule.name}</Text><Tag>优先级 {rule.priority}</Tag><Tag>{rule.selection.mode}</Tag></Space>,
          children: <Space direction="vertical" style={{ width: '100%' }}>
            <Space wrap><Text>名称</Text><Input style={{ width: 180 }} value={rule.name} onChange={(e) => update((next) => { next.rules[ruleIndex].name = e.target.value })} /><Text>优先级</Text><InputNumber min={-1_000_000} max={1_000_000} value={rule.priority} onChange={(priority) => update((next) => { next.rules[ruleIndex].priority = Number(priority) || 0 })} /><Button danger size="small" icon={<DeleteOutlined />} onClick={() => update((next) => { next.rules.splice(ruleIndex, 1) })}>删除规则</Button></Space>
            <Text strong>全部条件同时满足（AND）</Text>
            {rule.conditions.map((condition, conditionIndex) => {
              const operators = node.matchSchema.operators[condition.type] || []
              return <Space key={conditionIndex} wrap align="center">
                <AutoComplete style={{ width: 200 }} value={condition.key} options={fieldOptions} onChange={(key) => update((next) => { next.rules[ruleIndex].conditions[conditionIndex].key = key })} onSelect={(key) => update((next) => {
                  const field = node.matchSchema.fields.find((item) => item.key === key); if (!field) return
                  const target = next.rules[ruleIndex].conditions[conditionIndex]
                  const op = node.matchSchema.operators[field.type]?.[0] || 'eq'
                  target.key = field.key; target.type = field.type; target.itemType = field.itemType; target.op = op; target.value = initialConditionValue(field.type, op)
                })} placeholder="字段 key（可手填）" />
                <Select style={{ width: 120 }} value={condition.type} options={Object.keys(node.matchSchema.operators).map((type) => ({ value: type }))} onChange={(type) => update((next) => { const target = next.rules[ruleIndex].conditions[conditionIndex]; const op = node.matchSchema.operators[type]?.[0] || 'eq'; target.type = type; target.itemType = type === 'array' ? 'string' : undefined; target.op = op; target.value = initialConditionValue(type, op) })} />
                {condition.type === 'array' && <Select style={{ width: 110 }} value={condition.itemType || 'string'} options={['string', 'int', 'float', 'bool', 'date', 'datetime', 'time', 'enum'].map((type) => ({ value: type }))} onChange={(itemType) => update((next) => { next.rules[ruleIndex].conditions[conditionIndex].itemType = itemType })} />}
                <Select style={{ width: 150 }} value={condition.op} options={operators.map((op) => ({ value: op }))} onChange={(op) => update((next) => { const target = next.rules[ruleIndex].conditions[conditionIndex]; target.op = op; target.value = initialConditionValue(target.type, op) })} />
                <ConditionValue condition={condition} onChange={(conditionValue) => update((next) => { next.rules[ruleIndex].conditions[conditionIndex].value = conditionValue })} />
                <Button size="small" danger icon={<DeleteOutlined />} disabled={rule.conditions.length <= 1} onClick={() => update((next) => { next.rules[ruleIndex].conditions.splice(conditionIndex, 1) })} />
              </Space>
            })}
            <Button size="small" icon={<PlusOutlined />} disabled={rule.conditions.length >= 10} onClick={() => addCondition(ruleIndex)}>增加 AND 条件</Button>
            <Divider style={{ margin: '6px 0' }} /><Text strong>命中后选择</Text>
            <SelectionEditor value={rule.selection} cases={value.cases} onChange={(selection) => update((next) => { next.rules[ruleIndex].selection = selection })} />
          </Space>,
        }))} />}
      </Card>

      <Space><Button type="primary" disabled={disabled} icon={<SaveOutlined />} onClick={onSave}>保存本节点</Button><Button disabled={disabled} icon={<UndoOutlined />} onClick={onReset}>重置为默认模板</Button><Text type="secondary">选择顺序：强制 Case → 第一条命中规则 → 默认选择。</Text></Space>
    </Space>
  )
}
