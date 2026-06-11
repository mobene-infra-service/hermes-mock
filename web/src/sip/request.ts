import md5 from 'blueimp-md5'

// mock 前端坐席软电话调 Hermes call-center 的 REST。
// 全部经 mock 后端反向代理（/call-center/* → call-center），反代按当前机构注入 ORG_CODE_KEY，
// 并把前端带的 X-Agent-Number 转成 AGENT_NUMBER_KEY/AGENT_CODE_KEY；前端只需指明坐席号即可。
//
// 多坐席同页：agent 号作为**显式参数**逐次传入（不再用模块级全局），避免多坐席并发串号。

export interface HermesResp<T = unknown> {
  code?: number
  msg?: string
  data?: T
}

// 工作台 token 表（坐席号 → WS 登录签发的 token）。多坐席同页按号隔离；
// SDK REST 与真实工作台前端一致带 authorization 头（网关模式必需，直连模式服务端忽略）。
const agentTokens = new Map<string, string>()
export const setAgentToken = (agent: string, token: string) => { if (agent) agentTokens.set(agent, token) }
export const clearAgentToken = (agent: string) => { agentTokens.delete(agent) }

function clip(s: string, n = 160): string {
  const oneLine = s.replace(/\s+/g, ' ').trim()
  return oneLine.length > n ? `${oneLine.slice(0, n)}...` : oneLine
}

function apiShapeHint(text: string): string {
  if (text.trimStart().startsWith('<')) {
    return '返回了 HTML 页面，请检查机构配置里的 callCenterUrl / gatewayUrl 是否指向 Hermes call-center API（不要指到管理前端）。'
  }
  return `响应不是 JSON：${clip(text)}`
}

async function callCenter<T = unknown>(agent: string, path: string, method: 'GET' | 'POST', body?: unknown): Promise<HermesResp<T>> {
  const r = await fetch('/call-center/agent-workbench/sdk' + path, {
    method,
    headers: {
      'Content-Type': 'application/json',
      ...(agent ? { 'X-Agent-Number': agent } : {}),
      ...(agentTokens.get(agent) ? { authorization: agentTokens.get(agent) as string } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await r.text()
  if (!r.ok) throw new Error(`${path}: HTTP ${r.status}${text ? ` ${clip(text)}` : ''}`)
  try {
    return text ? JSON.parse(text) : {}
  } catch {
    throw new Error(`${path}: ${apiShapeHint(text)}`)
  }
}

export interface SipAddr {
  host: string
  port: number | string
  ssl: boolean
}

// getSipWebrtcAddr 取 FreeSWITCH 的 WebRTC 接入地址（host/port/ssl），jssip 据此连 ws(s) 注册。
export const getSipWebrtcAddr = (agent: string) => callCenter<SipAddr>(agent, '/agent/webrtc/addr', 'GET')

// switchStatus 切某坐席工作状态（AgentStatusEnum code：2在线/5呼叫中/6小休/7忙/9自动外呼）。
export const switchStatus = (agent: string, action: number) => callCenter(agent, '/agent/status/switch', 'POST', { action })
export const onDialing = (agent: string) => switchStatus(agent, 5)
export const onIdle = (agent: string) => switchStatus(agent, 2)
export const onResting = (agent: string) => switchStatus(agent, 6)
export const onBusy = (agent: string) => switchStatus(agent, 7)
export const onAutoOut = (agent: string) => switchStatus(agent, 9) // 自动外呼态（群呼可分配）

// markSipReady 标记坐席「SIP 在线」（call-center onlineSipClient，约 45s TTL，需周期刷新）。
// FreeSWITCH 静态 directory 注册不会通知 call-center，故由前端按 /public/auth/sip 的 digest 规则
// 补标记，使 call-center 的 agentIsReady(ws && sip) 成立、坐席得以切在线并外呼。realm 对齐 wsagent。
export async function markSipReady(number: string, password: string): Promise<boolean> {
  const realm = 'hermes'
  const nonce = Math.random().toString(16).slice(2, 10)
  const uri = `sip:${number}@hermes`
  const ha1 = md5(`${number}:${realm}:${password}`)
  const ha2 = md5(`REGISTER:${uri}`)
  const response = md5(`${ha1}:${nonce}:${ha2}`)
  const r = await fetch('/call-center/public/auth/sip', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ authorization: '', algorithm: 'MD5', username: number, realm, nonce, uri, response }),
  })
  const text = await r.text()
  if (!r.ok) throw new Error(`/public/auth/sip: HTTP ${r.status}${text ? ` ${clip(text)}` : ''}`)
  if (text.trimStart().startsWith('<')) throw new Error(`/public/auth/sip: ${apiShapeHint(text)}`)
  return text.trim() === '1'
}
