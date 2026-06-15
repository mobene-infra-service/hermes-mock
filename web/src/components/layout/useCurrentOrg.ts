import { useEffect, useState, useCallback } from 'react'
import { listOrgs, setCurrentOrg, type OrgConfig } from '../../api'
import { resetSipWebrtcAddrCache } from '../../sip/request'

// 轻量全局当前机构状态（topbar 机构切换器用）。各页本就各自拉 orgs，这里只为 topbar 提供切换能力。
// 用一个简单事件总线在切换后通知（无需引入额外状态库）。
const ORG_EVENT = 'hm-org-changed'

export function useCurrentOrg() {
  const [orgs, setOrgs] = useState<OrgConfig[]>([])
  const [current, setCurrent] = useState('')

  const load = useCallback(async () => {
    try {
      const r = await listOrgs()
      setOrgs(r.orgs || [])
      setCurrent(r.current || '')
    } catch { /* ignore */ }
  }, [])

  useEffect(() => {
    void load()
    const h = () => void load()
    window.addEventListener(ORG_EVENT, h)
    return () => window.removeEventListener(ORG_EVENT, h)
  }, [load])

  const switchOrg = useCallback(async (code: string) => {
    await setCurrentOrg(code)
    resetSipWebrtcAddrCache()
    setCurrent(code)
    window.dispatchEvent(new Event(ORG_EVENT))
  }, [])

  // 当前机构展示名（orgName 优先，缺省回退 orgCode）。
  const currentOrg = orgs.find((o) => o.orgCode === current)
  const currentName = currentOrg?.orgName || current

  return { orgs, current, currentName, switchOrg, reload: load }
}

// 供其它页（如 OrgsPage）切换后广播，使 topbar 同步。
export function notifyOrgChanged() {
  window.dispatchEvent(new Event(ORG_EVENT))
}
