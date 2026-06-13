package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"hermes-mock/internal/api"
	"hermes-mock/internal/callbacks"
	"hermes-mock/internal/calltrace"
	"hermes-mock/internal/cluster"
	"hermes-mock/internal/config"
	"hermes-mock/internal/model"
	"hermes-mock/internal/orchestrator"
	"hermes-mock/internal/orgcfg"
	"hermes-mock/internal/sipagent"
	"hermes-mock/internal/siptrace"
	"hermes-mock/internal/testkit"
	"hermes-mock/internal/tracelog"

	"embed"
)

// 前端构建产物（make web 把 web/dist 同步到这里）。占位 index.html 保证 embed 不报错。
//
//go:embed all:web/dist
var webDist embed.FS

// 接线顺序：Config → Logger → Repository(工厂) → 领域 Store → Handlers → Router → Server。
func main() {
	// 1. 解析配置
	cfg, err := config.Load()
	if err != nil {
		logrus.Fatalf("load config: %v", err)
	}
	// 2. 初始化 logger
	if lvl, e := logrus.ParseLevel(cfg.LogLevel); e == nil {
		logrus.SetLevel(lvl)
	}
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})

	// 3. 初始化 Repository（工厂模式：DBType=mysql/sqlite；hermes_mock 库必配，含 AutoMigrate）
	repo, err := model.InitRepository(cfg)
	if err != nil {
		logrus.Fatalf("init repository: %v", err)
	}

	// 4. 领域 Store（内存缓存 + 解析；写穿透 Repository）
	clu, err := cluster.New(repo)
	if err != nil {
		logrus.Fatalf("init cluster store: %v", err)
	}
	logrus.Infof("cluster: 已接 hermes_mock 库，载入 %d 行为档/%d 客户组/%d 线路绑定",
		len(clu.ListProfiles()), len(clu.ListGroups()), len(clu.ListBindings()))
	// 机构配置 + 当前机构选择。mock 与 Hermes 的全部接入配置
	// （OpenAPI 服务地址/凭据/hermes-ws 工作台地址）都在「机构」页维护，不走环境变量；绝不直连 Hermes 业务库。
	orgStore, err := orgcfg.New(repo)
	if err != nil {
		logrus.Fatalf("init org config store: %v", err)
	}
	if cur := orgStore.Current(); cur != "" {
		logrus.Infof("orgcfg: 当前测试机构=%s（已配置 %d 个机构，OpenAPI 接入）", cur, len(orgStore.List()))
	} else {
		logrus.Warn("orgcfg: 尚未配置任何机构——先到「机构」页配置 OpenAPI 接入，业务场景才可用")
	}

	// 5. 观测与编排组件
	// 通话跟踪（被叫腿生命周期落 mock_call）
	tracker := calltrace.New(repo)
	// 通话链路事件总线（SIP/媒体/WS 统一时间线，可观测核心）
	bus := tracelog.New()
	// 在 sipgo 传输层注册 SIP tracer：自动捕获**所有收发的原始 SIP 报文**（含业务头），
	// 按 Call-ID 聚合进 tracelog。必须在创建 SIP agent（建 UA/传输）之前安装。
	siptrace.Install(bus)
	// 针对性测试编排
	kit := testkit.New(cfg, bus, clu)
	kit.SetRepo(repo)
	kit.SetOrgs(orgStore)
	// 编排器：经 Hermes 业务 REST 触发 call-bot 任务 / 坐席操作（转接/转组/切状态）。
	orch := orchestrator.New(orgStore)
	kit.SetBizCaller(orch) // 让 testkit 能触发群呼/自动外呼任务并观测链路
	// Hermes 回调接收（webhook）：回调地址需在 Hermes 侧配置指向 mock。
	cbStore := callbacks.New(repo)

	// 6. 启动 SIP agent（diago）：接收 FreeSWITCH 的 INVITE，按入口端口对应的客户集群行为应答（只演客户被叫腿）
	sipPorts, err := cfg.ListenPorts()
	if err != nil {
		logrus.Fatalf("parse SIP listen ports: %v", err)
	}
	for _, port := range sipPorts {
		agent, err := sipagent.NewOnPort(cfg, port, clu, tracker, bus)
		if err != nil {
			logrus.Fatalf("init sip agent on %d: %v", port, err)
		}
		go func(port int, agent *sipagent.Agent) {
			if err := agent.Run(); err != nil {
				logrus.Errorf("sip agent on %d stopped: %v", port, err)
			}
		}(port, agent)
	}
	// 启动期对账：cluster 端口绑定 vs 实际 SIP 监听端口。提示「死绑定」（绑了 mock 没监听的端口→永不生效）
	// 和「未绑定的监听口」（该口来话会回退按号/默认兜底），直击「在 cluster 页绑了端口却不按行为处理」。
	warnBindingPortMismatch(clu, sipPorts)

	// 通话链路常态落库：定期把会话+事件刷到 mock_trace_*。
	go traceFlushLoop(repo, bus)

	// 观测数据治理：周期清理早于 TTL 的呼叫记录/链路/回调，防长期膨胀。
	go pruneLoop(repo, cfg.ObserveTTLDays)

	// 7. HTTP：配置后台 + REST + 前端
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// RequestLogger 在外层：统一记录每个请求结果（含错误响应体），根治「接口报错却没日志」。
	// Recovery 在内层：就地恢复 panic（带堆栈落 Error），恢复后外层 RequestLogger 仍能记到最终 500。
	r.Use(api.RequestLogger(), api.Recovery())
	api.Register(r, cfg, repo, clu, tracker, kit, bus, orgStore, cbStore, orch)

	distFS, _ := fs.Sub(webDist, "web/dist")
	api.MountFrontend(r, distFS)

	addr := fmt.Sprintf(":%d", cfg.HTTPPort)
	logrus.Infof("hermes-mock HTTP on %s | SIP %s:%v/%s", addr, cfg.SIPListenIP, sipPorts, cfg.SIPTransport)
	if err := r.Run(addr); err != nil {
		logrus.Fatal(err)
	}
}

// warnBindingPortMismatch 启动期对账：cluster 端口绑定 ↔ 实际 SIP 监听端口。
//   - 死绑定：绑定的端口 mock 根本没监听 → 该绑定永不生效（来话进不到这个端口）。
//   - 未绑定监听口：该端口有来话时会回退按号段/默认兜底，而非某条端口绑定的行为。
//
// 这是「在 cluster 页绑了端口却不按绑定行为处理」的头号原因，启动时直接点出来。
func warnBindingPortMismatch(clu *cluster.Store, listenPorts []int) {
	listening := make(map[int]bool, len(listenPorts))
	for _, p := range listenPorts {
		listening[p] = true
	}
	bound := make(map[int]bool)
	for _, p := range clu.BoundPorts() {
		bound[p] = true
		if !listening[p] {
			logrus.Warnf("⚠️ 端口绑定 %d 不在 SIP 监听端口 %v 中——该绑定不生效（mock 未监听此端口）。"+
				"请把该端口加入 SIP_LISTEN_PORTS，或改绑到已监听的端口", p, listenPorts)
		}
	}
	for _, p := range listenPorts {
		if !bound[p] {
			logrus.Infof("ℹ️ SIP 监听端口 %d 未配置 cluster 端口绑定——该端口来话将回退按号段/默认兜底（非端口绑定行为）", p)
		}
	}
}

// traceFlushLoop 周期把 tracelog 会话与事件落到 hermes_mock 库（已落过的按 seq 跳过）。
func traceFlushLoop(repo model.Repository, bus *tracelog.Bus) {
	flushed := map[string]int64{} // session_id -> 已落库的最大 seq
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		for _, s := range bus.Sessions() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = repo.SaveTraceSession(ctx, &cluster.TraceSessionRow{
				SessionID: s.ID, CallUUID: s.CallID, Kind: s.Kind, Title: s.Title,
				LegRole: legRoleOf(s), Line: legLineOf(s),
				StartedAt: s.StartedAt, UpdatedAt: s.UpdatedAt,
			})
			// 注：被叫腿的 call_record 由 calltrace.Tracker 在 INVITE 时按 call_uuid 主键直接落库，
			// 不再从 trace 会话反推（已删 CallRecordFromTraceSession）。这里只落 trace 会话/事件。
			last := flushed[s.ID]
			var rows []cluster.TraceEventRow
			for _, e := range s.Events {
				if e.Seq <= last {
					continue
				}
				hdrs, _ := json.Marshal(e.Headers)
				rows = append(rows, cluster.TraceEventRow{
					SessionID: s.ID, Seq: e.Seq, TS: e.TS, Leg: e.Leg,
					Channel: string(e.Channel), Dir: string(e.Dir), Method: e.Method,
					Summary: e.Summary, HeadersJSON: string(hdrs), RawMessage: e.Raw,
				})
				if e.Seq > flushed[s.ID] {
					flushed[s.ID] = e.Seq
				}
			}
			_ = repo.CreateTraceEvents(ctx, rows)
			cancel()
		}
	}
}

// pruneLoop 周期清理早于 TTL 的观测数据（呼叫记录/链路/回调）。ttlDays<=0 表示不清理。
func pruneLoop(repo model.Repository, ttlDays int) {
	if ttlDays <= 0 {
		logrus.Info("观测数据保留无上限（OBSERVE_TTL_DAYS<=0），跳过周期清理")
		return
	}
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	prune := func() {
		before := time.Now().Add(-time.Duration(ttlDays) * 24 * time.Hour)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n, err := repo.PruneObservations(ctx, before)
		if err != nil {
			logrus.Warnf("观测数据清理失败: %v", err)
			return
		}
		if n > 0 {
			logrus.Infof("观测数据清理：删除 %d 行（早于 %s）", n, before.Format(time.RFC3339))
		}
	}
	prune() // 启动即清一次
	for range t.C {
		prune()
	}
}

// legRoleOf 推导单腿角色（customer/agent）：优先看事件 detail["role"]，否则按 leg 前缀 agent:。
func legRoleOf(s *tracelog.Session) string {
	for _, e := range s.Events {
		if r := e.Detail["role"]; r != "" {
			return r
		}
		if strings.HasPrefix(e.Leg, "agent:") {
			return "agent"
		}
	}
	for _, leg := range s.Legs {
		if strings.HasPrefix(leg, "agent:") {
			return "agent"
		}
	}
	return "customer"
}

// legLineOf 取该腿涉及的线路名（事件 detail["line"]，观测用）。
func legLineOf(s *tracelog.Session) string {
	for _, e := range s.Events {
		if l := e.Detail["line"]; l != "" {
			return l
		}
	}
	return ""
}
