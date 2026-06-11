# AGENTS.md — hermes-mock

> 人与 AI 协作的**唯一权威入口**。Codex 原生读取本文件；Claude Code 经 `CLAUDE.md` 的 `@AGENTS.md` 复用同一份。
>
> **每次开工先看 [docs/STATUS.md](docs/STATUS.md) 的「当前焦点」；改动涉及定位/边界前先读 [docs/SCOPE.md](docs/SCOPE.md)。**

## 一句话定位

hermes-mock = 一个**可编程的 SIP「被叫客户线路对端」**，给开发者做**批量通话业务测试**：
把 mock 配成被测 Hermes 的线路 `address`，由 **Hermes 业务侧**（call-center 群呼 / call-bot / OTP / 坐席外呼）发起外呼，
FreeSWITCH 把 INVITE 送到 mock，mock 作**被叫 UAS** 按配置的客户行为档应答/拒接/振铃不接/放音/DTMF/IVR/挂断/故障，
并**落库** + 采集**真实 SIP 报文**供断言。

## 边界铁律（改之前必读）

- ✅ mock 后端（diago/sipgo）**永远只演被叫客户腿**：被动接 INVITE，按行为档应答。
- ✅ 发起方**永远是 Hermes 业务侧**（经 OpenAPI / 真实链路），不是 mock 后端。
- ✅ **坐席外呼**走**前端浏览器 jssip 软电话**（`web/src/sip` + `AgentSoftphone`），经真实 hermes-ws 工作台 + call-center 选线 bridge 到 mock 被叫腿——坐席**不在 mock 后端模拟**。
- ❌ **不做**：mock 后端主动当 UAC 呼出 / B2BUA 桥接 / 后端批量模拟坐席 / 重型可观测平台（SIP 梯形图·跨腿聚合·对话拉取）/ 录音回放平台 / 直写 Hermes 业务库（`t_line`/`t_agent`/`t_cdr`…）。
- 完整背景、能力清单与**非目标**：见 [docs/SCOPE.md](docs/SCOPE.md)。

## 构建 / 运行

```bash
make tidy    # 拉 Go 依赖（首次写入 diago/sipgo 版本，需有网，GOPROXY 建议 goproxy.cn）
make web     # 构建前端并同步到 cmd/hermes-mock/web/dist（go:embed）
make run     # 本地运行（默认 HTTP_PORT=8080，SIP :5060）
make build   # 交叉编译 Linux 二进制
```

- 本地真实 Hermes 栈：`deploy/hermes-stack/`（docker compose；**本地辅助，不进 git**——机器相关配置，新机器需另行获取）；**端口映射、IP/RTP 坑点、reloadxml 等见 [docs/STATUS.md](docs/STATUS.md) 的「本地栈速查」**。
- 调试：diago `sip.SIPDebug=true` / `media.RTPDebug=true`；`sngrep` / `tcpdump` 抓包验证与 FS 互通。

## 架构 / 目录

- 入口：`cmd/hermes-mock/main.go`（Gin + `go:embed` 前端 + 启动被叫 SIP agent）。
- **完整目录树与模块职责见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)（唯一描述处，勿在别处复制粘贴目录结构）。**

## 编码约定

- **Go**：`gofmt` + `goimports`；包名 lower_snake_case，导出标识符 PascalCase；日志用 `logrus.WithFields`；新增包配表驱动 `*_test.go`，提交前 `go test ./...`；diago API 未稳定，相关调用标注 `// TODO[diago-api]`。
- **TypeScript**：`npm run lint`；React 组件 PascalCase，hooks camelCase，公共类型集中 `web/src/types`。
- **JSON 字段统一驼峰。**
- 提交遵循 Conventional Commits（`feat:` / `fix:` / `refactor:`）。

## 给 AI 的协作约定（重要——防止「走偏」）

1. **动手前**：对照 [docs/SCOPE.md](docs/SCOPE.md) 的非目标清单，确认本次改动不越界；越界则先和用户确认。
2. **完成显著改动后**：更新 [docs/STATUS.md](docs/STATUS.md)（feature 状态 / 已知 bug / 当前焦点）。
3. **做出影响定位或边界的取舍时**：追加一条到 [docs/DECISIONS.md](docs/DECISIONS.md)（带日期 + 为什么）。
4. **目录结构变化**：只更新 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)，不在本文件展开。
5. 文档之间**只链接、不复制**——每个事实只有一个「家」。

## 文档索引

| 文档 | 内容 |
|---|---|
| [docs/SCOPE.md](docs/SCOPE.md) | 背景 / 预期定位 / 核心能力 / **非目标**（防膨胀锚点） |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 架构图 / 目录树 / 模块职责 / 技术栈版本 / 数据流 / DB 表（以代码为准） |
| [docs/STATUS.md](docs/STATUS.md) | 当前焦点 / feature 进度 / 已知 bug / 本地栈速查（**最常更新**） |
| [docs/DECISIONS.md](docs/DECISIONS.md) | 关键决策日志（为什么这么定，倒序） |
| [docs/CHANGELOG.md](docs/CHANGELOG.md) | 改动记录（改了什么 + 验证状态，倒序） |
| [docs/hermes/](docs/hermes/) | **被测系统 Hermes 代码梳理**专区（前端通话 SDK / 后端总览 / 坐席外呼链路…） |