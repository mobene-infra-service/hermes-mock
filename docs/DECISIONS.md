# DECISIONS — 关键决策日志

> 记录**影响定位 / 边界 / 架构**的取舍：决策、背景、影响。倒序（最新在上）。
> 做出此类取舍时追加一条；不记日常实现细节（那些进代码注释或 STATUS）。
>
> 初始几条由现有文档与代码状态**回填**整理，日期为近似月份。

---

## 2026-06-18 · FS Docker 部署：Hermes 线路目标使用内网 mock 入口，SIP 响应按包源回 Kamailio
- **背景**：将 mock 从 K8s PodIP 迁到 `hermes-freeswitch-test` Docker 后，FS/Kamailio 同机存在两类地址：内网 `172.16.7.27` 和公网 `47.251.74.116`。Kamailio 配置 `listen=udp:172.16.7.27:5060 advertise 47.251.74.116:5060` 且 `alias="47.251.74.116"`，所以转发出去的顶层 Via 会写公网 `47.251.74.116:5060`，但 Docker mock 实际收到包的来源应是内网 `172.16.7.27:5060`。实测 Hermes 线路目标配置为 `47.251.74.116:15060` 时，mock 无 `收到 INVITE` 日志；改为 `172.16.7.27:15060` 后 INVITE 正常进入 mock。
- **决策**：
  1. FS 机器 Docker/裸跑部署时，Hermes 线路地址使用 mock 监听的**内网入口**：`172.16.7.27:15060` 到 `172.16.7.27:15069`。公网 `47.251.74.116` 是 Kamailio 对外 advertise/alias 地址，不作为同机 mock 线路目标。
  2. mock Docker 镜像默认开启 `SIP_RESPONSE_TO_SOURCE=true`。实现上在 UDP 入站 SIP 请求进入 sipgo parser 前，为顶层 Via 补 `rport=<包源端口>;received=<包源IP>`，让 diago/sipgo 后续 180/200/拒接响应沿标准路径回到实际包源，避免被 Kamailio 公网 Via 带偏。
  3. 保留代码默认 `SIP_RESPONSE_TO_SOURCE=false`，只在 Docker 部署默认打开，避免改变其它部署拓扑的默认 SIP 行为。
- **影响**：`internal/config` 新增 `SIP_RESPONSE_TO_SOURCE`；`internal/sipagent` 接入 `sipgo.WithUserAgentTransportLayerOptions(sip.WithTransportLayerReadFilter(...))`；`deploy/Dockerfile` 默认打开该开关；启动/通话日志通过 `responseDest` 暴露 sipgo 实际响应目的地址。部署文档层面要求 FS 机器线路目标使用内网 IP。
- **状态**：`go test ./internal/sipagent`、`go test ./...` 通过；FS 机器实测线路目标改为 `172.16.7.27:15060` 后呼叫可进入 mock。

## 2026-06-12 · 端口绑定权威化：命中端口绑定不再回退按号匹配别的组
- **背景**：排查「cluster 页绑了端口却不按绑定行为处理」。`sipagent.resolveRule` 旧逻辑 `res = ResolveByPort(port); if res == nil { res = ResolveByNumber(callee) }`——只要端口解析返回 nil（绑定禁用 / 绑定的组不存在 / 组 behaviorCode 指向缺失行为档），就**静默回退**按号段匹配，可能命中**另一个**号段覆盖该号的组、用上完全不同的行为；且全程**无任何日志**。叠加 `UpsertBinding` 只校验端口 1-65535、**不校验端口是否在实际 SIP 监听端口（`SIP_LISTEN_PORTS`，默认仅 5060）内**——绑了非监听端口即「死绑定」，永不收到来话。两者合力 → 用户感知「绑定不生效/只生效一部分」。
- **决策**：
  1. **端口绑定权威**：入口端口有**启用**绑定时，只按该绑定客户组(+组内/全局个例)解析；命中端口但组/行为档缺失 → **默认兜底 + WARN**，不再静默串到别的组（对齐 STATUS「listenPort→customer_group 决定行为」的本意）。端口**无**绑定才回退按号段/个例。
  2. **可见性**：`resolveRule` 每通记录解析来源（`port-binding`/`number` Info；未命中/缺失 WARN，提示具体排查点）。新增 `Store.HasBinding/BoundPorts`。
  3. **启动期对账**：`warnBindingPortMismatch` 比对绑定端口 ↔ 实际监听端口，警告死绑定（绑了没监听的口）、提示未绑定监听口（走号段/默认兜底）。
  4. **解析预览诊断**：`/cluster/resolve` 返回 `source`+`note`，点明「端口未绑定→运行时回退按号/默认」「端口已绑定但组缺失」「端口不在监听端口→死绑定」，cluster 页预览直接可见。
- **影响**：`sipagent/agent.go`（resolveRule）、`cluster/store.go`（HasBinding/BoundPorts）、`cmd/main.go`（启动对账）、`api/api.go`（clusterResolve 诊断）、`web/src/api.ts`（类型）。语义变更：以前「绑定的组行为档缺失」会串到别的组，现在改为默认兜底——更可预期，但依赖「绑定+号段双命中」旧巧合行为的用例需复核。
- **状态**：`go build`/`go vet`/`go test ./internal/{cluster,sipagent,api}` 全绿（新增 `TestHasBindingAndBoundPorts`）；`tsc` 通过。**端到端待本地栈**（确认启动告警 + 每通解析来源日志 + 绑定行为生效）。

## 2026-06-11 · 群呼 createAndImport 即自动拨号，移除多余 start；补任务暂停/取消 API
- **背景**：审查 `CALL-FLOWS.md` 时发现 `orchestrator.RunCallCenterTask` 在 `createAndImport` 后默认再调 `status/start/{taskCode}`，并用 `if strings.Contains(err, "Task status is incorrect") { return nil }` 吞掉错误。比对 Hermes 真实源码：
  - `TaskOpenApiController.createTaskAndImportNumber`：注释「创建任务并导入号码**(可选)**」；末尾直接 `NotifyDialJob.runJob(...)` 异步拨号；`createTask` 内 `status = determineCurrentStatusByDate(...)`（startDate≤今天≤endDate 即 `IN_PROGRESS`）。
  - `CallTaskService.startTask`：**仅接受 `status==PAUSE`**，否则抛 `Task status is incorrect`。即 `start` 的语义是「恢复暂停任务」，不是「启动新任务」。
  - 结论：`createAndImport` 后再调 `start` 是**无意义调用**，mock 用容错掩盖了它。
- **决策**：
  1. `RunCallCenterTask` **移除默认 start 逻辑**，建任务即返回（createAndImport 已自动拨号）。`AutoStart` 字段保留（兼容前端表单）但不再触发 start，加注释说明。
  2. **补任务生命周期管理 API**（Hermes 本有但 mock 未接）：`StopCallCenterTask`(暂停)/`StartCallCenterTask`(恢复 PAUSE)/`CancelCallCenterTask`(取消)/`GetCallCenterTaskStatus`(查状态) + orchestrator 包装 + api 路由 `POST/GET /tests/callcenter-task/:taskCode/{pause,resume,cancel,status}`。`api.Deps` 增 `Orch` 字段（main.go 注入），`Register` 签名加 orchestrator 参数。
- **影响**：`hermesopenapi/api.go`、`orchestrator/orchestrator.go`、`api/api.go`（Deps+Register+4 handler+4 路由）、`cmd/main.go`（Register 调用）。
- **状态**：`go build`/`go test ./...`（除 hermesopenapi 沙箱网络）全绿；端到端待本地栈（暂停/取消真实生效 + 确认移除 start 后群呼正常拨号）。

---

## 2026-06-11 · DB 模型重构：call_uuid 聚合锚 + 配置/观测分离 + 单腿 trace（阶段0–3）
- **背景**：`mock_call_record` 被 4 个写入器以互不相交的主键规则写入（Tracker 裸 uuid / `CallRecordFromTraceSession` 从 SIP 反推 / testkit / 坐席转接），一通电话散成 3 条记录 + 2 个 bus session 无法合并；38 列大宽表混「发起预期」与「SIP 观测」靠脆弱 merge 缝合；`trace_session.legs[]` 做跨腿聚合（踩 SCOPE 非目标）；DDL 与 AutoMigrate 双轨漂移（org_config 缺 4 列、callback 索引名打架、配置表 gmt 实体无字段、死表 `mock_test_case`）；`upsertByKey` 先查后写有竞态；`customer_override` 全局 `uk_number` 唯一（同号不能跨组）。
- **决策**：
  - **聚合锚用 `call_uuid`**（被叫腿从真实 `X-CALL-UUID`/`x-session-id` 头由 `BizUUIDFromHeaders` 提取）。mock 是 UAS、无法给 Hermes 发起的 INVITE 注头，故 mock 生成的 id 无法端到端贯穿——`record_id` 仅作内部主键，跨场景/跨腿关联一律靠 `call_uuid`。被叫腿 `Tracker.Start(callUUID,…)` 直接用 call_uuid 作 record_id 主键，INVITE 重传幂等合并到一行。
  - **删 `CallRecordFromTraceSession` 反推**：traceFlushLoop 只落 trace；被叫腿 call_record 由 Tracker 在 INVITE 时直接落。根除「一通电话多条记录」。
  - **拆表**：`mock_call_record`→`mock_call`（聚合根，删 4 个死的 SIP 观测列 sip_call_id/signal/media/callback_summary，加 expect_outcome）；`mock_trace_session`→`mock_trace_leg`（**写入侧严格单腿**：加 leg_role/line，删 legs[]）。**「一通业务通话含多腿」由读时按 call_uuid 归并**（`ListTraceLegsByCallUUID` + api 装配），是纯展示装配、不写回不产业务结论——守住「写入侧不跨腿聚合」的 SCOPE 边界（前端梯形图本就主要从 events 的 SIP From/To 重建 parties，不依赖 DB legs[]）。
  - **schema 单源**：entity gorm tag 为唯一权威，DDL 为生成快照；启动 AutoMigrate。索引名全库唯一（sqlite 约束：`uk_behavior_code`/`uk_group_code` 不能都叫 `uk_code`）。
  - **override 复合唯一 `(group_code, number)`**；全部 upsert 改 `clause.OnConflict` 消竞态；配置实体补 `gmt_create/gmt_modified`。
  - **老观测数据直接 DROP 重建**（测试产物非资产，AutoMigrate 不支持改名/拆列），配置 5 表数据保留。
  - **观测表 TTL**：`OBSERVE_TTL_DAYS`（默认 7）+ `PruneObservations` + main `pruneLoop` 周期清理，防膨胀。**凭据暂不加密**（内网测试环境，仅加注释）。
- **影响**：`entity/db.go`（MockCall/TraceLeg）、`model/{repo,factory,sql/*}`、`cluster/{store,rows}`（override 复合键，删 call_record_trace.go）、`calltrace/tracker.go`、`sipagent/agent.go`、`api/api.go`（traceSessionFromEntity 单腿）、`cmd/main.go`（traceFlushLoop + pruneLoop）、`config`、`deploy/ddl`、`web/src/types`（4 字段改可选）。
- **待实测**：坐席 jssip 外呼注 `x-session-id: CCMDL{callId}`，被叫腿据此提取 call_uuid 与坐席侧 `CCMDL+callID` 一致——**前提是 FS/call-center bridge 透传 `x-session-id` 到被叫腿**（Hermes 真实链路 `Bridge.kt` 确带 `sessionId=CCMDL+callUuid`，见 `docs/hermes/agent-outbound-call.md:108,110`），需本地栈端到端确认；不透传则坐席外呼两腿降级为各自独立记录（功能不退化，仅少跨腿关联）。
- **状态**：阶段 0–3 + BizUUID 优先级修复代码全绿（`go build`/`go test`，仅 hermesopenapi 因沙箱禁 httptest 绑端口失败，与本次无关）；DB 端到端 + 前端 build/实测待本地栈。

---

## 2026-06-11 · 坐席记录 call_uuid 前缀按腿区分（CCMDL 仅坐席外呼）
- **背景**：审查指出 `saveCallRecord`（`POST /call-records`，**仅坐席软电话调用**——群呼/callbot/otp 走 testkit、被叫腿走 calltrace，均不经此接口）对 inbound/outbound 两条腿一律写 `CallUUID = "CCMDL" + in.CallID`。比对 Hermes 真实源码（`/Users/Xuxx/IdeaProjects/hermes`）：
  - `hermes-common/.../BizType.kt`：`CCMDL`=坐席手动外呼、`CCINC`=呼入、`CCTSK`=群呼任务、`CBTSK`=callbot、`OTPVC`=otp……sessionId 格式 = **5 位 BizType 前缀 + 32 位 v7 uuid**（`SidUtils.validSid` 校验长度=37、前缀匹配）。
  - `hermes-call-center/.../TransferService.kt:366-368`（`getSessionId`）：`OUTBOUND_CALL→CCMDL+callUuid`、`INBOUND_CALL→CCINC+callUuid`、`GROUP_CALL→CCTSK+callUuid`。
  - `OutboundCallHandler.kt:310-311`：坐席外呼时后端 `extractValidCallUuid(sessionId, CCMDL)` 剥前缀取 uuid，bridge 到客户腿（=mock 被叫腿）时再拼回 `CCMDL+callUuid` → 故**坐席外呼**两腿 call_uuid 确为 `CCMDL{uuid}`，一致。
  - 前端 `web/src/sip/index.ts`：外呼注 `x-session-id: CCMDL{uuidv7}`（`in.CallID`=裸 uuid，补前缀正确）；**接来电**时 `currentCallId = getHeader('x-session-id')`（`index.ts:169`）——**已是带前缀的完整 sessionId**（`CCINC{uuid}`/`CCTSK{uuid}`）。
- **问题**：inbound 腿再叠 `CCMDL` → `CCMDLCCINC{uuid}` **双前缀**，既不合法也对不上任何腿；outbound 腿叠 `CCMDL` 才正确。
- **决策**：`saveCallRecord` 按腿区分——**坐席外呼**（`!Inbound`）补 `CCMDL` 前缀（前端裸 uuid）；**坐席接来电**（`Inbound`）直接用 `in.CallID`（头原值已含 CCINC/CCTSK 等正确前缀）。
- **影响**：`internal/api/api.go` `saveCallRecord` 一处。修正了上一条「CCMDL 天然一致」的不完整结论：该结论**只对坐席外呼成立**，inbound 腿前缀本就不同。
- **状态**：`go build`/`go test ./internal/api` 绿；端到端待本地栈。

---

## 2026-06-11 · BizUUID 提取优先级修正：x-session-id 是通用 callUuid 载体，x-jcallid 降为兜底
- **背景**：审查坐席外呼关联链路时发现，`tracelog.BizUUIDFromHeaders` 旧优先级把 `x-jcallid` 放在 primary（`x-call-uuid/x-callid/x-jcallid/x-call-id`）、`x-session-id` 放在 secondary。但比对 Hermes 真实代码梳理：① **所有被叫场景的 callUuid 通用载体是 `x-session-id`**（hermes-common `EslEventConstant.SID = "variable_sip_h_x-session-id"`，群呼/callbot/otp/坐席外呼共用，证据 `docs/hermes/otp.md:90,127`、`agent-outbound-call.md:79`）；② **`X-JCallId` 在坐席外呼里传的是 `businessId`，不是 callUuid**（`agent-outbound-call.md:80`）。旧优先级会让被叫腿在 INVITE 同时带 `X-JCallId` 时错取 businessId 当 call_uuid，与坐席侧 `saveCallRecord` 写的 `CCMDL{uuid}` 关联不上——是个潜在裂缝（当前未暴露大概率因 FS 未透传 `X-JCallId` 到被叫腿）。
- **决策**：调整 `BizUUIDFromHeaders` 优先级为 **真 callUuid 头(`x-call-uuid`/`x-callid`/`x-call-id`) + `x-session-id` 族 同权优先 → `x-jcallid` 最末兜底**。坐席侧 `saveCallRecord` 的 `CCMDL+callId` 写法**保留**（符合 Hermes `BizType.CCMDL` 语义，是 sessionId 的合法整体格式，不剥前缀）。
- **影响**：`internal/tracelog/bus.go`（优先级列表 + 注释带证据）；`tracelog`/`siptrace` 两个优先级测试改为验证「x-session-id/真callUuid 优先于 x-jcallid」。影响全局 trace 聚合键（siptrace + sipagent 共用），群呼/callbot/otp/坐席全场景回归通过。
- **状态**：代码全绿；真实透传行为仍待本地栈端到端确认（同上条「待实测」）。



---

## 2026-06-11 · Trace 列表接口瘦身：摘要列表 + 详情/匹配单查
- **背景**：`GET /trace/sessions` 每次返回全量 session（含每条 event 的原始 SIP 报文，单响应数百 KB），被 CallTracePage(2s)、每张坐席卡(1.5s 且非终态永久轮询)、Overview 多处高频并发拉取——带宽浪费、前端卡顿。
- **决策**：`GET /trace/sessions` 无 query 返回**轻量摘要**（id/title/kind/callId/legs/eventCount，不含 events）；events 走 `GET /trace/sessions/:id` 单查。坐席软电话找「自己这通」trace 改用 `?match=<jssip callId>`——服务端复刻原 `sessionMatchesCall` 的「整 session JSON 子串包含」语义、回完整单条，**绕开匹配键藏在 event 里、列表瘦身后前端 find 必失效**的命门（match 语义与旧前端逻辑等价，不改变命中结果）。
- **影响**：`internal/api`（摘要 DTO + `traceSessions` 双行为 + `hermesOverview` 改摘要）；前端 `TraceSession.events` 改可选 + 加 `eventCount`；CallTracePage 选中单查 `detail`；AgentSoftphone `refreshTrace` 走 `findTraceSession` + 可见性守卫（offsetParent）+ 非终态轮询上限；轮询周期放宽（2s→3s、1.5s→2.5s）。
- **状态**：已落地，`go build/test` + `tsc -b`/`vite build` + embed 同步通过；jssip callId 在 event 的实际落点待本地栈起一通坐席外呼端到端确认。

---

## 2026-06-11 · Cluster 按 mock SIP 入口端口绑定客户组
- **背景**：mock 的用途是作为 Hermes 线路对端；Hermes 侧 `t_line.address` 已经负责把 INVITE 送到 `mockIP:port`。mock 内部再次填写 `lineAddress` 会把“外部送达地址”和“内部客户组路由”混在一起，也无法自然表达 5060/5061 不同客户池。
- **决策**：`mock_line_binding` 的主路由键改为 `listenPort`，语义为 `mock SIP 入口端口 -> customer_group`；`lineAddress` 从 cluster 绑定中删除。`transport` 不进入绑定模型，继续由全局 `SIP_TRANSPORT` 控制，当前按端口区分即可。
- **影响**：`sipagent` 支持 `SIP_LISTEN_PORTS` 多端口启动；cluster 解析优先按入口端口命中客户组；前端绑定页改为端口绑定。
- **状态**：已落地，Go 全量测试、前端构建与 embed 校验通过。

---

## 2026-06-11 · Trace leg 只表示 mock 侧真实通话腿
- **背景**：坐席外呼客户腿 trace 中，同一 SIP Call-ID 的事件被记录成客户号、`customer`、外显主叫号三种 leg，导致页面误报 3 个 leg。
- **决策**：`leg` 仅表示 mock 侧真实腿标识（客户号 / `agent:分机`）；角色名放事件 `Detail`，对端主叫号只从 SIP From/To 展示，不进入 `legs`。
- **影响**：`siptrace` 按 SIP Call-ID 固定 INVITE 的被叫号作为 leg，`sipagent` 的 FLOW/BRIDGE 事件使用真实 callee。
- **状态**：已落地，Go 全量测试通过；线上需新生成 trace 验证历史数据不回写。

---

<!-- 新决策模板（复制到最上方）：
## YYYY-MM · 一句话决策
- **背景**：为什么需要这个决策。
- **决策**：具体怎么定。
- **影响**：动了哪些模块/文档/边界。
- **状态**：已落地 / 进行中 / 待验证。
-->
