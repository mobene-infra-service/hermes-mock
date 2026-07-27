# SCOPE — 背景 / 预期 / 边界

> 本文是 hermes-mock 的**定位锚点**：它存在的理由、要做成什么、以及**明确不做什么**。
> 实现一旦和这里冲突，要么改实现、要么改本文（并在 [DECISIONS.md](DECISIONS.md) 记一笔），不允许默默漂移。

## 一、背景：为什么需要这个 mock

被测系统 Hermes 是一套呼叫中心栈（call-center 群呼 / call-bot AI 外呼 / OTP 语音验证码 / 坐席外呼），
外呼最终经 FreeSWITCH 打到运营商**线路**对端。要测这些业务，传统做法是在 FS dialplan 里写 mock 线路，
但那样**测不到真实 SIP/媒体交互**，也无法灵活模拟"客户接听/拒接/振铃不接/放音/按键/故障"。

hermes-mock 用真实 SIP（emiago/sipgo + diago）做一个**可编程的被叫客户线路对端**，替代 dialplan mock：
把 mock 的地址配成 Hermes 线路 `t_line.address`，业务侧发起外呼后 FS 会把 INVITE 真正送到 mock，
mock 按预设的"客户行为档"应答，并采集真实 SIP 报文 + 落库，供测试断言。

## 二、预期定位（基准链路）

```
开发者 ──配 mock 线路 address──▶ Hermes (basic: t_line.address = mock)
        Hermes 业务层发起外呼 (call-center 群呼 / call-bot / OTP / 坐席外呼) ── 选 mock 线路
                                          │
                                FreeSWITCH ──INVITE──▶  mock (被叫 UAS)
                                                           │
                         按【客户行为档】决定：接听 / 拒接 / 振铃不接 / 放音 / DTMF / 挂断 / 故障
                                                           │
                采集真实 SIP（INVITE/响应码/BYE + 原始报文）→ 落 mock_call + mock_trace_leg → 断言通过/失败
```

**两条铁律：**
1. **发起方永远是 Hermes 业务层**（经 OpenAPI / 真实链路触发），不是 mock 后端。
2. **mock 后端永远是被动被叫**（客户线路对端），只演"客户腿"。

## 三、核心能力（可编程被叫）

- **行为档**（`mock_behavior_profile`）：6 种 outcome（ANSWER/REJECT/BUSY/NO_ANSWER/UNAVAILABLE/BRIDGE）
  + 振铃/通话时长 + 拒接 SIP 码 + 放音 + DTMF 序列 + IVR 脚本 + **9 种故障注入** + **接通率%**。
- **批量客户**：客户组（`mock_customer_group`）= 一个号段 N 个虚拟客户，引用行为档、绑定 mock SIP 入口端口；改组状态/行为档 → 整批生效。
- **个例覆盖**（`mock_customer_override`）：组内个别号码的例外行为/状态。
- **端口绑定**（`mock_line_binding`）：mock SIP 入口端口 ↔ 客户组；Hermes 线路 `t_line.address` 仍在 Hermes 侧配置为 `mockIP:port`，mock 内部不再按 `lineAddress` 路由。
- **真实 SIP 采集**：传输层抓原始报文，**按单腿（SIP Call-ID）落库** `mock_trace_leg/event`；同一通业务通话的多腿由 `call_uuid` 关联，「一通含多腿」的视图在**读时**按 call_uuid 归并装配（纯展示、不写回、不在写入侧做跨腿业务聚合）。
- **Hermes 业务发起**：经 OpenAPI 触发 call-bot / OTP / call-center；坐席外呼经前端 jssip 软电话。

## 四、角色边界

| 角色 | 由谁承担 |
|---|---|
| 呼叫**发起** | Hermes 业务层（call-center/call-bot/otp）或前端 jssip 坐席 |
| **被叫客户腿** | **mock 后端**（diago/sipgo UAS）——本项目核心 |
| **坐席**（接听员一方） | 真实 Hermes 工作台坐席；mock 体系内由**前端浏览器 jssip 软电话**承担（不在 mock 后端用 SIP/WS 模拟坐席话路） |
| **坐席的准备 / 管控** | mock **经 Hermes OpenAPI** 查询 / 创建 / 编辑真实坐席、控制坐席工作状态（测试准备能力，**保留**；经 OpenAPI、不直写 Hermes 库） |
| 选线路 / 桥接 | Hermes call-center 后端 + FreeSWITCH（不在 mock） |

## 五、非目标 ❌（Out of Scope —— 防膨胀锚点）

以下能力**明确不属于 mock**：

- ❌ **mock 后端主动当 UAC 呼出**（`Originate`/`/api/dial`/`/api/scenario` 方向反了）。
- ❌ **mock 后端做 B2BUA 桥接**（接听后再呼第二腿）。
- ❌ **mock 后端用 SIP/WS 模拟坐席在线/话路**。
- ❌ **重型可观测平台**：SIP 跨腿业务聚合、ASR/TTS 对话拉取——只保留"按 Call-ID 抓单腿真实报文 + 轻量链路时间线"。
- ❌ **录音回放平台**。

> 新功能动手前，先确认它不在上面这张清单里；若确有必要突破，先在 [DECISIONS.md](DECISIONS.md) 记录理由并和用户确认。

## 六、边界注：策略流 Mock 编排（第二类 mock 的控制台）

`/stratflow-mock` 页 + `/api/stratflow/mock/*` 是 **hermes-stratflow「应用层 mock」的编排/观测台**（经 OpenAPI 配结局分布/开关门闸/触发 run/观测计划断言分支），
属「经 OpenAPI 触发 Hermes 业务 + 测试编排」的延伸（与群呼/callbot/OTP 触发同类），**不触碰被叫腿定位**。

要点：stratflow 应用层 mock 与 hermes-mock 被叫腿是**同一通触达的互斥 mock**——开 stratflow mock 则**不产真实 SIP**（走事件层合成回执），
用 hermes-mock 被叫腿则须关 stratflow mock。故本能力测的是**策略图分支/回执逻辑**，不测 SIP/媒体。它是控制台、不是被叫腿核心，
**不得**长成 stratflow 完整管理台。详见 [DECISIONS.md](DECISIONS.md) 2026-07-08 条 + [hermes/stratflow-mock-openapi-spec.md](hermes/stratflow-mock-openapi-spec.md)。

## 七、边界注：通用 HTTP Mock（测试控制面辅助）

`/http-mock` 页 + `ANY /mock/{token}` 提供一个可编程 HTTP 测试桩：按 method/query/header/jsonBody/rawBody 参数选择固定响应或命名 Case 权重池，
也可对未命中规则的请求按权重随机 Case，或通过显式 Case / FULL 覆盖控制 status、headers、原始 body、延迟与超时。首个用途是给 call-center/call-bot 的
`confirmUrlBeforeDial` 提供拨打前确认，但接口本身不绑定 Hermes 业务模型，可供其它 webhook/HTTP 联调用例复用。

这项能力属于「测试控制面/外部依赖桩」，不改变两条 SIP 铁律：mock 后端仍不主动发起通话、不做 B2BUA、不模拟坐席话路。
它也**不是**通用 API 网关、流量代理、录制回放平台或生产服务虚拟化平台；只做命名 Endpoint 的可控响应（固定/条件规则/权重随机）与轻量调用记录。

## 八、边界注：短信厂商 Mock（有状态外部依赖桩）

`/sms-mock` 页 + `ANY /sms-mock/{provider}/{token}` 模拟的是 **Hermes-Arke 所调用的外部短信厂商**（当前 CM/v1 Adapter 要求 POST JSON）。它需要先按厂商协议返回提交结果，再以相同 `reference` 异步回 DLR；因此是独立短信模块，而不是给通用 HTTP Mock 增加几个响应字段。两者只复用条件规则、命名 Case 和概率选择语义。

短信核心只认识规范化消息、选择结果和持久化回执任务；具体线协议由 `provider/protocolVersion` Adapter 负责。当前 `CM/v1` 对齐 Hermes 实际 `SmsCmService` 的请求、响应和回调 DTO，支持 Accepted 后送达/失败、提交拒绝、超时、畸形响应、无 DLR、重复 DLR，以及持久化网络重试、重启恢复和手工立即/重发/取消。CM 成功 DLR 的 `errorCode` 必须为空，这是 Hermes 当前成功判定的真实契约。

协议演进分两级处理：仅响应/DLR 增删字段时可使用页面里的受限模板（必须保留精确 `reference`）；请求结构或语义变化新增版本 Adapter，使旧 Endpoint 继续可复测。提交请求与同步响应的 method/header/body、DLR 的 method/header/body 都由 Adapter 持有；其它厂商新增 Adapter 后复用同一状态机、表和页面，不复制一套 worker。

该模块会主动发出的只有**厂商 HTTP DLR**，不发起 SIP 呼叫，故不突破“mock 后端不当 SIP UAC”的铁律。它不是生产短信网关、不代理真实厂商流量、不保存真实产品 token，也不负责 Arke 回业务方的最终通知；只用于可控测试外部依赖行为。
