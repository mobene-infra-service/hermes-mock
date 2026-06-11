// SipCall：mock 坐席软电话核心（jssip 封装，移植自 org-management-temp 并精简掉埋点/全局 store）。
// 流程：getSipWebrtcAddr() 拿 FS 地址 → jssip.UA 用分机号+口令注册 → REGISTERED 后 call() 发 INVITE
// （带 x-call_center_type:OUTBOUND_CALL / x-agent_channel / x-session-id，与真实坐席外呼一致）。
import * as jssip from 'jssip'
import {
  EndEvent,
  IceCandidateEvent,
  IncomingEvent,
  OutgoingEvent,
  PeerConnectionEvent,
  RTCSession,
} from 'jssip/lib/RTCSession'
import { IncomingRTCSessionEvent, OutgoingRTCSessionEvent } from 'jssip/lib/UA'
import { uuidv7 } from 'uuidv7'
import ring from './ring'
import { getSipWebrtcAddr, onAutoOut, onBusy, onDialing, onIdle, onResting } from './request'
import type SipController from './controller'

export interface InitConfig {
  host: string
  port: string
  domain?: string
  proto: boolean // ⚠️ 仅 domain 兜底用到 host；proto/port 在连接路径上被忽略——
  // 实际 ws(s)/host/port 由 getSipWebrtcAddr() 返回的 d.ssl/d.host/d.port 决定（见 initSipConnection）。
  extNo: string
  extPwd: string
  checkMic?: boolean
  autoRegister?: boolean
  debug?: boolean
  stateEventListener: (event: string, data: unknown) => void
  sipController?: SipController
}

type CallDirection = 'outbound' | 'inbound'

export interface CallExtraParam {
  outNumber?: string
  businessId?: string
  /** 线路类型（Hermes 2026-06 特性：选线仅认 X-JLineType 头；空=不发，Hermes 默认 base） */
  lineType?: string
}

export interface CallEndEvent {
  originator: string
  cause: string
  code: number
  answered: boolean
}

export const enum SipState {
  MIC_ERROR = 'MIC_ERROR',
  ERROR = 'ERROR',
  CONNECTED = 'CONNECTED',
  DISCONNECTED = 'DISCONNECTED',
  REGISTERED = 'REGISTERED',
  UNREGISTERED = 'UNREGISTERED',
  REGISTER_FAILED = 'REGISTER_FAILED',
  INCOMING_CALL = 'INCOMING_CALL',
  OUTGOING_CALL = 'OUTGOING_CALL',
  IN_CALL = 'IN_CALL',
  HOLD = 'HOLD',
  UNHOLD = 'UNHOLD',
  CALL_END = 'CALL_END',
  MUTE = 'MUTE',
  UNMUTE = 'UNMUTE',
}

// FreeSWITCH SIP WebSocket in the local stack drops idle browser transports at
// about 60s. Send a legal SIP REGISTER refresh well before that; JsSIP's own
// expiry refresh can otherwise wait until expires-5s or longer.
const REGISTER_EXPIRES_SECONDS = 120
const REGISTER_KEEPALIVE_MS = 25_000

export default class SipCall {
  private constraints = { audio: true, video: false }
  private audioView = document.createElement('audio')
  private ua!: jssip.UA
  private socket: jssip.WebSocketInterface | null = null

  private localAgent: string
  private ringId: string // 每实例独立的振铃音 DOM id（多坐席同页不冲突）
  private outgoingSession: RTCSession | undefined
  private incomingSession: RTCSession | undefined
  private currentSession: RTCSession | undefined
  private direction: CallDirection | undefined
  private currentCallId: string | undefined
  private sipController?: SipController
  private stateEventListener: (event: string, data: unknown) => void
  private closed = false
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private registerKeepaliveTimer: ReturnType<typeof setInterval> | null = null

  // 多坐席同页：去单例，每坐席各 new 一个 SipCall 实例。
  public constructor(config: InitConfig) {
    this.localAgent = config.extNo
    this.ringId = `ringMediaAudio_${config.extNo}`
    if (!config.domain) config.domain = config.host
    this.sipController = config.sipController
    this.stateEventListener = config.stateEventListener
    if (config.checkMic) this.micCheck()
    if (config.debug) jssip.debug.enable('JsSIP:*')
    else jssip.debug.disable()
    if (config.extNo && config.extPwd) this.initSipConnection(config)
    else throw new Error('extNo / extPwd 必填')
  }

  // destroy 关闭本实例（替代旧 destroyInstance 静态方法）。
  public destroy() {
    this.cleanSDK()
  }

  private initSipConnection(config: InitConfig) {
    getSipWebrtcAddr(this.localAgent)
      .then((res) => {
        const d = res?.data
        if (!d) {
          this.onChangeState(SipState.REGISTER_FAILED, { msg: '取 WebRTC 地址失败' })
          return
        }
        const proto = d.ssl ? 'wss' : 'ws'
        const wsServer = `${proto}://${d.host}:${d.port}`
        this.socket = new jssip.WebSocketInterface(wsServer)
        this.ua = new jssip.UA({
          sockets: [this.socket],
          uri: `sip:${config.extNo}@${d.host}`,
          password: config.extPwd,
          register: false,
          register_expires: REGISTER_EXPIRES_SECONDS,
          connection_recovery_min_interval: 2,
          connection_recovery_max_interval: 10,
          session_timers: false,
          user_agent: 'JsSIP 3.10',
        })

        this.ua.on('connected', () => {
          this.clearReconnectTimer()
          this.onChangeState(SipState.CONNECTED, null)
          if (config.autoRegister) this.ua.register()
        })
        this.ua.on('disconnected', (e: { error?: boolean; reason?: string; code?: number }) => {
          if (this.closed) return
          this.stopRegisterKeepalive()
          this.onChangeState(SipState.DISCONNECTED, { reason: e?.reason || '', code: e?.code, error: !!e?.error })
          this.scheduleReconnect()
        })
        this.ua.on('registered', () => {
          this.startRegisterKeepalive()
          this.onChangeState(SipState.REGISTERED, { localAgent: this.localAgent })
        })
        this.ua.on('unregistered', () => {
          if (this.closed) return
          this.stopRegisterKeepalive()
          this.onChangeState(SipState.UNREGISTERED, { localAgent: this.localAgent })
        })
        this.ua.on('registrationFailed', (e: { cause?: string }) => {
          this.stopRegisterKeepalive()
          this.onChangeState(SipState.REGISTER_FAILED, { msg: '注册失败:' + e.cause })
          this.ua.stop()
          this.socket = null
        })
        this.ua.on('registrationExpiring', () => this.ua.register())

        this.ua.on('newRTCSession', (data: IncomingRTCSessionEvent | OutgoingRTCSessionEvent) => {
          const s = data.session
          let currentEvent: string
          if (data.originator === 'remote') {
            this.incomingSession = data.session
            this.currentSession = this.incomingSession
            this.currentCallId = data.request.getHeader('x-session-id')
            this.direction = 'inbound'
            currentEvent = SipState.INCOMING_CALL
            this.playAudio()
          } else {
            this.direction = 'outbound'
            currentEvent = SipState.OUTGOING_CALL
            this.playAudio()
          }

          s.on('peerconnection', (evt: PeerConnectionEvent) => this.handleAudio(evt.peerconnection))
          s.on('icecandidate', (evt: IceCandidateEvent) => {
            if (evt.candidate.type === 'srflx' || evt.candidate.type === 'relay') evt.ready()
          })
          s.on('progress', (evt: IncomingEvent | OutgoingEvent) => {
            if ([180, 183].includes((evt as OutgoingEvent)?.response?.status_code)) onDialing(this.localAgent).catch(() => {})
            this.onChangeState(currentEvent, {
              direction: this.direction,
              otherLegNumber:
                data.originator === 'remote' ? data.request.from.uri.user : data.request.to.uri.user,
              callId: this.currentCallId,
            })
          })
          s.on('accepted', () => {
            this.stopAudio()
            this.onChangeState(SipState.IN_CALL, null)
          })
          s.on('ended', (evt: EndEvent) => {
            this.stopAudio()
            this.cleanCallingData()
            this.onChangeState(SipState.CALL_END, {
              answered: true,
              cause: evt.cause,
              code: (evt.message as { status_code?: number } | null)?.status_code ?? 0,
              originator: evt.originator,
            } as CallEndEvent)
          })
          s.on('failed', (evt: EndEvent) => {
            this.stopAudio()
            this.cleanCallingData()
            this.onChangeState(SipState.CALL_END, {
              answered: false,
              cause: evt.cause,
              code: (evt.message as { status_code?: number } | null)?.status_code ?? 0,
              originator: evt.originator,
            } as CallEndEvent)
          })
          s.on('hold', () => this.onChangeState(SipState.HOLD, null))
          s.on('unhold', () => this.onChangeState(SipState.UNHOLD, null))
        })

        this.ua.start()
      })
      .catch((err) => {
        console.error('取 SIP 地址失败:', err)
        this.onChangeState(SipState.REGISTER_FAILED, { msg: String(err) })
      })
  }

  private scheduleReconnect() {
    this.clearReconnectTimer()
    this.reconnectTimer = setTimeout(() => {
      if (this.closed || !this.ua || this.ua.isConnected()) return
      try {
        this.ua.start()
      } catch (err) {
        this.onChangeState(SipState.ERROR, { msg: `SIP 重连失败：${String(err)}` })
      }
    }, 2000)
  }

  private clearReconnectTimer() {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }

  private startRegisterKeepalive() {
    if (this.registerKeepaliveTimer) return
    this.registerKeepaliveTimer = setInterval(() => {
      if (this.closed || !this.ua || !this.ua.isConnected()) return
      try {
        this.ua.register()
      } catch (err) {
        this.onChangeState(SipState.ERROR, { msg: `SIP 保活 REGISTER 失败：${String(err)}` })
      }
    }, REGISTER_KEEPALIVE_MS)
  }

  private stopRegisterKeepalive() {
    if (this.registerKeepaliveTimer) {
      clearInterval(this.registerKeepaliveTimer)
      this.registerKeepaliveTimer = null
    }
  }

  private handleAudio(pc: RTCPeerConnection) {
    this.audioView.autoplay = true
    if ('ontrack' in pc) {
      pc.ontrack = (media) => {
        if (media.streams.length > 0 && media.streams[0].active) {
          this.audioView.srcObject = media.streams[0]
        }
      }
    }
  }

  private cleanCallingData() {
    this.outgoingSession = undefined
    this.incomingSession = undefined
    this.currentSession = undefined
    this.direction = undefined
    this.currentCallId = ''
  }

  private onChangeState(event: string, data: unknown) {
    if (this.closed) return
    this.stateEventListener?.(event, data)
  }

  private checkCurrentCallIsActive(): boolean {
    if (!this.currentSession || !this.currentSession.isEstablished()) {
      this.onChangeState(SipState.ERROR, { msg: '当前通话不存在或已销毁，无法执行该操作。' })
      return false
    }
    return true
  }

  public register() {
    if (this.ua && this.ua.isConnected()) this.ua.register()
    else this.onChangeState(SipState.ERROR, { msg: 'websocket 尚未连接' })
  }

  public unregister() {
    if (this.ua && this.ua.isConnected() && this.ua.isRegistered()) {
      this.ua.unregister({ all: true })
      this.socket = null
      this.cleanSDK()
    }
  }

  private checkPhoneNumber(phone: string): boolean {
    if (phone && phone.toUpperCase().includes('INTE')) return true
    return /^\d+$/.test(phone) && phone.length <= 15
  }

  // call 发起外呼：注入与真实坐席外呼一致的业务头，FS/call-center 据此选线 bridge。
  public call(phone: string, param: CallExtraParam = {}): string | undefined {
    if (!this.checkPhoneNumber(phone)) throw new Error('号码格式不正确')
    this.micCheck()
    if (this.currentSession && !this.currentSession.isEnded()) throw new Error('当前通话尚未结束')
    this.currentCallId = uuidv7().replace(/-/g, '')
    if (this.ua && this.ua.isRegistered()) {
      const extraHeaders: string[] = ['X-JCallId: ' + this.currentCallId]
      if (this.sipController && this.sipController.rtpId) extraHeaders.push('x-rtp-id: ' + this.sipController.rtpId)
      extraHeaders.push('x-session-id: ' + `CCMDL${this.currentCallId}`)
      if (param.businessId) extraHeaders.push('X-JBusinessId: ' + param.businessId)
      if (param.outNumber) extraHeaders.push('X-JOutNumber: ' + param.outNumber)
      if (param.lineType) extraHeaders.push('X-JLineType: ' + param.lineType) // 按 type 选线（cc6251c：后端仅认此头）
      extraHeaders.push('x-call_center_type: ' + 'OUTBOUND_CALL')
      extraHeaders.push('x-agent_channel: ' + this.localAgent)
      this.outgoingSession = this.ua.call(phone, {
        eventHandlers: {
          peerconnection: (e: { peerconnection: RTCPeerConnection }) => this.handleAudio(e.peerconnection),
        },
        mediaConstraints: this.constraints,
        extraHeaders,
        sessionTimersExpires: 120,
      })
      this.currentSession = this.outgoingSession
      return this.currentCallId
    }
    this.onChangeState(SipState.ERROR, { msg: '请在注册成功后再外呼' })
    return ''
  }

  public answer() {
    if (this.currentSession && this.currentSession.isInProgress())
      this.currentSession.answer({ mediaConstraints: this.constraints })
    else this.onChangeState(SipState.ERROR, { msg: '通话尚未建立，无法应答' })
  }

  public hangup() {
    if (this.currentSession && !this.currentSession.isEnded()) this.currentSession.terminate()
    else this.onChangeState(SipState.ERROR, { msg: '当前无通话可挂断' })
  }

  public hold() {
    if (this.currentSession && this.checkCurrentCallIsActive()) this.currentSession.hold()
  }

  // isRegistered 本实例是否已注册到 FS（供容器判断坐席卡是否就绪可派号）。
  public isRegistered(): boolean {
    return !!(this.ua && this.ua.isRegistered())
  }

  public unhold() {
    if (this.currentSession && this.checkCurrentCallIsActive() && this.currentSession.isOnHold())
      this.currentSession.unhold()
  }

  public mute() {
    if (this.currentSession && this.checkCurrentCallIsActive()) {
      this.currentSession.mute()
      this.onChangeState(SipState.MUTE, null)
    }
  }

  public unmute() {
    if (this.currentSession && this.checkCurrentCallIsActive()) {
      this.currentSession.unmute()
      this.onChangeState(SipState.UNMUTE, null)
    }
  }

  public transfer(phone: string) {
    if (this.currentSession && this.checkCurrentCallIsActive()) this.currentSession.refer(phone)
  }

  public sendDtmf(tone: string) {
    if (this.currentSession) this.currentSession.sendDTMF(tone, { duration: 160, interToneGap: 1200 })
  }

  public micCheck() {
    navigator.permissions
      ?.query({ name: 'microphone' as PermissionName })
      .then((result) => {
        if (result.state === 'denied') {
          this.onChangeState(SipState.MIC_ERROR, { msg: '麦克风权限被禁用，请允许使用麦克风' })
          return
        }
        if (navigator.mediaDevices == undefined) {
          this.onChangeState(SipState.MIC_ERROR, { msg: '无法访问麦克风（需 HTTPS 或 localhost）' })
          return
        }
        navigator.mediaDevices
          .getUserMedia({ video: false, audio: true })
          .then((s) => s.getTracks().forEach((t) => t.stop()))
          .catch(() => this.onChangeState(SipState.MIC_ERROR, { msg: '麦克风检测异常' }))
      })
      .catch(() => {})
  }

  public static async getMediaDeviceInfo() {
    if (navigator.mediaDevices == null) return []
    return navigator.mediaDevices.enumerateDevices()
  }

  public setResting() {
    return onResting(this.localAgent)
  }
  public setIdle() {
    return onIdle(this.localAgent)
  }
  public setBusy() {
    return onBusy(this.localAgent)
  }
  public setAutoOut() {
    return onAutoOut(this.localAgent)
  }

  public playAudio() {
    if (!ring) return
    let el = document.getElementById(this.ringId) as HTMLAudioElement | null
    if (!el) {
      el = document.createElement('audio')
      el.id = this.ringId
      el.hidden = true
      el.src = ring
      el.loop = true
      document.body.appendChild(el)
    }
    el.play().catch(() => {})
  }

  public stopAudio() {
    const el = document.getElementById(this.ringId)
    if (el && el.parentNode) el.parentNode.removeChild(el)
  }

  private cleanSDK() {
    this.closed = true
    this.clearReconnectTimer()
    this.stopRegisterKeepalive()
    this.stopAudio()
    // 清理远端音频流：停轨 + 解绑 srcObject，避免断开/卸载后游离 audio 仍引用远端流累积。
    try {
      const ms = this.audioView.srcObject as MediaStream | null
      if (ms) ms.getTracks().forEach((t) => t.stop())
      this.audioView.srcObject = null
    } catch { /* ignore */ }
    this.cleanCallingData()
    if (this.ua) this.ua.stop()
    this.socket = null
  }
}
