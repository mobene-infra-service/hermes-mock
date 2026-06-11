# Hermes 代码梳理（被测系统参考专区）

> ⚠️ 这里是**被测系统 Hermes** 的代码理解笔记，**不是 hermes-mock 自身的设计**（后者见 [../SCOPE.md](../SCOPE.md) / [../ARCHITECTURE.md](../ARCHITECTURE.md)）。
> 目的：把"读 Hermes 源码得出的结论"沉淀下来，让人和 AI 不必每次重读全量源码。

## 引用约定（重要）

- **源码根**：`<在此填你本地的 Hermes 源码目录，例如 ~/IdeaProjects/hermes>`。下文所有路径均**相对该根**。
- **每条结论尽量带 `路径/File.kt:行号`**——这是索引型文档的核心：AI 顺着行号去读源码即可，文档**不贴大段代码**。
- 凡推断（非源码直证）标注 **「未在代码中找到，推断」**，落地前需实测/复核。
- Hermes 升级后行号会漂移：发现对不上时，**就近搜符号名**重新定位并更新行号 + 本篇头部的"基于 <commit>"。

## 目录

| 文档 | 内容 | 状态 |
|---|---|---|
| [frontend-call-sdk.md](frontend-call-sdk.md) | 前端坐席通话 SDK（jssip/WebRTC：取址→注册→切态→发起；信令头；mock 复刻） | 初稿 |
| [callbot-outbound.md](callbot-outbound.md) | call-bot AI 机器人外呼任务（建任务/导入→Quartz 入队→TaskNumberScanner 发 Kafka→OutboundTaskDialer 选线 originate→SalesScriptDispatcher/CallAgentEventHandler 接管→CDR/回调；mock 当被叫客户腿） | 已补（Hermes .kt 行号逐文件实读补全，链官方 docs/modules/call-bot.md；剩话术 Handler/AI 对话细节未逐行读） |
| [callcenter-group-call.md](callcenter-group-call.md) | 群呼/批量外呼任务（call-center 预测式 autocall：建任务→导号→Quartz调度→比例/PID发起→接通转空闲坐席AUTO_OUTBOUND；REST/DTO/事件；mock 当被叫客户腿） | 初稿（拨号线程+GROUP_CALL handler 2 处文件待搜索补全） |
| [backend-overview.md](backend-overview.md) | 后端总览（call-center / call-bot / otp / basic / fs-esl-proxy / hermes-ws 职责与接口） | 骨架待填 |
| [agent-outbound-call.md](agent-outbound-call.md) | 坐席手动外呼端到端链路（选线/bridge/重试证据） | 已迁入 |
| [otp.md](otp.md) | OTP 语音验证码外呼（请求→originate→接通放音念码→挂机/回调；mock 当被叫客户腿） | 初稿（事件流/契约直证，部分 Controller/业务 .kt 行号待补） |

## 添加一篇 = 3 步

1. `cp _TEMPLATE.md <主题>.md`（kebab-case 英文名）
2. 按模板填：结论 + `路径:行号` 证据 + 时序/结构图 + 对 mock 的启示
3. 回本表登记一行
