# StratFlow Mock 端到端测试手册

> 适用环境：hermes-mock 编排台连接 Hermes StratFlow 测试环境。
>
> 本手册是可重复执行的测试流程与安全清场规范。StratFlow 接口字段权威源仍是 Hermes 仓的 `docs/stratflow-mock-openapi-spec.md`。

## 1. 测试目标与边界

这套测试验证的是 StratFlow 的应用层 mock：节点派发时生成合成回执并驱动策略图，不经过 SIP、FreeSWITCH 或 hermes-mock 被叫腿。

核心闭环：

```text
管理 API 创建字段/方案/画布/版本/授权
  → 机构 API 创建集合/选字段/绑定
  → hermes-mock 配 gate 与节点结局
  → OpenAPI 导入名单生成 run
  → progress + hermes-test 数据库断言
  → 逐节点清理配置并恢复共享状态
```

P2 节点按下面的边界测试：

| 节点/能力 | 测试方式 |
|---|---|
| Splitter | 直接执行 Hermes 真实分流，不配置专用 Mock |
| API Query / API Push | 执行真实节点、重试与持久化逻辑，只把 HTTP URL 指向 `/mock/{token}` |
| CALL / SMS → API Push | 类型化 Case 根据真实下发请求自动生成 `callbackPayload`；仅按字段目录部分覆盖 |
| 主动获取 | 本轮不覆盖 |

不在本手册范围：真实 SIP/媒体、坐席软电话、群呼或 call-bot 真实下游质量，以及 connection reset、TLS/DNS、畸形 HTTP 报文和大响应等底层传输故障。

## 2. 两组 API 前缀不要混用

hermes-mock 对外暴露两组不同前缀：

| 能力 | 前缀 | 示例 |
|---|---|---|
| gate/config/plans/decisions/clear | `/api/stratflow/mock` | `GET /api/stratflow/mock/gate` |
| 发现/import/progress | `/api/stratflow` | `POST /api/stratflow/collections/{code}/import` |

常见错误是把 import 写成 `/api/stratflow/mock/collections/...`，该路径返回 404。

## 3. 安全规则

### 3.1 开始前必须快照

至少记录：

```http
GET /api/stratflow/mock/gate
```

保存 `master/mode/schemeModes/deliveryPaused/receiptWindowSec`。`global/schemes` 只是旧客户端兼容字段。结束时必须恢复原值。

### 3.2 修复版控制项均按当前机构隔离

2026-07-11 修复版 Hermes 中，全局模式、scheme override、delivery pause、receipt window、config、plans 与 `DELETE /mock/all` 均按当前登录机构隔离。“global”表示当前机构全局，不再表示整个测试环境。

运行模式为：`REAL`（真实下游）、`MOCK`（合成回执）、`PAUSED`（新动作既不真实发送也不生成 Mock plan）。测试环境 capability 开启后，模式缺失或 Redis 异常默认 `PAUSED`。

执行破坏性清场前仍应检查当前机构在途 run：

```sql
SELECT code, org_name, def_code, status, number_count,
       reached_end_count, terminal_count, gmt_create
FROM stratflow.t_sf_run
WHERE status = 1 AND is_deleted = 0
ORDER BY gmt_create;
```

当前机构存在活跃 run 时：

- 可以使用专用 `[E2E]` 方案的 scheme override 与 per-version config；
- 切换当前机构 global gate 或 delivery pause 前，必须确认用例预期；
- 清空当前机构 plans 会让其在途 mock 动作转由超时路径处理，只能用于专门的计划清理用例；
- plans 查询会同时返回 `PENDING` 与 `DEAD`；DEAD 须记录 `retryCount/lastError`，修复故障后可在页面单条“重新入队”；
- PAUSED 的新派发最多约 5 秒重查一次 gate，切换到 REAL/MOCK 后无需重启，验收时允许这段恢复延迟；
- 机构切换竞态：在 A 机构发起慢查询后立即切 B，页面不得回填 A 数据；后续 gate/config/import/plans 请求头必须为 B，旧请求仍只能作用于 A；
- 版本前置：全部旧 Hermes 实例已停止、旧 `sf:mock:*` 已清理，环境只运行本次单一新版本；不执行混合版本兼容用例；
- Kafka 故障：验证 listener 重试期间 offset 不提交，耗尽后 DLT 必须有 broker ack；恢复后重放 DLT 不得重复推进已终态动作；
- 取消竞态：回执和 cancel 并发时只能出现“回执先完成”或“取消先完成”两种一致终态，禁止 action 成功但 cursor/counter 取消；
- 大 backlog：持续新增流量时旧 PAUSED 行仍必须获得固定处理配额，plan claim 处理超过 30 秒不得被其它实例正常 steal；
- 其他机构的 run 不再构成控制面测试的 BLOCKED 条件。

如果远端尚未部署本次修复版，先完成停旧实例与旧 Mock 数据清理，不在旧版共享状态上继续执行新用例。

### 3.3 禁止记录凭据

测试报告只记录环境、机构名、业务 code、runCode、batchCode 和结果；不得写入 JWT、OpenAPI Key、默认密码。

## 4. `test_xuhui` 永久 E2E 资产

### 4.1 字段

| key | 类型 | 约束 |
|---|---|---|
| `sf_e2e_case_id` | string | 必填，最大 64 |
| `sf_e2e_seq` | int | 必填 |
| `sf_e2e_note` | string | 可选，最大 128 |

### 4.2 方案

| 场景 | defCode | 当前/基线 versionCode | 节点 |
|---|---|---|---|
| SMS | `019f4cc0fbd3782783b736fab395140f` | 当前 v2 `019f4cd8d3e57d549f50ce134015b5e8`；历史 v1 `019f4cc0fd7671999317c6325cc72882` | `sms_main` |
| CALL | `019f4cc151667df18249d59c80db1b1d` | `019f4cc15347738f913f0274ad7198af` | `call_main` |
| MIXED | `019f4cc158107631b45738a869c5ba3c` | `019f4cc1594a70da8c827cc1b9112908` | `sms_mixed`、`call_mixed` |
| CONTROL | `019f4cc15cac791da5300d404daf2674` | `019f4cc15dd477048dcdf87ed61dafc2` | `sms_cancel`、`sms_plan_clear`、`sms_gate` |
| WINDOW | `019f4cc161247b799a733bb95663cbe5` | `019f4cc163647436bd39a3b6a3a7786c` | `sms_window` |

每次测试都应通过发现接口重新取得当前 versionCode，不要永久假定表中值不变：

```http
GET /api/stratflow/workflows/{defCode}
```

### 4.3 集合

| 场景 | collectionCode |
|---|---|
| SMS | `019f4cc1c49874ddbc0cadc17752ac03` |
| CALL | `019f4cc1c7dd78099ce97157998fd6df` |
| MIXED | `019f4cc1cca47f94add06b684dfccd40` |
| CONTROL | `019f4cc1d03f792f987e35735aaebcdd` |
| WINDOW | `019f4cc1d3ce75338c6695de66858c71` |
| NO_BINDING | `019f4cc1d742755091cbb1343afdf6df` |

## 5. 标准执行流程

### 5.1 发现与绑定检查

```http
GET /api/stratflow/workflows
GET /api/stratflow/workflows/{defCode}
GET /api/stratflow/collections
GET /api/stratflow/collections/{collectionCode}/fields
GET /api/stratflow/collections/{collectionCode}/bindings
```

断言：

- 方案可见且 `orgEnabled=true`；
- detail 的 `versionCode` 非空；
- 集合包含两个必填字段；
- binding 的 `defCode` 与预期一致。

### 5.2 开启专用方案 gate

```http
PUT /api/stratflow/mock/gate/scheme/{defCode}?mode=MOCK
```

只开当前专用方案 override；不要为了方便切全局 gate。

### 5.3 查看 Case、Schema 与编译预览

```http
GET /api/stratflow/mock/config/{versionCode}
```

未自定义时返回可编辑默认模板。重点检查：

- `config.cases`：可编辑 Case；
- `resultSchema`：CALL/SMS 可用状态、A–Z、RingType、最大拨次；
- `matchSchema`：bizFields 类型与操作符；
- `previews[caseKey]`：具体回执步骤、预期变量和触达节点出口。

### 5.4 配置节点

PUT 必须提交 GET 返回的完整 `config`。下面是最小 SMS 固定结果示例：

```http
PUT /api/stratflow/mock/config/{versionCode}/{nodeId}
Content-Type: application/json

{
  "forcedCaseKey": "delivered",
  "cases": [
    {"key":"delivered","name":"送达","delayMs":0,"result":{"type":"SMS","status":"DELIVERED","partCount":1}},
    {"key":"failed","name":"黑名单","delayMs":0,"result":{"type":"SMS","status":"FAILED","errorCode":"BLACKLIST","errorDesc":"黑名单拦截"}}
  ],
  "defaultSelection": {"mode":"FIXED","caseKey":"delivered"},
  "rules": []
}
```

取消 `forcedCaseKey` 并把默认选择改成 50:50：

```json
{
  "forcedCaseKey": null,
  "defaultSelection": {
    "mode": "WEIGHTED",
    "choices": [{"caseKey":"delivered","weight":1},{"caseKey":"failed","weight":1}]
  }
}
```

实际 PUT 仍要带完整 `cases/rules`。配置必须先于 import；每条名单派发时按“强制 Case → 第一条命中 bizFields 规则 → 默认选择”定型，之后修改配置不会改变已生成计划。画布 Condition 在回执写入变量后分支，与这里的 Case 选择规则不是一件事。

### 5.5 导入并取得 runCode

```http
POST /api/stratflow/collections/{collectionCode}/import
Content-Type: application/json

{
  "idempotencyKey": "sf-e2e-<case>-<date>",
  "rows": [
    {
      "phone": "13000000001",
      "businessId": "B123",
      "ticketId": "T123",
      "orderId": "O123",
      "userId": "U123",
      "bizFields": {
        "sf_e2e_case_id": "SF-E2E-001",
        "sf_e2e_seq": 1,
        "sf_e2e_note": "sms delivered"
      }
    }
  ]
}
```

四个业务标识与 `phone` 同级，不进入 `bizFields`。页面支持逐行 JSON、CSV 独立列及公共 JSON；显式空串和首尾空格会原样发送，由 Hermes 最终保存。

按 `plans[].result` 分流：

- `1`：run 已创建，保存 `runCode/versionCode`；
- `2`：字段契约失败，检查 `failFields`；
- `3`：无可用绑定。

### 5.6 部分成功、零合法行与幂等重放

用“合法、非法、合法”三行验证部分成功，前后两行可故意使用同一个合法号码：

```json
{
  "idempotencyKey": "sf-e2e-partial-<date>",
  "rows": [
    {"phone":"4155552671","bizFields":{"sf_e2e_case_id":"SF-PARTIAL-1","sf_e2e_seq":1}},
    {"phone":"bad-number","bizFields":{"sf_e2e_case_id":"SF-PARTIAL-2","sf_e2e_seq":2}},
    {"phone":"4155552671","bizFields":{"sf_e2e_case_id":"SF-PARTIAL-3","sf_e2e_seq":3}}
  ]
}
```

首次响应断言：

- `total=3/success=2/fail=1`，`errors[0].rowNo=2`，号码 reason 为固定英文 `Contains invalid characters`；
- `errorsTruncated=false`，且 `plans[].result==1` 的 `runCode` 仍被选中进入观测；
- 数据库 Entry 的原始 `row_no` 为 `1、3`，两个 Entry code 不同；相同号码不在 mock 或 Hermes 导入层合并。

用完全相同的 `idempotencyKey` 重放后断言 batch/run 不变，统计仍为 `3/2/1`，但 `errors=[]/errorsTruncated=true`。失败明细没有持久化，不能把重放解释成“原批次没有失败原因”。

再用新的 key 提交全部非法行，代理应返回 HTTP 400，响应中保留：

```json
{
  "error": "No valid rows to import",
  "upstreamCode": 42011,
  "upstreamData": {
    "total": 1,
    "success": 0,
    "fail": 1,
    "errors": [
      {
        "rowNo": 1,
        "errors": [
          {"fieldKey":"phone","reason":"Contains invalid characters"}
        ]
      }
    ],
    "errorsTruncated": false
  }
}
```

同时确认没有创建 batch/entry/run。`error` 是 Hermes 固定英文消息，页面逻辑只按 `upstreamCode` 分支。

本地 JSON/CSV 解析可以显示号码或字段语义提示，但不得因此禁用提交、过滤失败行或重排重复行；只有 JSON 语法、行对象形状、CSV 表头/列结构等无法可靠构造请求的问题才可本地阻止。

### 5.7 API Query / API Push 顺序重试

在 `/http-mock` 创建两个命名 Case，例如 `limited=HTTP 429`、`ok=HTTP 200`，顺序响应配置为：

```text
limited → ok
```

让 API Query 或 API Push 节点调用该 Endpoint，并只导入一条名单。断言：

- 节点走真实 HTTP、成功条件、Attempt 和重试逻辑；hermes-mock 不直接合成 API 节点结果；
- 调用记录依次命中 `limited`、`ok`，选择方式为 `SEQUENCE`；序列耗尽后继续返回 `ok`；
- `__mock_case` / `X-Mock-Case` 可临时指定 Case 且不消耗顺序位置；
- 保存 Endpoint 或重启 hermes-mock 后，下一次请求重新从 `limited` 开始；
- 多请求并发共享同一个 Endpoint 顺序，因此重试验收使用单条名单，避免把“第几次请求”误当成“某条名单第几次 Attempt”。

CALL/SMS 上游 Case 若需覆盖 API Push 业务字段，只在节点 Case 的“API Push 回调字段覆盖”中选择目录字段；不得粘贴完整 Kafka 回调 JSON。未覆盖字段由 Hermes 自动生成，无来源字段为 `null`。

### 5.8 进度断言

```http
GET /api/stratflow/collections/{collectionCode}/executions/{runCode}/progress
    ?uploadStartTime=2026-07-10%2000:00:00
    &uploadEndTime=2026-07-11%2000:00:00
```

- 时间使用 UTC `yyyy-MM-dd HH:mm:ss`；
- 两端必填；
- 跨度不得超过 31 天；
- 分支看 `nodes[].edgeFlow`，不要把内部 phase 当公开契约。

超时结局不会生成 mock plan，plans 为空不等于没有派发。

### 5.8 数据库事实核验

run 漏斗：

```sql
SELECT code, version_code, status, number_count, reached_end_count,
       terminal_count, expired_count, canceled_count, terminal_at
FROM stratflow.t_sf_run
WHERE code = :runCode AND is_deleted = 0;
```

逐条分支：

```sql
SELECT JSON_UNQUOTE(JSON_EXTRACT(e.biz_fields, '$.sf_e2e_case_id')) AS case_id,
       p.node_id, p.out_port, s.phase, s.current_node_id
FROM stratflow.t_sf_run r
JOIN stratflow.t_sf_entry e ON e.batch_code = r.batch_code AND e.is_deleted = 0
LEFT JOIN stratflow.t_sf_node_entry_progress p
       ON p.run_code = r.code AND p.entry_code = e.code AND p.is_deleted = 0
LEFT JOIN stratflow.t_sf_run_entry_state s
       ON s.run_code = r.code AND s.entry_code = e.code AND s.is_deleted = 0
WHERE r.code = :runCode AND r.is_deleted = 0;
```

回执与重拨：

```sql
SELECT a.node_id, a.final_status, t.attempt_no, t.send_status,
       t.result, t.last_error, t.send_time, t.receipt_time
FROM stratflow.t_sf_action a
LEFT JOIN stratflow.t_sf_attempt t
       ON t.action_code = a.code AND t.is_deleted = 0
WHERE a.run_code = :runCode AND a.is_deleted = 0
ORDER BY a.entry_code, a.node_id, t.attempt_no;
```

持久化 mock plan：

```sql
SELECT action_code, run_code, node_id, entry_code, channel, outcome_key,
       status, next_due_at, claim_token, claimed_at, retry_count, last_error, completed_at
FROM stratflow.t_sf_mock_action_plan
WHERE org_code = :orgCode AND run_code = :runCode AND is_deleted = 0
ORDER BY next_due_at;
```

负载用例不能只看 run 完成数；必须同时核对 action/attempt 数和各节点分布。

## 6. 推荐测试矩阵

### 6.1 校验与守卫

- `forcedCaseKey`、固定选择或概率池引用不存在的 Case；
- Case key/规则名重复，概率池为空、重复 Case、零/负权重，负延迟；
- CALL/SMS 跨类型字段、当前状态无效字段、NOT_CONNECTED 提前终态；
- bizFields 规则字段类型与画布 fieldContract 冲突、操作符/右值不合法；
- receipt window 59 秒；
- 空 rows；
- 缺必填字段；
- int 类型错误；
- 无绑定集合；
- progress 缺时间参数、超过 31 天。

### 6.2 SMS

至少覆盖 DELIVERED、带自定义 `errorCode/errorDesc/partCount` 的 FAILED、NO_RECEIPT。超时用例把 receipt window 临时设为 60 秒，并给断言预留至少 3 分钟。

### 6.3 CALL

分别配置并强制 CONNECTED、NOT_CONNECTED、CANCELLED、NOT_DIALED、NO_RECEIPT，重点核对：

- A 与 Z 意向分支，以及自定义通话时长；
- 全量 RingType 中的典型失败是否按 `maxRedialTimes+1` 生成多次 attempt；
- 指定后续拨次 CONNECTED/CANCELLED 是否先产生前置失败回执；
- NOT_DIALED 是否只在首拨终止；
- TIMEOUT 是否没有合成回执。

### 6.4 混合、窗口与控制

- SMS 成功 → CALL；
- SMS 失败 → 不进入 CALL；
- CALL 失败分支；
- 不在执行窗口时 skip；
- 延迟计划取消后不得被晚到回执反转；
- 在途计划生成后把 scheme mode 切为 `PAUSED`，原计划仍应完成；新动作保持待派发。

### 6.5 幂等与版本

- 相同 key + 相同 payload：同 batch/run；
- 相同 key + 不同 payload：仍返回首次 batch/run；payload 一致性由调用方保证；
- 无 key 重复提交：不同 batch/run；
- 发布 v2 后，新 run 使用 v2，旧 run 仍固定旧 versionCode；
- v2 mock config 不继承 v1。

### 6.6 分布与负载

- SMS 两结局 50:50，建议 n≥200；
- CALL A/B 50:50，建议 n≥200；
- MIXED 强制成功，建议 n≥500；
- 验收同时看完成率、分支数量、action 数、attempt 数、总时长与异常 entry。

## 7. 清场

优先逐节点删除配置，避免清理其他方案：

```http
DELETE /api/stratflow/mock/config/{versionCode}/{nodeId}
```

随后验证每个节点：

- `configured=false`；
- `config` 已回落服务端默认模板；
- 不再存在旧 `forcedOutcome/weights/baseDelayMs` 字段。

决策历史通过 `GET /api/stratflow/mock/decisions?runCode=...` 查看；DONE 默认保留 7 天。执行 `scope=plans/all` 会连同 DONE 历史一起删除，且可能中断在途回执，只能在明确清场时使用。

恢复开始前快照：

- global mode；
- 非 E2E scheme overrides；
- deliveryPaused；
- receiptWindowSec。

只有在确认当前机构的在途动作可以被清理时，才允许：

```http
DELETE /api/stratflow/mock/all?scope=plans&confirm=true
DELETE /api/stratflow/mock/all?scope=config
```

保留 `[E2E]` 字段、方案、版本、集合、绑定和历史 run，便于复测。

## 8. 2026-07-10 基线结果与 2026-07-11 修复状态

- SMS 50:50：103/97；CALL 50:50：94/106。
- 500 条混合 run 完成，但完整 SMS→CALL 为 499/500；1 条强制 DELIVERED 回执丢失后走 SMS fail。
- 60 秒回执窗的超时用例可能约 120 秒才完成。
- 旧部署的 `plans` 查询随 Redis keys 增多可能超过 10 秒；修复版改为 MySQL `orgCode + runCode` 有界查询，hermes-mock 端 5 秒超时且不阻断 progress，待部署复测。
- 旧部署的非空 `fieldContract` 会使方案详情/版本/自检等接口 500；修复版已统一 MyBatis JSON ObjectMapper，待部署复测。
- hermes-mock 的 `/api/orgs` 当前匿名暴露 `apiKey/defaultAgentPassword` 字段，必须按 P0 处理。
- 旧部署的 mock 控制项没有租户隔离；修复版已按当前机构隔离并增加资源归属校验，待跨机构 E2E。
- 幂等仅按 key 去重：相同 key 始终返回首次 batch/run，不比较 payload。
- 本地 `go test ./...`、Hermes `:hermes-stratflow:test`、`npm --prefix web run build` 全部通过；远端未部署，原 499/500 结果仍有效。

## 9. 2026-07-30 名单导入合同本地联调结果

本轮使用隔离的 MySQL、Redis、Basic/crypto stub、hermes-mock 和本地 profile StratFlow，不修改共享测试环境。临时机构/集合/方案构成 `Start → End` 最小图，验证结果如下：

- 直接调用 StratFlow 提交“重复合法号、非法号、重复合法号”：`code=0`、`total=3/success=2/fail=1`，唯一错误为原始第 2 行，reason=`Contains invalid characters`。
- 数据库只创建原始行号 `1、3` 的两个 Entry；两行号码规整/tokenize 结果相同，但 Entry code 不同。Run 有两个独立 cursor，最终 `number_count=2/terminal_count=2/reached_end_count=2`。
- 同 key 重放返回相同 batch/run 和完整统计，`errors=[]/errorsTruncated=true`。
- 全部非法的新请求返回 `42011`、固定英文 `No valid rows to import` 和结构化行明细，且对应 key 的 batch 数为 0；库内仍只有前一批的 1 个 batch、2 个 Entry、1 个 Run。
- 经 hermes-mock 代理执行同一部分成功用例得到 `3/2/1` 和第 2 行明细；代理重放保持 `errors=[]/errorsTruncated=true`，全部非法时以 HTTP 400 保留 `upstreamCode=42011/upstreamData`。
- 代理返回的物理 Run `53517cde25373c7aaeac32973d88d5bc` 可继续查询进度：Start 与 End 均 `inflow=2/processed=2`，Run 为 `numberCount=2/terminalCount=2/reachedEndCount=2`。这证明部分失败不会阻断成功重复行的独立执行与观测。
