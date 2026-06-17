package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"hermes-mock/internal/bootstrap"
	"hermes-mock/internal/callbacks"
	"hermes-mock/internal/calltrace"
	"hermes-mock/internal/cluster"
	"hermes-mock/internal/config"
	"hermes-mock/internal/entity"
	"hermes-mock/internal/hermesopenapi"
	"hermes-mock/internal/model"
	"hermes-mock/internal/orchestrator"
	"hermes-mock/internal/orgcfg"
	"hermes-mock/internal/preflight"
	"hermes-mock/internal/testkit"
	"hermes-mock/internal/tracelog"
)

// Deps 聚合 HTTP 层依赖。mock 只演被叫客户线路：不主动呼出、不模拟坐席。
type Deps struct {
	Cfg     *config.Config
	Repo    model.Repository
	Cluster *cluster.Store
	Tracker *calltrace.Tracker
	Kit     *testkit.Kit
	Bus     *tracelog.Bus
	Orgs    *orgcfg.Store
	CB      *callbacks.Store
	Orch    *orchestrator.Orchestrator
}

// Register 注册 REST 路由。
func Register(r *gin.Engine, cfg *config.Config, repo model.Repository, clu *cluster.Store, tracker *calltrace.Tracker, kit *testkit.Kit, bus *tracelog.Bus, orgs *orgcfg.Store, cb *callbacks.Store, orch *orchestrator.Orchestrator) {
	d := &Deps{Cfg: cfg, Repo: repo, Cluster: clu, Tracker: tracker, Kit: kit, Bus: bus, Orgs: orgs, CB: cb, Orch: orch}
	g := r.Group("/api")
	g.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok", "mode": cfg.Mode}) })

	// 通话监控（落库的呼叫记录聚合）
	g.GET("/calls", d.listCalls)
	g.GET("/calls/active", d.listActiveCalls)
	g.GET("/stats", d.stats)

	// 媒体管理（预置 G.711 WAV：列表 / 上传，供放音行为档选择）
	g.GET("/audio", d.listAudio)
	g.POST("/audio", d.uploadAudio)

	// 机构 OpenAPI 接入（mock 调 Hermes 的唯一凭据来源；一键切当前测试机构）
	g.GET("/orgs", d.listOrgs)
	g.POST("/orgs", d.upsertOrg)
	g.DELETE("/orgs/:orgCode", d.deleteOrg)
	g.POST("/orgs/ping", d.pingOrg)
	g.GET("/orgs/current", d.currentOrg)
	g.POST("/orgs/current", d.setCurrentOrg)
	g.GET("/orgs/tts", d.openapiTts)                  // 真实 TTS 模板（群呼/外呼表单联动）
	g.GET("/orgs/agent-groups", d.openapiAgentGroups) // 真实技能组（群呼指定接听坐席组，表单联动）

	// Hermes 回调接收（webhook）+ 查询筛选。回调地址需在 Hermes 侧配置指向 mock。
	g.POST("/callbacks/:source", d.receiveCallback)
	g.GET("/callbacks", d.queryCallbacks)
	// mock 自有呼叫记录（任务预期 + 真实 SIP 观测聚合），不从 Hermes 拉记录。
	g.GET("/call-records", d.queryCallRecords)
	g.POST("/call-records", d.saveCallRecord) // 前端坐席软电话外呼结束回存坐席侧记录 + 断言

	// 通话链路可观测（关键 SIP 信令事件时间线：INVITE/180/200/BYE + 原始报文）
	g.GET("/trace/sessions", d.traceSessions)
	g.GET("/trace/sessions/:id", d.traceSession)

	// 一屏概览（mock 通话统计/活跃 + 近期链路；不查业务库）
	g.GET("/hermes/overview", d.hermesOverview)

	// 经 OpenAPI 管理真实 Hermes 坐席（查/建/改/删/启停/控状态）
	g.GET("/agents/managed", d.listManagedAgents)
	g.POST("/agents/managed", d.addManagedAgent)
	g.PUT("/agents/managed", d.updateManagedAgent)
	g.DELETE("/agents/managed/:number", d.deleteManagedAgent)
	g.POST("/agents/managed/enabled", d.setManagedAgentEnabled)
	g.POST("/agents/managed/status", d.switchManagedAgentStatus)

	// 业务测试主线：Hermes 业务发起外呼 → mock 客户被叫按规则应答 → 采集真实 SIP 断言
	g.POST("/tests/callcenter-task", d.runCallCenterTask)    // call-center 预测式群呼
	g.POST("/tests/retry-switch-line", d.runRetrySwitchLine) // 失败重试换线（47bf482/b90673c：X-Line-Name 互异 + Call-ID 独立）
	g.POST("/tests/callbot", d.runCallBot)                   // call-bot 机器人任务
	g.POST("/tests/autocall", d.runAutoCall)                 // call-bot 自动外呼
	g.POST("/tests/otp", d.runOTP)                           // OTP 语音验证码
	g.POST("/tests/otp-batch", d.runOTPBatch)
	g.GET("/tests/preflight", d.preflight)  // 场景就绪自检
	g.POST("/tests/bootstrap", d.bootstrap) // 一键播种最小可运行配置
	g.GET("/tests/runs", d.testRuns)

	// 群呼任务生命周期管理（createAndImport 后即自动拨号；以下用于运行期暂停/恢复/取消）
	g.POST("/tests/callcenter-task/:taskCode/pause", d.pauseCallCenterTask)
	g.POST("/tests/callcenter-task/:taskCode/resume", d.resumeCallCenterTask)
	g.POST("/tests/callcenter-task/:taskCode/cancel", d.cancelCallCenterTask)
	g.GET("/tests/callcenter-task/:taskCode/status", d.callCenterTaskStatus)

	// 客户集群（号段组 + 个例 + 端口绑定 + 行为档）—— 可编程被叫的配置中心
	g.GET("/cluster/profiles", d.listProfiles)
	g.POST("/cluster/profiles", d.upsertProfile)
	g.GET("/cluster/groups", d.listGroups)
	g.POST("/cluster/groups", d.upsertGroup)
	g.POST("/cluster/groups/state", d.setGroupState)      // 一键切客户组在线/离线
	g.POST("/cluster/customer/state", d.setCustomerState) // 切单个客户在线/离线
	g.GET("/cluster/overrides", d.listOverrides)
	g.POST("/cluster/overrides", d.upsertOverride)
	g.GET("/cluster/bindings", d.listBindings)
	g.POST("/cluster/bindings", d.upsertBinding)
	g.GET("/cluster/resolve", d.clusterResolve) // 解析预览：给被叫号(+入口端口)看命中哪个组/行为
	g.DELETE("/cluster/profiles/:code", d.deleteProfile)
	g.DELETE("/cluster/groups/:code", d.deleteGroup)
	g.DELETE("/cluster/overrides/:number", d.deleteOverride)
	g.DELETE("/cluster/bindings/:listenPort", d.deleteBinding)

	// 浏览器→Hermes 反向代理：让 mock 前端里的 jssip 坐席软电话同源调到 call-center / hermes-ws
	//（免 CORS、免自签证书）；直连模式下注入网关本会注入的身份头。
	registerHermesProxy(r, orgs)
}

func (d *Deps) listProfiles(c *gin.Context)  { c.JSON(http.StatusOK, d.Cluster.ListProfiles()) }
func (d *Deps) listGroups(c *gin.Context)    { c.JSON(http.StatusOK, d.Cluster.ListGroups()) }
func (d *Deps) listOverrides(c *gin.Context) { c.JSON(http.StatusOK, d.Cluster.ListOverrides()) }
func (d *Deps) listBindings(c *gin.Context)  { c.JSON(http.StatusOK, d.Cluster.ListBindings()) }

func (d *Deps) upsertProfile(c *gin.Context) {
	var p cluster.BehaviorProfile
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out, err := d.Cluster.UpsertProfile(p)
	clusterReply(c, out, err)
}
func (d *Deps) upsertGroup(c *gin.Context) {
	var g cluster.CustomerGroup
	if err := c.ShouldBindJSON(&g); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out, err := d.Cluster.UpsertGroup(g)
	clusterReply(c, out, err)
}

// setGroupState 一键切换客户组在线/离线（ENABLED/DISABLED）。
func (d *Deps) setGroupState(c *gin.Context) {
	var req struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.State == "" {
		req.State = "ENABLED"
	}
	if err := d.Cluster.SetGroupState(req.Code, req.State); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "code": req.Code, "state": req.State})
}

// setCustomerState 切换单个客户在线/离线（写个例，优先于组）。
func (d *Deps) setCustomerState(c *gin.Context) {
	var req struct {
		Number    string `json:"number"`
		GroupCode string `json:"groupCode"`
		State     string `json:"state"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.State == "" {
		req.State = "ENABLED"
	}
	if err := d.Cluster.SetOverrideState(req.Number, req.GroupCode, req.State); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "number": req.Number, "state": req.State})
}
func (d *Deps) upsertOverride(c *gin.Context) {
	var o cluster.CustomerOverride
	if err := c.ShouldBindJSON(&o); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out, err := d.Cluster.UpsertOverride(o)
	clusterReply(c, out, err)
}
func (d *Deps) upsertBinding(c *gin.Context) {
	var b cluster.LineBinding
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out, err := d.Cluster.UpsertBinding(b)
	clusterReply(c, out, err)
}

func (d *Deps) deleteProfile(c *gin.Context) {
	clusterDelete(c, d.Cluster.DeleteProfile(c.Param("code")))
}
func (d *Deps) deleteGroup(c *gin.Context) { clusterDelete(c, d.Cluster.DeleteGroup(c.Param("code"))) }
func (d *Deps) deleteOverride(c *gin.Context) {
	clusterDelete(c, d.Cluster.DeleteOverride(c.Param("number")))
}
func (d *Deps) deleteBinding(c *gin.Context) {
	port, err := strconv.Atoi(c.Param("listenPort"))
	if err != nil {
		clusterDelete(c, err)
		return
	}
	clusterDelete(c, d.Cluster.DeleteBinding(port))
}

func clusterDelete(c *gin.Context, err error) {
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (d *Deps) clusterResolve(c *gin.Context) {
	number := c.Query("number")
	line := c.Query("line")
	listenPort := c.Query("listenPort")
	var res *cluster.Resolved
	source := "number"
	var note string
	if listenPort != "" {
		port, err := strconv.Atoi(listenPort)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// 对齐 SIP 运行时语义：端口有启用绑定 → 绑定权威；端口无绑定 → 运行时会回退按号/默认（这里点明）。
		if d.Cluster.HasBinding(port) {
			source = "port-binding"
			res = d.Cluster.ResolveByPort(port, number)
			if res == nil || res.Profile == nil {
				note = "端口已绑定但客户组/行为档缺失，运行时回退默认兜底（检查组是否存在、组 behaviorCode 是否有效）"
			}
		} else {
			source = "number-fallback"
			res = d.Cluster.ResolveByNumber(number)
			note = "该端口未配置启用绑定，运行时按号段/默认兜底解析（非端口绑定行为）"
		}
		// 死绑定提示：端口不在实际 SIP 监听端口中 → 该端口根本收不到来话。
		if ports, err := d.Cfg.ListenPorts(); err == nil {
			listened := false
			for _, p := range ports {
				if p == port {
					listened = true
					break
				}
			}
			if !listened {
				ps := make([]string, len(ports))
				for i, p := range ports {
					ps[i] = strconv.Itoa(p)
				}
				note = strings.TrimSpace(note + "；⚠️ 端口 " + listenPort + " 不在 SIP 监听端口 [" + strings.Join(ps, ",") + "] 中，该端口绑定不生效（mock 未监听）")
			}
		}
	} else if line != "" {
		source = "line"
		res = d.Cluster.ResolveByLine(line, number)
	} else {
		res = d.Cluster.ResolveByNumber(number)
	}
	if res == nil {
		c.JSON(http.StatusOK, gin.H{"matched": false, "source": source, "note": note})
		return
	}
	c.JSON(http.StatusOK, gin.H{"matched": true, "source": source, "note": note, "resolved": res})
}

func clusterReply(c *gin.Context, out any, err error) {
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, out)
}

// traceSessionSummary 是会话列表的轻量摘要（不含 events）。列表轮询只需这些字段，
// 避免每次把每条 event 的原始 SIP 报文（数百 KB）也序列化下发。events 走 /trace/sessions/:id 单查。
type traceSessionSummary struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Kind       string    `json:"kind"`
	CallID     string    `json:"callId"`
	StartedAt  time.Time `json:"startedAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Legs       []string  `json:"legs"`
	EventCount int       `json:"eventCount"`
}

func toTraceSummary(s *tracelog.Session) traceSessionSummary {
	return traceSessionSummary{
		ID: s.ID, Title: s.Title, Kind: s.Kind, CallID: s.CallID,
		StartedAt: s.StartedAt, UpdatedAt: s.UpdatedAt, Legs: s.Legs, EventCount: len(s.Events),
	}
}

func (d *Deps) traceSessions(c *gin.Context) {
	// ?match=<token>：服务端复刻前端 sessionMatchesCall 的子串语义（坐席软电话用 jssip callId 找自己那条
	// trace），回匹配到的完整 session（含 events）。仅扫内存——进行中的通话必在内存，且匹配键多藏在 event 里——
	// 不合并 DB，免去列表全量序列化的开销。
	if match := c.Query("match"); match != "" {
		hits := make([]*tracelog.Session, 0, 1)
		for _, s := range d.Bus.Sessions() {
			if b, err := json.Marshal(s); err == nil && strings.Contains(string(b), match) {
				hits = append(hits, s)
			}
		}
		c.JSON(http.StatusOK, hits)
		return
	}
	// 无 match：返回摘要列表（不含 events）。内存优先 + DB 补充已落库的旧会话，最后压成摘要。
	sessions := d.Bus.Sessions()
	if d.Repo != nil {
		rows, err := d.Repo.ListTraceSessions(c.Request.Context(), 100)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		seen := map[string]bool{}
		for _, s := range sessions {
			seen[s.ID] = true
		}
		for i := range rows {
			if s := traceSessionFromEntity(&rows[i]); s != nil && !seen[s.ID] {
				sessions = append(sessions, s)
			}
		}
	}
	out := make([]traceSessionSummary, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, toTraceSummary(s))
	}
	c.JSON(http.StatusOK, out)
}

func (d *Deps) traceSession(c *gin.Context) {
	s := d.Bus.Session(c.Param("id"))
	if s == nil {
		if d.Repo != nil {
			row, err := d.Repo.GetTraceSession(c.Request.Context(), c.Param("id"))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			s = traceSessionFromEntity(row)
		}
		if s == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
	}
	c.JSON(http.StatusOK, s)
}

// hermesOverview 一屏概览：mock 端通话统计/活跃 + 链路（近期会话）。
func (d *Deps) hermesOverview(c *gin.Context) {
	sessions := d.Bus.Sessions()
	if len(sessions) > 12 {
		sessions = sessions[:12]
	}
	summaries := make([]traceSessionSummary, 0, len(sessions))
	for _, s := range sessions {
		summaries = append(summaries, toTraceSummary(s))
	}
	c.JSON(http.StatusOK, gin.H{
		"mock": gin.H{
			"stats":  d.Tracker.Stats(),
			"active": d.Tracker.Active(),
		},
		"trace": gin.H{
			"sessions": summaries,
		},
	})
}

// ===== 经 OpenAPI 管理真实 Hermes 坐席（查/建/改/删/启停/控状态）=====
// 直接操作 Hermes basic 的真实坐席台账（mock 只走 OpenAPI、不碰库）；坐席上线/外呼走前端 jssip 软电话。

// openapiClient 取当前机构的 OpenAPI 客户端，未配凭据时直接回错。
func (d *Deps) openapiClient(c *gin.Context) (*hermesopenapi.Client, bool) {
	cred, ok := d.orgCred("")
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前机构未配置 OpenAPI 凭据（去「机构」页配置）"})
		return nil, false
	}
	return hermesopenapi.New(cred), true
}

// listManagedAgents 分页查真实坐席（POST /openapi/agent/page，支持筛选）。
func (d *Deps) listManagedAgents(c *gin.Context) {
	cli, ok := d.openapiClient(c)
	if !ok {
		return
	}
	pageNum, _ := strconv.Atoi(c.DefaultQuery("pageNum", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "50"))
	f := hermesopenapi.AgentFilter{
		AgentName:      c.Query("agentName"),
		Number:         c.Query("number"),
		AgentGroupCode: c.Query("agentGroupCode"),
		DepCode:        c.Query("depCode"),
		Status:         c.Query("status"),
	}
	list, total, err := cli.ListAgentsWithFilter(c.Request.Context(), pageNum, pageSize, f)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"agents": list, "total": total})
}

// addManagedAgent 单建真实坐席。
func (d *Deps) addManagedAgent(c *gin.Context) {
	cli, ok := d.openapiClient(c)
	if !ok {
		return
	}
	var req hermesopenapi.AddAgentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	a, err := cli.AddAgent(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, a)
}

// updateManagedAgent 改真实坐席。
func (d *Deps) updateManagedAgent(c *gin.Context) {
	cli, ok := d.openapiClient(c)
	if !ok {
		return
	}
	var req hermesopenapi.UpdateAgentReq
	if err := c.ShouldBindJSON(&req); err != nil || req.AgentNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 agentNumber"})
		return
	}
	if err := cli.UpdateAgent(c.Request.Context(), req); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "agentNumber": req.AgentNumber})
}

// deleteManagedAgent 删真实坐席（Hermes 要求先停用启用中的坐席）。
func (d *Deps) deleteManagedAgent(c *gin.Context) {
	cli, ok := d.openapiClient(c)
	if !ok {
		return
	}
	if err := cli.DeleteAgent(c.Request.Context(), c.Param("number")); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// setManagedAgentEnabled 批量启停真实坐席（按 agentCode）。
func (d *Deps) setManagedAgentEnabled(c *gin.Context) {
	cli, ok := d.openapiClient(c)
	if !ok {
		return
	}
	var req struct {
		AgentCodes []string `json:"agentCodes"`
		Enabled    bool     `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.AgentCodes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 agentCodes"})
		return
	}
	if err := cli.SetAgentEnabled(c.Request.Context(), req.AgentCodes, req.Enabled); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "count": len(req.AgentCodes), "enabled": req.Enabled})
}

// switchManagedAgentStatus 控真实坐席工作状态（ONLINE/RESTING/BUSY/OFFLINE…）。
func (d *Deps) switchManagedAgentStatus(c *gin.Context) {
	cli, ok := d.openapiClient(c)
	if !ok {
		return
	}
	var req struct {
		Number string `json:"number"`
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Number == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 number"})
		return
	}
	if _, err := cli.SwitchAgentStatus(c.Request.Context(), req.Number, req.Status); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "number": req.Number, "status": req.Status})
}

// preflight 业务测试场景就绪自检：采集运行态快照，对每类场景给出可操作诊断。
// 服务地址全部取自当前机构配置（机构页维护）。
func (d *Deps) preflight(c *gin.Context) {
	var callCenterURL, callBotURL, otpURL string
	if cred, ok := d.Orgs.CurrentCred(); ok {
		if cred.Mode == "gateway" {
			callCenterURL = cred.GatewayURL
			callBotURL = cred.GatewayURL
			otpURL = cred.GatewayURL
		} else {
			callCenterURL = cred.CallCenterURL
			callBotURL = cred.CallBotURL
			otpURL = cred.OTPURL
		}
	}
	in := preflight.Inputs{
		CallCenterBaseURL: callCenterURL,
		CallBotBaseURL:    callBotURL,
		OTPBaseURL:        otpURL,
		LineDBConnected:   d.Orgs.Current() != "", // 机构已配置即视为可经 OpenAPI 接入
		CustomerGroups:    len(d.Cluster.ListGroups()),
		LineBindings:      len(d.Cluster.ListBindings()),
	}
	in.LineCount = len(d.Cluster.ListBindings()) // 线路以 mock 绑定衡量（线路实体在 Hermes 配置）
	c.JSON(http.StatusOK, gin.H{
		"callCenterTask": preflight.CallCenterTask(in),
		"autoCall":       preflight.AutoCall(in),
		"otp":            preflight.OTP(in),
		"inputs":         in,
	})
}

// bootstrap 一键播种最小可运行配置（行为档+客户组+端口绑定），
// 让业务测试场景由「未就绪」变「就绪」，新用户零配置即可跑通。
func (d *Deps) bootstrap(c *gin.Context) {
	var p bootstrap.Params
	_ = c.ShouldBindJSON(&p)
	if p.OrgCode == "" {
		p.OrgCode = d.Orgs.Current() // 默认用当前测试机构
	}
	res, err := bootstrap.Seed(d.Cluster, p)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": res})
}

// runOTP 经 hermes-otp 下发语音验证码，并观测客户腿是否进入 mock（号码应映射到 mock 线路）。
func (d *Deps) runOTP(c *gin.Context) {
	var s testkit.OTPParams
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, d.Kit.RunOTPObserved(s))
}

func (d *Deps) runOTPBatch(c *gin.Context) {
	var s testkit.OTPBatchParams
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, d.Kit.RunOTPBatchObserved(s))
}

// runCallBot 经 call-bot 创建任务并观测客户腿进入 mock。
func (d *Deps) runCallBot(c *gin.Context) {
	var s testkit.CallBotTaskParams
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, d.Kit.RunCallBotTaskObserved(s))
}

// runAutoCall 经 call-bot 直接发起自动外呼，触发后观测客户腿进 mock。
func (d *Deps) runAutoCall(c *gin.Context) {
	var s testkit.AutoCallParams
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, d.Kit.RunAutoCallObserved(s))
}

// runCallCenterTask 经 call-center 建并启动预测式群呼任务，触发后观测客户腿进 mock。
func (d *Deps) runCallCenterTask(c *gin.Context) {
	var s testkit.CallCenterTaskParams
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, d.Kit.RunCallCenterTaskObserved(s))
}

// runRetrySwitchLine 失败重试换线场景：群呼 1 个号（首呼落拒接线失败→重试换线→接听线 200），
// 断言 ≥2 次 INVITE、X-Line-Name 互异、每次独立 Call-ID、最终接通。
func (d *Deps) runRetrySwitchLine(c *gin.Context) {
	var s testkit.RetrySwitchLineParams
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, d.Kit.RunRetrySwitchLine(s))
}

// listOrgs 列出机构 + 当前测试机构。
func (d *Deps) listOrgs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"orgs": d.Orgs.List(), "current": d.Orgs.Current()})
}

// upsertOrg 新增/编辑机构 OpenAPI 接入配置（地址/密钥/模式）。
func (d *Deps) upsertOrg(c *gin.Context) {
	var o orgcfg.OrgConfig
	if err := c.ShouldBindJSON(&o); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out, err := d.Orgs.Upsert(o)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, out)
}

// deleteOrg 删除机构配置。
func (d *Deps) deleteOrg(c *gin.Context) {
	if err := d.Orgs.Delete(c.Param("orgCode")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// pingOrg 用一次 OpenAPI 调用验证某机构凭据是否连通（默认当前机构）。
func (d *Deps) pingOrg(c *gin.Context) {
	var req struct {
		OrgCode string `json:"orgCode"`
	}
	_ = c.ShouldBindJSON(&req)
	cred, ok := d.orgCred(req.OrgCode)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "机构未配置"})
		return
	}
	err := hermesopenapi.New(cred).Ping(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "msg": "OpenAPI 连通"})
}

// currentOrg 返回当前测试机构。
func (d *Deps) currentOrg(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"current": d.Orgs.Current()})
}

// setCurrentOrg 一键切换当前测试机构（后续任务默认用该机构）。
func (d *Deps) setCurrentOrg(c *gin.Context) {
	var req struct {
		OrgCode string `json:"orgCode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := d.Orgs.SetCurrent(req.OrgCode); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "current": d.Orgs.Current()})
}

// orgCred 取机构凭据（空 orgCode=当前机构）。
func (d *Deps) orgCred(orgCode string) (hermesopenapi.Cred, bool) {
	if orgCode == "" {
		return d.Orgs.CurrentCred()
	}
	return d.Orgs.CredOf(orgCode)
}

// openapiTts 经 OpenAPI 读当前机构可用 TTS 模板（外呼/群呼表单联动选择）。
func (d *Deps) openapiTts(c *gin.Context) {
	cred, ok := d.orgCred("")
	if !ok {
		c.JSON(http.StatusOK, gin.H{"tts": []any{}, "error": "机构未配置"})
		return
	}
	tts, err := hermesopenapi.New(cred).ListTts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"tts": []any{}, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tts": tts})
}

// openapiAgentGroups 机构技能组（群呼指定接听坐席组，表单联动）。
// 优先用 basic 的坐席组接口（带真名 + 坐席数）；不可用时降级为从机构坐席聚合（仅 code+count，无真名）。
func (d *Deps) openapiAgentGroups(c *gin.Context) {
	cred, ok := d.orgCred("")
	if !ok {
		c.JSON(http.StatusOK, gin.H{"groups": []any{}, "error": "机构未配置"})
		return
	}
	cli := hermesopenapi.New(cred)
	type grp struct {
		Code  string `json:"code"`
		Name  string `json:"name,omitempty"`
		Count int64  `json:"count"`
	}
	// 优先：basic 坐席组接口（带真名）
	if ags, err := cli.ListAgentGroups(c.Request.Context(), cred.OrgCode); err == nil && len(ags) > 0 {
		out := make([]grp, 0, len(ags))
		for _, g := range ags {
			out = append(out, grp{Code: g.Code, Name: g.Name, Count: g.Count})
		}
		c.JSON(http.StatusOK, gin.H{"groups": out})
		return
	}
	// 降级：从机构坐席聚合（无真名）
	agents, _, err := cli.ListAgents(c.Request.Context(), 1, 500)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"groups": []any{}, "error": err.Error()})
		return
	}
	seen := map[string]int64{}
	for _, a := range agents {
		if a.AgentGroupCode != "" {
			seen[a.AgentGroupCode]++
		}
	}
	groups := make([]grp, 0, len(seen))
	for code, n := range seen {
		groups = append(groups, grp{Code: code, Count: n})
	}
	c.JSON(http.StatusOK, gin.H{"groups": groups})
}

// receiveCallback 接收 Hermes 回调（webhook）：记录 + 按 callUuid 关联进通话链路。
func (d *Deps) receiveCallback(c *gin.Context) {
	source := c.Param("source")
	body, _ := io.ReadAll(c.Request.Body)
	rec := d.CB.Record(source, c.ClientIP(), body)
	if rec.CallUUID != "" {
		sess := d.Bus.EnsureByCallID(rec.CallUUID, "callback", "Hermes 回调 "+source)
		d.Bus.Emit(sess, "", tracelog.ChanFlow, tracelog.DirIn, "回调:"+orStrA(rec.Event, source),
			"收到 Hermes 回调（"+source+"）"+clipStr(string(body), 120),
			map[string]string{"source": source, "event": rec.Event, "callUuid": rec.CallUUID})
	}
	logrus.Infof("收到 Hermes 回调 source=%s event=%s callUuid=%s", source, rec.Event, rec.CallUUID)
	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "received"})
}

// queryCallbacks 查询回调记录（带筛选：source/event/orgCode/callUuid/keyword）。
func (d *Deps) queryCallbacks(c *gin.Context) {
	recs := d.CB.Query(callbacks.Filter{
		Source:   c.Query("source"),
		Event:    c.Query("event"),
		OrgCode:  c.Query("orgCode"),
		CallUUID: c.Query("callUuid"),
		Keyword:  c.Query("keyword"),
		Limit:    atoiDefault(c.Query("limit"), 200),
	})
	c.JSON(http.StatusOK, gin.H{"callbacks": recs})
}

func (d *Deps) queryCallRecords(c *gin.Context) {
	f := cluster.CallRecordFilter{
		Scenario:       c.Query("scenario"),
		Status:         c.Query("status"),
		OrgCode:        c.Query("orgCode"),
		RunID:          c.Query("runId"),
		TaskName:       c.Query("taskName"),
		TaskCode:       c.Query("taskCode"),
		CustomerGroup:  c.Query("customerGroup"),
		CustomerNumber: c.Query("customerNumber"),
		AgentGroupCode: c.Query("agentGroupCode"),
		AgentNumber:    c.Query("agentNumber"),
		LineCode:       c.Query("lineCode"),
		TraceID:        c.Query("traceId"),
		CallUUID:       c.Query("callUuid"),
		Keyword:        c.Query("keyword"),
		Page:           atoiDefault(c.Query("page"), 1),
		PageSize:       atoiDefault(c.Query("pageSize"), 50),
	}
	if t, ok := parseTimeQuery(c.Query("startedFrom")); ok {
		f.StartedFrom = &t
	}
	if t, ok := parseTimeQuery(c.Query("startedTo")); ok {
		f.StartedTo = &t
	}
	records, meta, err := d.Repo.ListCallRecords(c.Request.Context(), f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if records == nil {
		records = []cluster.CallRecordRow{}
	}
	d.enrichCallRecordTraceIDs(c.Request.Context(), records)
	c.JSON(http.StatusOK, cluster.CallRecordPage{
		Records: records, Total: meta.Total, Page: int(meta.Page), PageSize: int(meta.PageSize),
	})
}

// enrichCallRecordTraceIDs 给通话记录补齐 traceId：事实表 mock_call 以 call_uuid 作为跨腿关联锚，
// trace 表 mock_trace_leg 则以 session_id 作为前端可打开的 trace id、以 call_uuid 保存关联锚。
// 早期/实时记录可能只落了 call_uuid 没落 trace_id；查询时补齐，避免各页面显示「无对应 trace」。
func (d *Deps) enrichCallRecordTraceIDs(ctx context.Context, records []cluster.CallRecordRow) {
	for i := range records {
		if strings.TrimSpace(records[i].TraceID) != "" {
			continue
		}
		if traceID := d.traceIDForCallUUID(ctx, records[i].CallUUID); traceID != "" {
			records[i].TraceID = traceID
		}
	}
}

func (d *Deps) traceIDForCallUUID(ctx context.Context, callUUID string) string {
	callUUID = strings.TrimSpace(callUUID)
	if callUUID == "" {
		return ""
	}
	if d.Bus != nil {
		for _, s := range d.Bus.Sessions() {
			if s != nil && strings.TrimSpace(s.CallID) == callUUID {
				return s.ID
			}
		}
	}
	if d.Repo != nil {
		legs, err := d.Repo.ListTraceLegsByCallUUID(ctx, callUUID)
		if err == nil && len(legs) > 0 {
			return legs[0].SessionID
		}
	}
	return ""
}

// saveCallRecord 由前端坐席软电话在每通外呼结束时回存「坐席侧」记录（含期望/实际/断言）。
// 这是坐席腿视角，区别于 mock 被叫腿自动落的 sip-inbound 记录；不设 TraceID（避免按 trace 合并进被叫腿行），
// traceId 仅存进 detail 供「查看链路」跳转。
func (d *Deps) saveCallRecord(c *gin.Context) {
	var in struct {
		CallID        string `json:"callId"`
		AgentNumber   string `json:"agentNumber"`
		Customer      string `json:"customer"`
		Expect        string `json:"expectOutcome"`
		ExpectFault   string `json:"expectFault"`
		Disabled      bool   `json:"expectDisabled"`
		Answered      bool   `json:"answered"`
		Inbound       bool   `json:"inbound"` // true=被叫来电（群呼/转接进坐席），false=坐席外呼
		EndCause      string `json:"endCause"`
		Verdict       string `json:"verdict"` // pass|fail|unknown
		VerdictText   string `json:"verdictReason"`
		TraceID       string `json:"traceId"`
		DisplayCaller string `json:"displayCaller"`
		StartedAtMs   int64  `json:"startedAtMs"`
		AnsweredAtMs  int64  `json:"answeredAtMs"`
		DurationMs    int64  `json:"durationMs"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if in.CallID == "" || in.AgentNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "callId / agentNumber 必填"})
		return
	}
	started := time.Now()
	if in.StartedAtMs > 0 {
		started = time.UnixMilli(in.StartedAtMs)
	}
	var answeredAt *time.Time
	if in.AnsweredAtMs > 0 {
		t := time.UnixMilli(in.AnsweredAtMs)
		answeredAt = &t
	}
	status := cluster.CallRecordStatusEnded
	if !in.Answered {
		status = cluster.CallRecordStatusFailed
	}
	detail, _ := json.Marshal(map[string]any{
		"traceId": in.TraceID, "expectOutcome": in.Expect, "expectFault": in.ExpectFault,
		"expectDisabled": in.Disabled, "verdict": in.Verdict, "verdictReason": in.VerdictText,
		"displayCaller": in.DisplayCaller, "answered": in.Answered, "inbound": in.Inbound,
	})
	var endedAt *time.Time
	if in.DurationMs > 0 {
		endBase := started
		if answeredAt != nil {
			endBase = *answeredAt
		}
		t := endBase.Add(time.Duration(in.DurationMs) * time.Millisecond)
		endedAt = &t
	}
	// 区分坐席外呼 / 被叫来电（群呼转接进坐席腿）
	scenario, source, dir, callType := "agent-call", "agent", "AGENT_TO_CUSTOMER", "agent-outbound"
	recPrefix := "agent-call:"
	summary := in.VerdictText
	if in.Inbound {
		scenario, dir, callType = "agent-inbound", "TRANSFER_TO_AGENT", "agent-inbound"
		recPrefix = "agent-inbound:"
		if in.Answered {
			summary = "坐席 " + in.AgentNumber + " 接听了转接来电"
		} else {
			summary = "坐席 " + in.AgentNumber + " 未接转接来电（" + in.EndCause + "）"
		}
	}
	// call_uuid 取 Hermes 业务 sessionId（与被叫腿 / trace 关联锚一致）。BizType 5 位前缀语义见 Hermes
	// BizType.kt：坐席手动外呼=CCMDL、呼入=CCINC、群呼=CCTSK……
	//   - 坐席外呼：in.CallID 是前端 jssip 生成的裸 v7 uuid，需补 CCMDL 前缀
	//     （前端注 x-session-id: CCMDL{uuid}，后端 extractValidCallUuid 剥前缀取 uuid 后 bridge 回 mock 被叫腿仍是 CCMDL{uuid}）。
	//   - 坐席接来电：in.CallID 直接取自 INVITE 的 x-session-id 头，已含正确前缀（CCINC/CCTSK…），不能再叠 CCMDL。
	callUUID := in.CallID
	if !in.Inbound {
		callUUID = "CCMDL" + in.CallID
	}
	row := cluster.CallRecordRow{
		RecordID:       recPrefix + in.CallID,
		Scenario:       scenario,
		Source:         source,
		AgentNumber:    in.AgentNumber,
		CustomerNumber: in.Customer,
		Direction:      dir,
		CallType:       callType,
		Status:         status,
		Result:         summary,
		TraceID:        in.TraceID,
		CallUUID:       callUUID,
		StartedAt:      started,
		AnsweredAt:     answeredAt,
		EndedAt:        endedAt,
		DurationMs:     in.DurationMs,
		LastEventAt:    time.Now(),
		DetailJSON:     string(detail),
		LastSummary:    summary,
	}
	if err := d.Repo.SaveCallRecord(c.Request.Context(), &row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "recordId": row.RecordID})
}

func orStrA(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func clipStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func parseTimeQuery(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func (d *Deps) testRuns(c *gin.Context) { c.JSON(http.StatusOK, d.Kit.Recent()) }

// ---- 群呼任务生命周期管理（暂停/恢复/取消/查状态）----
// taskCode 来自 runCallCenterTask 创建响应（Hermes data.code）。

func (d *Deps) taskAction(c *gin.Context, fn func(string) ([]byte, error)) {
	if d.Orch == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "业务编排器未注入"})
		return
	}
	taskCode := c.Param("taskCode")
	if taskCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "taskCode 必填"})
		return
	}
	out, err := fn(taskCode)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "raw": json.RawMessage(out)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "taskCode": taskCode, "data": json.RawMessage(out)})
}

func (d *Deps) pauseCallCenterTask(c *gin.Context)  { d.taskAction(c, d.Orch.PauseCallCenterTask) }
func (d *Deps) resumeCallCenterTask(c *gin.Context) { d.taskAction(c, d.Orch.ResumeCallCenterTask) }
func (d *Deps) cancelCallCenterTask(c *gin.Context) { d.taskAction(c, d.Orch.CancelCallCenterTask) }
func (d *Deps) callCenterTaskStatus(c *gin.Context) { d.taskAction(c, d.Orch.CallCenterTaskStatus) }

func (d *Deps) listCalls(c *gin.Context)       { c.JSON(http.StatusOK, d.Tracker.Recent()) }
func (d *Deps) listActiveCalls(c *gin.Context) { c.JSON(http.StatusOK, d.Tracker.Active()) }
func (d *Deps) stats(c *gin.Context)           { c.JSON(http.StatusOK, d.Tracker.Stats()) }

// audioFile 媒体文件元信息。
type audioFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// listAudio 列出 AudioDir 下的 .wav 预置音频（供放音行为档下拉选择）。
func (d *Deps) listAudio(c *gin.Context) {
	entries, err := os.ReadDir(d.Cfg.AudioDir)
	if err != nil {
		c.JSON(http.StatusOK, []audioFile{})
		return
	}
	out := make([]audioFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".wav") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, audioFile{Name: e.Name(), Size: fi.Size()})
	}
	c.JSON(http.StatusOK, out)
}

// uploadAudio 上传一个 .wav 到 AudioDir（multipart 字段 file）。
func (d *Deps) uploadAudio(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 file 字段"})
		return
	}
	name := filepath.Base(fh.Filename)
	if !strings.EqualFold(filepath.Ext(name), ".wav") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 .wav"})
		return
	}
	if err := os.MkdirAll(d.Cfg.AudioDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	dst := filepath.Join(d.Cfg.AudioDir, name)
	if err := c.SaveUploadedFile(fh, dst); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, audioFile{Name: name, Size: fh.Size})
}

// MountFrontend 把 embed 的前端挂到 Gin；非 /api 路径做 SPA fallback 到 index.html。
func MountFrontend(r *gin.Engine, distFS fs.FS) {
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if strings.HasPrefix(p, "/assets/") || p == "/favicon.svg" {
			name := strings.TrimPrefix(p, "/")
			if data, err := fs.ReadFile(distFS, name); err == nil {
				c.Data(http.StatusOK, contentType(name), data)
				return
			}
			c.JSON(http.StatusNotFound, gin.H{"error": "asset not found", "path": p})
			return
		}
		name := strings.TrimPrefix(p, "/")
		if name == "" {
			name = "index.html"
		}
		if data, err := fs.ReadFile(distFS, name); err == nil {
			c.Data(http.StatusOK, contentType(name), data)
			return
		}
		if idx, err := fs.ReadFile(distFS, "index.html"); err == nil {
			c.Data(http.StatusOK, "text/html; charset=utf-8", idx)
			return
		}
		c.Status(http.StatusNotFound)
	})
}

func contentType(name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// registerHermesProxy 注册「浏览器 → Hermes」反向代理，让 mock 前端里的 jssip 坐席软电话
// 同源调到 call-center / hermes-ws（免 CORS、免自签证书）。真实环境这些前缀由网关转发并注入身份头；
// 直连模式由本代理注入 ORG_CODE_KEY/AGENT_NUMBER_KEY（坐席号取自前端请求头 X-Agent-Number）。
// 目标地址按请求时的「当前机构配置」动态解析——改机构页/切机构即生效，无需重启。
//
//	/agent-workbench/api/ws   → hermes-ws（工作台 WebSocket，透传 Upgrade）
//	/agent-workbench/sdk/**   → call-center（坐席工作台 SDK：webrtc/addr、status/switch…）
//	/call-center/**（剥前缀）  → call-center（兼容前端带 /call-center 前缀的写法）
func registerHermesProxy(r *gin.Engine, orgs *orgcfg.Store) {
	ccBase := func() string {
		oc, ok := orgs.CurrentConfig()
		if !ok {
			return ""
		}
		if u := strings.TrimRight(strings.TrimSpace(oc.CallCenterURL), "/"); u != "" {
			return u
		}
		// 网关模式：工作台 SDK / public SIP auth 仍落到 call-center 产品路由下。
		// 前端请求本地 /call-center/**；反代剥本地前缀后，这里把目标 base 固定到
		// gatewayUrl/call-center，最终目标形如 /call-center/agent-workbench/sdk/**。
		gw := strings.TrimSpace(oc.GatewayURL)
		if gw == "" {
			return ""
		}
		u := strings.TrimRight(normalizeProxyTarget(gw), "/")
		if !strings.HasSuffix(u, "/call-center") {
			u += "/call-center"
		}
		return u
	}
	wsBase := func() string {
		if oc, ok := orgs.CurrentConfig(); ok {
			return oc.AgentWSHost()
		}
		return ""
	}

	r.Any("/call-center/*path", func(c *gin.Context) {
		// 容器内 call-center 的 endpoint 不带 /call-center 前缀，剥掉再转发。
		proxyHTTP(c, ccBase(), strings.TrimPrefix(c.Request.URL.Path, "/call-center"), orgs)
	})
	r.Any("/agent-workbench/*path", func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/agent-workbench/api/ws") {
			proxyWS(c, wsBase(), c.Request.URL.Path)
			return
		}
		proxyHTTP(c, ccBase(), c.Request.URL.Path, orgs)
	})
}

// proxyHTTP 把当前请求反代到 targetBase（如 http://hermes-call-center:8080），用 newPath 覆盖路径，
// 并注入直连模式身份头（ORG_CODE_KEY + AGENT_NUMBER_KEY/AGENT_CODE_KEY）。
func proxyHTTP(c *gin.Context, targetBase, newPath string, orgs *orgcfg.Store) {
	if targetBase == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "当前机构未配置 call-center 地址（direct 填 callCenterUrl；gateway 填网关地址即可自动回退）"})
		return
	}
	u, err := url.Parse(normalizeProxyTarget(targetBase))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	agent := c.GetHeader("X-Agent-Number")
	org := orgs.Current()
	targetPath := joinProxyPath(u.Path, newPath)
	// 坐席 SIP 保活心跳是「30s × N 坐席」的高频固定流量、成功时无诊断价值，降到 Debug 避免刷屏；
	// 失败不受影响——仍由下方 ModifyResponse（≥400 Warn）/ ErrorHandler（连接错 Error）上报，藏不住。
	logf := logrus.Infof
	if strings.Contains(c.Request.URL.Path, "/public/auth/sip") {
		logf = logrus.Debugf
	}
	logf("Hermes 反代: %s %s → %s://%s%s (agent=%s org=%s)", c.Request.Method, c.Request.URL.Path, u.Scheme, u.Host, targetPath, agent, org)
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
			req.Host = u.Host
			req.URL.Path = targetPath
			req.URL.RawPath = ""
			req.Header.Del("X-Agent-Number")
			if org != "" {
				req.Header.Set("ORG_CODE_KEY", org)
			}
			if agent != "" {
				req.Header.Set("AGENT_NUMBER_KEY", agent)
				req.Header.Set("AGENT_CODE_KEY", agent)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("X-Hermes-Mock-Proxy-Target", u.Scheme+"://"+u.Host+targetPath)
			if resp.StatusCode >= 400 {
				logrus.Warnf("Hermes 反代响应 %d: %s%s", resp.StatusCode, u.Host, targetPath)
			}
			ct := strings.ToLower(resp.Header.Get("Content-Type"))
			if resp.StatusCode < 300 && strings.Contains(ct, "text/html") {
				raw, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				msg := "Hermes 反代目标返回了 HTML，通常表示机构 gatewayUrl/callCenterUrl 指到了管理前端或缺少 /api/call-center 前缀"
				logrus.Warnf("%s: target=%s://%s%s body=%q", msg, u.Scheme, u.Host, targetPath, clip(string(raw), 160))
				body, _ := json.Marshal(gin.H{
					"error":  msg,
					"target": u.Scheme + "://" + u.Host + targetPath,
				})
				resp.StatusCode = http.StatusBadGateway
				resp.Status = http.StatusText(http.StatusBadGateway)
				resp.Header.Set("Content-Type", "application/json; charset=utf-8")
				resp.Header.Del("Content-Encoding")
				resp.Header.Del("Content-Length")
				resp.Body = io.NopCloser(bytes.NewReader(body))
				resp.ContentLength = int64(len(body))
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logrus.Errorf("Hermes 反代失败: %s%s: %v", u.Host, targetPath, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"反代到 ` + u.Host + targetPath + ` 失败: ` + strings.ReplaceAll(err.Error(), `"`, "'") + `"}`))
		},
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

func joinProxyPath(basePath, reqPath string) string {
	basePath = strings.TrimRight(strings.TrimSpace(basePath), "/")
	reqPath = "/" + strings.TrimLeft(strings.TrimSpace(reqPath), "/")
	if basePath == "" {
		return reqPath
	}
	return basePath + reqPath
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// proxyWS 把工作台 WebSocket 反代到 hermes-ws。
// hostPort 支持：hermes-ws:8081（集群内，走 http）/ wss://xxx 或 https://xxx（公网 ingress，走 TLS）/
// 裸域名无端口（按公网惯例默认 https）。httputil.ReverseProxy 自动透传 WebSocket Upgrade。
func proxyWS(c *gin.Context, hostPort, path string) {
	if hostPort == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "当前机构未配置 hermes-ws 地址（机构页 agentWsUrl）"})
		return
	}
	u, err := url.Parse(normalizeProxyTarget(hostPort))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	logrus.Infof("工作台 WS 反代: %s → %s://%s%s", c.Request.RemoteAddr, u.Scheme, u.Host, path)
	proxy := &httputil.ReverseProxy{Director: func(req *http.Request) {
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
		req.Host = u.Host
		req.URL.Path = path
	}}
	proxy.ServeHTTP(c.Writer, c.Request)
}

// normalizeProxyTarget 把机构页填的地址规整成可转发的 http(s) URL：
// ws→http、wss→https；无 scheme 时——带端口按集群内服务走 http（如 hermes-ws:8081），
// 不带端口按公网域名走 https（如 hermes-webrtc-test.financifyx.com，ingress 仅 TLS）。
func normalizeProxyTarget(target string) string {
	target = strings.TrimSpace(strings.TrimRight(target, "/"))
	switch {
	case strings.HasPrefix(target, "wss://"):
		return "https://" + strings.TrimPrefix(target, "wss://")
	case strings.HasPrefix(target, "ws://"):
		return "http://" + strings.TrimPrefix(target, "ws://")
	case strings.Contains(target, "://"):
		return target
	}
	host := target
	if i := strings.Index(host, "/"); i > 0 {
		host = host[:i]
	}
	if strings.Contains(host, ":") {
		return "http://" + target
	}
	return "https://" + target
}

// traceSessionFromEntity 把落库的单腿链路（mock_trace_leg）+事件装配回 tracelog.Session（前端 trace 页模型）。
// 单腿：Legs 从事件的 leg 去重收集（前端梯形图主要据 events 的 SIP From/To 重建，legs 仅辅助）。
func traceSessionFromEntity(row *entity.TraceLeg) *tracelog.Session {
	if row == nil {
		return nil
	}
	s := &tracelog.Session{
		ID:        row.SessionID,
		Title:     row.Title,
		Kind:      row.Kind,
		CallID:    row.CallUUID,
		StartedAt: row.StartedAt,
		UpdatedAt: row.UpdatedAt,
	}
	legSeen := map[string]bool{}
	for _, er := range row.Events {
		var headers []tracelog.HeaderKV
		if strings.TrimSpace(er.HeadersJSON) != "" {
			_ = json.Unmarshal([]byte(er.HeadersJSON), &headers)
		}
		s.Events = append(s.Events, tracelog.Event{
			Seq: er.Seq, TS: er.TS, Session: er.SessionID, Leg: er.Leg,
			Channel: tracelog.Channel(er.Channel), Dir: tracelog.Dir(er.Dir),
			Method: er.Method, Summary: er.Summary, Headers: headers,
			Raw: er.RawMessage, CallID: row.CallUUID,
		})
		if er.Leg != "" && !legSeen[er.Leg] {
			legSeen[er.Leg] = true
			s.Legs = append(s.Legs, er.Leg)
		}
	}
	return s
}
