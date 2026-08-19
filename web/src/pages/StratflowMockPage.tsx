import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Card, Select, Button, Space, Table, Tag, Typography, InputNumber, Input, Switch,
  message, Modal, Alert, Collapse, Descriptions, Empty, Divider, Upload, Radio,
} from 'antd'
import { ReloadOutlined, ThunderboltOutlined, ClearOutlined, PlusOutlined, UploadOutlined } from '@ant-design/icons'
import {
  sfGate, sfSetGlobalGate, sfSetSchemeGate, sfClearSchemeGate, sfSetDeliveryPaused, sfSetReceiptWindow,
  sfListConfig, sfPutConfig, sfDeleteConfig, sfClearMock, sfListPlans, sfListDecisions, sfRequeuePlan,
  sfWorkflows, sfWorkflowDetail, sfCollections, sfCollectionFields, sfCollectionBindings, sfExecutionProgress, sfImport,
  CURRENT_ORG_STORAGE_KEY, ApiRequestError,
} from '../api'
import type {
  SfGateView, SfNode, SfNodeConfig, SfWorkflow, SfWorkflowDetail,
  SfCollection, SfField, SfActionPlan, SfDecision, SfImportFailureData, SfImportResult, SfImportRowError,
  SfRunProgress, SfRunNode, SfBinding, SfImportRow, SfDispatchMode,
} from '../types'
import { SF_IMPORT_RUN_CREATED, SF_IMPORT_FIELD_FAIL } from '../types'
import { PageHeader } from '../components/layout/PageHeader'
import { InfoBanner } from '../components/layout/InfoBanner'
import { usePolling } from '../hooks/usePolling'
import { ORG_CHANGED_EVENT } from '../components/layout/useCurrentOrg'
import { StratflowMockNodeEditor } from '../components/StratflowMockNodeEditor'
import {
  composeStratflowImportRows, generateSequentialPhones, MAX_STRATFLOW_IMPORT_ROWS, parseStratflowImportCsv, splitStratflowPhones,
  type SfImportComposeResult,
} from './stratflow-import'
import { importErrorDetailState, selectImportedRun } from './stratflow-import-result'

const { Text, Paragraph } = Typography

const SF_IMPORT_REQUEST_MESSAGES: Record<number, string> = {
  42002: '集合已禁用，请先启用后再导入',
  42003: '集合尚未配置字段',
  42004: '集合暂无可用绑定方案',
  42006: '导入名单不能为空',
  42008: '没有可运行方案',
  42011: '名单没有任何合法行',
  42012: '名单行数超过单次导入上限',
  42013: 'idempotencyKey 长度超过上限',
  42014: '可运行方案数超过单批上限',
  42015: '集合不存在或当前机构无权访问',
  42016: '集合字段配置无效，请先修正字段配置',
}

type SfImportRequestFailureView = {
  code?: number
  message: string
  data?: SfImportFailureData
}

function isSfImportFailureData(value: unknown): value is SfImportFailureData {
  if (!value || typeof value !== 'object') return false
  const data = value as Partial<SfImportFailureData>
  return typeof data.total === 'number' && typeof data.success === 'number'
    && typeof data.fail === 'number' && Array.isArray(data.errors)
    && typeof data.errorsTruncated === 'boolean'
}

function formatImportRowError(error: SfImportRowError): string {
  return error.errors.map((field) => {
    const location = `${field.fieldKey}${field.itemIndex == null ? '' : `[${field.itemIndex}]`}`
    const params = [
      field.maxLength == null ? '' : `maxLength=${field.maxLength}`,
      field.actualLength == null ? '' : `actualLength=${field.actualLength}`,
      field.maxScale == null ? '' : `maxScale=${field.maxScale}`,
      field.actualScale == null ? '' : `actualScale=${field.actualScale}`,
    ].filter(Boolean)
    return `${location}: ${field.reason}${params.length ? ` (${params.join(', ')})` : ''}`
  }).join('；')
}

function importRequestFailureView(error: unknown): SfImportRequestFailureView {
  if (error instanceof ApiRequestError) {
    const code = error.upstreamCode
    return {
      code,
      message: code == null ? error.message : (SF_IMPORT_REQUEST_MESSAGES[code] || error.message),
      data: code === 42011 && isSfImportFailureData(error.upstreamData) ? error.upstreamData : undefined,
    }
  }
  return { message: error instanceof Error ? error.message : String(error) }
}

// 把 Date 格式化成 UTC "yyyy-MM-dd HH:mm:ss"（run 进度接口窗口参数用）。
function fmtUTC(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}`
}
function defaultWindow(): [string, string] {
  const now = Date.now()
  return [fmtUTC(new Date(now - 24 * 3600e3)), fmtUTC(new Date(now + 24 * 3600e3))]
}

const DISPATCH_MODE_OPTIONS: { value: SfDispatchMode; label: string }[] = [
  { value: 'PAUSED', label: 'PAUSED · 暂停新派发' },
  { value: 'MOCK', label: 'MOCK · 合成回执' },
  { value: 'REAL', label: 'REAL · 真实下游' },
]

const SELECTION_MODE_LABEL: Record<string, string> = {
  FORCED: '强制 Case',
  RULE_FIXED: '规则固定',
  RULE_WEIGHTED: '规则概率',
  DEFAULT_FIXED: '默认固定',
  DEFAULT_WEIGHTED: '默认概率',
}

const isDispatchMode = (value: unknown): value is SfDispatchMode => value === 'REAL' || value === 'MOCK' || value === 'PAUSED'

// 策略流应用层 mock 编排台：发现方案/版本/名单 → 配触达节点结局 → 导名单触发 run → 观测计划 + 断言分支落点。
// 边界：这是「应用层 mock（stratflow 合成回执）」的编排，与 SIP 被叫腿正交——测策略图分支时**不产真实 SIP**。
export default function StratflowMockPage() {
  const [gate, setGate] = useState<SfGateView | null>(null)
  const [gateErr, setGateErr] = useState('')

  const [workflows, setWorkflows] = useState<SfWorkflow[]>([])
  const [defCode, setDefCode] = useState<string>()
  const [detail, setDetail] = useState<SfWorkflowDetail | null>(null)

  const [nodes, setNodes] = useState<SfNode[]>([])
  const [edits, setEdits] = useState<Record<string, SfNodeConfig>>({})
  const [configErr, setConfigErr] = useState('')

  const [collections, setCollections] = useState<SfCollection[]>([])
  const [collCode, setCollCode] = useState<string>()
  const [fields, setFields] = useState<SfField[]>([])
  const [bindings, setBindings] = useState<SfBinding[]>([])

  const [phonesText, setPhonesText] = useState('')
  const [bizText, setBizText] = useState('')
  const [idemKey, setIdemKey] = useState('')
  const [importing, setImporting] = useState(false)
  const [importRes, setImportRes] = useState<SfImportResult | null>(null)
  const [importRequestFailure, setImportRequestFailure] = useState<SfImportRequestFailureView | null>(null)
  const [importPreview, setImportPreview] = useState<SfImportComposeResult | null>(null)
  const [csvFileName, setCsvFileName] = useState('')
  const [phoneGeneratorOpen, setPhoneGeneratorOpen] = useState(false)
  const [phoneGeneratorStart, setPhoneGeneratorStart] = useState('13800138000')
  const [phoneGeneratorCount, setPhoneGeneratorCount] = useState(100)
  const [phoneGeneratorMode, setPhoneGeneratorMode] = useState<'overwrite' | 'append'>('overwrite')

  const [runCode, setRunCode] = useState('')
  const [plans, setPlans] = useState<SfActionPlan[]>([])
  const [decisions, setDecisions] = useState<SfDecision[]>([])
  const [decisionTotal, setDecisionTotal] = useState(0)
  const [decisionPage, setDecisionPage] = useState(1)
  const [plansErr, setPlansErr] = useState('')
  const [progress, setProgress] = useState<SfRunProgress | null>(null)
  const [timeWin, setTimeWin] = useState<[string, string]>(defaultWindow())
  const [autoObserve, setAutoObserve] = useState(false)
  const observingRef = useRef(false)
  /** 各选择上下文独立代次；旧异步响应只能完成网络请求，禁止覆盖新上下文。 */
  const orgGenerationRef = useRef(0)
  const workflowGenerationRef = useRef(0)
  const collectionGenerationRef = useRef(0)
  const observeGenerationRef = useRef(0)
  const versionCode = detail?.versionCode
  const master = gate?.master ?? false
  const globalMode: SfDispatchMode = isDispatchMode(gate?.mode) ? gate.mode : (gate?.global ? 'MOCK' : 'REAL')
  // 回执窗压缩(秒)本地态：从 gate 同步，避免逐键触发接口/中途校验；点「应用」才提交。
  const [winSec, setWinSec] = useState(0)
  useEffect(() => { setWinSec(gate?.receiptWindowSec ?? 0) }, [gate?.receiptWindowSec])

  // —— gate ——
  const loadGate = useCallback(async () => {
    const generation = orgGenerationRef.current
    try {
      const next = await sfGate()
      if (generation !== orgGenerationRef.current) return
      setGate(next); setGateErr('')
    } catch (e) {
      if (generation !== orgGenerationRef.current) return
      setGateErr(String(e)); setGate(null)
    }
  }, [])

  const loadWorkflows = useCallback(async () => {
    const generation = orgGenerationRef.current
    try {
      const next = (await sfWorkflows()).workflows || []
      if (generation === orgGenerationRef.current) setWorkflows(next)
    } catch (e) { if (generation === orgGenerationRef.current) message.error(String(e)) }
  }, [])

  const applyGate = async (operation: () => Promise<SfGateView>) => {
    const generation = orgGenerationRef.current
    try {
      const next = await operation()
      if (generation === orgGenerationRef.current) setGate(next)
    } catch (e) { if (generation === orgGenerationRef.current) message.error(String(e)) }
  }

  useEffect(() => { void loadGate(); void loadWorkflows() }, [loadGate, loadWorkflows])

  // —— 选方案 → 拿 versionCode + 配置 ——
  const pickWorkflow = async (dc: string) => {
    const generation = ++workflowGenerationRef.current
    setDefCode(dc); setDetail(null); setNodes([]); setEdits({}); setConfigErr('')
    try {
      const d = await sfWorkflowDetail(dc)
      if (generation !== workflowGenerationRef.current) return
      setDetail(d)
      if (d.versionCode) await loadConfig(d.versionCode, generation)
    } catch (e) { message.error(String(e)) }
  }

  const loadConfig = async (ver: string, generation = workflowGenerationRef.current) => {
    try {
      const list = (await sfListConfig(ver)).nodes || []
      if (generation !== workflowGenerationRef.current) return
      const incompatible = list.find((n) => !Array.isArray(n.config?.cases) || !n.config.defaultSelection
        || !Array.isArray(n.config.rules) || !Array.isArray(n.resultSchema?.statuses)
        || !Array.isArray(n.resultSchema.ringStatuses) || !Array.isArray(n.resultSchema.intentions)
        || !Array.isArray(n.matchSchema?.fields) || !n.matchSchema.operators || !n.previews)
      if (incompatible) {
        throw new Error(`Hermes StratFlow Mock 配置协议不兼容：节点 ${incompatible.nodeId} 未返回类型化 cases/defaultSelection/schema/previews；请先部署配套 Hermes 后端并清理旧 sf:mock:cfg:* 配置`)
      }
      setNodes(list)
      setConfigErr('')
      const init: Record<string, SfNodeConfig> = {}
      list.forEach((n) => {
        init[n.nodeId] = JSON.parse(JSON.stringify(n.config)) as SfNodeConfig
      })
      setEdits(init)
    } catch (e) {
      if (generation !== workflowGenerationRef.current) return
      const error = String(e)
      setNodes([]); setEdits({}); setConfigErr(error); message.error(error)
    }
  }

  const saveNode = async (n: SfNode) => {
    if (!versionCode) return
    const orgGeneration = orgGenerationRef.current
    const workflowGeneration = workflowGenerationRef.current
    const ed = edits[n.nodeId]
    if (!ed) return
    try {
      await sfPutConfig(versionCode, n.nodeId, ed)
      if (orgGeneration !== orgGenerationRef.current || workflowGeneration !== workflowGenerationRef.current) return
      message.success(`已存 ${n.nodeId}`)
      await loadConfig(versionCode, workflowGeneration)
    } catch (e) {
      if (orgGeneration === orgGenerationRef.current && workflowGeneration === workflowGenerationRef.current) message.error(String(e))
    }
  }

  const resetNode = async (n: SfNode) => {
    if (!versionCode) return
    const orgGeneration = orgGenerationRef.current
    const workflowGeneration = workflowGenerationRef.current
    try {
      await sfDeleteConfig(versionCode, n.nodeId)
      if (orgGeneration !== orgGenerationRef.current || workflowGeneration !== workflowGenerationRef.current) return
      message.success(`已重置 ${n.nodeId}`)
      await loadConfig(versionCode, workflowGeneration)
    } catch (e) {
      if (orgGeneration === orgGenerationRef.current && workflowGeneration === workflowGenerationRef.current) message.error(String(e))
    }
  }

  const setNodeEdit = (nodeId: string, config: SfNodeConfig) =>
    setEdits((prev) => ({ ...prev, [nodeId]: config }))

  // —— 名单 ——
  const loadCollections = useCallback(async () => {
    const generation = orgGenerationRef.current
    try {
      const next = (await sfCollections()).collections || []
      if (generation === orgGenerationRef.current) setCollections(next)
    } catch (e) { if (generation === orgGenerationRef.current) message.error(String(e)) }
  }, [])
  useEffect(() => { void loadCollections() }, [loadCollections])

  useEffect(() => {
    const onOrgChanged = () => {
      orgGenerationRef.current++
      workflowGenerationRef.current++
      collectionGenerationRef.current++
      observeGenerationRef.current++
      observingRef.current = false
      setGate(null); setGateErr(''); setWorkflows([]); setDefCode(undefined); setDetail(null)
      setNodes([]); setEdits({}); setConfigErr(''); setCollections([]); setCollCode(undefined); setFields([]); setBindings([])
      setPhonesText(''); setBizText(''); setIdemKey(''); setImportRes(null); setImportRequestFailure(null); setImportPreview(null); setCsvFileName(''); setImporting(false)
      setPhoneGeneratorOpen(false)
      setRunCode(''); setPlans([]); setDecisions([]); setDecisionTotal(0); setDecisionPage(1); setPlansErr(''); setProgress(null); setAutoObserve(false)
      Modal.destroyAll()
      void loadGate(); void loadWorkflows(); void loadCollections()
    }
    const onStorage = (event: StorageEvent) => { if (event.key === CURRENT_ORG_STORAGE_KEY) onOrgChanged() }
    window.addEventListener(ORG_CHANGED_EVENT, onOrgChanged)
    window.addEventListener('storage', onStorage)
    return () => {
      window.removeEventListener(ORG_CHANGED_EVENT, onOrgChanged)
      window.removeEventListener('storage', onStorage)
    }
  }, [loadCollections, loadGate, loadWorkflows])

  const pickCollection = async (code: string) => {
    const generation = ++collectionGenerationRef.current
    observeGenerationRef.current++; observingRef.current = false
    setCollCode(code); setFields([]); setBindings([]); setImportPreview(null); setCsvFileName(''); setImportRes(null); setImportRequestFailure(null)
    setRunCode(''); setPlans([]); setDecisions([]); setDecisionTotal(0); setDecisionPage(1); setPlansErr(''); setProgress(null)
    try {
      const [f, b] = await Promise.all([sfCollectionFields(code), sfCollectionBindings(code)])
      if (generation !== collectionGenerationRef.current) return
      setFields(f.fields || [])
      setBindings(b.bindings || [])
    } catch (e) { message.error(String(e)) }
  }

  // 新派发模式：三态方案覆盖优先；兼容旧后端 bool view；无覆盖回落当前机构 global。
  const schemeModeOf = (dc: string): SfDispatchMode => {
    const mode = gate?.schemeModes?.[dc]
    if (isDispatchMode(mode)) return mode
    if (gate?.schemes && Object.prototype.hasOwnProperty.call(gate.schemes, dc)) return gate.schemes[dc] ? 'MOCK' : 'REAL'
    return globalMode
  }
  // —— 触发 import ——
  // 导入前护栏：只读现有前端状态，列出会让"分支断言失真 / 根本没走 mock"的风险，交用户显式确认。
  // 纯前端，不改任何 stratflow 行为；采样在派发那刻定型 → 配置/门必须先于导入，故在此拦一道。
  const importWarnings = (): string[] => {
    const w: string[] = []
    if (bindings.length === 0) {
      w.push('该名单当前无绑定方案：导入不会生成任何 run（result=无可用绑定）。')
      return w
    }
    const boundDefs = bindings.map((b) => b.defCode)
    if (defCode && !boundDefs.includes(defCode)) {
      w.push(`你在本页配置的是方案 ${defCode}，但该名单绑定的是 ${boundDefs.join('、')}：你的结局配置不会作用到本次导入。`)
    }
    boundDefs.forEach((dc) => {
      const mode = schemeModeOf(dc)
      if (!master) {
        w.push(`方案 ${dc} 所在环境未加载 Mock capability：本次导入会走真实下游。`)
      } else if (mode === 'REAL') {
        w.push(`方案 ${dc} 当前为 REAL：本次导入会调用真实下游。`)
      } else if (mode === 'PAUSED') {
        w.push(`方案 ${dc} 当前为 PAUSED：新动作不会真实发送也不会生成 Mock 计划，run 会停在待派发。`)
      }
    })
    if (defCode && boundDefs.includes(defCode) && nodes.length > 0) {
      const probabilistic = nodes.filter((n) => {
        const config = edits[n.nodeId]
        return config && Array.isArray(config.cases) && !!config.defaultSelection && Array.isArray(config.rules)
          && !config.forcedCaseKey && (config.defaultSelection.mode === 'WEIGHTED' || config.rules.some((rule) => rule.selection.mode === 'WEIGHTED'))
      }).map((n) => n.nodeId)
      if (probabilistic.length > 0) {
        w.push(`方案 ${defCode} 的节点 ${probabilistic.join('、')} 含概率选择且未强制 Case：每条名单会在派发时独立抽取结果。需要完全确定的断言时请临时设置强制 Case。`)
      }
    }
    return w
  }

  const previewImportRows = (nextBizText = bizText, syncPhones = true) => {
    const result = composeStratflowImportRows(phonesText, nextBizText, fields)
    setImportPreview(result)
    setCsvFileName('')
    setImportRes(null)
    setImportRequestFailure(null)
    if (result.blockingErrorCount > 0) {
      message.error(`结构解析失败，共 ${result.blockingErrorCount} 个问题`)
      return result
    }
    if (syncPhones && result.mode === 'ROWS') setPhonesText(result.rows.map((row) => row.phone).join('\n'))
    if (result.semanticWarningCount > 0) {
      message.warning(`已解析 ${result.rows.length} 条名单；${result.semanticWarningCount} 个语义问题将由 Hermes 最终校验`)
    } else {
      message.success(`已解析 ${result.rows.length} 条名单`)
    }
    return result
  }

  const importCsvFile = async (file: File) => {
    const orgGeneration = orgGenerationRef.current
    const collectionGeneration = collectionGenerationRef.current
    if (file.size > 10 * 1024 * 1024) {
      message.error('CSV 文件不能超过 10 MB')
      return
    }
    try {
      const content = await file.text()
      if (orgGeneration !== orgGenerationRef.current || collectionGeneration !== collectionGenerationRef.current) return
      const result = parseStratflowImportCsv(content, fields)
      setImportPreview(result)
      setCsvFileName(file.name)
      setImportRes(null)
      setImportRequestFailure(null)
      if (result.blockingErrorCount > 0) {
        message.error(`CSV 结构解析失败，共 ${result.blockingErrorCount} 个问题`)
        return
      }
      setPhonesText(result.rows.map((row) => row.phone).join('\n'))
      setBizText('')
      if (result.semanticWarningCount > 0) {
        message.warning(`已从 ${file.name} 解析 ${result.rows.length} 条名单；${result.semanticWarningCount} 个语义问题将由 Hermes 最终校验`)
      } else {
        message.success(`已从 ${file.name} 解析 ${result.rows.length} 条名单`)
      }
    } catch (e) {
      if (orgGeneration === orgGenerationRef.current && collectionGeneration === collectionGenerationRef.current) {
        message.error(`读取 CSV 文件失败：${String(e)}`)
      }
    }
  }

  const applyPhoneGeneration = () => {
    try {
      const generated = generateSequentialPhones(phoneGeneratorStart, phoneGeneratorCount)
      const existing = splitStratflowPhones(phonesText)
      const next = phoneGeneratorMode === 'append' ? [...existing, ...generated] : generated
      if (next.length > MAX_STRATFLOW_IMPORT_ROWS) {
        message.error(`号码总数不能超过 ${MAX_STRATFLOW_IMPORT_ROWS}`)
        return
      }
      setPhonesText(next.join('\n'))
      setImportPreview(null)
      setCsvFileName('')
      setImportRes(null)
      setImportRequestFailure(null)
      setPhoneGeneratorOpen(false)
      message.success(`${phoneGeneratorMode === 'append' ? '已追加' : '已生成'} ${generated.length} 个号码`)
    } catch (e) {
      message.error(e instanceof Error ? e.message : String(e))
    }
  }

  const doImport = () => {
    if (!collCode) { message.warning('先选名单集合'); return }
    const composed = importPreview?.mode === 'CSV'
      ? importPreview
      : composeStratflowImportRows(phonesText, bizText, fields)
    setImportPreview(composed)
    if (composed.blockingErrorCount > 0) {
      message.error(`结构解析失败，共 ${composed.blockingErrorCount} 个问题`)
      return
    }
    const code = collCode
    const rows: SfImportRow[] = composed.rows
    const warns = importWarnings()
    if (warns.length === 0) { void runImport(code, rows); return }
    const orgGeneration = orgGenerationRef.current
    const collectionGeneration = collectionGenerationRef.current
    Modal.confirm({
      title: '导入前确认：以下情况可能让分支断言失真',
      width: 560,
      content: (
        <ul style={{ paddingLeft: 18, marginBottom: 0 }}>
          {warns.map((t, i) => <li key={i} style={{ marginBottom: 6 }}>{t}</li>)}
        </ul>
      ),
      okText: '仍然导入',
      okButtonProps: { danger: true },
      cancelText: '返回修正',
      onOk: () => {
        if (orgGeneration !== orgGenerationRef.current || collectionGeneration !== collectionGenerationRef.current) {
          message.error('机构或名单集合已变化，本次导入已取消')
          return
        }
        return runImport(code, rows)
      },
    })
  }

  const runImport = async (code: string, rows: SfImportRow[]) => {
    const orgGeneration = orgGenerationRef.current
    const collectionGeneration = collectionGenerationRef.current
    setImporting(true)
    setImportRes(null)
    setImportRequestFailure(null)
    try {
      const res = await sfImport(code, {
        idempotencyKey: idemKey || undefined,
        rows,
      })
      if (orgGeneration !== orgGenerationRef.current || collectionGeneration !== collectionGenerationRef.current) return
      setImportRes(res)
      setImportRequestFailure(null)
      // 多绑定名单会为每个绑定方案各返回一条 plan：优先取"本页选中并配置的方案 defCode"那条 run，
      // 否则回退首条成功——否则可能观测到别的方案的 run，令你为选中方案配的强制结局看似不生效。
      const ok = selectImportedRun(res, defCode)
      const errorState = importErrorDetailState(res)
      if (ok) {
        observeGenerationRef.current++; observingRef.current = false
        setRunCode(ok.runCode)
        setPlans([]); setDecisions([]); setDecisionTotal(0); setDecisionPage(1); setPlansErr(''); setProgress(null)
        if (errorState.kind === 'not-retained') {
          message.warning(`命中原幂等批次，已取得 run ${ok.runCode}；失败原因未保留`)
        } else if (res.fail > 0) {
          message.warning(`部分导入成功：成功 ${res.success} 行、失败 ${res.fail} 行；已取得 run ${ok.runCode}`)
        } else {
          message.success(`已生成 run ${ok.runCode}`)
        }
        void observe(ok.runCode, 1)
      } else {
        const fail = (res.plans || [])[0]
        message.warning(fail?.result === SF_IMPORT_FIELD_FAIL ? `字段契约失败：${(fail.failFields || []).join(', ')}` : '无可用绑定/未生成 run，看导入结果')
      }
    } catch (e) {
      if (orgGeneration === orgGenerationRef.current && collectionGeneration === collectionGenerationRef.current) {
        const failure = importRequestFailureView(e)
        setImportRequestFailure(failure)
        message.error(failure.message)
      }
    } finally {
      if (orgGeneration === orgGenerationRef.current && collectionGeneration === collectionGenerationRef.current) setImporting(false)
    }
  }

  // —— 观测 ——
  const observe = useCallback(async (rc?: string, pageNo = decisionPage) => {
    const code = rc || runCode
    if (!code || !collCode || observingRef.current) return
    const generation = ++observeGenerationRef.current
    observingRef.current = true
    try {
      await Promise.allSettled([
        Promise.all([sfListPlans(code, 'PENDING'), sfListPlans(code, 'DEAD')])
          .then(([pending, dead]) => {
            if (generation !== observeGenerationRef.current) return
            const byAction = new Map<string, SfActionPlan>()
            const merged = [...(pending.plans || []), ...(dead.plans || [])]
            merged.forEach((p) => byAction.set(p.actionCode, p))
            setPlans([...byAction.values()]); setPlansErr('')
          })
          .catch((e) => {
            if (generation !== observeGenerationRef.current) return
            setPlans([]); setPlansErr(`计划查询失败，进度数据仍有效：${String(e)}`)
          }),
        sfListDecisions(code, pageNo, 100)
          .then((page) => {
            if (generation !== observeGenerationRef.current) return
            setDecisions(page.records || []); setDecisionTotal(page.total || 0)
          })
          .catch((e) => {
            if (generation !== observeGenerationRef.current) return
            setDecisions([]); setDecisionTotal(0); message.error(`决策记录查询失败：${String(e)}`)
          }),
        sfExecutionProgress(collCode, code, timeWin[0], timeWin[1])
          .then((next) => { if (generation === observeGenerationRef.current) setProgress(next) })
          .catch((e) => {
            if (generation !== observeGenerationRef.current) return
            setProgress(null); message.error(`进度查询失败：${String(e)}`)
          }),
      ])
    } finally {
      if (generation === observeGenerationRef.current) observingRef.current = false
    }
  }, [runCode, collCode, timeWin, decisionPage])

  usePolling(() => { if (autoObserve && runCode) void observe() }, 3000, { immediate: false })
  useEffect(() => { if (runCode && collCode) void observe() }, [decisionPage])

  const executeClearMock = async (scope: 'all' | 'plans' | 'config') => {
    const orgGeneration = orgGenerationRef.current
    const workflowGeneration = workflowGenerationRef.current
    try {
      await sfClearMock(scope)
      if (orgGeneration !== orgGenerationRef.current || workflowGeneration !== workflowGenerationRef.current) return
      message.success(`已清空（${scope}）`)
      if (scope !== 'config') { setPlans([]); setDecisions([]); setDecisionTotal(0); setDecisionPage(1); setProgress(null) }
      if (versionCode && scope !== 'plans') await loadConfig(versionCode, workflowGeneration)
    } catch (e) {
      if (orgGeneration === orgGenerationRef.current && workflowGeneration === workflowGenerationRef.current) message.error(String(e))
    }
  }

  const clearMock = (scope: 'all' | 'plans' | 'config') => {
    const orgGeneration = orgGenerationRef.current
    if (scope === 'config') { void executeClearMock(scope); return }
    Modal.confirm({
      title: '确认清理在途 Mock 计划？',
      content: '将删除当前机构的 PENDING/DEAD 回放计划以及 DONE 决策历史；已派发动作若仍在途将不再收到合成回执，只能等待回执超时。该操作可能令正在执行的 run 变慢或走失败分支。',
      okText: '确认清理',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: () => {
        if (orgGeneration !== orgGenerationRef.current) {
          message.error('机构已变化，本次清场已取消')
          return
        }
        return executeClearMock(scope)
      },
    })
  }

  const requeuePlan = async (plan: SfActionPlan) => {
    const orgGeneration = orgGenerationRef.current
    const observeGeneration = observeGenerationRef.current
    const selectedRun = runCode
    if (plan.runCode !== runCode) {
      message.error('页面上下文已变化，请刷新计划后再重新入队')
      return
    }
    try {
      await sfRequeuePlan(plan.actionCode)
      if (
        orgGeneration !== orgGenerationRef.current ||
        observeGeneration !== observeGenerationRef.current ||
        plan.runCode !== selectedRun
      ) return
      message.success('DEAD 计划已重新入队')
      observeGenerationRef.current++; observingRef.current = false
      await observe()
    } catch (e) { message.error(String(e)) }
  }

  const rawSchemeState = defCode ? gate?.schemeModes?.[defCode] : undefined
  const schemeState: SfDispatchMode | undefined = isDispatchMode(rawSchemeState)
    ? rawSchemeState
    : defCode && gate?.schemes && Object.prototype.hasOwnProperty.call(gate.schemes, defCode)
      ? (gate.schemes[defCode] ? 'MOCK' : 'REAL')
      : undefined
  const requiredFields = useMemo(() => fields.filter((f) => f.required), [fields])
  const phoneCount = useMemo(() => splitStratflowPhones(phonesText).length, [phonesText])
  const importErrorSummary = importRes && importRes.fail > 0
    ? importRes
    : importRequestFailure?.data && importRequestFailure.data.fail > 0
      ? importRequestFailure.data
      : null
  const importErrorState = importErrorSummary ? importErrorDetailState(importErrorSummary) : null

  return (
    <div className="page-container">
      <PageHeader
        title="策略流 Mock 编排"
        status={master ? { tone: globalMode === 'MOCK' ? 'success' : globalMode === 'PAUSED' ? 'warning' : 'neutral', text: `Mock capability 可用 · 当前机构 ${globalMode}` } : { tone: 'danger', text: '本环境不可 mock' }}
        onReload={() => { void loadGate(); void loadWorkflows() }}
      />
      <InfoBanner title="应用层 mock 编排（与 SIP 被叫腿正交）">
        这里驱动 <Text code>hermes-stratflow</Text> 的应用层 mock：派发那刻按 Case 选择规则采样 → 合成回执事件，<Text strong>不打真实电话、不经被叫腿</Text>。
        用于测策略图分支/回执逻辑。顺序：选方案(拿 versionCode) → 开方案门闸 → 配结局 → 选名单导入触发 run → 观测计划 + 按 edgeFlow 断言分支。
      </InfoBanner>

      {gateErr && <Alert type="error" showIcon style={{ marginBottom: 12 }} message="读取 gate 失败" description={gateErr} />}
      {gate && !master && (
        <Alert type="warning" showIcon style={{ marginBottom: 12 }}
          message="本环境未启用 Mock capability"
          description="只有 local/test profile 且 stratflow.mock-capability.enabled=true 才加载控制面；其它环境恒 REAL。" />
      )}

      {/* ① gate 开关 */}
      <Card title="① 运行期开关" size="small" style={{ marginBottom: 12 }}>
        <Space size="large" wrap>
          <Space>
            <Text>当前机构新派发模式：</Text>
            <Select<SfDispatchMode> disabled={!master} style={{ width: 190 }} value={globalMode} options={DISPATCH_MODE_OPTIONS}
              onChange={(v) => void applyGate(() => sfSetGlobalGate(v))} />
          </Space>
          <Space>
            <Text>当前机构回放暂停（step-through）：</Text>
            <Switch disabled={!master} checked={!!gate?.deliveryPaused}
              onChange={(v) => void applyGate(() => sfSetDeliveryPaused(v))} />
          </Space>
          <Space>
            <Text>回执窗压缩(秒)：</Text>
            <InputNumber min={0} step={30} style={{ width: 110 }} disabled={!master} value={winSec}
              onChange={(v) => setWinSec(Number(v) || 0)} />
            <Button size="small" disabled={!master}
              onClick={() => void applyGate(async () => {
                const next = await sfSetReceiptWindow(winSec)
                message.success(winSec > 0 ? `回执窗压到 ${winSec}s（仅 mock 派发）` : '已恢复真实回执窗')
                return next
              })}>应用</Button>
          </Space>
          <Text type="secondary">未配置或 Redis 读取异常时默认 PAUSED，避免误触真实下游。切换只影响尚未固化模式的新派发；在途 REAL/MOCK 重试继续沿用原模式。</Text>
        </Space>
      </Card>

      {/* ② 方案 + versionCode + 方案门闸 */}
      <Card title="② 选方案（gate 按 defCode，config 按 versionCode）" size="small" style={{ marginBottom: 12 }}>
        <Space wrap align="center">
          <Select style={{ width: 320 }} placeholder="选择授权方案" value={defCode} onChange={pickWorkflow}
            options={workflows.map((w) => ({ value: w.defCode, label: `${w.name}（${w.defCode}）${w.orgEnabled ? '' : ' [未启用]'}` }))} />
          {detail && <Tag color="blue">versionCode: {detail.versionCode || '—（无启用发布版本）'}</Tag>}
          {defCode && (
            <>
              <Text>本方案新派发模式：</Text>
              <Select<SfDispatchMode> disabled={!master} style={{ width: 190 }} value={schemeState ?? globalMode} options={DISPATCH_MODE_OPTIONS}
                onChange={(v) => void applyGate(() => sfSetSchemeGate(defCode, v))} />
              {schemeState !== undefined && (
                <Button size="small" disabled={!master}
                  onClick={() => void applyGate(() => sfClearSchemeGate(defCode))}>清方案覆盖(回落当前机构全局)</Button>
              )}
            </>
          )}
        </Space>
      </Card>

      {/* ③ 结局配置 */}
      {versionCode && (
        <Card title="③ 触达节点结局配置" size="small" style={{ marginBottom: 12 }}
          extra={<Button size="small" icon={<ReloadOutlined />} onClick={() => loadConfig(versionCode)}>刷新</Button>}>
          {configErr ? <Alert type="error" showIcon message="触达节点配置加载失败" description={configErr} /> : nodes.length === 0 ? <Empty description="该版本无触达节点（VOICEBOT_CALL/SMS_SEND）" /> : (
            <Collapse items={nodes.map((n) => {
              const ed = edits[n.nodeId]
              return {
                key: n.nodeId,
                label: <Space><Text strong>{n.nodeId}</Text><Tag>{n.type}</Tag>{n.channel && <Tag color="geekblue">{n.channel}</Tag>}{n.configured ? <Tag color="green">已自定义</Tag> : <Tag>默认模板</Tag>}{ed?.forcedCaseKey && <Tag color="orange">强制 {ed.forcedCaseKey}</Tag>}</Space>,
                children: ed ? <StratflowMockNodeEditor node={n} value={ed} disabled={!master} onChange={(config) => setNodeEdit(n.nodeId, config)} onSave={() => void saveNode(n)} onReset={() => void resetNode(n)} /> : null,
              }
            })} />
          )}
        </Card>
      )}

      {/* ④ 名单 + 触发 */}
      <Card title="④ 选名单导入触发 run" size="small" style={{ marginBottom: 12 }}>
        <Space direction="vertical" style={{ width: '100%' }}>
          <Space wrap align="center">
            <Select style={{ width: 320 }} placeholder="选择名单集合" value={collCode} onChange={pickCollection}
              options={collections.map((c) => ({ value: c.code, label: `${c.name}（${c.code}）· ${c.entryCount}条 · 绑定${c.boundPlanCount}方案` }))} />
            <Button size="small" icon={<ReloadOutlined />} onClick={loadCollections}>刷新名单</Button>
          </Space>
          {collCode && (
            <>
              {requiredFields.length > 0 && (
                <Alert type="info" showIcon message={<span>必填业务字段：{requiredFields.map((f) => <Tag key={f.key}>{f.displayName}（{f.key}）</Tag>)} —— 缺失行会由 Hermes 标记为失败，不影响其它合法行。</span>} />
              )}
              <Text type="secondary" style={{ fontSize: 12 }}>
                绑定方案：{bindings.length === 0 ? '无（导入不会生成 run）' : bindings.map((b) => (
                  <Tag key={b.defCode} color={schemeModeOf(b.defCode) === 'MOCK' ? 'green' : schemeModeOf(b.defCode) === 'PAUSED' ? 'gold' : 'default'}>
                    {(b.defName || b.defCode)} · {schemeModeOf(b.defCode)}
                  </Tag>
                ))}
              </Text>
              {fields.length > 0 && (
                <Space size={[4, 6]} wrap>
                  <Text type="secondary" style={{ fontSize: 12 }}>集合字段：</Text>
                  {fields.map((field) => (
                    <Tag key={field.key} color={field.required ? 'red' : undefined}>
                      {field.displayName} · {field.key}:{field.dataType === 'array' ? `array<${field.itemType || '?'}>` : field.dataType}{field.required ? ' *' : ''}
                    </Tag>
                  ))}
                </Space>
              )}
              <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'flex-start' }}>
                <div style={{ width: 360, maxWidth: '100%' }}>
                  <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 4 }}>
                    <Text type="secondary">号码（每行一个） · 已填 {phoneCount} 条</Text>
                    <Space size={6}>
                      <Button size="small" icon={<PlusOutlined />} onClick={() => setPhoneGeneratorOpen(true)}>批量生成</Button>
                      <Upload accept=".csv,text/csv" showUploadList={false} beforeUpload={(file) => {
                        void importCsvFile(file)
                        return false
                      }}>
                        <Button size="small" icon={<UploadOutlined />}>导入 CSV</Button>
                      </Upload>
                    </Space>
                  </Space>
                  <Input.TextArea rows={8} style={{ width: '100%' }} value={phonesText} onChange={(e) => {
                    setPhonesText(e.target.value); setImportPreview(null); setCsvFileName(''); setImportRes(null); setImportRequestFailure(null)
                  }} placeholder={'13800138000\n13800138001'} />
                </div>
                <div style={{ width: 560, maxWidth: '100%' }}>
                  <Space wrap style={{ width: '100%', justifyContent: 'space-between', marginBottom: 4 }}>
                    <Text type="secondary">公共业务标识 / 字段 JSON（手填/批量生成号码时，可空）</Text>
                    <Button size="small" disabled={fields.length === 0} onClick={() => previewImportRows()}>校验公共字段</Button>
                  </Space>
                  <Input.TextArea rows={8} style={{ width: '100%', fontFamily: 'monospace' }} value={bizText} onChange={(e) => {
                    setBizText(e.target.value); setImportPreview(null); setCsvFileName(''); setImportRes(null); setImportRequestFailure(null)
                  }} placeholder={'{"businessId":"B123","bizFields":{"customer_name":"张三","tags":["vip"]}}'} />
                  <Text type="secondary" style={{ display: 'block', marginTop: 4, fontSize: 12 }}>
                    公共对象会应用到号码区的全部号码。<Text code>businessId</Text> / <Text code>ticketId</Text> / <Text code>orderId</Text> / <Text code>userId</Text> 与 phone 同级；CSV 也可使用这些独立列。业务字段放入 bizFields，已知数组字段用 <Text code>|</Text> 分隔。
                  </Text>
                </div>
                <div style={{ width: 240, maxWidth: '100%' }}>
                  <Text type="secondary">idempotencyKey（可空）</Text>
                  <Input maxLength={128} style={{ width: '100%', display: 'block', marginTop: 4 }} value={idemKey} onChange={(e) => {
                    setIdemKey(e.target.value); setImportRes(null); setImportRequestFailure(null)
                  }} placeholder="防重复提交" />
                </div>
              </div>
              {importPreview && (
                <Card size="small" title={`解析预览 · ${importPreview.rows.length} 条`}>
                  <Space wrap style={{ marginBottom: importPreview.blockingErrorCount > 0 || importPreview.semanticWarningCount > 0 ? 8 : 12 }}>
                    <Tag color={importPreview.mode === 'CSV' ? 'blue' : importPreview.mode === 'ROWS' ? 'purple' : 'default'}>
                      {importPreview.mode === 'CSV' ? `CSV${csvFileName ? ` · ${csvFileName}` : ''}` : importPreview.mode === 'ROWS' ? '逐行数据' : '公共字段'}
                    </Tag>
                    <Text type="secondary">已识别字段：{importPreview.fieldKeys.length ? importPreview.fieldKeys.join('、') : '无'}</Text>
                    {importPreview.blockingErrorCount > 0 && <Tag color="red">{importPreview.blockingErrorCount} 个结构错误</Tag>}
                    {importPreview.semanticWarningCount > 0 && <Tag color="gold">{importPreview.semanticWarningCount} 个语义提示</Tag>}
                    {importPreview.blockingErrorCount === 0 && importPreview.semanticWarningCount === 0 && (
                      <Tag color="green">{importPreview.mode === 'CSV' ? '解析通过 · 待 Hermes 校验' : '校验通过'}</Tag>
                    )}
                  </Space>
                  {importPreview.blockingErrorCount > 0 && (
                    <Alert type="error" showIcon message="请修正结构错误后再导入" description={(
                      <>
                        <ol style={{ margin: '6px 0 0', paddingLeft: 20 }}>
                          {importPreview.blockingErrors.slice(0, 20).map((error, index) => <li key={`${index}-${error}`}>{error}</li>)}
                        </ol>
                        {importPreview.blockingErrorCount > 20 && <Text type="secondary">这里只展示前 20 个问题；共 {importPreview.blockingErrorCount} 个{importPreview.blockingErrorsTruncated ? '，详细错误已截断' : ''}。</Text>}
                      </>
                    )} />
                  )}
                  {importPreview.semanticWarningCount > 0 && (
                    <Alert type="warning" showIcon style={{ marginTop: importPreview.blockingErrorCount > 0 ? 8 : 0 }}
                      message="以下是本地语义提示，不会阻止提交" description={(
                        <>
                          <ol style={{ margin: '6px 0 0', paddingLeft: 20 }}>
                            {importPreview.semanticWarnings.slice(0, 20).map((warning, index) => <li key={`${index}-${warning}`}>{warning}</li>)}
                          </ol>
                          {importPreview.semanticWarningCount > 20 && <Text type="secondary">这里只展示前 20 个提示；共 {importPreview.semanticWarningCount} 个{importPreview.semanticWarningsTruncated ? '，详细提示已截断' : ''}。Hermes 会按原始行号最终校验。</Text>}
                        </>
                      )} />
                  )}
                  {importPreview.blockingErrorCount === 0 && (
                    <Table size="small" pagination={false} rowKey="rowNo"
                      style={{ marginTop: importPreview.semanticWarningCount > 0 ? 8 : 0 }}
                      dataSource={importPreview.rows.slice(0, 5).map((row, index) => ({ ...row, rowNo: index + 1 }))}
                      columns={[
                        { title: '#', dataIndex: 'rowNo', width: 54 },
                        { title: 'phone', dataIndex: 'phone', width: 180 },
                        { title: '业务标识', width: 260, render: (_: unknown, row) => <Text code>{JSON.stringify(Object.fromEntries(Object.entries({ businessId: row.businessId, ticketId: row.ticketId, orderId: row.orderId, userId: row.userId }).filter(([, value]) => value !== undefined)))}</Text> },
                        { title: 'bizFields', dataIndex: 'bizFields', render: (value: Record<string, unknown>) => <Text code style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(value)}</Text> },
                      ]} />
                  )}
                  {importPreview.blockingErrorCount === 0 && importPreview.rows.length > 5 && <Text type="secondary">仅预览前 5 条，提交时会按原顺序导入全部 {importPreview.rows.length} 条。</Text>}
                </Card>
              )}
              <Button type="primary" icon={<ThunderboltOutlined />} loading={importing} onClick={doImport}>导入并触发 run</Button>
              {importRequestFailure && (
                <Alert type="error" showIcon
                  message={importRequestFailure.code == null ? '导入请求失败' : `导入请求失败（errorCode ${importRequestFailure.code}）`}
                  description={importRequestFailure.message} />
              )}
              {importRes && (
                <Descriptions size="small" bordered column={2} style={{ marginTop: 8 }}>
                  <Descriptions.Item label="批次">{importRes.batchCode || importRes.code}</Descriptions.Item>
                  <Descriptions.Item label="成功/失败/总数">{importRes.success}/{importRes.fail}/{importRes.total}</Descriptions.Item>
                  <Descriptions.Item label="plans" span={2}>
                    {(importRes.plans || []).map((p, i) => (
                      <Tag key={i} color={p.result === SF_IMPORT_RUN_CREATED ? 'green' : p.result === SF_IMPORT_FIELD_FAIL ? 'red' : 'orange'}>
                        {p.defCode}: {p.result === SF_IMPORT_RUN_CREATED ? `run ${p.runCode}` : p.result === SF_IMPORT_FIELD_FAIL ? `字段失败 ${(p.failFields || []).join('/')}` : '无绑定'}
                      </Tag>
                    ))}
                  </Descriptions.Item>
                </Descriptions>
              )}
              {importErrorSummary && importErrorState && (
                <Card size="small" title={`失败明细 · ${importErrorSummary.fail} 行`}>
                  {importErrorState.kind === 'not-retained' && (
                    <Alert type="warning" showIcon style={{ marginBottom: 8 }}
                      message="命中幂等重放：统计来自原批次，失败原因未保留" />
                  )}
                  {importErrorState.kind === 'truncated' && (
                    <Alert type="warning" showIcon style={{ marginBottom: 8 }}
                      message={`失败共 ${importErrorState.fail} 行，仅返回前 ${importErrorState.shown} 行明细`} />
                  )}
                  {importErrorSummary.errors.length > 0 && (
                    <Table size="small" pagination={false} rowKey="rowNo" dataSource={importErrorSummary.errors}
                      scroll={{ y: 240 }} columns={[
                        { title: '原始行号', dataIndex: 'rowNo', width: 100 },
                        { title: '错误原因', render: (_: unknown, row: SfImportRowError) => formatImportRowError(row) },
                      ]} />
                  )}
                </Card>
              )}
            </>
          )}
        </Space>
      </Card>

      <Modal title="批量生成连续手机号" open={phoneGeneratorOpen} okText={phoneGeneratorMode === 'append' ? '追加号码' : '覆盖号码'}
        cancelText="取消" onOk={applyPhoneGeneration} onCancel={() => setPhoneGeneratorOpen(false)}>
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <div>
            <Text type="secondary">起始手机号</Text>
            <Input style={{ marginTop: 4 }} value={phoneGeneratorStart} onChange={(e) => setPhoneGeneratorStart(e.target.value)} placeholder="13800138000" />
          </div>
          <div>
            <Text type="secondary">生成数量（最多 {MAX_STRATFLOW_IMPORT_ROWS}）</Text><br />
            <InputNumber min={1} max={MAX_STRATFLOW_IMPORT_ROWS} precision={0} style={{ width: '100%', marginTop: 4 }} value={phoneGeneratorCount}
              onChange={(value) => setPhoneGeneratorCount(Number(value) || 1)} />
          </div>
          <Radio.Group value={phoneGeneratorMode} onChange={(e) => setPhoneGeneratorMode(e.target.value as 'overwrite' | 'append')}
            options={[{ value: 'overwrite', label: '覆盖号码区' }, { value: 'append', label: `追加到现有 ${phoneCount} 条之后` }]} />
        </Space>
      </Modal>

      {/* ⑤ 观测 + 断言 */}
      <Card title="⑤ 观测计划 + 断言分支（edgeFlow）" size="small"
        extra={
          <Space>
            <Text type="secondary">自动</Text>
            <Switch size="small" checked={autoObserve} onChange={setAutoObserve} />
            <Button size="small" icon={<ReloadOutlined />} onClick={() => observe()}>刷新</Button>
            <Button size="small" danger icon={<ClearOutlined />} onClick={() => clearMock('all')}>清场</Button>
          </Space>
        }>
        <Space wrap align="center" style={{ marginBottom: 8 }}>
          <Text>runCode：</Text>
          <Input style={{ width: 240 }} value={runCode} onChange={(e) => {
            observeGenerationRef.current++; observingRef.current = false
            setRunCode(e.target.value); setPlans([]); setDecisions([]); setDecisionTotal(0); setDecisionPage(1); setPlansErr(''); setProgress(null)
          }} placeholder="import 后自动填，也可手填" />
          <Text type="secondary">进度窗口(UTC)：</Text>
          <Input style={{ width: 170 }} value={timeWin[0]} onChange={(e) => {
            observeGenerationRef.current++; observingRef.current = false; setProgress(null)
            setTimeWin([e.target.value, timeWin[1]])
          }} />
          <Text>~</Text>
          <Input style={{ width: 170 }} value={timeWin[1]} onChange={(e) => {
            observeGenerationRef.current++; observingRef.current = false; setProgress(null)
            setTimeWin([timeWin[0], e.target.value])
          }} />
          <Text type="secondary">≤31天</Text>
        </Space>
        <Divider style={{ margin: '8px 0' }} orientation="left" plain>mock 回放计划（PENDING / DEAD）</Divider>
        {plansErr && <Alert type="warning" showIcon message={plansErr} style={{ marginBottom: 8 }} />}
        <Table size="small" rowKey="actionCode" pagination={{ pageSize: 5 }} dataSource={plans} locale={{ emptyText: '无在途计划' }}
          columns={[
            { title: '节点', dataIndex: 'nodeId', width: 120 },
            { title: '结局', dataIndex: 'outcomeKey' },
            { title: '渠道', dataIndex: 'channel', width: 70 },
            { title: '状态', width: 90, render: (_: unknown, p: SfActionPlan) => <Tag color={p.status === 'DEAD' ? 'red' : 'processing'}>{p.status || 'PENDING'}</Tag> },
            { title: '步', width: 70, render: (_: unknown, p: SfActionPlan) => p.idx == null || !p.steps ? '—' : `${p.idx + 1}/${p.steps.length}` },
            { title: '重试', dataIndex: 'retryCount', width: 70 },
            { title: '最近错误', dataIndex: 'lastError', ellipsis: true },
            { title: '操作', width: 90, render: (_: unknown, p: SfActionPlan) => p.status === 'DEAD' ? <Button size="small" onClick={() => void requeuePlan(p)}>重新入队</Button> : null },
          ]} />
        <Divider style={{ margin: '8px 0' }} orientation="left" plain>Mock 决策记录（DONE 保留 7 天）</Divider>
        <Table size="small" rowKey="actionCode" dataSource={decisions} scroll={{ x: 1200 }} locale={{ emptyText: '暂无决策记录；导入并发生触达派发后生成' }}
          pagination={{ current: decisionPage, pageSize: 100, total: decisionTotal, showSizeChanger: false, onChange: (page) => {
            observeGenerationRef.current++; observingRef.current = false; setDecisionPage(page)
          }, showTotal: (total) => `共 ${total} 条` }}
          columns={[
            { title: '名单/节点', width: 210, render: (_: unknown, d: SfDecision) => <><Text code>{d.entryCode}</Text><br /><Text>{d.nodeId} · {d.channel}</Text></> },
            { title: '选择方式', width: 210, render: (_: unknown, d: SfDecision) => <><Tag color={d.selectionMode?.includes('WEIGHTED') ? 'purple' : d.selectionMode === 'FORCED' ? 'orange' : 'blue'}>{d.selectionMode ? (SELECTION_MODE_LABEL[d.selectionMode] || d.selectionMode) : '—'}</Tag><div>{d.selectionMode === 'FORCED' ? '临时强制覆盖' : d.matchedRule ? `命中规则：${d.matchedRule}` : '未命中规则，走默认'}{d.totalWeight ? ` · 权重 ${d.selectedWeight}/${d.totalWeight}` : ''}</div></> },
            { title: 'Case', width: 220, render: (_: unknown, d: SfDecision) => <><Text>{d.caseName || d.caseKey}</Text><br /><Text code>{d.caseKey}</Text>{d.noReceipt && <Tag color="gold">不回执</Tag>}</> },
            { title: '回放', width: 90, render: (_: unknown, d: SfDecision) => <Tag color={d.status === 'DEAD' ? 'red' : d.status === 'DONE' ? 'green' : 'processing'}>{d.status}</Tag> },
            { title: '预期变量/出口', width: 230, render: (_: unknown, d: SfDecision) => <><Text code style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(d.expectedVars || {})}</Text><div>出口：{d.expectedPort || '—'}</div></> },
            { title: '实际变量/出口', width: 230, render: (_: unknown, d: SfDecision) => <><Text code style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(d.actualVars || {})}</Text><div>{d.routed ? `已分流：${d.actualPort || '—'}` : '尚未分流'}</div></> },
            { title: '错误', dataIndex: 'lastError', ellipsis: true },
          ]} />
        <Divider style={{ margin: '8px 0' }} orientation="left" plain>run 进度（漏斗 + 边流量）</Divider>
        {progress ? (
          <>
            <Descriptions size="small" column={4} style={{ marginBottom: 8 }}>
              <Descriptions.Item label="号码数">{progress.run.numberCount}</Descriptions.Item>
              <Descriptions.Item label="终态">{progress.run.terminalCount}</Descriptions.Item>
              <Descriptions.Item label="到达End">{progress.run.reachedEndCount}</Descriptions.Item>
              <Descriptions.Item label="取消/过期">{progress.run.canceledCount}/{progress.run.expiredCount}</Descriptions.Item>
            </Descriptions>
            <Table size="small" rowKey="nodeId" pagination={false} dataSource={progress.nodes} locale={{ emptyText: '无节点进度' }}
              columns={[
                { title: '节点', dataIndex: 'nodeId', width: 140 },
                { title: '流入', dataIndex: 'inflow', width: 70 },
                { title: '已处理', dataIndex: 'processed', width: 80 },
                { title: '处理中', dataIndex: 'processing', width: 80 },
                { title: '边流量 edgeFlow（断言分支）', render: (_: unknown, n: SfRunNode) => <Space wrap>{Object.entries(n.edgeFlow || {}).map(([k, v]) => <Tag key={k} color="blue">{k}: {v}</Tag>)}</Space> },
              ]} />
          </>
        ) : <Paragraph type="secondary">导入生成 run 后自动拉取；或填 runCode + 窗口后点刷新。</Paragraph>}
      </Card>
    </div>
  )
}
