package config

import "testing"

// TestAllowedSourceMatcher 表驱动校验 SIP 来源白名单解析与命中逻辑。
func TestAllowedSourceMatcher(t *testing.T) {
	cases := []struct {
		name       string
		spec       string
		restricted bool
		allow      map[string]bool // addr -> 期望是否放行
		wantErr    bool
	}{
		{
			name:       "empty 不限制全放行",
			spec:       "",
			restricted: false,
			allow:      map[string]bool{"1.2.3.4:5060": true, "8.8.8.8": true},
		},
		{
			name:       "CIDR 段命中",
			spec:       "172.16.0.0/12",
			restricted: true,
			allow: map[string]bool{
				"172.16.4.141:5060": true,
				"172.31.0.1":        true,
				"10.0.0.1:5060":     false,
				"8.8.8.8:5060":      false,
			},
		},
		{
			name:       "裸 IP + 多段混合",
			spec:       "127.0.0.1, 10.0.0.0/8 ,192.168.107.30",
			restricted: true,
			allow: map[string]bool{
				"127.0.0.1:5060":    true,
				"10.5.6.7:15060":    true,
				"192.168.107.30":    true,
				"192.168.107.31":    false,
				"172.16.4.141:5060": false,
				"notanip":           false,
			},
		},
		{name: "非法 CIDR 报错", spec: "172.16.0.0/99", wantErr: true},
		{name: "非法 IP 报错", spec: "999.1.1.1", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{SIPAllowedSources: tc.spec}
			m, restricted, err := c.AllowedSourceMatcher()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，得到 nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("非预期报错: %v", err)
			}
			if restricted != tc.restricted {
				t.Fatalf("restricted=%v，期望 %v", restricted, tc.restricted)
			}
			for addr, want := range tc.allow {
				if got := m(addr); got != want {
					t.Errorf("matcher(%q)=%v，期望 %v", addr, got, want)
				}
			}
		})
	}
}
