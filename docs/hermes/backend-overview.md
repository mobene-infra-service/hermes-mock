# Hermes 后端总览（Hermes 代码梳理）

> 目标读者：要理解被测系统 Hermes 各后端服务怎么协作、mock 该对接哪个接口的开发者。
> 范围：各后端服务的职责、关键入口/接口、外呼事件流。
> 基于：<待填 commit/日期>；源码根与引用约定见 [README.md](README.md)。
> ⚠️ **本篇当前为骨架**：除已知片段外，多数单元格待按源码填充（带 `路径:行号`）。

---

## 1. 服务全景（待填）

| 服务 | 职责 | 关键入口 / 接口 | 证据 |
|---|---|---|---|
| `basic` | 机构 / 线路(`t_line`) / 坐席(`t_agent`) 基础数据 + OpenAPI 供给 | OpenAPI（mock 经此读写线路绑定相关） | ⚠️ 待填 |
| `call-center` | 群呼 / 坐席外呼 / **选线** / bridge / CDR | `OutboundCallHandler` / `LinePhoneInfoService` | 见 §2 |
| `call-bot` | AI 外呼任务（创建/导入/触发） | `CreateAndImportTaskReq` 等（mock orchestrator 调） | ⚠️ 待填 |
| `otp` | 语音验证码外呼 | OpenAPI | ⚠️ 待填 |
| `fs-esl-proxy` | FS 的 ESL 事件 → 按 `x-scene` 路由进 Kafka topic | `FreeSwitchListener.kt:50-55` | 已知 |
| `hermes-ws` | 坐席工作台 WS 信令（登录 / 状态推送） | `/agent-workbench/api/ws` | 见 [frontend-call-sdk.md](frontend-call-sdk.md) |
| FreeSWITCH | SIP/RTP/WebRTC 媒体；dialplan 不含选线逻辑 | internal/external profile | 见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §3 |

## 2. 外呼事件流（已知片段）

坐席外呼 / 自动外呼的 `ESL → fs-esl-proxy → Kafka → call-center handler → 选线 → bridge` 链路，已在
[../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) §2.5 完整梳理（含 `CompositeEventDispatcher`、
`OutboundCallHandler.onCallStart`、`LinePhoneInfoService.getAvailablePhones` 选线、`Bridge.kt` 命令形态、
`OutboundRetryCallHandler` 换线重试）。本篇不重复，按需在此补"非外呼"的其他事件流（呼入、CDR 回调等）。

## 3. OpenAPI（mock 发起用，待填）

mock `orchestrator` 经 OpenAPI 触发的 call-bot / otp / call-center 接口：

| 业务 | 接口 | 请求 DTO | 证据 |
|---|---|---|---|
| call-bot 任务 | ⚠️ 待填 | `CreateAndImportTaskReq`（mock 侧 `internal/hermesopenapi`） | ⚠️ 待填 |
| otp | ⚠️ 待填 | | ⚠️ 待填 |
| call-center 群呼 | ⚠️ 待填 | | ⚠️ 待填 |

> 接入模式（gateway X-OpenApi-Key / direct 注入 ORG 头）见 mock 侧 [../ARCHITECTURE.md](../ARCHITECTURE.md) `orgcfg` 与 DDL `mock_org_config`。

## 4. 待补

- 各服务端口 / 包结构 / 启动入口 / 配置前缀。
- call-bot / otp 任务创建接口 DTO 与回调（webhook）规范。
- basic 的线路(`t_line`)/坐席(`t_agent`) 数据模型与 OpenAPI 端点。
- 呼入(inbound)链路（与外呼对照）。

## 附：关键证据索引（路径:行号）

- `hermes/hermes-fs-esl-proxy/.../FreeSwitchListener.kt:50-55`：ESL 事件按 `x-scene` 路由 Kafka topic。
- 外呼链路全套证据：见 [../AGENT-OUTBOUND-CALL.md](../AGENT-OUTBOUND-CALL.md) 附录。
