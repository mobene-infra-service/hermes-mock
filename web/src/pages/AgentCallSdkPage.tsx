import { useEffect, useRef, useState } from 'react'
import {
  AutoComplete, Button, Card, Col, Divider, Form, Input, InputNumber, Row, Select, Space, Switch, Tag, Typography, message,
} from 'antd'
import { ApiOutlined, PhoneOutlined, PoweroffOutlined } from '@ant-design/icons'
import { PageHeader } from '../components/layout/PageHeader'
import { InfoBanner } from '../components/layout/InfoBanner'

const { Text, Paragraph } = Typography

// ===== 官方 SIP Dialpad SDK 接入 =====
// SDK 经 script 标签挂在 window.SipDialpadSDK；这里用最小局部类型描述对外暴露的三方法，
// 避免散落 any。SDK 真实类型由业务方维护，文档见「拨号盘UI插件SDK 接入文档.md」。
interface DialpadInstance {
  instanceId?: string
  render: (containerId: string, config: RenderConfig) => void
  makeCall: (phoneNumber: string, params?: CallParams) => void
  getLineTypeData: () => Promise<LineTypeData>
  destroy: () => void
}
interface DialpadFactory {
  createSipDialpadSDK: (options?: { instanceId?: string }) => DialpadInstance
}

// RenderConfig / CallParams 对照接入文档 API 参考。
interface RenderConfig {
  extNo: string
  extPwd: string
  serverAddr?: string
  summary?: boolean
  language?: 'zh' | 'en' | 'es'
  features?: { callHistory?: boolean; callSummary?: boolean }
  iframe?: { url: string; width: number; height: number }
  voicemail?: { autoHangup?: boolean; hangupDelaySec?: number }
  autoOnline?: boolean
}
interface CallParams {
  outNumber?: string
  businessId?: string
  ticketId?: string
  orderId?: string
  lineType?: string
  skipFormatCheck?: boolean
}
interface LineTypeData {
  lineTypes: string[]
  selectedLineType: string
  lineTypeLocked: boolean
}

const CONTAINER_ID = 'sip-dialpad-sdk'
// 默认 SDK 脚本：官方 latest 公共地址。字段可编辑，便于临时回退到指定版本或签名 URL 排查问题。
const DEFAULT_SCRIPT_URL = 'https://pub-res.hermesomni.com/sdk/js-sip/sip-dialpad-sdk.latest.js'
const SERVER_ADDR_OPTIONS = [
  { value: 'hermes-test.financifyx.com', label: 'hermes-test.financifyx.com（测试）' },
  { value: 'wise-api.hermesomni.com', label: 'wise-api.hermesomni.com（生产）' },
]

// loadSdkScript：按 URL 动态注入 <script>，去重缓存（同 URL 复用同一个 Promise，避免重复注入）。
// 不写进 index.html——只有进入本页才加载这个外部脚本，其它页面零负担。
const scriptCache = new Map<string, Promise<void>>()
function loadSdkScript(url: string): Promise<void> {
  const cached = scriptCache.get(url)
  if (cached) return cached
  const p = new Promise<void>((resolve, reject) => {
    const el = document.createElement('script')
    el.src = url
    el.async = true
    el.onload = () => resolve()
    el.onerror = () => {
      scriptCache.delete(url) // 失败不缓存，允许重试
      const isHttp = url.startsWith('http://')
      const mixedHint = isHttp && window.location.protocol === 'https:'
        ? '（当前页是 HTTPS 而脚本是 HTTP，浏览器会拦截混合内容——改用 https 脚本地址，或在 HTTP/localhost 环境打开本页）'
        : ''
      reject(new Error(`SDK 脚本加载失败：${url}${mixedHint}。也可能是网络不可达 / CSP 限制 script-src。`))
    }
    document.head.appendChild(el)
  })
  scriptCache.set(url, p)
  return p
}

// 表单模型（覆盖 createSipDialpadSDK options + RenderConfig 全字段）。
interface ConfigForm {
  instanceId?: string
  extNo: string
  extPwd: string
  serverAddr?: string
  scriptUrl: string
  language: 'zh' | 'en' | 'es'
  summary: boolean
  autoOnline: boolean
  callHistory: boolean
  callSummary: boolean
  iframeUrl?: string
  iframeWidth?: number
  iframeHeight?: number
  voicemailAutoHangup: boolean
  voicemailDelaySec?: number
}
interface CallForm {
  phoneNumber: string
  outNumber?: string
  businessId?: string
  ticketId?: string
  orderId?: string
  lineType?: string
  skipFormatCheck: boolean
}

const trimOrUndef = (s?: string) => { const v = (s || '').trim(); return v || undefined }

// 坐席外呼 · SDK 调试台：用官方 Dialpad SDK 复刻坐席外呼，把 SDK 全部能力（createSipDialpadSDK options
// + RenderConfig + makeCall CallParams）外露成可视化表单，便于对照测试 SDK 行为。
// 与 /agent-call（mock 自研 jssip 软电话）独立并存。SDK 经 serverAddr 直连 Hermes，不走 mock 反代。
export default function AgentCallSdkPage() {
  const [cfgForm] = Form.useForm<ConfigForm>()
  const [callForm] = Form.useForm<CallForm>()
  const sdkRef = useRef<DialpadInstance | null>(null)
  const [mounted, setMounted] = useState(false) // 是否已 render（SDK 已挂载）
  const [busy, setBusy] = useState(false) // 挂载中（脚本加载 + render）
  const [logs, setLogs] = useState<string[]>([])
  const [lineTypeData, setLineTypeData] = useState<LineTypeData>({ lineTypes: ['base'], selectedLineType: 'base', lineTypeLocked: false })
  const [lineTypesLoading, setLineTypesLoading] = useState(false)

  const log = (msg: string) => {
    const ts = new Date().toLocaleTimeString('zh-CN', { hour12: false })
    setLogs((prev) => [`${ts}  ${msg}`, ...prev].slice(0, 200))
  }

  // 读取 SDK 维护的线路类型缓存快照。页面不直接请求 Hermes /line-types；
  // 首次真实拉取、缓存与锁定语义都由 SDK getLineTypeData() 负责。
  const loadLineTypesFromSdk = async () => {
    const sdk = sdkRef.current
    if (!sdk) { message.warning('请先「挂载 SDK」'); return }
    setLineTypesLoading(true)
    try {
      const data = await sdk.getLineTypeData()
      const lineTypes = Array.from(new Set(['base', ...(Array.isArray(data.lineTypes) ? data.lineTypes : [])]))
      const selectedLineType = data.selectedLineType && lineTypes.includes(data.selectedLineType)
        ? data.selectedLineType
        : 'base'
      const next: LineTypeData = { lineTypes, selectedLineType, lineTypeLocked: Boolean(data.lineTypeLocked) }
      setLineTypeData(next)
      callForm.setFieldValue('lineType', selectedLineType)
      log(`getLineTypeData() → ${JSON.stringify(next)}`)
    } catch (e) {
      log(`✗ getLineTypeData 失败：${String(e)}`)
      message.error(String(e))
    } finally {
      setLineTypesLoading(false)
    }
  }

  // 卸载时销毁 SDK 实例，释放 SIP/WS 资源（符合 SDK「已销毁不可复用」语义）。
  useEffect(() => {
    return () => { try { sdkRef.current?.destroy() } catch { /* ignore */ } sdkRef.current = null }
  }, [])

  // 挂载 SDK：加载脚本 → createSipDialpadSDK({instanceId}) → render(containerId, config)。
  // 仅传用户实际填写的字段，空值不传以尊重 SDK 默认值。
  const handleRender = async (v: ConfigForm) => {
    setBusy(true)
    try {
      log(`加载 SDK 脚本：${v.scriptUrl}`)
      await loadSdkScript(v.scriptUrl)
      const factory = (window as unknown as { SipDialpadSDK?: DialpadFactory }).SipDialpadSDK
      if (!factory?.createSipDialpadSDK) throw new Error('脚本已加载但未找到 window.SipDialpadSDK.createSipDialpadSDK（检查脚本地址是否正确）')

      // 已挂载过先销毁，保证单实例（SDK 底层 SIP 单例，每页一个会话）。
      if (sdkRef.current) { try { sdkRef.current.destroy() } catch { /* ignore */ } sdkRef.current = null }

      const instanceId = trimOrUndef(v.instanceId)
      const sdk = factory.createSipDialpadSDK(instanceId ? { instanceId } : undefined)
      sdkRef.current = sdk
      log(`createSipDialpadSDK(${instanceId ? `instanceId=${instanceId}` : ''}) → ${sdk.instanceId || 'ok'}`)

      // 组装 RenderConfig：可选字段按填写情况裁剪。
      const cfg: RenderConfig = {
        extNo: v.extNo.trim(),
        extPwd: v.extPwd,
        serverAddr: trimOrUndef(v.serverAddr),
        language: v.language,
        summary: v.summary,
        autoOnline: v.autoOnline,
        features: { callHistory: v.callHistory, callSummary: v.callSummary },
      }
      const iframeUrl = trimOrUndef(v.iframeUrl)
      if (iframeUrl && v.iframeWidth && v.iframeHeight) {
        cfg.iframe = { url: iframeUrl, width: v.iframeWidth, height: v.iframeHeight }
      }
      // voicemail：只在用户开了自动挂断或填了延迟时下发（避免无意义覆盖本地设置）。
      if (v.voicemailAutoHangup || typeof v.voicemailDelaySec === 'number') {
        cfg.voicemail = { autoHangup: v.voicemailAutoHangup, hangupDelaySec: v.voicemailDelaySec }
      }

      sdk.render(CONTAINER_ID, cfg)
      setMounted(true)
      log(`render('${CONTAINER_ID}', ${JSON.stringify(cfg)})`)
      message.success('SDK 已挂载')
      void loadLineTypesFromSdk()
    } catch (e) {
      log(`✗ 挂载失败：${String(e)}`)
      message.error(String(e))
    } finally {
      setBusy(false)
    }
  }

  const handleDestroy = () => {
    try {
      sdkRef.current?.destroy()
      log('destroy() — 实例已销毁')
      message.success('SDK 已销毁')
    } catch (e) {
      log(`✗ destroy 失败：${String(e)}`)
    } finally {
      sdkRef.current = null
      setMounted(false)
      setLineTypeData({ lineTypes: ['base'], selectedLineType: 'base', lineTypeLocked: false })
      callForm.setFieldValue('lineType', undefined)
    }
  }

  // 编程式外呼：仅传非空 CallParams。
  // lineType 选项来自 SDK getLineTypeData()，外呼时仍按 SDK 文档通过 makeCall 参数透传。
  const handleMakeCall = (v: CallForm) => {
    if (!sdkRef.current) { message.warning('请先「挂载 SDK」'); return }
    const params: CallParams = {
      outNumber: trimOrUndef(v.outNumber),
      businessId: trimOrUndef(v.businessId),
      ticketId: trimOrUndef(v.ticketId),
      orderId: trimOrUndef(v.orderId),
      lineType: trimOrUndef(v.lineType),
      skipFormatCheck: v.skipFormatCheck || undefined,
    }
    // 去掉 undefined，保留 SDK 看到的最小参数集。
    const clean = Object.fromEntries(Object.entries(params).filter(([, val]) => val !== undefined)) as CallParams
    try {
      sdkRef.current.makeCall(v.phoneNumber.trim(), clean)
      log(`makeCall('${v.phoneNumber.trim()}', ${JSON.stringify(clean)})`)
    } catch (e) {
      log(`✗ makeCall 失败：${String(e)}`)
      message.error(String(e))
    }
  }

  return (
    <div className="page-container">
      <PageHeader
        title="坐席外呼 · SDK 调试台"
        status={{ tone: mounted ? 'success' : 'neutral', text: mounted ? '已挂载' : '未挂载' }}
        extra={mounted
          ? <Button danger icon={<PoweroffOutlined />} onClick={handleDestroy}>销毁 SDK</Button>
          : <Tag color="default">render 后启用控制</Tag>}
      />
      <InfoBanner title="官方 SIP Dialpad SDK · 直连 Hermes（不经 mock 反代）">
        本页用业务方提供的官方拨号盘 SDK（<Text code>window.SipDialpadSDK</Text>）复刻坐席外呼，并把 SDK 全部能力外露成表单：左侧填 <Text code>createSipDialpadSDK</Text> options 与 <Text code>RenderConfig</Text> 后「挂载 SDK」，右侧用 <Text code>makeCall</Text> 编程式外呼。线路类型 <Text code>lineType</Text> 通过 <Text code>sdk.getLineTypeData()</Text> 读取 SDK 缓存快照，不由 mock 页面直接请求 Hermes；外呼时仍通过 <Text code>makeCall(phoneNumber, {'{ lineType }'})</Text> 透传。拨号盘 UI 由 SDK 自渲染到下方容器。SDK 经 <Text code>serverAddr</Text> 直连 Hermes，与 <Text code>/agent-call</Text> 的 mock 自研软电话独立并存。需麦克风权限 + HTTPS/localhost；外部脚本需可访问 CDN。
      </InfoBanner>

      <Row gutter={16}>
        {/* 配置区：createSipDialpadSDK options + RenderConfig 全字段 */}
        <Col xs={24} lg={12}>
          <Card size="small" title={<Space><ApiOutlined />挂载配置（createSipDialpadSDK + render）</Space>} style={{ marginBottom: 16 }}>
            <Form<ConfigForm>
              form={cfgForm}
              layout="vertical"
              initialValues={{
                scriptUrl: DEFAULT_SCRIPT_URL,
                serverAddr: 'hermes-test.financifyx.com',
                language: 'zh',
                summary: true,
                autoOnline: false,
                callHistory: true,
                callSummary: true,
                voicemailAutoHangup: false,
              }}
              onFinish={handleRender}
            >
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="extNo" label="坐席分机号 extNo" rules={[{ required: true, message: '必填' }]}>
                    <Input placeholder="如 1001" />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="extPwd" label="分机密码 extPwd" rules={[{ required: true, message: '必填' }]}>
                    <Input.Password placeholder="分机密码" autoComplete="off" />
                  </Form.Item>
                </Col>
              </Row>

              <Form.Item name="serverAddr" label="服务域名 serverAddr（不含 protocol）" tooltip="HTTP/WS 基础域名；SIP host/port 由后端运行时下发，不由此控制">
                <AutoComplete options={SERVER_ADDR_OPTIONS} placeholder="hermes-test.financifyx.com" />
              </Form.Item>
              <Form.Item name="scriptUrl" label="SDK 脚本地址 scriptUrl" tooltip="默认使用官方 latest 公共地址；字段可编辑，便于临时回退到指定版本或签名 URL 排查问题" rules={[{ required: true, message: '必填' }]}>
                <Input placeholder={DEFAULT_SCRIPT_URL} />
              </Form.Item>
              <Form.Item name="instanceId" label="实例 ID instanceId（可选）" tooltip="留空=随机（刷新丢上下文）；传稳定值如 agent-1 可刷新后保留 token/persist">
                <Input placeholder="留空自动随机" />
              </Form.Item>

              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="language" label="界面语言 language">
                    <Select options={[{ value: 'zh', label: '中文' }, { value: 'en', label: 'English' }, { value: 'es', label: 'Español' }]} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="summary" label="通话小结总开关" valuePropName="checked" tooltip="summary：小结能力顶层开关">
                    <Switch />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="autoOnline" label="自动在线" valuePropName="checked" tooltip="autoOnline：控制面连接成功且坐席离线时自动切空闲在线">
                    <Switch />
                  </Form.Item>
                </Col>
              </Row>

              <Divider orientation="left" plain style={{ margin: '4px 0 12px' }}><Text type="secondary" style={{ fontSize: 12 }}>features（UI 模块显隐）</Text></Divider>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="callHistory" label="通话记录 callHistory" valuePropName="checked">
                    <Switch />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="callSummary" label="通话小结 callSummary" valuePropName="checked">
                    <Switch />
                  </Form.Item>
                </Col>
              </Row>

              <Divider orientation="left" plain style={{ margin: '4px 0 12px' }}><Text type="secondary" style={{ fontSize: 12 }}>iframe 业务面板（三项全填才启用）</Text></Divider>
              <Form.Item name="iframeUrl" label="iframe URL">
                <Input placeholder="业务页面基础 URL（留空=不启用）" />
              </Form.Item>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="iframeWidth" label="宽度 width(px)">
                    <InputNumber min={1} style={{ width: '100%' }} placeholder="如 360" />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="iframeHeight" label="高度 height(px)">
                    <InputNumber min={1} style={{ width: '100%' }} placeholder="如 480" />
                  </Form.Item>
                </Col>
              </Row>

              <Divider orientation="left" plain style={{ margin: '4px 0 12px' }}><Text type="secondary" style={{ fontSize: 12 }}>voicemail（语音信箱自动挂断）</Text></Divider>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="voicemailAutoHangup" label="识别后自动挂断 autoHangup" valuePropName="checked">
                    <Switch />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="voicemailDelaySec" label="挂断延迟 hangupDelaySec(0–5)" tooltip="留空=保留本地设置；填了会被规范化到 [0,5]">
                    <InputNumber min={0} max={5} style={{ width: '100%' }} placeholder="默认 3" />
                  </Form.Item>
                </Col>
              </Row>

              <Space>
                <Button type="primary" htmlType="submit" icon={<ApiOutlined />} loading={busy}>
                  {mounted ? '重新挂载（render）' : '挂载 SDK（render）'}
                </Button>
                <Button danger disabled={!mounted} icon={<PoweroffOutlined />} onClick={handleDestroy}>销毁（destroy）</Button>
              </Space>
            </Form>
          </Card>
        </Col>

        {/* makeCall 控制台 + 调用日志 */}
        <Col xs={24} lg={12}>
          <Card size="small" title={<Space><PhoneOutlined />编程式外呼（makeCall）</Space>} style={{ marginBottom: 16 }}>
            <Form<CallForm>
              form={callForm}
              layout="vertical"
              initialValues={{ skipFormatCheck: false }}
              onFinish={handleMakeCall}
            >
              <Form.Item name="phoneNumber" label="被叫号码 phoneNumber" rules={[{ required: true, message: '必填' }]}>
                <Input placeholder="待拨打的电话号码（如 mock 客户号）" />
              </Form.Item>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="outNumber" label="外显主叫 outNumber">
                    <Input placeholder="本次外呼线路号码" />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item
                    name="lineType"
                    label="线路类型 lineType"
                    tooltip="选项来自 sdk.getLineTypeData()。锁定时表示坐席绑定外显号码，不应再切换。"
                  >
                    <Select
                      placeholder={mounted ? '选择线路类型' : '挂载 SDK 后读取'}
                      loading={lineTypesLoading}
                      disabled={!mounted || lineTypeData.lineTypeLocked}
                      options={lineTypeData.lineTypes.map((t) => ({ value: t, label: t }))}
                      popupRender={(menu) => (
                        <>
                          {menu}
                          <Divider style={{ margin: '4px 0' }} />
                          <Button type="link" size="small" loading={lineTypesLoading} onClick={() => void loadLineTypesFromSdk()} style={{ paddingInline: 12 }}>
                            刷新线路类型
                          </Button>
                        </>
                      )}
                    />
                  </Form.Item>
                  {mounted && (
                    <Text type={lineTypeData.lineTypeLocked ? 'warning' : 'secondary'} style={{ display: 'block', marginTop: -16, marginBottom: 16, fontSize: 12 }}>
                      {lineTypeData.lineTypeLocked ? '线路已锁定：坐席绑定外显号码，不应切换。' : `SDK 当前默认：${lineTypeData.selectedLineType || 'base'}`}
                    </Text>
                  )}
                </Col>
                <Col span={12}>
                  <Form.Item name="businessId" label="业务 ID businessId" tooltip="传入后触发 iframe 业务面板展示">
                    <Input placeholder="自定义业务 ID" />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="ticketId" label="进件 ticketId">
                    <Input placeholder="风控进件 ticketId" />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="orderId" label="订单 orderId">
                    <Input placeholder="订单 ID" />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="skipFormatCheck" label="跳过号码格式校验" valuePropName="checked" tooltip="加密 token 回拨时需 true 以绕过 /^\d+$/ 校验">
                    <Switch />
                  </Form.Item>
                </Col>
              </Row>
              <Button type="primary" htmlType="submit" icon={<PhoneOutlined />} disabled={!mounted}>
                外呼 makeCall
              </Button>
              {!mounted && <Text type="secondary" style={{ fontSize: 12, marginLeft: 12 }}>挂载 SDK 后可外呼</Text>}
            </Form>
          </Card>

          <Card size="small" title="调用日志">
            <Paragraph style={{ margin: 0, maxHeight: 220, overflow: 'auto', fontSize: 11, fontFamily: 'monospace' }}>
              {logs.length ? logs.map((l, i) => <div key={i}>{l}</div>) : <Text type="secondary">暂无调用记录。填配置后「挂载 SDK」开始。</Text>}
            </Paragraph>
          </Card>
        </Col>
      </Row>

      {/* SDK 拨号盘挂载容器：render() 后由 SDK 自渲染浮动面板到此 */}
      <Card size="small" title="拨号盘（SDK 渲染）" style={{ marginTop: 0 }}>
        <div id={CONTAINER_ID} style={{ minHeight: 80 }} />
        {!mounted && <Text type="secondary" style={{ fontSize: 12 }}>挂载 SDK 后，官方拨号盘 UI 在此渲染（可拖拽/最小化的浮动面板）。</Text>}
      </Card>
    </div>
  )
}
