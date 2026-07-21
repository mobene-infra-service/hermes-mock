# StratFlow mock 下游 · `/openapi` 对接要点（hermes-mock 侧）

> **状态：已落地。** 我方（hermes-mock）提的 `/openapi/mock` 只读发现接口 stratflow 已实现。
> **接口契约（字段/参数）的权威源** = hermes 仓 `docs/reference/api-reference.md` 的
> 「hermes-stratflow — 应用层 Mock 管理」小节。
> 本篇**不复制契约**，只沉淀「hermes-mock 侧消费这套接口时必须处理、且与最初设想不同」的要点，供我方 Go client / 前端落地对齐。
> 关联：[[StratFlow mock 下游 · 使用与测试手册]]、hermes `controller/openapi/MockAdminController.kt` + `MockDiscoveryController`（新增发现接口）。

## 1. 落地结论（我方两个阻塞点已解）

- **versionCode 语义已钉死**：`GET /openapi/mock/workflows/{defCode}` 返回的 `versionCode` = **当前启用发布版本**（= 新 run 实际绑定版本，非草稿），且与 `import.plans[].versionCode`、`runs[].versionCode` 一致 → 三方可交叉核对，配置不会静默失效。
- **import 回带 runCode 已加**：`POST /openapi/collections/{code}/import` 响应 `data.plans[].runCode`（`result==1` 时非空），断言闭环可直接拿到 run。

## 2. ⚠️ 与最初方案不同、Go client / 前端**必须按实际改**的 4 点

1. **run 进度必须带时间窗**
   `GET /openapi/mock/collections/{code}/runs/{rid}/progress?uploadStartTime=&uploadEndTime=`
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

## 3. 不变的坑（沿用手册）

- **gate 为三态且缺配置 fail-safe PAUSED**：进编排台先 `GET /openapi/mock/gate` 探 `master`+`mode/schemeModes`；旧 `global/schemes` 仅兼容二态。REAL/MOCK 写请求同时携带 `mode+enabled` 兼容旧 Hermes，PAUSED 不允许降级。`master=false` 整台只读。
- **两个 code 别混**：gate 按 `defCode`（方案）、config 按 `versionCode`（发布版本）；编排台经 `workflows/{defCode}` 把二者串起来。
- **配置须先于 import**：Case 在派发那刻按「强制 Case → 首条 bizFields 规则 → 默认选择」一次定型；先保存完整 Case/选择配置再导名单。
- **超时无回执不进入回放队列，但会保存 DONE 决策**：`GET /plans` 只看 PENDING/DEAD，`GET /decisions` 可看到该动作选中了 NO_RECEIPT；最终超时变量和出口仍以真实 timeout 处理后的 decisions/progress 为准。DEAD 可经 `POST /plans/{actionCode}/requeue` 恢复。

## 4. hermes-mock 侧落地清单（据已落地契约收敛）

- Go client（`internal/hermesopenapi`，复用共用网关/凭据）：
  gate 组、类型化 `config` 组、`plans`、分页 `decisions`、`all`；发现组 `workflows / workflows/{defCode} / collections[?name&status] / collections/{code}/fields / collections/{code}/bindings / collections/{code}/runs / runs/{rid}/progress[?uploadStartTime&uploadEndTime]`；触发 `import`（解析 `plans[].{result,runCode,versionCode,failFields}`）。
- api：`/api/stratflow/mock/*` 透传；`api.Deps` 复用同一 `Client`。
- 前端「策略流 Mock 编排」页：gate 探测/开关 → 选方案(得 versionCode) → 结局配置表 → 选名单+字段拼 rows → import → **进度按 edgeFlow 断言 + 传时间窗** → 清场。
- 落地前补 `docs/DECISIONS.md`（为何把 stratflow 应用层 mock 编排台放进 hermes-mock）+ `docs/SCOPE.md` 一句注（编排能力、非被叫腿核心）。
</content>
