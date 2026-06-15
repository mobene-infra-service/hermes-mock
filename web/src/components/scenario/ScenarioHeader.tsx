import { Button } from 'antd'
import type { ReactNode } from 'react'
import type { PreflightReport } from '../../api'
import { PageHeader, type StatusTone } from '../layout/PageHeader'
import { InfoBanner } from '../layout/InfoBanner'

// preflight 报告 → 页头状态小标签的 tone/文案。
function readyStatus(report?: PreflightReport): { tone: StatusTone; text: string } | undefined {
  if (!report) return undefined
  const fails = report.checks.filter((c) => c.status === 'FAIL').length
  const warns = report.checks.filter((c) => c.status === 'WARN').length
  if (report.ready) return { tone: warns ? 'warning' : 'success', text: warns ? `就绪 · ${warns} 提示` : '就绪' }
  return { tone: 'danger', text: `未就绪 · ${fails} 项缺失` }
}

// 业务测试场景页统一页头：标题 + 就绪标签 + 播种/刷新（PageHeader）+「Hermes 发起·mock 扮被叫」说明条（InfoBanner）。
export function ScenarioHeader({
  title, ready, bootstrapping, onBootstrap, onReload, note, extra, infoTitle, infoDesc,
}: {
  title: string
  ready?: PreflightReport
  bootstrapping?: boolean
  onBootstrap?: () => void
  onReload?: () => void
  note?: ReactNode // 传入则完全替代默认说明条
  extra?: ReactNode
  infoTitle?: ReactNode
  infoDesc?: ReactNode
}) {
  return (
    <>
      <PageHeader
        title={title}
        status={readyStatus(ready)}
        onReload={onReload}
        actions={(
          <>
            {extra}
            {onBootstrap && (
              <Button type="primary" loading={bootstrapping} onClick={onBootstrap}>播种 mock 客户配置</Button>
            )}
          </>
        )}
      />
      {note ?? (
        <InfoBanner title={infoTitle ?? 'Hermes 业务发起 · mock 扮客户被叫'}>
          {infoDesc ?? '由 Hermes 业务侧发起外呼，mock 扮演外部客户被叫线路应答；下方展示每通电话的 Hermes 业务侧与 mock 客户被叫侧状态。'}
        </InfoBanner>
      )}
    </>
  )
}
