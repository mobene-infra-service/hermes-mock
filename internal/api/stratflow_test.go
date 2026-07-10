package api

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// 冒烟：注册策略流路由不应 panic（gin/httprouter 在路径冲突时注册期即 panic），
// 且 18 条 stratflow 路由全部登记。仅校验注册，不触发 handler。
func TestStratflowRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api")
	d := &Deps{}

	// 与 Register 中 stratflow 块保持一致。
	g.GET("/stratflow/mock/gate", d.sfGate)
	g.PUT("/stratflow/mock/gate/global", d.sfSetGlobalGate)
	g.PUT("/stratflow/mock/gate/scheme/:defCode", d.sfSetSchemeGate)
	g.DELETE("/stratflow/mock/gate/scheme/:defCode", d.sfClearSchemeGate)
	g.PUT("/stratflow/mock/delivery", d.sfSetDeliveryPaused)
	g.GET("/stratflow/mock/config/:versionCode", d.sfListConfig)
	g.PUT("/stratflow/mock/config/:versionCode/:nodeId", d.sfPutConfig)
	g.DELETE("/stratflow/mock/config/:versionCode/:nodeId", d.sfDeleteConfig)
	g.DELETE("/stratflow/mock/all", d.sfClearMock)
	g.GET("/stratflow/mock/plans", d.sfListPlans)
	g.GET("/stratflow/workflows", d.sfWorkflows)
	g.GET("/stratflow/workflows/:defCode", d.sfWorkflowDetail)
	g.GET("/stratflow/collections", d.sfCollections)
	g.GET("/stratflow/collections/:code/fields", d.sfCollectionFields)
	g.GET("/stratflow/collections/:code/bindings", d.sfCollectionBindings)
	g.GET("/stratflow/collections/:code/runs", d.sfRuns)
	g.GET("/stratflow/collections/:code/runs/:rid/progress", d.sfRunProgress)
	g.POST("/stratflow/collections/:code/import", d.sfImport)

	got := 0
	for _, ri := range r.Routes() {
		if len(ri.Path) >= 14 && ri.Path[:14] == "/api/stratflow" {
			got++
		}
	}
	if got != 18 {
		t.Fatalf("期望 18 条 stratflow 路由，实际 %d", got)
	}
}
