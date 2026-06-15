import {
  DashboardOutlined, NodeIndexOutlined, ClusterOutlined, CustomerServiceOutlined,
  BankOutlined, BellOutlined, TeamOutlined, RobotOutlined, SafetyCertificateOutlined,
  PhoneOutlined,
} from '@ant-design/icons'
import type { ReactNode } from 'react'

export interface NavItem {
  key: string // 路由路径
  label: string
  icon: ReactNode
  group: string // 面包屑一级（侧栏分组）
}

export interface NavGroup {
  label?: string // 分组小标题（首组无标题）
  items: NavItem[]
}

// 侧边栏导航模型（对照 Figma 重构稿）：三组——主导航 / 通话测试场景 / 观测。
export const NAV_GROUPS: NavGroup[] = [
  {
    items: [
      { key: '/overview', label: '总览', icon: <DashboardOutlined />, group: '控制台' },
      { key: '/orgs', label: '机构', icon: <BankOutlined />, group: '控制台' },
      { key: '/cluster', label: '客户配置', icon: <ClusterOutlined />, group: '控制台' },
      { key: '/agents', label: '坐席', icon: <TeamOutlined />, group: '控制台' },
    ],
  },
  {
    label: '通话测试场景',
    items: [
      { key: '/agent-call', label: '坐席外呼', icon: <CustomerServiceOutlined />, group: '通话测试场景' },
      { key: '/group-call', label: '群呼任务', icon: <PhoneOutlined />, group: '通话测试场景' },
      { key: '/callbot', label: 'call-bot 外呼', icon: <RobotOutlined />, group: '通话测试场景' },
      { key: '/otp', label: 'OTP 验证码', icon: <SafetyCertificateOutlined />, group: '通话测试场景' },
    ],
  },
  {
    label: '观测',
    items: [
      { key: '/trace', label: '通话链路', icon: <NodeIndexOutlined />, group: '观测' },
      { key: '/callbacks', label: 'Hermes 回调', icon: <BellOutlined />, group: '观测' },
    ],
  },
]

export const ALL_NAV_ITEMS: NavItem[] = NAV_GROUPS.flatMap((g) => g.items)

// 由当前路径找到导航项（面包屑/高亮用）。
export function navItemByPath(pathname: string): NavItem | undefined {
  const seg = '/' + (pathname.split('/')[1] || 'overview')
  return ALL_NAV_ITEMS.find((i) => i.key === seg)
}
