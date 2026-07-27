// Package httpmock 提供通用、可编程的 HTTP Mock：
// /mock/{token} 是数据面，Endpoint 配置/规则常驻内存；调用记录异步落 hermes_mock 库。
package httpmock

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	ActionRespond = "RESPOND"
	ActionTimeout = "TIMEOUT"

	OverrideNone     = "NONE"
	OverrideCaseOnly = "CASE_ONLY"
	OverrideFull     = "FULL"

	SelectionDefault         = "DEFAULT"
	SelectionRuleFixed       = "RULE_FIXED"
	SelectionDefaultWeighted = "DEFAULT_WEIGHTED"
	SelectionRuleWeighted    = "RULE_WEIGHTED"
	SelectionExplicitCase    = "EXPLICIT_CASE"

	maxWaitMs          = 30_000
	maxBodyBytes       = 512 * 1024
	maxEndpointName    = 128
	maxWeightedCases   = 100
	maxWeightPerChoice = 1_000_000
)

var caseNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

// ResponseSpec 一次 HTTP 响应的完整定义。Body 始终按原文写出，不做 JSON 二次序列化。
type ResponseSpec struct {
	Action      string            `json:"action,omitempty"`
	Status      int               `json:"status,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	DelayMs     int               `json:"delayMs,omitempty"`
	TimeoutMs   int               `json:"timeoutMs,omitempty"`
}

// Condition 从请求的 method/query/header/jsonBody/rawBody 中提取值并比较。
type Condition struct {
	Source   string `json:"source"`
	Field    string `json:"field,omitempty"`
	Operator string `json:"operator"`
	Value    any    `json:"value,omitempty"`
}

// WeightedCase 引用一个命名 Case，Weight 为相对权重（如 8:2 = 80%:20%）。
type WeightedCase struct {
	Case   string `json:"case"`
	Weight int    `json:"weight"`
}

// Rule 同一条内 conditions 为 AND；规则按 priority 从高到低，第一条命中即停止。
// Case 与 WeightedCases 二选一：固定命中某 Case，或在多个 Case 间按权重选择。
type Rule struct {
	Name          string         `json:"name"`
	Priority      int            `json:"priority,omitempty"`
	Conditions    []Condition    `json:"conditions"`
	Case          string         `json:"case,omitempty"`
	WeightedCases []WeightedCase `json:"weightedCases,omitempty"`
}

// EndpointConfig 一条 Endpoint 的运行配置。
type EndpointConfig struct {
	AllowedMethods       []string                `json:"allowedMethods,omitempty"`
	OverridePolicy       string                  `json:"overridePolicy,omitempty"`
	DefaultResponse      ResponseSpec            `json:"defaultResponse"`
	DefaultWeightedCases []WeightedCase          `json:"defaultWeightedCases,omitempty"`
	Cases                map[string]ResponseSpec `json:"cases,omitempty"`
	Rules                []Rule                  `json:"rules,omitempty"`
}

// NormalizeEndpointConfig 返回经过与 Endpoint 保存时相同强校验的配置副本。
// 短信 Mock 等协议适配器可借此复用通用 HTTP Mock 的规则/概率选择语义，
// 而不必依赖 Endpoint 持久化模型。
func NormalizeEndpointConfig(config EndpointConfig) (EndpointConfig, error) {
	if err := config.normalizeAndValidate(); err != nil {
		return EndpointConfig{}, err
	}
	return config, nil
}

// Endpoint API 层使用的强类型配置视图。
type Endpoint struct {
	ID          int64          `json:"id"`
	Token       string         `json:"token"`
	Name        string         `json:"name"`
	Enabled     bool           `json:"enabled"`
	Config      EndpointConfig `json:"config"`
	Remark      string         `json:"remark,omitempty"`
	GmtCreate   time.Time      `json:"gmtCreate,omitempty"`
	GmtModified time.Time      `json:"gmtModified,omitempty"`
	InvokePath  string         `json:"invokePath,omitempty"`
	InvokeURL   string         `json:"invokeUrl,omitempty"`
}

// IncomingRequest 是规则引擎读取的请求快照。
type IncomingRequest struct {
	Method   string
	Query    map[string][]string
	Header   http.Header
	RawBody  []byte
	JSONBody any
}

// Decision 是规则、case 与本次覆盖合并后的最终响应。
type Decision struct {
	Response       ResponseSpec   `json:"response"`
	MatchedRule    string         `json:"matchedRule,omitempty"`
	SelectedCase   string         `json:"selectedCase,omitempty"`
	SelectionMode  string         `json:"selectionMode,omitempty"`
	SelectedWeight int            `json:"selectedWeight,omitempty"`
	TotalWeight    int            `json:"totalWeight,omitempty"`
	Overrides      map[string]any `json:"overrides,omitempty"`
}

func (e *Endpoint) normalizeAndValidate() error {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		return fmt.Errorf("name 必填")
	}
	if len(e.Name) > maxEndpointName {
		return fmt.Errorf("name 最长 %d 字符", maxEndpointName)
	}
	if len(e.Remark) > 255 {
		return fmt.Errorf("remark 最长 255 字符")
	}
	if err := e.Config.normalizeAndValidate(); err != nil {
		return err
	}
	return nil
}

func (c *EndpointConfig) normalizeAndValidate() error {
	if c.OverridePolicy == "" {
		c.OverridePolicy = OverrideCaseOnly
	}
	c.OverridePolicy = strings.ToUpper(strings.TrimSpace(c.OverridePolicy))
	switch c.OverridePolicy {
	case OverrideNone, OverrideCaseOnly, OverrideFull:
	default:
		return fmt.Errorf("overridePolicy 仅支持 NONE/CASE_ONLY/FULL")
	}
	seenMethods := map[string]bool{}
	methods := make([]string, 0, len(c.AllowedMethods))
	for _, method := range c.AllowedMethods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" || seenMethods[method] {
			continue
		}
		if !validMethod(method) {
			return fmt.Errorf("不支持的 HTTP method: %s", method)
		}
		seenMethods[method] = true
		methods = append(methods, method)
	}
	sort.Strings(methods)
	c.AllowedMethods = methods
	if err := normalizeResponse(&c.DefaultResponse); err != nil {
		return fmt.Errorf("defaultResponse: %w", err)
	}
	if c.Cases == nil {
		c.Cases = map[string]ResponseSpec{}
	}
	normalizedCases := make(map[string]ResponseSpec, len(c.Cases))
	for rawName, response := range c.Cases {
		name := strings.TrimSpace(rawName)
		if !caseNamePattern.MatchString(name) {
			return fmt.Errorf("case 名 %q 仅支持 1-64 位字母、数字、_、-、.", rawName)
		}
		if err := normalizeResponse(&response); err != nil {
			return fmt.Errorf("case %s: %w", name, err)
		}
		normalizedCases[name] = response
	}
	c.Cases = normalizedCases
	var err error
	c.DefaultWeightedCases, err = normalizeWeightedCases("defaultWeightedCases", c.DefaultWeightedCases, c.Cases)
	if err != nil {
		return err
	}
	for i := range c.Rules {
		rule := &c.Rules[i]
		rule.Name = strings.TrimSpace(rule.Name)
		if rule.Name == "" {
			rule.Name = fmt.Sprintf("rule-%d", i+1)
		}
		if len(rule.Name) > 128 {
			return fmt.Errorf("rule %d name 最长 128 字符", i+1)
		}
		rule.Case = strings.TrimSpace(rule.Case)
		rule.WeightedCases, err = normalizeWeightedCases(fmt.Sprintf("rule %s weightedCases", rule.Name), rule.WeightedCases, c.Cases)
		if err != nil {
			return err
		}
		hasFixedCase := rule.Case != ""
		hasWeightedCases := len(rule.WeightedCases) > 0
		if hasFixedCase == hasWeightedCases {
			return fmt.Errorf("rule %s 必须且只能配置 case 或 weightedCases 之一", rule.Name)
		}
		if hasFixedCase {
			if _, ok := c.Cases[rule.Case]; !ok {
				return fmt.Errorf("rule %s 引用了不存在的 case %q", rule.Name, rule.Case)
			}
		}
		if len(rule.Conditions) == 0 {
			return fmt.Errorf("rule %s 至少需要一个 condition", rule.Name)
		}
		for j := range rule.Conditions {
			if err := normalizeCondition(&rule.Conditions[j]); err != nil {
				return fmt.Errorf("rule %s condition %d: %w", rule.Name, j+1, err)
			}
		}
	}
	return nil
}

func normalizeWeightedCases(label string, choices []WeightedCase, cases map[string]ResponseSpec) ([]WeightedCase, error) {
	if len(choices) == 0 {
		return nil, nil
	}
	if len(choices) > maxWeightedCases {
		return nil, fmt.Errorf("%s 最多支持 %d 个结果", label, maxWeightedCases)
	}
	seen := make(map[string]bool, len(choices))
	out := make([]WeightedCase, 0, len(choices))
	for i, choice := range choices {
		choice.Case = strings.TrimSpace(choice.Case)
		if choice.Case == "" {
			return nil, fmt.Errorf("%s 第 %d 项 case 必填", label, i+1)
		}
		if _, ok := cases[choice.Case]; !ok {
			return nil, fmt.Errorf("%s 引用了不存在的 case %q", label, choice.Case)
		}
		if seen[choice.Case] {
			return nil, fmt.Errorf("%s 中 case %q 重复", label, choice.Case)
		}
		if choice.Weight <= 0 || choice.Weight > maxWeightPerChoice {
			return nil, fmt.Errorf("%s 中 case %q 的 weight 必须在 1-%d", label, choice.Case, maxWeightPerChoice)
		}
		seen[choice.Case] = true
		out = append(out, choice)
	}
	return out, nil
}

func normalizeResponse(r *ResponseSpec) error {
	r.Action = strings.ToUpper(strings.TrimSpace(r.Action))
	if r.Action == "" {
		r.Action = ActionRespond
	}
	switch r.Action {
	case ActionRespond, ActionTimeout:
	default:
		return fmt.Errorf("action 仅支持 RESPOND/TIMEOUT")
	}
	if r.Status == 0 {
		r.Status = http.StatusOK
	}
	if r.Status < 200 || r.Status > 599 {
		return fmt.Errorf("status 必须在 200-599")
	}
	if r.ContentType == "" {
		r.ContentType = "text/plain; charset=utf-8"
	}
	if strings.ContainsAny(r.ContentType, "\r\n") {
		return fmt.Errorf("contentType 非法")
	}
	if r.DelayMs < 0 || r.DelayMs > maxWaitMs {
		return fmt.Errorf("delayMs 必须在 0-%d", maxWaitMs)
	}
	if r.TimeoutMs < 0 || r.TimeoutMs > maxWaitMs {
		return fmt.Errorf("timeoutMs 必须在 0-%d", maxWaitMs)
	}
	if r.Action == ActionTimeout && r.TimeoutMs == 0 {
		r.TimeoutMs = 5000
	}
	if len(r.Body) > maxBodyBytes {
		return fmt.Errorf("body 不能超过 %d bytes", maxBodyBytes)
	}
	for key, value := range r.Headers {
		if err := validateResponseHeader(key, value); err != nil {
			return err
		}
	}
	return nil
}

func normalizeCondition(c *Condition) error {
	c.Source = strings.TrimSpace(c.Source)
	c.Operator = strings.ToUpper(strings.TrimSpace(c.Operator))
	switch c.Source {
	case "method", "query", "header", "jsonBody", "rawBody":
	default:
		return fmt.Errorf("source 仅支持 method/query/header/jsonBody/rawBody")
	}
	if c.Source != "method" && c.Source != "rawBody" && strings.TrimSpace(c.Field) == "" {
		return fmt.Errorf("field 必填")
	}
	switch c.Operator {
	case "EQ", "NE", "IN", "CONTAINS", "PREFIX", "EXISTS":
	default:
		return fmt.Errorf("operator 仅支持 EQ/NE/IN/CONTAINS/PREFIX/EXISTS")
	}
	return nil
}

func validMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

var blockedResponseHeaders = map[string]bool{
	"connection":          true,
	"content-length":      true,
	"date":                true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

func validateResponseHeader(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "\r\n:") {
		return fmt.Errorf("响应 header 名非法: %q", key)
	}
	if blockedResponseHeaders[strings.ToLower(key)] {
		return fmt.Errorf("不允许设置传输层响应 header: %s", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("响应 header %s 值非法", key)
	}
	return nil
}

func (c EndpointConfig) methodAllowed(method string) bool {
	if len(c.AllowedMethods) == 0 {
		return true
	}
	method = strings.ToUpper(method)
	for _, allowed := range c.AllowedMethods {
		if allowed == method {
			return true
		}
	}
	return false
}

// MethodAllowed 判断 Endpoint 是否接受该 HTTP method；空 allowedMethods 表示全部允许。
func (c EndpointConfig) MethodAllowed(method string) bool { return c.methodAllowed(method) }
