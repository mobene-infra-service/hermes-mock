package orchestrator

import (
	"testing"

	"hermes-mock/internal/orgcfg"
)

// 无机构配置时：调用应明确报「未配置 OpenAPI 凭据」（不静默、不直连）。
func newEmpty() *Orchestrator {
	s := orgcfg.NewMemory() // 单测内存座、无机构
	return New(s)
}

// 未配置机构时，各任务方法都应报错（证明走 OpenAPI 凭据而非裸 URL）。
func TestTasksRequireOrgCred(t *testing.T) {
	o := newEmpty()
	if _, err := o.RunOTP(OTPScenario{To: "8610000", TemplateCode: "T1"}); err == nil {
		t.Error("未配机构 OTP 应报错")
	}
	if _, err := o.RunCallBot(CallBotScenario{Name: "t", Numbers: []string{"8610000"}}); err == nil {
		t.Error("未配机构 call-bot 应报错")
	}
	if _, err := o.RunCallCenterTask(CallCenterTaskScenario{Name: "t", Numbers: []string{"8610000"}}); err == nil {
		t.Error("未配机构 群呼 应报错")
	}
}

// extractTaskCode 兼容 data 为字符串 / 对象.code / 对象.taskCode（hermesopenapi 返回 data 原文）。
func TestExtractTaskCode(t *testing.T) {
	cases := map[string]string{
		`"TASK123"`:          "TASK123", // data 原文是字符串
		`{"code":"C9"}`:      "C9",      // data 原文是对象
		`{"taskCode":"TC7"}`: "TC7",
		`{"data":"TASK123"}`: "TASK123", // 兼容整包
		`{"other":"x"}`:      "",
		`not json`:           "",
	}
	for raw, want := range cases {
		if got := extractTaskCode([]byte(raw)); got != want {
			t.Errorf("extractTaskCode(%s)=%q want %q", raw, got, want)
		}
	}
}
