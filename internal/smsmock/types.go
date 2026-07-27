// Package smsmock 提供可插拔短信厂商协议 Mock：厂商无关核心负责规则、概率、
// 持久化 DLR 状态机与重试；Adapter 只负责具体线协议的解析和报文生成。
package smsmock

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"hermes-mock/internal/httpmock"
)

const (
	SubmitActionRespond = "RESPOND"
	SubmitActionTimeout = "TIMEOUT"

	SubmitResultAccepted  = "ACCEPTED"
	SubmitResultRejected  = "REJECTED"
	SubmitResultMalformed = "MALFORMED"
	SubmitResultCustom    = "CUSTOM"

	maxWaitMs        = 120_000
	maxReceiptDelay  = 7 * 24 * 60 * 60 * 1000
	maxRepeatCount   = 20
	maxCallbackTries = 10
	maxTemplateBytes = 512 * 1024
)

var smsCaseNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

// SubmissionSpec 描述厂商同步提交阶段，不含任何厂商字段名。
type SubmissionSpec struct {
	Action          string `json:"action,omitempty"`
	Result          string `json:"result,omitempty"`
	HTTPStatus      int    `json:"httpStatus,omitempty"`
	DelayMs         int    `json:"delayMs,omitempty"`
	TimeoutMs       int    `json:"timeoutMs,omitempty"`
	ErrorCode       int    `json:"errorCode,omitempty"`
	Details         string `json:"details,omitempty"`
	Parts           int    `json:"parts,omitempty"`
	RawBodyTemplate string `json:"rawBodyTemplate,omitempty"`
}

// ReceiptSpec 描述异步 DLR。Repeat 是总投递次数（用于模拟厂商重复回调），
// 单次投递失败的网络重试由 EndpointConfig 的 retry 参数控制。
type ReceiptSpec struct {
	Enabled          bool   `json:"enabled"`
	DelayMs          int    `json:"delayMs,omitempty"`
	StatusCode       string `json:"statusCode,omitempty"`
	ErrorCode        string `json:"errorCode,omitempty"`
	ErrorDescription string `json:"errorDescription,omitempty"`
	Operator         string `json:"operator,omitempty"`
	Repeat           int    `json:"repeat,omitempty"`
	RepeatIntervalMs int    `json:"repeatIntervalMs,omitempty"`
	RawBodyTemplate  string `json:"rawBodyTemplate,omitempty"`
}

type CaseSpec struct {
	Submit  SubmissionSpec `json:"submit"`
	Receipt ReceiptSpec    `json:"receipt"`
}

// EndpointConfig 是厂商无关配置。规则复用 httpmock 的 method/query/header/jsonBody/rawBody
// 条件与固定/概率 Case；其中 jsonBody 指规范化后的 reference/recipient/sender/content、
// productToken、message metadata 与 submissionMetadata。
type EndpointConfig struct {
	CallbackURL            string                  `json:"callbackUrl"`
	CallbackTimeoutMs      int                     `json:"callbackTimeoutMs,omitempty"`
	CallbackMaxAttempts    int                     `json:"callbackMaxAttempts,omitempty"`
	CallbackRetryBackoffMs int                     `json:"callbackRetryBackoffMs,omitempty"`
	AllowCaseOverride      bool                    `json:"allowCaseOverride"`
	DefaultCase            string                  `json:"defaultCase"`
	DefaultWeightedCases   []httpmock.WeightedCase `json:"defaultWeightedCases,omitempty"`
	Cases                  map[string]CaseSpec     `json:"cases"`
	Rules                  []httpmock.Rule         `json:"rules,omitempty"`
}

type Endpoint struct {
	ID              int64          `json:"id"`
	Token           string         `json:"token"`
	Name            string         `json:"name"`
	Enabled         bool           `json:"enabled"`
	Provider        string         `json:"provider"`
	ProtocolVersion string         `json:"protocolVersion"`
	Config          EndpointConfig `json:"config"`
	Remark          string         `json:"remark,omitempty"`
	GmtCreate       time.Time      `json:"gmtCreate,omitempty"`
	GmtModified     time.Time      `json:"gmtModified,omitempty"`
	InvokePath      string         `json:"invokePath,omitempty"`
	InvokeURL       string         `json:"invokeUrl,omitempty"`
}

// CanonicalSubmission/Message 是全部 Adapter 与核心之间的稳定契约。
type CanonicalSubmission struct {
	ProductToken string
	Messages     []CanonicalMessage
	Metadata     map[string]string
}

type CanonicalMessage struct {
	Reference string
	Recipient string
	Sender    string
	Content   string
	Metadata  map[string]string
}

type MessageOutcome struct {
	Message CanonicalMessage
	Case    string
	Spec    CaseSpec
}

type WireResponse struct {
	Action      string
	HTTPStatus  int
	ContentType string
	Headers     http.Header
	Body        string
	DelayMs     int
	TimeoutMs   int
}

// WireCallback 是 Adapter 生成、核心持久化并异步投递的完整 DLR HTTP 请求。
// URL 仍由 Endpoint 配置，method/header/body 完全由具体厂商协议决定。
type WireCallback struct {
	Method  string
	Headers http.Header
	Body    string
}

type ProviderField struct {
	Path        string `json:"path"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type ProviderInfo struct {
	Provider                 string          `json:"provider"`
	ProtocolVersion          string          `json:"protocolVersion"`
	DisplayName              string          `json:"displayName"`
	HermesVendorName         string          `json:"hermesVendorName"`
	HermesConfig             map[string]any  `json:"hermesConfig"`
	HermesCallbackHint       string          `json:"hermesCallbackHint"`
	SubmitContentType        string          `json:"submitContentType"`
	ReceiptContentType       string          `json:"receiptContentType"`
	MatchFields              []ProviderField `json:"matchFields"`
	SubmitTemplateVariables  []string        `json:"submitTemplateVariables"`
	ReceiptTemplateVariables []string        `json:"receiptTemplateVariables"`
	DefaultConfig            EndpointConfig  `json:"defaultConfig"`
}

type Selection struct {
	Case           string
	MatchedRule    string
	SelectionMode  string
	SelectedWeight int
	TotalWeight    int
}

// Invocation 是 PrepareInvocation 同步持久化后的执行计划。
type Invocation struct {
	Endpoint   *Endpoint
	Response   WireResponse
	MessageIDs []int64
}

type InvokeRequest struct {
	Provider string
	Token    string
	Method   string
	Query    map[string][]string
	Header   http.Header
	Body     []byte
	Remote   string
}

type InvokeError struct {
	Response WireResponse
	Err      error
}

func (e *InvokeError) Error() string { return e.Err.Error() }
func (e *InvokeError) Unwrap() error { return e.Err }

// RequestError 让 Adapter 为协议级请求错误选择正确 HTTP 状态（例如 method 不支持为 405）。
// 未包装的解析错误由核心按 400 处理。
type RequestError struct {
	Status int
	Err    error
}

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

func normalizeEndpoint(endpoint *Endpoint, adapter Adapter, validateURL func(string) error) error {
	endpoint.Name = strings.TrimSpace(endpoint.Name)
	if endpoint.Name == "" || len(endpoint.Name) > 128 {
		return fmt.Errorf("name 必填且最长 128 字符")
	}
	if len(endpoint.Remark) > 255 {
		return fmt.Errorf("remark 最长 255 字符")
	}
	endpoint.Provider = strings.ToUpper(strings.TrimSpace(endpoint.Provider))
	endpoint.ProtocolVersion = strings.ToLower(strings.TrimSpace(endpoint.ProtocolVersion))
	if endpoint.Provider != adapter.Provider() || endpoint.ProtocolVersion != adapter.ProtocolVersion() {
		return fmt.Errorf("Endpoint 协议 %s/%s 与适配器不一致", endpoint.Provider, endpoint.ProtocolVersion)
	}
	cfg := &endpoint.Config
	cfg.CallbackURL = strings.TrimSpace(cfg.CallbackURL)
	if cfg.CallbackTimeoutMs == 0 {
		cfg.CallbackTimeoutMs = 5000
	}
	if cfg.CallbackMaxAttempts == 0 {
		cfg.CallbackMaxAttempts = 3
	}
	if cfg.CallbackRetryBackoffMs == 0 {
		cfg.CallbackRetryBackoffMs = 1000
	}
	if cfg.CallbackTimeoutMs < 100 || cfg.CallbackTimeoutMs > maxWaitMs {
		return fmt.Errorf("callbackTimeoutMs 必须在 100-%d", maxWaitMs)
	}
	if cfg.CallbackMaxAttempts < 1 || cfg.CallbackMaxAttempts > maxCallbackTries {
		return fmt.Errorf("callbackMaxAttempts 必须在 1-%d", maxCallbackTries)
	}
	if cfg.CallbackRetryBackoffMs < 100 || cfg.CallbackRetryBackoffMs > maxWaitMs {
		return fmt.Errorf("callbackRetryBackoffMs 必须在 100-%d", maxWaitMs)
	}
	if len(cfg.Cases) == 0 {
		return fmt.Errorf("cases 不能为空")
	}
	normalizedCases := make(map[string]CaseSpec, len(cfg.Cases))
	hasReceipt := false
	for rawName, spec := range cfg.Cases {
		name := strings.TrimSpace(rawName)
		if !smsCaseNamePattern.MatchString(name) {
			return fmt.Errorf("case 名 %q 仅支持 1-64 位字母、数字、_、-、.", rawName)
		}
		if err := normalizeCase(&spec); err != nil {
			return fmt.Errorf("case %s: %w", name, err)
		}
		if err := adapter.ValidateCase(spec); err != nil {
			return fmt.Errorf("case %s 不符合 %s/%s: %w", name, adapter.Provider(), adapter.ProtocolVersion(), err)
		}
		hasReceipt = hasReceipt || spec.Receipt.Enabled
		normalizedCases[name] = spec
	}
	cfg.Cases = normalizedCases
	cfg.DefaultCase = strings.TrimSpace(cfg.DefaultCase)
	if _, ok := cfg.Cases[cfg.DefaultCase]; !ok {
		return fmt.Errorf("defaultCase %q 不存在", cfg.DefaultCase)
	}
	if hasReceipt {
		if cfg.CallbackURL == "" {
			return fmt.Errorf("存在启用 DLR 的 Case 时 callbackUrl 必填")
		}
		if err := validateURL(cfg.CallbackURL); err != nil {
			return err
		}
	}
	// 借用 HTTP Mock 的同一套强校验，保证规则优先级、AND 条件、概率 Case 与显式覆盖语义一致。
	fakeCases := make(map[string]httpmock.ResponseSpec, len(cfg.Cases))
	for name := range cfg.Cases {
		fakeCases[name] = httpmock.ResponseSpec{Status: 200, Body: name}
	}
	override := httpmock.OverrideNone
	if cfg.AllowCaseOverride {
		override = httpmock.OverrideCaseOnly
	}
	validated, err := httpmock.NormalizeEndpointConfig(httpmock.EndpointConfig{
		OverridePolicy:       override,
		DefaultResponse:      httpmock.ResponseSpec{Status: 200, Body: cfg.DefaultCase},
		DefaultWeightedCases: cfg.DefaultWeightedCases,
		Cases:                fakeCases,
		Rules:                cfg.Rules,
	})
	if err != nil {
		return fmt.Errorf("规则配置非法: %w", err)
	}
	cfg.DefaultWeightedCases = validated.DefaultWeightedCases
	cfg.Rules = validated.Rules
	return nil
}

func normalizeCase(spec *CaseSpec) error {
	s := &spec.Submit
	s.Action = strings.ToUpper(strings.TrimSpace(s.Action))
	if s.Action == "" {
		s.Action = SubmitActionRespond
	}
	if s.Action != SubmitActionRespond && s.Action != SubmitActionTimeout {
		return fmt.Errorf("submit.action 仅支持 RESPOND/TIMEOUT")
	}
	s.Result = strings.ToUpper(strings.TrimSpace(s.Result))
	if s.Result == "" {
		s.Result = SubmitResultAccepted
	}
	switch s.Result {
	case SubmitResultAccepted, SubmitResultRejected, SubmitResultMalformed, SubmitResultCustom:
	default:
		return fmt.Errorf("submit.result 仅支持 ACCEPTED/REJECTED/MALFORMED/CUSTOM")
	}
	if s.HTTPStatus == 0 {
		s.HTTPStatus = 200
	}
	if s.HTTPStatus < 200 || s.HTTPStatus > 599 {
		return fmt.Errorf("submit.httpStatus 必须在 200-599")
	}
	if s.Parts == 0 {
		s.Parts = 1
	}
	if s.Parts < 1 || s.Parts > 255 {
		return fmt.Errorf("submit.parts 必须在 1-255")
	}
	if s.DelayMs < 0 || s.DelayMs > maxWaitMs || s.TimeoutMs < 0 || s.TimeoutMs > maxWaitMs {
		return fmt.Errorf("submit delayMs/timeoutMs 必须在 0-%d", maxWaitMs)
	}
	if s.Action == SubmitActionTimeout && s.TimeoutMs == 0 {
		s.TimeoutMs = 31_000
	}
	if len(s.RawBodyTemplate) > maxTemplateBytes {
		return fmt.Errorf("submit.rawBodyTemplate 不能超过 %d bytes", maxTemplateBytes)
	}
	if s.Result == SubmitResultCustom && strings.TrimSpace(s.RawBodyTemplate) == "" {
		return fmt.Errorf("CUSTOM 必须提供 rawBodyTemplate")
	}
	r := &spec.Receipt
	if !r.Enabled {
		r.Repeat = 0
		return nil
	}
	if s.Action == SubmitActionTimeout || s.Result == SubmitResultRejected || s.Result == SubmitResultMalformed {
		return fmt.Errorf("超时/提交拒绝/畸形响应不能自动发送 DLR")
	}
	if r.DelayMs < 0 || r.DelayMs > maxReceiptDelay {
		return fmt.Errorf("receipt.delayMs 必须在 0-%d", maxReceiptDelay)
	}
	if r.StatusCode == "" {
		r.StatusCode = "2"
	}
	if r.Operator == "" {
		r.Operator = "MOCK"
	}
	if r.Repeat == 0 {
		r.Repeat = 1
	}
	if r.Repeat < 1 || r.Repeat > maxRepeatCount {
		return fmt.Errorf("receipt.repeat 必须在 1-%d", maxRepeatCount)
	}
	if r.RepeatIntervalMs < 0 || r.RepeatIntervalMs > maxWaitMs {
		return fmt.Errorf("receipt.repeatIntervalMs 必须在 0-%d", maxWaitMs)
	}
	if r.Repeat > 1 && r.RepeatIntervalMs == 0 {
		r.RepeatIntervalMs = 100
	}
	if len(r.RawBodyTemplate) > maxTemplateBytes {
		return fmt.Errorf("receipt.rawBodyTemplate 不能超过 %d bytes", maxTemplateBytes)
	}
	return nil
}

func sortedCaseNames(cases map[string]CaseSpec) []string {
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
