package smsmock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hermes-mock/internal/config"
	"hermes-mock/internal/entity"
	"hermes-mock/internal/httpmock"
	"hermes-mock/internal/model"
)

const testReference = "abcdef01234567890123456789abcdef"

func cmRequestBody(reference, content string) string {
	return fmt.Sprintf(`{"messages":{"authentication":{"producttoken":"real-secret-token"},"msg":[{"from":"CashNow","to":[{"number":"005215512345678"}],"minimumNumberOfMessageParts":1,"maximumNumberOfMessageParts":8,"body":{"type":"AUTO","content":%q},"reference":%q}]}}`, content, reference)
}

func TestCMV1AdapterMatchesArkeWireContract(t *testing.T) {
	adapter := NewCMV1Adapter()
	info := adapter.Info()
	if info.HermesVendorName != "CM" || info.HermesConfig["key"] != "mock-product-token" || info.HermesConfig["defaultSender"] != "SMS" {
		t.Fatalf("Hermes CmConfig 接入提示错误: %+v", info.HermesConfig)
	}
	if _, legacySnakeCase := info.HermesConfig["default_sender"]; legacySnakeCase {
		t.Fatalf("Hermes CmConfig 使用 camelCase defaultSender，不应输出 default_sender: %+v", info.HermesConfig)
	}
	body := cmRequestBody(testReference, "hello")
	submission, err := adapter.ParseSubmission(InvokeRequest{Method: http.MethodPost, Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	if submission.ProductToken != "real-secret-token" || len(submission.Messages) != 1 {
		t.Fatalf("解析结果错误: %+v", submission)
	}
	message := submission.Messages[0]
	if message.Reference != testReference || message.Recipient != "005215512345678" || message.Sender != "CashNow" || message.Content != "hello" {
		t.Fatalf("规范化消息错误: %+v", message)
	}
	sanitized := adapter.SanitizeSubmission(InvokeRequest{Method: http.MethodPost, Body: []byte(body)})
	if strings.Contains(sanitized, "real-secret-token") || !strings.Contains(sanitized, "[REDACTED]") {
		t.Fatalf("producttoken 未脱敏: %s", sanitized)
	}
	spec := cmV1DefaultConfig().Cases["accepted-delivered"]
	response, err := adapter.BuildSubmissionResponse([]MessageOutcome{{Message: message, Spec: spec}})
	if err != nil {
		t.Fatal(err)
	}
	var submit struct {
		ErrorCode int `json:"errorCode"`
		Messages  []struct {
			Reference string `json:"reference"`
			To        string `json:"to"`
			Parts     int    `json:"parts"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(response.Body), &submit); err != nil {
		t.Fatal(err)
	}
	if submit.ErrorCode != 0 || len(submit.Messages) != 1 || submit.Messages[0].Reference != testReference || submit.Messages[0].Parts != 1 {
		t.Fatalf("CM Accepted 响应错误: %s", response.Body)
	}
	wireReceipt, err := adapter.BuildReceipt(message, spec.Receipt, time.Date(2026, 7, 24, 8, 9, 10, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if wireReceipt.Method != http.MethodPost || wireReceipt.Headers.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("DLR transport=%+v", wireReceipt)
	}
	var receiptPayload struct {
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
	if err := json.Unmarshal([]byte(wireReceipt.Body), &receiptPayload); err != nil {
		t.Fatal(err)
	}
	if receiptPayload.Messages.Msg.Reference != testReference || receiptPayload.Messages.Msg.Status.Code != "2" || receiptPayload.Messages.Msg.Status.ErrorCode != "" {
		t.Fatalf("CM DLR 错误: %s", wireReceipt.Body)
	}
}

func TestCMV1SafeTemplateSupportsMinorProtocolChange(t *testing.T) {
	adapter := NewCMV1Adapter()
	spec := CaseSpec{Submit: SubmissionSpec{
		Action: SubmitActionRespond, Result: SubmitResultCustom, HTTPStatus: 202, Parts: 2,
		RawBodyTemplate: `{"details":"Created","errorCode":0,"messages":[{"to":"${recipient}","status":"Accepted","reference":"${reference}","parts":${parts},"messageDetails":"","messageErrorCode":0}],"newField":"v2"}`,
	}}
	if err := normalizeCase(&spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidateCase(spec); err != nil {
		t.Fatal(err)
	}
	response, err := adapter.BuildSubmissionResponse([]MessageOutcome{{Message: CanonicalMessage{Reference: testReference}, Spec: spec}})
	if err != nil {
		t.Fatal(err)
	}
	if response.HTTPStatus != 202 || !strings.Contains(response.Body, testReference) || !strings.Contains(response.Body, `"newField":"v2"`) {
		t.Fatalf("模板响应错误: %+v", response)
	}
	bad := spec
	bad.Submit.RawBodyTemplate = `{"reference":"${unknown}"}`
	if err := adapter.ValidateCase(bad); err == nil {
		t.Fatal("未知模板变量应被拒绝")
	}
	submitUsesReceiptVariable := spec
	submitUsesReceiptVariable.Submit.RawBodyTemplate = `{"reference":"${reference}","receivedAt":"${receivedAt}"}`
	if err := adapter.ValidateCase(submitUsesReceiptVariable); err == nil {
		t.Fatal("同步响应模板不应暴露 DLR 阶段的 receivedAt")
	}
	missingReference := spec
	missingReference.Submit.RawBodyTemplate = `{"accepted":true}`
	if err := adapter.ValidateCase(missingReference); err == nil {
		t.Fatal("同步响应模板丢失 reference 应被拒绝")
	}
	misplacedReference := spec
	misplacedReference.Submit.RawBodyTemplate = `{"reference":"${reference}","messages":[]}`
	if err := adapter.ValidateCase(misplacedReference); err == nil {
		t.Fatal("同步响应模板不能只在无关字段保留 reference")
	}
	receiptUsesSubmitVariable := cmV1DefaultConfig().Cases["accepted-delivered"]
	receiptUsesSubmitVariable.Receipt.RawBodyTemplate = `{"messages":{"msg":{"reference":"${reference}","parts":${parts}}}}`
	if err := adapter.ValidateCase(receiptUsesSubmitVariable); err == nil {
		t.Fatal("DLR 模板不应暴露提交阶段的 parts")
	}
	customReceipt := cmV1DefaultConfig().Cases["accepted-delivered"]
	customReceipt.Receipt.RawBodyTemplate = `{"messages":{"msg":{"received":"${receivedAt}","to":"${recipient}","reference":"${reference}","status":{"code":"${statusCode}","errorCode":"${errorCode}","errorDescription":"${errorDescription}"},"operator":"${operator}","newField":"v2"}}}`
	if err := adapter.ValidateCase(customReceipt); err != nil {
		t.Fatalf("DLR 小字段扩展应可由安全模板完成: %v", err)
	}
	receiptMissingReference := cmV1DefaultConfig().Cases["accepted-delivered"]
	receiptMissingReference.Receipt.RawBodyTemplate = `{"messages":{"msg":{"status":{"code":"2","errorCode":""}}}}`
	if err := adapter.ValidateCase(receiptMissingReference); err == nil {
		t.Fatal("DLR 模板丢失 reference 应被拒绝")
	}
	receiptMisplacedReference := cmV1DefaultConfig().Cases["accepted-delivered"]
	receiptMisplacedReference.Receipt.RawBodyTemplate = `{"reference":"${reference}","messages":{"msg":{"status":{"code":"2","errorCode":""}}}}`
	if err := adapter.ValidateCase(receiptMisplacedReference); err == nil {
		t.Fatal("DLR 模板不能只在无关字段保留 reference")
	}
}

func TestCMV1RejectsAmbiguousInputAndIncompatibleBatchCases(t *testing.T) {
	adapter := NewCMV1Adapter()
	tests := []struct {
		name string
		body string
	}{
		{name: "trailing-json", body: cmRequestBody(testReference, "hello") + `{}`},
		{name: "multiple-recipients", body: strings.Replace(cmRequestBody(testReference, "hello"), `"to":[{"number":"005215512345678"}]`, `"to":[{"number":"1"},{"number":"2"}]`, 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := adapter.ParseSubmission(InvokeRequest{Method: http.MethodPost, Body: []byte(tc.body)}); err == nil {
				t.Fatal("歧义 CM 请求应被拒绝")
			}
		})
	}
	accepted := cmV1DefaultConfig().Cases["accepted-delivered"]
	timeout := cmV1DefaultConfig().Cases["submit-timeout"]
	if _, err := adapter.BuildSubmissionResponse([]MessageOutcome{
		{Message: CanonicalMessage{Reference: testReference}, Spec: accepted},
		{Message: CanonicalMessage{Reference: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, Spec: timeout},
	}); err == nil {
		t.Fatal("同一 HTTP 批次不能静默混用响应与超时 Case")
	}
}

func newSMSMockTestRepo(t *testing.T) model.Repository {
	t.Helper()
	repo, err := model.InitRepository(&config.Config{DBType: model.DBTypeSQLite, DBPath: filepath.Join(t.TempDir(), "smsmock.db")})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func createTestEndpoint(t *testing.T, store *Store, callbackURL string, mutate func(*EndpointConfig)) *Endpoint {
	t.Helper()
	cfg := cmV1DefaultConfig()
	cfg.CallbackURL = callbackURL
	if mutate != nil {
		mutate(&cfg)
	}
	endpoint, err := store.Upsert(Endpoint{
		Name: "arke-cm", Enabled: true, Provider: CMProvider, ProtocolVersion: CMV1, Config: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func prepareAndComplete(t *testing.T, store *Store, endpoint *Endpoint, query map[string][]string, reference string) *Invocation {
	t.Helper()
	invocation, err := store.PrepareInvocation(context.Background(), InvokeRequest{
		Provider: "cm", Token: endpoint.Token, Method: http.MethodPost, Query: query,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(cmRequestBody(reference, "hello")), Remote: "127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSubmission(context.Background(), invocation, entity.SMSMockSubmitResponded, true); err != nil {
		t.Fatal(err)
	}
	return invocation
}

func waitMessage(t *testing.T, store *Store, id int64, timeout time.Duration, predicate func(*entity.SMSMockMessage) bool) *entity.SMSMockMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		row, err := store.GetMessage(id)
		if err == nil && predicate(row) {
			return row
		}
		time.Sleep(20 * time.Millisecond)
	}
	row, _ := store.GetMessage(id)
	t.Fatalf("等待消息状态超时: %+v", row)
	return nil
}

func TestStoreRetryDuplicateManualAndRedaction(t *testing.T) {
	var calls atomic.Int32
	callbackBodies := make(chan string, 8)
	callbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		callbackBodies <- string(body)
		n := calls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("temporary"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackServer.Close()
	store, err := New(newSMSMockTestRepo(t), DefaultRegistry(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	endpoint := createTestEndpoint(t, store, callbackServer.URL, func(cfg *EndpointConfig) {
		cfg.CallbackMaxAttempts = 3
		cfg.CallbackRetryBackoffMs = 100
		duplicate := cfg.Cases["duplicate-delivered"]
		duplicate.Receipt.DelayMs = 0
		duplicate.Receipt.RepeatIntervalMs = 100
		cfg.Cases["duplicate-delivered"] = duplicate
	})
	invocation := prepareAndComplete(t, store, endpoint, map[string][]string{"__mock_case": {"duplicate-delivered"}}, testReference)
	row := waitMessage(t, store, invocation.MessageIDs[0], 5*time.Second, func(row *entity.SMSMockMessage) bool {
		return row.ReceiptStatus == entity.SMSMockReceiptSucceeded && row.ReceiptSentCount == 2
	})
	if calls.Load() != 4 || row.CallbackAttempts != 4 {
		t.Fatalf("期望首个 DLR 重试 3 次 + 重复 DLR 1 次，calls=%d row=%+v", calls.Load(), row)
	}
	if strings.Contains(row.RequestBody, "real-secret-token") || !strings.Contains(row.RequestBody, "[REDACTED]") {
		t.Fatalf("持久化请求泄漏 token: %s", row.RequestBody)
	}
	attempts, err := store.ListAttempts(row.ID)
	if err != nil || len(attempts) != 4 {
		t.Fatalf("attempts=%d err=%v", len(attempts), err)
	}
	// 已完成后手工触发会把 targetCount 增一，形成明确可观测的再次回调。
	if _, err := store.EnqueueCallback(row.ID); err != nil {
		t.Fatal(err)
	}
	row = waitMessage(t, store, row.ID, 3*time.Second, func(row *entity.SMSMockMessage) bool {
		return row.ReceiptStatus == entity.SMSMockReceiptSucceeded && row.ReceiptSentCount == 3
	})
	if calls.Load() != 5 || row.ReceiptTargetCount != 3 {
		t.Fatalf("手工重发未生效: calls=%d row=%+v", calls.Load(), row)
	}
	close(callbackBodies)
	for body := range callbackBodies {
		if !strings.Contains(body, testReference) {
			t.Fatalf("DLR 丢失 reference: %s", body)
		}
	}
}

func TestResolveCaseRulesProbabilityAndExplicitOverride(t *testing.T) {
	cfg := cmV1DefaultConfig()
	cfg.DefaultWeightedCases = []httpmock.WeightedCase{{Case: "accepted-delivered", Weight: 1}, {Case: "accepted-failed", Weight: 1}}
	cfg.Rules = []httpmock.Rule{{
		Name: "special-recipient", Priority: 10, Case: "no-dlr",
		Conditions: []httpmock.Condition{{Source: "jsonBody", Field: "recipient", Operator: "EQ", Value: "00123"}},
	}}
	request := InvokeRequest{Method: http.MethodPost, Query: map[string][]string{}, Header: http.Header{}, Body: []byte(`{}`)}
	submission := CanonicalSubmission{ProductToken: "token"}
	matched, err := resolveCase(cfg, submission, CanonicalMessage{Recipient: "00123"}, request)
	if err != nil || matched.Case != "no-dlr" || matched.MatchedRule != "special-recipient" {
		t.Fatalf("规则未命中: %+v err=%v", matched, err)
	}
	explicitReq := request
	explicitReq.Query = map[string][]string{"__mock_case": {"submit-rejected"}}
	explicit, err := resolveCase(cfg, submission, CanonicalMessage{Recipient: "00123"}, explicitReq)
	if err != nil || explicit.Case != "submit-rejected" || explicit.SelectionMode != httpmock.SelectionExplicitCase {
		t.Fatalf("显式 Case 未覆盖规则: %+v err=%v", explicit, err)
	}
	counts := map[string]int{}
	for i := 0; i < 300; i++ {
		selection, err := resolveCase(cfg, submission, CanonicalMessage{Recipient: "00999"}, request)
		if err != nil {
			t.Fatal(err)
		}
		counts[selection.Case]++
	}
	if counts["accepted-delivered"] < 90 || counts["accepted-failed"] < 90 {
		t.Fatalf("1:1 概率池未产生两种结果或严重偏离: %+v", counts)
	}
}

func TestStoreRecoversDeliveringCallbackAfterRestart(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	repo := newSMSMockTestRepo(t)
	now := time.Now().UTC()
	rows := []entity.SMSMockMessage{{
		EndpointID: 1, Token: "old", Provider: CMProvider, ProtocolVersion: CMV1,
		Reference: testReference, ReceivedAt: now.Add(-time.Minute), SubmitState: entity.SMSMockSubmitResponded,
		ReceiptEnabled: true, ReceiptStatus: entity.SMSMockReceiptDelivering,
		ReceiptTargetCount: 1, CallbackURL: server.URL, CallbackMethod: http.MethodPost,
		CallbackHeadersJSON: `{"Content-Type":["application/json"]}`,
		CallbackBody:        `{"messages":{"msg":{"reference":"` + testReference + `","status":{"code":"2"}}}}`,
		CallbackTimeoutMs:   1000, CallbackMaxAttempts: 2, CallbackRetryBackoffMs: 100,
	}}
	if err := repo.CreateSMSMockMessages(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	store, err := New(repo, DefaultRegistry(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("重启恢复后未重新投递 DELIVERING DLR")
	}
	row := waitMessage(t, store, rows[0].ID, 2*time.Second, func(row *entity.SMSMockMessage) bool {
		return row.ReceiptStatus == entity.SMSMockReceiptSucceeded
	})
	if row.ReceiptSentCount != 1 || row.CallbackAttempts != 1 {
		t.Fatalf("恢复后状态错误: %+v", row)
	}
}

func TestStoreRecoversOnlyStalePendingSubmissions(t *testing.T) {
	repo := newSMSMockTestRepo(t)
	now := time.Now().UTC()
	rows := []entity.SMSMockMessage{
		{
			Reference: "stale-submit", ReceivedAt: now.Add(-callbackClaimLease - time.Second),
			SubmitState: entity.SMSMockSubmitPending, ReceiptEnabled: true, ReceiptStatus: entity.SMSMockReceiptWaiting,
		},
		{
			Reference: "fresh-submit", ReceivedAt: now,
			SubmitState: entity.SMSMockSubmitPending, ReceiptEnabled: true, ReceiptStatus: entity.SMSMockReceiptWaiting,
		},
	}
	if err := repo.CreateSMSMockMessages(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	store, err := New(repo, DefaultRegistry(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stale, err := store.GetMessage(rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.SubmitState != entity.SMSMockSubmitClientCanceled || stale.ReceiptStatus != entity.SMSMockReceiptCanceled || stale.SubmitCompletedAt == nil {
		t.Fatalf("过期同步提交未安全终止: %+v", stale)
	}
	fresh, err := store.GetMessage(rows[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.SubmitState != entity.SMSMockSubmitPending || fresh.ReceiptStatus != entity.SMSMockReceiptWaiting {
		t.Fatalf("新实例不应终止其它实例仍在 lease 内的提交: %+v", fresh)
	}
	if err := store.CompleteSubmission(context.Background(), &Invocation{MessageIDs: []int64{rows[0].ID}}, entity.SMSMockSubmitResponded, true); err == nil {
		t.Fatal("已恢复为终态的提交不应被旧执行流重新激活")
	}
	if err := store.CompleteSubmission(context.Background(), &Invocation{MessageIDs: []int64{rows[1].ID}}, entity.SMSMockSubmitClientCanceled, false); err != nil {
		t.Fatalf("fresh 提交应仍可正常完成: %v", err)
	}
	fresh, err = store.GetMessage(rows[1].ID)
	if err != nil || fresh.ReceiptStatus != entity.SMSMockReceiptCanceled {
		t.Fatalf("未写出 Accepted 的提交不能遗留 WAITING_SUBMIT: row=%+v err=%v", fresh, err)
	}
}

func TestStoreDoesNotStealFreshCallbackClaimFromAnotherInstance(t *testing.T) {
	var calls atomic.Int32
	called := make(chan int32, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCallbacks := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseCallbacks()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		called <- n
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	repo := newSMSMockTestRepo(t)
	now := time.Now().UTC()
	due := now.Add(-time.Second)
	rows := []entity.SMSMockMessage{{
		EndpointID: 1, Token: "shared", Provider: CMProvider, ProtocolVersion: CMV1,
		Reference: testReference, ReceivedAt: now, SubmitState: entity.SMSMockSubmitResponded,
		ReceiptEnabled: true, ReceiptStatus: entity.SMSMockReceiptPending, ReceiptDueAt: &due,
		ReceiptTargetCount: 1, CallbackURL: server.URL, CallbackMethod: http.MethodPost,
		CallbackHeadersJSON: `{"Content-Type":["application/json"]}`,
		CallbackBody:        `{"messages":{"msg":{"reference":"` + testReference + `","status":{"code":"2","errorCode":""}}}}`,
		CallbackTimeoutMs:   5000, CallbackMaxAttempts: 2, CallbackRetryBackoffMs: 100,
	}}
	if err := repo.CreateSMSMockMessages(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	store1, err := New(repo, DefaultRegistry(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store1.Close()
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("第一实例未开始 DLR")
	}
	store2, err := New(repo, DefaultRegistry(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	select {
	case n := <-called:
		t.Fatalf("第二实例窃取了仍在 lease 内的 DLR claim，call=%d", n)
	case <-time.After(300 * time.Millisecond):
	}
	releaseCallbacks()
	row := waitMessage(t, store1, rows[0].ID, 3*time.Second, func(row *entity.SMSMockMessage) bool {
		return row.ReceiptStatus == entity.SMSMockReceiptSucceeded
	})
	if row.ReceiptSentCount != 1 || calls.Load() != 1 {
		t.Fatalf("多实例 claim 结果错误: calls=%d row=%+v", calls.Load(), row)
	}
}

func TestStoreDataPlaneRefreshesEndpointAcrossInstances(t *testing.T) {
	repo := newSMSMockTestRepo(t)
	store1, err := New(repo, DefaultRegistry(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store1.Close()
	// 第二实例先启动，证明不是依靠启动快照看到随后由第一实例创建的 Endpoint。
	store2, err := New(repo, DefaultRegistry(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	endpoint := createTestEndpoint(t, store1, "http://127.0.0.1/callback", nil)
	changedProtocol := *endpoint
	changedProtocol.ProtocolVersion = "v2"
	if _, err := store1.Upsert(changedProtocol); err == nil {
		t.Fatal("现有 Endpoint 不应原地切协议版本，升级必须新建以保留旧用例")
	}
	invocation, err := store2.PrepareInvocation(context.Background(), InvokeRequest{
		Provider: "cm", Token: endpoint.Token, Method: http.MethodPost,
		Query: map[string][]string{"__mock_case": {"no-dlr"}}, Body: []byte(cmRequestBody(testReference, "hello")),
	})
	if err != nil {
		t.Fatalf("第二实例未刷新新 Endpoint: %v", err)
	}
	if err := store2.CompleteSubmission(context.Background(), invocation, entity.SMSMockSubmitResponded, true); err != nil {
		t.Fatal(err)
	}
	if err := store1.Delete(endpoint.ID); err != nil {
		t.Fatal(err)
	}
	_, err = store2.PrepareInvocation(context.Background(), InvokeRequest{
		Provider: "cm", Token: endpoint.Token, Method: http.MethodPost,
		Body: []byte(cmRequestBody("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "hello")),
	})
	var invokeErr *InvokeError
	if !errors.As(err, &invokeErr) || invokeErr.Response.HTTPStatus != http.StatusNotFound {
		t.Fatalf("第二实例应立即看到 Endpoint 已删除: err=%v", err)
	}
}

func TestStoreCancelWaitingReceiptAndManualResendAfterSubmit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, err := New(newSMSMockTestRepo(t), DefaultRegistry(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	endpoint := createTestEndpoint(t, store, server.URL, func(cfg *EndpointConfig) {
		delivered := cfg.Cases["accepted-delivered"]
		delivered.Receipt.DelayMs = 0
		cfg.Cases["accepted-delivered"] = delivered
	})
	invocation, err := store.PrepareInvocation(context.Background(), InvokeRequest{
		Provider: "cm", Token: endpoint.Token, Method: http.MethodPost,
		Body: []byte(cmRequestBody(testReference, "hello")),
	})
	if err != nil {
		t.Fatal(err)
	}
	messageID := invocation.MessageIDs[0]
	if _, err := store.EnqueueCallback(messageID); err == nil {
		t.Fatal("同步响应完成前不应允许提前投递 DLR")
	}
	if err := store.CancelCallback(messageID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSubmission(context.Background(), invocation, entity.SMSMockSubmitResponded, true); err != nil {
		t.Fatal(err)
	}
	row := waitMessage(t, store, messageID, time.Second, func(row *entity.SMSMockMessage) bool {
		return row.SubmitState == entity.SMSMockSubmitResponded && row.ReceiptStatus == entity.SMSMockReceiptCanceled
	})
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("已取消的 WAITING_SUBMIT 被重新激活: calls=%d row=%+v", calls.Load(), row)
	}
	if _, err := store.EnqueueCallback(messageID); err != nil {
		t.Fatal(err)
	}
	row = waitMessage(t, store, messageID, 3*time.Second, func(row *entity.SMSMockMessage) bool {
		return row.ReceiptStatus == entity.SMSMockReceiptSucceeded
	})
	if calls.Load() != 1 || row.ReceiptSentCount != 1 {
		t.Fatalf("提交完成后的手工重发未生效: calls=%d row=%+v", calls.Load(), row)
	}
}

func TestStoreClearWaitsForInflightAndSkipsQueuedCallbacks(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, callbackWorkerCount+1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCallbacks := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseCallbacks()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, err := New(newSMSMockTestRepo(t), DefaultRegistry(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	endpoint := createTestEndpoint(t, store, server.URL, func(cfg *EndpointConfig) {
		delivered := cfg.Cases["accepted-delivered"]
		delivered.Receipt.DelayMs = 0
		cfg.Cases["accepted-delivered"] = delivered
	})
	for i := 0; i < callbackWorkerCount+1; i++ {
		reference := fmt.Sprintf("%032d", i+1)
		prepareAndComplete(t, store, endpoint, map[string][]string{"__mock_case": {"accepted-delivered"}}, reference)
	}
	for i := 0; i < callbackWorkerCount; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("未填满回调 worker")
		}
	}
	rows, err := store.ListMessages(entity.SMSMockMessageFilter{EndpointID: endpoint.ID, Limit: callbackWorkerCount + 1})
	if err != nil {
		t.Fatal(err)
	}
	delivering, pending := 0, 0
	for _, row := range rows {
		switch row.ReceiptStatus {
		case entity.SMSMockReceiptDelivering:
			delivering++
		case entity.SMSMockReceiptPending:
			pending++
		}
	}
	if delivering != callbackWorkerCount || pending != 1 {
		t.Fatalf("调度器不应预 claim 超过空闲 worker 的任务: delivering=%d pending=%d rows=%+v", delivering, pending, rows)
	}
	deleted := make(chan error, 1)
	go func() {
		_, err := store.DeleteMessages(endpoint.ID)
		deleted <- err
	}()
	select {
	case err := <-deleted:
		t.Fatalf("清空不应越过进行中的外部回调: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	releaseCallbacks()
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("回调结束后清空仍未返回")
	}
	time.Sleep(300 * time.Millisecond)
	if calls.Load() != callbackWorkerCount {
		t.Fatalf("队列中的陈旧任务仍发出回调: calls=%d want=%d", calls.Load(), callbackWorkerCount)
	}
	if rows, err := store.ListMessages(entity.SMSMockMessageFilter{EndpointID: endpoint.ID}); err != nil || len(rows) != 0 {
		t.Fatalf("消息未清空: rows=%+v err=%v", rows, err)
	}
}

func TestCallbackURLPolicyAndRedirectBlocking(t *testing.T) {
	store, err := New(newSMSMockTestRepo(t), DefaultRegistry(), "arke.internal,*.example.com,127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "exact-host-with-port", url: "http://arke.internal:8080/public/sms/callback"},
		{name: "wildcard-subdomain", url: "https://sms.example.com/callback"},
		{name: "wildcard-does-not-include-apex", url: "https://example.com/callback", wantErr: true},
		{name: "userinfo", url: "http://user:secret@arke.internal/callback", wantErr: true},
		{name: "unsupported-scheme", url: "file:///tmp/callback", wantErr: true},
		{name: "unlisted-host", url: "http://attacker.invalid/callback", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := store.validateCallbackURL(tc.url)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateCallbackURL(%q) err=%v wantErr=%v", tc.url, err, tc.wantErr)
			}
		})
	}

	var redirectedCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	endpoint := createTestEndpoint(t, store, redirect.URL, func(cfg *EndpointConfig) {
		cfg.CallbackMaxAttempts = 1
		delivered := cfg.Cases["accepted-delivered"]
		delivered.Receipt.DelayMs = 0
		cfg.Cases["accepted-delivered"] = delivered
	})
	invocation := prepareAndComplete(t, store, endpoint, map[string][]string{"__mock_case": {"accepted-delivered"}}, testReference)
	row := waitMessage(t, store, invocation.MessageIDs[0], 3*time.Second, func(row *entity.SMSMockMessage) bool {
		return row.ReceiptStatus == entity.SMSMockReceiptFailed
	})
	if redirectedCalls.Load() != 0 || row.CallbackLastHTTPStatus != http.StatusFound {
		t.Fatalf("DLR 不应跟随 redirect: redirectedCalls=%d row=%+v", redirectedCalls.Load(), row)
	}
}
