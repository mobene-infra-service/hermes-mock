package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// 经 RequestLogger + Recovery 跑一次请求，返回响应记录器与本次捕获的日志条目。
func runThroughMiddleware(t *testing.T, method, path string, handler gin.HandlerFunc) (*httptest.ResponseRecorder, []*logrus.Entry) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	hook := logtest.NewLocal(logrus.StandardLogger())
	defer hook.Reset()
	prevLevel := logrus.GetLevel()
	logrus.SetLevel(logrus.DebugLevel) // 让 2xx 的 Debug 日志也能被断言到
	defer logrus.SetLevel(prevLevel)

	r := gin.New()
	r.Use(RequestLogger(), Recovery())
	r.Handle(method, path, handler)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w, hook.AllEntries()
}

func lastEntry(entries []*logrus.Entry) *logrus.Entry {
	if len(entries) == 0 {
		return nil
	}
	return entries[len(entries)-1]
}

func TestRequestLoggerLogsErrorsWithBody(t *testing.T) {
	cases := []struct {
		name      string
		handler   gin.HandlerFunc
		wantCode  int
		wantLevel logrus.Level
		wantResp  string // 期望错误响应体被抄进日志的 resp 字段（子串）
	}{
		{
			name:      "4xx 打 Warn 且带响应体",
			handler:   func(c *gin.Context) { c.JSON(http.StatusBadRequest, gin.H{"error": "需提供 number"}) },
			wantCode:  http.StatusBadRequest,
			wantLevel: logrus.WarnLevel,
			wantResp:  "需提供 number",
		},
		{
			name:      "5xx 打 Error 且带响应体",
			handler:   func(c *gin.Context) { c.JSON(http.StatusInternalServerError, gin.H{"error": "boom"}) },
			wantCode:  http.StatusInternalServerError,
			wantLevel: logrus.ErrorLevel,
			wantResp:  "boom",
		},
		{
			name:      "2xx 仅 Debug 不带响应体",
			handler:   func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) },
			wantCode:  http.StatusOK,
			wantLevel: logrus.DebugLevel,
			wantResp:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, entries := runThroughMiddleware(t, http.MethodGet, "/api/x", tc.handler)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
			e := lastEntry(entries)
			if e == nil {
				t.Fatal("没有任何日志条目")
			}
			if e.Level != tc.wantLevel {
				t.Errorf("level = %v, want %v", e.Level, tc.wantLevel)
			}
			if e.Data["status"] != tc.wantCode {
				t.Errorf("status 字段 = %v, want %d", e.Data["status"], tc.wantCode)
			}
			resp, _ := e.Data["resp"].(string)
			if tc.wantResp == "" {
				if resp != "" {
					t.Errorf("成功响应不应抄 body，却得到 resp=%q", resp)
				}
			} else if !strings.Contains(resp, tc.wantResp) {
				t.Errorf("resp=%q 未包含 %q", resp, tc.wantResp)
			}
		})
	}
}

func TestRecoveryLogsPanicAndReturns500(t *testing.T) {
	w, entries := runThroughMiddleware(t, http.MethodGet, "/api/boom", func(c *gin.Context) {
		panic("kaboom")
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "internal server error") {
		t.Errorf("body = %q, 期望含 internal server error", w.Body.String())
	}
	var panicked, logged500 bool
	for _, e := range entries {
		if e.Level == logrus.ErrorLevel && e.Data["panic"] != nil {
			panicked = true
			if _, ok := e.Data["stack"].(string); !ok {
				t.Error("panic 日志缺少 stack 字段")
			}
		}
		if e.Data["status"] == http.StatusInternalServerError {
			logged500 = true // 外层 RequestLogger 记录到了最终 500
		}
	}
	if !panicked {
		t.Error("Recovery 未把 panic 落 Error 日志")
	}
	if !logged500 {
		t.Error("RequestLogger 未记录到 panic 恢复后的 500")
	}
}

// 健康检查被高频探针轮询，必须跳过日志。
func TestRequestLoggerSkipsHealth(t *testing.T) {
	_, entries := runThroughMiddleware(t, http.MethodGet, "/api/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	for _, e := range entries {
		if e.Data["path"] == "/api/health" {
			t.Errorf("/api/health 不应产生请求日志，却得到: %v", e.Data)
		}
	}
}
