# call-bot AI 机器人外呼任务（Hermes 代码梳理）

> 目标读者：要理解 Hermes `call-bot` 服务「AI 机器人外呼任务」端到端机制、并据此让 hermes-mock 充当「被机器人外呼的客户被叫腿」做验证的开发者。
> 范围：建机器人外呼任务（导入号码）→ 批量外呼 → 接通后由机器人/IVR（ASR+TTS+话术流程）接管 → 挂机/CDR/回调。**不**覆盖：坐席手动外呼（见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md)）、坐席工作台前端 SDK（见 [frontend-call-sdk.md](frontend-call-sdk.md)）、call-center 群呼（与本篇是同构但不同服务的链路）。
> 基于：**当前 HEAD 源码核查**（2026-06，本轮已逐文件实读 `hermes-call-bot/*.kt` 补全行号）；源码根与引用约定见 [README.md](README.md)。
> 证据约定：每条结论尽量带「路径:行号」（路径相对 `/Users/Xuxx/IdeaProjects/hermes`）。
> **官方文档可链接**：Hermes 仓库自带模块文档 [`docs/modules/call-bot.md`](../../../hermes/docs/modules/call-bot.md)（源码结构 / Service / ESL handler / 枚举 / Kafka / DDL 总览，本篇行号与之交叉核对）、总入口 [`docs/INDEX.md`](../../../hermes/docs/INDEX.md)。`hermes-call-bot/README.md` 为空壳（仅标题）。
> 包根：`com.hermes.call.bot`（即 `hermes-call-bot/src/main/kotlin/com/hermes/call/bot/`）。
> 证据级别：①**mock 侧直证**＝`internal/hermesopenapi/*.go`（mock 调 Hermes 的唯一通道）。②**Hermes 源码实读**＝本轮逐文件 Read 所得行号（可信）。③**DDL 直证**＝`deploy/hermes-stack/ddl/call-bot.sql`（与 `hermes/docs/architecture/ddl/call-bot.sql` 同源）。仅个别「话术 Handler 内具体 FS app 串」「AI_CALL Dify 对话细节」标注为「已定位类但未逐行读，推断」。

---

## 1. 一句话概述 + 端到端时序图

**一句话**：经 OpenAPI `POST /openapi/task/create-and-import` 在 `hermes-call-bot` 建一个外呼任务（带 taskType / robotCode / salesScriptCode / 号码列表 / 外呼时段），Quartz `NotifyDialJob` 到点把任务塞进 Redis 运行队列，`TaskNumberScanner` 按时段+并发取号发 Kafka `call-bot_task_call_dial`，`OutboundTaskDialer` 消费后用**自有 `LinePhoneInfoService` 选线 + `DistributeConcurrentService` 取并发许可 + 解密被叫**，经自有 `FreeswitchApi`（复用 fs-esl-proxy `OriginateReq`）让 FS 拨出**无人工坐席的客户腿**；客户接听后，call-bot 经 `FsEventKafkaListener`(topic `esl-event_call-bot`)→`CompositeEventDispatcher`→`OutboundTaskEventHandler`(call-type `CALL_CUSTOMER_PROXY`)→按 taskType 分发到 `SalesScriptDispatcher`(IVR 话术，TTS 放音+DTMF 按键收集) / `CallAgentEventHandler`(AI_CALL，Dify 大模型对话) / `NumberDetectionHandler`(号码探测)接管，最后挂机写 `t_cdr_*` + 轨迹 `t_call_traces`，发 Kafka 回调 topic 由 hermes-callback 推 `callback_url`。

```
mock orchestrator / 前端           hermes-call-bot                fs-esl-proxy            FreeSWITCH            被叫客户腿(=hermes-mock)
   |                                  |                              |                       |                       |
   |--(1) POST /openapi/task/         |                              |                       |                       |
   |       create-and-import -------->| 建 t_call_task + 导入号码     |                       |                       |
   |   {name,taskType,robotCode,      |  (number_imported=1,         |                       |                       |
   |    salesScriptCode,numbers[]}    |   生成 t_cdr_task 待拨记录)    |                       |                       |
   |<-- {taskCode} -------------------|                              |                       |                       |
   |                                  |                              |                       |                       |
   |                          (2) Quartz 调度：按 dial_time_period   |                       |                       |
   |                              + concurrent 并发 取待拨号码        |                       |                       |
   |                                  |--(3) POST /esl/originate----->|--(4) ESL originate--->|                       |
   |                                  |   {service,uuid,callee,       |   sofia/external/     |--(5) INVITE---------->|
   |                                  |    caller,callerAddress,...}  |   {prefix}{被叫}@{网关}|   主叫=线路号          |  按行为档应答
   |                                  |                              |   &park()             |<----- 180/200 --------|
   |                                  |<==(6) ESL CHANNEL_CREATE / CALLSTATE(RINGING) / ANSWER / PARK ==(Kafka esl-event_callbot)|
   |                                  |                              |                       |                       |
   |                          (7) onAnswer：在客户腿上驱动机器人      |                       |                       |
   |                              TTS 放音 / ASR 收音 / 话术流程       |--(execute playback/   |--(8) RTP 媒体--------->|  收 TTS 音
   |                              /DTMF 分支 / Dify 对话              |   detect_speech/      |<-- DTMF / 语音 -------|  回按键/语音
   |                                  |                              |   play_and_get_digits)|                       |
   |                                  |<==(9) ESL DTMF / PLAYBACK_STOP / DETECTED_SPEECH ====================(Kafka)|
   |                                  |                              |                       |                       |
   |                          (10) 流程结束→挂机；写 t_cdr_call_bot / |--(hangup)------------>|--(11) BYE------------>|
   |                               t_cdr_task / t_call_traces        |                       |                       |
   |<==(12) webhook 回调 callback_url（任务/识别结果）================|                       |                       |
```

要点：
- **(1) 是 OpenAPI HTTP**（mock 已直证 + Hermes Controller 实读）。**(3)(4)** 是 ESL originate：call-bot 复用 fs-esl-proxy 的 `OriginateReq`（`OutboundTaskDialer.kt:60` import `com.hermes.fs.esl.proxy.entity.req.OriginateReq`），经自有 `FreeswitchApi` 下发。**(2)(7)(10) 是 call-bot 内部逻辑**：(2) Quartz `NotifyDialJob` 入运行队列 + `OutboundTaskDialer` 消费 `TASK_CALL_DIAL_TOPIC` 拨号；(7) `OutboundTaskEventHandler`→`SalesScriptDispatcher`/`CallAgentEventHandler` 接管；(10) 落库。均已实读，见下文。
- 与坐席外呼最大的不同：**没有「坐席腿」**。call-bot 的 originate 是独立一腿（客户腿），接通后由 call-bot 在这条腿上驱动 playback/detect，而非 bridge 第二条腿（AI_CALL 场景才可能再起机器人腿，见 §2.4）。
- 对 mock：**(5) 的入向 INVITE 与坐席外呼/群呼是同一种**——`sofia/external/{prefix}{被叫}@mock`，主叫=线路号。mock 只需作被叫 UAS 应答、收 TTS、按行为档回 DTMF。
- ⚠️ **call-type 头与 call-center 不同**：call-bot 用 `sip_h_x-call_bot_type`（ESL 事件侧 `variable_sip_h_x-call_bot_type`），call-center 用 `x-call_center_type`。证据：`hermes-call-bot/src/main/kotlin/com/hermes/call/bot/constant/enums/CallTypeEnum.kt:26,31`。

---

## 2. 逐环节说明（带证据）

### 2.1 任务创建 / 号码导入（OpenAPI 入口）
- **结论**：call-bot 任务创建走 `POST /openapi/task/create-and-import`，一次性建任务并导入号码。Controller：`TaskOpenApiController`（`@RequestMapping("/openapi/task")`）。
- **直证（Hermes Controller，实读）**：`hermes-call-bot/src/main/kotlin/com/hermes/call/bot/controller/openapi/TaskOpenApiController.kt:29-34`（`@RestController @RequestMapping("/openapi/task")`，依赖 `CallTaskService`/`TaskCdrService`）；建任务并导号端点 `:109-121`（`@PostMapping("create-and-import")` → `taskService.createTaskAndImportNumbers(req, orgCode, orgName, source=TaskNumberSourceEnum.OPENAPI)`）；单独导号 `:103-107`（`POST number/import`）；另含 `status/start`/`status/stop`/`cancel`/`pause`/`resume`/`subtasks`/`list`/`detail/{taskCode}`/`cdr/{callUuid}/record-url`（`:39-160`）。
- **直证（mock 侧）**：`internal/hermesopenapi/api.go:280-283`（`CreateCallBotTask` → `POST /openapi/task/create-and-import`）；`internal/hermesopenapi/client.go:38`（`prodCallBot="call-bot"`）；调用方组装 body 见 `internal/orchestrator/orchestrator.go:48-61`、`:197-204`（`numberInfos` 把号码转 `[{"number":n}]`）。
- **直证（请求 DTO，实读）**：`CreateAndImportTaskReq` = `hermes-call-bot/.../entity/request/calltask/CreateAndImportTaskReq.kt:22-133`。字段：`name`(正则 `^[a-zA-Z_0-9]+$`)、`taskType: CallbotTaskTypeEnum`(`:34`)、`robotCode?`/`salesScriptCode?`(`:37/:40`)、`callbackUrl?`(任务完成回调,`:48`)、`confirmUrlBeforeDial?`(呼前确认,`:52`)、`maxRedialTimes?`(1-5)/`redialInterval?`(0-60min)/`redialConditions: Set<RingTypeEnum>?`(`:57-65`)、`isFirstDialPriority`(首呼优先,默认 true,`:69`)/`isPriorityTask`(`:73`)、`startDate?`/`endDate?`/`dialTimePeriod: List<String>?`(如 `09:30-11:50`,`:85-103`)、`numbers: List<NumberInfo>`(1-10000,`:108`)、`encrypted`(`:111`)、`ttsTextVariableMap?`(`:115`)、`duplicateHandleType?`(去重,`:118`)、`validityType?: TaskValidityTypeEnum`(默认 CUSTOM,`:121`)、`concurrent?`(1-2000,`:126`)、`ttsCode?`/`ttsText?`(号码探测用,`:129/:132`)。`RingTypeEnum` 在 hermes-common（`com.hermes.common.constant.enums.RingTypeEnum`）。
- **直证（数据模型）**：任务落 `callbot.t_call_task`，字段对应 `code/org_code/name/task_type/robot_code/robot_version_code/status/start_date/end_date/timezone/dial_time_period/number_count/sales_script_code/max_redial_times/redial_interval/callback_url/number_imported/tts_code/tts_text/sales_script_type/confirm_url_before_dial/is_first_dial_priority/is_priority_task/number_source/validity_type/concurrent`。证据：`deploy/hermes-stack/ddl/call-bot.sql:304-352`。号码导入后 `number_imported=1`、`number_count` 计数（`:327`、`:318`）。
- **直证（枚举，实读）**：`CallbotTaskTypeEnum` = `hermes-call-bot/.../constant/enums/CallbotTaskTypeEnum.kt:6-22`，取值 **`IVR(1)` / `AI_CALL(2)` / `NUMBER_DETECTION(3)`**（mock 注释「1=IVR 2=AI_CALL」正确，另有第三类号码探测）。号码来源 `TaskNumberSourceEnum` = `.../constant/enums/TaskNumberSourceEnum.kt:6-14`，`OPENAPI(1)`/`ORG_MANAGER(2)`；OpenAPI 入口固定传 `OPENAPI`（`TaskOpenApiController.kt:105,118`）。话术类型 `SalesScriptTypeEnum` = `.../constant/enums/SalesScriptTypeEnum.kt:9-19`，`ONE_SENTENCE_NOTIFICATION(1 一句话通知)` / `INTENTION_COLLECTION(4 按键收集意向)`（2/3 已注释停用）。

### 2.2 机器人 / 话术 / 模板配置（任务引用的资源）
- **结论**：任务不内嵌话术，而是**引用**预先建好的机器人(robot)、话术(salesScript)、（autocall 场景下）通知模板(template)。
- **直证（数据模型）**：
  - 机器人 `t_call_bot`：`robot_type`(类型)、`intent_code`(意向标签)、`max_concurrency`(机器人级最大并发)。证据：`ddl/call-bot.sql:243-265`。
  - 机器人版本 `t_call_bot_version`：**`asr_code`(ASR)、`tts_code`(TTS)、`tts_config`、`variable_list`(变量)、`model_config`(大模型配置)、`prompt_config`(prompt)、`flow_config`(会话流程配置)、`status`(草稿0/发布1)**。证据：`ddl/call-bot.sql:272-298`。→ **这是「话术流程引擎」配置的落点**：流程图/prompt/模型都在版本表，任务通过 `robot_version_code`(`t_call_task:312`) 绑定具体版本。
  - 话术 `t_sales_script`：`type`(话术类型)、`tts_code/tts_text`、**按键 IVR 相关：`key_guide_text`(引导话术)、`error_input_prompt_text`、`key_intention_map`(意向采集映射)、`wait_time_sec`(等待输入)、`max_error_input_count`、`max_wait_input_intention_count`、`end_input_prompt_text`**。证据：`ddl/call-bot.sql:717-748`。→ **DTMF 按键收集 / 意向识别的参数都在话术表**（IVR 型任务）。
  - autocall 模板 `t_auto_call_template`：`sales_script_code`、`tts_code/tts_text`、`scenario_code`、`redial_max_times`、`redial_interval`。证据：`ddl/call-bot.sql:209-236`。
- **技术栈直证**：`hermes-call-bot/build.gradle.kts:20`（`libs.azure.speech`，artifact jar）= **Azure 语音 ASR/TTS**；`:24`（`libs.dify.client`）= **Dify 大模型对话**（AI_CALL 型）；`:13`（`spring-boot-starter-quartz`）= **任务调度器**；`:23`（`libs.disruptor`）= 高吞吐事件环（疑用于 ASR/对话流水线，⚠️ 推断）；`:25`（`libs.libphonenumber`）= 号码规整/国家码。

### 2.3 任务调度 / 批量发起（Quartz → 运行队列 → Kafka 拨号器 → originate）
- **结论**：任务非即时全量拨出，链路是 **Quartz `NotifyDialJob`（到点把任务放入 Redis 运行队列）→ `TaskNumberScanner`（常驻扫号、按时段/并发取待拨号码、发 Kafka `TASK_CALL_DIAL_TOPIC`）→ `OutboundTaskDialer`（消费该 topic，选线 + 取并发许可 + 解密号码 + 经 `FreeswitchApi` originate）**。
- **直证（Quartz 调度，实读）**：`hermes-call-bot/.../backgroud/task/outbound/CallTaskScheduler.kt:22-134`（`saveTask`：按 `dialTimePeriod` 逗号分隔的每个时段 `TimePeriod.toCron()` 建 Cron 触发器，`startAt(startDate)`/`endAt(endDate)`，时区用任务 `timezone`；开始日在次日及以后补一条凌晨触发器 `:107-127`；`createNotifyCompletedJob` 建结束监控 `:173-218`）；`NotifyDialJob.kt:26-143`（`@DisallowConcurrentExecution`；按 `salesScriptType` 预生成 TTS `:39-55`；`runJob` 按 `determineCurrentStatusByDate` 处理 COMPLETED/RESTING/PAUSE/IDLE，IN_PROGRESS 且已导号→`CallTaskHelper.addRunningTask` `:99-135`）；`NotifyCompletedJob.kt:14-32`。
- **直证（运行队列，实读）**：`CallTaskHelper.kt:20-247`——运行中机构集合 Redis zset `hermes:call-bot:task:running:org-code`(`:25-28`)、机构下优先任务集合 `...:running:priority:org:{org}`(`:220-223`) 与非优先集合 `...:running:org:{org}`(`:225-228`)；`getRandomRunningTask()` 用 Lua「取首个并重打分(轮转)」先抽机构再抽任务，**优先集合空才取非优先**(`:70-104`)；`determineCurrentStatusByDate`(`:109-141`) 判时段状态。
- **直证（扫号 → 拨号 topic）**：拨号消息 topic `KafkaConstant.TASK_CALL_DIAL_TOPIC = "call-bot_task_call_dial"`（`constant/KafkaConstant.kt:25`）。常驻扫号器为 `backgroud/task/outbound/TaskNumberScanner.kt`（官方文档 [`docs/modules/call-bot.md`](../../../hermes/docs/modules/call-bot.md) §任务调度：「扫描待拨号码、区分首拨/重拨优先、尊重时间窗口与并发、发布拨号消息到 Kafka」；其并发 key `TaskNumberScanner.taskConcurrentKey(task.code)` 被 `OutboundTaskDialer.kt:389` 引用，类与方法已实证存在，⚠️ 扫号主循环逐行细节未读）。
- **直证（拨号器 originate，实读）**：`backgroud/task/outbound/OutboundTaskDialer.kt:77-105` 是 `DeadLetterConsumer`，消费 `TASK_CALL_DIAL_TOPIC`（group `outbound-task-dialer`，`maxRetryCount=3`）；`handle`→`doDial`(`:237-240`,`:253-`)：校验任务/CDR 状态(`:265-293`)、首呼优先 vs 重呼优先门控(`:326-348`)、`linePhoneInfoService.getAvailablePhones(task.orgCode)` 选线(`:349`)、`distributeConcurrentService.getSemaphoreIdByRandomWeight(...)` 加权随机取线路并发许可(`:414-419`)、任务级并发许可(`:384-413`)、AI_CALL 再取机器人并发(`limiter.tryAcquire`,`:440-464`)、`phoneDecryptService.decryptPhone(...)` 解密被叫(`:469-473`)、`confirmUrlBeforeDial` 呼前确认 webhook(`:480-489`)。最后经 `freeswitchApi`（构造器 `:81`）下发 originate（`OriginateReq` 来自 fs-esl-proxy，`:60`）。
- **外显号 / 线路选择**：**call-bot 自有 `LinePhoneInfoService` + `DistributeConcurrentService`**（不复用 call-center 的）——`OutboundTaskDialer.kt:35` import `com.hermes.call.bot.service.LinePhoneInfoService`、`:84` 注入；选主叫 `linePhoneInfoService.getCaller(phone)`(`:437`)。CDR 落点 `t_cdr_task.line_code/line_name/line_address/callee_prefix/phone_code/caller`(`ddl/call-bot.sql:580-584`/`:564`)。`DistributeConcurrentService` 基于 Redisson `RPermitExpirableSemaphore`（官方文档 §并发控制）。
- **路由头**：originate 写 `sip_h_x-call_bot_type`（call-bot 自有头，**非** call-center 的 `x-scene`/`x-call_center_type`）；fs-esl-proxy 仍按 `x-scene` 路由 Kafka topic 到 `esl-event_call-bot`（见 §2.5）。证据：`CallTypeEnum.kt:26`。

### 2.4 接通后机器人接管（TTS / ASR / 话术流程 / DTMF）
- **结论**：originate 默认 `&park()`，客户接听后**没有第二条腿可 bridge**；call-bot 消费 ESL `CHANNEL_ANSWER` 后，在这条客户腿上下发一系列 FS app 来驱动机器人：放音(`playback`/TTS 合成音)、收音(`detect_speech`/`play_and_get_digits`)、识别(ASR)→ 进话术流程引擎 → 决定下一句 TTS / 是否收 DTMF / 是否转人工。
- **直证（媒体能力来源）**：`build.gradle.kts:20` Azure Speech（TTS 合成 + ASR 识别）、`:24` Dify（AI 对话决策）。话术流程配置在 `t_call_bot_version.flow_config`/`prompt_config`(`ddl:285`/`:284`)，按键脚本在 `t_sales_script`(`:737-743`)。
- **直证（对话/按键落点）**：每轮对话/事件写 `t_call_traces`（`role` 1客户/2坐席/3机器人/4系统、`trace_type` 消息/电话事件、`channel_uuid`、`content`）。证据：`ddl/call-bot.sql:358-379`（含注释 `role: 1-客户,2-坐席,3-机器人,4-系统`）。对话另落 `t_cdr_dialog_call_bot`(`chat_type` 1人工/0机器人, `content`, `send_time`)(`:537-552`)。**按键序列**落 `t_cdr_task.keypress_sequence`（注释含 `[{"key":"1","time":...}]`）(`:618`)、意向采集 `sales_script_intention_code/name`、错误/超时计数 `sales_script_intention_input_error_count`/`_timeout_count`(`:614-617`)。
- **谁驱动 playback/detect（实读）**：ESL 事件经 `FsEventKafkaListener`→`CompositeEventDispatcher` 按 call-type 路由到 handler。客户腿 call-type = `CALL_CUSTOMER_PROXY`，对应 `backgroud/task/outbound/OutboundTaskEventHandler.kt:36-71`（`callType()=CALL_CUSTOMER_PROXY`，`:71`），它再按任务/话术类型二次分发：
  - **IVR 型**（话术）→ `SalesScriptDispatcher.kt:13-101`（`callType()=OUTBOUND_TASK`，`:19-21`），它持有一组 `SalesScriptCallHandler`，按 `cdr.salesScriptType`（ONE_SENTENCE_NOTIFICATION / INTENTION_COLLECTION）选具体 handler（`getHandler` `:86-101`），并把 onRinging/onAnswer/onPark/onPlaybackEnd/onDTMF/onBackgroundJobResult 全转发给它（`:23-84`）——**这就是「接通后放 TTS / 收 DTMF」的状态机**。
  - **AI_CALL 型** → `backgroud/task/outbound/agent/CallAgentEventHandler.kt`（call-type `HERMES_AGENT_CALL`，官方文档 §CallAgentEventHandler：onAnswer 区分客户/坐席通道并触发 AI 对话）；对话决策走 `service/CallRobotChatService.kt`（originate 机器人腿）+ `service/DifyIntentionAnalysisService.kt`（Dify 意图分析）。⚠️ 这两类已定位到类，但其内部「具体下发哪些 FS app（playback/detect_speech/play_and_get_digits）的字符串」未逐行读，落地按需读 `SalesScriptCallHandler` 各实现与 `CallRobotChatService`。
  - **NUMBER_DETECTION 型** → `backgroud/task/outbound/handler/NumberDetectionHandler.kt`（`OutboundTaskDialer.kt:11` import 实证）。
- **AI_CALL vs IVR（实读对照）**：`OutboundTaskDialer.replayLastEndEvent` 按 `task.taskType` 三分支分别交给 `salesScriptDispatcher`(IVR)/`callAgentEventHandler`(AI_CALL)/`numberDetectionHandler`(NUMBER_DETECTION)（`OutboundTaskDialer.kt:184-194`）——这是「IVR=话术状态机、AI_CALL=Dify 对话、NUMBER_DETECTION=号码探测」三类接管路径的直证。

### 2.5 挂机 / 结果落库（CDR）/ 回调（webhook）
- **结论**：挂机后按业务类型写 CDR；任务型外呼写 `t_cdr_task`（含意向、按键、重拨、对话次数），机器人通话写 `t_cdr_call_bot`，直接自动外呼(autocall)写 `t_cdr_auto_call`/`t_custom_cdr_auto_call`；对话明细写 `t_cdr_dialog_call_bot`、轨迹写 `t_call_traces`。任务完成 / 结果经 `t_call_task.callback_url` 回调；拨打前可经 `confirm_url_before_dial` 回调确认。
- **直证（CDR 模型）**：
  - `t_cdr_task`：`status`(记录状态)、`begin/ring/end/answer_time`、`hangup_code/hangup_cause`、`dial/ring/talk/call_duration`、`dial_attempt/dial_count/next_dial_time`、`intention/is_completed`、`conversation_count`、`ring_type`、`business_id/ticket_id/order_id/user_id`。证据：`ddl/call-bot.sql:559-631`。
  - `t_cdr_call_bot`：`status`、`customer_ring/answer_time` + `agent_ring/answer_time`（**说明机器人通话也可能转/接坐席腿**）、`hangup_type`、`conversation_count`、`call_result`。证据：`:446-489`。
  - `t_cdr_auto_call`（autocall.originate 落点）：`template_code/template_name`、`scenario_code`、`tts_code/tts_text`、`sales_script_code`、`ring_type`、`dial_attempt`、`parent_uuid`。证据：`:386-439`。
  - 客户腿明细 `t_cdr_customer`（`task_code`+`call_uuid`+`uuid` 唯一）。证据：`:496-530`。
  - 任务日报 `t_task_stat_daily`（接通/未接/语音信箱/各时长档计数）。证据：`:754-786`。
- **直证（轨迹/对话落点，实读）**：每轮对话/事件写 `t_call_traces`，角色枚举 `TraceRoleEnum.kt:9-19` = `CUSTOMER(1)/AGENT(2)/ROBOT(3)/SYSTEM(4)`、轨迹事件类型 `CallEventTypeEnum.kt:9-18` = `START_CALL/ANSWER/BRIDGE/HANGUP`；写入接口 `CallTraceService.saveCallEventTrace(callUuid, callType, channelUuid, traceType, traceTime, role, content, agentNumber, robotCode)`（`service/CallTraceService.kt`，DTO 含 `AiCallEventData`/`VoicemailDetectedEventData` 等，`:9-19`）。DDL 对照 `ddl/call-bot.sql:358-379`。对话另落 `t_cdr_dialog_call_bot`(`:537-552`)。**按键序列** `t_cdr_task.keypress_sequence`(`:618`)、意向 `sales_script_intention_code/name`、错误/超时计数(`:614-617`)。
- **直证（回调字段 + topic，实读）**：`t_call_task.callback_url`(`ddl:326`)、`confirm_url_before_dial`(`ddl:332`，呼前确认实读于 `OutboundTaskDialer.kt:480-489` 用 `OkHttpUtils.postObject(confirmUrlBeforeDial, {number,taskCode,businessId,ticketId,...})`）。回调 Kafka topic（`constant/KafkaConstant.kt`）：任务完成 `TASK_END_CALLBACK_TOPIC="call-bot_task_end_callback"`(`:20`)、autocall 回调 `AUTO_CALL_CALLBACK_TOPIC="call-bot_autocall_callback"`(`:14`)、机器人通话结束 `CALL_BOT_END_CALLBACK_TOPIC="call-bot_call-bot-call-end-callback"`(`:61`)、轨迹 `CALL_TRACE_TOPIC="call-bot_call_traces"`(`:86`)。AutoCall 完成回调由 `AutoCallEventHandler.onCallEnd` 发 `auto-call-callback`（官方文档 §AutoCallEventHandler）。⚠️ **回调 webhook 的最终 HTTP 报文 JSON schema** 由 hermes-callback 服务消费上述 topic 后组装，未在本篇逐行读，落地按需读 hermes-callback。mock 侧已具备「回调落库」能力（`mock_callback`）作接收端验证。
- **拉取轨迹/对话（验证用，实读）**：mock 经 `GET /openapi/call-trace/{callUuid}` 取 call-bot 通话轨迹。Hermes Controller：`controller/openapi/CallTraceOpenApiController.kt:16-36`（`@RequestMapping("/openapi/call-trace")`，`GET /{callUuid}` → `callTraceService.getCallTraces(callUuid)` 并按机构时区转换 `:21-36`）。mock 侧直证：`internal/hermesopenapi/api.go:286-288`（`GetCallBotTrace`）。数据源即 `t_call_traces`。

### 2.6 直接自动外呼（autocall，免任务的轻量路径）
- **结论**：除「建任务批量拨」外，call-bot 另有**即时单/批发起** `POST /openapi/autocall/originate`，传 `templateCode` + `numbers` + `ttsTextVariableMap` + `encrypted`，按模板拨出（不建持久任务）。Service 不直接打 FS，而是写 CDR 后发 Kafka 给自动呼叫拨号器异步拨。
- **直证（Controller + DTO，实读）**：`controller/openapi/AutoCallOpenApiController.kt:12-22`（`@RequestMapping("/openapi/autocall")`，`POST /originate` → `autoCallService.originate(req, orgCode)`）；DTO `entity/request/autocall/AutoCallReq.kt:10-33`（`templateCode`(32 位)、`numbers: List<NumberInfo>`(1-100)、`ttsTextVariableMap?`、`encrypted`）。mock 侧：`internal/hermesopenapi/api.go:276-278`、body 组装 `orchestrator.go:71-83`。
- **直证（Service 拨号路径，实读）**：`service/AutoCallService.kt:96-232` `originate`——取模板/场景、号码去重(`:119`)、未加密则 `CryptoUtils.tryTokenizeBatch` 每批 ≤1000 加密(`:122-134`)、`processTtsTextVariables` 替换 `{{变量}}`(`:137`,`:238-248`)、批量插 `AutoCallCdrPO`(status NOT_STARTED,`:159-201`)、**逐条发 Kafka `KafkaConstant.AUTO_CALL_CDR_DIAL_TOPIC="call-bot_auto-call-cdr-dial"`**(`:229-231`)。该 topic 由 `backgroud/task/autocall/AutoCallDialer.kt` 消费拨号、`AutoCallEventHandler.kt`(call-type `AUTO_CALL`)接管放音(官方文档 §AutoCallEventHandler：onAnswer 放音 + 3 分钟时长限制)。落 `t_cdr_auto_call`（`ddl:386-439`）。

---

## 3. 关键接口 / DTO / 事件

| 类型 | 名称 | 用途 | 证据 |
|---|---|---|---|
| OpenAPI | `POST /openapi/task/create-and-import`（call-bot） | 建机器人外呼任务 + 导入号码 | `TaskOpenApiController.kt:109-121`（实读）；mock `api.go:280-283` |
| OpenAPI | `POST /openapi/autocall/originate`（call-bot） | 即时自动外呼（模板） | `AutoCallOpenApiController.kt:17-21`（实读）；mock `api.go:276-278` |
| OpenAPI | `GET /openapi/call-trace/{callUuid}`（call-bot） | 拉机器人通话轨迹/对话 | `CallTraceOpenApiController.kt:21-36`（实读）；mock `api.go:286-288` |
| Kafka | `call-bot_task_call_dial` / `call-bot_auto-call-cdr-dial` | 任务/autocall 待拨消息 → 拨号器消费 originate | `KafkaConstant.kt:25,9`；`OutboundTaskDialer.kt:105`、`AutoCallService.kt:230` |
| ESL→Kafka | topic `esl-event_call-bot`，group `call-bot-fs-event-consumer` | FS ESL 事件入 call-bot | `FsEventKafkaListener.kt:23-24`（实读）；`FsConstant.kt:5` |
| FS 命令 | originate（复用 fs-esl-proxy `OriginateReq`），客户腿无坐席 bridge | 客户腿外呼形态 | `OutboundTaskDialer.kt:60,81`（实读 import/注入）；命令串形态见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §4 |
| 请求字段 | `name/taskType/numbers/robotCode/salesScriptCode/callbackUrl/confirmUrlBeforeDial/dialTimePeriod/concurrent/...` | 建任务 body | `CreateAndImportTaskReq.kt:22-133`（实读） |
| 枚举 | `CallbotTaskTypeEnum` IVR(1)/AI_CALL(2)/NUMBER_DETECTION(3) | 任务类型 | `CallbotTaskTypeEnum.kt:6-22`（实读） |
| 枚举 | `CallTypeEnum` CALL_CUSTOMER_PROXY/AUTO_CALL/HERMES_AGENT_CALL/OUTBOUND_TASK/... + 头 `sip_h_x-call_bot_type` | ESL 路由 call type | `CallTypeEnum.kt:6-32`（实读） |
| 枚举 | `SalesScriptTypeEnum` ONE_SENTENCE_NOTIFICATION(1)/INTENTION_COLLECTION(4) | 话术类型 → 选 handler | `SalesScriptTypeEnum.kt:9-19`（实读） |
| 表 | `t_call_task` | 任务主体 | `ddl/call-bot.sql:304-352`（直证） |
| 表 | `t_call_bot` / `t_call_bot_version` | 机器人 + 版本（asr/tts/flow/prompt/model） | `ddl/call-bot.sql:243-265` / `:272-298`（直证） |
| 表 | `t_sales_script` | 话术（key_guide_text/key_intention_map/DTMF 收集） | `ddl/call-bot.sql:717-748`（直证） |
| 表 | `t_call_traces` | 通话轨迹（role 1客户/2坐席/3机器人/4系统） | `ddl:358-379` + `TraceRoleEnum.kt:9-19`（实读） |
| 表 | `t_cdr_task` / `t_cdr_call_bot` / `t_cdr_auto_call` / `t_cdr_dialog_call_bot` | CDR + 对话 + 按键/意向 | `ddl/call-bot.sql:559/446/386/537`（直证） |
| 调度/拨号 | `CallTaskScheduler`(Quartz)+`NotifyDialJob`→`CallTaskHelper`(运行队列)→`TaskNumberScanner`→`OutboundTaskDialer` | 按时段/并发触发批量拨打 | `CallTaskScheduler.kt:22-218`、`NotifyDialJob.kt:26-143`、`CallTaskHelper.kt:20-247`、`OutboundTaskDialer.kt:77-`（实读） |
| ESL handler | `OutboundTaskEventHandler`(CALL_CUSTOMER_PROXY)→`SalesScriptDispatcher`(IVR)/`CallAgentEventHandler`(AI_CALL)/`NumberDetectionHandler` | 接通后驱动机器人 + 落 CDR | `OutboundTaskEventHandler.kt:36-71`、`SalesScriptDispatcher.kt:13-101`、`OutboundTaskDialer.kt:184-194`（实读） |
| ESL 事件 | `CHANNEL_CREATE/CALLSTATE(RINGING/EARLY)/CHANNEL_ANSWER/CHANNEL_PARK/DTMF/PLAYBACK_STOP/CHANNEL_BRIDGE/CHANNEL_HANGUP_COMPLETE/BACKGROUND_JOB` | 驱动机器人 + 落 CDR | `FsEventKafkaListener.kt:37-75`（实读）；`CompositeEventDispatcher.kt:16-113` 按 call type 路由 |

---

## 4. 对 hermes-mock 的启示

- **可复用 / 已具备**：
  - **同一种入向 INVITE**：call-bot 机器人外呼打到 mock 时，与坐席外呼/群呼**完全同构**——`INVITE sofia/external/{calleePrefix}{被叫}@mock`，主叫=线路号(`caller`)、被叫=`calleePrefix+号码`。mock 现有「被叫 UAS 按行为档应答」能力（ANSWER/REJECT/BUSY/NO_ANSWER/UNAVAILABLE + 接通率 + 故障注入）直接适用，**无需新增 SIP 能力**。证据：[SCOPE.md](../SCOPE.md) §三、[../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §4 共同点。
  - **配合机器人接管的两个关键点**：(a) mock 必须**真正 200 应答并建立 RTP**，机器人才会开始放 TTS（park 后 call-bot 在 ANSWER 事件后才下发 playback）；(b) mock 行为档的 **DTMF 序列 / IVR 脚本** 正好用来**模拟客户对机器人按键应答**（IVR 型任务收 `play_and_get_digits`）。mock 已有「放音 + DTMF 序列 + IVR 脚本」档位。证据：[SCOPE.md](../SCOPE.md) §三（行为档含 DTMF 序列 + IVR 脚本）。
  - **触发链已具备**：mock orchestrator 已能经 OpenAPI 建 call-bot 任务/autocall（`RunCallBot`/`RunAutoCall`，`orchestrator.go:48-83`），并能拉轨迹核验（`GetCallBotTrace`）。
  - **回调接收**：mock 已有 `mock_callback` 落库，可作 `callback_url` 的接收端断言「任务完成/识别结果」回调。证据：[STATUS.md](../STATUS.md)「呼叫记录/回调落库」。
- **做不到 / 需外部依赖 / 需桩**：
  - **ASR/TTS/大模型是 Hermes 侧真实依赖**：机器人接管依赖 Azure Speech + Dify（`build.gradle.kts:20,24`），本地起 call-bot 需提供或桩化这些外部服务（参考 [hermes-stack/README.md](../../deploy/hermes-stack/README.md)「服务外部依赖」与 crypto-stub 思路）。mock 自身**不模拟机器人**，只演被叫客户。
  - **mock 不参与选线/调度/ESL 事件**：选线、Quartz 调度、机器人会话驱动全在 call-bot 后端；mock 看不到 ESL 事件，只看到一条 INVITE。
  - **要验证「机器人识别了客户按键/语音」**：mock 回 DTMF/放固定语音 → 需经 `GET /openapi/call-trace/{callUuid}` 或 `t_call_traces`/`t_cdr_dialog_call_bot` 反查机器人是否正确识别。mock 侧只能控「客户说/按什么」，识别正确性由 Hermes 落库体现。
  - **号码导入与加密**：autocall 传 `encrypted:false`（mock `orchestrator.go:78`）；真实链路客户号常被 crypto tokenize（见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §2.5、hermes-stack crypto-stub）。本地需 crypto-stub 才能跑通号码加解密。

---

## 5. 待补 / 推断项

本轮已逐文件实读 `hermes-call-bot` 并补全绝大多数行号；仍剩以下「已定位类、未逐行读」或纯推断项：
- ⚠️ **话术 Handler 内具体 FS app 串**：`SalesScriptCallHandler` 的两个实现（ONE_SENTENCE_NOTIFICATION / INTENTION_COLLECTION）内部下发的 playback / detect / play_and_get_digits 具体命令字符串未逐行读。落地读 `SalesScriptDispatcher` 注入的 `handlers` 各实现类（按 `salesScriptType()` 区分）。
- ⚠️ **AI_CALL（Dify）对话细节**：`backgroud/task/outbound/agent/CallAgentEventHandler.kt`、`service/CallRobotChatService.kt`(originate 机器人腿)、`service/DifyIntentionAnalysisService.kt`(意图分析) 已定位但未逐行读。
- ⚠️ **TaskNumberScanner 扫号主循环逐行细节**：类与并发 key 方法已实证（被 `OutboundTaskDialer.kt:389` 引用），主循环行号未读；call-bot 调度无 call-center 那种「比例/PID 预测式」，而是「时段+任务/线路/机器人并发许可」门控（见 §2.3）。
- ⚠️ **回调 webhook 最终 HTTP 报文 schema**：call-bot 只发 Kafka（`TASK_END_CALLBACK_TOPIC` 等，见 §2.5），实际向 `callback_url` 发 HTTP 的组装在 hermes-callback，未读。
- ⚠️ `disruptor` 在 call-bot 中的确切用途（依赖存在，使用点未读，推断为对话/事件流水线）。
- ⚠️ 时序图 (7)(8)(9) 的 FS app 序列以「Handler 分发链 + DDL 字段 + 依赖库」为据，最终命令串仍需读 Handler 实现或 FS 抓包确认。

## 附：关键证据索引（路径:行号）

**Hermes 源码实读（本轮，路径相对 `/Users/Xuxx/IdeaProjects/hermes`）**
- `hermes-call-bot/.../controller/openapi/TaskOpenApiController.kt:29-34,103-121`：`/openapi/task` 根、`create-and-import`、`number/import`。
- `hermes-call-bot/.../controller/openapi/AutoCallOpenApiController.kt:12-22`：`/openapi/autocall/originate`。
- `hermes-call-bot/.../controller/openapi/CallTraceOpenApiController.kt:16-36`：`/openapi/call-trace/{callUuid}`。
- `hermes-call-bot/.../entity/request/calltask/CreateAndImportTaskReq.kt:22-133`：建任务 DTO 全字段。
- `hermes-call-bot/.../entity/request/autocall/AutoCallReq.kt:10-33`：autocall DTO。
- `hermes-call-bot/.../constant/enums/CallbotTaskTypeEnum.kt:6-22`（IVR1/AI_CALL2/NUMBER_DETECTION3）、`TaskNumberSourceEnum.kt:6-14`、`SalesScriptTypeEnum.kt:9-19`、`CallTypeEnum.kt:6-32`（头 `sip_h_x-call_bot_type`）。
- `hermes-call-bot/.../constant/enums/call/trace/TraceRoleEnum.kt:9-19`（1客户/2坐席/3机器人/4系统）、`CallEventTypeEnum.kt:9-18`。
- `hermes-call-bot/.../constant/FsConstant.kt:5`（`CALL_BOT_SERVICE_NAME="call-bot"`）、`KafkaConstant.kt`（topic 名 `:9,14,20,25,30,61,86`）。
- `hermes-call-bot/.../listener/fs/FsEventKafkaListener.kt:16-86`（topic `esl-event_call-bot`、事件→方法映射）、`CallEventHandler.kt:12-160`（接口 + CDR 时间换算）、`listener/fs/business/CompositeEventDispatcher.kt:16-113`（按 call type 路由）。
- `hermes-call-bot/.../backgroud/task/outbound/CallTaskScheduler.kt:22-218`、`NotifyDialJob.kt:26-143`、`NotifyCompletedJob.kt:14-32`、`CallTaskHelper.kt:20-247`（Quartz + 运行队列 zset + `getRandomRunningTask`）。
- `hermes-call-bot/.../backgroud/task/outbound/OutboundTaskDialer.kt:60,77-105,184-194,237-489`（消费 `TASK_CALL_DIAL_TOPIC`、选线 `LinePhoneInfoService.getAvailablePhones/getCaller`、并发许可 `DistributeConcurrentService`、解密、`confirmUrlBeforeDial`、按 taskType 三分支）。
- `hermes-call-bot/.../backgroud/task/outbound/OutboundTaskEventHandler.kt:36-71`（CALL_CUSTOMER_PROXY）、`SalesScriptDispatcher.kt:13-101`（OUTBOUND_TASK，按 salesScriptType 选 handler）。
- `hermes-call-bot/.../service/AutoCallService.kt:96-232`（autocall originate → tokenize → 发 Kafka）、`CallTraceService.kt:1-40`（`saveCallEventTrace`/轨迹）。

**mock 侧直证**
- `internal/hermesopenapi/api.go:276-288`（`AutoCall`/`CreateCallBotTask`/`GetCallBotTrace`）、`client.go:38`（`prodCallBot="call-bot"`）、`orchestrator.go:40-83,197-204`（body 字段）。

**DDL 直证（`deploy/hermes-stack/ddl/call-bot.sql`，与 `hermes/docs/architecture/ddl/call-bot.sql` 同源）**
- `:304-352` `t_call_task`；`:243-265`/`:272-298` `t_call_bot`/`t_call_bot_version`；`:717-748` `t_sales_script`；`:358-379` `t_call_traces`；`:559-631`/`:446-489`/`:386-439`/`:537-552` 各 CDR/对话表；`:5-203` Quartz `qrtz_*`。

**官方文档（可直接链接）**
- [`hermes/docs/modules/call-bot.md`](../../../hermes/docs/modules/call-bot.md)：源码结构、Service/ESL handler 职责、枚举、Kafka topic、DDL 总览（本篇行号与之交叉核对一致）。
- [`hermes/docs/INDEX.md`](../../../hermes/docs/INDEX.md)：Hermes 文档中心总入口。
- `build.gradle.kts:13,20,24,25`：quartz / azure.speech(ASR+TTS) / dify(LLM) / libphonenumber；`settings.gradle.kts`：模块 `hermes-call-bot`。
</content>
</invoke>
