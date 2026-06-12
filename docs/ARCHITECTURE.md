# ARCHITECTURE — 架构 / 目录 / 模块职责

> 以代码为准（`cmd/hermes-mock/main.go` 接线 + 各包注释 + DDL + `web/src/App.tsx` + `go.mod`）。
> **这是描述目录结构的唯一处**；动了目录就更新这里，别在 README/AGENTS 里另写一份。

## 一、架构总览

```
 浏览器 ─▶ React 配置后台（Vite，//go:embed 进单二进制）
              │ REST / 前端 jssip 软电话（坐席外呼）
 ┌────────────▼──────────────── hermes-mock（Go 单二进制）──────────────────┐
 │ api:           Gin HTTP，服务前端 + 客户配置 / 业务测试触发 / 链路观测 REST       │
 │ cluster:       客户集群（行为档+客户组+个例+端口绑定）持久化 hermes_mock 库       │
 │ sipagent(diago): 被叫 UAS——接 FS 的 INVITE，按客户行为应答/放音/DTMF/挂断/故障   │
 │ orchestrator:  经 Hermes OpenAPI 让业务侧发起外呼（call-center/call-bot/otp）   │
 │ siptrace+tracelog: 传输层抓真实 SIP 报文，聚合成链路时间线（周期落库）            │
 │ calltrace/callbacks: 每通被叫 / Hermes 回调 落库（mock_call/mock_callback）│
 │ agents/wsagent: 坐席状态表 + hermes-ws 上线（坐席场景；详见 STATUS 开放问题）     │
 └────────────────────────▲───────────────────────────────────────────────┘
              真实 SIP/RTP │（Hermes 线路 t_line.address 指向 mock）
 ┌────────────────────────┴───────────────────────────────────────────────┐
 │ 被测 Hermes 栈：basic + call-center/call-bot/otp + fs-esl-proxy + FreeSWITCH │
 └─────────────────────────────────────────────────────────────────────────┘
```

## 二、目录结构

```
cmd/
  hermes-mock/main.go    入口：load config → 建 cluster/orgcfg → 装 siptrace → 起 sipagent
                         → 注册 api 路由 → go:embed 前端 → trace 周期落库
  hermes-mock/web/dist/  前端构建产物（make web 同步，go:embed 进二进制）
  probe/main.go          独立 Hermes 栈探测 CLI
internal/                见下表
web/                     React 18 + Vite + Antd 前端（src/pages 页面、src/components/scenario 场景公共组件、
                         src/hooks 共享 hook、src/sip 软电话、src/api.ts）
deploy/                  Dockerfile / deploy.yaml(K8s) / ddl/hermes_mock.sql（mock 自身库 DDL）
                         （deploy/hermes-stack/ 本地真实栈为本地辅助，不进 git）
assets/                  放音素材（默认 hello.wav）
docs/                    本套文档
```

## 三、internal 各包职责（main.go 接线印证）

> 分层：`entity`（PO/DTO 叶子）→ `model`（Repository 接口 + 工厂 + GORM 实现）→ 领域 Store（内存缓存/解析）→ api。
> 业务包不直接持有 *gorm.DB；接线顺序 Config → Logger → Repository(工厂) → 领域 Store → Handlers → Router。

| 包 | 职责 |
|---|---|
| `config` | 环境变量集中声明与解析（HTTP / SIP-RTP / DB；业务接入配置一律在「机构」页，不走 env） |
| `entity` | **持久化对象（PO）+ 共享 DTO**：10 个 GORM 实体（行为档/客户组/个例/端口绑定/MockCall 呼叫记录/测试运行/TraceLeg 单腿链路/链路事件/回调/机构配置）+ Meta/Resolved/过滤器。依赖图叶子。gorm tag 为 schema 权威 |
| `model` | **Repository 接口唯一定义处** + 工厂（DBType=mysql/sqlite，openGormDB 统一连接池，启动 AutoMigrate 全实体）；`model/sql` 子包按表分文件实现（GormRepository） |
| `cluster` | **客户集群领域服务**：四类配置的内存缓存 + `ResolveByNumber/ResolveByLine`（SIP 来话热路径，只读缓存不查库）+ `TakeNumbers` 取号游标；写穿透 Repository |
| `orgcfg` | 机构 OpenAPI 接入配置缓存 + 当前机构选择（gateway 走网关 X-OpenApi-Key / direct 注入 ORG 头）；写穿透 Repository |
| `sipagent` | **diago 被叫 UAS**：接 FS 的 INVITE，按 cluster 解析的行为应答/拒接/振铃/放音/DTMF/IVR/挂断/故障（只演客户被叫腿） |
| `calltrace` | 被叫腿通话生命周期跟踪 → 按 call_uuid 主键经 Repository 落 `mock_call`（INVITE 重传幂等合并一行） |
| `tracelog` | 通话链路事件总线（SIP/媒体/WS 统一时间线）；内存环 + `traceFlushLoop` 周期经 Repository 落 `mock_trace_leg/event`（写入严格单腿，多腿读时按 call_uuid 归并） |
| `siptrace` | sipgo 传输层 tracer：捕获**所有收发的原始 SIP 报文**（含 X- 业务头），按 Call-ID 聚合进 tracelog（须在建 SIP agent 前 Install） |
| `orchestrator` | 经 Hermes 业务 REST/OpenAPI 触发 call-bot/otp/call-center 任务 + 坐席操作 |
| `callbacks` | 接收 Hermes 回调（webhook）→ 经 Repository 落 `mock_callback` |
| `hermesprobe` | Hermes 栈健康探测（HTTP `/state/up`，目标从当前机构配置推导；不直连业务库） |
| `testkit` | 业务测试编排（触发 + 真实 SIP 断言）；`SetBizCaller(orch)` 接 orchestrator，`SetRepo` 落测试历史 |
| `agents` | **坐席状态表**：工作台 WS 在线态 + SIP 注册态 + 工作状态（坐席场景，见 STATUS 开放问题） |
| `wsagent` | **坐席工作台 WS 客户端**：经 hermes-ws 让坐席上线/切状态（地址/口令从当前机构配置动态取） |
| `api` | Gin 路由 + REST + 前端 embed 挂载（`Register` / `MountFrontend`） |

**支撑包**（未在 main 直接接线，被上面的包引用）：
`behavior`（被叫行为类型 Outcome/Fault/IVR/Rule）、
`hermesopenapi`（Hermes OpenAPI 客户端，被 orchestrator/orgcfg 用）、
`bootstrap`（启动播种端口绑定/默认数据）、
`preflight`（场景就绪自检）。

## 四、一通呼叫的数据流

**被叫（核心路径）**：
```
FS ──INVITE──▶ siptrace 抓原始报文 ──▶ sipagent.handleInbound
   └─ cluster 解析：按 INVITE 到达的 mock SIP listen_port 命中 mock_line_binding
        → customer_group → 若被叫号有 customer_override 用个例行为，否则用组 behavior_code
   └─ 按 outcome 应答/拒接/放音/DTMF/IVR/挂断/故障
   └─ calltrace 按 call_uuid 主键写 mock_call（被叫腿，INVITE 重传幂等合并）；
      tracelog 记事件 ──▶ traceFlushLoop 周期落 mock_trace_leg/event（单腿，不反推业务语义）
```

**发起（Hermes 业务侧）**：
```
前端 / testkit ──▶ orchestrator ──（取 orgcfg 当前机构的 OpenAPI 凭据）──▶ Hermes 业务 REST
                                                              （call-bot/otp/call-center 群呼/外呼，选 mock 线路）
坐席外呼：前端 jssip 软电话 ──▶ hermes-ws 登录 + 注册 FS + 发 INVITE ──▶ call-center 选线 bridge ──▶ mock 被叫腿
```

## 五、技术栈与版本锁

| 层 | 选型 |
|---|---|
| 语言 | Go 1.24 |
| SIP | `emiago/sipgo v1.4.0` |
| UAS/SDP/RTP/RFC4733 DTMF/playback | `emiago/diago v0.28.0` |
| WS（坐席工作台客户端） | `gobwas/ws v1.4.0` |
| HTTP | `gin v1.11.0` |
| ORM | `gorm v1.31.0` + MySQL driver |
| 前端 | React 18 + Vite + Ant Design + React Router |
| 媒体限制 | **无 ICE/STUN**——mock 与 FreeSWITCH 须在同一可路由网络 |

## 六、前端页面（web/src/pages，按侧边菜单分组）

| 路由 | 页面 | 作用 |
|---|---|---|
| `/overview` | OverviewPage | 总览：通话统计 + 活跃通话 + 近期链路 |
| `/orgs` | OrgsPage | 机构 OpenAPI 接入凭据，一键切当前测试机构 |
| `/cluster` | ClusterPage | **核心**：行为档 / 客户组 / 个例 / 端口绑定 CRUD |
| `/agents` | AgentsPage | 坐席台账 CRUD（查/建/改/删/启停账号；纯管理，无 SIP 状态/上线） |
| **通话测试场景**（二级菜单，原 CallScenariosPage 已拆分为独立页面） | | |
| `/agent-call` | （App 常驻层 AgentSoftphone） | 坐席外呼：多坐席浏览器 jssip 软电话（选坐席→卡片→连接/外呼/接听规则/通话控制） |
| `/group-call` | GroupCallPage | call-center 群呼任务（左配置右结果 + 内嵌记录） |
| `/callbot` | CallbotPage | call-bot 机器人外呼（子 tab：任务 / 模板自动外呼） |
| `/otp` | OtpPage | OTP 语音验证码批量 |
| `/trace` | CallTracePage | 通话链路：会话列表 + 事件时间线（可展开原始 SIP） |
| `/callbacks` | CallbacksPage | Hermes 回调（webhook）接收与查询 |

> 业务测试场景页共享公共模块：`components/scenario/utils.tsx`（ScenarioSummary/CallBoard/RunSteps/JSONBlock/ReadyLabel + 工具）、`components/scenario/ScenarioHeader.tsx`、`hooks/useScenarioMeta.ts`（机构/客户组/技能组/TTS/端口绑定/preflight 加载 + 派生选项 + 播种）。坐席软电话：`components/AgentSoftphone.tsx` + `sip/{index,controller,request}.ts`（多实例 jssip）。

## 七、持久化（hermes_mock 库，10 张 mock_* 表）

DDL：`deploy/ddl/hermes_mock.sql`（实体 gorm tag 为 schema 权威，DDL 为生成快照）。

**配置域**：`mock_behavior_profile` · `mock_customer_group` · `mock_customer_override`（复合唯一 `group_code+number`）· `mock_line_binding`（`listen_port→组`）· `mock_org_config`

**记录域**（`call_uuid` 为聚合锚）：
- `mock_call`（**聚合根**，一通电话 1 行）：发起面(scenario/source/org/task/run/customer/agent/line/expect_outcome) + 结果面(status/result/hangup/时间/duration)。被叫腿 `record_id == call_uuid`（INVITE 重传幂等合并）。
- `mock_trace_leg`（**写入侧严格单腿**：`session_id`=单腿键 + `call_uuid` 关联 + `leg_role`/`line`）；「一通含多腿」由 api 读时按 `call_uuid` 归并（纯展示装配，不写回、不跨腿聚合）。
- `mock_trace_event`（单腿时间线 + 原始 SIP 报文）
- `mock_callback`（Hermes webhook，按 `call_uuid` 关联）· `mock_test_run`（测试运行历史）

观测域（call/trace/callback）按 `OBSERVE_TTL_DAYS`（默认 7）周期清理防膨胀；配置域不清理。
