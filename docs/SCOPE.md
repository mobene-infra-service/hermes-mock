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
