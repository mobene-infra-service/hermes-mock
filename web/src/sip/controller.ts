// SipController：mock 坐席软电话的 hermes-ws 工作台客户端（移植自 org-management-temp，
// 改为用 `login` action + md5 加盐口令上线，不依赖 HTTP 登录拿 token）。
//
// 连 wss?://{host}:{port}/agent-workbench/api/ws（经 mock 后端反代到 hermes-ws）：
//   发 {action:'login', params:{username, password:md5(ts+明文口令+nonce), timestamp, nonce}}
//   收 {action:'auth', content:{token, rtpengineId, ...}} 即上线 → 触发 loginHook（再做 jssip 注册）
//   每 2s 发 {action:'ping'} 保活。
import { message } from 'antd'
import md5 from 'blueimp-md5'
import { setAgentToken, clearAgentToken } from './request'

export interface SipControllerParams {
  proto?: boolean
  host?: string
  port?: string
  username?: string
  password?: string
  kick?: (msg?: string) => void
  statusListener?: (v: number) => void
  callbackInfo?: (v: unknown) => void
  groupCallNotify?: (v: unknown) => void
  otherEvent?: (v: unknown) => void
  onOpenHook?: () => void // 工作台 WS 物理连上（尚未登录）——用于细化连接阶段态
  loginHook?: () => void // 收到 auth（登录成功）——只在首次触发，避免重复 auth 重建实例
  onCloseHook?: (info?: CloseInfo) => void // WS 关闭，带关闭码/原因供 UI 提示
}

// CloseInfo WS 关闭信息（code/reason 透传给 UI，避免静默）。
export interface CloseInfo {
  code: number
  reason: string
  wasClean: boolean
}

class SipController {
  client: WebSocket | null = null
  agentStatus: number = 1
  loginStatus: boolean = false
  exitStatus: boolean = false
  beforeStatusTimestamp: { timestamp?: number; content?: number } | null = null
  rtpId: string = ''
  loginInfo: { username: string; password: string }
  auth: { token: string; refreshToken: string; expireAt: number } = {
    token: '',
    refreshToken: '',
    expireAt: 0,
  }
  private heartbeatTimer: ReturnType<typeof setTimeout> | null = null
  private host?: string
  private port?: string
  private proto?: boolean
  private kick?: (msg?: string) => void
  private statusListener?: (v: number) => void
  private callbackInfo?: (v: unknown) => void
  private groupCallNotify?: (v: unknown) => void
  private otherEvent?: (v: unknown) => void
  private onOpenHook?: () => void
  private loginHook?: () => void
  private onCloseHook?: (info?: CloseInfo) => void

  // 多坐席同页：去单例，每坐席各 new 一个 SipController 实例。
  public constructor(params: SipControllerParams) {
    this.proto = params.proto
    this.host = params.host
    this.port = params.port
    this.kick = params.kick
    this.statusListener = params.statusListener
    this.callbackInfo = params.callbackInfo
    this.groupCallNotify = params.groupCallNotify
    this.otherEvent = params.otherEvent
    this.onOpenHook = params.onOpenHook
    this.loginHook = params.loginHook
    this.onCloseHook = params.onCloseHook
    this.loginInfo = { username: params.username || '', password: params.password || '' }
    this.initWebSocket()
  }

  // destroy 关闭本实例（替代旧 destroyInstance 静态方法）。
  public destroy() {
    this.logout()
  }

  private initWebSocket() {
    if (!this.host || !this.port) return
    if (this.client) this.client.close()
    const baseUrl =
      (this.proto ? 'wss' : 'ws') + '://' + this.host + ':' + this.port + '/agent-workbench/api/ws'
    this.client = new WebSocket(baseUrl)
    this.listen()
  }

  private listen() {
    if (!this.client) {
      message.error('WebSocket 连接未初始化')
      return
    }
    this.client.onopen = () => {
      console.log('工作台 WS 已连接')
      this.onOpenHook?.()
      this.login()
    }
    this.client.onmessage = (event: MessageEvent) => {
      let res: { action?: string; content?: unknown; timestamp?: number; msg?: string }
      try {
        res = JSON.parse(event.data)
      } catch {
        return
      }
      if (res?.action === 'auth' && res?.content) {
        const c = res.content as { token?: string; refreshToken?: string; expireAt?: number; rtpengineId?: string }
        this.auth.token = c.token || ''
        this.auth.refreshToken = c.refreshToken || ''
        this.auth.expireAt = c.expireAt || 0
        setAgentToken(this.loginInfo.username, this.auth.token) // SDK REST 鉴权（authorization 头）
        this.rtpId = c.rtpengineId || ''
        // 仅在首次登录成功时触发 loginHook：hermes-ws 在 token 刷新/重连时可能重发 auth，
        // 若每次都触发会重复 new SipCall、旧 UA/socket 泄漏 + 信令打架。
        const firstAuth = !this.loginStatus
        this.loginStatus = true
        if (firstAuth) this.loginHook?.()
        return
      }
      if (res?.action === 'status') {
        if (!res?.timestamp) return
        const content = res.content as number
        if (this.beforeStatusTimestamp) {
          if (Number(this.beforeStatusTimestamp.timestamp) < Number(res.timestamp)) {
            this.beforeStatusTimestamp = { timestamp: res.timestamp, content }
            this.agentStatus = content
            this.statusListener?.(content)
          } else {
            this.statusListener?.(this.beforeStatusTimestamp.content as number)
          }
        } else {
          this.beforeStatusTimestamp = { timestamp: res.timestamp, content }
          this.agentStatus = content
          this.statusListener?.(content)
        }
        return
      }
      if (res?.action === 'numberInfo') {
        this.callbackInfo?.(res.content)
        return
      }
      if (res?.action === 'ping') {
        this.client?.send(JSON.stringify({ action: 'pong' }))
        return
      }
      if (res?.action === 'kick') {
        this.client?.close()
        this.kick?.(typeof res.msg === 'string' ? res.msg : undefined)
        return
      }
      if (res?.action === 'groupCallNotify') {
        this.groupCallNotify?.(res.content)
        return
      }
      if (res?.action === 'PongTimeOut') {
        message.error('工作台连接超时')
        return
      }
      if (res?.action) this.otherEvent?.(res)
    }
    this.client.onclose = (event) => {
      console.log('工作台 WS 已关闭', event.code, event.reason)
      this.loginStatus = false
      this.statusListener?.(1)
      this.clearHeartbeat()
      this.onCloseHook?.({ code: event.code, reason: event.reason, wasClean: event.wasClean })
    }
    this.client.onerror = () => {
      // WS 物理错误：以 close 形式告知 UI（onerror 的 Event 不带原因），避免只 console 静默
      console.error('工作台 WS 错误')
      this.onCloseHook?.({ code: 0, reason: 'WebSocket 连接错误', wasClean: false })
    }
  }

  // login 用 md5 加盐口令上线（对齐 hermes-ws 的 login action；mock 后端 wsagent 同款）。
  public login() {
    const { username, password } = this.loginInfo
    const timestamp = String(Date.now())
    const nonce = Math.random().toString(36).slice(2, 10)
    const sign = md5(timestamp + password + nonce)
    this.client?.send(
      JSON.stringify({ action: 'login', params: { username, password: sign, timestamp, nonce } }),
    )
    this.heartBeat()
  }

  public heartBeat() {
    if (this.client?.readyState === WebSocket.OPEN) {
      this.client.send(JSON.stringify({ action: 'ping' }))
      this.heartbeatTimer = setTimeout(() => this.heartBeat(), 2000)
    }
  }

  private clearHeartbeat() {
    if (this.heartbeatTimer) {
      clearTimeout(this.heartbeatTimer)
      this.heartbeatTimer = null
    }
  }

  public logout() {
    this.exitStatus = true
    this.auth.token = ''
    clearAgentToken(this.loginInfo.username)
    if (this.client?.readyState === WebSocket.OPEN) {
      this.client.send(JSON.stringify({ action: 'logout', actionId: '' }))
    }
    this.clearHeartbeat()
    this.client?.close()
  }
}

export default SipController
