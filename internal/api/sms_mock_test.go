package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"hermes-mock/internal/config"
	"hermes-mock/internal/entity"
	"hermes-mock/internal/model"
	"hermes-mock/internal/smsmock"
)

func TestInvokeSMSMockCMContractAndAutomaticDLR(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reference := "abcdef01234567890123456789abcdef"
	dlrBodies := make(chan string, 1)
	callbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		dlrBodies <- string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackServer.Close()
	repo, err := model.InitRepository(&config.Config{
		DBType: model.DBTypeSQLite, DBPath: filepath.Join(t.TempDir(), "api-smsmock.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := smsmock.New(repo, smsmock.DefaultRegistry(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info := store.ProviderInfos()[0]
	cfg := info.DefaultConfig
	cfg.CallbackURL = callbackServer.URL
	failed := cfg.Cases["accepted-failed"]
	failed.Receipt.DelayMs = 0
	cfg.Cases["accepted-failed"] = failed
	endpoint, err := store.Upsert(smsmock.Endpoint{
		Name: "arke-cm", Enabled: true, Provider: info.Provider, ProtocolVersion: info.ProtocolVersion, Config: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Cfg: &config.Config{}, SMSMock: store}
	router := gin.New()
	router.Any("/sms-mock/:provider/:token", d.invokeSMSMock)
	body := fmt.Sprintf(`{"messages":{"authentication":{"producttoken":"secret"},"msg":[{"from":"CashNow","to":[{"number":"005215512345678"}],"minimumNumberOfMessageParts":1,"maximumNumberOfMessageParts":8,"body":{"type":"AUTO","content":"hello"},"reference":%q}]}}`, reference)
	req := httptest.NewRequest(http.MethodPost, "/sms-mock/cm/"+endpoint.Token+"?__mock_case=accepted-failed", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), reference) {
		t.Fatalf("CM submit response=%d %s", response.Code, response.Body.String())
	}
	select {
	case dlr := <-dlrBodies:
		var parsed struct {
			Messages struct {
				Msg struct {
					Reference string `json:"reference"`
					Status    struct {
						Code      string `json:"code"`
						ErrorCode string `json:"errorCode"`
					} `json:"status"`
				} `json:"msg"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(dlr), &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Messages.Msg.Reference != reference || parsed.Messages.Msg.Status.Code != "3" || parsed.Messages.Msg.Status.ErrorCode != "206" {
			t.Fatalf("DLR 契约错误: %s", dlr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到自动 CM DLR")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := store.ListMessages(entity.SMSMockMessageFilter{EndpointID: endpoint.ID})
		if err == nil && len(rows) == 1 && rows[0].ReceiptStatus == entity.SMSMockReceiptSucceeded {
			if strings.Contains(rows[0].RequestBody, "secret") {
				t.Fatalf("调用记录泄漏 producttoken: %s", rows[0].RequestBody)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("DLR 成功状态未落库")
}

func TestInvokeSMSMockInvalidCMBodyReturnsCMErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo, err := model.InitRepository(&config.Config{DBType: model.DBTypeSQLite, DBPath: filepath.Join(t.TempDir(), "invalid.db")})
	if err != nil {
		t.Fatal(err)
	}
	store, err := smsmock.New(repo, smsmock.DefaultRegistry(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info := store.ProviderInfos()[0]
	cfg := info.DefaultConfig
	// 所有自动 DLR Case 改为关闭，使本契约测试不依赖 callbackUrl。
	for name, spec := range cfg.Cases {
		spec.Receipt.Enabled = false
		cfg.Cases[name] = spec
	}
	endpoint, err := store.Upsert(smsmock.Endpoint{Name: "cm", Enabled: true, Provider: info.Provider, ProtocolVersion: info.ProtocolVersion, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Cfg: &config.Config{}, SMSMock: store}
	router := gin.New()
	router.Any("/sms-mock/:provider/:token", d.invokeSMSMock)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/sms-mock/cm/"+endpoint.Token, strings.NewReader(`{"bad":true}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		ErrorCode int   `json:"errorCode"`
		Messages  []any `json:"messages"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.ErrorCode == 0 || envelope.Messages == nil {
		t.Fatalf("不是 CM 错误包络: %s err=%v", recorder.Body.String(), err)
	}
	methodRecorder := httptest.NewRecorder()
	router.ServeHTTP(methodRecorder, httptest.NewRequest(http.MethodGet, "/sms-mock/cm/"+endpoint.Token, nil))
	if methodRecorder.Code != http.StatusMethodNotAllowed || !strings.Contains(methodRecorder.Body.String(), `"errorCode":405`) {
		t.Fatalf("错误 method 应返回 CM 405 包络: status=%d body=%s", methodRecorder.Code, methodRecorder.Body.String())
	}
}

func TestEnrichSMSMockURLPrefersDedicatedPublicBase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name     string
		cfg      config.Config
		expected string
	}{
		{name: "sms-base", cfg: config.Config{SMSMockPublicBaseURL: "https://sms.internal/", HTTPMockPublicBaseURL: "https://http.example"}, expected: "https://sms.internal/sms-mock/cm/token"},
		{name: "http-fallback", cfg: config.Config{HTTPMockPublicBaseURL: "https://http.example/"}, expected: "https://http.example/sms-mock/cm/token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "http://browser.example/api/sms-mocks", nil)
			d := &Deps{Cfg: &tc.cfg}
			endpoint := &smsmock.Endpoint{Provider: "CM", Token: "token"}
			d.enrichSMSMockURL(c, endpoint)
			if endpoint.InvokeURL != tc.expected || endpoint.InvokePath != "/sms-mock/cm/token" {
				t.Fatalf("url=%q path=%q", endpoint.InvokeURL, endpoint.InvokePath)
			}
		})
	}
}

func TestWriteSMSMockResponsePreservesAdapterHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	d := &Deps{}
	d.writeSMSMockResponse(c, smsmock.WireResponse{
		HTTPStatus: http.StatusAccepted,
		Headers: http.Header{
			"Content-Type": []string{"text/plain; charset=utf-8"},
			"X-Vendor-Ack": []string{"ack-1"},
		},
		Body: "queued",
	})
	if recorder.Code != http.StatusAccepted || recorder.Header().Get("X-Vendor-Ack") != "ack-1" || recorder.Body.String() != "queued" {
		t.Fatalf("response=%d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}
