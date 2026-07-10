package api

import (
	"net/http"
	"strconv"

	"hermes-mock/internal/hermesopenapi"

	"github.com/gin-gonic/gin"
)

// stratflow.go —— 策略流应用层 mock 编排台的 HTTP 面（透传到 hermes-stratflow OpenAPI）。
//
// 定位：这是「应用层 mock（stratflow 合成回执）」的编排/观测台，与 mock 后端的 SIP 被叫腿正交——
// 用它测策略图分支/回执逻辑时**不产真实 SIP**（见 docs/SCOPE.md 补注 + DECISIONS 同日条目）。
// 全部经当前机构 OpenAPI 凭据调 stratflow，mock 自身不持有任何 stratflow 状态。

// sfClient 取当前机构 OpenAPI 客户端（复用坐席管理同一入口）。
func (d *Deps) sfClient(c *gin.Context) (*hermesopenapi.Client, bool) { return d.openapiClient(c) }

// sfErr 统一把 stratflow 调用错误落 502（上游/网关问题），成功落 200。
func sfErr(c *gin.Context, err error) bool {
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return true
	}
	return false
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
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	enabled, _ := strconv.ParseBool(c.Query("enabled"))
	v, err := cli.StratflowSetGlobalGate(c.Request.Context(), enabled)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

func (d *Deps) sfSetSchemeGate(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	enabled, _ := strconv.ParseBool(c.Query("enabled"))
	v, err := cli.StratflowSetSchemeGate(c.Request.Context(), c.Param("defCode"), enabled)
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
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	paused, _ := strconv.ParseBool(c.Query("paused"))
	v, err := cli.StratflowSetDeliveryPaused(c.Request.Context(), paused)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, v)
}

func (d *Deps) sfSetReceiptWindow(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	seconds, err := strconv.ParseInt(c.Query("seconds"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "seconds 需为整数秒（0=关闭，非 0 须 ≥60）"})
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
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	if err := cli.StratflowClearMock(c.Request.Context(), c.DefaultQuery("scope", "all")); sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
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
	list, err := cli.StratflowListPlans(c.Request.Context(), runCode)
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"plans": list})
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

func (d *Deps) sfRuns(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	list, err := cli.StratflowRuns(c.Request.Context(), c.Param("code"))
	if sfErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": list})
}

func (d *Deps) sfRunProgress(c *gin.Context) {
	cli, ok := d.sfClient(c)
	if !ok {
		return
	}
	start, end := c.Query("uploadStartTime"), c.Query("uploadEndTime")
	if start == "" || end == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 uploadStartTime/uploadEndTime（UTC yyyy-MM-dd HH:mm:ss，跨度≤31天）"})
		return
	}
	v, err := cli.StratflowRunProgress(c.Request.Context(), c.Param("code"), c.Param("rid"), start, end)
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
