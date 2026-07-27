package smsmock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	CMProvider = "CM"
	CMV1       = "v1"
)

var (
	cmSubmitTemplateVariables  = []string{"reference", "recipient", "sender", "content", "parts"}
	cmReceiptTemplateVariables = []string{"reference", "recipient", "sender", "content", "receivedAt", "statusCode", "errorCode", "errorDescription", "operator"}
)

type cmV1Adapter struct{}

func NewCMV1Adapter() Adapter                { return &cmV1Adapter{} }
func (*cmV1Adapter) Provider() string        { return CMProvider }
func (*cmV1Adapter) ProtocolVersion() string { return CMV1 }

type cmRequest struct {
	Messages struct {
		Authentication struct {
			ProductToken string `json:"producttoken"`
		} `json:"authentication"`
		Msg []struct {
			From string `json:"from"`
			To   []struct {
				Number string `json:"number"`
			} `json:"to"`
			MinimumParts int `json:"minimumNumberOfMessageParts"`
			MaximumParts int `json:"maximumNumberOfMessageParts"`
			Body         struct {
				Type    string `json:"type"`
				Content string `json:"content"`
			} `json:"body"`
			Reference string `json:"reference"`
		} `json:"msg"`
	} `json:"messages"`
}

func (a *cmV1Adapter) ParseSubmission(request InvokeRequest) (CanonicalSubmission, error) {
	if request.Method != http.MethodPost {
		return CanonicalSubmission{}, &RequestError{Status: http.StatusMethodNotAllowed, Err: fmt.Errorf("CM v1 短信提交仅支持 POST")}
	}
	var parsed cmRequest
	decoder := json.NewDecoder(bytes.NewReader(request.Body))
	if err := decoder.Decode(&parsed); err != nil {
		return CanonicalSubmission{}, fmt.Errorf("解析 CM v1 请求失败: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return CanonicalSubmission{}, fmt.Errorf("解析 CM v1 请求失败: JSON 末尾存在多余内容")
	}
	if len(parsed.Messages.Msg) == 0 {
		return CanonicalSubmission{}, fmt.Errorf("messages.msg 不能为空")
	}
	if len(parsed.Messages.Msg) > 100 {
		return CanonicalSubmission{}, fmt.Errorf("messages.msg 最多 100 条")
	}
	result := CanonicalSubmission{ProductToken: parsed.Messages.Authentication.ProductToken}
	seenReferences := make(map[string]struct{}, len(parsed.Messages.Msg))
	for i, item := range parsed.Messages.Msg {
		if strings.TrimSpace(item.Reference) == "" {
			return CanonicalSubmission{}, fmt.Errorf("messages.msg[%d].reference 不能为空", i)
		}
		if len(item.Reference) > 128 {
			return CanonicalSubmission{}, fmt.Errorf("messages.msg[%d].reference 最长 128 bytes", i)
		}
		if _, duplicate := seenReferences[item.Reference]; duplicate {
			return CanonicalSubmission{}, fmt.Errorf("messages.msg[%d].reference %q 重复", i, item.Reference)
		}
		seenReferences[item.Reference] = struct{}{}
		if len(item.To) != 1 || strings.TrimSpace(item.To[0].Number) == "" {
			return CanonicalSubmission{}, fmt.Errorf("messages.msg[%d].to 必须且只能包含一个非空号码", i)
		}
		if len(item.To[0].Number) > 64 {
			return CanonicalSubmission{}, fmt.Errorf("messages.msg[%d].to[0].number 最长 64 bytes", i)
		}
		if len(item.From) > 128 {
			return CanonicalSubmission{}, fmt.Errorf("messages.msg[%d].from 最长 128 bytes", i)
		}
		if len(item.Body.Content) > 65_535 {
			return CanonicalSubmission{}, fmt.Errorf("messages.msg[%d].body.content 最长 65535 bytes", i)
		}
		result.Messages = append(result.Messages, CanonicalMessage{
			Reference: item.Reference,
			Recipient: item.To[0].Number,
			Sender:    item.From,
			Content:   item.Body.Content,
			Metadata: map[string]string{
				"bodyType":                    item.Body.Type,
				"minimumNumberOfMessageParts": fmt.Sprint(item.MinimumParts),
				"maximumNumberOfMessageParts": fmt.Sprint(item.MaximumParts),
			},
		})
	}
	return result, nil
}

func (*cmV1Adapter) SanitizeSubmission(request InvokeRequest) string {
	var raw map[string]any
	if json.Unmarshal(request.Body, &raw) != nil {
		return "[invalid CM request body omitted]"
	}
	if messages, ok := raw["messages"].(map[string]any); ok {
		if auth, ok := messages["authentication"].(map[string]any); ok {
			if _, exists := auth["producttoken"]; exists {
				auth["producttoken"] = "[REDACTED]"
			}
		}
	}
	clean, err := json.Marshal(raw)
	if err != nil {
		return "[CM request body omitted]"
	}
	return string(clean)
}

func (a *cmV1Adapter) BuildSubmissionResponse(outcomes []MessageOutcome) (WireResponse, error) {
	if len(outcomes) == 0 {
		return WireResponse{}, fmt.Errorf("CM v1 响应至少需要一条 message")
	}
	if err := validateCMV1Batch(outcomes); err != nil {
		return WireResponse{}, err
	}
	first := outcomes[0].Spec.Submit
	response := WireResponse{
		Action: first.Action, HTTPStatus: first.HTTPStatus, ContentType: "application/json; charset=utf-8",
		Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		DelayMs: first.DelayMs, TimeoutMs: first.TimeoutMs,
	}
	if first.RawBodyTemplate != "" {
		if len(outcomes) != 1 {
			return WireResponse{}, fmt.Errorf("CM 自定义响应模板仅支持单消息请求")
		}
		body, err := renderTemplate(first.RawBodyTemplate, templateData{Message: outcomes[0].Message, Parts: first.Parts}, cmSubmitTemplateVariables)
		if err != nil {
			return WireResponse{}, err
		}
		response.Body = body
		if first.Result != SubmitResultMalformed && !json.Valid([]byte(body)) {
			return WireResponse{}, fmt.Errorf("CM 自定义响应模板渲染后不是合法 JSON")
		}
		if first.Result != SubmitResultMalformed && !cmSubmissionContainsReference(body, outcomes[0].Message.Reference) {
			return WireResponse{}, fmt.Errorf("CM 自定义响应模板必须保留精确 reference")
		}
		return response, nil
	}
	if first.Result == SubmitResultMalformed {
		response.ContentType = "text/plain; charset=utf-8"
		response.Headers.Set("Content-Type", response.ContentType)
		response.Body = "not-json"
		return response, nil
	}
	type messageDetail struct {
		To               string `json:"to"`
		Status           string `json:"status"`
		Reference        string `json:"reference"`
		Parts            int    `json:"parts"`
		MessageDetails   string `json:"messageDetails"`
		MessageErrorCode int    `json:"messageErrorCode"`
	}
	payload := struct {
		Details   string          `json:"details"`
		ErrorCode int             `json:"errorCode"`
		Messages  []messageDetail `json:"messages"`
	}{Details: fmt.Sprintf("Created %d message(s)", len(outcomes)), Messages: make([]messageDetail, 0, len(outcomes))}
	for _, outcome := range outcomes {
		spec := outcome.Spec.Submit
		status := "Accepted"
		if spec.Result == SubmitResultRejected {
			status = "Rejected"
			payload.ErrorCode = spec.ErrorCode
			if payload.ErrorCode == 0 {
				payload.ErrorCode = 10
			}
			if spec.Details != "" {
				payload.Details = spec.Details
			}
		}
		payload.Messages = append(payload.Messages, messageDetail{
			To: outcome.Message.Recipient, Status: status, Reference: outcome.Message.Reference,
			Parts: spec.Parts, MessageDetails: spec.Details, MessageErrorCode: spec.ErrorCode,
		})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return WireResponse{}, err
	}
	response.Body = string(body)
	return response, nil
}

func (*cmV1Adapter) BuildReceipt(message CanonicalMessage, spec ReceiptSpec, receivedAt time.Time) (WireCallback, error) {
	if spec.RawBodyTemplate != "" {
		body, err := renderTemplate(spec.RawBodyTemplate, templateData{
			Message: message, ReceivedAt: receivedAt, StatusCode: spec.StatusCode,
			ErrorCode: spec.ErrorCode, ErrorDescription: spec.ErrorDescription, Operator: spec.Operator,
		}, cmReceiptTemplateVariables)
		if err != nil {
			return WireCallback{}, err
		}
		if !json.Valid([]byte(body)) {
			return WireCallback{}, fmt.Errorf("CM DLR 模板渲染后不是合法 JSON")
		}
		if !cmReceiptContainsReference(body, message.Reference) {
			return WireCallback{}, fmt.Errorf("CM DLR 模板必须保留精确 reference")
		}
		return WireCallback{Method: http.MethodPost, Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Body: body}, nil
	}
	payload := map[string]any{"messages": map[string]any{"msg": map[string]any{
		"received": receivedAt.UTC().Format("2006-01-02T15:04:05"),
		"to":       message.Recipient, "reference": message.Reference,
		"status":   map[string]string{"code": spec.StatusCode, "errorCode": spec.ErrorCode, "errorDescription": spec.ErrorDescription},
		"operator": spec.Operator,
	}}}
	body, err := json.Marshal(payload)
	if err != nil {
		return WireCallback{}, err
	}
	return WireCallback{Method: http.MethodPost, Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Body: string(body)}, nil
}

// CM 的一次 HTTP 请求只能有一套 transport 行为（HTTP 状态、延迟、超时或畸形响应）。
// Arke 当前固定单消息提交；这里允许同类多消息，但拒绝把互不兼容的 Case 静默压成第一条的行为。
func validateCMV1Batch(outcomes []MessageOutcome) error {
	if len(outcomes) <= 1 {
		return nil
	}
	first := outcomes[0].Spec.Submit
	if first.RawBodyTemplate != "" || first.Result == SubmitResultCustom {
		return fmt.Errorf("CM 自定义响应模板仅支持单消息请求")
	}
	for i := 1; i < len(outcomes); i++ {
		current := outcomes[i].Spec.Submit
		if current.RawBodyTemplate != "" || current.Result == SubmitResultCustom {
			return fmt.Errorf("CM 自定义响应模板仅支持单消息请求")
		}
		if current.Action != first.Action || current.Result != first.Result ||
			current.HTTPStatus != first.HTTPStatus || current.DelayMs != first.DelayMs ||
			current.TimeoutMs != first.TimeoutMs {
			return fmt.Errorf("CM 批量请求中的 Case 提交行为不兼容（message %d）", i)
		}
		if first.Result == SubmitResultRejected && (current.ErrorCode != first.ErrorCode || current.Details != first.Details) {
			return fmt.Errorf("CM 批量请求中的拒绝错误不一致（message %d）", i)
		}
	}
	return nil
}

func cmSubmissionContainsReference(body, expected string) bool {
	var payload struct {
		Messages []struct {
			Reference string `json:"reference"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil {
		return false
	}
	for _, message := range payload.Messages {
		if message.Reference == expected {
			return true
		}
	}
	return false
}

func cmReceiptContainsReference(body, expected string) bool {
	var payload struct {
		Messages struct {
			Message struct {
				Reference string `json:"reference"`
			} `json:"msg"`
		} `json:"messages"`
	}
	return json.Unmarshal([]byte(body), &payload) == nil && payload.Messages.Message.Reference == expected
}

func (*cmV1Adapter) BuildProtocolError(status int, cause error) WireResponse {
	if status < 400 || status > 599 {
		status = http.StatusBadRequest
	}
	body, _ := json.Marshal(map[string]any{"details": cause.Error(), "errorCode": status, "messages": []any{}})
	return WireResponse{
		Action: SubmitActionRespond, HTTPStatus: status, ContentType: "application/json; charset=utf-8",
		Headers: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Body: string(body),
	}
}

func (a *cmV1Adapter) ValidateCase(spec CaseSpec) error {
	sample := CanonicalMessage{Reference: "0123456789abcdef0123456789abcdef", Recipient: "005215512345678", Sender: "SMS", Content: "hello"}
	if _, err := a.BuildSubmissionResponse([]MessageOutcome{{Message: sample, Spec: spec}}); err != nil {
		return err
	}
	if spec.Receipt.Enabled {
		if _, err := a.BuildReceipt(sample, spec.Receipt, time.Unix(0, 0)); err != nil {
			return err
		}
	}
	return nil
}

func (a *cmV1Adapter) Info() ProviderInfo {
	return ProviderInfo{
		Provider: a.Provider(), ProtocolVersion: a.ProtocolVersion(), DisplayName: "CM Messaging Gateway v1",
		HermesVendorName: "CM",
		HermesConfig: map[string]any{
			"url": "${invokeUrl}", "key": "mock-product-token", "defaultSender": "SMS",
		},
		HermesCallbackHint: "http://hermes-arke:8080/public/sms/callback?provider=cm",
		SubmitContentType:  "application/json", ReceiptContentType: "application/json",
		MatchFields: []ProviderField{
			{Path: "reference", Label: "Reference", Description: "Arke 生成的 32 位 smsUUID"},
			{Path: "recipient", Label: "收件号码"}, {Path: "sender", Label: "Sender"},
			{Path: "content", Label: "短信内容"}, {Path: "productToken", Label: "Product Token"},
			{Path: "metadata.bodyType", Label: "Body Type"},
		},
		SubmitTemplateVariables:  append([]string(nil), cmSubmitTemplateVariables...),
		ReceiptTemplateVariables: append([]string(nil), cmReceiptTemplateVariables...),
		DefaultConfig:            cmV1DefaultConfig(),
	}
}

func cmV1DefaultConfig() EndpointConfig {
	delivered := CaseSpec{
		Submit: SubmissionSpec{Action: SubmitActionRespond, Result: SubmitResultAccepted, HTTPStatus: 200, Parts: 1},
		// Hermes SmsCmService 以 errorCode.isBlank() 判断成功，成功 DLR 必须保留空字符串。
		Receipt: ReceiptSpec{Enabled: true, DelayMs: 500, StatusCode: "2", ErrorCode: "", Operator: "MOCK", Repeat: 1},
	}
	failed := CaseSpec{
		Submit:  SubmissionSpec{Action: SubmitActionRespond, Result: SubmitResultAccepted, HTTPStatus: 200, Parts: 1},
		Receipt: ReceiptSpec{Enabled: true, DelayMs: 500, StatusCode: "3", ErrorCode: "206", ErrorDescription: "Rejected", Operator: "MOCK", Repeat: 1},
	}
	duplicate := delivered
	duplicate.Receipt.Repeat = 2
	duplicate.Receipt.RepeatIntervalMs = 100
	return EndpointConfig{
		CallbackTimeoutMs: 5000, CallbackMaxAttempts: 3, CallbackRetryBackoffMs: 1000,
		AllowCaseOverride: true, DefaultCase: "accepted-delivered",
		Cases: map[string]CaseSpec{
			"accepted-delivered": delivered,
			"accepted-failed":    failed,
			"submit-rejected": {
				Submit: SubmissionSpec{Action: SubmitActionRespond, Result: SubmitResultRejected, HTTPStatus: 200, ErrorCode: 10, Details: "Authentication failed", Parts: 1},
			},
			"submit-timeout": {
				Submit: SubmissionSpec{Action: SubmitActionTimeout, Result: SubmitResultAccepted, HTTPStatus: 200, TimeoutMs: 31_000, Parts: 1},
			},
			"malformed-response": {
				Submit: SubmissionSpec{Action: SubmitActionRespond, Result: SubmitResultMalformed, HTTPStatus: 200, Parts: 1},
			},
			"no-dlr": {
				Submit: SubmissionSpec{Action: SubmitActionRespond, Result: SubmitResultAccepted, HTTPStatus: 200, Parts: 1},
			},
			"duplicate-delivered": duplicate,
		},
	}
}
