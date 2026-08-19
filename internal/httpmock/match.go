package httpmock

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
)

// Resolve 根据规则、命名 case 与本次请求覆盖生成最终响应。
func Resolve(cfg EndpointConfig, req IncomingRequest) (Decision, error) {
	return resolveWithPicker(cfg, req, rand.Intn)
}

func resolveWithPicker(cfg EndpointConfig, req IncomingRequest, pick func(int) int) (Decision, error) {
	return resolve(cfg, req, pick, "")
}

func resolveWithSequence(cfg EndpointConfig, req IncomingRequest, sequenceCase string) (Decision, error) {
	return resolve(cfg, req, rand.Intn, sequenceCase)
}

func resolve(cfg EndpointConfig, req IncomingRequest, pick func(int) int, sequenceCase string) (Decision, error) {
	decision := Decision{
		Response: cfg.DefaultResponse, SelectedCase: "default", SelectionMode: SelectionDefault,
	}
	if sequenceCase != "" {
		response, ok := cfg.Cases[sequenceCase]
		if !ok {
			return Decision{}, fmt.Errorf("sequenceCases 引用了不存在的 case %q", sequenceCase)
		}
		decision.Response = response
		decision.SelectedCase = sequenceCase
		decision.SelectionMode = SelectionSequence
	} else {
		rules := append([]Rule(nil), cfg.Rules...)
		sort.SliceStable(rules, func(i, j int) bool { return rules[i].Priority > rules[j].Priority })
		matched := false
		for _, rule := range rules {
			if matchesRule(rule, req) {
				matched = true
				decision.MatchedRule = rule.Name
				if len(rule.WeightedCases) > 0 {
					if err := applyWeightedSelection(&decision, cfg.Cases, rule.WeightedCases, SelectionRuleWeighted, pick); err != nil {
						return Decision{}, fmt.Errorf("rule %s: %w", rule.Name, err)
					}
				} else {
					response, ok := cfg.Cases[rule.Case]
					if !ok {
						return Decision{}, fmt.Errorf("rule %s 引用了不存在的 case %q", rule.Name, rule.Case)
					}
					decision.Response = response
					decision.SelectedCase = rule.Case
					decision.SelectionMode = SelectionRuleFixed
				}
				break
			}
		}
		if !matched && len(cfg.DefaultWeightedCases) > 0 {
			if err := applyWeightedSelection(&decision, cfg.Cases, cfg.DefaultWeightedCases, SelectionDefaultWeighted, pick); err != nil {
				return Decision{}, fmt.Errorf("defaultWeightedCases: %w", err)
			}
		}
	}

	// 显式 case 比参数匹配规则优先；NONE 策略完全忽略调用方控制参数。
	if cfg.OverridePolicy != OverrideNone {
		caseName := firstNonBlank(req.Query["__mock_case"], []string{req.Header.Get("X-Mock-Case")})
		if caseName != "" {
			response, ok := cfg.Cases[caseName]
			if !ok {
				return Decision{}, fmt.Errorf("未知 __mock_case %q", caseName)
			}
			decision.Response = response
			decision.SelectedCase = caseName
			decision.SelectionMode = SelectionExplicitCase
			decision.SelectedWeight = 0
			decision.TotalWeight = 0
			decision.Overrides = map[string]any{"case": caseName}
		}
	}

	if cfg.OverridePolicy == OverrideFull {
		if decision.Overrides == nil {
			decision.Overrides = map[string]any{}
		}
		if err := applyFullOverrides(&decision.Response, decision.Overrides, req); err != nil {
			return Decision{}, err
		}
		if len(decision.Overrides) == 0 {
			decision.Overrides = nil
		}
	}
	return decision, nil
}

func applyWeightedSelection(decision *Decision, cases map[string]ResponseSpec, choices []WeightedCase, mode string, pick func(int) int) error {
	if len(choices) == 0 {
		return fmt.Errorf("概率结果为空")
	}
	total := 0
	for _, choice := range choices {
		if choice.Weight <= 0 {
			return fmt.Errorf("case %q 的 weight 必须大于 0", choice.Case)
		}
		total += choice.Weight
	}
	if total <= 0 {
		return fmt.Errorf("总权重必须大于 0")
	}
	draw := pick(total)
	if draw < 0 || draw >= total {
		return fmt.Errorf("随机选择器返回越界值 %d，期望 0-%d", draw, total-1)
	}
	acc := 0
	for _, choice := range choices {
		acc += choice.Weight
		if draw >= acc {
			continue
		}
		response, ok := cases[choice.Case]
		if !ok {
			return fmt.Errorf("引用了不存在的 case %q", choice.Case)
		}
		decision.Response = response
		decision.SelectedCase = choice.Case
		decision.SelectionMode = mode
		decision.SelectedWeight = choice.Weight
		decision.TotalWeight = total
		return nil
	}
	return fmt.Errorf("概率结果选择失败")
}

func matchesRule(rule Rule, req IncomingRequest) bool {
	if len(rule.Conditions) == 0 {
		return false
	}
	for _, condition := range rule.Conditions {
		actual, exists := conditionValue(condition, req)
		if !compareCondition(actual, exists, condition.Operator, condition.Value) {
			return false
		}
	}
	return true
}

func conditionValue(c Condition, req IncomingRequest) (any, bool) {
	switch c.Source {
	case "method":
		return req.Method, req.Method != ""
	case "query":
		values, ok := req.Query[c.Field]
		return values, ok
	case "header":
		values, ok := req.Header[httpCanonicalHeader(c.Field)]
		if !ok {
			value := req.Header.Get(c.Field)
			return value, value != ""
		}
		return values, true
	case "rawBody":
		return string(req.RawBody), len(req.RawBody) > 0
	case "jsonBody":
		return lookupJSONPath(req.JSONBody, c.Field)
	default:
		return nil, false
	}
}

func compareCondition(actual any, exists bool, operator string, expected any) bool {
	if operator == "EXISTS" {
		return exists
	}
	if !exists {
		return operator == "NE"
	}
	actualValues := stringValues(actual)
	expectedValues := stringValues(expected)
	switch operator {
	case "EQ":
		return anyPair(actualValues, expectedValues, func(a, b string) bool { return a == b })
	case "NE":
		return !anyPair(actualValues, expectedValues, func(a, b string) bool { return a == b })
	case "IN":
		return anyPair(actualValues, expectedValues, func(a, b string) bool { return a == b })
	case "CONTAINS":
		return anyPair(actualValues, expectedValues, func(a, b string) bool { return strings.Contains(a, b) })
	case "PREFIX":
		return anyPair(actualValues, expectedValues, func(a, b string) bool { return strings.HasPrefix(a, b) })
	default:
		return false
	}
}

func anyPair(left, right []string, fn func(string, string) bool) bool {
	for _, a := range left {
		for _, b := range right {
			if fn(a, b) {
				return true
			}
		}
	}
	return false
}

func stringValues(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return []string{x}
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			out = append(out, scalarString(item))
		}
		return out
	default:
		return []string{scalarString(x)}
	}
}

func scalarString(v any) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func lookupJSONPath(root any, path string) (any, bool) {
	if root == nil {
		return nil, false
	}
	if strings.TrimSpace(path) == "" {
		return root, true
	}
	current := root
	for _, part := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[part]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			current = node[idx]
		default:
			return nil, false
		}
	}
	return current, true
}

func applyFullOverrides(response *ResponseSpec, out map[string]any, req IncomingRequest) error {
	value := func(queryKey, headerKey string) string {
		if values := req.Query[queryKey]; len(values) > 0 && strings.TrimSpace(values[0]) != "" {
			return values[0]
		}
		return req.Header.Get(headerKey)
	}
	if v := strings.ToUpper(strings.TrimSpace(value("__mock_action", "X-Mock-Action"))); v != "" {
		if v != ActionRespond && v != ActionTimeout {
			return fmt.Errorf("__mock_action 仅支持 RESPOND/TIMEOUT")
		}
		response.Action = v
		out["action"] = v
	}
	if v := strings.TrimSpace(value("__mock_status", "X-Mock-Status")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 200 || n > 599 {
			return fmt.Errorf("__mock_status 必须在 200-599")
		}
		response.Status = n
		out["status"] = n
	}
	if v := value("__mock_delay_ms", "X-Mock-Delay-Ms"); strings.TrimSpace(v) != "" {
		n, err := parseWaitOverride("__mock_delay_ms", v)
		if err != nil {
			return err
		}
		response.DelayMs = n
		out["delayMs"] = n
	}
	if v := value("__mock_timeout_ms", "X-Mock-Timeout-Ms"); strings.TrimSpace(v) != "" {
		n, err := parseWaitOverride("__mock_timeout_ms", v)
		if err != nil {
			return err
		}
		response.TimeoutMs = n
		out["timeoutMs"] = n
	}
	if v := value("__mock_body", "X-Mock-Body"); v != "" {
		if len(v) > maxBodyBytes {
			return fmt.Errorf("__mock_body 不能超过 %d bytes", maxBodyBytes)
		}
		response.Body = v
		out["body"] = v
	}
	if v := strings.TrimSpace(value("__mock_content_type", "X-Mock-Content-Type")); v != "" {
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("__mock_content_type 非法")
		}
		response.ContentType = v
		out["contentType"] = v
	}
	if response.Action == ActionTimeout && response.TimeoutMs == 0 {
		response.TimeoutMs = 5000
	}
	return nil
}

func parseWaitOverride(name, value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 || n > maxWaitMs {
		return 0, fmt.Errorf("%s 必须在 0-%d", name, maxWaitMs)
	}
	return n, nil
}

func firstNonBlank(groups ...[]string) string {
	for _, group := range groups {
		for _, value := range group {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func httpCanonicalHeader(key string) string {
	parts := strings.Split(strings.ToLower(key), "-")
	for i, part := range parts {
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "-")
}
