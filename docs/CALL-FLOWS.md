# 通话流程详解（各场景端到端 + 数据结构）

> 本文梳理 hermes-mock 在各种通话场景下的**完整数据流、关键步骤、数据结构与样例**。
> 以代码为准（`cmd/hermes-mock/main.go` 接线 + `internal/*` 各包 + `deploy/ddl/hermes_mock.sql`）。
> 配套阅读：[ARCHITECTURE.md](ARCHITECTURE.md)（架构/目录）、[hermes/agent-outbound-call.md](hermes/agent-outbound-call.md)（被测系统坐席外呼链路）。
>
> **一句话定位**：mock 永远只演「被叫客户腿」，被动接 INVITE 按行为档应答；发起方永远是 Hermes 业务侧或前端 jssip 坐席软电话。

---

## 0. 全局组件与数据落点速查

```
                         ┌─────────────── hermes-mock（Go 单二进制）──────────────┐
 浏览器 React 后台 ──REST─▶│ api(Gin)                                              │
 前端 jssip 软电话 ─┐      │   ├─ testkit      触发业务场景 + 真实链路断言           │
                   │      │   ├─ orchestrator 经 Hermes OpenAPI 发起（群呼/callbot/otp）│
                   │      │   ├─ cluster       客户集群解析（号段组+个例+端口绑定）   │
                   │      │   ├─ calltrace     被叫腿生命周期 → mock_call            │
                   │      │   ├─ tracelog.Bus  链路事件总线（内存环）→ mock_trace_*  │
                   │      │   └─ callbacks      Hermes webhook → mock_callback       │
                   │      │ sipagent(diago UAS) 被叫腿：接 INVITE 按行为应答           │
                   │      │ siptrace(sipgo)     传输层抓真实 SIP 报文 → tracelog      │
                   │      └────────▲──────────────────────────────────────────────┘
                   │   真实 SIP/RTP │（Hermes 线路 address 指向 mock）
        wss(WebRTC)│      ┌────────┴──────────────────────────────────────────────┐
                   └─────▶│ 被测 Hermes 栈：basic + call-center/call-bot/otp        │
                          │                 + fs-esl-proxy + FreeSWITCH             │
                          └─────────────────────────────────────────────────────────┘
```

| 数据落点 | 表 | 写入者 | 主键/聚合键 |
|---|---|---|---|
| 配置 | `mock_behavior_profile` / `mock_customer_group` / `mock_customer_override` / `mock_line_binding` | `cluster.Store`（写穿透 + 内存缓存） | code / (group_code,number) / listen_port |
| 机构凭据 | `mock_org_config` | `orgcfg.Store` | org_code |
| 通话记录（聚合根） | `mock_call` | `calltrace.Tracker`（被叫腿）/ `testkit`（发起）/ `api.saveCallRecord`（坐席软电话） | `record_id`；关联锚 `call_uuid` |
| 链路（单腿） | `mock_trace_leg` | `traceFlushLoop`（5s 周期，源 `tracelog.Bus`） | `session_id`；关联锚 `call_uuid` |
| 链路事件 | `mock_trace_event` | 同上 | `session_id + seq` |
| 回调 | `mock_callback` | `callbacks.Store` | 关联锚 `call_uuid` |
| 测试运行历史 | `mock_test_run` | `testkit.Kit` | run_id |

> **核心关联锚 `call_uuid`**：被叫腿从 INVITE 的 `x-session-id` 头（Hermes 业务 sessionId，格式 `<5位BizType前缀><32位uuid>`）由 `tracelog.BizUUIDFromHeaders` 提取。一通业务通话的 `mock_call` / N 条 `mock_trace_leg` / `mock_callback` 全靠它串联。BizType 前缀语义（见 Hermes `BizType.kt`）：

| 前缀 | 场景 |
|---|---|
| `CCMDL` | 坐席手动外呼 |
| `CCINC` | 呼入 / 转接坐席 |
| `CCTSK` | 群呼任务 |
| `CBTSK` / `CBRNT` / `CBCTM` | Call Bot 任务 / 实时通知 / 自定义通知 |
| `OTPVC` | OTP 语音验证码 |

---

## 1. 公共子流程：被叫腿（mock UAS 接 INVITE）

这是**所有场景共用的核心**：无论群呼、callbot、otp 还是坐席外呼，最终打到 mock 的都是一条 `sofia/external/{被叫}@{网关}` 的普通 SIP INVITE。mock 的处理逻辑完全一致（`sipagent/agent.go: handleInbound`）。

### 1.1 行为解析链（INVITE 到达 → 决定怎么应答）

```
INVITE 到达 mock listen_port
   │
   ▼ resolveRule(callee)                              [agent.go:77]
   │   ① ResolveByPort(listenPort, callee)            [cluster/store.go:164]
   │      listen_port ──▶ mock_line_binding ──▶ group_code
   │   ② group 命中后：若被叫号有 customer_override 用个例行为，否则用组 behavior_code
   │   ③ ResolveByNumber(callee) 兜底（无端口绑定时按号段直接命中组）
   │   ④ 都未命中 ──▶ defaultRule()（应答 + 默认放音）
   │
   ▼ clusterToRule(resolved)                          [agent.go:106]
   │   • Disabled(组/个例 state=DISABLED) ──▶ 强制 Outcome=UNAVAILABLE, code=503
   │   • Outcome=ANSWER 且 RollAnswer(answerRatio)=false ──▶ 降级 NO_ANSWER（模拟接通率）
   │   • Outcome=BRIDGE ──▶ 降级 ANSWER（mock 只演客户腿）
   │
   ▼ behavior.Rule（最终生效行为）
```

**关键数据结构 `cluster.Resolved`**（解析输出）：
```go
type Resolved struct {
    GroupCode string           // 命中的客户组 code
    Number    string           // 被叫号
    Profile   *BehaviorProfile // 有效行为档（组默认或个例覆盖）
    Disabled  bool             // 组/个例 state==DISABLED
}
```

**关键数据结构 `behavior.Rule`**（sipagent 据此应答）：
```go
type Rule struct {
    Outcome    Outcome   // ANSWER/REJECT/BUSY/NO_ANSWER/UNAVAILABLE
    RingMs     int       // 振铃时长
    TalkMs     int       // 接听后通话时长（到点挂断）
    HangupCode int       // 拒接/不可用 SIP 码（486/503/480）
    Playback   string    // 接听后放音文件
    DTMF       string    // 接听后发送 DTMF 序列（OTP/IVR 选择）
    ExpectDTMF bool       // 接听后监听对端按键（IVR 交互观测）
    Fault      Fault     // 故障注入
    IVR        []IVRStep // 脚本化 IVR（放音→收键→分支，多轮）
}
```

### 1.2 被叫腿状态机（handleInbound）

```
                          ┌─ REJECT/BUSY ──▶ 486 ──────────────────┐
                          ├─ UNAVAILABLE ──▶ 503 ──────────────────┤
 INVITE ─▶ Tracker.Start ─┤                                        ├─▶ tracker.Rejected(code)
 (aggKey=call_uuid)       ├─ NO_ANSWER ──▶ 180 振铃 → 480 ─────────┤
                          │                                        │
                          └─ ANSWER 分支 ▼                         │
                              ├─ Fault=NO_RESPONSE ─▶ 不响应,等超时 ─┘ (Rejected 408)
                              ├─ Fault=SLOW_ANSWER ─▶ 180 → 拖延 → 200
                              ├─ RingMs>0 ─▶ 180 振铃
                              ▼
                          answer() 200 OK ─▶ Tracker.Answered ─▶ emitCodec(媒体协商)
                              ├─ Fault=ANSWER_DROP ─▶ 立即 BYE ─▶ Tracker.Ended
                              ├─ IVR 非空 ─▶ runIVR(放音→收键→分支) ─▶ BYE ─▶ Ended
                              ▼ 接听后媒体动作（写流至多一个，互斥）：
                              │   RTP_LOSS / RTP_REORDER / NO_RTP / ONE_WAY_AUDIO（媒体故障）
                              │   或 DTMF!="" ─▶ sendDTMF（OTP/IVR 选择）
                              │   或 常态 ─▶ streamAudio（持续放音，FS 可录音）
                              │   读流（可并存）：ExpectDTMF ─▶ recvDTMF（观测对端按键）
                              ▼ sleep(TalkMs) ─▶ emitRTPStats（采样收发包）
                              ├─ Fault=HALF_HANGUP ─▶ 本侧永不发 BYE,持有到对端挂
                              ▼
                          hangup() BYE ─▶ Tracker.Ended
```

每个决策点都向 `tracelog.Bus` emit 一条 `ChanFlow` 事件（"命中"/"决策"/"应答"/"故障注入"/"放音"/"挂断"），与 siptrace 抓到的真实 SIP 报文（`ChanSIP`）同会话。

### 1.3 被叫腿落库（两条独立写入）

```
sipagent.handleInbound
   │
   ├─▶ calltrace.Tracker（按 call_uuid 主键写 mock_call）
   │      Start  ─▶ RecordID=call_uuid, scenario=sip-inbound, status=RINGING
   │      Answered ─▶ status=ANSWERED, answered_at
   │      Rejected ─▶ status=REJECTED, hangup_code, ended_at
   │      Ended    ─▶ status=ENDED, ended_at
   │      （同 call_uuid 多次 SaveCallRecord 幂等合并到一行；状态只进不退）
   │
   └─▶ tracelog.Bus（内存环）── traceFlushLoop(5s) ──▶ mock_trace_leg + mock_trace_event
          SessionID=call_uuid, LegRole=customer, Line=线路名
```

**`mock_call` 行样例（被叫腿，群呼场景）**：
```json
{
  "recordId": "CCTSK0196ed1e3e4f765794c4e66014c5c68e",
  "scenario": "sip-inbound",
  "source": "sip",
  "customerNumber": "8613800138000",
  "callUuid": "CCTSK0196ed1e3e4f765794c4e66014c5c68e",
  "status": "ENDED",
  "result": "ANSWER",
  "startedAt": "2026-06-11T15:13:21.003Z",
  "answeredAt": "2026-06-11T15:13:23.115Z",
  "endedAt": "2026-06-11T15:13:31.220Z",
  "durationMs": 8105,
  "detailJson": "{\"caller\":\"02150000\"}"
}
```

**`mock_trace_leg` + 事件样例**（同一通话，按 call_uuid 关联；events 是 `tracelog.Bus` 内一通话的完整时间线，混合 siptrace 抓的真实 SIP 报文 `ChanSIP` 与 sipagent 的业务决策 `ChanFlow`、媒体观测 `ChanBridge`）：

`TraceEvent` 字段结构：
```go
type TraceEvent struct {
    Seq         int64     // 会话内自增序号
    TS          time.Time // 事件时刻
    Leg         string    // 腿标识：客户号 / agent:分机 / ""
    Channel     string    // SIP / FLOW / BRIDGE / WS / ESL
    Dir         string    // IN / OUT / -（相对 mock）
    Method      string    // INVITE/180/200/BYE（SIP）或 命中/决策/应答/挂断（FLOW）
    Summary     string    // 人类可读摘要
    HeadersJSON string    // 结构化 SIP 头（含 X- 业务头），仅 ChanSIP
    RawMessage  string    // 原始 SIP 报文 req.String()，仅 ChanSIP
}
```

**(A) 正常接听（ANSWER + 持续放音）的完整 events**：
```json
{
  "sessionId": "CCTSK0196ed1e3e4f765794c4e66014c5c68e",
  "callUuid": "CCTSK0196ed1e3e4f765794c4e66014c5c68e",
  "legRole": "customer", "line": "mock-cb-cn", "kind": "call",
  "title": "呼入 02150000 → 8613800138000 (8613800138000)",
  "events": [
    {"seq":1,"channel":"SIP","dir":"IN","method":"INVITE","leg":"8613800138000",
     "summary":"INVITE sip:8613800138000@mock",
     "headers":[{"name":"Call-ID","value":"a1b2-c3d4@fs"},{"name":"x-session-id","value":"CCTSK0196ed1e3e4f765794c4e66014c5c68e"},{"name":"x-call_center_type","value":"GROUP_CALL"},{"name":"X-Line-Name","value":"MOCK-CB-CN"},{"name":"From","value":"<sip:02150000@fs>"},{"name":"To","value":"<sip:8613800138000@mock>"}],
     "raw":"INVITE sip:8613800138000@mock SIP/2.0\r\nVia: ...\r\nx-session-id: CCTSK0196...\r\n..."},
    {"seq":2,"channel":"FLOW","dir":"-","method":"命中","leg":"8613800138000",
     "summary":"被叫=8613800138000 主叫=02150000 入口端口=5060 规则=ANSWER",
     "detail":{"role":"customer","listenPort":"5060","line":"MOCK-CB-CN"}},
    {"seq":3,"channel":"SIP","dir":"OUT","method":"180","summary":"180 Ringing"},
    {"seq":4,"channel":"FLOW","dir":"-","method":"决策","summary":"振铃"},
    {"seq":5,"channel":"SIP","dir":"OUT","method":"200","summary":"200 OK",
     "headers":[{"name":"CSeq","value":"1 INVITE"},{"name":"Contact","value":"<sip:mock@10.0.0.5:5060>"}]},
    {"seq":6,"channel":"SIP","dir":"IN","method":"ACK"},
    {"seq":7,"channel":"FLOW","dir":"-","method":"应答","summary":"决定应答（媒体建立）"},
    {"seq":8,"channel":"BRIDGE","dir":"-","method":"媒体协商",
     "summary":"编解码 PCMU（pt=0 rate=8000）","detail":{"codec":"PCMU","payloadType":"0","sampleRate":"8000"}},
    {"seq":9,"channel":"BRIDGE","dir":"-","method":"放音","summary":"持续发送音频（hello.wav），对端可听"},
    {"seq":10,"channel":"BRIDGE","dir":"-","method":"媒体统计",
     "summary":"RTP 收 412 包/65920 字节，发 400 包/64000 字节（双向）","detail":{"rxPackets":"412","txPackets":"400","twoWay":"true"}},
    {"seq":11,"channel":"SIP","dir":"IN","method":"BYE","summary":"BYE（对端挂断）"},
    {"seq":12,"channel":"FLOW","dir":"-","method":"挂断","summary":"通话时长到，挂断"}
  ]
}
```

**(B) 拒接（BUSY 486）的 events**——状态机走拒接分支，无媒体：
```json
"events": [
  {"seq":1,"channel":"SIP","dir":"IN","method":"INVITE","headers":[{"name":"x-session-id","value":"CCTSK0196..."}]},
  {"seq":2,"channel":"FLOW","dir":"-","method":"命中","summary":"被叫=... 规则=BUSY"},
  {"seq":3,"channel":"SIP","dir":"OUT","method":"486","summary":"486 Busy Here"},
  {"seq":4,"channel":"FLOW","dir":"-","method":"决策","summary":"拒接(Busy Here) 码=486"}
]
```

**(C) 故障注入 / IVR 的额外 FLOW 事件**（接在 seq 7 应答之后）——每种分支 emit 一条 `故障注入`/`IVR` 事件：
| 行为 | 额外事件（channel=FLOW/BRIDGE） |
|---|---|
| `NO_RESPONSE` | `故障注入: NO_RESPONSE：收到 INVITE 不响应（压 SIP 超时/408）`（无 200，对端最终 CANCEL/超时）|
| `SLOW_ANSWER` | `故障注入: SLOW_ANSWER：180 后延迟 12000ms 才 200` |
| `ANSWER_DROP` | `故障注入: ANSWER_DROP：应答后立即挂断` → 紧接 BYE |
| `RTP_LOSS` | `故障注入: RTP_LOSS：发媒体但丢 30% 帧`；媒体统计 twoWay=true 但丢包 |
| `NO_RTP` | `故障注入: NO_RTP：接听后不发媒体`；媒体统计 txPackets=0 |
| `ONE_WAY_AUDIO` | `故障注入: ONE_WAY_AUDIO：只收不发`；媒体统计 twoWay=false |
| `HALF_HANGUP` | `故障注入: HALF_HANGUP：本侧不发 BYE`（持有到对端挂或超时）|
| IVR 脚本 | 每步一条 `IVR: 放音 welcome.wav → 等待按键`、收到按键 `IVR: 收到 DTMF=1 → 跳转 confirm`，结束 `挂断: IVR 脚本结束` |

> 完整事件类型见 `sipagent/agent.go: handleInbound` 各 `a.bus.Emit(...)` 调用点；真实 SIP 报文由 `siptrace` 传输层抓取（每条收发的 INVITE/18x/2xx/ACK/BYE/CANCEL 都带完整 headers + raw）。

---

## 2. 场景一：call-center 群呼任务（预测式拨号）

**触发方**：testkit / 前端群呼页 → orchestrator → Hermes call-center OpenAPI。
**特点**：Hermes 预测式拨号驱动，FS 经线路批量呼出客户腿到 mock；接通后可转接真实坐席。

### 2.1 端到端时序

```
前端群呼页 / testkit.RunCallCenterTaskObserved
   │ ① bus.OpenSession("test", "群呼任务 {name}")  → r.TraceID=sess（编排会话）
   │
   ▼ orchestrator.CallCenterTask(...)                         [orchestrator.go:126]
   │   POST {call-center}/openapi/task/createAndImport         建任务+导号 → **即自动拨号**
   │   请求体：{name, modeStrategy, proportion, ttsCode, ttsText, sortMethod,
   │           startDate, endDate, dialTimePeriod, numbers:[{number}], lineType,
   │           坐席分配二选一：agentNumbers[] 或 agentGroupCodes[1个]}
   │   ※ orgCode 不进请求体——Hermes 经凭据头 ORG_CODE 取机构（当前机构）。
   │   ※ 坐席分配二选一（对照 AddCallTaskAndImportNumberReq）：
   │       agentNumbers（坐席号列表，@Size max 500）或 agentGroupCodes（技能组，@Size(max=1,min=1) 仅 1 个）。
   │   ※ 不再调 status/start——createAndImport 后 Hermes 即 NotifyDialJob 拨号，
   │     任务状态按日期判定为 IN_PROGRESS；status/start 仅用于恢复 PAUSE 态任务
   │     （证据：Hermes TaskOpenApiController + CallTaskService.startTask 仅接受 PAUSE）。
   │
   ▼ Hermes call-center 预测式拨号（Quartz 分钟级调度）
   │   FS originate sofia/external/{calleePrefix}{被叫}@{线路网关}
   │   INVITE 带 x-session-id: CCTSK{callUuid}、x-call_center_type:GROUP_CALL
   │
   ▼ mock 被叫腿（§1 公共子流程）应答 ─▶ mock_call(sip-inbound) + mock_trace_leg(customer)
   │
   ▼ testkit 断言：waitAnyLegInviteOK(numbers, waitSec)        [kit.go:830]
   │   轮询 bus.Sessions()，命中某客户号的 INVITE 且最终 200 OK = 客户腿接通
   │
   ▼ （可选）坐席腿断言：waitSeatTransferAnswered(agentGroups)  [kit.go:392]
       群呼接通后 Hermes 转接坐席 → FS bridge 到坐席 jssip 软电话（不经 mock SIP）
       → 前端坐席软电话回存 agent-inbound 记录 → testkit 查 mock_call(scenario=agent-inbound)
   │
   ▼ finish：写 mock_test_run + 每客户号一条 mock_call(scenario=callcenter-task, source=testkit)
```

### 2.2 关键数据结构

**触发入参 `testkit.CallCenterTaskParams`**：
```go
type CallCenterTaskParams struct {
    OrgCode        string
    Name           string
    CustomerGroup  string   // 从客户组取号（与 Numbers 二选一）
    CustomerLimit  int
    Numbers        []string // 客户号（→ mock 客户线路）
    AgentGroups    []string // 转接技能组（与 AgentNumbers 二选一；Hermes 仅取 1 个）
    AgentNumbers   []string // 指定坐席号列表（与 AgentGroups 二选一）
    ObserveAgent   string   // 期望收到工作台 WS 通知的坐席号
    TTSCode        string
    TTSText        string
    Proportion     int
    LineType       string   // 线路类型（仅用该 type 线路选号）
    AutoStart      *bool
    WaitSec        int      // 观测最长秒（默认 90）
}
```

**断言原语 `testkit.legInviteEvidence`**（"真接通"证据，避免撞号误判）：
```go
type legInviteEvidence struct {
    Seen     bool   // 收到 INVITE
    Answered bool   // INVITE 最终 200 OK（才算真接通）
    Failed   bool   // CANCEL / 4xx-6xx / INCOMING_CALL_BARRED
    Session  string
    Detail   string
}
```

**运行结果 `testkit.Run`**：
```go
type Run struct {
    ID         string         // run id（8位）
    Case       string         // "callcenter-task"
    OK         bool
    TraceID    string         // 编排 bus session
    Steps      []Step         // 逐步断言（创建任务/客户腿接通/坐席腿接通）
    Artifacts  map[string]any // numbers/agentGroups/customerGroup/taskName/orgCode...
    Calls      []CallView     // 每客户号一通对话视图
}
```

**落库 `mock_call`（testkit 发起视角）样例**：
```json
{
  "recordId": "trace:{编排sess}",
  "scenario": "callcenter-task",
  "source": "testkit",
  "runId": "a1b2c3d4",
  "orgCode": "org001",
  "taskName": "mock_群呼_xxx",
  "customerGroup": "demo_customers",
  "customerNumber": "8613800138000",
  "agentGroupCode": "skill_01",
  "status": "ANSWERED",
  "callType": "callcenter-task"
}
```

> **为什么一通群呼电话落两条 `mock_call`**：是**同一通电话的两个视角**，靠 `call_uuid` 关联，不是重复：
>
> | | 第①条（被叫腿观测） | 第②条（发起预期+断言） |
> |---|---|---|
> | 写入者 | `calltrace.Tracker`（INVITE 实时落） | `testkit.persistCallRecords`（run 结束落） |
> | `scenario` | `sip-inbound` | `callcenter-task` |
> | `source` | `sip` | `testkit` |
> | `record_id` | `call_uuid`（SIP 业务 sessionId） | `trace:{编排sess}` |
> | 视角 | mock 亲历的客观 SIP 事实（振铃/接听/SIP 码/时长） | 测试意图（哪个组/哪些坐席组）+ 断言结论 |
>
> **为什么不合并成一条**：群呼是 Hermes **预测式拨号**，testkit 发起任务时 mock 还不知道这批号会被分到哪些 `call_uuid`（Hermes 后端临拨临生成），**发起即对齐 call_uuid 物理上做不到**。故被叫腿（真实 callUuid 主键）与发起记录（编排会话主键）分两行，是当前架构下的合理折中。前端记录页按 `scenario` 筛选区分展示。

### 2.3 任务生命周期管理（运行期暂停/恢复/取消）

`createAndImport` 后任务即自动拨号，运行期可经以下端点控制（mock 已接 Hermes 任务管理 OpenAPI）：

| mock 端点 | Hermes OpenAPI | 说明 |
|---|---|---|
| `POST /tests/callcenter-task/:taskCode/pause` | `task/status/stop/{taskCode}` | 暂停（→ PAUSE） |
| `POST /tests/callcenter-task/:taskCode/resume` | `task/status/start/{taskCode}` | 恢复（仅 PAUSE 态可恢复） |
| `POST /tests/callcenter-task/:taskCode/cancel` | `task/cancel/{taskCode}` | 取消（终态） |
| `GET /tests/callcenter-task/:taskCode/status` | `task/status/{taskCode}` | 查任务状态 |

> `taskCode` 来自 `runCallCenterTask` 创建响应（Hermes `data.code`，`orchestrator.extractTaskCode` 提取）。

### 2.4 群呼接通后转接坐席（前端软电话可接听）

群呼客户腿接通后，Hermes 把客户转给 `agentGroups` 里的坐席：

```
mock 客户被叫腿接通（§1）
   ▼ Hermes call-center 选坐席（技能组匹配 + 在线态）→ FS bridge
   ▼ 客户腿 ⇄ 坐席 jssip 软电话（WebRTC，x-session-id: CCINC/CCTSK{uuid}）
       └─ 坐席腿走 FS↔浏览器 jssip，**不经 mock 后端 SIP**（mock 只演客户腿）
   ▼ 前端软电话 INCOMING_CALL → applyAnswerRule 自动接 / 手动「接听」按钮
   ▼ 通话结束回存 agent-inbound 记录（§6）
   ▼ testkit.waitSeatTransferAnswered 查 mock_call(scenario=agent-inbound) 断言坐席腿接通
```

**前端能接，但有前提**（`testkit/kit.go:376-379`）：
1. 坐席软电话必须**已注册 FS + 状态切到可接**（在线就绪），且其技能组在群呼 `agentGroups` 内。
2. 坐席腿是 **WebRTC**，需 FS 的 wss/DTLS-SRTP + 可路由媒体；**本地 RTP/转坐席链路常走不通**。
3. 故坐席腿断言标 `Optional`，**不计入 run 成败**（仅展示坐席侧是否接通），避免本地环境把整条 run 判失败。

---

## 3. 场景二：call-bot 机器人外呼（任务 / 自动外呼）

**触发方**：testkit / 前端 callbot 页 → orchestrator → Hermes call-bot OpenAPI。
**特点**：无人工坐席腿；客户腿接通后由机器人/IVR 接管。mock 演客户腿，可发 DTMF / 跑 IVR 脚本模拟客户应答。

### 3.1 两种子场景

```
A. call-bot 任务（建任务导号）                B. 自动外呼（直接 originate）
   testkit.RunCallBotTaskObserved              testkit.RunAutoCallObserved
   │ orchestrator.CallBotTask                   │ orchestrator.AutoCall
   │   POST {call-bot}/openapi/task/            │   POST {call-bot}/openapi/autocall/originate
   │        create-and-import                   │   body:{templateCode, numbers:[{number}],
   │   body:{name, taskType(1=IVR/2=AI_CALL),   │         ttsTextVariableMap, encrypted}
   │         numbers, robotCode, salesScriptCode}│
   └────────────────┬───────────────────────────┘
                    ▼ Hermes call-bot 驱动 FS originate
                    │   INVITE x-session-id: CBTSK{callUuid} / CBRNT...
                    ▼ mock 被叫腿应答（§1）
                    │   行为档可配 DTMF / IVR 模拟客户按键，供机器人识别
                    ▼ testkit 断言客户腿 INVITE 200（waitAnyLegInviteOK）
                    ▼ mock_call(sip-inbound) + mock_call(scenario=callbot-task/autocall, source=testkit)
```

### 3.2 关键数据结构

**`testkit.CallBotTaskParams` / `AutoCallParams`**：
```go
type CallBotTaskParams struct {
    Name, Robot, Script string
    TaskType            int      // 1=IVR 2=AI_CALL
    CustomerGroup       string
    Numbers             []string
    WaitSec             int      // 默认 60
}
type AutoCallParams struct {
    TemplateCode  string
    CustomerGroup string
    Numbers       []string
    WaitSec       int           // 默认 20
}
```

**客户腿模拟机器人交互的行为档 `BehaviorProfile`（含 IVR）样例**：
```json
{
  "code": "ivr_press_1",
  "outcome": "ANSWER",
  "talkMs": 15000,
  "ivrJson": "[{\"id\":\"start\",\"prompt\":\"welcome.wav\",\"waitMs\":5000,\"branch\":{\"1\":\"confirm\"},\"sendDtmf\":\"1\"},{\"id\":\"confirm\",\"prompt\":\"ok.wav\",\"branch\":{},\"onNoKey\":\"HANGUP\"}]"
}
```
> 验证机器人是否正确识别按键，需经 Hermes `GET /openapi/call-trace/{callUuid}` 反查（mock 只控「客户按什么」，识别正确性由 Hermes 落库体现）。

---

## 4. 场景三：OTP 语音验证码

**触发方**：testkit / 前端 otp 页 → orchestrator → Hermes otp OpenAPI。
**特点**：Hermes 主动呼客户播报验证码，mock 演客户接听；可监听对端 DTMF 或本侧不交互。

### 4.1 端到端时序

```
testkit.RunOTPObserved（单个）/ RunOTPBatchObserved（批量，可并发）
   │ orchestrator.OTP(to, templateCode, params)
   │   POST {otp}/openapi/send
   │   body:{to, templateCode, params:{code:"123456"}, encrypted}
   │
   ▼ Hermes otp 驱动 FS originate
   │   INVITE x-session-id: OTPVC{callUuid}、x-call_bot_type:otp
   │
   ▼ mock 被叫腿应答（§1）─▶ 播报/接听 ─▶ mock_call(sip-inbound) + trace_leg
   │
   ▼ testkit 断言：waitAnyLegInviteOK([to], waitSec=30) INVITE 200
   ▼ mock_call(scenario=otp, source=testkit) + mock_test_run
```

### 4.2 关键数据结构

**`testkit.OTPParams` / `OTPBatchParams`**：
```go
type OTPParams struct {
    To           string            // 客户号
    CustomerGroup string           // 或从组取号
    TemplateCode string
    Params       map[string]string // 模板变量（验证码等）
    WaitSec      int               // 默认 30
}
type OTPBatchParams struct {
    CustomerGroup string
    Numbers       []string
    TemplateCode  string
    Params        map[string]string
    Concurrent    bool             // 并发下发
}
```

**批量结果 `testkit.ScenarioResult`**（含压测指标）：
```go
type ScenarioResult struct {
    Total, Passed, Failed int
    Metrics ScenarioMetrics // PassRate/AvgDurMs/MinDurMs/MaxDurMs/P90DurMs/Concurrent
    Runs    []Run
    Calls   []CallView
}
```

---

## 5. 场景四：坐席手动外呼（前端 jssip 软电话）

**触发方**：前端浏览器 jssip 软电话（`web/src/sip` + `AgentSoftphone`），经真实 hermes-ws 工作台 + call-center 选线 bridge 到 mock 被叫腿。
**特点**：坐席腿不在 mock 后端，由**前端浏览器 WebRTC** 承担；mock 只补被叫客户腿。这是唯一 mock 能控制注入自定义头的场景。

### 5.1 端到端时序

```
前端坐席软电话 AgentSoftphone.call(被叫)                    [web/src/sip/index.ts:317]
   │ currentCallId = uuidv7()（裸 32 位 uuid）
   │ INVITE extraHeaders:
   │   X-JCallId: {callId}
   │   x-session-id: CCMDL{callId}          ← 坐席外呼 BizType 前缀
   │   x-call_center_type: OUTBOUND_CALL
   │   x-agent_channel: {坐席号}
   │
   ▼ FS park 坐席腿 → ESL → hermes-call-center.OutboundCallHandler
   │   extractValidCallUuid(sessionId, CCMDL) ─▶ callUuid = {callId}（剥前缀）
   │   选线路（机构号码池 + 并发权重）
   │   在坐席腿上执行 bridge：sofia/external/{被叫}@{线路网关}
   │   INVITE 带 x-session-id: CCMDL{callId}（同一值回传）
   │
   ▼ mock 被叫腿应答（§1）
   │   BizUUIDFromHeaders 提取 x-session-id = CCMDL{callId}
   │   ─▶ mock_call(sip-inbound, call_uuid=CCMDL{callId}) + trace_leg(customer)
   │
   ▼ FS 自动 bridge 坐席腿 + 客户腿，双向 RTP
   │
   ▼ 通话结束 → 前端按规则断言 → saveAgentCallRecord(POST /call-records)
       callId={callId}, inbound=false, expectOutcome/expectFault, answered, verdict...
       后端 saveCallRecord：call_uuid = "CCMDL"+callId（与被叫腿一致！）
       ─▶ mock_call(scenario=agent-call, source=agent, record_id=agent-call:{callId})
```

**关键：两腿 call_uuid 一致**。坐席侧记录 `CCMDL{callId}` 与被叫腿 `CCMDL{callId}` 相同 → 同一通话的坐席视角与客户视角通过 `call_uuid` 关联。

### 5.2 前端外呼前的行为预览

```
前端外呼前 clusterResolve(number/line/listenPort)   GET /api/cluster/resolve
   ─▶ 返回 Resolved（预告本号会接/拒/故障），前端据此设「期望断言」
```

### 5.3 关键数据结构

**前端回存 `AgentCallRecord`**（POST /call-records 入参）：
```go
{
  callId, agentNumber, customer,
  expectOutcome, expectFault, expectDisabled,  // 外呼前从行为档解析的期望
  answered, endCause,
  verdict, verdictReason,                       // 期望 vs 实际的断言结论
  traceId, displayCaller,
  startedAtMs, answeredAtMs, durationMs
}
```

**落库 `mock_call`（坐席外呼）样例**：
```json
{
  "recordId": "agent-call:0196ed1e3e4f765794c4e66014c5c68e",
  "scenario": "agent-call",
  "source": "agent",
  "agentNumber": "1001",
  "customerNumber": "8613800138000",
  "direction": "AGENT_TO_CUSTOMER",
  "callType": "agent-outbound",
  "callUuid": "CCMDL0196ed1e3e4f765794c4e66014c5c68e",
  "status": "ENDED",
  "result": "符合预期：期望 ANSWER，实际接通",
  "detailJson": "{\"verdict\":\"pass\",\"expectOutcome\":\"ANSWER\",...}"
}
```

---

## 6. 场景五：坐席接被叫来电（群呼/转接进坐席腿）

**触发方**：群呼接通后 Hermes 转接，或呼入分配，FS bridge 把客户腿桥到坐席 jssip 软电话。
**特点**：坐席腿是被叫方；与坐席外呼共用 `POST /call-records` 但 `inbound=true`。

### 6.1 与坐席外呼的关键差异

```
jssip 收到来电 INCOMING_CALL                          [web/src/sip/index.ts:169]
   │ currentCallId = data.request.getHeader('x-session-id')
   │   ← 已是带前缀的完整 sessionId：CCINC{uuid}（呼入）或 CCTSK{uuid}（群呼转接）
   │
   ▼ 按接听规则自动接/拒（applyAnswerRule）
   ▼ 通话结束 → saveAgentCallRecord(inbound=true)
       后端 saveCallRecord：call_uuid = in.CallID（直接用，不再加 CCMDL！）
       ─▶ mock_call(scenario=agent-inbound, record_id=agent-inbound:{callId})
```

> **前缀按腿区分**（修正点）：坐席**外呼** `in.CallID` 是前端裸 uuid → 后端补 `CCMDL` 前缀；坐席**接来电** `in.CallID` 取自 `x-session-id` 头已含正确前缀（`CCINC`/`CCTSK`）→ 后端直接用，不能再叠 `CCMDL`（否则成 `CCMDLCCINC{uuid}` 双前缀对不上）。

---

## 7. 场景六：Hermes 回调（webhook）

**触发方**：Hermes 业务侧（回调地址需在 Hermes `t_callback_address` 配置指向 mock）。
**特点**：非通话本身，是任务结果/CDR/会话推送等异步事件；按 call_uuid 并入对应通话链路。

### 7.1 端到端

```
Hermes ──webhook──▶ POST /api/callbacks/:source              [api.go:824]
   │ callbacks.Store.Record(source, remote, payload)
   │   extract(payload)：从顶层或 data 嵌套提取 event/orgCode/callUuid
   │     event   ← event/eventType/type/action
   │     orgCode ← orgCode/org_code
   │     callUuid← callUuid/call_uuid/callId/uuid
   │   ─▶ mock_callback（落库）
   │
   ▼ 若 callUuid 非空：bus.EnsureByCallID(callUuid, "callback", "Hermes 回调 {source}")
       bus.Emit(sess, "", ChanFlow, DirIn, "回调:{event}", ...)
       ─▶ 并入该通话的 trace_leg（同 call_uuid）
```

### 7.2 关键数据结构

**`callbacks.Record`** / **`mock_callback` 行**：
```go
type Record struct {
    Seq      int64
    TS       time.Time
    Source   string          // 路径段（callbot/autocall/cdr 等）
    Event    string          // 从 payload 提取
    OrgCode  string
    CallUUID string          // 关联锚
    Remote   string          // 来源 IP
    Payload  json.RawMessage // 原始回调 JSON
}
```

**回调 payload 样例**（CDR 类）：
```json
{
  "event": "CALL_END",
  "orgCode": "org001",
  "callUuid": "CCTSK0196ed1e3e4f765794c4e66014c5c68e",
  "data": { "duration": 8, "hangupCode": 16, "billSec": 6 }
}
```

---

## 8. 跨场景对照表

| 维度 | 群呼任务 | call-bot | OTP | 坐席外呼 | 坐席接来电 |
|---|---|---|---|---|---|
| 触发方 | Hermes call-center | Hermes call-bot | Hermes otp | 前端 jssip | Hermes 转接 |
| 触发接口 | `POST /tests/callcenter-task` | `/tests/callbot`·`/tests/autocall` | `/tests/otp`·`/tests/otp-batch` | jssip INVITE | — |
| Hermes OpenAPI | `task/createAndImport`（即自动拨号） | `task/create-and-import`·`autocall/originate` | `openapi/send` | （WebRTC 链路） | — |
| BizType 前缀 | `CCTSK` | `CBTSK`/`CBRNT` | `OTPVC` | `CCMDL` | `CCINC`/`CCTSK` |
| mock 演什么 | 被叫客户腿 | 被叫客户腿（可发 DTMF/IVR） | 被叫客户腿（接听/收键） | 被叫客户腿 | 坐席腿（前端） |
| 坐席参与 | 接通后转接（前端软电话） | 无 | 无 | 主叫（前端软电话） | 被叫（前端软电话） |
| 落 mock_call scenario | sip-inbound + callcenter-task | sip-inbound + callbot-task/autocall | sip-inbound + otp | agent-call | agent-inbound |
| 记录写入者 | calltrace + testkit | calltrace + testkit | calltrace + testkit | api.saveCallRecord | api.saveCallRecord |
| 断言原语 | waitAnyLegInviteOK + 坐席腿 | waitAnyLegInviteOK | waitAnyLegInviteOK | 前端 assertCall | 前端（接听即记） |

---

## 9. 数据关联总览（一通业务通话的全貌）

```
                          call_uuid（Hermes 业务 sessionId，跨所有落点的关联锚）
                                          │
        ┌─────────────────────┬───────────┴───────────┬──────────────────────┐
        ▼                     ▼                       ▼                      ▼
  mock_call               mock_call              mock_trace_leg          mock_callback
  (sip-inbound)           (场景视角,testkit)      (N 条单腿)              (按 call_uuid)
  被叫腿状态机             发起预期+断言            customer/agent 各一    任务结果/CDR
  record_id=call_uuid     record_id=trace:{sess}  session_id=call_uuid
        │                                              │
        └──────────────────────────────────────────────┴─▶ mock_trace_event（单腿时间线+原始 SIP 报文）

读时装配（前端 trace 页）：traceSessions/:id 按 call_uuid 归并多条单腿 leg 的 events
                          → 一通业务通话含多腿的梯形图（纯展示，写入侧严格单腿，不跨腿聚合）
```

> 关键约束（守 SCOPE 非目标「❌ SIP 跨腿业务聚合」）：**写入侧严格单腿**，每条 SIP Call-ID 一行 `mock_trace_leg`；「一通通话含多腿」的视图只在**读时**按 `call_uuid` 归并装配，不写回任何聚合表、不产业务结论。

---

## 附：关键代码索引

| 环节 | 文件:函数 |
|---|---|
| 被叫腿主流程 | `internal/sipagent/agent.go: handleInbound`（141）|
| 行为解析 | `internal/sipagent/agent.go: resolveRule`（77）/ `clusterToRule`（106）|
| 客户集群解析 | `internal/cluster/store.go: ResolveByPort/ResolveByNumber`（164/135）|
| 被叫腿落库 | `internal/calltrace/tracker.go: Start/Answered/Rejected/Ended`|
| 链路总线 | `internal/tracelog/bus.go: EnsureByCallID/Emit/BizUUIDFromHeaders`|
| 链路周期落库 | `cmd/hermes-mock/main.go: traceFlushLoop`（139）|
| 业务编排 | `internal/orchestrator/orchestrator.go`（CallCenterTask/CallBotTask/AutoCall/OTP）|
| 场景断言 | `internal/testkit/kit.go`（Run*Observed / waitAnyLegInviteOK）|
| 坐席记录回存 | `internal/api/api.go: saveCallRecord`（893）|
| 回调接收 | `internal/api/api.go: receiveCallback`（824）+ `internal/callbacks/store.go`|
