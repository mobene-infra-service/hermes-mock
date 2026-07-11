import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Card, Select, Button, Space, Table, Tag, Typography, InputNumber, Input, Switch,
  message, Modal, Alert, Collapse, Descriptions, Empty, Divider,
} from 'antd'
import { ReloadOutlined, ThunderboltOutlined, ClearOutlined } from '@ant-design/icons'
import {
  sfGate, sfSetGlobalGate, sfSetSchemeGate, sfClearSchemeGate, sfSetDeliveryPaused, sfSetReceiptWindow,
  sfListConfig, sfPutConfig, sfDeleteConfig, sfClearMock, sfListPlans, sfRequeuePlan,
  sfWorkflows, sfWorkflowDetail, sfCollections, sfCollectionFields, sfCollectionBindings, sfRunProgress, sfImport,
  CURRENT_ORG_STORAGE_KEY,
} from '../api'
import type {
  SfGateView, SfNode, SfNodeConfig, SfWorkflow, SfWorkflowDetail,
  SfCollection, SfField, SfActionPlan, SfImportResult, SfRunProgress, SfRunNode, SfBinding, SfImportRow, SfDispatchMode,
} from '../types'
import { SF_IMPORT_RUN_CREATED, SF_IMPORT_FIELD_FAIL } from '../types'
import { PageHeader } from '../components/layout/PageHeader'
import { InfoBanner } from '../components/layout/InfoBanner'
import { usePolling } from '../hooks/usePolling'
import { ORG_CHANGED_EVENT } from '../components/layout/useCurrentOrg'

const { Text, Paragraph } = Typography

// 把 Date 格式化成 UTC "yyyy-MM-dd HH:mm:ss"（run 进度接口窗口参数用）。
function fmtUTC(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}`
}
function defaultWindow(): [string, string] {
  const now = Date.now()
  return [fmtUTC(new Date(now - 24 * 3600e3)), fmtUTC(new Date(now + 24 * 3600e3))]
}

// 单节点的可编辑配置（本地态；初值来自 GET /config 返回的有效权重/强制/延迟）。
type Edit = { forcedOutcome?: string; baseDelayMs: number; weights: Record<string, number> }

const DISPATCH_MODE_OPTIONS: { value: SfDispatchMode; label: string }[] = [
  { value: 'PAUSED', label: 'PAUSED · 暂停新派发' },
  { value: 'MOCK', label: 'MOCK · 合成回执' },
  { value: 'REAL', label: 'REAL · 真实下游' },
]

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
  const [edits, setEdits] = useState<Record<string, Edit>>({})

  const [collections, setCollections] = useState<SfCollection[]>([])
  const [collCode, setCollCode] = useState<string>()
  const [fields, setFields] = useState<SfField[]>([])
  const [bindings, setBindings] = useState<SfBinding[]>([])

  const [phonesText, setPhonesText] = useState('')
  const [bizText, setBizText] = useState('')
  const [idemKey, setIdemKey] = useState('')
  const [importing, setImporting] = useState(false)
  const [importRes, setImportRes] = useState<SfImportResult | null>(null)

  const [runCode, setRunCode] = useState('')
  const [plans, setPlans] = useState<SfActionPlan[]>([])
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
    setDefCode(dc); setDetail(null); setNodes([]); setEdits({})
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
      setNodes(list)
      const init: Record<string, Edit> = {}
      list.forEach((n) => {
        init[n.nodeId] = {
          forcedOutcome: n.forcedOutcome ?? undefined,
          baseDelayMs: n.baseDelayMs || 0,
          weights: Object.fromEntries(n.outcomes.map((o) => [o.key, o.weight])),
        }
      })
      setEdits(init)
    } catch (e) { if (generation === workflowGenerationRef.current) message.error(String(e)) }
  }

  const saveNode = async (n: SfNode) => {
    if (!versionCode) return
    const orgGeneration = orgGenerationRef.current
    const workflowGeneration = workflowGenerationRef.current
    const ed = edits[n.nodeId]
    const cfg: SfNodeConfig = {
      forcedOutcome: ed.forcedOutcome || null,
      baseDelayMs: ed.baseDelayMs || 0,
      weights: ed.weights,
    }
    try {
      await sfPutConfig(versionCode, n.nodeId, cfg)
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

  const patchEdit = (nodeId: string, patch: Partial<Edit>) =>
    setEdits((prev) => ({ ...prev, [nodeId]: { ...prev[nodeId], ...patch } }))

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
      setNodes([]); setEdits({}); setCollections([]); setCollCode(undefined); setFields([]); setBindings([])
      setPhonesText(''); setBizText(''); setIdemKey(''); setImportRes(null); setImporting(false)
      setRunCode(''); setPlans([]); setPlansErr(''); setProgress(null); setAutoObserve(false)
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
    setCollCode(code); setFields([]); setBindings([])
    setRunCode(''); setPlans([]); setPlansErr(''); setProgress(null)
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
      const unset = nodes.filter((n) => !edits[n.nodeId]?.forcedOutcome).map((n) => n.nodeId)
      if (unset.length > 0) {
        w.push(`方案 ${defCode} 有 ${unset.length}/${nodes.length} 个触达节点未设强制结局（${unset.join('、')}）：将按默认权重随机分支。要断言特定分支请先逐节点设 forcedOutcome。`)
      }
    }
    return w
  }

  const doImport = () => {
    if (!collCode) { message.warning('先选名单集合'); return }
    const phones = phonesText.split('\n').map((s) => s.trim()).filter(Boolean)
    if (!phones.length) { message.warning('至少填一个号码'); return }
    let biz: Record<string, unknown> = {}
    if (bizText.trim()) {
      try { biz = JSON.parse(bizText) } catch { message.error('公共业务字段不是合法 JSON'); return }
    }
    const code = collCode
    const rows: SfImportRow[] = phones.map((p) => ({ phone: p, bizFields: biz }))
    const warns = importWarnings()
    if (warns.length === 0) { void runImport(code, rows); return }
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
      onOk: () => runImport(code, rows),
    })
  }

  const runImport = async (code: string, rows: SfImportRow[]) => {
    const orgGeneration = orgGenerationRef.current
    const collectionGeneration = collectionGenerationRef.current
    setImporting(true)
    try {
      const res = await sfImport(code, {
        idempotencyKey: idemKey || undefined,
        rows,
      })
      if (orgGeneration !== orgGenerationRef.current || collectionGeneration !== collectionGenerationRef.current) return
      setImportRes(res)
      // 多绑定名单会为每个绑定方案各返回一条 plan：优先取"本页选中并配置的方案 defCode"那条 run，
      // 否则回退首条成功——否则可能观测到别的方案的 run，令你为选中方案配的强制结局看似不生效。
      const plans = res.plans || []
      const ok = plans.find((p) => p.defCode === defCode && p.result === SF_IMPORT_RUN_CREATED && p.runCode)
        ?? plans.find((p) => p.result === SF_IMPORT_RUN_CREATED && p.runCode)
      if (ok) {
        observeGenerationRef.current++; observingRef.current = false
        setRunCode(ok.runCode)
        setPlans([]); setPlansErr(''); setProgress(null)
        message.success(`已生成 run ${ok.runCode}`)
        void observe(ok.runCode)
      } else {
        const fail = (res.plans || [])[0]
        message.warning(fail?.result === SF_IMPORT_FIELD_FAIL ? `字段契约失败：${(fail.failFields || []).join(', ')}` : '无可用绑定/未生成 run，看导入结果')
      }
    } catch (e) {
      if (orgGeneration === orgGenerationRef.current && collectionGeneration === collectionGenerationRef.current) message.error(String(e))
    } finally {
      if (orgGeneration === orgGenerationRef.current && collectionGeneration === collectionGenerationRef.current) setImporting(false)
    }
  }

  // —— 观测 ——
  const observe = useCallback(async (rc?: string) => {
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
        sfRunProgress(collCode, code, timeWin[0], timeWin[1])
          .then((next) => { if (generation === observeGenerationRef.current) setProgress(next) })
          .catch((e) => {
            if (generation !== observeGenerationRef.current) return
            setProgress(null); message.error(`进度查询失败：${String(e)}`)
          }),
      ])
    } finally {
      if (generation === observeGenerationRef.current) observingRef.current = false
    }
  }, [runCode, collCode, timeWin])

  usePolling(() => { if (autoObserve && runCode) void observe() }, 3000, { immediate: false })

  const executeClearMock = async (scope: 'all' | 'plans' | 'config') => {
    const orgGeneration = orgGenerationRef.current
    const workflowGeneration = workflowGenerationRef.current
    try {
      await sfClearMock(scope)
      if (orgGeneration !== orgGenerationRef.current || workflowGeneration !== workflowGenerationRef.current) return
      message.success(`已清空（${scope}）`)
      if (scope !== 'config') { setPlans([]); setProgress(null) }
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
      content: '删除 plans 后，对应已派发动作不会再收到合成回执，只能等待回执超时。该操作仅作用于当前机构，但可能令正在执行的 run 变慢或走失败分支。',
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

  return (
    <div className="page-container">
      <PageHeader
        title="策略流 Mock 编排"
        status={master ? { tone: globalMode === 'MOCK' ? 'success' : globalMode === 'PAUSED' ? 'warning' : 'neutral', text: `Mock capability 可用 · 当前机构 ${globalMode}` } : { tone: 'danger', text: '本环境不可 mock' }}
        onReload={() => { void loadGate(); void loadWorkflows() }}
      />
      <InfoBanner title="应用层 mock 编排（与 SIP 被叫腿正交）">
        这里驱动 <Text code>hermes-stratflow</Text> 的应用层 mock：派发那刻按结局词表采样 → 合成回执事件，**不打真实电话、不经被叫腿**。
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
          {nodes.length === 0 ? <Empty description="该版本无触达节点（VOICEBOT_CALL/SMS_SEND）" /> : (
            <Collapse items={nodes.map((n) => {
              const ed = edits[n.nodeId]
              return {
                key: n.nodeId,
                label: <Space><Text strong>{n.nodeId}</Text><Tag>{n.type}</Tag>{n.channel && <Tag color="geekblue">{n.channel}</Tag>}{ed?.forcedOutcome && <Tag color="orange">强制 {ed.forcedOutcome}</Tag>}</Space>,
                children: ed ? (
                  <>
                    <Space wrap style={{ marginBottom: 8 }}>
                      <Text>强制结局：</Text>
                      <Select allowClear style={{ width: 260 }} placeholder="不强制（按权重随机）" value={ed.forcedOutcome}
                        onChange={(v) => patchEdit(n.nodeId, { forcedOutcome: v })}
                        options={n.outcomes.map((o) => ({ value: o.key, label: `${o.label}（${o.key}）` }))} />
                      <Text>基础延迟 baseDelayMs：</Text>
                      <InputNumber min={0} step={1000} value={ed.baseDelayMs} onChange={(v) => patchEdit(n.nodeId, { baseDelayMs: v || 0 })} />
                      <Text type="secondary">过大会被拒（须小于回执窗口）。</Text>
                    </Space>
                    <Table size="small" rowKey="key" pagination={false} dataSource={n.outcomes}
                      columns={[
                        { title: '结局', dataIndex: 'label', render: (v: string, o) => <Space><Text>{v}</Text><Text code style={{ fontSize: 11 }}>{o.key}</Text>{o.steps === 0 && <Tag color="gold">超时不回执</Tag>}</Space> },
                        { title: '默认权重', dataIndex: 'defaultWeight', width: 90 },
                        {
                          title: '有效权重', width: 130, render: (_: unknown, o) => (
                            <InputNumber min={0} disabled={!!ed.forcedOutcome} value={ed.weights[o.key]}
                              onChange={(v) => patchEdit(n.nodeId, { weights: { ...ed.weights, [o.key]: v || 0 } })} />
                          ),
                        },
                        { title: '回执步数', dataIndex: 'steps', width: 80 },
                      ]} />
                    <Space style={{ marginTop: 8 }}>
                      <Button type="primary" size="small" disabled={!master} onClick={() => saveNode(n)}>保存本节点</Button>
                      <Button size="small" disabled={!master} onClick={() => resetNode(n)}>重置为默认</Button>
                    </Space>
                  </>
                ) : null,
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
                <Alert type="info" showIcon message={<span>必填业务字段：{requiredFields.map((f) => <Tag key={f.key}>{f.displayName}（{f.key}）</Tag>)} —— 未提供会导致导入 result=2（字段契约失败）。</span>} />
              )}
              <Text type="secondary" style={{ fontSize: 12 }}>
                绑定方案：{bindings.length === 0 ? '无（导入不会生成 run）' : bindings.map((b) => (
                  <Tag key={b.defCode} color={schemeModeOf(b.defCode) === 'MOCK' ? 'green' : schemeModeOf(b.defCode) === 'PAUSED' ? 'gold' : 'default'}>
                    {(b.defName || b.defCode)} · {schemeModeOf(b.defCode)}
                  </Tag>
                ))}
              </Text>
              <Space align="start" wrap style={{ width: '100%' }}>
                <div>
                  <Text type="secondary">号码（每行一个）</Text>
                  <Input.TextArea rows={5} style={{ width: 300 }} value={phonesText} onChange={(e) => setPhonesText(e.target.value)} placeholder={'13800138000\n13800138001'} />
                </div>
                <div>
                  <Text type="secondary">公共业务字段 JSON（应用到每行，可空）</Text>
                  <Input.TextArea rows={5} style={{ width: 300 }} value={bizText} onChange={(e) => setBizText(e.target.value)} placeholder={'{"customer_name":"张三"}'} />
                </div>
                <div>
                  <Text type="secondary">idempotencyKey（可空）</Text>
                  <Input style={{ width: 220 }} value={idemKey} onChange={(e) => setIdemKey(e.target.value)} placeholder="防重复提交" />
                </div>
              </Space>
              <Button type="primary" icon={<ThunderboltOutlined />} loading={importing} onClick={doImport}>导入触发 run</Button>
              {importRes && (
                <Descriptions size="small" bordered column={2} style={{ marginTop: 8 }}>
                  <Descriptions.Item label="批次">{importRes.batchCode || importRes.code}</Descriptions.Item>
                  <Descriptions.Item label="成功/总数">{importRes.success}/{importRes.total}</Descriptions.Item>
                  <Descriptions.Item label="plans" span={2}>
                    {(importRes.plans || []).map((p, i) => (
                      <Tag key={i} color={p.result === SF_IMPORT_RUN_CREATED ? 'green' : p.result === SF_IMPORT_FIELD_FAIL ? 'red' : 'orange'}>
                        {p.defCode}: {p.result === SF_IMPORT_RUN_CREATED ? `run ${p.runCode}` : p.result === SF_IMPORT_FIELD_FAIL ? `字段失败 ${(p.failFields || []).join('/')}` : '无绑定'}
                      </Tag>
                    ))}
                  </Descriptions.Item>
                </Descriptions>
              )}
            </>
          )}
        </Space>
      </Card>

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
            setRunCode(e.target.value); setPlans([]); setPlansErr(''); setProgress(null)
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
        <Divider style={{ margin: '8px 0' }} orientation="left" plain>mock 计划（PENDING / DEAD；超时结局无计划）</Divider>
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
