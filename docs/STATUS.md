# STATUS — 当前状态 / Feature / 已知问题

> **这是最常更新的文档**：开工前先读「当前焦点」，收工后更新 Feature 状态与已知问题。

## 当前焦点

- **前端按 Figma 重构稿全量重做（2026-06-15 落地）**：新设计系统（slate/blue 色板 + Antd token 单一来源 `web/src/constants/theme.ts`）；自绘深色侧栏 + 顶栏面包屑/机构切换器（`web/src/components/layout/`）；全部 14 屏统一 PageHeader + InfoBanner；场景页（群呼/OTP/call-bot）配置表单收进右侧抽屉、主区改全宽结论横幅 + 通话明细行 + 历史。纯前端、零后端接口改动。`tsc`/`vite build`/`make sync-web`/`go build` 全绿，Playwright 逐屏核对一致。**待办**：① 本机无 eslint 可执行文件，`npm run lint` 未跑；② 端到端（接真实后端/本地栈）跑一遍各场景，确认抽屉提交 + 实时观测 + 机构切换器联动正常；③ 设计稿里坐席卡展开态/批量派号细节（BatchBar 吸顶汇总、接听规则面板）已沿用原有富交互、未逐像素对齐，如需再细化。
- **DB 模型重构（阶段0–3）已落地**：根除「一通电话散成 3 条 call_record + 2 个 bus session」核心病。`call_uuid` 为唯一跨场景聚合锚；`mock_call_record`→`mock_call`（聚合根，删 4 个死的 SIP 观测列）、`mock_trace_session`→`mock_trace_leg`（写入严格单腿，多腿读时按 call_uuid 归并）；override 复合唯一 + OnConflict 消竞态 + schema 单源（实体 tag 权威，DDL 快照）；观测表 TTL（`OBSERVE_TTL_DAYS=7`）。**待办**：① 本地栈跑一通被叫群呼，确认 `mock_call` 1 行/通 + `mock_trace_leg` 单腿 + 前端记录页/trace 梯形图正常；② 坐席 jssip 外呼确认 `x-session-id: CCMDL{callId}` 被 FS/call-center bridge 透传到被叫腿（两腿同 call_uuid → 多腿视图），不透传则两腿独立记录（不退化）；③ `make web` 前端构建（本机无 node_modules，tsc 未跑）。
- **Trace 接口瘦身降轮询开销已落地**：`/trace/sessions` 无 query 返回摘要（不含 events、带 `eventCount`），详情走 `/trace/sessions/:id` 单查，坐席软电话用 `?match=<jssip callId>` 让服务端匹配回完整单条；前端轮询加可见性守卫 + 非终态上限 + 周期放宽。
- Cluster 绑定模型已从 `lineAddress` 收敛为 mock SIP 入口端口：Hermes 线路负责把 INVITE 送到 `mockIP:port`，mock 内部按 `listenPort -> customer_group` 决定客户行为。

## 已知技术债 / 待办

- _（坐席死代码 `wsagent`/`agents.Registry` 与 `hermesprobe` 健康探测已于 2026-06-13 清理，见验证记录。）_

## 验证记录

- 2026-06-15：行为档拒接码与配置体验修正——后端 `reasonForCode` 补齐常见 SIP 4xx/5xx/6xx reason phrase，拒接/不可用/振铃不接日志明确打印实际响应码与 reason，`rejectCall` 不再吞 `Respond` 错误；新增表驱动测试覆盖自定义 500/603 等。前端 Cluster 行为档弹窗将「挂断码」改为「拒接/响应码」并补充生效条件与默认码说明，补齐 DTMF/故障注入/监听按键说明，移除已废弃的 BRIDGE/桥接目标新配置入口，修复 IVR「添加按键分支」因空键被过滤导致无反应的问题。`npm run build`、`make sync-web`、`make verify-embed`、`go test ./internal/sipagent`、`go build ./...` 通过（Vite 仅保留 chunk size warning）。

- 2026-06-13：统一后端请求日志，根治「接口报错却没日志」——`main.go` 由 `gin.New()+gin.Recovery()` 改挂 `api.RequestLogger()+api.Recovery()`（新增 `internal/api/middleware.go`）。`RequestLogger` 在响应写出后按状态码统一落日志：5xx→Error、4xx→Warn（均把错误响应体抄进 `resp` 字段，封顶 4KB）、2xx/3xx→Debug（默认 info 级不刷屏），`/api/health` 跳过（k8s 探针高频轮询）；零改动 handler，现有+未来接口自动有日志。`Recovery` 经 logrus 把 panic 落 Error 带堆栈并返回 500 JSON，置于 RequestLogger 内层使恢复后仍记到最终 500。**同时统一 mock→Hermes 出站调用日志**：`hermesopenapi.New` 给 `http.Client` 装 `loggingTransport`（新增 `internal/hermesopenapi/logging.go`），每次 Hermes OpenAPI 调用打 `method/url/reqBody/status/respBody/latency`（体封顶 2KB，2xx→Info、非 2xx→Warn、传输错→Error，**不打请求头避免泄漏 X-OpenApi-Key**）——一处覆盖 orchestrator + 各 api handler + ping/tts/agent-groups/managed-agents 全部出站。`go build ./...`、`go test ./...` 全绿，新增 `middleware_test.go`/`logging_test.go` 表驱动覆盖日志级别、响应体抄录与回灌不丢、panic 落库、health 跳过。

- 2026-06-13：清理死代码——删 `internal/hermesprobe/`（Hermes 栈健康探测，页面展示早已撤、前端不消费）、`internal/wsagent/`（坐席经 hermes-ws 上线，坐席统一走前端 jssip 软电话，全链零调用方）、`internal/agents/`（坐席状态表，唯一写入方是 wsagent）。连带：api 删 `/hermes/health`、`GET /agents`、`/agents/login`、`/agents/login-batch`、`/agents/status-batch` 路由及 handler（`hermesHealth`/`listAgents`/`agentLogin`/`agentLoginBatch`/`agentStatusBatch`/`agentStatusCode`）+ `Deps.Prober/Agents/Ws` 字段 + `Register` 形参收窄；`hermesOverview` 去掉 `hermes.health` 与 `mock.agents` 段（前端 OverviewPage 本就只读 `mock.stats/active`+`trace.sessions`，**总览页与 `/hermes/overview` 端点保留**）；`testkit.New` 去掉未用的 `prober` 形参；main 删对应接线；前端 `types/index.ts` 删 `ServiceHealth` 接口 + `Overview.hermes` 字段。`go build ./...`、`go vet ./...`、`go test ./...`、`go mod tidy` 全绿（同步修正 `kit_test.go` 的 `New` 调用）。**注**：前端类型改动需 `make web` 重建 embed 后再部署（仅类型收敛，运行时本就不消费这两段，不影响行为）。

- 2026-06-12：群呼任务参数补全 + 模式策略组合（后端+前端）——`BizCaller.CallCenterTask` 长签名收敛为 `entity.CallCenterTaskReq` 单结构体（顺带删 orchestrator 死字段 ObserveAgent/WaitSec/AutoStart）；`modeStrategy=1→proportion` / `=2→lossRate+historicalConnectionRate` 按 Hermes `validateAddRequest` 分支下发；补齐 sortMethod/isPriorityTask/isVmHangup/maxRedialTimes/redialInterval/bestRingDuration/agentMaxRingDuration/assignDelaySeconds/transferType/description（可选项有值才入 body）。前端 `GroupCallPage` 删「自动启动」、TTS code 改 Select 展示名称、新增模式策略条件渲染 + 高级参数折叠面板；lineType 维持 AutoComplete 支持自定义输入。`gofmt`/`go build`/`go test`（除 hermesopenapi 沙箱）全绿；`tsc -b`/`vite build`/`make sync-web` 通过。**端到端实测待本地栈**。

- 2026-06-11：群呼参数对照 Hermes `AddCallTaskAndImportNumberReq` 修正——移除多余「组织码」（orgCode 由凭据头取，前端改只读显示当前机构）；坐席分配改**二选一**（agentNumbers 坐席号 / agentGroupCodes 技能组，Hermes `@Size(max=1)` 技能组仅取 1 个，修复原多选触发校验失败）；技能组展示真名（`/orgs/agent-groups` 优先调 basic `getAgentGroupsByOrgCode`，失败降级聚合）；前端 `GroupCallPage` 加坐席分配 Radio（技能组单选/坐席号多选）。`go build`/`go test`（除 hermesopenapi 沙箱）全绿；端到端待本地栈。

- 2026-06-11：群呼任务移除多余 `status/start`（createAndImport 即自动拨号；对照 Hermes `CallTaskService.startTask` 仅接受 PAUSE）+ 补暂停/恢复/取消/查状态（**后端+前端全打通**）：testkit 提取 taskCode 塞 `Run.Artifacts`、`api.ts` 加请求函数、`GroupCallPage` 加 taskCode 展示 + 暂停/恢复/取消按钮；路由 `/tests/callcenter-task/:taskCode/{pause,resume,cancel,status}`（`api.Deps` 增 `Orch`）。新增 `docs/CALL-FLOWS.md`。`go build`/`go test`（除 hermesopenapi 沙箱）全绿；暂停/取消端到端待本地栈。

- 2026-06-11：修正坐席记录 `call_uuid` 前缀按腿区分（对照 Hermes `BizType.kt`/`TransferService.kt`）——`saveCallRecord`（仅坐席软电话调用）坐席外呼补 `CCMDL`（前端裸 uuid），坐席接来电直接用 `in.CallID`（头原值已含 CCINC/CCTSK 前缀），不再对 inbound 叠 `CCMDL` 致双前缀。`go build`/`go test ./internal/api` 绿。

- 2026-06-11：修正 `BizUUIDFromHeaders` 提取优先级——`x-session-id`(=`CCMDL{uuid}`，Hermes 通用 callUuid 载体) 与真 callUuid 头同权优先，`x-jcallid`(坐席外呼里=businessId) 降为最末兜底；消除被叫腿错取 businessId、与坐席侧 `CCMDL+callId` 关联不上的潜在裂缝。`tracelog`/`siptrace` 两个优先级测试改验新语义，全场景回归绿。坐席侧 `CCMDL+callId` 写法保留（符合 `BizType.CCMDL`）。

- 2026-06-11：DB 模型重构（阶段0–3）——override 复合唯一 + OnConflict + 配置实体补 gmt + 索引名全库唯一；删 `CallRecordFromTraceSession` 反推、`Tracker.Start` 用 call_uuid 作主键；`mock_call`/`mock_trace_leg` 拆表（删 4 死列、写入单腿 + 读时按 call_uuid 归并、新增 `ListTraceLegsByCallUUID`）；`OBSERVE_TTL_DAYS` + `PruneObservations` + `pruneLoop`。`go build ./...`、`go test $(go list ./...|grep -v hermesopenapi)` 全绿（新增 `TestTraceLegsByCallUUID`/`TestPruneObservations`，改造 call_record/trace 用例）；迁移冒烟确认 10 张新表建出、`mock_call_record`/`mock_trace_session`/`mock_test_case` 消失；`go vet` 干净。**DB/前端端到端实测待本地栈**（被叫群呼记录形态 + 坐席外呼跨腿 call_uuid 透传 + `make web`）。

- 2026-06-11：trace 列表接口瘦身——`/trace/sessions` 摘要化（不含 events，新增 `eventCount`），events 走 `/trace/sessions/:id` 单查，坐席软电话改 `?match=<jssip callId>`（服务端整 session 子串匹配，复刻原 `sessionMatchesCall`，回完整单条）；前端轮询加可见性守卫（`document.hidden` / 常驻软电话 `display:none` 时 `offsetParent===null` 跳过）、非终态轮询上限（坐席卡 ~40 次）、周期放宽（CallTracePage 2s→3s、坐席卡 1.5s→2.5s）；`hermesOverview` 同步改摘要。`go build ./...`、`go test ./internal/{api,tracelog}`（含新摘要/match 用例）、`tsc -b`、`vite build`、`make sync-web` 通过；`eslint` 因本机缺可执行文件未运行。**端到端实测待本地栈**。

- 2026-06-11：修复 sipagent/siptrace/tracelog 并发与逻辑问题——`tracelog.Bus` 读取返回深拷贝快照（消除与 emit 的数据竞争）；`siptrace` 的 `cid2biz/cid2leg` 加 `maxCID` 淘汰（堵内存泄漏）；bizUUID 提取抽成共享 `tracelog.BizUUIDFromHeaders`（call-uuid 族优先）保两路聚合键一致；sipagent 接听后写流改为单一互斥分派（修 RTP_LOSS/REORDER+DTMF 双写）、IVR 只取一次 AudioReader。`go build ./...`、`go test ./internal/{sipagent,siptrace,tracelog,cluster}`、`go test -race ./internal/{tracelog,siptrace}` 通过；全量 `go test ./...` 仅 `internal/hermesopenapi` 因沙箱禁止 httptest 绑定本地端口失败（环境所致，与本次无关）。

- 2026-06-11：坐席外呼页（/agent-call）交互优化——顶部使用说明默认折叠（点「使用说明 ▸」展开）；坐席选择由下拉多选改为**表格多选**（搜索 + 技能组/状态筛选 + 表头全选/全不选，技能组改为独立列、不再混进下拉文本）；坐席卡片改**网格多列并排**（折叠态 `sm12/lg8`，展开态全宽）、**默认折叠**、单卡链路/日志收进默认收起的 `Collapse`。`tsc -b`、`vite build` 通过；`eslint` 因本机缺可执行文件未运行。
- 2026-06-11：将 cluster 绑定改为入口端口绑定；`go test ./...`、`npm run build`、`make verify-embed` 通过；临时实例 `SIP_LISTEN_PORTS=15060,15061` 确认双端口监听与 `listenPort -> customer_group` 解析生效；本地浏览器确认端口绑定页/弹窗不再出现线路地址字段（构建仍有 Vite chunk size 提示）。
- 2026-06-11：修正坐席外呼客户腿 trace 出现 3 个 leg 的问题；`go test ./internal/siptrace`、`go test ./...` 通过。
- 2026-06-11：修正坐席外呼记录表分页只改页码不请求的问题；`npm run build`、`make verify-embed` 通过，临时预览点击第 2 页确认发出 `page=2` 请求。
- 2026-06-11：审计前端分页；将坐席管理页 `/agents` 改为服务端分页，临时预览点击第 2 页确认发出 `pageNum=2` 请求。
