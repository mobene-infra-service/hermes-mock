-- =====================================================================
-- hermes_mock 库 DDL —— hermes-mock 自己的持久化数据。
--
-- 定位铁律：本库**只存 mock 自身**的配置 / 剧本 / 测试 / 观测记录，
--          **绝不**包含任何 Hermes 业务表（t_line/t_agent/t_cdr 等）。
--          mock 写 Hermes 业务库（如供给线路）是另一回事，走 basic 库，不在此。
--
-- 核心抽象：mock 是「可批量编排的虚拟客户集群」：
--   行为档(behavior_profile)  ← 一组可自定义的应答行为
--   客户组(customer_group)    ← 一个号段 N 个客户，引用一个行为档，绑定到一个 mock SIP 入口端口
--   客户个例(customer_override)← 组内个别号码的例外行为（覆盖组行为）
--   端口绑定(line_binding)    ← mock SIP 入口端口 ↔ 客户组 的对应
--
-- 形态：复用 hermes-stack 的 MySQL 实例，独立库 hermes_mock，逻辑隔离。
-- Schema 单源：本 DDL 是 internal/entity 各 GORM 实体 tag 的生成快照；改表结构先改实体，再同步此处。
-- 加载：mysql -uroot -p123456 < hermes_mock.sql
-- =====================================================================

CREATE DATABASE IF NOT EXISTS `hermes_mock`
  DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE `hermes_mock`;

SET FOREIGN_KEY_CHECKS = 0;

-- ---------------------------------------------------------------------
-- 1) 行为档：一组可复用的「应答行为」（所有小功能均可自定义）
--    被客户组 / 客户个例 / 坐席组引用。
-- ---------------------------------------------------------------------
DROP TABLE IF EXISTS `mock_behavior_profile`;
CREATE TABLE `mock_behavior_profile` (
  `id`            bigint unsigned NOT NULL AUTO_INCREMENT,
  `code`          varchar(64)  NOT NULL                  COMMENT '行为档编码',
  `name`          varchar(128) NOT NULL DEFAULT ''       COMMENT '行为档名称',
  `outcome`       varchar(16)  NOT NULL DEFAULT 'ANSWER' COMMENT 'ANSWER/REJECT/BUSY/NO_ANSWER/UNAVAILABLE/BRIDGE',
  `ring_ms`       int          NOT NULL DEFAULT 0        COMMENT '振铃时长 ms（可自定义）',
  `talk_ms`       int          NOT NULL DEFAULT 8000     COMMENT '接听后通话时长 ms（可自定义）',
  `hangup_code`   int          NOT NULL DEFAULT 0        COMMENT '拒接/不可用 SIP 码 486/503/480（可自定义）',
  `playback`      varchar(128) NOT NULL DEFAULT ''       COMMENT '接听后放音文件（可自定义）',
  `dtmf`          varchar(64)  NOT NULL DEFAULT ''       COMMENT '接听后发送 DTMF 序列，如 159#（可自定义）',
  `expect_dtmf`   tinyint      NOT NULL DEFAULT 0        COMMENT '接听后监听对端按键（IVR 交互观测）1=是',
  `fault`         varchar(24)  NOT NULL DEFAULT ''       COMMENT '故障注入 NONE/ONE_WAY_AUDIO/NO_RTP/HALF_HANGUP/NO_RESPONSE/SLOW_ANSWER/ANSWER_DROP/RTP_LOSS/RTP_REORDER（可自定义）',
  `bridge_target` varchar(128) NOT NULL DEFAULT ''       COMMENT 'BRIDGE 时桥接目标 SIP URI（可自定义）',
  `ivr_json`      text                                   COMMENT 'IVR 脚本（JSON 数组：放音→收键→分支，多轮对话；空=不用 IVR）',
  `answer_ratio`  int          NOT NULL DEFAULT 100      COMMENT '接通率%（批量随机：100=全接，0=全不接，可模拟接通率）',
  `remark`        varchar(255) NOT NULL DEFAULT '',
  `gmt_create`    datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `gmt_modified`  datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_behavior_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='mock 行为档（可复用应答行为）';

-- ---------------------------------------------------------------------
-- 2) 客户组：一个号段 N 个虚拟客户 + 引用行为档
--    号码范围 [number_start, number_start+count)，或显式前缀+起始。
-- ---------------------------------------------------------------------
DROP TABLE IF EXISTS `mock_customer_group`;
CREATE TABLE `mock_customer_group` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT,
  `code`           varchar(64)  NOT NULL                 COMMENT '客户组编码',
  `name`           varchar(128) NOT NULL DEFAULT ''      COMMENT '客户组名称',
  `number_prefix`  varchar(32)  NOT NULL DEFAULT ''      COMMENT '号码前缀（如 8613800）',
  `number_start`   bigint       NOT NULL DEFAULT 0       COMMENT '号段起始（数值部分）',
  `count`          int          NOT NULL DEFAULT 1       COMMENT '号段内客户数量（批量）',
  `behavior_code`  varchar(64)  NOT NULL DEFAULT ''      COMMENT '引用的行为档 code（整组默认行为）',
  `state`          varchar(16)  NOT NULL DEFAULT 'ENABLED' COMMENT '组状态 ENABLED/DISABLED（批量控制在线/可用）',
  `remark`         varchar(255) NOT NULL DEFAULT '',
  `gmt_create`     datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `gmt_modified`   datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_group_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='mock 客户组（号段批量）';

-- ---------------------------------------------------------------------
-- 3) 客户个例覆盖：组内个别号码的例外行为/状态（覆盖组默认）
-- ---------------------------------------------------------------------
DROP TABLE IF EXISTS `mock_customer_override`;
CREATE TABLE `mock_customer_override` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT,
  `group_code`     varchar(64)  NOT NULL                 COMMENT '所属客户组 code',
  `number`         varchar(32)  NOT NULL                 COMMENT '具体客户号码',
  `behavior_code`  varchar(64)  NOT NULL DEFAULT ''      COMMENT '覆盖行为档 code（空=仅改状态）',
  `state`          varchar(16)  NOT NULL DEFAULT 'ENABLED' COMMENT '个例状态（覆盖组状态）',
  `remark`         varchar(255) NOT NULL DEFAULT '',
  `gmt_create`     datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `gmt_modified`   datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_group_number` (`group_code`, `number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='mock 客户个例覆盖';

-- ---------------------------------------------------------------------
-- 4) 入口端口绑定：mock SIP 入口端口 ↔ 客户组
--    Hermes 线路 address 仍在 Hermes 侧配置为 mockIP:listen_port；
--    mock 只按 INVITE 到达的本地端口命中客户组，再按组(或个例)行为应答。
-- ---------------------------------------------------------------------
DROP TABLE IF EXISTS `mock_line_binding`;
CREATE TABLE `mock_line_binding` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT,
  `listen_port`    int          NOT NULL                 COMMENT 'mock SIP 入口端口，如 5060/5061',
  `line_code`      varchar(64)  NOT NULL DEFAULT ''      COMMENT '可选：Hermes 线路 code，仅作标识/兼容',
  `line_name`      varchar(128) NOT NULL DEFAULT ''      COMMENT '可选：线路名（FS 经 X-Line-Name 注入，主要用于观测）',
  `group_code`     varchar(64)  NOT NULL DEFAULT ''      COMMENT '绑定的客户组 code',
  `enabled`        tinyint      NOT NULL DEFAULT 1,
  `remark`         varchar(255) NOT NULL DEFAULT '',
  `gmt_create`     datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `gmt_modified`   datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_port` (`listen_port`),
  KEY `idx_line_code` (`line_code`),
  KEY `idx_group` (`group_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='mock 入口端口↔客户组绑定';

-- ---------------------------------------------------------------------
-- 5) （已移除）坐席组 mock_agent_group：mock 只演客户被叫腿，坐席由真实 Hermes 坐席承担。
-- ---------------------------------------------------------------------

-- ---------------------------------------------------------------------
-- 6) （已移除）测试用例定义 mock_test_case：用例是 testkit 代码内的 Run* 方法，无数据驱动需求。
-- ---------------------------------------------------------------------

-- ---------------------------------------------------------------------
-- 7) 测试运行历史：每次运行结果 + 步骤断言（回归趋势）
-- ---------------------------------------------------------------------
DROP TABLE IF EXISTS `mock_test_run`;
CREATE TABLE `mock_test_run` (
  `id`            bigint unsigned NOT NULL AUTO_INCREMENT,
  `run_id`        varchar(32)  NOT NULL,
  `case_code`     varchar(64)  NOT NULL DEFAULT '',
  `case_kind`     varchar(32)  NOT NULL DEFAULT '',
  `ok`            tinyint      NOT NULL DEFAULT 0,
  `duration_ms`   int          NOT NULL DEFAULT 0,
  `trace_id`      varchar(32)  NOT NULL DEFAULT '',
  `steps_json`    json         NULL,
  `artifacts_json` json        NULL,
  `started_at`    datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_run` (`run_id`),
  KEY `idx_case_time` (`case_code`, `started_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='mock 测试运行历史';

-- ---------------------------------------------------------------------
-- 8) 通话链路（单腿）：一条 SIP 对话腿的元信息。
--     写入侧严格单腿（一条 SIP Call-ID 一行）；「一通业务通话含多腿」由读时按 call_uuid 归并。
-- ---------------------------------------------------------------------
DROP TABLE IF EXISTS `mock_trace_leg`;
CREATE TABLE `mock_trace_leg` (
  `id`            bigint unsigned NOT NULL AUTO_INCREMENT,
  `session_id`    varchar(64)  NOT NULL                     COMMENT '单腿聚合键（业务 callUuid 优先，否则 SIP Call-ID）',
  `call_uuid`     varchar(96)  NOT NULL DEFAULT ''          COMMENT '业务 callUuid：同一通业务通话的多腿共享（关联锚）',
  `leg_role`      varchar(16)  NOT NULL DEFAULT ''          COMMENT 'customer / agent',
  `line`          varchar(128) NOT NULL DEFAULT ''          COMMENT '线路名/标识（观测用）',
  `kind`          varchar(24)  NOT NULL DEFAULT '',
  `title`         varchar(255) NOT NULL DEFAULT '',
  `started_at`    datetime(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`    datetime(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_leg_session` (`session_id`),
  KEY `idx_leg_call_uuid` (`call_uuid`),
  KEY `idx_leg_time` (`started_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='mock 通话链路（单腿）';

-- ---------------------------------------------------------------------
-- 9) 通话链路事件：SIP/媒体/WS 时间线（含原始 SIP 报文）
-- ---------------------------------------------------------------------
DROP TABLE IF EXISTS `mock_trace_event`;
CREATE TABLE `mock_trace_event` (
  `id`            bigint unsigned NOT NULL AUTO_INCREMENT,
  `session_id`    varchar(32)  NOT NULL,
  `seq`           bigint       NOT NULL DEFAULT 0,
  `ts`            datetime(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `leg`           varchar(64)  NOT NULL DEFAULT '',
  `channel`       varchar(8)   NOT NULL DEFAULT 'SIP'     COMMENT 'SIP/WS/MEDIA/BRIDGE/FLOW',
  `dir`           varchar(4)   NOT NULL DEFAULT '-'       COMMENT 'IN/OUT/-',
  `method`        varchar(32)  NOT NULL DEFAULT '',
  `summary`       varchar(512) NOT NULL DEFAULT '',
  `headers_json`  json         NULL                      COMMENT '结构化 SIP 头（含 X- 业务头）',
  `raw_message`   mediumtext   NULL                      COMMENT '原始 SIP 报文（req.String()）',
  PRIMARY KEY (`id`),
  KEY `idx_event_session_seq` (`session_id`, `seq`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='mock 通话链路事件（含原始 SIP 报文）';

-- ---------------------------------------------------------------------
-- 10) mock 通话记录（聚合根）：一行代表一通业务/客户电话。
--     发起侧（testkit/坐席外呼）先写预期，被叫腿（calltrace.Tracker 按 call_uuid 主键）补齐状态。
--     SIP 报文级观测在 mock_trace_leg/event，不在本表（已下沉）。
-- ---------------------------------------------------------------------
DROP TABLE IF EXISTS `mock_call`;
CREATE TABLE `mock_call` (
  `id`               bigint unsigned NOT NULL AUTO_INCREMENT,
  `record_id`        varchar(96)  NOT NULL                  COMMENT '主键：被叫腿=call_uuid，发起侧=run/trace/call 派生',
  `scenario`         varchar(32)  NOT NULL DEFAULT 'unknown' COMMENT 'sip-inbound/callcenter-task/callbot-task/otp/agent-call 等',
  `source`           varchar(24)  NOT NULL DEFAULT 'mock'    COMMENT 'testkit/sip/agent 等',
  `run_id`           varchar(64)  NOT NULL DEFAULT '',
  `org_code`         varchar(64)  NOT NULL DEFAULT '',
  `task_name`        varchar(128) NOT NULL DEFAULT '',
  `task_code`        varchar(64)  NOT NULL DEFAULT '',
  `customer_group`   varchar(64)  NOT NULL DEFAULT '',
  `customer_number`  varchar(64)  NOT NULL DEFAULT '',
  `agent_group_code` varchar(64)  NOT NULL DEFAULT '',
  `agent_number`     varchar(64)  NOT NULL DEFAULT '',
  `line_code`        varchar(64)  NOT NULL DEFAULT '',
  `line_address`     varchar(128) NOT NULL DEFAULT '',
  `line_name`        varchar(128) NOT NULL DEFAULT '',
  `direction`        varchar(32)  NOT NULL DEFAULT 'HERMES_TO_MOCK',
  `call_type`        varchar(32)  NOT NULL DEFAULT '',
  `expect_outcome`   varchar(24)  NOT NULL DEFAULT ''       COMMENT '发起侧期望行为（断言用）',
  `status`           varchar(24)  NOT NULL DEFAULT 'PENDING' COMMENT 'PENDING/RINGING/ANSWERED/ENDED/REJECTED/FAILED',
  `result`           varchar(64)  NOT NULL DEFAULT '',
  `hangup_code`      int          NOT NULL DEFAULT 0,
  `trace_id`         varchar(64)  NOT NULL DEFAULT '',
  `call_uuid`        varchar(96)  NOT NULL DEFAULT ''       COMMENT '关联锚：与 mock_trace_leg.call_uuid 关联',
  `started_at`       datetime(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `answered_at`      datetime(3)  NULL,
  `ended_at`         datetime(3)  NULL,
  `duration_ms`      bigint       NOT NULL DEFAULT 0,
  `last_event_at`    datetime(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `steps_json`       json         NULL,
  `detail_json`      json         NULL,
  `last_summary`     varchar(512) NOT NULL DEFAULT '',
  `gmt_create`       datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `gmt_modified`     datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_record` (`record_id`),
  KEY `idx_call_scenario_time` (`scenario`, `started_at`),
  KEY `idx_call_status_time` (`status`, `started_at`),
  KEY `idx_call_org_time` (`org_code`, `started_at`),
  KEY `idx_call_customer` (`customer_group`, `customer_number`),
  KEY `idx_call_agent` (`agent_group_code`, `agent_number`),
  KEY `idx_call_task` (`task_name`, `task_code`),
  KEY `idx_call_trace` (`trace_id`),
  KEY `idx_call_uuid` (`call_uuid`),
  KEY `idx_call_line` (`line_code`, `line_address`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='mock 通话记录（聚合根：发起预期 + 被叫腿结果）';

SET FOREIGN_KEY_CHECKS = 1;

-- =====================================================================
-- 关系总览：
--   配置域：behavior_profile ◄── customer_group ──► line_binding(listen_port→组)
--                            ◄── customer_override（组内例外，复合唯一 group_code+number）
--     解析一通呼叫的行为：line_binding(按 listen_port/line) → customer_group
--        → 若被叫号有 customer_override 用个例行为，否则用组 behavior_code。
--   记录域：mock_call（聚合根，一通电话 1 行）
--             │ call_uuid（被叫腿=record_id；与下表关联锚）
--             ▼
--           mock_trace_leg（单腿 SIP 对话，sip_call_id=session_id）
--             │ session_id
--             ▼
--           mock_trace_event（单腿时间线 + 原始报文）
--     mock_callback 按 call_uuid 关联；「一通含多腿」由读时按 call_uuid 归并（写入侧不跨腿聚合）。
-- =====================================================================

-- ============================================================
-- mock_org_config：机构 OpenAPI 接入配置（mock 与 Hermes 交互的唯一凭据来源）
-- 不存任何 Hermes 业务数据，仅存「怎么调 Hermes OpenAPI」。
-- ============================================================
DROP TABLE IF EXISTS `mock_org_config`;
CREATE TABLE `mock_org_config` (
  `id`               bigint unsigned NOT NULL AUTO_INCREMENT,
  `org_code`         varchar(64)  NOT NULL DEFAULT ''      COMMENT 'Hermes 机构 code',
  `org_name`         varchar(128) NOT NULL DEFAULT ''      COMMENT '机构名',
  `mode`             varchar(16)  NOT NULL DEFAULT 'direct' COMMENT 'gateway=走网关(X-OpenApi-Key) / direct=直连服务(注入ORG头)',
  `gateway_url`      varchar(256) NOT NULL DEFAULT ''      COMMENT '网关地址(gateway 模式)',
  `api_key`          varchar(128) NOT NULL DEFAULT ''      COMMENT 'X-OpenApi-Key(gateway 模式)',
  `basic_url`        varchar(256) NOT NULL DEFAULT ''      COMMENT 'basic 服务地址(direct 模式)',
  `call_center_url`  varchar(256) NOT NULL DEFAULT ''      COMMENT 'call-center 服务地址(direct 模式)',
  `call_bot_url`     varchar(256) NOT NULL DEFAULT ''      COMMENT 'call-bot 服务地址(direct 模式)',
  `otp_url`          varchar(256) NOT NULL DEFAULT ''      COMMENT 'otp 服务地址(direct 模式)',
  `agent_ws_url`     varchar(256) NOT NULL DEFAULT ''      COMMENT 'hermes-ws 工作台地址(host:port)',
  `user_code`        varchar(64)  NOT NULL DEFAULT ''      COMMENT '直连模式注入的操作人(审计)',
  `default_agent_group_code` varchar(64)  NOT NULL DEFAULT '' COMMENT '坐席外呼默认技能组',
  `default_agent_role_code`  varchar(64)  NOT NULL DEFAULT '' COMMENT '坐席外呼默认角色',
  `default_dep_code`         varchar(64)  NOT NULL DEFAULT '' COMMENT '坐席外呼默认部门',
  `default_agent_password`   varchar(128) NOT NULL DEFAULT '' COMMENT '坐席默认登录口令（明文，仅内网测试）',
  `remark`           varchar(255) NOT NULL DEFAULT '',
  `gmt_create`       datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `gmt_modified`     datetime     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_org` (`org_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='mock 机构 OpenAPI 接入配置';

-- ============================================================
-- mock_callback：收到的 Hermes 回调（webhook）落库（取代旧内存环）。
-- 回调地址需在 Hermes 侧(t_callback_address)配置指向 mock，这里只接收+展示。
-- ============================================================
DROP TABLE IF EXISTS `mock_callback`;
CREATE TABLE `mock_callback` (
  `id`           bigint unsigned NOT NULL AUTO_INCREMENT,
  `seq`          bigint       NOT NULL DEFAULT 0          COMMENT '接收顺序号',
  `ts`           datetime(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `source`       varchar(32)  NOT NULL DEFAULT ''         COMMENT '回调来源/类型（路径段，如 callbot/autocall/cdr）',
  `event`        varchar(64)  NOT NULL DEFAULT ''         COMMENT '事件名（从 payload 提取）',
  `org_code`     varchar(64)  NOT NULL DEFAULT ''         COMMENT '机构（从 payload 提取）',
  `call_uuid`    varchar(96)  NOT NULL DEFAULT ''         COMMENT '关联通话（从 payload 提取）',
  `remote`       varchar(64)  NOT NULL DEFAULT ''         COMMENT '来源 IP',
  `payload_json` mediumtext   NULL                        COMMENT '原始回调 JSON',
  PRIMARY KEY (`id`),
  KEY `idx_cb_ts` (`ts`),
  KEY `idx_cb_call_uuid` (`call_uuid`),
  KEY `idx_cb_source_event` (`source`, `event`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='mock 收到的 Hermes 回调';
