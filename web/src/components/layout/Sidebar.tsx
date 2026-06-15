import { Link, useLocation } from 'react-router-dom'
import { NAV_GROUPS } from './nav'

// 侧边栏（对照 Figma 重构稿）：深色 slate #0f172a，品牌 LogoMark + 三组导航。
// 自绘（非 antd Menu）以贴合设计：分组小标题、active 蓝底、icon+label 40px 行高。
export default function Sidebar() {
  const loc = useLocation()
  const selected = '/' + (loc.pathname.split('/')[1] || 'overview')
  return (
    <aside className="hm-sidebar">
      <Link to="/overview" className="hm-brand">
        <span className="hm-logo">H</span>
        <span className="hm-brand-name">hermes-mock</span>
      </Link>

      <nav className="hm-nav">
        {NAV_GROUPS.map((group, gi) => (
          <div className="hm-nav-group" key={group.label || `g${gi}`}>
            {group.label && <div className="hm-nav-group-label">{group.label}</div>}
            {group.items.map((item) => {
              const active = selected === item.key
              return (
                <Link
                  key={item.key}
                  to={item.key}
                  className={`hm-nav-item${active ? ' is-active' : ''}`}
                >
                  <span className="hm-nav-icon">{item.icon}</span>
                  <span className="hm-nav-label">{item.label}</span>
                </Link>
              )
            })}
          </div>
        ))}
      </nav>
    </aside>
  )
}
