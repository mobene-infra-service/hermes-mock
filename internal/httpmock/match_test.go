package httpmock

import (
	"net/http"
	"testing"
)

func testConfig(t *testing.T) EndpointConfig {
	t.Helper()
	cfg := EndpointConfig{
		AllowedMethods: []string{"POST"}, OverridePolicy: OverrideFull,
		DefaultResponse: ResponseSpec{Status: 200, Body: "true"},
		Cases: map[string]ResponseSpec{
			"deny":    {Status: 200, Body: "false"},
			"timeout": {Action: ActionTimeout, TimeoutMs: 2500},
		},
		Rules: []Rule{{
			Name: "deny-number", Priority: 10, Case: "deny",
			Conditions: []Condition{{Source: "jsonBody", Field: "number", Operator: "IN", Value: []any{"8613800000001"}}},
		}},
	}
	if err := cfg.normalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestResolveRuleCaseAndFullOverride(t *testing.T) {
	cfg := testConfig(t)
	req := IncomingRequest{
		Method: "POST", Header: http.Header{}, Query: map[string][]string{},
		JSONBody: map[string]any{"number": "8613800000001"},
	}
	decision, err := Resolve(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Response.Body != "false" || decision.MatchedRule != "deny-number" || decision.SelectedCase != "deny" {
		t.Fatalf("规则响应错误: %+v", decision)
	}

	// 显式 case 覆盖规则。
	req.Query["__mock_case"] = []string{"timeout"}
	decision, err = Resolve(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Response.Action != ActionTimeout || decision.Response.TimeoutMs != 2500 || decision.SelectedCase != "timeout" {
		t.Fatalf("case 覆盖错误: %+v", decision)
	}

	// FULL 标量覆盖最后生效。
	req.Query["__mock_status"] = []string{"503"}
	req.Query["__mock_delay_ms"] = []string{"1200"}
	req.Query["__mock_body"] = []string{"busy"}
	decision, err = Resolve(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Response.Status != 503 || decision.Response.DelayMs != 1200 || decision.Response.Body != "busy" {
		t.Fatalf("FULL 覆盖错误: %+v", decision)
	}
}

func TestResolveConditionSourcesAndOperators(t *testing.T) {
	req := IncomingRequest{
		Method:   "POST",
		Query:    map[string][]string{"scene": {"slow"}},
		Header:   http.Header{"X-Tenant": []string{"org001"}},
		RawBody:  []byte(`{"user":{"id":"u1"},"tags":["a","b"]}`),
		JSONBody: map[string]any{"user": map[string]any{"id": "u1"}, "tags": []any{"a", "b"}},
	}
	conditions := []Condition{
		{Source: "method", Operator: "EQ", Value: "POST"},
		{Source: "query", Field: "scene", Operator: "PREFIX", Value: "sl"},
		{Source: "header", Field: "X-Tenant", Operator: "EQ", Value: "org001"},
		{Source: "jsonBody", Field: "user.id", Operator: "IN", Value: []any{"u1", "u2"}},
		{Source: "jsonBody", Field: "tags", Operator: "CONTAINS", Value: "b"},
		{Source: "rawBody", Operator: "CONTAINS", Value: `"user"`},
	}
	for _, condition := range conditions {
		actual, exists := conditionValue(condition, req)
		if !compareCondition(actual, exists, condition.Operator, condition.Value) {
			t.Fatalf("条件未命中: %+v actual=%v exists=%v", condition, actual, exists)
		}
	}
}

func TestResolveWeightedCasesForDefaultAndRule(t *testing.T) {
	cfg := EndpointConfig{
		DefaultResponse: ResponseSpec{Status: 200, Body: "fallback"},
		Cases: map[string]ResponseSpec{
			"allow":   {Status: 200, Body: "true"},
			"deny":    {Status: 200, Body: "false"},
			"timeout": {Action: ActionTimeout, TimeoutMs: 3000},
		},
		DefaultWeightedCases: []WeightedCase{{Case: "allow", Weight: 3}, {Case: "deny", Weight: 1}},
		Rules: []Rule{{
			Name: "vip-random", Priority: 10,
			Conditions:    []Condition{{Source: "header", Field: "X-Tier", Operator: "EQ", Value: "vip"}},
			WeightedCases: []WeightedCase{{Case: "deny", Weight: 2}, {Case: "timeout", Weight: 1}},
		}},
	}
	if err := cfg.normalizeAndValidate(); err != nil {
		t.Fatal(err)
	}

	baseReq := IncomingRequest{Method: "POST", Query: map[string][]string{}, Header: http.Header{}}
	decision, err := resolveWithPicker(cfg, baseReq, func(total int) int { return 2 })
	if err != nil {
		t.Fatal(err)
	}
	if decision.SelectedCase != "allow" || decision.SelectionMode != SelectionDefaultWeighted || decision.SelectedWeight != 3 || decision.TotalWeight != 4 {
		t.Fatalf("默认概率选择错误: %+v", decision)
	}
	decision, err = resolveWithPicker(cfg, baseReq, func(total int) int { return 3 })
	if err != nil {
		t.Fatal(err)
	}
	if decision.SelectedCase != "deny" || decision.Response.Body != "false" {
		t.Fatalf("默认概率边界错误: %+v", decision)
	}

	vipReq := baseReq
	vipReq.Header = http.Header{"X-Tier": []string{"vip"}}
	decision, err = resolveWithPicker(cfg, vipReq, func(total int) int { return 2 })
	if err != nil {
		t.Fatal(err)
	}
	if decision.MatchedRule != "vip-random" || decision.SelectedCase != "timeout" || decision.SelectionMode != SelectionRuleWeighted || decision.TotalWeight != 3 {
		t.Fatalf("规则概率选择错误: %+v", decision)
	}

	vipReq.Query["__mock_case"] = []string{"allow"}
	decision, err = resolveWithPicker(cfg, vipReq, func(total int) int { return 2 })
	if err != nil {
		t.Fatal(err)
	}
	if decision.SelectedCase != "allow" || decision.SelectionMode != SelectionExplicitCase || decision.TotalWeight != 0 {
		t.Fatalf("显式 case 未覆盖概率结果: %+v", decision)
	}
}

func TestEndpointValidationRejectsInvalidWeightedCases(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
	}{
		{name: "fixed and weighted", rule: Rule{Name: "bad", Case: "allow", WeightedCases: []WeightedCase{{Case: "deny", Weight: 1}}}},
		{name: "missing target", rule: Rule{Name: "bad"}},
		{name: "zero weight", rule: Rule{Name: "bad", WeightedCases: []WeightedCase{{Case: "allow", Weight: 0}}}},
		{name: "duplicate case", rule: Rule{Name: "bad", WeightedCases: []WeightedCase{{Case: "allow", Weight: 1}, {Case: "allow", Weight: 2}}}},
		{name: "unknown case", rule: Rule{Name: "bad", WeightedCases: []WeightedCase{{Case: "missing", Weight: 1}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := EndpointConfig{
				DefaultResponse: ResponseSpec{Status: 200},
				Cases:           map[string]ResponseSpec{"allow": {Status: 200}, "deny": {Status: 200}},
				Rules: []Rule{{
					Name: tt.rule.Name, Case: tt.rule.Case, WeightedCases: tt.rule.WeightedCases,
					Conditions: []Condition{{Source: "method", Operator: "EQ", Value: "POST"}},
				}},
			}
			if err := cfg.normalizeAndValidate(); err == nil {
				t.Fatal("应拒绝非法概率配置")
			}
		})
	}
}

func TestEndpointValidationRejectsUnsafeHeader(t *testing.T) {
	endpoint := Endpoint{
		Name: "bad", Enabled: true,
		Config: EndpointConfig{DefaultResponse: ResponseSpec{
			Status: 200, Headers: map[string]string{"Content-Length": "1"}, Body: "x",
		}},
	}
	if err := endpoint.normalizeAndValidate(); err == nil {
		t.Fatal("应拒绝 Content-Length")
	}
}
