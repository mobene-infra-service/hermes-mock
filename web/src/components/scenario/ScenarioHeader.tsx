import { Alert, Button, Space, Typography } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import type { ReactNode } from 'react'
import type { PreflightReport } from '../../api'
import { ReadyLabel } from './utils'

const { Title } = Typography

// 业务测试场景页统一页头：标题 + 就绪标签 + 播种/刷新；可选「Hermes 发起·mock 扮被叫」说明条。
export function ScenarioHeader({
  title, ready, bootstrapping, onBootstrap, onReload, note, extra,
}: {
  title: string
  ready?: PreflightReport
  bootstrapping?: boolean
  onBootstrap?: () => void
  onReload?: () => void
  note?: ReactNode // 额外说明
  extra?: ReactNode
}) {
  return (
    <div style={{ marginBottom: 16 }}>
      <Space style={{ width: '100%', justifyContent: 'space-between', flexWrap: 'wrap' }} align="start">
        <Space align="center">
          <Title level={4} style={{ margin: 0 }}>{title}</Title>
          {ready !== undefined && <ReadyLabel report={ready} />}
        </Space>
        <Space>
          {extra}
          {onBootstrap && <Button size="small" type="primary" loading={bootstrapping} onClick={onBootstrap}>播种 mock 客户配置</Button>}
          {onReload && <Button size="small" icon={<ReloadOutlined />} onClick={onReload}>刷新</Button>}
        </Space>
      </Space>
      {note ?? (
        <Alert
          type="info"
          showIcon
          style={{ marginTop: 12 }}
          message="Hermes 业务发起 · mock 扮客户被叫"
          description="由 Hermes 业务侧发起外呼，mock 扮演外部客户被叫线路应答；下方展示每通电话的 Hermes 业务侧与 mock 客户被叫侧状态。"
        />
      )}
    </div>
  )
}
