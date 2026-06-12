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

// hermes-ws 工作台推送的消息 action 常量（集中声明，消灭散落字符串）。
export const WsAction = {
  Auth: 'auth',                         // 登录成功，下发 token/rtpengineId
  Status: 'status',                     // 坐席工作状态变更（带时间戳去重）
  NumberInfo: 'numberInfo',             // 号码/回拨信息
  Ping: 'ping',                         // 服务端心跳探测（回 pong）
  Kick: 'kick',                         // 坐席被踢（同号他处登录等）
  GroupCallNotify: 'groupCallNotify',   // 群呼任务/号码进度推送（type 1/2/3/4）
  PongTimeOut: 'PongTimeOut',           // 心跳超时
  CurrentCallUuid: 'currentCallUuid',   // 本通业务 callUuid/callType/客户号（mock 据此关联 hermes 侧 callId 做断言）
  VoicemailNotify: 'voicemailNotify',   // 转语音信箱通知
  CallTrace: 'callTrace',               // 实时对话/通话轨迹（ASR）
  IncomingRouting: 'incomingRouting',   // 来电路由信息
  WarpUpTimeNotify: 'warpUpTimeNotify', // 话后整理时长（注意服务端拼写为 warp）
} as const

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
  callLinkInfo?: (v: unknown) => void // currentCallUuid：hermes 下发本通 callUuid/callType/客户号
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
  private callLinkInfo?: (v: unknown) => void
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
    this.callLinkInfo = params.callLinkInfo
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
      if (!res?.action) return
      switch (res.action) {
        case WsAction.Auth: {
          if (!res.content) return
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
        case WsAction.Status: {
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
        case WsAction.NumberInfo:
          this.callbackInfo?.(res.content)
          return
        case WsAction.Ping:
          this.client?.send(JSON.stringify({ action: 'pong' }))
          return
        case WsAction.Kick:
          this.client?.close()
          this.kick?.(typeof res.msg === 'string' ? res.msg : undefined)
          return
        case WsAction.GroupCallNotify:
          this.groupCallNotify?.(res.content)
          return
        case WsAction.CurrentCallUuid:
          this.callLinkInfo?.(res.content) // 本通业务 callUuid/callType/客户号 → 关联 hermes 侧 callId
          return
        case WsAction.PongTimeOut:
          message.error('工作台连接超时')
          return
        // 其余已知但 mock 暂不专门消费的 action（语音信箱/轨迹/路由/整理时长）显式列出，统一透传 otherEvent，
        // 避免像参考实现那样塞进一个桶后再到组件里拆。要消费时给上面任一加专用回调即可。
        case WsAction.VoicemailNotify:
        case WsAction.CallTrace:
        case WsAction.IncomingRouting:
        case WsAction.WarpUpTimeNotify:
        default:
          this.otherEvent?.(res)
      }
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
