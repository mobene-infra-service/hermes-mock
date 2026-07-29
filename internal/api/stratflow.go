package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hermes-mock/internal/hermesopenapi"

	"github.com/gin-gonic/gin"
)

func registerStratflowRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/stratflow/mock/gate", d.sfGate)
	g.PUT("/stratflow/mock/gate/global", d.sfSetGlobalGate)
	g.PUT("/stratflow/mock/gate/scheme/:defCode", d.sfSetSchemeGate)
	g.DELETE("/stratflow/mock/gate/scheme/:defCode", d.sfClearSchemeGate)
	g.PUT("/stratflow/mock/delivery", d.sfSetDeliveryPaused)
	g.PUT("/stratflow/mock/receipt-window", d.sfSetReceiptWindow)
	g.GET("/stratflow/mock/config/:versionCode", d.sfListConfig)
	g.PUT("/stratflow/mock/config/:versionCode/:nodeId", d.sfPutConfig)
	g.DELETE("/stratflow/mock/config/:versionCode/:nodeId", d.sfDeleteConfig)
	g.DELETE("/stratflow/mock/all", d.sfClearMock)
	g.GET("/stratflow/mock/plans", d.sfListPlans)
	g.GET("/stratflow/mock/decisions", d.sfListDecisions)
	g.POST("/stratflow/mock/plans/:actionCode/requeue", d.sfRequeuePlan)
	g.GET("/stratflow/workflows", d.sfWorkflows)
	g.GET("/stratflow/workflows/:defCode", d.sfWorkflowDetail)
	g.GET("/stratflow/collections", d.sfCollections)
	g.GET("/stratflow/collections/:code/fields", d.sfCollectionFields)
	g.GET("/stratflow/collections/:code/bindings", d.sfCollectionBindings)
	g.GET("/stratflow/collections/:code/version-runs", d.sfVersionRuns)
	g.GET("/stratflow/collections/:code/version-runs/:defCode/:versionCode/progress", d.sfVersionRunProgress)
	g.GET("/stratflow/collections/:code/executions", d.sfExecutions)
	g.GET("/stratflow/collections/:code/executions/:rid/progress", d.sfExecutionProgress)
	g.POST("/stratflow/collections/:code/import", d.sfImport)
}

// stratflow.go —— 策略流应用层 mock 编排台的 HTTP 面（透传到 hermes-stratflow OpenAPI）。
//
// 定位：这是「应用层 mock（stratflow 合成回执）」的编排/观测台，与 mock 后端的 SIP 被叫腿正交——
// 用它测策略图分支/回执逻辑时**不产真实 SIP**（见 docs/SCOPE.md 补注 + DECISIONS 同日条目）。
// 全部经当前机构 OpenAPI 凭据调 stratflow，mock 自身不持有任何 stratflow 状态。

// sfClient 取当前机构 OpenAPI 客户端（复用坐席管理同一入口）。
func (d *Deps) sfClient(c *gin.Context) (*hermesopenapi.Client, bool) {
	orgCode := strings.TrimSpace(c.GetHeader("X-Hermes-Mock-Org"))
	cred, ok := d.orgCred(orgCode)
	if !ok {
		if orgCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "当前机构未配置 OpenAPI 凭据（去「机构」页配置）"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "指定机构未配置 OpenAPI 凭据: " + orgCode})
		}
		return nil, false
	}
	return hermesopenapi.New(cred), true
}

// sfErr 保留 Hermes 参数/权限/冲突语义；仅网络与上游 5xx 映射为网关错误。
func sfErr(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	status := http.StatusBadGateway
	body := gin.H{"error": err.Error()}
	var upstream *hermesopenapi.UpstreamError
	if errors.As(err, &upstream) {
		if upstream.BusinessCode != 0 {
			body["upstreamCode"] = upstream.BusinessCode
		}
		switch upstream.Kind {
		case "timeout":
			status = http.StatusGatewayTimeout
		case "http":
			if upstream.HTTPStatus >= 400 && upstream.HTTPStatus < 500 {
				status = upstream.HTTPStatus
			}
		case "business":
			switch upstream.BusinessCode {
			case 1001, 1002:
				status = http.StatusUnauthorized
			default:
				status = http.StatusBadRequest
			}
		}
	}
	c.JSON(status, body)
	return true
}

// —— ① gate 开关 ——

func (d *Deps) sfGate(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	v, err := cli.StratflowGate(c.Request.Context())
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

func (d *Deps) sfSetGlobalGate(c *gin.Context) {
	mode, ok := sfDispatchMode(c)
	if !ok {
		return
	}
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	v, err := cli.StratflowSetGlobalMode(c.Request.Context(), mode)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

func (d *Deps) sfSetSchemeGate(c *gin.Context) {
	mode, parsed := sfDispatchMode(c)
	if !parsed {
		return
	}
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	v, err := cli.StratflowSetSchemeMode(c.Request.Context(), c.Param("defCode"), mode)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

func (d *Deps) sfClearSchemeGate(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	v, err := cli.StratflowClearSchemeGate(c.Request.Context(), c.Param("defCode"))
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

func (d *Deps) sfSetDeliveryPaused(c *gin.Context) {
	paused, ok := sfStrictBool(c.Query("paused"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "paused 须为 true 或 false"})
		return
	}
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	v, err := cli.StratflowSetDeliveryPaused(c.Request.Context(), paused)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

func (d *Deps) sfSetReceiptWindow(c *gin.Context) {
	seconds, err := strconv.ParseInt(c.Query("seconds"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "seconds 需为整数秒（0=关闭，非 0 须 ≥60）"})
		return
	}
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	v, err := cli.StratflowSetReceiptWindow(c.Request.Context(), seconds)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

// —— ② per-node 结局配置 ——

func (d *Deps) sfListConfig(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	list, err := cli.StratflowListConfig(c.Request.Context(), c.Param("versionCode"))
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"nodes": list})
}

func (d *Deps) sfPutConfig(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	var cfg hermesopenapi.SfMockNodeConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := cli.StratflowPutConfig(c.Request.Context(), c.Param("versionCode"), c.Param("nodeId"), cfg); sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (d *Deps) sfDeleteConfig(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	if err := cli.StratflowDeleteConfig(c.Request.Context(), c.Param("versionCode"), c.Param("nodeId")); sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// —— ③ 清空 / 在途计划 ——

func (d *Deps) sfClearMock(c *gin.Context) {
	scope := c.DefaultQuery("scope", "all")
	if scope != "all" && scope != "plans" && scope != "config" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scope 须为 all / plans / config"})
		return
	}
	confirm := false
	if scope == "all" || scope == "plans" {
		var err error
		confirm, err = strconv.ParseBool(c.Query("confirm"))
		if err != nil || !confirm {
			c.JSON(http.StatusBadRequest, gin.H{"error": "清理在途计划须显式传 confirm=true"})
			return
		}
	}
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	if err := cli.StratflowClearMock(c.Request.Context(), scope, confirm); sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// sfDispatchMode 优先读取新三态 mode；enabled 只作为旧客户端兼容入口，且必须严格可解析。
func sfDispatchMode(c *gin.Context) (string, bool) {
	if raw := strings.TrimSpace(c.Query("mode")); raw != "" {
		mode := strings.ToUpper(raw)
		if mode != "REAL" && mode != "MOCK" && mode != "PAUSED" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "mode 须为 REAL / MOCK / PAUSED"})
			return "", false
		}
		return mode, true
	}
	enabled, ok := sfStrictBool(c.Query("enabled"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "须提供 mode=REAL|MOCK|PAUSED，或 enabled=true|false"})
		return "", false
	}
	if enabled {
		return "MOCK", true
	}
	return "REAL", true
}

func sfStrictBool(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func (d *Deps) sfListPlans(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	runCode := c.Query("runCode")
	if runCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 runCode"})
		return
	}
	status := strings.ToUpper(strings.TrimSpace(c.Query("status")))
	if status != "" && status != "PENDING" && status != "DEAD" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status 须为 PENDING 或 DEAD"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	list, err := cli.StratflowListPlansByStatus(ctx, runCode, status)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"plans": list})
}

func (d *Deps) sfListDecisions(c *gin.Context) {
	runCode := strings.TrimSpace(c.Query("runCode"))
	if runCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 runCode"})
		return
	}
	status := strings.ToUpper(strings.TrimSpace(c.Query("status")))
	if status != "" && status != "PENDING" && status != "DEAD" && status != "DONE" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status 须为 PENDING / DEAD / DONE"})
		return
	}
	pageNo, errNo := strconv.Atoi(c.DefaultQuery("pageNo", "1"))
	pageSize, errSize := strconv.Atoi(c.DefaultQuery("pageSize", "100"))
	if errNo != nil || errSize != nil || pageNo < 1 || pageSize < 1 || pageSize > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pageNo 须 >=1，pageSize 须为 1-200"})
		return
	}
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	page, err := cli.StratflowListDecisions(ctx, runCode, status, pageNo, pageSize)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, page)
}

func (d *Deps) sfRequeuePlan(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	actionCode := c.Param("actionCode")
	if actionCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 actionCode"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if sfErr(c, cli.StratflowRequeuePlan(ctx, actionCode)) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// —— ④ 发现 ——

func (d *Deps) sfWorkflows(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	list, err := cli.StratflowWorkflows(c.Request.Context())
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"workflows": list})
}

func (d *Deps) sfWorkflowDetail(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	v, err := cli.StratflowWorkflowDetail(c.Request.Context(), c.Param("defCode"))
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

func (d *Deps) sfCollections(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	list, err := cli.StratflowCollections(c.Request.Context(), c.Query("name"), c.Query("status"))
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"collections": list})
}

func (d *Deps) sfCollectionFields(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	list, err := cli.StratflowCollectionFields(c.Request.Context(), c.Param("code"))
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"fields": list})
}

func (d *Deps) sfCollectionBindings(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	list, err := cli.StratflowCollectionBindings(c.Request.Context(), c.Param("code"))
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"bindings": list})
}

func (d *Deps) sfVersionRuns(c *gin.Context) {
	pageNumber, errNumber := strconv.Atoi(c.DefaultQuery("pageNumber", "1"))
	pageSize, errSize := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if errNumber != nil || errSize != nil || pageNumber < 1 || pageSize < 1 || pageSize > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pageNumber 须 >=1，pageSize 须为 1-500"})
		return
	}
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	page, err := cli.StratflowVersionRuns(c.Request.Context(), c.Param("code"), pageNumber, pageSize)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, page)
}

func (d *Deps) sfExecutions(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	list, err := cli.StratflowExecutions(c.Request.Context(), c.Param("code"))
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"executions": list})
}

func (d *Deps) sfExecutionProgress(c *gin.Context) {
	start, end := c.Query("uploadStartTime"), c.Query("uploadEndTime")
	if start == "" || end == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 uploadStartTime/uploadEndTime（UTC yyyy-MM-dd HH:mm:ss，跨度≤31天）"})
		return
	}
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	v, err := cli.StratflowExecutionProgress(c.Request.Context(), c.Param("code"), c.Param("rid"), start, end)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

func (d *Deps) sfVersionRunProgress(c *gin.Context) {
	start, end := c.Query("uploadStart"), c.Query("uploadEnd")
	if start == "" || end == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 uploadStart/uploadEnd（ISO-8601 UTC instant，左闭右开且跨度≤30天）"})
		return
	}
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	v, err := cli.StratflowVersionRunProgress(
		c.Request.Context(),
		c.Param("code"),
		c.Param("defCode"),
		c.Param("versionCode"),
		start,
		end,
	)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

// —— ⑤ 触发 ——

func (d *Deps) sfImport(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	var req hermesopenapi.SfImportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Rows) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rows 不能为空"})
		return
	}
	res, err := cli.StratflowImport(c.Request.Context(), c.Param("code"), req)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, res)
}
