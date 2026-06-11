# Hermes 前端坐席通话 SDK（jssip / WebRTC）

> 目标读者：要在 hermes-mock 前端复刻/对接坐席软电话、或排查坐席外呼信令的开发者。
> 范围：Hermes 工作台前端坐席软电话 SDK——**取 WebRTC 地址 → jssip 注册到 FS → 切坐席状态 → 发起外呼(INVITE)**，以及 hermes-mock 前端如何复刻对接。
> **不**覆盖：FS 收到 INVITE 之后的后端选线 / bridge / ESL 事件回流——那条见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md)（本篇与它互补，不重复贴证据，只引用其小节）。
> 基于：[../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) 已核查证据 + memory `agent-outbound-frontend-jssip`；源码根与引用约定见 [README.md](README.md)。
> ⚠️ **本篇为初稿**：流程主干已三方验证，但 jssip 事件/错误处理/媒体参数等标注为"推断/待补"，请按源码补全。

---

## 1. 一句话 + 结构

前端坐席用 **jssip 经 WebRTC(wss) 注册到 FreeSWITCH**，点外呼时发一条带 `x-call_center_type:OUTBOUND_CALL` 的 SIP INVITE；**选线/桥接全在后端**，SDK 本身只管"注册 + 发起 + 状态上报"。

```
坐席浏览器（org-management 前端）
  ├─ request.ts    HTTP：取 webrtc 地址 / 切坐席状态 / markSipReady
  ├─ index.ts      jssip.UA：注册 + call(被叫) 发 INVITE(带业务头)
  └─ controller.ts 编排状态机：登录 → 注册 → 切在线 → 呼叫
        │ wss（注册 / INVITE）
        ▼
   FreeSWITCH internal profile（ws-binding:5066 / wss-binding:7443）
        │ INVITE 之后：CHANNEL_CREATE → fs-esl-proxy → call-center 选线 bridge 客户腿
        ▼ （详见 ../AGENT-OUTBOUND-CALL.md §2.5）
   被叫客户腿（运营商线路 / hermes-mock）
```

## 2. 端到端使用流程（带证据）

> 路径相对源码根（见 README 引用约定）。前端模块名沿用 `org-management-temp/src/sip/*`——**若前端仓库已改名，请在此更正**（推断：org-management-temp 可能是临时/旧名）。

### 2.1 取 WebRTC 地址
- 前端 `getSipWebrtcAddr()` → `GET /call-center/agent-workbench/sdk/agent/webrtc/addr`，返回 `{host, port, ssl}`，拼成 `wss://{host}:{port}`。
- 证据：前端 `org-management-temp/src/sip/request.ts`；后端 `hermes-call-center/.../WebrtcController.kt:16-25`（配置 `WebrtcProperties` 前缀 `com.hermes.webrtc`）。
- 详见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §2.1。返回的是**对外网关/边缘 host**，承载 wss 的是 FS internal profile。

### 2.2 jssip 注册到 FS
- 前端 `new jssip.UA({ uri: 'sip:{extNo}@{host}', password: extPwd })` 注册；`extNo` = 坐席分机号 = 后续 INVITE 的主叫。
- FS 承载 wss 的是 **internal profile**：`ws-binding:5066` / `wss-binding:7443`。证据：`freeswitch_config/.../sip_profiles/internal.xml:310,314`。
- 详见 §2.2。⚠️ WebRTC 媒体相关参数（`rtp-secure-media`、ICE candidate ACL、Opus）stock 配置未显式出现，**生产 FS 应另有配置，落地前对照**（推断）。

### 2.3 切坐席状态
- 前端 `POST /call-center/agent-workbench/sdk/agent/status/switch`，`action`：**2 在线 / 5 呼叫中 / 6 小休 / 7 忙 / 9 自动外呼**。
- 后端 `AgentController.toDialing()` 状态机（ONLINE/RESTING/BUSY/AUTO_OUTBOUND/OFFLINE）。证据：`hermes-call-center/.../AgentController.kt:23-101`。
- ⚠️ `DIALING`（呼叫中）由**服务端**在外呼事件里推进，前端传 DIALING 直接 success 不动作（`AgentController.kt:28-31`）。详见 §2.3。

### 2.4 发起外呼：call() 的关键 SIP 头
- 前端 `org-management-temp/src/sip/index.ts` 的 `call(被叫)` 在 `extraHeaders` 带：
  - `X-JCallId:{uuid}`
  - `x-session-id:CCMDL{uuid}`
  - `x-call_center_type:OUTBOUND_CALL` ← 后端据此路由到外呼处理器
  - `x-agent_channel:{坐席号}` ← **后端不读**，坐席身份取 SIP 主叫号 `Caller-Caller-ID-Number`
- 详见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §2.4（含每个头进入 FS/ESL 后的字段映射与后端读取点）。

## 3. 关键接口 / 信令汇总

| 类型 | 名称 | 用途 | 证据 |
|---|---|---|---|
| HTTP | `GET /call-center/agent-workbench/sdk/agent/webrtc/addr` | 取 wss 地址 | `WebrtcController.kt:16-25` |
| HTTP | `POST /call-center/agent-workbench/sdk/agent/status/switch` | 切坐席工作态 | `AgentController.kt:23-101` |
| HTTP | `GET .../agent/org/agents` | 机构在线坐席 | `AgentController.kt:103-106` |
| HTTP | `/public/auth/sip`（markSipReady） | 上报 SIP 注册就绪（mock 侧用） | memory；⚠️ Hermes 原版路径待核 |
| SIP 头 | `X-JCallId` / `x-session-id:CCMDL…` / `x-call_center_type:OUTBOUND_CALL` / `x-agent_channel` | INVITE 业务头 | §2.4 |
| SIP | `ws-binding:5066` / `wss-binding:7443` | FS WebRTC 接入 | `internal.xml:310,314` |

## 4. 事件 / 状态机（部分推断）

- **jssip.UA 事件**（⚠️ 推断，按前端源码补全）：`registered` / `registrationFailed` / `newRTCSession`（呼叫建立）/ `ended` / `failed`。
- **坐席状态**（来自后端枚举）：`OFFLINE / ONLINE / DIALING / RINGING / CALLING / RESTING / BUSY / AUTO_OUTBOUND`。外呼推进：DIALING→RINGING→CALLING（见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §2.5 C）。
- **外呼前置（关键）**：call-center `agentIsReady` = **WS 在线 && SIP-ready**；SIP-ready 经 `/public/auth/sip` 上报，约 **45s TTL 需周期刷新**。证据：memory `agent-outbound-frontend-jssip`。

## 5. hermes-mock 侧的复刻（已验证）

- mock 前端 `web/src/sip/{index,controller,request}.ts` + `components/AgentSoftphone.tsx`，挂在 `CallScenariosPage`「坐席外呼」tab。
- 后端反代：`internal/api/api.go` `registerHermesProxy`——`/agent-workbench/*`、`/call-center/*` → call-center（注入 `ORG_CODE_KEY`/`AGENT_NUMBER_KEY`）；`/agent-workbench/api/ws` → hermes-ws。
- 本地联调必备配置（FS ws/wss 端口、call-center `com.hermes.webrtc`、坐席分机、dialplan agent_outbound extension）+ **已三方验证通过**：见 [../STATUS.md](../STATUS.md)「本地栈速查」与 memory `agent-outbound-frontend-jssip` / `hermes-mock-local-stack-quirks`。
- ⚠️ RTP 媒体本地难通（坐席浏览器侧听声音是深水区），见 [../STATUS.md](../STATUS.md) 已知问题。

## 6. 待补 / 推断项

- ⚠️ 前端 SIP SDK 源码确切目录与仓库名（`org-management-temp` 是否最新）。
- ⚠️ jssip 版本、UA 完整配置项（ICE/STUN、`session_timers`、`registrar_server` 等）。
- ⚠️ 媒体编解码协商（Opus vs PCMU/PCMA）与 DTLS-SRTP 证书链。
- ⚠️ jssip 事件回调、错误码、断线重连与重注册策略。
- ⚠️ `markSipReady` 在 Hermes 原版前端的确切端点（mock 侧用 `/public/auth/sip`）。

## 附：关键证据索引（路径:行号）

- `hermes-call-center/.../WebrtcController.kt:16-25`：返回 wss `{host,port,ssl}`。
- `hermes-call-center/.../AgentController.kt:23-101`：坐席状态机；`:28-31` DIALING 服务端推进。
- `freeswitch_config/.../sip_profiles/internal.xml:310,314`：`ws-binding:5066` / `wss-binding:7443`。
- `org-management-temp/src/sip/{request,index}.ts`：取址 / 注册 / call() extraHeaders。
- 链路全景与后端选线/bridge：[../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md)。
