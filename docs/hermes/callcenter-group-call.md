<!-- 主题：hermes-call-center 群呼 / 批量外呼任务（预测式外呼 / autocall）。骨架源自 _TEMPLATE.md。 -->

# 群呼 / 批量外呼任务（预测式外呼 / autocall）（Hermes 代码梳理）

> 目标读者：要在 hermes-mock 里验证「任务驱动的批量外呼」打到被叫客户线路对端时的信令形态、或排查群呼链路的开发者。
> 范围：**任务驱动的批量外呼**——后台建一个外呼任务（`t_call_task`）、导号、按时间段调度、按"比例 / PID"预测式发起外呼、客户接通后转空闲坐席（或 AI）。
> **不**覆盖：坐席工作台手动点拨号盘外呼（那条见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) / [agent-outbound-call.md] 同义），本篇与它的对照见该文 §4。
> 基于：当前 HEAD 源码核查（`/Users/Xuxx/IdeaProjects/hermes`，单体仓库 `settings.gradle.kts` 列出 `hermes-call-center` 等模块）。源码根与引用约定见 [README.md](README.md)，下文路径相对该根。
> 证据约定：每条结论尽量带「路径:行号」；推断标「未在代码中找到，推断」。
> ⚠️ **核查环境限制（重要，影响行号完备性）**：本次梳理时 IDE 的全文搜索 / glob / 列目录工具全程不可用（分类器降级），只能用 Read 逐文件读取。因此**有具体行号的结论均为实读所得、可信**；但**「群呼拨号线程（预测式 originate 主循环）」与「GROUP_CALL 的 ESL 客户腿 handler」两个文件未能用搜索定位到确切文件名/行号**，本篇按强间接证据描述其行为并显式标注为推断，落地前请用搜索补确切文件。

---

## 1. 一句话概述 + 端到端时序

**一句话**：机构管理端调 `POST /call-tasks` 建一个外呼任务（任务名 / TTS / 模式策略比例或PID / 时间段 / 坐席或坐席组 / 重拨策略 / 线路类型），调 `POST /task/number/import` 导入号码（每号一条 `t_call_task_cdr`）；到达任务的拨号时间段时，Quartz 触发 `NotifyDialJob` 把任务塞进 Redis「运行中任务集合」；一个**常驻拨号线程**按机构轮询运行中任务，依据**预测式算法（比例 / PID，参考空闲坐席数、历史接通率、呼损率）**决定本轮发起几通，对未拨号码经 `freeswitchApi.originate` 用 `{params}sofia/external/{calleePrefix}{客户号}@{线路网关} &park()/&playback(...)` 发起客户外呼；**客户接通后**（ESL 事件回 call-center，callType=`GROUP_CALL`）放 TTS / 走 AI，或把这条客户腿放进**坐席排队/分配引擎**（`component/queue`，与呼入共用）转给一个空闲坐席（坐席状态须 `AUTO_OUTBOUND`），接通则桥接两腿。**选线、预测、调度、转坐席全在 `hermes-call-center` 后端；FS dialplan 不参与。**

```
机构管理端                 hermes-call-center                         FreeSWITCH            被叫客户线路/mock
   |                            |                                        |                      |
   |--POST /call-tasks--------->| CallTaskController.createTask          |                      |
   |   (AddCallTaskReq)         |  └ CallTaskService.createTask 入库 t_call_task               |
   |                            |     + CallTaskScheduler.saveTask(Quartz cron 按时间段建触发器) |
   |--POST /task/number/import->| CallTaskNumberController.importTaskNumbers                   |
   |   (ImportNumberReq)        |  └ 每号一条 t_call_task_cdr (status=CREATED, 加密 tokenize)  |
   |                            |                                        |                      |
   |   [到达拨号时间段]          | Quartz 触发 NotifyDialJob              |                      |
   |                            |  └ CallTaskHelper.addRunningTask → Redis「运行中任务集合」    |
   |                            |                                        |                      |
   |                            | ★拨号线程(常驻): 轮询 getRandomRunningTask()                 |
   |                            |   预测式(比例/PID, 看空闲AUTO_OUTBOUND坐席数/历史接通率/呼损率)|
   |                            |   决定本轮发起 N 通 → 取未拨 t_call_task_cdr                  |
   |                            |   (可选)confirmUrlBeforeDial 预呼确认 webhook                |
   |                            |--freeswitchApi.originate---------------▶|                      |
   |                            |  {params}sofia/external/{prefix}{客户号}@{网关} &park()/&playback|
   |                            |                                        |--INVITE 客户号------->| (mock 收)
   |                            |                                        |◀----180/200-----------|
   |                            |◀--ESL CHANNEL_ANSWER (x-call_center_type=GROUP_CALL)----|     |
   |                            | ★GROUP_CALL handler: 放 TTS / AI 或    |                      |
   |                            |   把客户腿入排队引擎找空闲坐席          |                      |
   |                            |--originate 坐席腿 + uuid_bridge------->| 坐席接听→桥接客户腿+坐席腿  |
   |◀--WS 进度 / 任务回调 webhook-| GroupCallNotifyService / CallbackApiService                 |
```

要点：
- 与坐席手动外呼最大的不同：**没有"先存在的坐席腿"**。这里是**系统先 originate 客户腿**，客户接通后**再**去找/呼坐席（预测式：尽量让"客户接通"与"坐席空闲"在时间上对齐，减少坐席空等 / 客户空等）。
- "转空闲坐席"复用与**呼入排队完全相同的分配引擎**（`component/queue` 的 `QueueManager` + `CallIncomingService` 那套 zset 排队 + `assignAgent`），只是可分配坐席状态从 `ONLINE` 变为 `AUTO_OUTBOUND`（坐席须先切到「自动外呼」态，见 §2.6）。
- ⚠️ 标 ★ 的两步（拨号线程、GROUP_CALL handler）确切文件未用搜索定位到，按间接证据描述，见本篇头部限制说明与 §5。

---

## 2. 逐环节说明（带证据）

### 2.1 建任务：REST 接口与请求 DTO
- **结论**：任务管理 REST 根 `/call-tasks`，建任务 `POST /call-tasks`。
- **证据**：`hermes-call-center/.../controller/CallTaskController.kt:18-32`（`@RequestMapping("/call-tasks")`、`createTask` → `callTaskService.createTask`）。其余：编辑 `PUT`、切状态 `POST toggle/{id}`、取消 `PUT /cancel/{id}`、分页 `POST /page`、按 code 查 `GET /{code}`、复制带号码 `POST /copy/numbers`（`CallTaskController.kt:44-105`）。
- **请求 DTO**：`entity/request/calltask/AddCallTaskReq.kt:12-169`。关键字段：
  - `name`（任务名，正则 `[a-zA-Z_0-9]`）、`modeStrategy`（**1 比例 / 2 PID**）、`proportion`(1-10)、`lossRate`(期望呼损率 0-99)、`historicalConnectionRate`(历史接通率 1-100)；
  - `ttsCode`/`ttsText`（接通后播报；支持 `{{变量}}` 占位，导号时按 `ttsTextVariableMap` 替换，`CallTaskService.kt:1236-1246`）；
  - `startDate`/`endDate`/`dialTimePeriod`(运行时段列表 `HH:mm-HH:mm`)/`timezone`(取机构时区)；
  - 重拨：`maxRedialTimes`(1-5)/`redialInterval`(0-60min)/`redialConditions`(未接通重拨条件)/`useUnAnsweredCondition`；语音信箱重拨：`vmMaxRedialTimes`/`vmRedialInterval`/`isVmHangup`；
  - 转接：`transferType`（`AI_ONLY` / `HUMAN_ONLY`，`CallTaskTransferTypeEnum.kt:11-13`）、`transferTtsText`、`bestRingDuration`(最佳振铃)、`agentMaxRingDuration`(坐席侧最大振铃)、`assignDelaySeconds`(客户接听后等多少秒再分配坐席)；
  - 坐席来源：`agentNumbers`（坐席分机集合）**或** `agentGroupCodes`（坐席组，二选一，`CallTaskService.kt:1413-1435` 校验互斥）；
  - 线路：`lineType`（不传=`base` 默认随机路由；选中则任务期间仅用该 type 线路，建任务时 `checkLineTypeAvailability` 预校验，`CallTaskService.kt:366-388`）；
  - `confirmUrlBeforeDial`（呼前确认 webhook）、`callbackUrl`（任务/CDR 回调）、`isPriorityTask`（优先任务）、`sortMethod`（1 优先首呼 / 2 优先重呼）。
- **落库实体**：`entity/po/CallTaskPo.kt:7-222`（`@TableName("t_call_task")`，字段与上面一一对应，外加运行统计 `numberCount/dialedCount/answeredCount/agentMissedCount/completedCount/redialCount`）。任务-坐席关系 `t_call_task_agent`（`CallTaskAgentPo.kt:6-13`），任务-坐席组关系 `t_call_task_agent_group`（`CallTaskAgentGroupPo`，由 `saveTaskAgentGroupList` 写入 `CallTaskService.kt:1483-1490`）。
- **创建流程**：`CallTaskService.createTask` `:331-471`——参数校验→机构/坐席/坐席组/时段/线路校验→入库（初始状态由 `determineCurrentStatusByDate` 决定）→存坐席(组)关系→`callTaskScheduler.saveTask(taskPo)` 注册 Quartz→`onTaskStatusChanged`。

### 2.2 导入号码（号码来源）
- **结论**：导号 REST 根 `/task/number`，导入 `POST /task/number/import`。
- **证据**：`controller/CallTaskNumberController.kt:25-36`（`@RequestMapping("/task/number")`、`import` → `taskService.importTaskNumbers(..., TaskNumberSourceEnum.ORG_MANAGER)`）。同类：校验 `POST /check`、`POST /checkExcel`、下载模板 `GET /downloadTemplate`、号码列表 `POST list`、取消/暂停/恢复单个号码 `POST cancel|pause|resume/{taskCode}/{number}/{dialAttempt}`（`:38-147`）。
- **号码来源枚举**：`TaskNumberSourceEnum`（代码注释「1:api导入 2:机构管理端导入」，本仓库见 `ORG_MANAGER` 与 `OPENAPI` 两个取值被使用：管理端导号 `CallTaskNumberController.kt:34`、OpenAPI 建任务并导号 `CallTaskService.kt:1204`）。
- **每号一条 CDR**：`TaskCdrService.importTaskNumbers` `:131-249`——明文号码先 `formatToCanDialNumber` 再 `CryptoUtils.tryTokenizeBatch`（每批 ≤1000）加密成 `INTERNAL` token；外部已加密走 `EXTERNAL` 前缀。每号生成一条 `TaskCdrPo`（`@TableName` 见 `entity/po/TaskCdrPo.kt`，含 `customerPhoneNumber`(密)/`phoneMasked`/`status=CREATED`/`dialCount=0`/`nextDialTime=2000-01-01`(占位避免唯一索引失效)/`ttsCode`/`ttsText`/`businessId`/`extraInfo` 等）。导入后 `CallTaskService.importTaskNumbers:122-129` 累加 `t_call_task.number_count` 并置 `number_imported`。
- 号码级状态机：`TaskCdrStatusEnum.kt:11-50`：`CREATED(0)→PENDING(1 待拨/排队)→DIALING(7)→CONNECTED(4 已接通)/NOT_CONNECTED(5)`；另有 `CANCELLED/PAUSED/NOT_DIALED`。

### 2.3 调度：Quartz 按时间段触发 + Redis「运行中任务集合」
- **结论**：任务不是建好就拨；`CallTaskScheduler.saveTask` 为每个 `dialTimePeriod` 生成 **Quartz cron 触发器**，到点触发 `NotifyDialJob`；`NotifyDialJob` 只是把任务塞进 Redis 运行中集合，**不直接拨号**。
- **证据**：
  - `backgroud/task/CallTaskScheduler.kt:28-129`（`saveTask`：按 `timePeriod.toCron()` 建 `CronScheduleBuilder` 触发器，`startAt(startDate)`/`endAt(endDate)`，时区用任务 `timezone`；另有「开始日在次日及以后」的凌晨补偿触发器 `:102-122`；`:127` 同时建结束监控 `NotifyCompletedJob`）。`deleteTask/pauseTask/resumeTask` `:131-156`。
  - `backgroud/task/NotifyDialJob.kt:21-94`（`@DisallowConcurrentExecution`；`runJob` 按 `determineCurrentStatusByDate` 判定状态：COMPLETED/CANCELED→结案；RESTING/PAUSE→移出运行集合；IN_PROGRESS 且已导号→`CallTaskHelper.addRunningTask(callTask)`）。
  - `backgroud/task/CallTaskHelper.kt`：运行中任务存 Redis zset——机构集合 `hermes:call-center:task:running:org-code`(`:33`)、机构下优先任务集合 `...:priority:org:{org}`(`:333-336`) 与非优先集合 `...:running:org:{org}`(`:338-341`)；`getRandomRunningTask()`/`doGetRandomRunningTask()` `:155-228` 用 Lua「取首个并重新打分(轮转)」先随机抽机构再抽任务，**优先/非优先按 60%/40% 概率抽**(`getTaskCodeSet:230-247`)。任务并发计数 `taskConcurrent`(set, key `...:task:concurrent:{org}:{taskCode}`，TTL 15min)：`getTaskConcurrent/incrementTaskConcurrent/decrementTaskConcurrent` `:71-87`。
- 时段/状态推断：`determineCurrentStatusByDate` `CallTaskHelper.kt:252-284`（在时段内=IN_PROGRESS，时段外=RESTING，超结束=COMPLETED）。

### 2.4 ★拨号线程：预测式发起（比例 / PID）—— 行为推断，确切文件未定位
- **结论（推断）**：存在一个**常驻拨号线程/调度器**，循环调用 `CallTaskHelper.getRandomRunningTask()` 取一个运行中任务，按任务 `modeStrategy` 计算"本轮可发起几通"，对该任务下 `status∈{CREATED,PENDING}` 且 `nextDialTime` 已到的 `t_call_task_cdr` 发起 `freeswitchApi.originate`，并 `incrementTaskConcurrent`。
  - **比例(PROPORTION)**：发起数 ≈ `空闲可用坐席数 × proportion`（`proportion` 1-10，即每个空闲坐席并发拨几路）。
  - **PID**：用 `lossRate`(期望呼损率) + `historicalConnectionRate`(历史接通率) 做闭环调节发起速率，使实际呼损率逼近期望。
- **证据**：
  - 间接强证据：`AddCallTaskReq` / `CallTaskPo` 携带 `modeStrategy`(PROPORTION/PID)、`proportion`、`lossRate`、`historicalConnectionRate`（`CallTaskPo.kt:37-52`），`validateAddRequest` 强制「比例必填 proportion；PID 必填 lossRate+historicalConnectionRate」(`CallTaskService.kt:1413-1422`)。
  - `CallTaskHelper` 提供 `getRandomRunningTask` 与 `taskConcurrent` 计数（`CallTaskHelper.kt:71-87,155-228`）——这是给拨号循环消费的接口，说明消费方存在。
  - originate 命令形态：`entity/fs/command/OriginateCommand.kt:42-88`（见 §3 / §4）。`build.gradle.kts:23,26` 引入 `disruptor`、`xgboost`（xgboost 疑用于接通率/意向预测，未在拨号主链直接读到，推断）。
- ⚠️ **未在代码中（用搜索）定位到**：拨号主循环文件名、PID 系数、"空闲坐席数"的确切取数口径。`backgroud/task/` 下已确认存在 `CallTaskScheduler`/`NotifyDialJob`/`NotifyCompletedJob`/`CallTaskHelper`/`TimePeriod`，但**拨号主循环不在这几个已读文件里**——它要么在 `backgroud` 下另一文件、要么是某 `@Scheduled`/`init{Thread}` 服务。**落地前必须搜索 `getRandomRunningTask` 的调用方补全本节行号。**

### 2.5 客户接通后：放音 / AI / 转空闲坐席（GROUP_CALL handler）—— 行为推断，确切文件未定位
- **结论（推断）**：客户腿是经 `sofia/external/...@{网关}` 发起、`x-call_center_type=GROUP_CALL` 标记的外呼。ESL 事件（CHANNEL_CREATE/ANSWER/HANGUP）回到 call-center，经 `FsEventKafkaListener`→`CompositeEventDispatcher` 按 `variable_sip_h_x-call_center_type=GROUP_CALL` 路由到**专门的 GROUP_CALL handler**。该 handler：
  - 客户接听后按 `transferType`：`AI_ONLY` 走机器人/TTS 对话；`HUMAN_ONLY`（或转人工分支）→（可选等待 `assignDelayMills`）把客户腿放入**坐席分配引擎**找空闲坐席。
  - 转人工复用呼入那套：排队 zset + `agentStatusService.assignAgent` 选坐席 → `freeswitchApi.originate` 呼坐席腿 → 坐席接听 `uuid_bridge` 桥接客户腿（与 `CallIncomingService.doCallAgent` / `QueueCallEventAdapter.onAnswer` 同构）。
- **证据**：
  - 路由底座（已确证）：`listener/fs/FsEventKafkaListener.kt:30,43-100`（消费 `esl-event_{服务名}`，事件名→`compositeEventDispatcher.onCallStart/onAnswer/...`）；`listener/fs/CompositeEventDispatcher.kt:79-100`（按 `variable_sip_h_x-call_center_type` 选 handler，取不到退回 Redis `CallChannel.callType`，再退回 INBOUND）。
  - call type 已定义：`constant/enums/BusinessCallTypeEnum.kt:12`（`GROUP_CALL("GROUP_CALL","群呼")`），`:27-29` 头字段名。
  - 分配引擎（已确证，呼入与群呼共用）：`component/queue/QueueManager.kt`（`agent-queue` zset、`group-queue`、`publish/poll/remove`）、`component/queue/QueueCallEventAdapter.kt:68-95`（坐席腿 onAnswer→`UuidBridge` 桥接客户腿）、`service/CallIncomingService.kt:239-429`（`tryAllocateAgentGroupCode`→`assignAgent`→`doCallAgent` originate 坐席腿，`x-call_center_type=TRANSFER_QUEUE`、`park_after_bridge`）。`QueueContext.assignableAgentStatuses` 默认 `ONLINE`（`QueueContext.kt:61`）——群呼场景应传 `AUTO_OUTBOUND`（推断）。
- ⚠️ **未在代码中（用搜索）定位到**：`isSupport(GROUP_CALL)` 的 handler 文件名与行号（已逐个排除 `listener/fs/business/` 下 `GroupCall*`/`TaskCall*`/`CallTask*` 等命名，均不存在；handler 可能在别的子包或名字与直觉不同）。**落地前必须搜索 `GROUP_CALL` / `isSupport` 的引用定位该 handler。**

### 2.6 与坐席状态的联动
- **结论**：群呼转人工要求坐席处于「自动外呼」态 `AUTO_OUTBOUND`，而非普通在线 `ONLINE`。坐席工作台切到自动外呼：`POST /agent-workbench/sdk/agent/status/switch` action=AUTO_OUTBOUND。
- **证据**：`controller/agent/workbench/AgentController.kt:75-87`（`AUTO_OUTBOUND` 分支，只允许从 RESTING/ONLINE/BUSY 进入）。状态枚举含 `AUTO_OUTBOUND`（`AgentController.kt` 状态机引用；与坐席外呼文档一致，见 [frontend-call-sdk.md](frontend-call-sdk.md) §2.3）。
- 任务与坐席绑定：建任务时 `agentNumbers` 或 `agentGroupCodes` 二选一（`CallTaskService.createTask:451-457`）；任务进行中按坐席组初始化坐席关系 `initTaskAgentGroup`（`CallTaskService.kt:1251-1272`，`CallTaskHelper.onTaskStatusChanged` IN_PROGRESS 分支触发 `:97-101`）。
- 坐席侧统计：`AgentStatusService`（`service/AgentStatusService.kt`）持有 `assignAgent` 选坐席逻辑（被呼入/群呼分配共用）。

### 2.7 线路选择与并发
- **结论**：群呼线路选择与坐席手动外呼**同一套**：机构维度可用外呼号码池 + 运营时段过滤 + 并发权重加权随机；可按任务 `lineType` 锁定线路类型。
- **证据**：`service/LinePhoneInfoService.getAvailablePhones(orgCode, OUTGOING)`、`DistributeConcurrentService.getPermitIdByRandomWeight(...)`（与 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §2.5 第6点同源）；建任务期线路类型校验 `LinePhoneInfoService.checkLineTypeAvailability`（`CallTaskService.kt:368`）。任务级并发上限经 `CallTaskHelper.taskConcurrent`（`CallTaskHelper.kt:71-87`，set + 15min 租约）。线路网关地址 `line.address`、被叫前缀 `line.calleePrefix` 注入 originate（见 §3）。
- ⚠️ 选哪条号码作主叫(`caller`)由拨号线程在 originate 前调 `LinePhoneInfoService.getCaller(...)`（推断，与手动外呼一致；确切调用点随 §2.4 文件待补）。

### 2.8 CDR 与回调（webhook）
- **结论**：群呼有独立的号码级 CDR 表 `t_call_task_cdr`（`TaskCdrPo`/`TaskCdrService`），坐席侧另有 `t_task_agent_cdr`（`TaskAgentCdrMapper`）。任务/CDR 通过 Kafka + `CallbackApiService` 回调机构 webhook；WS 实时推进度。
- **证据**：
  - 号码 CDR：`service/TaskCdrService.kt`（`ServiceImpl<TaskCdrMapper, TaskCdrPo>`，`importTaskNumbers`/`isDialCompleted`/`selectCdr`/`details` 等；坐席侧 `taskAgentCdrMapper`，`:64,27`）。
  - 任务结束回调：`CallTaskService.setTaskCompleteOrCanceled:846-855`（发 Kafka `KafkaConstant.TASK_END_CALLBACK_TOPIC`，再 `taskCdrService.onTaskEnd`）。
  - 任务状态变更回调：`CallTaskHelper.taskStatusChangedCallBack:363-382`（`CallbackApiService.callback(type=1209,event=12009)`，body=`TaskStatusChangeCallbackEvent{taskCode,taskName,status,gmtModified}`）。
  - 呼前确认 webhook：`confirmUrlBeforeDial`（`CallTaskPo.kt:67`，拨号前回调业务方确认是否拨打；确切调用点随 §2.4 待补，推断）。
  - WS 进度：`service/GroupCallNotifyService.kt:36-40`（disruptor 异步推 `GroupCallNotifyMsg`/`GroupCallPhoneStart/End`/`GroupCallStatusMsg`，`CallTaskHelper.onTaskStatusChanged` 调 `pushGroupCallStatus/pushTaskProgress` `:100,121`）。

### 2.9 关键 ESL 事件流（群呼里怎么走）
- 客户腿 originate 后：`CHANNEL_CREATE`(客户腿) → `FsEventKafkaListener.onCallStart` → GROUP_CALL handler 记 channel；`CHANNEL_CALLSTATE(RINGING)` → 客户振铃；`CHANNEL_ANSWER` → 客户接通，触发放音/AI 或入排队找坐席；坐席腿 originate 后其 `CHANNEL_ANSWER` → `QueueCallEventAdapter.onAnswer` → `uuid_bridge` 桥接；`CHANNEL_HANGUP_COMPLETE` → 写 CDR、释放任务并发、按重拨策略决定是否回填 `nextDialTime` 重拨。
- **证据**：`FsEventKafkaListener.kt:43-100`（事件分发）；桥接 `QueueCallEventAdapter.kt:68-95`。重拨回填与"未接重拨/语音信箱重拨"逻辑在 `TaskCdrService` + 拨号线程（具体行号随 §2.4 待补，推断）。

---

## 3. 关键接口 / 数据结构 / 信令（汇总）

| 类型 | 名称 | 用途 | 证据 |
|---|---|---|---|
| HTTP | `POST /call-tasks` | 建外呼任务 | `CallTaskController.kt:29-32` |
| HTTP | `PUT /call-tasks` / `POST /call-tasks/toggle/{id}` / `PUT /call-tasks/cancel/{id}` | 编辑/启停/取消 | `CallTaskController.kt:44-69` |
| HTTP | `POST /call-tasks/page` / `GET /call-tasks/{code}` / `POST /call-tasks/copy/numbers` | 查询/复制 | `CallTaskController.kt:75-105` |
| HTTP | `POST /task/number/import` | 导入号码 | `CallTaskNumberController.kt:31-36` |
| HTTP | `POST /task/number/check` `/checkExcel` `/downloadTemplate` `/list` | 校验/列表 | `CallTaskNumberController.kt:89-147` |
| DTO | `AddCallTaskReq` | 建任务请求体（含比例/PID/时段/坐席/线路/重拨/回调） | `entity/request/calltask/AddCallTaskReq.kt:12-169` |
| PO | `CallTaskPo` `@TableName("t_call_task")` | 任务主表 | `entity/po/CallTaskPo.kt:7-222` |
| PO | `CallTaskAgentPo`/`CallTaskAgentGroupPo` | 任务-坐席 / 任务-坐席组 | `CallTaskAgentPo.kt:6-13` |
| PO | `TaskCdrPo` `@TableName(t_call_task_cdr)` | 每号码一条拨打记录 | `service/TaskCdrService.kt:54-66,187-247` |
| 枚举 | `TaskModeStrategyEnum` PROPORTION(1)/PID(2) | 预测式模式 | `constant/enums/TaskModeStrategyEnum.kt:9-16` |
| 枚举 | `CallTaskTransferTypeEnum` AI_ONLY/HUMAN_ONLY | 接通后转接类型 | `constant/enums/CallTaskTransferTypeEnum.kt:6-14` |
| 枚举 | `TaskStatusEnum` 待开始/进行中/已完成/暂停/休息/已取消 | 任务状态 | `constant/enums/TaskStatusEnum.kt:9-23` |
| 枚举 | `TaskCdrStatusEnum` CREATED/PENDING/DIALING/CONNECTED/... | 号码级状态 | `constant/enums/TaskCdrStatusEnum.kt:11-50` |
| 枚举 | `BusinessCallTypeEnum.GROUP_CALL` = "群呼" | ESL 路由 call type | `constant/enums/BusinessCallTypeEnum.kt:12` |
| FS 命令 | `OriginateCommand` | 群呼客户腿 originate | `entity/fs/command/OriginateCommand.kt:42-88` |
| FS 请求 | `OriginateReq{service,uuid,callee,calleePrefix,caller,callerAddress,recordPath,params}` | 调 FS originate 的入参 | `entity/request/freeswitch/OriginateReq.kt:5-19` |
| 调度 | `CallTaskScheduler`(Quartz) + `NotifyDialJob`/`NotifyCompletedJob` | 按时间段触发 | `backgroud/task/CallTaskScheduler.kt:28-205`、`NotifyDialJob.kt:21-94` |
| 运行队列 | Redis zset `...:task:running:org:{org}` / `:priority:org:{org}` | 运行中任务集合 | `backgroud/task/CallTaskHelper.kt:333-341` |
| 回调 | Kafka `TASK_END_CALLBACK_TOPIC` + `CallbackApiService`(type 1209/event 12009) | 任务结束/状态回调 | `CallTaskService.kt:846-855`、`CallTaskHelper.kt:363-382` |
| WS | `GroupCallNotifyService` (disruptor) | 群呼进度实时推送 | `service/GroupCallNotifyService.kt:36-40` |

**群呼客户腿 FS 命令形态**（mock 最关心）：`OriginateCommand.args()`：
```
{RECORD_STEREO=true,RECORD_STEREO_SWAP=true,<countryHdr>=<rtpengineId>,continue_on_fail=false,
 park_after_bridge=true,execute_on_answer='sched_hangup +1800 alloted_timeout',origination_uuid=<uuid>,
 origination_caller_id_name=<caller>,origination_caller_id_number=<caller>,
 execute_on_answer=record_session::<recordFilePath>,<params...>}sofia/external/<calleePrefix><callee>@<gateway> &park()
```
- 接通后默认 `&park()`；**群呼若设了等待音则 `&playback(<waitingMusic>)`**（`OriginateCommand.kt:80-85`，注释「群呼设置参数」）。
- `caller` = 线路主叫号（外显号），`callee` = 客户号（明文，已在后端解密），`gateway` = `line.address`，`calleePrefix` = `line.calleePrefix`。

---

## 4. 对 hermes-mock 的启示

- **可复用 / 已具备**：
  - 群呼打到被叫客户线路时，mock 看到的就是一条**普通入向 SIP INVITE**：被叫号=`{calleePrefix}{客户号}`，主叫=线路外显号(`caller`)，从 `sofia/external/...@{网关}` 发来——**与坐席手动外呼的客户腿、机器人/通知外呼是同一种入流量**（见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §4 对照表）。mock 现有的被叫 UAS 能力（diago/sipgo）无需新增协议即可应答/拒接/振铃超时来扮演被叫客户。
  - 验证群呼"接通后转坐席"的真实媒体，可沿用 `deploy/fs/public_hm_agent.xml` 的 originate+bridge 脚手架（mock 扮客户腿，另一路 user 扮坐席腿）。
  - 关键 SIP/ESL 头：客户腿带 `x-call_center_type:GROUP_CALL`（ESL 里 `variable_sip_h_x-call_center_type`），mock 可据此断言"这通是群呼"。验证时主要看：被叫号、主叫（线路号）、录音相关参数、`&park()`/`&playback`。
- **做不到 / 需外部依赖 / 需桩**：
  - **预测式发起、调度、转坐席全在 call-center 后端**，FS dialplan 不参与；mock 无法只靠 FS 复刻"按比例/PID 控制发起速率""客户接通→找空闲坐席"。要端到端跑通群呼需真实/桩化的 hermes-call-center（含 Quartz、Redis 运行队列、Kafka、加解密 tokenize、PhoneApi/OrgApi/TtsApi、坐席状态 AUTO_OUTBOUND）。
  - mock 不参与 ESL 事件回流与坐席腿——它只演"客户被叫腿"。若只想压测/验证"被叫侧表现"，mock 批量应答即可；若要验证"转坐席桥接"，需再补一路坐席腿（真实 user 或第二路 mock）。
  - **重拨/呼损/接通率统计**依赖 call-center 的 `t_call_task_cdr` 状态机与拨号线程，mock 无法自洽模拟，只能在 mock 侧用应答/拒接/不应答的比例去"喂"真实 call-center 观察其预测调速行为。

---

## 5. 待补 / 推断项

- ⚠️ **拨号主循环（预测式 originate）的确切文件与行号未定位**（§2.4）：本次核查全文搜索工具不可用，`getRandomRunningTask` 的调用方未读到。比例算法的"空闲坐席数×proportion"与 PID 的具体公式/系数均为推断。**落地前搜索 `getRandomRunningTask` / `freeswitchApi.originate` / `OriginateCommand(` 的调用方。**
- ⚠️ **GROUP_CALL 的 ESL handler（`isSupport(GROUP_CALL)`）文件未定位**（§2.5）：已排除 `listener/fs/business/` 下若干直觉命名。**落地前搜索 `BusinessCallTypeEnum.GROUP_CALL` / `GROUP_CALL` 的引用。**
- ⚠️ `confirmUrlBeforeDial`（呼前确认）、`AUTO_OUTBOUND` 坐席与群呼分配引擎的对接点、`assignDelayMills` 的生效位置——字段/入口已确证，但触发代码点随上面两个文件一并待补。
- ⚠️ `xgboost` 依赖（`build.gradle.kts:26`）疑用于接通率/意向/语音信箱预测，未在已读链路中直接读到使用点（推断）。
- ⚠️ `TaskNumberSourceEnum` 完整取值（已见 `ORG_MANAGER`/`OPENAPI`，注释「1:api 2:机构管理端」）；OpenAPI 建任务并导号入口 `createTaskAndImportNumber`（`CallTaskService.kt:1149-1220`）的对外路由（疑在网关/另一 controller）未核。
- ⚠️ 坐席侧 CDR 表 `t_task_agent_cdr` 字段、`assignAgent` 的具体选坐席策略未逐字段核（`AgentStatusService`）。

## 附：关键证据索引（路径:行号）

- `hermes-call-center/.../controller/CallTaskController.kt:18-105`：群呼任务管理 REST（根 `/call-tasks`，建/改/启停/取消/查/复制）。
- `hermes-call-center/.../controller/CallTaskNumberController.kt:25-147`：号码管理 REST（根 `/task/number`，导入/校验/列表/单号暂停恢复取消）。
- `hermes-call-center/.../entity/request/calltask/AddCallTaskReq.kt:12-169`：建任务请求 DTO（比例/PID、时段、坐席/坐席组、线路type、重拨、回调、确认URL）。
- `hermes-call-center/.../entity/po/CallTaskPo.kt:7-222`：`t_call_task` 任务主表（含预测式与统计字段）。
- `hermes-call-center/.../service/CallTaskService.kt:331-471`：`createTask` 全流程（校验→入库→Quartz→坐席关系）；`:1413-1435` 模式/坐席互斥校验；`:846-855` 任务结束 Kafka 回调。
- `hermes-call-center/.../service/TaskCdrService.kt:131-249`：导号生成每号 `t_call_task_cdr`（tokenize 加密、状态 CREATED）。
- `hermes-call-center/.../backgroud/task/CallTaskScheduler.kt:28-205`：Quartz 按 `dialTimePeriod` 建 cron 触发器 + 结束监控 job。
- `hermes-call-center/.../backgroud/task/NotifyDialJob.kt:21-94`：到点把 IN_PROGRESS 任务塞进运行集合（不直接拨）。
- `hermes-call-center/.../backgroud/task/CallTaskHelper.kt:71-87,155-247,252-284,333-341,363-382`：运行中任务 Redis zset（优先60%/非优先40%）、`getRandomRunningTask`、任务并发计数、状态推断、状态变更回调。
- `hermes-call-center/.../entity/fs/command/OriginateCommand.kt:42-88`：群呼客户腿 FS originate 命令形态（`{params}sofia/external/{prefix}{callee}@{gateway} &park()/&playback`）。
- `hermes-call-center/.../entity/request/freeswitch/OriginateReq.kt:5-19`：originate 入参结构。
- `hermes-call-center/.../constant/enums/{TaskModeStrategyEnum,CallTaskTransferTypeEnum,TaskStatusEnum,TaskCdrStatusEnum}.kt`：预测模式/转接类型/任务与号码状态枚举。
- `hermes-call-center/.../constant/enums/BusinessCallTypeEnum.kt:12,27-29`：`GROUP_CALL` 与 ESL 路由头字段。
- `hermes-call-center/.../listener/fs/FsEventKafkaListener.kt:30,43-100` + `CompositeEventDispatcher.kt:79-100`：ESL 事件消费与按 call type 路由（GROUP_CALL handler 文件待补）。
- `hermes-call-center/.../component/queue/{QueueManager,QueueCallEventAdapter,QueueContext}.kt` + `service/CallIncomingService.kt:239-429`：坐席排队/分配引擎（群呼转人工复用，可分配状态默认 ONLINE，群呼应为 AUTO_OUTBOUND）。
- `hermes-call-center/.../controller/agent/workbench/AgentController.kt:75-87`：坐席切「自动外呼 AUTO_OUTBOUND」态。
- `hermes-call-center/.../service/GroupCallNotifyService.kt:36-40`：群呼进度 WS 实时推送（disruptor）。
