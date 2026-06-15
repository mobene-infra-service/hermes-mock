import { useLocation, Link } from 'react-router-dom'
import { Dropdown, message } from 'antd'
import { DownOutlined } from '@ant-design/icons'
import { navItemByPath } from './nav'
import { useCurrentOrg } from './useCurrentOrg'

// 顶栏（对照 Figma 重构稿）：左面包屑（分组 / 当前页）+ 右机构切换器（绿点+当前机构+下拉）+ 头像。
export default function TopBar() {
  const loc = useLocation()
  const nav = navItemByPath(loc.pathname)
  const { orgs, current, currentName, switchOrg } = useCurrentOrg()

  const onSwitch = async (code: string) => {
    if (code === current) return
    try {
      await switchOrg(code)
      message.success(`当前机构 → ${code}`)
    } catch (e) {
      message.error(String(e))
    }
  }

  const items = orgs.length
    ? orgs.map((o) => ({
      key: o.orgCode,
      label: `${o.orgCode}${o.orgName ? ' · ' + o.orgName : ''}`,
    }))
    : [{ key: '_none', label: '未配置机构', disabled: true }]

  return (
    <header className="hm-topbar">
      <div className="hm-breadcrumb">
        <span className="hm-bc-group">{nav?.group || '控制台'}</span>
        <span className="hm-bc-sep">/</span>
        <span className="hm-bc-current">{nav?.label || '总览'}</span>
      </div>
      <div className="hm-topbar-right">
        <Dropdown
          trigger={['click']}
          menu={{ items, onClick: ({ key }) => key !== '_none' && onSwitch(key) }}
        >
          <button className="hm-org-switcher" type="button">
            <span className="hm-org-dot" />
            <span className="hm-org-text">当前机构：{currentName || '未选择'}</span>
            <DownOutlined className="hm-org-caret" />
          </button>
        </Dropdown>
        <Link to="/orgs" className="hm-avatar" title="机构 / 账户">
          {(current || 'H').slice(0, 1).toUpperCase()}
        </Link>
      </div>
    </header>
  )
}
