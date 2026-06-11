package orgcfg

import "testing"

// 机构配置 CRUD + 当前机构选择（纯内存）。
func TestStoreCRUDAndCurrent(t *testing.T) {
	s := NewMemory() // 单测内存座
	if len(s.List()) != 0 {
		t.Fatal("初始应空")
	}
	// 新增
	if _, err := s.Upsert(OrgConfig{
		OrgCode: "org001", OrgName: "A", Mode: "direct", BasicURL: "http://b",
		DefaultAgentGroupCode: "mock_skill", DefaultDepCode: "mock", DefaultAgentPassword: "Mock1234",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(OrgConfig{OrgCode: "org002", OrgName: "B", Mode: "gateway", GatewayURL: "http://gw", APIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 2 {
		t.Errorf("应有 2 个机构, got %d", len(s.List()))
	}
	// 首次 Upsert 自动设为 current
	if s.Current() != "org001" {
		t.Errorf("current 应为 org001, got %s", s.Current())
	}
	// 切换
	if err := s.SetCurrent("org002"); err != nil {
		t.Fatal(err)
	}
	if s.Current() != "org002" {
		t.Error("切换 current 失败")
	}
	// 切到不存在的应报错
	if err := s.SetCurrent("nope"); err == nil {
		t.Error("切到未配置机构应报错")
	}
	// 当前凭据
	cred, ok := s.CurrentCred()
	if !ok || cred.Mode != "gateway" || cred.APIKey != "k" {
		t.Errorf("当前凭据错: %+v ok=%v", cred, ok)
	}
	// 编辑（同 code 更新，不新增）
	if _, err := s.Upsert(OrgConfig{OrgCode: "org001", OrgName: "A2", Mode: "direct", BasicURL: "http://b2"}); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 2 {
		t.Errorf("编辑不应新增, got %d", len(s.List()))
	}
	if c, _ := s.Get("org001"); c.OrgName != "A2" {
		t.Errorf("编辑未生效: %+v", c)
	}
	if _, err := s.Upsert(OrgConfig{
		OrgCode: "org001", OrgName: "A2", Mode: "direct", BasicURL: "http://b2",
		DefaultAgentGroupCode: "mock_skill", DefaultAgentRoleCode: "mock_agent", DefaultDepCode: "mock", DefaultAgentPassword: "Mock1234",
	}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Get("org001"); c.DefaultAgentGroupCode != "mock_skill" || c.DefaultDepCode != "mock" || c.DefaultAgentRoleCode != "mock_agent" {
		t.Errorf("默认坐席参数未保存: %+v", c)
	}
	// 删除当前机构应回退 current
	if err := s.Delete("org002"); err != nil {
		t.Fatal(err)
	}
	if s.Current() != "org001" {
		t.Errorf("删除当前机构后 current 应回退到 org001, got %s", s.Current())
	}
	if len(s.List()) != 1 {
		t.Errorf("删除后应剩 1, got %d", len(s.List()))
	}
}

// Cred() 正确映射到 OpenAPI 凭据。
func TestOrgConfigToCred(t *testing.T) {
	o := OrgConfig{OrgCode: "o1", OrgName: "n", Mode: "direct", BasicURL: "http://b", CallCenterURL: "http://c", CallBotURL: "http://cb", OTPURL: "http://otp", AgentWsURL: "ws:8081", UserCode: "u"}
	cred := credOf(o)
	if cred.OrgCode != "o1" || cred.BasicURL != "http://b" || cred.CallCenterURL != "http://c" || cred.CallBotURL != "http://cb" || cred.OTPURL != "http://otp" || cred.UserCode != "u" {
		t.Errorf("Cred 映射错: %+v", cred)
	}
}

func TestAgentWSHost(t *testing.T) {
	cases := []struct {
		name string
		org  OrgConfig
		want string
	}{
		{name: "explicit", org: OrgConfig{AgentWsURL: "127.0.0.1:18081", CallCenterURL: "http://127.0.0.1:8091"}, want: "127.0.0.1:18081"},
		{name: "localhost", org: OrgConfig{CallCenterURL: "http://127.0.0.1:8091"}, want: "127.0.0.1:18081"},
		{name: "docker-call-center", org: OrgConfig{CallCenterURL: "http://hermes-call-center:8080"}, want: "hermes-ws:8081"},
		{name: "docker-basic", org: OrgConfig{BasicURL: "http://hermes-basic:8080"}, want: "hermes-ws:8081"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.org.AgentWSHost(); got != tt.want {
				t.Fatalf("AgentWSHost()=%q, want %q", got, tt.want)
			}
		})
	}
}
