# CHANGELOG — 改动记录

> 本项目改动按主题记录（倒序，最新在上）。决策原因见 [DECISIONS.md](DECISIONS.md)，当前状态见 [STATUS.md](STATUS.md)。
---

## 2026-06-18

- **FS 机器 Docker 部署 SIP 回源与线路目标结论**：
  - 新增 `SIP_RESPONSE_TO_SOURCE`：Docker 镜像默认开启，代码默认关闭。开启后 mock 在 UDP 入站 SIP 请求进入 sipgo parser 前，为顶层 Via 补 `rport=<包源端口>;received=<包源IP>`，让 diago/sipgo 的 180/200/拒接响应回到实际包源，避免被 Kamailio `advertise 47.251.74.116:5060` 的公网 Via 带偏。
  - 日志新增 `responseDest`，用于确认 sipgo 实际构造的响应目的地址。
  - 实测确认 FS 机器上的 Hermes 线路目标应写内网 mock 入口（如 `172.16.7.27:15060`），不要写 Kamailio 公网 alias/advertise 地址（如 `47.251.74.116:15060`）。公网地址会被 Kamailio 识别为自身地址，INVITE 可能不会继续送到 Docker mock。
  - 验证：`go test ./internal/sipagent`、`go test ./...` 通过。

## 2026-06-15

- **坐席外呼卡片（折叠态）按 Figma `11:442` 重构为紧凑卡 + 自动刷新默认关闭**：
  - **紧凑卡片重画**（`AgentSoftphone.tsx` 折叠态 / `index.css` 新增 `.hm-agent-card`·`.hm-agent-head`·`.hm-agent-pill`·`.hm-minicall`·`.hm-agent-body` 等）：原折叠态仅隐藏 antd Card body、头部堆一排 Tag；改为 Figma 三段式——**head**（坐席号 + 坐席名 + 单个状态色药丸：通话中/振铃中蓝·就绪/在线绿·连接中琥珀·未连接灰，带 dot + ✕ 移除）、**MiniCall**（状态色摘要框：通话/振铃蓝底显「⇄ 客户 X · mm:ss」+「PCMU 双向 · 派号 i/n · trace ↗」，空闲灰底显「空闲·等待派号或外呼」+ 接听规则摘要，离线显「离线·点连接上线」）、**ActionRow**（被叫号 input + 单个情境主按钮：振铃→接听 / 通话→挂断 / 未连→绿色连接 / 空闲→外呼）。通话/振铃中每秒走字（`mmss()` + `setNowTick`，隐藏标签页跳过）。展开态完整卡片不变。
  - **本场景记录自动刷新默认关闭 + 记住选择**（`ScenarioRecords.tsx`）：原 `useState(true)` 默认开、且组件重挂载（切页/切 Tab）即复位回开；改为默认关闭，并按场景持久化到 localStorage（key `hm.records.auto.<scenario>`，群呼/otp/callbot/坐席各自独立），重挂载从 localStorage 恢复。
  - 验证：Playwright 实测 `/agent-call` 加入卡片——折叠态三段式渲染对齐 Figma、展开态详情卡片仍可用；`tsc -b`/`vite build`/`make sync-web`/`make verify-embed`/`go build` 全绿。


- **统一各页自动刷新（轮询）行为，修后台空转 + 终态不停**：新增 `web/src/hooks/usePolling.ts`（标签页隐藏 `document.hidden` 自动跳过、`enabled` 条件停止、`isVisible` 自定义可见性、fn 用 ref 镜像避免重建定时器）。逐页整改：
  - **总览 / 通话链路列表 / Hermes 回调**：原 `setInterval` 无 `document.hidden` 守卫——切到后台标签页仍每 3–4s 打请求；统一改 `usePolling`，隐藏即停、回前台续。回调页另修「每敲一个筛选字符就重建定时器」（筛选变化只触发一次即时 load，周期轮询单独跑）。
  - **群呼 liveCalls（最关键）**：原轮询**永不停**——即使全部接通/未接通已终态，仍每 3s 拉 200 条 sip-inbound，且不防后台。改为按条件轮询：`有取号 && (未拉到实时态 || 还有等待中) && 未超兜底上限(~5min)`，终态即停 + 隐藏跳过；提示文案随状态切换（进行中「每 3s 刷新」/ 终态「已全部出结果，停止刷新」）。
  - **通话链路详情**：原选中已结束会话仍每 3s 重拉完整 events（含大 raw SIP 报文）；改 `setTimeout` 链式调度 + `updatedAt` 稳定检测（连续 ~5 轮 15s 无新事件即停），隐藏跳过；需要可手动「刷新」。
  - **ScenarioRecords / 软电话进度聚合**：统一/补 `document.hidden` 守卫（ScenarioRecords 经 `usePolling` 的 `isVisible` 复用原 `offsetParent` 判断，保留 auto 开关）。
  - 验证：Playwright 实测总览页——前台稳定 3s/次、模拟 `document.hidden` 后 7s 增量为 0（完全停止）、恢复前台自动续；`tsc -b`/`vite build`/`make sync-web`/`go build` 全绿。

- **Figma 重构稿细节对齐（续）**：① 顶栏机构切换器改展示 **orgName**（缺省回退 orgCode）——`useCurrentOrg` 暴露 `currentName`、TopBar 取用；② **坐席外呼页补齐 Figma 缺失元素**：新增琥珀色**统一接听规则面板**（启用/振铃秒/动作 Segmented/命中概率 + 「应用到全部坐席」一键下发，新加入坐席默认套用 → `CardHandle.setRule` + `SoftphoneCard initialRule`），坐席卡展开态加**连接步骤条**（连接 WS · 登录 · 注册 SIP · 就绪，✓/○）；③ **群呼/OTP/call-bot 三场景页头去掉「播种 mock 客户配置」按钮**（播种入口统一收敛到「客户配置」页，避免重复）。`tsc -b`/`vite build`/`make sync-web`/`go build` 全绿，Playwright 实测机构名展示 + 规则面板/步骤条渲染正确。

- **前端全量按 Figma 重构稿落地新设计系统（14 屏）**：对照 Figma「hermes-mock · 通话测试场景」重构稿重写前端外壳与全部页面，纯前端改动、零后端接口变更。
  - **设计系统**：新增 `web/src/constants/theme.ts`（slate/blue 色板单一来源 + Antd 5 `ConfigProvider` token：主色 `#2563eb`、圆角 8/12、Inter 字体、Layout/Card/Table/Button 等组件 token）；`index.css` 重写为 CSS 变量色板 + 应用外壳/页头/信息条/统计卡/结论横幅/通话明细行样式。`main.tsx` 接入 `antdTheme`。
  - **应用外壳**（替代旧 antd `Layout`+`Menu`）：`components/layout/` 新增 `Sidebar`（深色 `#0f172a` 自绘侧栏：品牌 LogoMark + 三组导航「控制台/通话测试场景/观测」+ 蓝底 active）、`TopBar`（面包屑「分组 / 当前页」+ 机构切换器绿点下拉 + 头像）、`nav.tsx`（导航模型单一来源 + 面包屑映射）、`useCurrentOrg`（topbar 机构切换 + 事件总线同步）、`PageHeader`/`StatusPill`、`InfoBanner`。`App.tsx` 改用新外壳，路由不变。
  - **页面**：总览改 StatCard 行 + 双表（彩点状态）；机构/客户配置/坐席/通话链路/Hermes 回调统一 PageHeader + InfoBanner；**场景页（群呼/OTP/call-bot）配置表单移入右侧抽屉 Drawer**（页头「＋ 新建任务」打开，footer 取消/创建），主区改全宽 ResultBanner（语义色对齐新色板）+ 通话明细行（CallRow 改彩点 StatusTag + 双腿 LegView + 证据格）+ 历史记录；`ScenarioHeader` 重写为 PageHeader+InfoBanner；坐席外呼（AgentSoftphone）顶部换 PageHeader+InfoBanner。
  - **验证**：`tsc -b`、`vite build`、`make sync-web`/`verify-embed`、`go build ./...` 全绿（eslint 因本机缺可执行文件未跑）；Playwright 1440×900 逐屏核对 总览/群呼(+抽屉)/坐席外呼/客户配置/call-bot/Hermes 回调/坐席/机构/通话链路，视觉与设计稿一致。

## 2026-06-12

- **`/tests/callcenter-task` 由同步长轮询改为「建任务即返回」**：原 `RunCallCenterTaskObserved` 建任务后在 HTTP 内同步阻塞 `waitAnyLegInviteOK(WaitSec=90s)` + 转坐席 `waitSeatTransferAnswered(12s)`，群呼是预测式分钟级调度故接口最坏挂 ~102s。改为建任务+启动后立即返回 + 当前即时快照（`callViewsForCustomers` 非阻塞查此刻已进来的腿）；客户腿/坐席腿进展由前端 `GroupCallPage` 已有的 `liveCalls`（每 3s 轮询）实时补。删孤儿 `waitSeatTransferAnswered`、`CallCenterTaskParams.WaitSec` 默认 90s（字段保留兼容前端）；前端 toast 文案改「已创建并自动拨号」。`go build`/`go test` 全绿。
- **整理 hermes-ws 工作台消息处理（参考 org-management-temp）**：对照真实工作台前端确认 mock `controller.ts` 分发层本已对齐，真正问题是 `AgentSoftphone.tsx` 把 `groupCallNotify`/`callbackInfo`/`otherEvent` 全置空 no-op、hermes-ws 推的非状态消息被静默丢弃。整改：① controller 抽 `WsAction` 常量、`onmessage` 的 if 链重构为 `switch`、新增 `callLinkInfo` 专用回调接 `currentCallUuid`（hermes 下发本通业务 callUuid/callType/客户号）、显式列出 voicemailNotify/callTrace/incomingRouting/warpUpTimeNotify 等已知 action（统一透传 otherEvent，备扩展）；② AgentSoftphone 接住 `groupCallNotify`（群呼进度）+ `callLinkInfo`（hermes callUuid，联调可见、为关联断言铺路）进卡片日志，`numberInfo`/`otherEvent` 噪音降到 `console.debug`（不刷前端 UI）。避开了 org 自身的 incomingRouting bug。前端因本机无 node_modules 未跑 tsc。
- **批量坐席上线优化**：① `getSipWebrtcAddr` 改 single-flight 缓存（N 坐席共享一次 `/agent/webrtc/addr` 请求/结果，机构切换经 `resetSipWebrtcAddrCache` 失效）；② 前端 `markSipReady` 保活定时器从固定 `setInterval(30s)` 改自调度 `setTimeout` + 随机首相位，避免多坐席同秒爆发；③ 反代 `/public/auth/sip` 保活心跳成功降 Debug（失败仍 Warn/Error 不隐藏）。

- **修复「cluster 页绑了端口却不按绑定行为处理」+ 加诊断**（详见 [DECISIONS.md](DECISIONS.md) 2026-06-12）：
  - 根因①**静默回退**：`sipagent.resolveRule` 端口解析返回 nil（绑定禁用/组不存在/行为档缺失）时静默 `ResolveByNumber`，可能命中**别的号段组**的行为，且无日志。根因②**死绑定**：`UpsertBinding` 不校验端口是否在实际 SIP 监听端口（`SIP_LISTEN_PORTS` 默认仅 5060）内，绑了非监听端口永不收到来话。
  - **端口绑定权威化**：入口端口有启用绑定 → 只按绑定客户组(+个例)解析；组/行为档缺失则默认兜底 + WARN，不再串到别的组；端口无绑定才回退按号。
  - **可见性**：`resolveRule` 每通记录解析来源（`port-binding`/`number`，未命中/缺失 WARN 带排查提示）；新增 `Store.HasBinding/BoundPorts`。
  - **启动期对账**：`warnBindingPortMismatch` 比对绑定端口↔监听端口，警告死绑定 / 提示未绑定监听口。
  - **解析预览诊断**：`/cluster/resolve` 返回 `source`+`note`（端口未绑定→回退按号 / 已绑定但组缺失 / 端口不在监听端口→死绑定），cluster 页预览直接可见。
  - 验证：`go build`/`go vet`/`go test ./internal/{cluster,sipagent,api}` 全绿（新增 `TestHasBindingAndBoundPorts`）；`tsc` 通过。**端到端待本地栈**。
- **群呼坐席分配下拉就绪标记 + TTS 名称展示加固 + 期望WS坐席说明**（群呼表单后续打磨）：
  - **坐席号下拉「就绪」标记 + 排序**：新增跨页轻量 store `hooks/useReadyAgents`（`useSyncExternalStore`），常驻单例 `AgentSoftphone` 把 `sipReady` 的坐席号广播出去；`GroupCallPage` 坐席分配下拉精简为「号码 · 姓名」、用 `optionRender` 加「就绪/未就绪」Tag、**已就绪默认排前**（就绪=坐席已在「坐席外呼」页软电话上线，才能被群呼转接接听）。
  - **TTS 名称展示加固**：对照 Hermes basic `TtsVoiceDTO`（`code`/`name`/`displayName`），后端 `ListTts` 名称候选补 `displayName`/`description`、lang 候选补 `locale`/`countryCode`；前端 TTS code 已由 AutoComplete 改 Select（下拉 + 选中态均显示 `code · 名称`）。
  - **「期望WS坐席」澄清**：是 mock 侧断言参数 `observeAgent`（不进 Hermes 请求体）——填坐席号则额外断言其工作台 WS 收到来电/调度通知，留空只断客户腿；label/tooltip 已写明。
  - 验证：`gofmt`、`go build ./...`、`go test ./internal/{api,testkit,orchestrator}` 绿；`tsc -b`、`vite build`、`make sync-web` 通过。
- **群呼任务参数补全 + 模式策略组合校验（后端+前端）**（对照 Hermes `AddCallTaskAndImportNumberReq` 全字段 + `CallTaskService.validateAddRequest`）：
  - **参数透传重构**：`BizCaller.CallCenterTask` 由 13 个位置参数的长签名收敛为单一 `entity.CallCenterTaskReq` 结构体（entity 叶子包，无 import cycle）；`orchestrator.CallCenterTaskScenario` 改为该结构体别名（顺带删掉早已不用的 `ObserveAgent/WaitSec/AutoStart` 死字段）；`testkit.CallCenterTaskParams.toReq()` 装配，两处调用点同步。
  - **模式策略组合**：`modeStrategy=1(比例)` → 下发 `proportion(1-10)`；`modeStrategy=2(PID)` → 下发 `lossRate(0-99)+historicalConnectionRate(1-100)`（互斥，按 Hermes 校验分支）。补齐 `sortMethod`、`isPriorityTask`、`isVmHangup`、`maxRedialTimes`、`redialInterval`、`bestRingDuration(默认40)`、`agentMaxRingDuration`、`assignDelaySeconds`、`transferType`、`description`——可选项仅在有值时入 body，缺省走 Hermes 默认。
  - **前端表单**（`GroupCallPage`）：① **删除「自动启动」开关**（createAndImport 即自动拨号，开关无效）；② **TTS code 由 AutoComplete 改 Select**（下拉展示 `code · 名称`，选后仍显示名称）；③ lineType 维持 AutoComplete（支持自由输入自定义 type）；④ 新增「模式策略」下拉 + 条件渲染（=1 显示比例 / =2 显示呼损率+历史接通率，均必填）；⑤ 新增「高级参数」折叠面板（排序方式 / 转接类型 / 分配延迟 / 重拨次数·间隔 / 最佳响铃 / 坐席最大响铃 / 优先任务 / 语音信箱即挂 / 任务描述）。`api.ts` 的 `runCallCenterTask` 参数类型同步扩充，删 `autoStart`。
  - 验证：`gofmt`、`go build ./...`、`go test`（除 hermesopenapi 沙箱）全绿；`tsc -b`、`vite build`、`make sync-web` 通过。**端到端实测待本地栈**。

## 2026-06-11

- **群呼任务参数对照 Hermes `AddCallTaskAndImportNumberReq` 修正（后端+前端）**：① **移除多余的「组织码」字段**——orgCode 不在请求体，Hermes 经凭据头 `ORG_CODE` 取机构；前端表单「组织码」改为只读显示当前机构。② **坐席分配改二选一**——`agentNumbers`（坐席号列表，max 500）或 `agentGroupCodes`（技能组，Hermes `@Size(max=1,min=1)` 仅接受 1 个）；原 orchestrator 写死 `agentNumbers:[]` + 多选技能组会触发 Hermes Size 校验失败，已修：body 二选一、技能组只取首个；testkit `CallCenterTaskParams`+`BizCaller` 加 `agentNumbers`。③ **技能组展示真名**——`/orgs/agent-groups` 优先调 basic `GET /api/agentGroup/getAgentGroupsByOrgCode/{orgCode}` 拿 `{code,name,count}`，失败降级为坐席聚合（仅 code+count）；前端技能组选项显示 `名称（code·N 坐席）`。前端 `GroupCallPage` 加坐席分配 Radio（技能组单选 / 坐席号多选，数据源 `listManagedAgents`）。`go build`/`go test`（除 hermesopenapi 沙箱）全绿。
- **群呼任务：移除多余的 `status/start` 调用 + 补暂停/取消/查状态（后端+前端全打通）**（对照 Hermes `TaskOpenApiController`/`CallTaskService`）：`createAndImport` 后 Hermes 即 `NotifyDialJob` 自动拨号、状态按日期判为 `IN_PROGRESS`，`status/start` 仅用于恢复 `PAUSE` 任务——原 `RunCallCenterTask` 默认调 start 再吞 `Task status is incorrect` 是无意义调用，已移除（`AutoStart` 字段保留但不再触发，前端开关 tooltip 标注已无效）。新增 `StopCallCenterTask`/`CancelCallCenterTask`/`GetCallCenterTaskStatus` 客户端方法 + orchestrator 包装 + 路由 `POST/GET /tests/callcenter-task/:taskCode/{pause,resume,cancel,status}`（`api.Deps` 增 `Orch`）。**打通数据链路**：testkit `RunCallCenterTaskObserved` 用 `parseTaskCode(out)` 提取 Hermes taskCode 塞进 `Run.Artifacts["taskCode"]`；前端 `api.ts` 加 `pause/resume/cancel/getStatus` 请求函数，`GroupCallPage` 结果区加 taskCode 展示 + 暂停/恢复/取消/查状态按钮。`go build`/`go test`（除 hermesopenapi 沙箱）全绿。
- 新增 `docs/CALL-FLOWS.md`：各通话场景端到端流程（群呼/callbot/otp/坐席外呼/接来电/回调）+ 流程图 + 数据结构与样例；扩充 trace_leg events 样例（含各 channel/故障注入/IVR/RTP 统计）、补清「群呼为何落两条 mock_call（两视角，预测式拨号无法发起即对齐 call_uuid）」、坐席转接可接听及其前提。
- **修正坐席记录 `call_uuid` 前缀按腿区分**（对照 Hermes 源码 `BizType.kt`/`TransferService.kt:366-368`）：`saveCallRecord`（仅坐席软电话调用）原对 inbound/outbound 一律写 `CCMDL+in.CallID`。但 Hermes 里 `CCMDL`=坐席手动外呼、`CCINC`=呼入、`CCTSK`=群呼，各 5 位前缀；坐席接来电时前端 `in.CallID` 取自 `x-session-id` 头**已含正确前缀**，再叠 `CCMDL` 会成 `CCMDLCCINC{uuid}` 双前缀且对不上被叫腿。改为：坐席外呼补 `CCMDL`（前端裸 uuid），坐席接来电直接用 `in.CallID`。`go build`/`go test ./internal/api` 绿。
- **修正 `BizUUIDFromHeaders` 提取优先级**（审查坐席外呼关联链路时发现）：旧优先级把 `x-jcallid` 排在 `x-session-id` 之前，但 Hermes 真实链路里 `x-session-id`(=`CCMDL{uuid}`) 才是所有被叫场景的通用 callUuid 载体（`EslEventConstant.SID`），而 `X-JCallId` 在坐席外呼传的是 businessId。改为「真 callUuid + x-session-id 同权优先 → x-jcallid 最末兜底」。两个优先级测试改为验证新语义。
- **DB 模型重构（阶段0–3）**：根除「一通电话散成 3 条 call_record + 2 个 bus session」的核心病，确立 `call_uuid` 为唯一跨场景聚合锚，配置/观测分离，trace 单腿化。
  - 阶段0 止血：`customer_override` 改复合唯一 `(group_code,number)`（同号可跨组）；全部 upsert 改 `clause.OnConflict` 消「先查后写」竞态；配置实体补 `gmt_create/gmt_modified`；补齐实体 `uniqueIndex` tag 并全库唯一命名（sqlite 索引名全局唯一约束：`uk_behavior_code`/`uk_group_code`）；DDL 与实体对齐（删死表 `mock_test_case`、org_config 补 4 列、callback 索引名、override 复合唯一），头部声明「实体 tag 为 schema 权威、DDL 为快照」。
  - 阶段1 砍双写：删 `cluster/call_record_trace.go`（`CallRecordFromTraceSession` 从 SIP 反推、拆 agent/customer 腿——越界且是病根）；`traceFlushLoop` 只落 trace；`calltrace.Tracker.Start(callUUID,…)` 改用被叫腿真实 `call_uuid` 作 record_id 主键 → INVITE 重传幂等合并到一行，与 trace 同 call_uuid。
  - 阶段2 拆表：`mock_call_record`→`mock_call`（聚合根，删 4 个已死的 SIP 观测列 sip_call_id/signal/media/callback_summary，加 expect_outcome；`SaveCallRecord` upsert 键纯 record_id）；`mock_trace_session`→`mock_trace_leg`（**写入严格单腿**：加 leg_role/line，删 legs）；新增 `ListTraceLegsByCallUUID`，「一通含多腿」由 api 读时按 call_uuid 归并（纯展示装配，不写回、守 SCOPE）；DDL 全量同步；前端 `CallRecord` 的 4 个观测字段改可选（条件渲染，零逻辑破坏）。老观测表 DROP 重建，配置 5 表数据保留。
  - 阶段3 治理：`OBSERVE_TTL_DAYS`（默认 7）+ `PruneObservations` + main `pruneLoop` 周期清理观测表防膨胀；凭据暂不加密（内网测试，仅注释）。坐席 jssip 外呼已注 `x-session-id: CCMDL{callId}`，与坐席侧 `CCMDL+callID` 天然同 call_uuid（待实测 FS bridge 透传）。
  - 验证：`go build ./...`、`go test $(go list ./... | grep -v hermesopenapi)` 全绿（新增 `TestTraceLegsByCallUUID`/`TestPruneObservations`，改造 `TestCallRecordSaveMergeAndList`/`TestTraceSessionRoundTrip`）；迁移冒烟确认 10 张新表建出、旧表名消失；`go vet` 干净。`hermesopenapi` 因沙箱禁 httptest 绑端口失败（环境所致，无关）。**DB/前端端到端实测待本地栈**。
- 降低 `/trace/sessions` 轮询开销：列表接口瘦身为**摘要**（不含 events，新增 `eventCount`），events 改走 `/trace/sessions/:id` 单查；坐席软电话用 `?match=<jssip callId>` 让服务端按整 session 子串匹配回完整单条，不再每卡每轮拉全量列表再前端 `find`。前端轮询加可见性守卫（标签页隐藏 / 常驻软电话切到别页 `display:none` 时跳过）、非终态轮询设上限、周期放宽（CallTracePage 2s→3s、坐席卡 1.5s→2.5s）；`hermesOverview` 的 trace.sessions 同步改摘要。验证：`go build ./...`、`go test ./internal/{api,tracelog}`、`tsc -b`、`vite build`、`make sync-web` 通过；`eslint` 因本机缺可执行文件未运行。
- 修复 sipagent/siptrace/tracelog 的并发与逻辑问题（审查后批量修）：
  - `tracelog.Bus` 的 `Sessions()/Session()` 改为返回深拷贝快照，消除 HTTP 读取侧与 SIP/agent `emit` 的跨 goroutine 数据竞争（`c.JSON` 在高并发下可能 panic）。
  - `siptrace.Tracer` 的 `cid2biz/cid2leg` 加 `maxCID` 容量上限淘汰，堵批量压测长跑的内存泄漏。
  - 业务 callUuid 提取抽成 `tracelog.BizUUIDFromHeaders`（call-uuid 族优先于 session-id 族），siptrace 与 sipagent 共用，保证同一通话两路聚合键一致、不分裂。
  - sipagent：接听后写流来源改为**单一互斥分派**（媒体故障 / 发 DTMF / 持续放音三选一），修复「RTP_LOSS/REORDER + 发 DTMF」并发双写同一 AudioWriter 导致 RTP 损坏；IVR 改为只取一次 AudioReader + 单个 Listen（修复每步重复 `AudioReader(WithAudioReaderDTMF)` 覆盖/泄漏拦截器）。
  - 清理/打磨：删未用字段 `Agent.ua`；INVITE 聚合键提取轻量化（不再每通 `req.String()` 构建原始报文）；WAV 解码帧按路径进程级缓存；NO_ANSWER 走默认振铃时长；自定义 SIP 码按码匹配 reason 文案。
  - 验证：`go build ./...`、`go test ./internal/{sipagent,siptrace,tracelog,cluster}`、`go test -race ./internal/{tracelog,siptrace}` 全绿；全量 `go test ./...` 仅 `internal/hermesopenapi` 因沙箱禁止 httptest 绑定本地端口失败（环境所致，与本次改动无关）。
- 优化坐席外呼页（/agent-call）交互与布局：顶部使用说明默认折叠；坐席选择由下拉多选改为表格多选（搜索 + 技能组/状态筛选 + 表头全选/全不选，技能组改为独立列、不再混入下拉文本）；坐席卡片改网格多列并排（折叠态 sm12/lg8、展开态全宽）、默认折叠、单卡链路/日志收进默认收起的 Collapse。验证：`tsc -b`、`vite build` 通过；`eslint` 因本机缺可执行文件未运行。
- 调整 cluster 绑定模型：删除绑定里的 `lineAddress`，改为 `listenPort -> customer_group`；新增 `SIP_LISTEN_PORTS` 支持 mock 多 SIP 端口监听。
- 修正 trace leg 语义：SIP tracer 记住同一 SIP Call-ID 的 INVITE 被叫号，后续 BYE/响应不再因 To/From 翻转误标成主叫号。
- 修正 sipagent 业务事件：FLOW/BRIDGE 使用真实 callee 作为 leg，并将 `customer` 作为角色 detail。
- 优化 trace 前端展示为梯形图，保留真实 SIP 报文展开。
- 修正坐席外呼记录表分页：页码变化会立即触发通话记录重新查询。
- 修正坐席管理页分页：`/agents/managed` 按当前页码和 pageSize 请求后端，不再只拉前 200 条做本地分页。
- 验证：`go test ./internal/siptrace`、`go test ./...`、`npm run build` 通过；`npm run lint` 因本地缺少 `eslint` 可执行文件未运行成功。
