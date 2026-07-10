import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Card, Select, Button, Space, Table, Tag, Typography, InputNumber, Input, Switch,
  message, Modal, Alert, Collapse, Descriptions, Empty, Divider,
} from 'antd'
import { ReloadOutlined, ThunderboltOutlined, ClearOutlined } from '@ant-design/icons'
import {
  sfGate, sfSetGlobalGate, sfSetSchemeGate, sfClearSchemeGate, sfSetDeliveryPaused, sfSetReceiptWindow,
  sfListConfig, sfPutConfig, sfDeleteConfig, sfClearMock, sfListPlans,
  sfWorkflows, sfWorkflowDetail, sfCollections, sfCollectionFields, sfCollectionBindings, sfRunProgress, sfImport,
} from '../api'
import type {
  SfGateView, SfNode, SfNodeConfig, SfWorkflow, SfWorkflowDetail,
  SfCollection, SfField, SfActionPlan, SfImportResult, SfRunProgress, SfRunNode, SfBinding, SfImportRow,
} from '../types'
import { SF_IMPORT_RUN_CREATED, SF_IMPORT_FIELD_FAIL } from '../types'
import { PageHeader } from '../components/layout/PageHeader'
import { InfoBanner } from '../components/layout/InfoBanner'
import { usePolling } from '../hooks/usePolling'

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
  const [progress, setProgress] = useState<SfRunProgress | null>(null)
  const [timeWin, setTimeWin] = useState<[string, string]>(defaultWindow())
  const [autoObserve, setAutoObserve] = useState(false)
  const versionCode = detail?.versionCode
  const master = gate?.master ?? false
  // 回执窗压缩(秒)本地态：从 gate 同步，避免逐键触发接口/中途校验；点「应用」才提交。
  const [winSec, setWinSec] = useState(0)
  useEffect(() => { setWinSec(gate?.receiptWindowSec ?? 0) }, [gate?.receiptWindowSec])

  // —— gate ——
  const loadGate = useCallback(async () => {
    try { setGate(await sfGate()); setGateErr('') }
    catch (e) { setGateErr(String(e)); setGate(null) }
  }, [])

  const loadWorkflows = useCallback(async () => {
    try { setWorkflows((await sfWorkflows()).workflows || []) }
    catch (e) { message.error(String(e)) }
  }, [])

  useEffect(() => { void loadGate(); void loadWorkflows() }, [loadGate, loadWorkflows])

  // —— 选方案 → 拿 versionCode + 配置 ——
  const pickWorkflow = async (dc: string) => {
    setDefCode(dc); setDetail(null); setNodes([]); setEdits({})
    try {
      const d = await sfWorkflowDetail(dc)
      setDetail(d)
      if (d.versionCode) await loadConfig(d.versionCode)
    } catch (e) { message.error(String(e)) }
  }

  const loadConfig = async (ver: string) => {
    try {
      const list = (await sfListConfig(ver)).nodes || []
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
    } catch (e) { message.error(String(e)) }
  }

  const saveNode = async (n: SfNode) => {
    if (!versionCode) return
    const ed = edits[n.nodeId]
    const cfg: SfNodeConfig = {
      forcedOutcome: ed.forcedOutcome || null,
      baseDelayMs: ed.baseDelayMs || 0,
      weights: ed.weights,
    }
    try { await sfPutConfig(versionCode, n.nodeId, cfg); message.success(`已存 ${n.nodeId}`); await loadConfig(versionCode) }
    catch (e) { message.error(String(e)) }
  }

  const resetNode = async (n: SfNode) => {
    if (!versionCode) return
    try { await sfDeleteConfig(versionCode, n.nodeId); message.success(`已重置 ${n.nodeId}`); await loadConfig(versionCode) }
    catch (e) { message.error(String(e)) }
  }

  const patchEdit = (nodeId: string, patch: Partial<Edit>) =>
    setEdits((prev) => ({ ...prev, [nodeId]: { ...prev[nodeId], ...patch } }))

  // —— 名单 ——
  const loadCollections = useCallback(async () => {
    try { setCollections((await sfCollections()).collections || []) }
    catch (e) { message.error(String(e)) }
  }, [])
  useEffect(() => { void loadCollections() }, [loadCollections])

  const pickCollection = async (code: string) => {
    setCollCode(code); setFields([]); setBindings([])
    try {
      const [f, b] = await Promise.all([sfCollectionFields(code), sfCollectionBindings(code)])
      setFields(f.fields || [])
      setBindings(b.bindings || [])
    } catch (e) { message.error(String(e)) }
  }

  // 该方案本次导入是否有效走 mock：master 硬闸门 + 方案覆盖(无覆盖回落全局)。关=真实派发，不产 mock 回执。
  const schemeMockOn = (dc: string) => master && (gate?.schemes?.[dc] ?? !!gate?.global)

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
      if (!schemeMockOn(dc)) {
        w.push(`方案 ${dc} 未开 mock（scheme 门=关${master ? '' : '；且本环境 master=关'}）：本次导入会走真实下游派发。`)
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
    setImporting(true)
    try {
      const res = await sfImport(code, {
        idempotencyKey: idemKey || undefined,
        rows,
      })
      setImportRes(res)
      // 多绑定名单会为每个绑定方案各返回一条 plan：优先取"本页选中并配置的方案 defCode"那条 run，
      // 否则回退首条成功——否则可能观测到别的方案的 run，令你为选中方案配的强制结局看似不生效。
      const plans = res.plans || []
      const ok = plans.find((p) => p.defCode === defCode && p.result === SF_IMPORT_RUN_CREATED && p.runCode)
        ?? plans.find((p) => p.result === SF_IMPORT_RUN_CREATED && p.runCode)
      if (ok) {
        setRunCode(ok.runCode)
        message.success(`已生成 run ${ok.runCode}`)
        void observe(ok.runCode)
      } else {
        const fail = (res.plans || [])[0]
        message.warning(fail?.result === SF_IMPORT_FIELD_FAIL ? `字段契约失败：${(fail.failFields || []).join(', ')}` : '无可用绑定/未生成 run，看导入结果')
      }
    } catch (e) { message.error(String(e)) }
    finally { setImporting(false) }
  }

  // —— 观测 ——
  const observe = useCallback(async (rc?: string) => {
    const code = rc || runCode
    if (!code || !collCode) return
    try {
      const [pl, pr] = await Promise.all([
        sfListPlans(code).then((r) => r.plans || []).catch(() => [] as SfActionPlan[]),
        sfRunProgress(collCode, code, timeWin[0], timeWin[1]).catch(() => null as SfRunProgress | null),
      ])
      setPlans(pl); setProgress(pr)
    } catch (e) { message.error(String(e)) }
  }, [runCode, collCode, timeWin])

  usePolling(() => { if (autoObserve && runCode) void observe() }, 3000, { immediate: false })

  const clearMock = async (scope: 'all' | 'plans' | 'config') => {
    try { await sfClearMock(scope); message.success(`已清空（${scope}）`); if (scope !== 'config') { setPlans([]); setProgress(null) } if (versionCode && scope !== 'plans') await loadConfig(versionCode) }
    catch (e) { message.error(String(e)) }
  }

  const schemeState = defCode ? gate?.schemes?.[defCode] : undefined
  const requiredFields = useMemo(() => fields.filter((f) => f.required), [fields])

  return (
    <div className="page-container">
      <PageHeader
        title="策略流 Mock 编排"
        status={master ? { tone: 'success', text: `mock 可用 · global ${gate?.global ? '开' : '关'}` } : { tone: 'danger', text: '本环境不可 mock' }}
        onReload={() => { void loadGate(); void loadWorkflows() }}
      />
      <InfoBanner title="应用层 mock 编排（与 SIP 被叫腿正交）">
        这里驱动 <Text code>hermes-stratflow</Text> 的应用层 mock：派发那刻按结局词表采样 → 合成回执事件，**不打真实电话、不经被叫腿**。
        用于测策略图分支/回执逻辑。顺序：选方案(拿 versionCode) → 开方案门闸 → 配结局 → 选名单导入触发 run → 观测计划 + 按 edgeFlow 断言分支。
      </InfoBanner>

      {gateErr && <Alert type="error" showIcon style={{ marginBottom: 12 }} message="读取 gate 失败" description={gateErr} />}
      {gate && !master && (
        <Alert type="warning" showIcon style={{ marginBottom: 12 }}
          message="本环境未启用 mock（stratflow.mock-downstream.enabled=false）"
          description="master 硬闸门为关：所有 mock 写操作会被拒。请在 local/test 环境操作。" />
      )}

      {/* ① gate 开关 */}
      <Card title="① 运行期开关" size="small" style={{ marginBottom: 12 }}>
        <Space size="large" wrap>
          <Space>
            <Text>全局 mock：</Text>
            <Switch disabled={!master} checked={!!gate?.global}
              onChange={async (v) => { try { setGate(await sfSetGlobalGate(v)) } catch (e) { message.error(String(e)) } }} />
          </Space>
          <Space>
            <Text>回放暂停（step-through）：</Text>
            <Switch disabled={!master} checked={!!gate?.deliveryPaused}
              onChange={async (v) => { try { setGate(await sfSetDeliveryPaused(v)) } catch (e) { message.error(String(e)) } }} />
          </Space>
          <Space>
            <Text>回执窗压缩(秒)：</Text>
            <InputNumber min={0} step={30} style={{ width: 110 }} disabled={!master} value={winSec}
              onChange={(v) => setWinSec(Number(v) || 0)} />
            <Button size="small" disabled={!master}
              onClick={async () => { try { setGate(await sfSetReceiptWindow(winSec)); message.success(winSec > 0 ? `回执窗压到 ${winSec}s（仅 mock 派发）` : '已恢复真实回执窗') } catch (e) { message.error(String(e)) } }}>应用</Button>
          </Space>
          <Text type="secondary">默认全局关；不显式开就走真实下游。回执窗压缩(0=真实窗，非0须≥60)仅作用于 mock 派发，用于把小时级超时压到秒级实测超时分支。切换只影响之后的新派发。</Text>
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
              <Text>本方案走 mock：</Text>
              <Switch disabled={!master} checked={schemeState ?? !!gate?.global}
                onChange={async (v) => { try { setGate(await sfSetSchemeGate(defCode, v)) } catch (e) { message.error(String(e)) } }} />
              {schemeState !== undefined && (
                <Button size="small" disabled={!master}
                  onClick={async () => { try { setGate(await sfClearSchemeGate(defCode)) } catch (e) { message.error(String(e)) } }}>清方案覆盖(回落全局)</Button>
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
                  <Tag key={b.defCode} color={schemeMockOn(b.defCode) ? 'green' : 'default'}>
                    {(b.defName || b.defCode)} · {schemeMockOn(b.defCode) ? 'mock 开' : 'mock 关→真实派发'}
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
          <Input style={{ width: 240 }} value={runCode} onChange={(e) => setRunCode(e.target.value)} placeholder="import 后自动填，也可手填" />
          <Text type="secondary">进度窗口(UTC)：</Text>
          <Input style={{ width: 170 }} value={timeWin[0]} onChange={(e) => setTimeWin([e.target.value, timeWin[1]])} />
          <Text>~</Text>
          <Input style={{ width: 170 }} value={timeWin[1]} onChange={(e) => setTimeWin([timeWin[0], e.target.value])} />
          <Text type="secondary">≤31天</Text>
        </Space>
        <Divider style={{ margin: '8px 0' }} orientation="left" plain>在途 mock 计划（超时结局无计划）</Divider>
        <Table size="small" rowKey="actionCode" pagination={{ pageSize: 5 }} dataSource={plans} locale={{ emptyText: '无在途计划' }}
          columns={[
            { title: '节点', dataIndex: 'nodeId', width: 120 },
            { title: '结局', dataIndex: 'outcomeKey' },
            { title: '渠道', dataIndex: 'channel', width: 70 },
            { title: '步', width: 70, render: (_: unknown, p: SfActionPlan) => `${p.idx + 1}/${p.steps.length}` },
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
