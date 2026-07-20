package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"hermes-mock/internal/config"
	"hermes-mock/internal/entity"
	"hermes-mock/internal/httpmock"
)

func TestInvokeHTTPMockAndRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := httpmock.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.Upsert(httpmock.Endpoint{
		Name: "pre-dial", Enabled: true,
		Config: httpmock.EndpointConfig{
			AllowedMethods: []string{"POST"}, OverridePolicy: httpmock.OverrideCaseOnly,
			DefaultResponse: httpmock.ResponseSpec{Status: 200, Body: "true"},
			Cases:           map[string]httpmock.ResponseSpec{"deny": {Status: 200, Body: "false"}},
			Rules: []httpmock.Rule{{
				Name: "deny-number", Priority: 10, Case: "deny",
				Conditions: []httpmock.Condition{{Source: "jsonBody", Field: "number", Operator: "EQ", Value: "8613800000001"}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Cfg: &config.Config{}, HTTPMock: store}
	r := gin.New()
	r.Any("/mock/:token", d.invokeHTTPMock)

	req := httptest.NewRequest(http.MethodPost, "/mock/"+endpoint.Token, strings.NewReader(`{"number":"8613800000001"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "secret")
	req.URL.RawQuery = "api_key=secret"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != "false" {
		t.Fatalf("response=%d %q", w.Code, w.Body.String())
	}
	records, err := store.ListRequests(entity.HTTPMockRequestFilter{EndpointID: endpoint.ID})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if records[0].MatchedRule != "deny-number" || records[0].SelectionMode != httpmock.SelectionRuleFixed || !strings.Contains(records[0].HeadersJSON, "[REDACTED]") || !strings.Contains(records[0].QueryJSON, "[REDACTED]") {
		t.Fatalf("记录错误: %+v", records[0])
	}
}

func TestInvokeHTTPMockTimeoutStopsWhenClientCancels(t *testing.T) {
	store, _ := httpmock.New(nil)
	endpoint, err := store.Upsert(httpmock.Endpoint{
		Name: "timeout", Enabled: true,
		Config: httpmock.EndpointConfig{DefaultResponse: httpmock.ResponseSpec{
			Action: httpmock.ActionTimeout, Status: 200, TimeoutMs: 5000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Cfg: &config.Config{}, HTTPMock: store}
	r := gin.New()
	r.Any("/mock/:token", d.invokeHTTPMock)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/mock/"+endpoint.Token, nil).WithContext(ctx)
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	started := time.Now()
	r.ServeHTTP(httptest.NewRecorder(), req)
	if time.Since(started) > time.Second {
		t.Fatal("客户端取消后 handler 未及时释放")
	}
	records, _ := store.ListRequests(entity.HTTPMockRequestFilter{EndpointID: endpoint.ID})
	if len(records) != 1 || !records[0].ClientCanceled {
		t.Fatalf("应记录 clientCanceled: %+v", records)
	}
}

func TestInvokeHTTPMockExplicitCase(t *testing.T) {
	store, _ := httpmock.New(nil)
	endpoint, err := store.Upsert(httpmock.Endpoint{
		Name: "case", Enabled: true,
		Config: httpmock.EndpointConfig{
			OverridePolicy:  httpmock.OverrideCaseOnly,
			DefaultResponse: httpmock.ResponseSpec{Status: 200, Body: "true"},
			Cases:           map[string]httpmock.ResponseSpec{"error": {Status: 503, Body: "busy"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Cfg: &config.Config{}, HTTPMock: store}
	r := gin.New()
	r.Any("/mock/:token", d.invokeHTTPMock)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mock/"+endpoint.Token+"?__mock_case=error", nil))
	if w.Code != 503 || w.Body.String() != "busy" {
		t.Fatalf("response=%d %q", w.Code, w.Body.String())
	}
}

func TestResolveConfirmURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name           string
		raw            string
		publicBaseURL  string
		forwardedProto string
		forwardedHost  string
		want           string
	}{
		{
			name: "absolute stays unchanged", raw: "http://other.example/mock/token?__mock_case=deny",
			want: "http://other.example/mock/token?__mock_case=deny",
		},
		{
			name: "relative uses forwarded current domain", raw: "/mock/token?__mock_case=deny",
			forwardedProto: "https", forwardedHost: "mock.example.com",
			want: "https://mock.example.com/mock/token?__mock_case=deny",
		},
		{
			name: "bare relative path gets leading slash", raw: "mock/token",
			want: "http://internal.example:18080/mock/token",
		},
		{
			name: "configured public base wins", raw: "/mock/token",
			publicBaseURL: "http://hermes-mock.internal:18080", forwardedProto: "https", forwardedHost: "public.example.com",
			want: "http://hermes-mock.internal:18080/mock/token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "http://internal.example:18080/api/tests/callbot", nil)
			if tt.forwardedProto != "" {
				c.Request.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			}
			if tt.forwardedHost != "" {
				c.Request.Header.Set("X-Forwarded-Host", tt.forwardedHost)
			}
			d := &Deps{Cfg: &config.Config{HTTPMockPublicBaseURL: tt.publicBaseURL}}
			got, err := d.resolveConfirmURL(c, tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("resolveConfirmURL=%q want %q", got, tt.want)
			}
		})
	}
}
