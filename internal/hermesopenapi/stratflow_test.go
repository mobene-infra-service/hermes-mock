package hermesopenapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyGateOmitsEmptyTriStateFields(t *testing.T) {
	raw, err := json.Marshal(SfMockGateView{Master: true, Global: false, Schemes: map[string]bool{"DEF": true}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"mode"`)) || bytes.Contains(raw, []byte(`"schemeModes"`)) {
		t.Fatalf("旧 gate 不应被代理成空字符串三态字段: %s", raw)
	}
}

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

// config 视图解析：完整 Case、Schema 与逐 Case 预览须原样透传。
func TestSfMockNodeViewParse(t *testing.T) {
	raw := `[{"nodeId":"voicebot_1","type":"VoicebotCall","channel":"CALL","configured":true,
	  "resultSchema":{"type":"CALL","statuses":["CONNECTED","NO_RECEIPT"],"ringStatuses":["answered"],"intentions":["A","Z"],"maxAttemptNo":3,"retryStepGapMs":800},
	  "matchSchema":{"fields":[{"key":"segment","type":"string"}],"operators":{"string":["eq"]}},
	  "config":{"forcedCaseKey":null,"cases":[{"key":"connected_z","name":"接通-Z","delayMs":1000,"result":{"type":"CALL","status":"CONNECTED","intention":"Z"}}],"defaultSelection":{"mode":"FIXED","caseKey":"connected_z"},"rules":[]},
	  "previews":{"connected_z":{"steps":[{"delayMs":1000,"status":"CONNECTED","data":{"intention":"Z"}}],"actionFinal":"SUCCESS","nodePort":"out","expectedVars":{"intention":"Z"},"dynamicVars":[]}}}]`
	var out []SfMockNodeView
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].NodeID != "voicebot_1" || out[0].Channel == nil || *out[0].Channel != "CALL" {
		t.Fatalf("node 解析错: %+v", out)
	}
	if !out[0].Configured || len(out[0].Config.Cases) != 1 || out[0].Config.Cases[0].Result.Intention == nil || *out[0].Config.Cases[0].Result.Intention != "Z" {
		t.Fatalf("Case 解析错: %+v", out[0].Config)
	}
	if out[0].Previews["connected_z"].NodePort != "out" || out[0].ResultSchema.MaxAttemptNo == nil || *out[0].ResultSchema.MaxAttemptNo != 3 {
		t.Fatalf("Schema/preview 解析错: %+v", out[0])
	}
	if err := validateTypedMockNode(out[0]); err != nil {
		t.Fatalf("完整类型化响应不应被判为旧协议: %v", err)
	}
}

func TestStratflowListConfigRejectsLegacyContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":[{"nodeId":"sms_1","type":"SmsSend","channel":"SMS","forcedOutcome":null,"baseDelayMs":0,"outcomes":[]}]}`))
	}))
	defer srv.Close()

	_, err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowListConfig(t.Context(), "V1")
	if err == nil || !strings.Contains(err.Error(), "配置协议不兼容") || !strings.Contains(err.Error(), "cases/defaultSelection") {
		t.Fatalf("旧协议应返回明确兼容错误，got %v", err)
	}
}

func TestSfMockDeadPlanParse(t *testing.T) {
	raw := `[{"actionCode":"A1","runCode":"R1","nodeId":"sms","entryCode":"E1","channel":"SMS","outcomeKey":"SMS_DELIVERED","baseMs":null,"idx":null,"steps":null,"status":"DEAD","nextDueAt":"2026-07-11T05:00:00","retryCount":120,"lastError":"broker down"}]`
	var out []SfMockActionPlan
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Status != "DEAD" || out[0].RetryCount != 120 || out[0].LastError == nil || *out[0].LastError != "broker down" {
		t.Fatalf("DEAD plan 解析错: %+v", out)
	}
}

// —— 以下用 httptest 断请求形状（本机沙箱禁监听端口时跳过，CI 正常跑）——

// gate 三态走 PUT + mode query。
func TestStratflowSetSchemeModeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/openapi/mock/gate/scheme/DEF_x" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("mode") != "PAUSED" {
			t.Fatalf("mode query 缺失: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"master":true,"mode":"MOCK","global":true,"schemeModes":{"DEF_x":"PAUSED"},"schemes":{"DEF_x":false},"deliveryPaused":false}}`))
	}))
	defer srv.Close()
	v, err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowSetSchemeMode(t.Context(), "DEF_x", "PAUSED")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Master || v.Mode != "MOCK" || v.SchemeModes["DEF_x"] != "PAUSED" {
		t.Fatalf("gate view 解析错: %+v", v)
	}
}

func TestStratflowSetRealCarriesLegacyEnabledFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "REAL" || r.URL.Query().Get("enabled") != "false" {
			t.Fatalf("REAL 应同时携带新旧参数: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"master":true,"global":false}}`))
	}))
	defer srv.Close()
	if _, err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowSetGlobalMode(t.Context(), "REAL"); err != nil {
		t.Fatal(err)
	}
}

func TestStratflowListDeadPlansCarriesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("runCode") != "RUN" || r.URL.Query().Get("status") != "DEAD" {
			t.Fatalf("plans status 未透传: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":[]}`))
	}))
	defer srv.Close()
	if _, err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowListPlansByStatus(t.Context(), "RUN", "DEAD"); err != nil {
		t.Fatal(err)
	}
}

func TestStratflowListDecisionsCarriesPagingAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/openapi/mock/decisions" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("runCode") != "RUN/1" || q.Get("status") != "DONE" || q.Get("pageNo") != "2" || q.Get("pageSize") != "50" {
			t.Fatalf("decisions query 未完整透传: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"records":[{"actionCode":"A1","runCode":"RUN/1","nodeId":"sms_1","entryCode":"E1","channel":"SMS","caseKey":"delivered","status":"DONE","noReceipt":false,"expectedVars":{},"actualVars":{},"routed":true}],"total":51,"pageNo":2,"pageSize":50}}`))
	}))
	defer srv.Close()
	page, err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowListDecisions(t.Context(), "RUN/1", "DONE", 2, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 51 || page.PageNo != 2 || len(page.Records) != 1 || page.Records[0].CaseKey != "delivered" {
		t.Fatalf("decisions page 解析错: %+v", page)
	}
}

func TestStratflowPutTypedConfigRoundTrip(t *testing.T) {
	caseKey := "connected_z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/openapi/mock/config/V1/call_1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var got SfMockNodeConfig
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got.Cases) != 1 || got.Cases[0].Result.Intention == nil || *got.Cases[0].Result.Intention != "Z" {
			t.Fatalf("Case JSON 丢字段: %+v", got)
		}
		if len(got.Rules) != 1 || got.Rules[0].Conditions[0].Value != "VIP" || got.DefaultSelection == nil || got.DefaultSelection.CaseKey == nil {
			t.Fatalf("规则/默认选择 JSON 丢字段: %+v", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
	}))
	defer srv.Close()
	intention := "Z"
	cfg := SfMockNodeConfig{
		Cases: []SfMockCase{{
			Key: caseKey, Name: "接通-Z", DelayMs: 1000,
			Result: SfMockCaseResult{Type: "CALL", Status: "CONNECTED", Intention: &intention},
		}},
		DefaultSelection: &SfMockSelection{Mode: "FIXED", CaseKey: &caseKey},
		Rules: []SfMockSelectionRule{{
			Name: "VIP", Priority: 100,
			Conditions: []SfMockMatchCondition{{Key: "customer_level", Type: "string", Op: "eq", Value: "VIP"}},
			Selection:  SfMockSelection{Mode: "FIXED", CaseKey: &caseKey},
		}},
	}
	if err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowPutConfig(t.Context(), "V1", "call_1", cfg); err != nil {
		t.Fatal(err)
	}
}

func TestStratflowRequeuePlanRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/openapi/mock/plans/A%2F1/requeue" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
	}))
	defer srv.Close()
	if err := New(Cred{Mode: "direct", OrgCode: "o1", StratflowURL: srv.URL}).StratflowRequeuePlan(t.Context(), "A/1"); err != nil {
		t.Fatal(err)
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
