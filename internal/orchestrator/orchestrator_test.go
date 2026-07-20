package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermes-mock/internal/orgcfg"
)

// 无机构配置时：调用应明确报「未配置 OpenAPI 凭据」（不静默、不直连）。
func newEmpty() *Orchestrator {
	s := orgcfg.NewMemory() // 单测内存座、无机构
	return New(s)
}

func TestConfirmURLBeforeDialForwardedToBothTaskAPIs(t *testing.T) {
	bodies := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode %s: %v", r.URL.Path, err)
		}
		bodies[r.URL.Path] = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"code":"TASK1"}}`))
	}))
	defer server.Close()

	orgs := orgcfg.NewMemory()
	if _, err := orgs.Upsert(orgcfg.OrgConfig{
		OrgCode: "ORG1", Mode: "direct", CallCenterURL: server.URL, CallBotURL: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	o := New(orgs)
	confirmURL := "http://mock:18080/mock/token?__mock_case=deny"
	if _, err := o.RunCallBot(CallBotScenario{Name: "bot", TaskType: 2, Numbers: []string{"861"}, ConfirmURLBeforeDial: confirmURL}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunCallCenterTask(CallCenterTaskScenario{Name: "cc", Numbers: []string{"861"}, ConfirmURLBeforeDial: confirmURL}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/openapi/task/create-and-import", "/openapi/task/createAndImport"} {
		if got := bodies[path]["confirmUrlBeforeDial"]; got != confirmURL {
			t.Errorf("%s confirmUrlBeforeDial=%v want %s", path, got, confirmURL)
		}
	}
}

// 未配置机构时，各任务方法都应报错（证明走 OpenAPI 凭据而非裸 URL）。
func TestTasksRequireOrgCred(t *testing.T) {
	o := newEmpty()
	if _, err := o.RunOTP(OTPScenario{To: "8610000", TemplateCode: "T1"}); err == nil {
		t.Error("未配机构 OTP 应报错")
	}
	if _, err := o.RunCallBot(CallBotScenario{Name: "t", Numbers: []string{"8610000"}}); err == nil {
		t.Error("未配机构 call-bot 应报错")
	}
	if _, err := o.RunCallCenterTask(CallCenterTaskScenario{Name: "t", Numbers: []string{"8610000"}}); err == nil {
		t.Error("未配机构 群呼 应报错")
	}
}

// extractTaskCode 兼容 data 为字符串 / 对象.code / 对象.taskCode（hermesopenapi 返回 data 原文）。
func TestExtractTaskCode(t *testing.T) {
	cases := map[string]string{
		`"TASK123"`:          "TASK123", // data 原文是字符串
		`{"code":"C9"}`:      "C9",      // data 原文是对象
		`{"taskCode":"TC7"}`: "TC7",
		`{"data":"TASK123"}`: "TASK123", // 兼容整包
		`{"other":"x"}`:      "",
		`not json`:           "",
	}
	for raw, want := range cases {
		if got := extractTaskCode([]byte(raw)); got != want {
			t.Errorf("extractTaskCode(%s)=%q want %q", raw, got, want)
		}
	}
}
