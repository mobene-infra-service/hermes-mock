package sipagent

import "testing"

// linearToMulaw 对照 G.711 已知向量：0→0xFF（μ-law 静音），满幅正/负有正确符号位。
func TestLinearToMulaw(t *testing.T) {
	if got := linearToMulaw(0); got != 0xFF {
		t.Errorf("0 应编码为 0xFF(静音), got 0x%02X", got)
	}
	// 正样本符号位(0x80)应为 0（^ 后），负样本符号位应为 1
	posMax := linearToMulaw(32767)
	negMax := linearToMulaw(-32768)
	if posMax == negMax {
		t.Errorf("正负满幅应不同: pos=0x%02X neg=0x%02X", posMax, negMax)
	}
	// μ-law(G.711): 最高位为符号位，互补后正样本=1、负样本=0
	if posMax&0x80 == 0 {
		t.Errorf("正样本最高位应为1(^后), got 0x%02X", posMax)
	}
	if negMax&0x80 != 0 {
		t.Errorf("负样本最高位应为0(^后), got 0x%02X", negMax)
	}
}

// sliceFrames 切 160 字节/帧，末帧静音补齐。
func TestSliceFrames(t *testing.T) {
	// 正好 2 帧
	if f := sliceFrames(make([]byte, 320)); len(f) != 2 || len(f[0]) != 160 {
		t.Errorf("320 字节应切 2 帧×160, got %d 帧", len(f))
	}
	// 1.5 帧 → 2 帧（末帧补齐到 160）
	f := sliceFrames(make([]byte, 240))
	if len(f) != 2 || len(f[1]) != 160 {
		t.Errorf("240 字节应切 2 帧(末帧补齐), got %d 帧 末帧 %d 字节", len(f), len(f[len(f)-1]))
	}
	if len(sliceFrames(nil)) != 0 {
		t.Error("空输入应返回 0 帧")
	}
}

// dialToneFrames 合成 1 秒拨号音 = 50 帧（1000ms/20ms），每帧 160 字节，且非全静音。
func TestDialToneFrames(t *testing.T) {
	f := dialToneFrames()
	if len(f) != 50 {
		t.Errorf("1 秒应为 50 帧, got %d", len(f))
	}
	if len(f) > 0 && len(f[0]) != 160 {
		t.Errorf("每帧应 160 字节, got %d", len(f[0]))
	}
	// 至少有一帧不是全 0xFF（静音），证明确实合成了可听音
	nonSilent := false
	for _, fr := range f {
		for _, b := range fr {
			if b != 0xFF {
				nonSilent = true
				break
			}
		}
		if nonSilent {
			break
		}
	}
	if !nonSilent {
		t.Error("合成拨号音不应全静音")
	}
}

// audioSource 描述放音来源。
func TestAudioSource(t *testing.T) {
	if got := audioSource("a.wav", "def.wav"); got != "a.wav" {
		t.Errorf("显式文件优先, got %q", got)
	}
	if got := audioSource("", "def.wav"); got != "def.wav" {
		t.Errorf("回退默认, got %q", got)
	}
	if got := audioSource("", ""); got != "合成拨号音" {
		t.Errorf("都空回退合成音, got %q", got)
	}
}
