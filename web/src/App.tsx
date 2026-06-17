import { Routes, Route, useLocation, Navigate } from 'react-router-dom'
import OverviewPage from './pages/OverviewPage'
import CallTracePage from './pages/CallTracePage'
import ClusterPage from './pages/ClusterPage'
import AgentsPage from './pages/AgentsPage'
import OrgsPage from './pages/OrgsPage'
import CallbacksPage from './pages/CallbacksPage'
import GroupCallPage from './pages/GroupCallPage'
import CallbotPage from './pages/CallbotPage'
import OtpPage from './pages/OtpPage'
import AgentCallSdkPage from './pages/AgentCallSdkPage'
import AgentSoftphone from './components/AgentSoftphone'
import Sidebar from './components/layout/Sidebar'
import TopBar from './components/layout/TopBar'

export default function App() {
  const loc = useLocation()
  const onAgentCall = loc.pathname === '/agent-call'
  return (
    <div className="hm-shell">
      <Sidebar />
      <div className="hm-main">
        <TopBar />
        <main className="hm-content">
          {/* 坐席软电话：常驻单例，只在 /agent-call 显示，切到别的页用 display:none 隐藏（不卸载→坐席不掉线）。
              这样「先让坐席在线 → 去群呼/callbot 页发起任务 → 坐席被转接接听」可行。 */}
          <div style={{ display: onAgentCall ? 'block' : 'none' }}>
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
              <Route path="/agent-call-sdk" element={<AgentCallSdkPage />} />
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
        </main>
      </div>
    </div>
  )
}
