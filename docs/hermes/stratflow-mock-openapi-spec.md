# StratFlow mock 下游 · `/openapi` 对接要点（hermes-mock 侧）

> **状态：已落地。** 我方（hermes-mock）提的 `/openapi/mock` 只读发现接口 stratflow 已实现。
> **接口契约（字段/参数）的权威源** = hermes 仓 `docs/reference/api-reference.md` 的
> 「hermes-stratflow — 应用层 Mock 管理」小节。
> 本篇**不复制契约**，只沉淀「hermes-mock 侧消费这套接口时必须处理、且与最初设想不同」的要点，供我方 Go client / 前端落地对齐。
> 关联：[[StratFlow mock 下游 · 使用与测试手册]]、hermes `controller/openapi/MockAdminController.kt` + `MockDiscoveryController`（新增发现接口）。

## 1. 落地结论（我方两个阻塞点已解）

- **versionCode 语义已钉死**：`GET /openapi/mock/workflows/{defCode}` 返回的 `versionCode` = **当前启用发布版本**（= 新 run 实际绑定版本，非草稿），且与 `import.plans[].versionCode`、`version-runs[].versionCode` 和物理 `executions[].versionCode` 一致 → 多方可交叉核对，配置不会静默失效。
- **import 回带 runCode 已加**：`POST /openapi/collections/{code}/import` 响应 `data.plans[].runCode`（`result==1` 时非空），断言闭环可直接拿到 run。

## 2. ⚠️ 与最初方案不同、Go client / 前端**必须按实际改**的 6 点

1. **物理 execution 进度必须带时间窗**
   `GET /openapi/mock/collections/{code}/executions/{rid}/progress?uploadStartTime=&uploadEndTime=`
   - UTC，格式 `yyyy-MM-dd HH:mm:ss`；**最大跨度 31 天**；URL 空格需编码（`2026-07-08%2000:00:00`）。
   - 实务：窗取 `[import.data.gmtCreate, now+1d]`（或用户可调），确保覆盖本次 run 上传时刻。
   - `{rid}` 传 `runCode`。

2. **断言模型 = 漏斗计数 + 边流量，不是 entry 级 phase**
   progress 返回 `data.nodes[] = { nodeId, inflow, processed, processing, edgeFlow:{ 边key: 计数 } }` + `data.run` 漏斗汇总（`numberCount/reachedEndCount/terminalCount/...`）。
   - **明确"当前不返回 entry 级 phase 明细"** → 断言"走了哪条分支"看 `nodes[].edgeFlow`（如 `voicebot_1.edgeFlow.success=7 / failed=3`），不要再按 phase 设计。

3. **import 响应要按 `plans[].result` 分流**
   - `result==1` → 取 `runCode` 进入观测/断言；
   - `result==2` → 字段契约失败，展示 `failFields`（对照 `collections/{code}/fields` 修 rows）；
   - `result==3` → 无可用绑定（该 collection 没绑方案，先去 `bindings` 确认）。

4. **发现接口返回比设想更全（利好，但 DTO 要对齐）**
   - `collections` 支持 `name`(模糊)/`status`(1 启用/2 禁用) 过滤，返回带 `boundPlanCount/fieldCount/entryCount`。
   - `fields`：`key/displayName/dataType/format/options/itemType/maxLen/scale/required/sort` —— 前端可据此生成动态表单校验。
   - `bindings`：`[{defCode, defName, status}]`（active 绑定）。
   - `Response` 信封含 `time` 字段；Go `encoding/json` 忽略未知字段，DTO 不必声明。

5. **版本记录与物理 execution 必须分开**
   - `GET /openapi/mock/collections/{code}/version-runs?pageNumber=&pageSize=` 返回数据库分组分页后的版本运行记录；一行身份为 `collectionCode + defCode + versionCode`，其中顶层 `code` 固定等于 `versionCode`，不存在唯一 `batchCode`。
   - `GET /openapi/mock/collections/{code}/version-runs/{defCode}/{versionCode}/progress?uploadStart=&uploadEnd=` 返回版本聚合进度，窗口参数为 ISO-8601 UTC instant、左闭右开且最大 30 天。
   - `GET /openapi/mock/collections/{code}/executions` 与 `/executions/{runCode}/progress` 保留物理身份。Mock plan、decision、DEAD requeue 和“本次导入”断言必须继续用物理 `runCode`，不能拿版本记录 `code` 代替。

6. **名单导入是部分成功合同，失败明细只在响应中存在**
   - 成功包解析 `total/success/fail/errors/errorsTruncated`；失败行的 `rowNo` 是原始 1-based 行号，不能因过滤非法行而压缩。
   - `success>0` 时即使 `fail>0` 也继续按 `plans[].result==1` 选择物理 `runCode` 观测；相同号码的重复行保持独立，不在 hermes-mock 去重、合并或重排。
   - `success==0` 时 Hermes 返回业务码 `42011` 和结构化 `data`。Go 代理以 HTTP 400 暴露 `error/upstreamCode/upstreamData`，Web 必须按 `upstreamCode` 分支，不能比较固定英文 `error` 文本。
   - Hermes 不持久化失败明细：首次响应展示有界 `errors`；命中已有部分成功批次的幂等重放返回 `errors=[]/errorsTruncated=true`。hermes-mock 不另建持久化副本。
   - 浏览器只拦无法构造请求的 JSON/CSV 结构问题；号码、必填、未知字段、类型、长度、枚举、数组及行数限制可以提示，但必须把原始行提交给 Hermes 作最终裁决。

## 3. 不变的坑（沿用手册）

- **gate 为三态且缺配置 fail-safe PAUSED**：进编排台先 `GET /openapi/mock/gate` 探 `master`+`mode/schemeModes`；旧 `global/schemes` 仅兼容二态。REAL/MOCK 写请求同时携带 `mode+enabled` 兼容旧 Hermes，PAUSED 不允许降级。`master=false` 整台只读。
- **两个 code 别混**：gate 按 `defCode`（方案）、config 按 `versionCode`（发布版本）；编排台经 `workflows/{defCode}` 把二者串起来。
- **配置须先于 import**：Case 在派发那刻按「强制 Case → 首条 bizFields 规则 → 默认选择」一次定型；先保存完整 Case/选择配置再导名单。
- **超时无回执不进入回放队列，但会保存 DONE 决策**：`GET /plans` 只看 PENDING/DEAD，`GET /decisions` 可看到该动作选中了 NO_RECEIPT；最终超时变量和出口仍以真实 timeout 处理后的 decisions/progress 为准。DEAD 可经 `POST /plans/{actionCode}/requeue` 恢复。

## 4. hermes-mock 侧落地清单（据已落地契约收敛）

- Go client（`internal/hermesopenapi`，复用共用网关/凭据）：
  gate 组、类型化 `config` 组、`plans`、分页 `decisions`、`all`；发现组 `workflows / workflows/{defCode} / collections[?name&status] / collections/{code}/fields / collections/{code}/bindings / collections/{code}/version-runs / version-runs/{defCode}/{versionCode}/progress / collections/{code}/executions / executions/{runCode}/progress`；触发 `import`（解析计数、结构化失败明细及 `plans[].{result,runCode,versionCode,failFields}`）。
- api：`/api/stratflow/mock/*` 透传；`api.Deps` 复用同一 `Client`。
- 前端「策略流 Mock 编排」页：gate 探测/开关 → 选方案(得 versionCode) → 结局配置表 → 选名单+字段拼 rows → import → **进度按 edgeFlow 断言 + 传时间窗** → 清场。
- 边界决策见 `docs/DECISIONS.md`；`docs/SCOPE.md` 保持“编排能力、非被叫腿核心”的定位。
