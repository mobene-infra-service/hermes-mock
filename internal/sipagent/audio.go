package sipagent

import (
	"encoding/binary"
	"math"
	"os"
)

// 音频帧工具：把预置 WAV 转成 20ms@8kHz PCMU(μ-law) 帧，或合成可听拨号音帧。
// 让 mock 应答后持续发真实音频（FS 录音/坐席/监听能听到声），媒体统计 tx>0。

const (
	frameSamples = 160 // 20ms @ 8kHz
)

// loadPCMUFrames 读 WAV，转成一组 20ms PCMU 帧（循环播放用）。
// 支持 8kHz：① μ-law(WAV fmt 7) 直接切帧；② 16-bit PCM mono（fmt 1）逐样本编码 μ-law。
// 解析失败/不支持返回 nil（调用方回退合成音）。
func loadPCMUFrames(path string) [][]byte {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil
	}
	// 解析 chunk，找 fmt 与 data
	var audioFmt uint16
	var numChan uint16
	var sampleRate uint32
	var bits uint16
	var data []byte
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		sz := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if body+sz > len(raw) {
			sz = len(raw) - body
		}
		switch id {
		case "fmt ":
			if sz >= 16 {
				audioFmt = binary.LittleEndian.Uint16(raw[body : body+2])
				numChan = binary.LittleEndian.Uint16(raw[body+2 : body+4])
				sampleRate = binary.LittleEndian.Uint32(raw[body+4 : body+8])
				bits = binary.LittleEndian.Uint16(raw[body+14 : body+16])
			}
		case "data":
			data = raw[body : body+sz]
		}
		pos = body + sz
		if sz%2 == 1 {
			pos++ // chunk 按偶数对齐
		}
	}
	if len(data) == 0 || sampleRate != 8000 || numChan != 1 {
		return nil // 仅支持 8kHz mono（电话标准），其余回退合成音
	}

	var pcmu []byte
	switch audioFmt {
	case 7: // μ-law，已是 PCMU
		pcmu = data
	case 1: // 16-bit PCM → μ-law
		if bits != 16 {
			return nil
		}
		pcmu = make([]byte, len(data)/2)
		for i := 0; i+1 < len(data); i += 2 {
			s := int16(binary.LittleEndian.Uint16(data[i : i+2]))
			pcmu[i/2] = linearToMulaw(s)
		}
	default:
		return nil
	}
	return sliceFrames(pcmu)
}

// sliceFrames 把 PCMU 字节流切成 160 字节/帧；末帧不足用静音(0xFF)补齐。
func sliceFrames(pcmu []byte) [][]byte {
	if len(pcmu) == 0 {
		return nil
	}
	var frames [][]byte
	for i := 0; i < len(pcmu); i += frameSamples {
		end := i + frameSamples
		if end > len(pcmu) {
			f := make([]byte, frameSamples)
			for j := range f {
				f[j] = 0xFF
			}
			copy(f, pcmu[i:])
			frames = append(frames, f)
		} else {
			frames = append(frames, pcmu[i:end])
		}
	}
	return frames
}

// dialToneFrames 合成 1 秒「拨号音」(350Hz+440Hz)，切成 20ms PCMU 帧循环播放。
// 用于无预置 WAV 时保证一定有可听声音（北美拨号音组合，电话里很自然）。
func dialToneFrames() [][]byte {
	const (
		rate    = 8000
		seconds = 1
		f1      = 350.0
		f2      = 440.0
		amp     = 0.28 // 适中音量，避免削顶
	)
	n := rate * seconds
	pcmu := make([]byte, n)
	for i := 0; i < n; i++ {
		t := float64(i) / rate
		v := amp * (math.Sin(2*math.Pi*f1*t) + math.Sin(2*math.Pi*f2*t)) / 2
		s := int16(v * 32767)
		pcmu[i] = linearToMulaw(s)
	}
	return sliceFrames(pcmu)
}

// linearToMulaw 16-bit 线性 PCM → 8-bit μ-law（G.711，ITU-T 标准实现）。
func linearToMulaw(sample int16) byte {
	const bias = 0x84
	const clip = 32635
	sign := byte(0)
	s := int(sample)
	if s < 0 {
		s = -s
		sign = 0x80
	}
	if s > clip {
		s = clip
	}
	s += bias
	exponent := byte(7)
	for mask := 0x4000; (s&mask) == 0 && exponent > 0; mask >>= 1 {
		exponent--
	}
	mantissa := byte((s >> (uint(exponent) + 3)) & 0x0F)
	return ^(sign | (exponent << 4) | mantissa)
}
