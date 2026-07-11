package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermes-mock/internal/hermesopenapi"

	"github.com/gin-gonic/gin"
)

// 冒烟：注册策略流路由不应 panic（gin/httprouter 在路径冲突时注册期即 panic），
// 且全部 stratflow 路由登记。仅校验注册，不触发 handler。
func TestStratflowRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api")
	d := &Deps{}

	registerStratflowRoutes(g, d)

	got := 0
	for _, ri := range r.Routes() {
		if len(ri.Path) >= 14 && ri.Path[:14] == "/api/stratflow" {
			got++
		}
	}
	if got != 20 {
		t.Fatalf("期望 20 条 stratflow 路由，实际 %d", got)
	}
}

func TestSfErrMapsUpstreamSemantics(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"business validation", &hermesopenapi.UpstreamError{Kind: "business", BusinessCode: 1000, Message: "bad"}, http.StatusBadRequest},
		{"auth", &hermesopenapi.UpstreamError{Kind: "business", BusinessCode: 1001, Message: "auth"}, http.StatusUnauthorized},
		{"upstream 404", &hermesopenapi.UpstreamError{Kind: "http", HTTPStatus: 404, Message: "missing"}, http.StatusNotFound},
		{"timeout", &hermesopenapi.UpstreamError{Kind: "timeout", Message: "slow"}, http.StatusGatewayTimeout},
		{"transport", &hermesopenapi.UpstreamError{Kind: "transport", Message: "down"}, http.StatusBadGateway},
		{"plain error", errors.New("boom"), http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			if !sfErr(c, tt.err) || w.Code != tt.want {
				t.Fatalf("sfErr code=%d want=%d", w.Code, tt.want)
			}
		})
	}
}

func TestSfDispatchModeStrictValidation(t *testing.T) {
	tests := []struct {
		query    string
		wantMode string
		wantOK   bool
	}{
		{"?mode=mock", "MOCK", true},
		{"?mode=PAUSED", "PAUSED", true},
		{"?enabled=true", "MOCK", true},
		{"?enabled=false", "REAL", true},
		{"?enabled=1", "", false},
		{"?mode=invalid", "", false},
		{"?enabled=maybe", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/gate"+tt.query, nil)
		got, ok := sfDispatchMode(c)
		if got != tt.wantMode || ok != tt.wantOK {
			t.Fatalf("query=%q got=(%q,%v) want=(%q,%v)", tt.query, got, ok, tt.wantMode, tt.wantOK)
		}
		if !tt.wantOK && w.Code != http.StatusBadRequest {
			t.Fatalf("query=%q code=%d want=400", tt.query, w.Code)
		}
	}
}

func TestSfControlRejectsInvalidInputBeforeResolvingClient(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		target  string
		handler func(*Deps, *gin.Context)
	}{
		{"global missing mode", http.MethodPut, "/gate", func(d *Deps, c *gin.Context) { d.sfSetGlobalGate(c) }},
		{"delivery invalid bool", http.MethodPut, "/delivery?paused=maybe", func(d *Deps, c *gin.Context) { d.sfSetDeliveryPaused(c) }},
		{"clear missing confirm", http.MethodDelete, "/all?scope=all", func(d *Deps, c *gin.Context) { d.sfClearMock(c) }},
		{"clear invalid scope", http.MethodDelete, "/all?scope=unknown", func(d *Deps, c *gin.Context) { d.sfClearMock(c) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(tt.method, tt.target, nil)
			tt.handler(&Deps{}, c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code=%d want=400 body=%s", w.Code, w.Body.String())
			}
		})
	}
}
