# 真实坐席手动外呼链路（Agent Manual Outbound Call）

> 目标读者：后续要在 hermes-mock 里实现「坐席外呼」功能的开发者。
> 范围：仅梳理**真实坐席手动外呼**（坐席工作台点拨号盘外呼）的端到端链路；与之对照的「机器人/群呼/语音通知」自动外呼见第 4 节。
>
> 证据约定：每条结论尽量带「文件:行号」。凡标「未在代码中找到，推断」的，是基于现有代码结构与 FreeSWITCH 行为做的合理推断，落地前需实测确认。

---

## 1. 一句话概述 + 端到端时序

**一句话**：坐席浏览器用 jsSIP 通过 **WebRTC(wss)** 注册到 FreeSWITCH(FS)，点外呼时发一条带 `x-call_center_type:OUTBOUND_CALL` 的 SIP INVITE 到 FS；FS 把这条「坐席腿」park 住并经 ESL→Kafka 把 `CHANNEL_CREATE` 事件抛给 `hermes-call-center`；后者**按机构选线路号码**，再在坐席腿上执行一条 `bridge` 命令，由 FS 经 `sofia/external/{被叫}@{线路网关}` 呼出「客户腿」，客户腿走运营商线路打到真实被叫，最后 FS 把坐席腿与客户腿桥接成一通话。**线路选择逻辑完全在 `hermes-call-center` 后端，不在 FS 的 dialplan 里。**

```
坐席浏览器(jsSIP)                FreeSWITCH                fs-esl-proxy            hermes-call-center            运营商线路/被叫
   |                               |                          |                          |                        |
   |--(1) GET webrtc/addr--------->| (经 hermes-call-center HTTP)                        |                        |
   |<-- {host,port,ssl} -----------|                          |                          |                        |
   |                               |                          |                          |                        |
   |--(2) REGISTER wss(注册)------>|(internal profile wss)     |                          |                        |
   |                               |                          |                          |                        |
   |--(3) POST status/switch(在线/呼叫中) → hermes-call-center HTTP                       |                        |
   |                               |                          |                          |                        |
   |--(4) INVITE 被叫 ------------>|                          |                          |                        |
   |   X-JCallId / x-session-id    | park 坐席腿               |                          |                        |
   |   x-call_center_type:OUTBOUND |                          |                          |                        |
   |   x-agent_channel:坐席号       |--(5) ESL CHANNEL_CREATE->|--(6) Kafka esl-event---->| onCallStart()          |
   |                               |                          |                          |  - 校验 sessionId/坐席   |
   |                               |                          |                          |  - 选线路(getAvailable   |
   |                               |                          |                          |    Phones+并发权重)       |
   |                               |<------(7) bgapi setvar / bridge (在坐席腿uuid上执行)--|  - 解密被叫/写CDR        |
   |                               |--(8) INVITE sofia/external/{prefix}{被叫}@{网关}---------------------------->|
   |                               |<-------------------- 180/200 ----------------------------------------------|
   |<==(9) FS 自动 bridge 坐席腿 + 客户腿，双向 RTP（FS/rtpengine 转码）==>|                |                        |
```

要点：
- (1)(3) 是 HTTP，走 hermes-call-center 的 REST。(2)(4)(9) 是 SIP/RTP，走 FS。(5)(6)(7)(8) 是 ESL/Kafka + FS originate/bridge。
- 坐席腿是 FS 的 **A-leg（caller=坐席分机号）**，客户腿是 **B-leg（caller=线路主叫号）**。后端靠「这条腿有没有 agentNumber / Redis CallChannel」来区分坐席腿与客户腿。

---

## 2. 逐环节说明（带证据）

### 2.1 取 WebRTC 地址
- 前端：`getSipWebrtcAddr()` → `GET /call-center/agent-workbench/sdk/agent/webrtc/addr`（已确证，前端 `org-management-temp/src/sip/request.ts`）。
- 后端实现：`WebrtcController.getAddr()` 直接返回配置里的 `host/port/ssl`。
  - 证据：`hermes-call-center/.../controller/agent/workbench/WebrtcController.kt:11`（`@RequestMapping("/agent-workbench/sdk/agent/webrtc")`）、`:16-25`（`/addr` 返回 `SipAddrDTO(host,port,ssl)`）。
  - 配置来源：`WebrtcProperties`，前缀 `com.hermes.webrtc`。证据：`config/properties/WebrtcProperties.kt:8-13`。
  - 实际值（测试环境）：`host: hermes-webrtc-test.wulicredit.com`、`port: 443`、`ssl: true` → 前端拼成 `wss://hermes-webrtc-test.wulicredit.com:443`。证据：`hermes-call-center/src/main/resources/application.yml:70-73`（本地 profile host 不同：`application-local.yml:98-100`）。
  - DTO：`entity/dto/SipAddrDTO.kt`（`host/port/ssl` 三字段）。

> 注意：这里返回的是一个**对外网关/边缘 host**（如 webrtc 反代域名），不是 FS 容器内网 IP；真正承载 wss 的是 FS 的 internal sofia profile。

### 2.2 坐席 WebRTC 注册到 FS
- 前端：`new jssip.UA({uri:'sip:{extNo}@{host}', password:extPwd})` 注册（已确证）。`extNo` = 坐席分机号，也就是后续 INVITE 的主叫。
- FS 侧承载 wss/ws 的是 **internal profile**：
  - `ws-binding :5066`、`wss-binding :7443`。证据：`freeswitch_config/config/sip_profiles/internal.xml:310`（`<param name="ws-binding" value=":5066"/>`）、`:314`（`<param name="wss-binding" value=":7443"/>`）。
  - 同文件 mock 副本一致：`hermes-mock/deploy/hermes-stack/freeswitch/config/conf/sip_profiles/internal.xml`（同为 stock 配置，含相同 ws/wss 段）。
- DTLS-SRTP 证书：`freeswitch_config/config/tls/dtls-srtp.pem`（WebRTC 媒体 DTLS）、`tls/wss.pem`（wss 信令 TLS）。证据：目录 `freeswitch_config/config/tls/`。
  - 说明：stock internal.xml 没有显式 `rtp-secure-media`/`wss-binding` 之外的 WebRTC 专项参数；生产环境通常还会加 `apply-candidate-acl`、`rtp-secure-media`、Opus 等。**这些在当前仓库里未显式出现，落地前需对照生产 FS 配置确认。（未在代码中找到，推断）**

### 2.3 坐席状态切换
- 前端：`POST /call-center/agent-workbench/sdk/agent/status/switch`，action 2在线/5呼叫中/6小休/7忙/9自动外呼（已确证）。
- 后端：`AgentController.toDialing()` 处理 `status/switch`，按目标状态（ONLINE/RESTING/BUSY/AUTO_OUTBOUND/OFFLINE）做状态机校验后写状态。
  - 证据：`controller/agent/workbench/AgentController.kt:18`（`@RequestMapping("/agent-workbench/sdk/agent")`）、`:23-101`（`status/switch` 状态机）。
  - 注意：`DIALING`（呼叫中）的切换由**服务端管理**，前端传 DIALING 直接 success 不动作。证据：`AgentController.kt:28-31`。真正进入 DIALING 是在外呼事件里由后端推进（见 2.5）。
- 机构在线坐席：`GET .../agent/org/agents` → `getOrgOnlineAgent()`。证据：`AgentController.kt:103-106`。（前端文档写的是 `agent/org/online`，后端这里路径是 `org/agents`，**两者可能是不同版本/不同端点，落地以实际网关路由为准**。）

### 2.4 坐席点外呼：jsSIP INVITE 的关键 SIP 头
前端 `org-management-temp/src/sip/index.ts` 的 `call(被叫)` 在 `extraHeaders` 带（已确证）：
- `X-JCallId:{uuid}`
- `x-session-id:CCMDL{uuid}`
- `x-call_center_type:OUTBOUND_CALL`
- `x-agent_channel:{坐席号}`

这些头进入 FS 后会被前缀成 `variable_sip_h_*` 出现在 ESL 事件里。后端读取的对应关系：
- `x-call_center_type` → 事件字段 `variable_sip_h_x-call_center_type`，值 `OUTBOUND_CALL`，用于**路由到外呼处理器**。证据：`constant/enums/BusinessCallTypeEnum.kt:27-28`（`CALL_TYPE_FS_FIELD="sip_h_x-call_center_type"`、`CALL_TYPE_FIELD_NAME_IN_EVENT="variable_sip_h_x-call_center_type"`）、`:10`（枚举 `OUTBOUND_CALL`）。
- `x-session-id`(CCMDL...) → 事件字段 `variable_sip_h_x-session-id`，做合法性校验并从中提取 callUuid。证据：`EslEventConstant.kt:125`（`SID = "variable_sip_h_x-session-id"`）、`OutboundCallHandler.kt:108-115`（`validSid(sessionId, BizType.CCMDL)`，非法直接 hangup）、`:311`（`SidUtils.extractValidCallUuid(...)`）。
- `X-JCallId` → 主要用于**桥接客户腿时再带回给对端**（回写 `sip_h_X-JCallId`），让两腿 callId 关联。证据：`entity/fs/command/Bridge.kt:50-52`（`if(!headerUuid.isNullOrBlank()) add("sip_h_X-JCallId=$headerUuid")`）。在外呼里 `headerUuid` 传的是 `businessId`，证据：`OutboundCallHandler.kt:503`。
- `x-agent_channel`（坐席号）→ **后端不直接读这个头**。后端识别坐席用的是 SIP 主叫号：`caller = Caller-Caller-ID-Number`（= 注册分机号 = 坐席号）。证据：`OutboundCallHandler.kt:98`（`caller = headers[EslEventConstant.CALLER]`）、`EslEventConstant.kt:36`（`CALLER="Caller-Caller-ID-Number"`）。被叫：`OutboundCallHandler.kt:100` + `EslEventConstant.kt:43`（`CALLEE="Caller-Destination-Number"`）。
  - 全仓库唯一与 `agent_channel` 字面相关的只有一处注释（`entity/dto/CallEventDto.kt:48 // agent_channel_uuid`），并非读取 INVITE 头。**结论：`x-agent_channel` 在当前后端是冗余/兼容头，坐席身份来自 SIP caller。（已确证：grep 无读取点）**

### 2.5 FS 把坐席腿交给后端 + 后端选线路 + 发起客户腿

**(A) 事件入口与路由**
- FS 的 ESL 事件经 `fs-esl-proxy` 监听后按 `sip_h_x-scene` 路由进对应 Kafka topic。证据：`hermes/hermes-fs-esl-proxy/.../listener/FreeSwitchListener.kt:50`（`DISPATCHER_HEADER="variable_sip_h_x-scene"`）、`:55`（`SERVICE_HEADER="sip_h_x-scene"`）。
- `hermes-call-center` 消费 topic `esl-event_{服务名}`，`CHANNEL_CREATE` → `onCallStart`。证据：`listener/fs/FsEventKafkaListener.kt:30`（topic）、`:48-49`（`"CHANNEL_CREATE" -> onCallStart`）。其余映射：`CHANNEL_ANSWER→onAnswer:52-60`、`CHANNEL_HANGUP_COMPLETE→onCallEnd:62-75`、`CHANNEL_CALLSTATE(RINGING)→onRinging:77-84`、`CHANNEL_PARK→onPark:97-99`。
- 分发器按 `variable_sip_h_x-call_center_type`（取不到再退回 Redis CallChannel.callType；再取不到默认 INBOUND）选 handler。证据：`listener/fs/CompositeEventDispatcher.kt:79-101`、注册逻辑 `:22-41`（`isSupport(callType)` 命中即注册）。
- `OUTBOUND_CALL` 实际命中的是 `OutboundRetryCallHandler`（它 `isSupport==OUTBOUND_CALL`），再委托给 `OutboundCallHandler`。证据：`listener/fs/business/OutboundRetryCallHandler.kt:41-47`（`isSupport`、`onCallStart→outboundCallHandler.onCallStart`）。注意 `OutboundCallHandler.isSupport` 返回 `false`，证据：`OutboundCallHandler.kt:91-93`（自身不直接注册，只被委托）。

**(B) onCallStart 里在坐席腿上做的事**（`OutboundCallHandler.onCallStart`）
1. 取 caller(坐席)/callee(被叫)/uuid/sessionId，校验 sessionId 合法（CCMDL）。证据：`OutboundCallHandler.kt:96-115`。
2. 判定是不是坐席腿：`CallChannel.getCallChannel(uuid)==null || agentNumber!=null` → 是坐席腿。新 INVITE 在 Redis 里查不到 channel，故为坐席腿。证据：`:117-119`。
3. 在坐席腿上 setvar：`hangup_after_bridge=false`、`park_after_bridge=true`、`continue_on_fail=true`（为重试/park 行为铺路）。证据：`:141-167`。
4. 查坐席机构信息、坐席状态；解密/校验被叫号码（支持内/外部加密号码与明文 tokenize）。证据：`:169-269`。
5. 坐席状态机：缓存呼叫前状态并切到 `DIALING`，失败则 hangup。证据：`:300-308`（`saveBeforeCallStatus` + `changeStatus(...,DIALING,...)`）。
6. **选线路号码（核心）**：
   - `linePhoneInfoService.getAvailablePhones(orgCode, OUTGOING)` 取该机构可用外呼号码列表。证据：`:392-393`。
   - 若坐席绑定了固定外显号 `phoneCode`，则只用该号。证据：`:394-399`。
   - 列表为空 → `No out show phone number are available` 早挂。证据：`:401-405`。
   - 按 `allocationWeight + concurrency` 加权随机抢一个并发许可：`distributeConcurrentService.getPermitIdByRandomWeight(...)`。证据：`:406-413`。抢不到 → `No concurrency available` 早挂 `:409-412`。
   - 主叫号 `lineCaller = linePhoneInfoService.getCaller(semaphore.obj)`（静态/动态号 + 前缀）。证据：`:414`、`getCaller` 实现 `LinePhoneInfoService.kt:141-166`。
   - `PhoneData` 的来源：`LinePhoneInfoService.updateCache` 调 `phoneApi.queryList(orgCode, PRODUCT_CODE_CALL_CENTER, OUTGOING)`，把 `line.address`(网关地址)、`line.calleePrefix`、`line.lineName/lineCode`、号码 `phone/concurrency/allocationWeight/prefix` 等装进 `PhoneData`。证据：`LinePhoneInfoService.kt:168-227`、`PhoneData` 定义 `:250-271`。线路按运营时段过滤：`filterAvailablePhones` `:81-115`。
   - **所以「按机构/线路选 t_line」的真实实现 = 机构维度的可用外呼号码 + 运营时段过滤 + 并发权重加权随机。线路网关地址来自 `line.address`，被叫前缀来自 `line.calleePrefix`。**（代码里字段叫 `lineName/lineCode/phoneCode/address/calleePrefix`，并无字面 `t_line`；`t_line` 应是上游 DB 表名，未在本仓库出现。未在代码中找到，推断）
7. 写两条 channel 到 Redis（坐席腿 `agentNumber=坐席`、客户腿 `bridgedChannel` 互指），写 CDR / AgentCDR。证据：`:416-494`（含 `customerChannelUuid` 生成 `:416`）。
8. **在坐席腿 uuid 上执行 `bridge` 客户腿**（不是新 originate）：
   - 构造 `Bridge(uuid=customerChannelUuid, caller=lineCaller, callee=calleePrefix+被叫明文, calleeGateway=line.address, parkAfterBridge=true, hangupAfterBridge=false, sessionId=CCMDL+callUuid, lineName=..., rtpengineId=...)`。证据：`:497-512`。
   - 通过 `freeswitchApi.sendMessage(execute, bridge)` 在坐席腿上执行。证据：`:536-545`。
   - 生成的 FS 命令字符串：`{params}sofia/external/{calleePrefix}{被叫}@{网关地址}`，params 含 `RECORD_STEREO=true`、`hangup_after_bridge`、`park_after_bridge`、`continue_on_fail`、`execute_on_answer='sched_hangup +1800 ...'`、`origination_uuid`、`origination_caller_id_*`、录音 `record_session::path`、`x-session-id`、`x-line-name` 等。证据：`Bridge.kt:34-81`（拼装）、`:79-80`（`"{%s}sofia/external/%s@%s"`）。

**(C) 通话推进与挂机**
- 客户腿振铃：坐席状态 DIALING→RINGING。证据：`OutboundCallHandler.onRinging:566-579`。
- 客户腿接听：坐席状态 RINGING/DIALING→CALLING，并对两腿启动实时语音监控。证据：`onAnswer:581-627`。
- 客户腿 5xx 失败：`OutboundRetryCallHandler` 在 5 秒窗口内最多重试 5 次，换一条没用过的线路 `retryWithSameAgent` 重新 bridge。证据：`OutboundRetryCallHandler.kt:57-128`（重试判定）、`:133-243`（换线重拨）、`OutboundCallHandler.retryWithSameAgent:1135-1260`。
- 挂机：区分坐席腿/客户腿写 CDR、释放并发、对端联动挂机、发 CDR 回调 Kafka。证据：`onCallEnd:645-845`。

---

## 3. mock 在该链路里的位置

mock 用 `emiago/diago + sipgo` 做 **SIP UAS（被叫侧）**，不支持 WebRTC（已确证）。在本链路里：

> **mock 永远是「经线路进来的客户被叫腿」的对端**。也就是上面时序图第 (8) 步 `INVITE sofia/external/{prefix}{被叫}@{网关}` 打出去后，那个「网关/线路对端」就是 mock。mock 收到 INVITE → 应答/拒接/振铃超时，扮演真实被叫客户。

- mock **不参与**坐席 WebRTC 注册、不收坐席的原始 INVITE、不参与选线路、不收 ESL 事件。这些都在「坐席浏览器 ↔ FS ↔ fs-esl-proxy ↔ hermes-call-center」之间完成。
- mock 看到的就是一条从 FS external profile（或线路网关）打来的普通 SIP INVITE，被叫号 = `calleePrefix+被叫`，主叫 = 线路 `lineCaller`。
- 现成的联调脚手架印证了这个定位：
  - `deploy/fs/public_hm_echo.xml`：被叫 `9999` → answer+echo，供「agent 主动呼出(UAC)」验证。证据：`hermes-mock/deploy/fs/public_hm_echo.xml:2-8`。
  - `deploy/fs/public_hm_agent.xml`：`originate sofia/external/600@172.30.0.10:5060 agentrec1001 ...`，注释明确「FS 呼 mock(扮客户 600)→应答→录音→bridge 到 user/1001」，并写明「Hermes 坐席上线主路径是工作台 SDK/WS，不依赖这里的 SIP REGISTER」。证据：`hermes-mock/deploy/fs/public_hm_agent.xml:3-11`、`:13-20`、`:26-31`。
- mock 的 FS dialplan（`deploy/hermes-stack/freeswitch/config/conf/dialplan/default.xml`）只处理 echo(1234)、robot_notice(lua)、incoming(bridge user/$1)，**没有任何外呼选线/坐席逻辑**——再次印证选线在后端。证据：`default.xml:15-40`。

---

## 4. 坐席手动外呼 vs 机器人/群呼自动外呼（fs-esl-proxy originate）对照

| 维度 | 坐席手动外呼（本文档） | 机器人/群呼/语音通知自动外呼 |
|---|---|---|
| 触发方 | 坐席浏览器 jsSIP 发 SIP INVITE | 业务后端 HTTP `POST /esl/originate` 调 fs-esl-proxy |
| 发起协议 | SIP INVITE over WebRTC(wss) | ESL `originate`（FS 主动外拨，无人工腿） |
| 谁有「坐席腿」 | 有，坐席腿是 A-leg（先存在再 bridge 客户腿） | 无人工坐席腿；客户腿接通后由机器人/IVR/通知逻辑接管 |
| FS 命令形态 | 坐席腿上 `bridge`：`{params}sofia/external/{prefix}{被叫}@{网关}`（证据 `Bridge.kt:79-80`） | 独立 `originate`：`{params}sofia/external/{prefix}{被叫}@{网关} &park()`（证据 `OriginateCommand.kt:94-103`） |
| 收尾动作 | `park_after_bridge=true`、`hangup_after_bridge=false`，接通后 FS 把两腿桥起来 | `&park()`（默认）或 `&playback(等待音)`，接通后挂在 park 由业务再决定（证据 `OriginateCommand.kt:97-103`） |
| 选线路 | `hermes-call-center` 的 `LinePhoneInfoService` 按机构号码池+并发权重选（证据 `OutboundCallHandler.kt:392-414`） | 由调用方在 `OriginateReq` 里传入 `gateway/caller/calleePrefix`（证据 `OriginateCommand.kt:10-55`） |
| 关键 SIP/ESL 头 | INVITE 带 `x-call_center_type:OUTBOUND_CALL`、`x-session-id:CCMDL...`、`X-JCallId`、`x-agent_channel` | originate 写 `sip_h_x-scene`(路由用) 等，无坐席头（证据 `FreeSwitchListener.kt:50-55`） |
| 是否经 ESL 事件回后端 | 是，`CHANNEL_CREATE` 等经 fs-esl-proxy→Kafka→call-center | 同样经 ESL 事件回，但由各自业务 handler 消费 |
| 用途 | 人工坐席一对一外呼 | 机器人外呼/群呼/语音通知（无人工） |

共同点：两者最终打到运营商/线路对端的 FS 通道形态都是 `sofia/external/{prefix}{被叫}@{网关}`，因此 **mock 作为被叫腿对两者是同一种入流量**。

---

## 5. 对 mock 实现「坐席外呼」的启示

### 5.1 可以复用 / 已具备
- **被叫腿能力**：mock 已能作为 `sofia/external/...@{网关}` 的被叫对端应答（`public_hm_echo.xml`、`public_hm_agent.xml`）。坐席外呼对 mock 而言与群呼/机器人外呼是**同一种入向 INVITE**，无需新增 mock 协议能力。
- **录音/桥接验证脚手架**：`public_hm_agent.xml` 的 `agentrec/agentbridge` 模式可直接用来验证「客户腿+坐席腿」双向媒体与立体声录音（FS 侧 originate + bridge user/<ext>）。
- **FS 配置**：mock 自带的 FS 配置 internal profile 已含 `ws-binding:5066 / wss-binding:7443`（stock），若要真起一个「可被 jsSIP 注册的坐席」，wss 端口与证书已就位（`tls/wss.pem`、`tls/dtls-srtp.pem`）。

### 5.2 做不到 / 需要外部依赖（或需在 mock 里另行模拟）
- **WebRTC 坐席腿**：mock 自身（diago/sipgo, UAS）不支持 WebRTC，无法扮演「用 jsSIP 注册并发起 INVITE 的坐席」。要在 mock 里跑通完整坐席外呼，需要**真实 FS（带 wss/DTLS-SRTP）+ 真实/桩化的 hermes-call-center**，mock 只补被叫腿。（已确证 mock 不支持 WebRTC）
- **选线路逻辑不在 FS**：不能靠改 FS dialplan 模拟「按机构选 t_line」。选线在 `hermes-call-center`（`LinePhoneInfoService` + `PhoneApi.queryList` + 并发许可）。mock 若要自洽地模拟整链，需要**桩化 `hermes-call-center` 的选线与 `bridge` 下发**，或直接在 mock 侧用 FS `originate/bridge` 复刻第 (8) 步命令（即用 `public_hm_agent.xml` 那种 originate 形态）。
- **坐席身份来自 SIP caller，不是 `x-agent_channel` 头**：若在 mock/桩里复刻后端逻辑，应以 INVITE 主叫号(`Caller-Caller-ID-Number`)当坐席号，而不是去解析 `x-agent_channel`（该头当前后端不读）。证据见 2.4。
- **依赖中间件**：真实链路依赖 fs-esl-proxy(ESL→Kafka)、Kafka、Redis(CallChannel/并发许可)、加解密服务(crypto tokenize/detokenize)、PhoneApi/OrgApi。mock 化坐席外呼时这些都需要桩或绕过。
- **WebRTC 网关/媒体细节未在本仓库固化**：stock internal.xml 未显式开 `rtp-secure-media`、ICE candidate ACL、Opus 等 WebRTC 必需项；生产 FS 应另有配置。落地前需对照生产 FS 实测。（未在代码中找到，推断）

### 5.3 最小可行路径建议（不在本任务范围内实现，仅记录）
若只想在 mock 体系里「演示坐席外呼的客户腿」，最省事的是沿用 `public_hm_agent.xml`：用 FS `originate sofia/external/{被叫}@mock + bridge user/<坐席分机>`，把「客户腿=mock、坐席腿=另一路 mock/或真实 user」桥起来，即可获得双向真实媒体与录音，而无需引入 WebRTC 与整套 call-center。要逼近真实，则需补 wss 坐席 + call-center 桩。

---

## 附：关键证据索引（文件:行号）
- `hermes-call-center/.../listener/fs/business/OutboundCallHandler.kt:392-414`：按机构选外呼线路号码 + 并发权重加权随机选线。
- `hermes-call-center/.../listener/fs/business/OutboundCallHandler.kt:497-545`：在坐席腿上执行 `bridge` 拉起客户腿（park/不挂断/带 sessionId）。
- `hermes-call-center/.../entity/fs/command/Bridge.kt:79-80`：FS 命令形态 `{params}sofia/external/{prefix}{被叫}@{网关}`。
- `hermes-call-center/.../constant/enums/BusinessCallTypeEnum.kt:27-28` + `listener/fs/CompositeEventDispatcher.kt:79-101`：靠 `variable_sip_h_x-call_center_type=OUTBOUND_CALL` 路由到外呼处理器。
- `freeswitch_config/config/sip_profiles/internal.xml:310,314`：坐席 WebRTC 注册的 `ws-binding:5066 / wss-binding:7443`。
