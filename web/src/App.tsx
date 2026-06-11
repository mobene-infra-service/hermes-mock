import { Layout, Menu, Typography } from 'antd'
import { Routes, Route, Link, useLocation, Navigate } from 'react-router-dom'
import {
  DashboardOutlined, NodeIndexOutlined, ClusterOutlined, CustomerServiceOutlined,
  BankOutlined, BellOutlined, TeamOutlined, RobotOutlined,
  SafetyCertificateOutlined,
} from '@ant-design/icons'
import OverviewPage from './pages/OverviewPage'
import CallTracePage from './pages/CallTracePage'
import ClusterPage from './pages/ClusterPage'
import AgentsPage from './pages/AgentsPage'
import OrgsPage from './pages/OrgsPage'
import CallbacksPage from './pages/CallbacksPage'
import GroupCallPage from './pages/GroupCallPage'
import CallbotPage from './pages/CallbotPage'
import OtpPage from './pages/OtpPage'
import AgentSoftphone from './components/AgentSoftphone'

const { Text } = Typography

const menuItems = [
  { key: '/overview', icon: <DashboardOutlined />, label: <Link to="/overview">总览</Link> },
  { key: '/orgs', icon: <BankOutlined />, label: <Link to="/orgs">机构</Link> },
  { key: '/cluster', icon: <ClusterOutlined />, label: <Link to="/cluster">客户配置</Link> },
  { key: '/agents', icon: <TeamOutlined />, label: <Link to="/agents">坐席</Link> },
  {
    key: 'scenarios', icon: <CustomerServiceOutlined />, label: '通话测试场景',
    children: [
      { key: '/agent-call', icon: <CustomerServiceOutlined />, label: <Link to="/agent-call">坐席外呼</Link> },
      { key: '/group-call', icon: <TeamOutlined />, label: <Link to="/group-call">群呼任务</Link> },
      { key: '/callbot', icon: <RobotOutlined />, label: <Link to="/callbot">call-bot 外呼</Link> },
      { key: '/otp', icon: <SafetyCertificateOutlined />, label: <Link to="/otp">OTP 验证码</Link> },
    ],
  },
  { key: '/trace', icon: <NodeIndexOutlined />, label: <Link to="/trace">通话链路</Link> },
  { key: '/callbacks', icon: <BellOutlined />, label: <Link to="/callbacks">Hermes 回调</Link> },
]

export default function App() {
  const loc = useLocation()
  const selected = '/' + (loc.pathname.split('/')[1] || 'overview')
  const onAgentCall = loc.pathname === '/agent-call'
  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Layout.Header
        style={{
          display: 'flex', alignItems: 'center', justifyContent: 'space-between',
          background: '#001529', position: 'sticky', top: 0, zIndex: 100,
        }}
      >
        <span style={{ display: 'flex', alignItems: 'baseline', gap: 12 }}>
          <span style={{ color: '#fff', fontSize: 18, fontWeight: 700, letterSpacing: 1 }}>hermes-mock</span>
          <Text style={{ color: 'rgba(255,255,255,0.45)', fontSize: 13 }}>
            Hermes 通话业务测试台 · 可编程被叫线路
          </Text>
        </span>
      </Layout.Header>
      <Layout>
        <Layout.Sider width={200} theme="light" style={{ borderRight: '1px solid #f0f0f0' }}>
          <Menu
            mode="inline"
            selectedKeys={[selected]}
            defaultOpenKeys={['scenarios']}
            items={menuItems}
            style={{ height: '100%', borderRight: 0 }}
          />
        </Layout.Sider>
        <Layout.Content style={{ background: '#f0f2f5' }}>
          {/* 坐席软电话：常驻单例，只在 /agent-call 显示，切到别的页用 display:none 隐藏（不卸载→坐席不掉线）。
              这样「先让坐席在线 → 去群呼/callbot 页发起任务 → 坐席被转接接听」可行。 */}
          <div className="page-container" style={{ display: onAgentCall ? 'block' : 'none' }}>
            <AgentSoftphone />
          </div>
          {/* 其它页面正常路由；/agent-call 的内容由上面常驻层提供，故路由占位为空 */}
          <div style={{ display: onAgentCall ? 'none' : 'block' }}>
            <Routes>
              <Route path="/" element={<Navigate to="/overview" replace />} />
              <Route path="/overview" element={<OverviewPage />} />
              <Route path="/orgs" element={<OrgsPage />} />
              <Route path="/cluster" element={<ClusterPage />} />
              <Route path="/agents" element={<AgentsPage />} />
              <Route path="/agent-call" element={null} />
              <Route path="/group-call" element={<GroupCallPage />} />
              <Route path="/callbot" element={<CallbotPage />} />
              <Route path="/otp" element={<OtpPage />} />
              {/* 旧路由兼容 */}
              <Route path="/call-records" element={<Navigate to="/overview" replace />} />
              <Route path="/call-scenarios" element={<Navigate to="/agent-call" replace />} />
              <Route path="/trace" element={<CallTracePage />} />
              <Route path="/callbacks" element={<CallbacksPage />} />
              <Route path="*" element={<Navigate to="/overview" replace />} />
            </Routes>
          </div>
        </Layout.Content>
      </Layout>
    </Layout>
  )
}
