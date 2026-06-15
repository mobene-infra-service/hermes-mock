import type { ReactNode } from 'react'
import { InfoCircleFilled } from '@ant-design/icons'

// 信息条（Figma InfoBanner）：浅蓝底 + ⓘ 图标 + 标题（深蓝）+ 描述（灰）。
// 替代各页零散的 antd Alert，统一视觉。
export function InfoBanner({ title, children, tone = 'info' }: {
  title: ReactNode
  children?: ReactNode
  tone?: 'info' | 'warning'
}) {
  return (
    <div className={`hm-info-banner is-${tone}`}>
      <InfoCircleFilled className="hm-info-icon" />
      <div className="hm-info-text">
        <div className="hm-info-title">{title}</div>
        {children && <div className="hm-info-desc">{children}</div>}
      </div>
    </div>
  )
}
