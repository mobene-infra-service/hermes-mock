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

	"hermes-mock/internal/agents"
	"hermes-mock/internal/api"
	"hermes-mock/internal/callbacks"
	"hermes-mock/internal/calltrace"
	"hermes-mock/internal/cluster"
	"hermes-mock/internal/config"
	"hermes-mock/internal/hermesprobe"
	"hermes-mock/internal/model"
	"hermes-mock/internal/orchestrator"
	"hermes-mock/internal/orgcfg"
	"hermes-mock/internal/sipagent"
	"hermes-mock/internal/siptrace"
	"hermes-mock/internal/testkit"
	"hermes-mock/internal/tracelog"
	"hermes-mock/internal/wsagent"

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
	// （服务地址/OpenAPI 凭据/hermes-ws/fs-esl）都在「机构」页维护，不走环境变量；绝不直连 Hermes 业务库。
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
	// 通话跟踪（被叫腿生命周期落 mock_call_record）
	tracker := calltrace.New(repo)
	// 坐席状态表（工作台 WS 在线态 + SIP 注册态 + 工作状态）
	registry := agents.New()
	// 通话链路事件总线（SIP/媒体/WS 统一时间线，可观测核心）
	bus := tracelog.New()
	// 在 sipgo 传输层注册 SIP tracer：自动捕获**所有收发的原始 SIP 报文**（含业务头），
	// 按 Call-ID 聚合进 tracelog。必须在创建 SIP agent（建 UA/传输）之前安装。
	siptrace.Install(bus)
	// Hermes 栈可观测（健康端点从当前机构配置推导）+ 针对性测试编排
	prober := hermesprobe.New(orgStore)
	kit := testkit.New(cfg, prober, bus, clu)
	kit.SetRepo(repo)
	kit.SetOrgs(orgStore)
	// 编排器：经 Hermes 业务 REST 触发 call-bot 任务 / 坐席操作（转接/转组/切状态）。
	orch := orchestrator.New(orgStore)
	kit.SetBizCaller(orch) // 让 testkit 能触发群呼/自动外呼任务并观测链路
	// Hermes 回调接收（webhook）：回调地址需在 Hermes 侧配置指向 mock。
	cbStore := callbacks.New(repo)
	// 坐席工作台客户端：让 mock 坐席经 hermes-ws 上线 + 切状态（地址/口令从当前机构配置动态取）。
	wsCli := wsagent.New(registry, bus, orgStore)

	// 6. 启动 SIP agent（diago）：接收 FreeSWITCH 的 INVITE，按客户集群行为应答（只演客户被叫腿）
	agent, err := sipagent.New(cfg, clu, tracker, bus)
	if err != nil {
		logrus.Fatalf("init sip agent: %v", err)
	}
	go func() {
		if err := agent.Run(); err != nil {
			logrus.Errorf("sip agent stopped: %v", err)
		}
	}()

	// 通话链路常态落库：定期把会话+事件刷到 mock_trace_*。
	go traceFlushLoop(repo, bus)

	// 7. HTTP：配置后台 + REST + 前端
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	api.Register(r, cfg, repo, clu, tracker, prober, kit, bus, orgStore, cbStore, registry, wsCli)

	distFS, _ := fs.Sub(webDist, "web/dist")
	api.MountFrontend(r, distFS)

	addr := fmt.Sprintf(":%d", cfg.HTTPPort)
	logrus.Infof("hermes-mock HTTP on %s | SIP %s:%d/%s", addr, cfg.SIPListenIP, cfg.SIPListenPort, cfg.SIPTransport)
	if err := r.Run(addr); err != nil {
		logrus.Fatal(err)
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
				Legs: strings.Join(s.Legs, ","), StartedAt: s.StartedAt, UpdatedAt: s.UpdatedAt,
			})
			if s.Kind == "call" || s.Kind == "sip-call" || s.Kind == "callback" || s.Kind == "outbound" {
				row := cluster.CallRecordFromTraceSession(s)
				_ = repo.SaveCallRecord(ctx, &row)
			}
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
