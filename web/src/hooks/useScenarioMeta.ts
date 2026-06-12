import { useCallback, useEffect, useState } from 'react'
import { message } from 'antd'
import {
  bootstrapDemo, getPreflight, listBindings, listGroups, listOrgAgentGroups, listOrgs, listOrgTts,
  type AgentGroupAgg, type CustomerGroup, type LineBinding, type PreflightReport, type TtsVoice,
} from '../api'

export interface PreflightSet {
  callCenterTask?: PreflightReport
  autoCall?: PreflightReport
  otp?: PreflightReport
}

// useScenarioMeta：业务测试场景页共享的元数据（机构/客户组/技能组/TTS/端口绑定/preflight）+ 派生选项 + 播种。
// 每个场景页独立调用（数据量小、跨页重新挂载会自动 reload）。
export function useScenarioMeta() {
  const [pf, setPf] = useState<PreflightSet | null>(null)
  const [currentOrg, setCurrentOrg] = useState('org001')
  const [customers, setCustomers] = useState<CustomerGroup[]>([])
  const [hermesAgentGroups, setHermesAgentGroups] = useState<AgentGroupAgg[]>([])
  const [ttsList, setTtsList] = useState<TtsVoice[]>([])
  const [bindings, setBindings] = useState<LineBinding[]>([])
  const [bootstrapping, setBootstrapping] = useState(false)

  const loadPf = useCallback(() => { getPreflight().then(setPf).catch(() => { /* ignore */ }) }, [])
  const loadMeta = useCallback(() => {
    listOrgs().then((r) => setCurrentOrg(r.current || 'org001')).catch(() => { /* ignore */ })
    listGroups().then(setCustomers).catch(() => { /* ignore */ })
    listOrgAgentGroups().then((r) => setHermesAgentGroups(r.groups || [])).catch(() => { /* ignore */ })
    listOrgTts().then((r) => setTtsList(r.tts || [])).catch(() => { /* ignore */ })
    listBindings().then(setBindings).catch(() => { /* ignore */ })
  }, [])

  const reload = useCallback(() => { loadPf(); loadMeta() }, [loadPf, loadMeta])
  useEffect(() => { reload() }, [reload])

  const bootstrap = useCallback(async () => {
    setBootstrapping(true)
    try {
      const r = await bootstrapDemo({ provisionLine: true })
      if (r.error) { message.error(`播种失败：${r.error}`); return }
      message.success(`已播种 mock 客户配置：${r.result?.customerGroup}`)
      reload()
    } catch (e) { message.error(String(e)) } finally { setBootstrapping(false) }
  }, [reload])

  const customerOptions = customers.map((g) => ({ value: g.code, label: `${g.code}（${g.count || 0} 客户）` }))
  const hermesSkillOptions = hermesAgentGroups.map((g) => ({
    value: g.code,
    label: g.name ? `${g.name}（${g.code}·${g.count} 坐席）` : `${g.code}（${g.count} 坐席）`,
  }))
  // TTS 选项：名称优先展示（ttsCode 是 32 位 hash，难辨认），code 保留供搜索/提交。
  const ttsOptions = ttsList.map((t) => ({ value: t.ttsCode, label: t.name || t.ttsCode, code: t.ttsCode }))

  return {
    pf, currentOrg, customers, hermesAgentGroups, ttsList, bindings,
    customerOptions, hermesSkillOptions, ttsOptions,
    bootstrapping, bootstrap, reload,
  }
}
