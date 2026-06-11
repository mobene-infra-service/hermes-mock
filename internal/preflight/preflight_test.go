package preflight

import "testing"

// 全空输入：每类场景都应有 FAIL（关键地址缺失），Ready=false。
func TestAllEmptyNotReady(t *testing.T) {
	in := Inputs{}
	for _, rep := range []Report{CallCenterTask(in), AutoCall(in), OTP(in)} {
		if rep.Ready {
			t.Errorf("%s 全空应不就绪", rep.Scenario)
		}
		hasFail := false
		for _, c := range rep.Checks {
			if c.Status == Fail {
				hasFail = true
			}
		}
		if !hasFail {
			t.Errorf("%s 全空应含 FAIL 项", rep.Scenario)
		}
	}
}

// 群呼场景：配齐 call-center 地址 + 线路即就绪（坐席由真实 Hermes 承担，不卡 mock）。
func TestCallCenterReadyWithMinimal(t *testing.T) {
	in := Inputs{
		CallCenterBaseURL: "http://cc:8080",
		LineDBConnected:   true, LineCount: 2, LineBindings: 1,
	}
	rep := CallCenterTask(in)
	if !rep.Ready {
		t.Errorf("配齐地址+线路应就绪, checks=%+v", rep.Checks)
	}
}

// 自动外呼：配齐 call-bot 地址 + 线路即就绪。
func TestAutoCallReady(t *testing.T) {
	rep := AutoCall(Inputs{CallBotBaseURL: "http://cb:8080", LineDBConnected: true, LineCount: 1, LineBindings: 1})
	if !rep.Ready {
		t.Errorf("配齐应就绪, checks=%+v", rep.Checks)
	}
	// 缺地址
	if AutoCall(Inputs{LineCount: 1}).Ready {
		t.Error("缺 call-bot 地址应不就绪")
	}
}

// OTP：配齐 otp 地址 + 线路即就绪。
func TestOTPReady(t *testing.T) {
	rep := OTP(Inputs{OTPBaseURL: "http://otp:8080", LineDBConnected: true, LineCount: 1, LineBindings: 1})
	if !rep.Ready {
		t.Errorf("配齐应就绪, checks=%+v", rep.Checks)
	}
	if OTP(Inputs{LineCount: 1}).Ready {
		t.Error("缺 otp 地址应不就绪")
	}
}

// 线路未连库 + 无线路：line-call 有 originate 地址仍应就绪（线路只 WARN）。
