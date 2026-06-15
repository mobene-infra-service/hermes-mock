import type { ReactNode } from 'react'
import { ReloadOutlined } from '@ant-design/icons'
import { Button } from 'antd'

// 页头状态小标签（Figma「Tag / 就绪」）：浅绿底 + 绿点 + 文案；可指定语义色。
export type StatusTone = 'success' | 'info' | 'warning' | 'danger' | 'neutral'

export function StatusPill({ tone = 'success', text }: { tone?: StatusTone; text: ReactNode }) {
  return (
    <span className={`hm-status-pill is-${tone}`}>
      <span className="hm-status-dot" />
      {text}
    </span>
  )
}

// 页头（Figma PageHeader）：标题 + 状态标签 + 右侧操作区（刷新 / 主操作）。
export function PageHeader({
  title, status, onReload, actions, extra,
}: {
  title: ReactNode
  status?: { tone?: StatusTone; text: ReactNode }
  onReload?: () => void
  actions?: ReactNode // 主操作按钮（如「新建任务」）
  extra?: ReactNode // 完全自定义右侧（覆盖 onReload/actions）
}) {
  return (
    <div className="hm-page-header">
      <div className="hm-page-title-group">
        <h1 className="hm-page-title">{title}</h1>
        {status && <StatusPill tone={status.tone} text={status.text} />}
      </div>
      <div className="hm-page-actions">
        {extra ?? (
          <>
            {onReload && (
              <Button icon={<ReloadOutlined />} onClick={onReload}>刷新</Button>
            )}
            {actions}
          </>
        )}
      </div>
    </div>
  )
}
