# hermes-mock

> 🤖 **AI 协作以 [AGENTS.md](AGENTS.md) 为准**（Codex / Claude Code 共用单一入口）。本 README 面向人类读者。

> **Hermes 通话业务测试台** —— 一个可编程的 SIP「被叫客户线路对端」。
>
> 把 mock 的线路 address 配进被测 Hermes，由 **Hermes 业务侧（call-center 群呼 / call-bot / OTP）主动发起外呼**，
> FreeSWITCH 经该线路把 INVITE 送到 mock；mock 作**被叫 UAS**，按你配置的**客户行为档**
> 接听 / 拒接 / 振铃不接 / 放音 / 收发 DTMF / IVR / 挂断 / 故障注入，并把每通通话**落库**、采集**真实 SIP 报文**供断言。

> **核心边界**：mock **只演客户被叫腿**——不主动呼出、不做 B2BUA 桥接、不模拟坐席。
> 涉及坐席的场景（call-center 预测式群呼、坐席手动外呼）里，坐席由**真实 Hermes 工作台坐席**承担。

## 架构

```
 浏览器 ─▶ React 配置后台（Vite，//go:embed 进单二进制）
              │ REST
 ┌────────────▼──────────────── hermes-mock（Go 单二进制）──────────────────┐
 │ api:      Gin HTTP，服务前端 + 客户配置 / 业务测试触发 / 链路观测 REST       │
 │ cluster:  客户集群（号段组 + 个例 + 行为档 + 入口端口↔客户组绑定），持久化 hermes_mock │
 │ sipagent(diago): 被叫 UAS——接 FS 的 INVITE，按客户行为应答/放音/DTMF/挂断/故障   │
 │ orchestrator: 经 Hermes OpenAPI 让 Hermes 业务侧发起外呼（call-center/bot/OTP） │
 │ siptrace+tracelog: 传输层抓真实 SIP 报文，按 Call-ID 聚合成链路时间线（落库）     │
 │ calltrace/callbacks: 每通被叫 / Hermes 回调 落库（mock_call_record / mock_callback）│
 └────────────────────────▲───────────────────────────────────────────────┘
              真实 SIP/RTP │（Hermes 线路 t_line.address 指向 mock）
 ┌────────────────────────┴───────────────────────────────────────────────┐
 │ 被测 Hermes 栈：basic + call-center/call-bot/otp + fs-esl-proxy + FreeSWITCH │
 └─────────────────────────────────────────────────────────────────────────┘
```

一通业务测试的主线：**配客户行为（DB）→ 在「重点通话场景」页选场景触发 → Hermes 业务侧外呼到 mock 线路 → mock 按行为应答 → 采集真实 SIP + 落 mock_call_record → 断言通过/失败**。

## 前端（6 个页面）

| 页面 | 作用 |
|---|---|
| **总览** | 通话统计 + 活跃通话 + 近期链路会话 |
| **机构** | Hermes OpenAPI 接入凭据（网关/直连），一键切当前测试机构。mock 与 Hermes 只走 OpenAPI，绝不直连业务库 |
| **客户配置** | 核心：行为档 / 客户组（号段批量） / 客户个例 / 入口端口↔客户组绑定 的 CRUD + 一键上下线 + 解析预览 |
| **坐席** | 坐席状态 / 上线；与前端 jssip 软电话配合（坐席外呼） |
| **重点通话场景** | 由 Hermes 业务侧发起、mock 扮客户被叫：call-center 群呼 / call-bot 任务 / OTP / 呼叫记录 + **坐席外呼（前端 jssip）**，另含 line-call 底层 SIP 冒烟 |
| **通话链路** | 会话列表 + 事件时间线（可展开真实 SIP 报文 + 业务头），多腿按 callUuid 合并 |
| **Hermes回调** | 接收并查询 Hermes webhook（回调地址需在 Hermes 侧配置指向 mock） |

## 技术栈

- 后端：Go 1.24 + [emiago/sipgo](https://github.com/emiago/sipgo)（SIP）+ [emiago/diago](https://github.com/emiago/diago)（UAS/SDP/RTP/RFC4733 DTMF/playback）+ Gin + GORM
- 前端：React 18 + Vite + Ant Design，`//go:embed` 进单二进制
- 持久化：独立库 `hermes_mock`（客户集群 / 呼叫记录 / 链路 / 回调 / 机构配置），**不含任何 Hermes 业务表**

## 目录结构

完整目录树与各 `internal` 包职责见 **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)**（唯一描述处）。
入口 `cmd/hermes-mock/main.go`；前端 `web/`；本地真实 Hermes 栈 `deploy/hermes-stack/`。

## 快速开始

```bash
make tidy          # 拉取 Go 依赖（首次写入 diago/sipgo 版本）
make web           # 构建前端并同步到 embed 目录
make run           # 本地运行（或 make build 产出 Linux 二进制）
```

DDL：`deploy/ddl/hermes_mock.sql`（建 `hermes_mock` 库及各表）。

## 配置（环境变量——仅基础设施；业务接入一律在「机构」页配）

| 变量 | 默认 | 说明 |
|---|---|---|
| `HTTP_PORT` | 18080 | 配置后台 / API 端口 |
| `SIP_LISTEN_IP` / `SIP_LISTEN_PORT` / `SIP_LISTEN_PORTS` | 0.0.0.0 / 15060 / 15060,15061,...,15069 | 被叫 SIP 监听；多端口用逗号分隔，默认监听 10 个入口端口，为空时兼容单端口 `SIP_LISTEN_PORT` |
| `SIP_TRANSPORT` / `CODECS` | udp / PCMU,PCMA | SIP 传输 / SDP 编解码 |
| `EXTERNAL_IP` | 自动 | 对 FS 暴露的可达 IP（写入 SDP/Contact）；多网卡/host network 部署建议显式设置，如 `172.16.7.27` |
| `RTP_PORT_START` / `RTP_PORT_END` | 10000 / 10999 | RTP 端口段 |
| `AUDIO_DIR` / `DEFAULT_PLAYBACK` | assets/audio / hello.wav | 放音目录 / 默认放音 |
| `MOCK_DB_DSN` | — | `hermes_mock` 库 DSN 整串（优先）；或组件拼装：`MYSQL_MASTER_PASSWORD`(secret) + `DBAddr`/`DBPort`/`DBName`/`DBUser`(configmap)。**必配**，未配启动失败 |
| `LOG_LEVEL` / `MODE` | info / DEV | 日志级别 / 运行模式 |

> **Hermes 服务地址（basic/call-center/call-bot/otp）、OpenAPI 凭据、hermes-ws 工作台、fs-esl originate** 全部在前端「机构」页维护（存 `mock_org_config`），改配置/切机构即时生效，不走环境变量。

## 与 FreeSWITCH / Hermes 对接（硬前提）

1. **Hermes basic 线路**：在被测 Hermes 配置 `t_line`/`t_line_phone`，确保线路 `address` 指向本 mock 的具体端口（如 FS 测试机 Docker/裸跑默认 `mockIP:15060` 到 `mockIP:15069`）；mock 仅在自己库维护「入口端口↔客户组」绑定，**不直写 Hermes 业务表**。
2. **FreeSWITCH**：保证 SIP/RTP 与 mock 网络互通、编解码 PCMU/PCMA 对齐（外呼目标 = `sofia/external/{callee}@{t_line.address=mock}`）。
3. 业务侧（call-center/call-bot/otp/fs-esl-proxy）**零代码改动**。
4. 坐席场景：群呼/手动外呼接通后转坐席，由**真实 Hermes 工作台坐席**承担（mock 不模拟坐席）。

本地真实 Hermes 栈在 `deploy/hermes-stack/`（**本地辅助，不进 git**——含机器相关 .env/jar 挂载路径/FS 配置快照，详见其 README）。

## 文档

- 项目背景 / 预期 / 边界：[`docs/SCOPE.md`](docs/SCOPE.md)
- 架构 / 目录 / 模块职责：[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- 当前状态 / feature / 已知问题：[`docs/STATUS.md`](docs/STATUS.md)
- 关键决策日志：[`docs/DECISIONS.md`](docs/DECISIONS.md)
- 改动记录：[`docs/CHANGELOG.md`](docs/CHANGELOG.md)

> diago/sipgo 锁定 **diago v0.28.0 / sipgo v1.4.0**；无 ICE/STUN，mock 与 FreeSWITCH 需在同一可路由网络。
