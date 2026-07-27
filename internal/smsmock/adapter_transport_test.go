package smsmock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermes-mock/internal/entity"
)

// transportTestAdapter 刻意使用非 POST/JSON 的提交和非 POST 的 DLR，证明新增厂商
// 只需实现 Adapter，不需要修改 Store 的选择、持久化、调度或重试核心。
type transportTestAdapter struct{}

func (*transportTestAdapter) Provider() string        { return "TRANSPORT_TEST" }
func (*transportTestAdapter) ProtocolVersion() string { return "v9" }
func (a *transportTestAdapter) Info() ProviderInfo {
	return ProviderInfo{Provider: a.Provider(), ProtocolVersion: a.ProtocolVersion(), DisplayName: "transport test"}
}
func (*transportTestAdapter) ParseSubmission(request InvokeRequest) (CanonicalSubmission, error) {
	if request.Method != http.MethodPatch {
		return CanonicalSubmission{}, &RequestError{Status: http.StatusMethodNotAllowed, Err: fmt.Errorf("需要 PATCH")}
	}
	reference := ""
	if values := request.Query["reference"]; len(values) > 0 {
		reference = values[0]
	}
	recipient := request.Header.Get("X-Recipient")
	if reference == "" || recipient == "" {
		return CanonicalSubmission{}, fmt.Errorf("缺少 reference/recipient")
	}
	return CanonicalSubmission{Messages: []CanonicalMessage{{Reference: reference, Recipient: recipient, Content: string(request.Body)}}}, nil
}
func (*transportTestAdapter) SanitizeSubmission(request InvokeRequest) string {
	return string(request.Body)
}
func (*transportTestAdapter) BuildSubmissionResponse(outcomes []MessageOutcome) (WireResponse, error) {
	return WireResponse{
		Action: SubmitActionRespond, HTTPStatus: http.StatusAccepted,
		Headers: http.Header{"Content-Type": []string{"text/plain"}, "X-Vendor-Ack": []string{outcomes[0].Message.Reference}},
		Body:    "queued",
	}, nil
}
func (*transportTestAdapter) BuildReceipt(message CanonicalMessage, _ ReceiptSpec, _ time.Time) (WireCallback, error) {
	return WireCallback{
		Method: http.MethodPut,
		Headers: http.Header{
			"Content-Type": []string{"text/plain"},
			"X-Signature":  []string{"adapter-owned-signature"},
		},
		Body: "delivered=" + message.Reference,
	}, nil
}
func (*transportTestAdapter) BuildProtocolError(status int, err error) WireResponse {
	return WireResponse{Action: SubmitActionRespond, HTTPStatus: status, Body: err.Error()}
}
func (*transportTestAdapter) ValidateCase(CaseSpec) error { return nil }

func TestAdapterOwnsSubmissionAndReceiptHTTPTransport(t *testing.T) {
	type callback struct {
		method    string
		signature string
		body      string
	}
	callbacks := make(chan callback, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		callbacks <- callback{method: r.Method, signature: r.Header.Get("X-Signature"), body: string(body)}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	adapter := &transportTestAdapter{}
	store, err := New(newSMSMockTestRepo(t), NewRegistry(adapter), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	endpoint, err := store.Upsert(Endpoint{
		Name: "non-post-provider", Enabled: true, Provider: adapter.Provider(), ProtocolVersion: adapter.ProtocolVersion(),
		Config: EndpointConfig{
			CallbackURL: server.URL, CallbackTimeoutMs: 1000, CallbackMaxAttempts: 1, CallbackRetryBackoffMs: 100,
			DefaultCase: "delivered", Cases: map[string]CaseSpec{
				"delivered": {
					Submit:  SubmissionSpec{Action: SubmitActionRespond, Result: SubmitResultAccepted, HTTPStatus: 202, Parts: 1},
					Receipt: ReceiptSpec{Enabled: true, Repeat: 1},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reference := "provider-specific-reference"
	invocation, err := store.PrepareInvocation(context.Background(), InvokeRequest{
		Provider: adapter.Provider(), Token: endpoint.Token, Method: http.MethodPatch,
		Query: map[string][]string{"reference": {reference}}, Header: http.Header{"X-Recipient": []string{"00123"}},
		Body: []byte("form-like-payload"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Response.HTTPStatus != http.StatusAccepted || invocation.Response.Headers.Get("X-Vendor-Ack") != reference {
		t.Fatalf("Adapter 同步 transport 丢失: %+v", invocation.Response)
	}
	if err := store.CompleteSubmission(context.Background(), invocation, entity.SMSMockSubmitResponded, true); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-callbacks:
		if got.method != http.MethodPut || got.signature != "adapter-owned-signature" || got.body != "delivered="+reference {
			t.Fatalf("Adapter DLR transport 丢失: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到 Adapter 自定义 transport 的 DLR")
	}
	row := waitMessage(t, store, invocation.MessageIDs[0], 2*time.Second, func(row *entity.SMSMockMessage) bool {
		return row.ReceiptStatus == entity.SMSMockReceiptSucceeded
	})
	var headers http.Header
	if err := json.Unmarshal([]byte(row.CallbackHeadersJSON), &headers); err != nil {
		t.Fatal(err)
	}
	if row.CallbackMethod != http.MethodPut || headers.Get("X-Signature") != "adapter-owned-signature" {
		t.Fatalf("DLR transport 未完整持久化: %+v", row)
	}
}
