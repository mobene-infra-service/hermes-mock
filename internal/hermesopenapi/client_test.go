package hermesopenapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// direct 模式：URL=服务地址+path，注入 ORG_CODE_KEY/ORG_NAME_KEY 头（网关本会注入的）。
func TestEndpointDirect(t *testing.T) {
	c := New(Cred{
		Mode: "direct", OrgCode: "org001", OrgName: "Test Org", UserCode: "mock",
		BasicURL: "http://basic:8080", CallCenterURL: "http://cc:8080", CallBotURL: "http://cb:8080",
	})
	url, h, err := c.endpoint("basic", "/openapi/agent/page")
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://basic:8080/openapi/agent/page" {
		t.Errorf("direct basic url 错: %s", url)
	}
	if h[hdrOrgCode] != "org001" {
		t.Errorf("应注入 ORG_CODE_KEY=org001, got %q", h[hdrOrgCode])
	}
	if h[hdrOrgName] == "" {
		t.Error("应注入 ORG_NAME_KEY")
	}
	if _, ok := h[hdrOpenAPIKey]; ok {
		t.Error("direct 模式不应带 X-OpenApi-Key")
	}
	// call-center / call-bot 走各自地址
	if u, _, _ := c.endpoint("call-center", "/openapi/task/createAndImport"); u != "http://cc:8080/openapi/task/createAndImport" {
		t.Errorf("cc url 错: %s", u)
	}
}

// gateway 模式：URL=网关+/{product}+path，带 X-OpenApi-Key，不注入 ORG 头。
func TestEndpointGateway(t *testing.T) {
	c := New(Cred{Mode: "gateway", GatewayURL: "https://gw.example.com", APIKey: "KEY123"})
	url, h, err := c.endpoint("call-center", "/openapi/task/createAndImport")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://gw.example.com/call-center/openapi/task/createAndImport" {
		t.Errorf("gateway url 错: %s", url)
	}
	if h[hdrOpenAPIKey] != "KEY123" {
		t.Errorf("应带 X-OpenApi-Key, got %q", h[hdrOpenAPIKey])
	}
	if _, ok := h[hdrOrgCode]; ok {
		t.Error("gateway 模式不应自注入 ORG_CODE_KEY（由网关注入）")
	}
}

// 配置缺失应明确报错（不静默）。
func TestEndpointErrors(t *testing.T) {
	if _, _, err := New(Cred{Mode: "gateway"}).endpoint("basic", "/x"); err == nil {
		t.Error("网关模式缺 url/key 应报错")
	}
	if _, _, err := New(Cred{Mode: "direct"}).endpoint("basic", "/x"); err == nil {
		t.Error("直连模式缺 orgCode 应报错")
	}
	if _, _, err := New(Cred{Mode: "direct", OrgCode: "o"}).endpoint("basic", "/x"); err == nil || !strings.Contains(err.Error(), "basic") {
		t.Errorf("直连缺 basic 地址应报错, got %v", err)
	}
}

func TestListAgentsWithFilterRequest(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/openapi/agent/page" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get(hdrOrgCode) != "org001" {
			t.Fatalf("missing org header: %q", r.Header.Get(hdrOrgCode))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"records":[],"total":0}}`))
	}))
	defer srv.Close()

	_, _, err := New(Cred{Mode: "direct", OrgCode: "org001", BasicURL: srv.URL}).ListAgentsWithFilter(
		t.Context(), 2, 25, AgentFilter{Number: "6199", AgentGroupCode: "mock_skill", DepCode: "mock", Status: "ENABLED"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"number": "6199", "agentGroupCode": "mock_skill", "depCode": "mock", "status": "ENABLED"} {
		if got[k] != want {
			t.Fatalf("body[%s]=%v, want %v; body=%v", k, got[k], want, got)
		}
	}
	if got["pageNum"].(float64) != 2 || got["pageSize"].(float64) != 25 {
		t.Fatalf("pagination not sent: %v", got)
	}
}

func TestUpdateAgentUsesPut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/openapi/agent/update" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got["agentNumber"] != "6199" || got["status"] != "DISABLED" {
			t.Fatalf("unexpected body: %v", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
	}))
	defer srv.Close()

	err := New(Cred{Mode: "direct", OrgCode: "org001", BasicURL: srv.URL}).UpdateAgent(
		t.Context(), UpdateAgentReq{AgentNumber: "6199", Status: "DISABLED", DepCode: "mock", AgentGroupCode: "mock_skill"},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetAgentEnabledUsesBasicStatusEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/agent/batchUpdateAgentStatus" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got["status"].(float64) != 0 {
			t.Fatalf("status=%v, want 0; body=%v", got["status"], got)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
	}))
	defer srv.Close()

	err := New(Cred{Mode: "direct", OrgCode: "org001", BasicURL: srv.URL}).SetAgentEnabled(t.Context(), []string{"ag-1"}, false)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSwitchAgentStatusUsesNumericAction(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agent-workbench/sdk/agent/status/switch" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get(hdrAgentNumber) != "6199" {
			t.Fatalf("missing agent header: %q", r.Header.Get(hdrAgentNumber))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
	}))
	defer srv.Close()

	_, err := New(Cred{Mode: "direct", OrgCode: "org001", CallCenterURL: srv.URL}).SwitchAgentStatus(t.Context(), "6199", "ONLINE")
	if err != nil {
		t.Fatal(err)
	}
	if got["action"].(float64) != 2 {
		t.Fatalf("action=%v, want 2; body=%v", got["action"], got)
	}
}

func TestPrepareAgentPhoneReadyCallsSipAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/public/auth/sip" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var got map[string]string
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got["username"] != "6199" || got["algorithm"] != "MD5" || got["response"] == "" {
			t.Fatalf("unexpected body: %v", got)
		}
		_, _ = w.Write([]byte(`1`))
	}))
	defer srv.Close()

	err := New(Cred{Mode: "direct", OrgCode: "org001", CallCenterURL: srv.URL}).PrepareAgentPhoneReady(t.Context(), "6199", "1234.")
	if err != nil {
		t.Fatal(err)
	}
}
