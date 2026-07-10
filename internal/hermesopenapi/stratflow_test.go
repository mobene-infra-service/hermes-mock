package hermesopenapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// prodStratflow：gateway 模式 URL = 网关 + /stratflow + path；direct 模式 = StratflowURL + path。
func TestEndpointStratflow(t *testing.T) {
	// gateway
	gw := New(Cred{Mode: "gateway", GatewayURL: "https://gw.example.com", APIKey: "K"})
	if u, h, err := gw.endpoint(prodStratflow, "/openapi/mock/gate"); err != nil {
		t.Fatal(err)
	} else if u != "https://gw.example.com/stratflow/openapi/mock/gate" {
		t.Errorf("gateway stratflow url 错: %s", u)
	} else if h[hdrOpenAPIKey] != "K" {
		t.Errorf("应带 X-OpenApi-Key")
	}
	// direct
	dir := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: "http://sf:8090"})
	if u, _, err := dir.endpoint(prodStratflow, "/openapi/mock/config/VER"); err != nil {
		t.Fatal(err)
	} else if u != "http://sf:8090/openapi/mock/config/VER" {
		t.Errorf("direct stratflow url 错: %s", u)
	}
	// direct 缺地址应报错、且错误信息带产品名
	if _, _, err := New(Cred{Mode: "direct", OrgCode: "o1"}).endpoint(prodStratflow, "/x"); err == nil || !strings.Contains(err.Error(), "stratflow") {
		t.Errorf("direct 缺 stratflow 地址应报错, got %v", err)
	}
}

// import 响应解析：取 result==1 的 plans[].runCode。
func TestSfImportResultParse(t *testing.T) {
	raw := `{
	  "code":"BATCH","batchCode":"BATCH","collectionCode":"COL","idempotencyKey":"k",
	  "status":3,"total":1,"success":1,"fail":0,
	  "plans":[{"defCode":"DEF","versionCode":"VER","result":1,"runCode":"RUN_1","failFields":null}]
	}`
	var r SfImportResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Result != SfImportResultRunCreated || r.Plans[0].RunCode != "RUN_1" {
		t.Fatalf("import 解析错: %+v", r.Plans)
	}
	if r.Success != 1 || r.CollectionCode != "COL" {
		t.Fatalf("批次字段解析错: %+v", r)
	}
}

// run 进度解析：断言看 nodes[].edgeFlow。
func TestSfRunProgressParse(t *testing.T) {
	raw := `{
	  "run":{"code":"RUN_1","versionCode":"VER","status":1,"numberCount":10,"terminalCount":3},
	  "nodes":[{"nodeId":"voicebot_1","inflow":10,"processed":10,"processing":0,"edgeFlow":{"success":7,"failed":3}}]
	}`
	var p SfRunProgress
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.Run.Code != "RUN_1" || p.Run.NumberCount != 10 {
		t.Fatalf("run 汇总解析错: %+v", p.Run)
	}
	if len(p.Nodes) != 1 || p.Nodes[0].EdgeFlow["success"] != 7 || p.Nodes[0].EdgeFlow["failed"] != 3 {
		t.Fatalf("edgeFlow 解析错: %+v", p.Nodes)
	}
}

// config 视图解析：steps=0 表示超时不回执；channel/forcedOutcome 可空。
func TestSfMockNodeViewParse(t *testing.T) {
	raw := `[{"nodeId":"voicebot_1","type":"VOICEBOT_CALL","channel":"CALL","forcedOutcome":null,"baseDelayMs":0,
	  "outcomes":[
	    {"key":"CALL_ANSWERED_A","label":"接通·高意向","weight":70,"defaultWeight":50,"steps":1},
	    {"key":"CALL_TIMEOUT_NO_RECEIPT","label":"超时无回执","weight":0,"defaultWeight":0,"steps":0}]}]`
	var out []SfMockNodeView
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].NodeID != "voicebot_1" || out[0].Channel == nil || *out[0].Channel != "CALL" {
		t.Fatalf("node 解析错: %+v", out)
	}
	if out[0].Outcomes[0].Weight != 70 || out[0].Outcomes[1].Steps != 0 {
		t.Fatalf("outcome 解析错: %+v", out[0].Outcomes)
	}
}

// —— 以下用 httptest 断请求形状（本机沙箱禁监听端口时跳过，CI 正常跑）——

// gate 开关走 PUT + enabled query。
func TestStratflowSetSchemeGateRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/openapi/mock/gate/scheme/DEF_x" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("enabled") != "true" {
			t.Fatalf("enabled query 缺失: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"master":true,"global":false,"schemes":{"DEF_x":true},"deliveryPaused":false}}`))
	}))
	defer srv.Close()
	v, err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowSetSchemeGate(t.Context(), "DEF_x", true)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Master || !v.Schemes["DEF_x"] {
		t.Fatalf("gate view 解析错: %+v", v)
	}
}

// 进度必须带 uploadStartTime/uploadEndTime。
func TestStratflowRunProgressCarriesTimeWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi/mock/collections/COL/runs/RUN_1/progress" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("uploadStartTime") != "2026-07-08 00:00:00" || r.URL.Query().Get("uploadEndTime") != "2026-07-09 00:00:00" {
			t.Fatalf("时间窗未透传: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"run":{"code":"RUN_1"},"nodes":[]}}`))
	}))
	defer srv.Close()
	_, err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowRunProgress(
		t.Context(), "COL", "RUN_1", "2026-07-08 00:00:00", "2026-07-09 00:00:00")
	if err != nil {
		t.Fatal(err)
	}
}

// import 走 POST，透传 rows/idempotencyKey。
func TestStratflowImportRequest(t *testing.T) {
	var got SfImportReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/openapi/collections/COL/import" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"plans":[{"result":1,"runCode":"RUN_1"}]}}`))
	}))
	defer srv.Close()
	res, err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowImport(
		t.Context(), "COL", SfImportReq{IdempotencyKey: "k1", Rows: []SfImportRow{{Phone: "138", BizFields: map[string]any{"n": "张三"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.IdempotencyKey != "k1" || len(got.Rows) != 1 || got.Rows[0].Phone != "138" {
		t.Fatalf("import 请求体错: %+v", got)
	}
	if len(res.Plans) != 1 || res.Plans[0].RunCode != "RUN_1" {
		t.Fatalf("import 响应错: %+v", res)
	}
}
