# OTP 语音验证码（Hermes 代码梳理）

> 目标读者：要在 hermes-mock 里验证「OTP 语音验证码」外呼链路、或排查 `hermes-otp` 行为的开发者。
> 范围：业务方调 OpenAPI 请求给某号码下发**语音验证码** → `hermes-otp` 外呼该号码 → 接通后 TTS 播报验证码 → 挂机回结果 的端到端链路；以及 OTP 外呼打到 hermes-mock 时长什么样、mock 该如何配合。
> **不**覆盖：坐席手动外呼（见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md)）、前端坐席 SDK（见 [frontend-call-sdk.md](frontend-call-sdk.md)）、群呼/机器人外呼。
> 基于：**当前 HEAD 源码核查**（2026-06，二次核查）；源码根与引用约定见 [README.md](README.md)（路径相对 `/Users/Xuxx/IdeaProjects/hermes`）。
> 证据约定：每条结论尽量带「路径:行号」；推断标「未在代码中找到，推断」，落地前实测确认。
> ⚠️ **本篇取证两度受限**：两次核查期间 Bash/IDE 全文搜索/glob/列目录/idea-mcp 等**所有非 Read 工具的安全分类器持续降级不可用**，仅原生 `Read`（需确切路径）可用。因此凡带行号的结论均为逐文件实读所得、可信；而**无法枚举目录 / 全文搜索**，导致 OTP **发送入口 Controller / 请求 DTO / 验证码生成存储 / TTS 文案拼装 / `callType()==OTP` 的业务 handler / playback·originate 下发 / 结果落库与回调** 等文件**仍未能定位到确切路径**（按确切类名/包名逐一试探 `Read` 未命中），相关结论按线索标「推断」、**行号留空待补**。本轮已新增直证：**`CompositeEventDispatcher.getHandler` 的 bug 逐行核实**（见 §2.6）、`CallTypeEnum` 顺序（OTP 首位）、knife4j 依赖（OpenAPI 文档运行时自动生成）。
> 📄 **官方文档**：`hermes-otp/README.md` 仅有标题一行（空）；`hermes/docs` 目录因列目录工具不可用未能枚举（未确认是否有 OTP 专篇）。`build.gradle.kts:22` 引入 **knife4j**，说明 OTP 的 OpenAPI 接口文档是**运行时由 Swagger/knife4j 自动生成**（启动后访问 `/doc.html`），仓库内无静态可链接的接口文档。

---

## 1. 一句话概述 + 端到端时序

**一句话**：业务方经 OpenAPI `POST /otp/openapi/send`（网关产品前缀 `otp`，落到 `hermes-otp` 服务的 `/openapi/send`）提交 `{to, templateCode, params, encrypted}`；`hermes-otp` 生成验证码、用 Azure TTS 合成语音，经 `fs-esl-proxy` 在 FreeSWITCH 上 `originate sofia/external/{prefix}{被叫}@{网关}` 外呼客户号码；接通后在该腿上 `playback` 念验证码（可念多遍、可能支持 DTMF 重听），念完挂机，按 `CHANNEL_HANGUP_COMPLETE` 落库并（推断）回调送达/接听结果。**OTP 与"实时语音通知/外呼任务"复用同一套 ESL 事件分发框架**（`CallTypeEnum.OTP / AUTO_CALL / OUTBOUND_TASK`）。

```
业务方                网关(hermes-gateway)        hermes-otp                fs-esl-proxy            FreeSWITCH              被叫客户(=hermes-mock)
  |                        |                          |                          |                        |
  |--POST /otp/openapi/send|                          |                          |                        |
  |  {to,templateCode,     |--(注入ORG_CODE_KEY)----->| ① 校验机构/模板          |                        |
  |   params,encrypted}    |   /openapi/send          | ② 生成验证码(存Redis?)   |                        |
  |                        |                          | ③ 拼TTS文案 + Azure合成  |                        |
  |                        |                          |   (com.hermes.azure.tts) |                        |
  |                        |                          |--④ POST /esl/originate-->| originate              |
  |                        |                          |   (sip_h_x-call_bot_type | sofia/external/         |
  |                        |                          |    =otp, x-scene=otp)    |   {prefix}{被叫}@{网关}--+--INVITE----->|
  |<-- 200 已受理(同步) ----|<-------------------------|                          |   &park()              |        振铃/接听
  |                        |                          |                          |<--ESL CHANNEL_CREATE----|<----180/200--|
  |                        |                          |<-Kafka esl-event_otp-----|   /ANSWER/PARK         |
  |                        |                          |   onCallStart/onAnswer   |                        |
  |                        |                          |--⑤ playback 验证码(念N遍)------------------------>| 播 TTS 音频  |
  |                        |                          |<-PLAYBACK_STOP onPlaybackEnd----------------------|             |
  |                        |                          |   (念够遍数→hangup)      |                        |
  |                        |                          |<-CHANNEL_HANGUP_COMPLETE onCallEnd----------------|--BYE-------->|
  |                        |                          | ⑥ 落库 + (推断)回调结果  |                        |
```

要点：
- ④⑤ 的 FS 命令下发走 `fs-esl-proxy`（`POST /esl/originate` 等），事件回流走 `fs-esl-proxy → Kafka topic `esl-event_otp` → hermes-otp`。**已直证事件回流**；④⑤ 的 originate/playback 下发**未直证（推断），形态对照 call-center `Bridge`/`OriginateCommand`**。
- OTP 是**纯外呼+放音**，没有人工坐席腿，与群呼/语音通知同框架；与坐席外呼（先 park 坐席腿再 bridge 客户腿）形态不同。

---

## 2. 逐环节说明（带证据）

### 2.1 OTP 服务定位与依赖
- **结论**：OTP 是独立 Spring Boot 服务 `hermes-otp`（`spring.application.name=hermes-otp`），包根 `com.hermes.otp`，独立 MySQL 库 `otp`，用 **Redisson(Redis)**、**Azure Speech(TTS/ASR)**、**Kafka**、**S3/OSS（音频）**、**OpenFeign**、**libphonenumber**。
- **证据**：
  - 模块登记：`settings.gradle.kts:10`（`include("hermes-otp")`）。
  - 主类/包根/Feign/定时：`hermes-otp/src/main/kotlin/com/hermes/otp/HermesOtpApplication.kt:9-13`（`@MapperScan("com.hermes.otp.mapper")`、`@EnableFeignClients`、`@EnableScheduling`）。
  - 依赖：`hermes-otp/build.gradle.kts:21`(redisson)、`:24`(azure.speech)、`:26`(libphonenumber)、`:34`(spring-kafka)、`:35`(aws s3)、`:29`(openfeign)。
  - 配置：`hermes-otp/src/main/resources/application-test.yml:31`（DB `otp`）、`:4-26`(redisson)、`:58-64`（`com.hermes.azure.tts-region=eastus` / `tts-subscription-key` / `asr-*`）、`:42-47`(OSS bucket)、`:51`(mapper `classpath:mapper/*Mapper.xml`)。
- **要点**：TTS 用 **Azure Speech**（区域 `eastus`），不是 FS 自带 mod_tts；验证码语音多半是「服务端合成音频文件 → 上传 OSS / 本地 → FS `playback` 播放」。（合成→落盘→playback 的具体实现**未在代码中找到，推断**。）

### 2.2 发送入口（OpenAPI）
- **结论**：业务方经网关 `POST /otp/openapi/send`，网关按产品前缀 `otp` 转发到 `hermes-otp` 的 `/openapi/send`，注入机构身份头 `ORG_CODE_KEY/ORG_NAME_KEY`（网关模式查 `t_secret_key` 得机构；direct 模式由调用方注入）。请求体含 `to`（被叫手机号）、`templateCode`（验证码模板）、`params`（模板变量，含验证码值等）、`encrypted`（号码是否加密）。
- **证据（来自 mock 侧客户端，已直证 OpenAPI 契约）**：
  - `hermes-mock/internal/hermesopenapi/api.go:298-305`（`SendOTP` → `POST /openapi/send`，body `{to, templateCode, encrypted:false, params}`）。
  - 产品前缀常量：`hermes-mock/internal/hermesopenapi/client.go:39`（`prodOTP = "otp"`）、`:103-104`（direct 模式取 `OTPURL`）、`:82-83`（gateway 模式拼 `{gatewayURL}/otp{path}` 带 `X-OpenApi-Key`）。
  - 鉴权与身份头注入语义：`client.go:4-8` 注释、`:26-33`（`ORG_CODE_KEY` 等头名，对照 hermes 网关 `OpenApiAuthFilter`）。
- **⚠️ Hermes 侧 Controller 类/路径/DTO 未直证**：逐文件试探（`controller/OpenApiController.kt`、`controller/openapi/*`、`controller/*OtpController.kt`、`service/*` 等）均未命中，**因目录枚举工具不可用**。**Controller 类名、`/openapi/send` 的 `@PostMapping`、请求 DTO 字段（手机号/模板/机构/回调 url）均「未在代码中找到，推断」，行号待补。** 推断 DTO 至少含 `to / templateCode / params / encrypted`（与 mock 客户端一一对应）；回调地址是否在请求体里传、还是机构配置里配，**待核**。

### 2.3 验证码生成 / 存储 / TTS 文案
- **结论（推断）**：验证码由 `hermes-otp` 生成或由调用方在 `params` 里传入；存储很可能在 **Redis（Redisson）**（用于"重听/防重/校验码有效期"）。TTS 文案 = 模板（`templateCode`）+ `params` 渲染后的文本，交 Azure Speech 合成。
- **证据**：依赖层面已直证 Redisson(`build.gradle.kts:21`)、Azure TTS(`application-test.yml:58-64`)。`common` 有 `RandomUtils`（`HermesOtpApplication` 同款 import 见 `listener/FsEventKafkaListener.kt:7`），生成随机码的常见工具。
- **⚠️ 验证码到底是"系统生成"还是"调用方传值"、Redis key/TTL、模板存哪（DB `otp` 表 / 配置）、文案拼接代码——均「未在代码中找到，推断」。** 注意 mock 客户端把验证码当作 `params` 的一部分传（`SendOTP` 的 `params map`），**倾向于"调用方在 params 里给值或给模板变量"**，但需核 Controller/Service 才能定论。

### 2.4 外呼发起（originate / 选线 / 外显号）
- **结论（推断，形态对照）**：`hermes-otp` 不自己连 FS，而是经 `fs-esl-proxy` 的 `POST /esl/originate` 让 FS 执行 `{params}sofia/external/{calleePrefix}{被叫}@{网关} &park()`，外呼成功后客户腿 park 等业务驱动 playback。选线/外显号/前缀由 `hermes-otp` 决定后通过 originate 参数（gateway/caller/calleePrefix）传入。
- **证据**：
  - originate 形态与 `&park()`：见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §4 表（`OriginateCommand.kt:94-103`，自动外呼通用形态）——**该证据在 call-center/通用模块，OTP 复用同一 fs-esl-proxy originate 协议（推断）**。
  - 路由头：FS 命令里写 `sip_h_x-scene`（fs-esl-proxy 据此路由 Kafka topic）与 `sip_h_x-call_bot_type=otp`（hermes-otp 内部分发用，见 §2.6）。`x-scene=otp` **推断**（topic 名为 `esl-event_otp` 已直证，见 `FsEventKafkaListener.kt:23`）。
- **⚠️ OTP 的选线/限频/外显号实现未直证**：是否复用 call-center 的 `LinePhoneInfoService` 选线、还是 OTP 自己查机构线路、限频（同号/机构 QPS）策略——**「未在代码中找到，推断」**。Azure TTS 合成 + originate 的串联代码（Service/Handler）未定位到。

### 2.5 接通后播报验证码（playback / 念几遍 / 重听）
- **结论（推断）**：客户腿接听（`CHANNEL_ANSWER` → `onAnswer`）后，`hermes-otp` 在该腿上 `playback` 验证码音频；播完一遍触发 `PLAYBACK_STOP`（→ `onPlaybackEnd`），由 handler 决定**再念一遍还是挂机**（典型 OTP 念 2–3 遍）。`DTMF`（→ `onDTMF`）的存在**暗示支持按键重听/确认**。
- **证据（事件钩子已直证，业务逻辑未直证）**：
  - 事件分发含 `PLAYBACK_STOP -> onPlaybackEnd`、`DTMF -> onDTMF`、`CHANNEL_ANSWER -> onAnswer`、`CHANNEL_PARK -> onPark`：`hermes-otp/src/main/kotlin/com/hermes/otp/listener/FsEventKafkaListener.kt:36-74`。
  - 处理器接口定义这些钩子（默认空实现）：`hermes-otp/src/main/kotlin/com/hermes/otp/listener/CallEventHandler.kt:11-51`。
- **⚠️ "念几遍""遍数从哪配""重听按键是几号键""playback 命令怎么下发"——均「未在代码中找到，推断」，行号待补**（需读 `callType()==OTP` 的那个 handler，其文件名未定位到）。

### 2.6 ESL 事件流（CHANNEL_CREATE / ANSWER / PLAYBACK / HANGUP / DTMF）— 已直证
- **结论**：`fs-esl-proxy` 把 FS ESL 事件按 `sip_h_x-scene` 路由进 Kafka topic；`hermes-otp` 消费 **`esl-event_otp`**，按 `Event-Name` 分发到 `CompositeEventDispatcher`，再按 `CallTypeEnum`（取自 `variable_sip_h_x-call_bot_type`）路由到具体 handler。
- **证据**：
  - 消费 topic + groupId：`hermes-otp/.../listener/FsEventKafkaListener.kt:23`（`TOPIC_FS_EVENT = "esl-event_${FsConstant.OTP_SERVICE_NAME}"`）、`:24`（group `otp-fs-event-consumer`）；服务名常量 `hermes-otp/.../constant/FsConstant.kt:5`（`OTP_SERVICE_NAME="otp"`）。
  - 事件名 → 钩子映射：`FsEventKafkaListener.kt:36-74`（`CHANNEL_CREATE→onCallStart:41-43`、`CHANNEL_ANSWER→onAnswer:45-47`、`CHANNEL_HANGUP_COMPLETE→onCallEnd:49-51`、`CHANNEL_CALLSTATE(RINGING)→onRinging:53-57`、`CHANNEL_BRIDGE→onBridge:59-61`、`PLAYBACK_STOP→onPlaybackEnd:63-65`、`CHANNEL_PARK→onPark:67-69`、`DTMF→onDTMF:71-73`、`BACKGROUND_JOB→onBackgroundJobResult:37-39`）。
  - 分发器按 callType 选 handler：`hermes-otp/.../listener/business/CompositeEventDispatcher.kt:18-24`（`afterSingletonsInstantiated` 按 `it.callType()` 注册到 `handlerMap`，`:20` 排除自身）、`:26-60`（各事件委派 `getHandler(e)?.onXxx`）。**注意 `getHandler:66-75` 有 bug，见本节末。**
  - 通话类型枚举与路由字段：`hermes-otp/.../enums/CallTypeEnum.kt:7-9`（`OTP("otp","一句话验证码")`、`AUTO_CALL`、`OUTBOUND_TASK`）、`:15`（`CALL_TYPE_FS_FIELD="sip_h_x-call_bot_type"`）、`:20`（事件字段 `variable_sip_h_x-call_bot_type`）。
  - 主叫/被叫/SID 字段名（common）：`hermes-common/.../constant/freeswitch/EslEventConstant.kt:36`（`CALLER="Caller-Caller-ID-Number"`）、`:43`（`CALLEE="Caller-Destination-Number"`）、`:125`（`SID="variable_sip_h_x-session-id"`）。
  - 挂机统计口径（拨号/振铃/接听/通话时长，从 FS 时间戳算）：`CallEventHandler.kt:53-117`（`getEndBaseInfo`）、`:139-150`（`CallEndBaseInfo` 字段）。
- **✅ `CompositeEventDispatcher.getHandler` 确为 bug（已逐行核实）**：`getHandler(eslEvent)` 体内（`CompositeEventDispatcher.kt:66-75`）写的是：
  ```kotlin
  private fun getHandler(eslEvent: EslEvent): CallEventHandler? {
      CallTypeEnum.entries.forEach {
          val handler = handlerMap[it]
          if (handler == null) { logger.error("未找到对应的通话处理器: {}", it) }
          return handler          // ← 第一轮迭代即 return，forEach 形同虚设
      }
      return null
  }
  ```
  **它根本没读 `eslEvent`（参数未被使用），也没按事件里的 `variable_sip_h_x-call_bot_type`/`CallTypeEnum` 做匹配**——`forEach` 第一轮（即 `CallTypeEnum.entries` 的第一个枚举）就无条件 `return handler`。因此**永远只返回 `entries[0]` 对应的 handler**：若该 handler 已注册就返回它，未注册（null）则直接返回 null（连后续枚举都不再尝试）。
  - **为何当前不炸**：`CallTypeEnum` 第一个枚举正是 `OTP`（`CallTypeEnum.kt:7`，已核实顺序 OTP→AUTO_CALL→OUTBOUND_TASK），且 `hermes-otp` 这一个服务里只要注册了 OTP handler，所有事件就都路由到它——对一个**只跑 OTP 的服务**恰好等价于"正确"。
  - **判定**：这是**真实的逻辑 bug（而非 OTP 特例的有意写法）**——正确实现应是 `handlerMap[CallTypeEnum.fromEvent(eslEvent)]` 之类按事件 callType 取值。只是因为本服务事实上单一业务（且 OTP 排在 entries 首位）而**被掩盖**。注意 `CompositeEventDispatcher` 自身 `callType()` 返回的是 `AUTO_CALL`（`:62-64`），但它在 `afterSingletonsInstantiated` 里会把自己排除（`:20` `if (it !is CompositeEventDispatcher)`），不参与注册。
  - **对 mock 的影响**：无——OTP 事件本就该进 OTP handler，结果正确；但若将来该服务接入 `AUTO_CALL`/`OUTBOUND_TASK` 第二种 callType，事件会被错误地全路由到 OTP handler。

### 2.7 挂机、结果落库、回调
- **结论（部分直证）**：挂机由 `CHANNEL_HANGUP_COMPLETE → onCallEnd` 处理，handler 用 `getEndBaseInfo` 从 FS 时间戳算出 hangupCode/各类时长后落库（DB `otp`）；送达/接听结果**回调**很可能经 `hermes-callback` 模块或 Kafka 异步发出。
- **证据**：`onCallEnd` 入口 `FsEventKafkaListener.kt:49-51`；时长/挂机原因解析 `CallEventHandler.kt:53-117`；存在独立 `hermes-callback` 模块 `settings.gradle.kts:12`。
- **⚠️ 回调具体形态（webhook URL 来源、重试、签名）、落库表结构（DB `otp` 的 mapper/entity）——「未在代码中找到，推断」，行号待补。**

---

## 3. 关键接口 / 数据结构 / 信令

| 类型 | 名称 | 说明 | 证据 |
|---|---|---|---|
| HTTP | `POST /otp/openapi/send`（网关）→ `/openapi/send`（服务） | 下发语音验证码 | mock `api.go:298-305`；前缀 `client.go:39` |
| Body | `{to, templateCode, params, encrypted}` | 被叫号 / 模板码 / 模板变量(含验证码?) / 号码是否加密 | mock `api.go:299-304`（Hermes 侧 DTO 未直证，推断） |
| Header | `ORG_CODE_KEY` / `ORG_NAME_KEY`（direct）或 `X-OpenApi-Key`（gateway） | 机构身份 | `client.go:26-33,82-90` |
| Kafka | topic `esl-event_otp`，group `otp-fs-event-consumer` | OTP 消费 ESL 事件 | `FsEventKafkaListener.kt:23-24` |
| 枚举 | `CallTypeEnum.OTP("otp")` / `AUTO_CALL` / `OUTBOUND_TASK` | 通话类型（同框架多业务） | `CallTypeEnum.kt:7-9` |
| FS头 | `sip_h_x-call_bot_type=otp`（事件 `variable_sip_h_x-call_bot_type`） | OTP 内部分发字段 | `CallTypeEnum.kt:15,20` |
| FS头 | `sip_h_x-scene`（值含 `otp`，推断） | fs-esl-proxy 路由 Kafka topic 用 | topic 名直证 `:23`；头值推断 |
| ESL事件 | `CHANNEL_CREATE/ANSWER/CALLSTATE(RINGING)/BRIDGE/PARK/PLAYBACK_STOP/HANGUP_COMPLETE/DTMF/BACKGROUND_JOB` | 驱动 OTP 状态机 | `FsEventKafkaListener.kt:36-74` |
| ESL字段 | `Caller-Caller-ID-Number`(主叫) / `Caller-Destination-Number`(被叫) / `variable_sip_h_x-session-id`(SID) | 通用字段 | `EslEventConstant.kt:36,43,125` |
| FS命令 | `{params}sofia/external/{prefix}{被叫}@{网关} &park()`（推断复用） | originate 客户腿 | `OriginateCommand.kt:94-103`（通用，OTP 复用为推断） |

---

## 4. 对 hermes-mock 的启示

**OTP 外呼打到 mock 时长什么样**：与群呼/机器人/坐席外呼的客户腿**完全同形**——一条从 FS external profile（线路网关）打来的普通 SIP INVITE，被叫 = `calleePrefix+被叫号(mock 客户号)`、主叫 = 该机构 OTP 线路的外显号。mock 作为「收验证码的客户」只需当一条被叫腿应答即可。

- **可复用 / 已具备**：
  - mock 已能作为 `sofia/external/...@{网关}` 的被叫对端应答（`hermes-mock/deploy/fs/public_hm_echo.xml`、`public_hm_agent.xml`），OTP 客户腿无需新增 mock 协议能力。
  - mock 已有 OTP 触发与观测脚手架：`hermes-mock/internal/hermesopenapi/api.go:298-305`（`SendOTP`）、`internal/orchestrator/orchestrator.go:85-101`（`RunOTP`）、`internal/testkit/kit.go:527-588`（`RunOTPObserved`：下发后 `waitAnyLeg` 断言"客户腿进 mock"）。OTP 配置 `OTP_BASE_URL`/`otpUrl`：`internal/config/config.go:53`、`internal/orgcfg/store.go:31`。
  - **关键配合点**：mock 接听后要**停留足够时长让 TTS 念完几遍**（OTP 念多遍，单次可能十几~几十秒）。当前 `RunOTPObserved` 默认 `WaitSec=30`（`kit.go:554-556`）且只断言"有腿进来"，**没有断言听到/收全验证码音频**——这是 mock 验证 OTP 链路的天然边界。
- **做不到 / 需外部依赖 / 需桩**：
  - **听不到/识别不了验证码内容**：mock 是 SIP UAS，能接听并接收 RTP，但要"听懂念的是什么数字"需要 ASR（本地难做）；验证 OTP 正确性更现实的做法是**对账**：mock 拿到的应答时长/通话态 + Hermes OTP 落库/回调里的验证码值比对。回调/落库取数当前 mock 未做（依赖 Hermes 侧回调形态，见 §2.7 待补）。
  - **RTP/录音本地难通**：见 [../STATUS.md](../STATUS.md) 已知问题；mock 侧"听 TTS"属深水区。
  - **依赖真实 `hermes-otp` + fs-esl-proxy + Kafka + Redis + Azure TTS**：要跑通完整 OTP，需真实 OTP 服务与中间件，mock 只补被叫腿。
- **建议（记录，不在本任务实现）**：让 mock OTP 客户腿**应答后保持 ≥ 一遍 TTS 时长再挂**（避免提前 BYE 截断播报），并把 `WaitSec` 调到覆盖"接通 + 念 N 遍"的总时长；若要校验码值正确，需打通 Hermes OTP 结果回调/查询接口（待 §2.2/§2.7 补全）。

---

## 5. 待补 / 推断项

- ⚠️ **OTP 发送 Controller 的类名 / `@RequestMapping` / DTO 字段 / 行号**：未直证（目录枚举工具不可用，逐文件试探未命中）。仅从 mock 客户端反推契约 `{to,templateCode,params,encrypted}`。
- ⚠️ **验证码生成方（系统生成 vs 调用方传值）、Redis key/TTL、模板存储与 TTS 文案拼接**：推断（依赖 Redisson/Azure 已直证，逻辑代码未定位）。
- ⚠️ **念几遍、重听 DTMF 键位、playback/originate 下发代码**：事件钩子（`onAnswer/onPlaybackEnd/onDTMF`）已直证，业务实现未定位（`callType()==OTP` 的 handler 文件名未找到）。
- ⚠️ **选线/外显号/限频策略**：是否复用 call-center `LinePhoneInfoService`，未直证。
- ⚠️ **结果回调（送达/接听）形态**：webhook URL 来源、重试、签名、落库表结构（DB `otp`）——未直证；`hermes-callback` 模块存在（`settings.gradle.kts:12`）但与 OTP 的关系未证。
- ✅ **`CompositeEventDispatcher.getHandler` 已核实为真实 bug**：`:66-75` 的 `forEach` 第一轮即 `return handler`、且完全没用 `eslEvent` 的 callType——恒返回 `entries[0]`(=OTP) 的 handler。因本服务只跑 OTP 且 OTP 排首位而被掩盖，当前不影响 OTP；详见 §2.6。
- ⚠️ **`x-scene` 头实际取值**：topic `esl-event_otp` 已直证，但 originate 写入的 `sip_h_x-scene` 具体字符串未直证。

## 附：关键证据索引（路径:行号）

- `settings.gradle.kts:10,12`：`hermes-otp`、`hermes-callback` 模块登记。
- `hermes-otp/build.gradle.kts:21,24,26,29,34,35`：Redisson / Azure Speech / libphonenumber / OpenFeign / Kafka / S3 依赖。
- `hermes-otp/src/main/kotlin/com/hermes/otp/HermesOtpApplication.kt:9-13`：包根 `com.hermes.otp`、mapper 扫描、Feign、定时任务。
- `hermes-otp/src/main/resources/application-test.yml:31,58-64,42-47`：DB `otp`、`com.hermes.azure.tts-*`(eastus)、OSS 音频桶。
- `hermes-otp/.../listener/FsEventKafkaListener.kt:23-24`：消费 topic `esl-event_otp` / group `otp-fs-event-consumer`；`:36-74`：ESL 事件名→钩子映射（含 `PLAYBACK_STOP→onPlaybackEnd`、`DTMF→onDTMF`）。
- `hermes-otp/.../constant/FsConstant.kt:5`：`OTP_SERVICE_NAME="otp"`。
- `hermes-otp/.../listener/business/CompositeEventDispatcher.kt:11-24`（按 callType 注册 handler，`:20` 排除自身）、`:26-60`（各事件委派 `getHandler(e)?.onXxx`）、`:62-64`（自身 `callType()=AUTO_CALL`）、`:66-75`（**getHandler 选择逻辑确为 bug**：forEach 首轮即 return，恒返回 entries 首个 handler，未用事件 callType）。
- `hermes-otp/.../enums/CallTypeEnum.kt:7-9,15,20`：`OTP/AUTO_CALL/OUTBOUND_TASK`，路由字段 `sip_h_x-call_bot_type`。
- `hermes-otp/.../listener/CallEventHandler.kt:11-51,53-117,139-150`：事件钩子接口 + 挂机时长/原因解析。
- `hermes-common/.../constant/freeswitch/EslEventConstant.kt:36,43,125`：主叫/被叫/SID 字段名。
- mock 侧：`internal/hermesopenapi/api.go:298-305`（SendOTP 契约）、`client.go:39,82-90,103-104`（前缀/鉴权/baseURL）、`internal/orchestrator/orchestrator.go:85-101`（RunOTP）、`internal/testkit/kit.go:527-588`（RunOTPObserved 断言）、`internal/config/config.go:53`、`internal/orgcfg/store.go:31`（OTP 地址配置）。
- 通用 originate 形态（OTP 复用为推断）：`OriginateCommand.kt:94-103`，转引 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §4。
