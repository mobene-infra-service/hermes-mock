// Package agents 维护 mock 模拟坐席的状态：工作台 WS 在线态、SIP 注册态、本地工作状态。
// 坐席要能被 call-center 呼叫/分配，需「双通道在线」：WS 在线（连 hermes-ws）+ SIP 注册（注册到 FS），
// 两者都满足 call-center 才认为 agentIsReady、才允许切 ONLINE、预测式拨号才把它当空闲坐席外呼客户。
package agents

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// RegState SIP 注册状态（坐席软电话注册到 FreeSWITCH）。
type RegState string

const (
	RegUnregistered RegState = "UNREGISTERED"
	RegRegistering  RegState = "REGISTERING"
	RegRegistered   RegState = "REGISTERED"
	RegFailed       RegState = "FAILED"
)

// WorkStatus 坐席工作状态（对齐 hermes AgentStatusEnum）。
type WorkStatus string

const (
	StatusOffline WorkStatus = "OFFLINE"
	StatusOnline  WorkStatus = "ONLINE"        // 在线/空闲（预测式拨号可分配）
	StatusRinging WorkStatus = "RINGING"       // 响铃中
	StatusCalling WorkStatus = "CALLING"       // 通话中
	StatusDialing WorkStatus = "DIALING"       // 呼叫中
	StatusWrapUp  WorkStatus = "WRAP_UP"       // 话后处理(ACW/整理中)
	StatusResting WorkStatus = "RESTING"       // 小休
	StatusBusy    WorkStatus = "BUSY"          // 忙碌
	StatusAutoOut WorkStatus = "AUTO_OUTBOUND" // 自动外呼
)

// WsState 坐席工作台 WebSocket 连接态（独立于 SIP 注册：WS 是工作台信令，SIP 是话路）。
type WsState string

const (
	WsOffline    WsState = "OFFLINE"    // 未连
	WsConnecting WsState = "CONNECTING" // 连接/登录中
	WsOnline     WsState = "ONLINE"     // 已登录在线（hermes-ws 已回 auth）
	WsFailed     WsState = "FAILED"     // 连接/登录失败
)

// Agent 一个坐席的状态快照。
type Agent struct {
	Number   string     `json:"number"`
	RegState RegState   `json:"regState"` // SIP 注册态
	Status   WorkStatus `json:"status"`   // 工作状态
	LastErr  string     `json:"lastErr,omitempty"`
	WsState  WsState    `json:"wsState"` // 工作台 WS 连接态
	WsErr    string     `json:"wsErr,omitempty"`
}

// Ready 坐席是否「双通道在线」（WS 在线 + SIP 已注册）——对应 call-center 的 agentIsReady。
func (a *Agent) Ready() bool { return a.WsState == WsOnline && a.RegState == RegRegistered }

// Registry 坐席状态的并发安全存储。
type Registry struct {
	mu     sync.RWMutex
	agents map[string]*Agent
	order  []string // 稳定展示顺序
}

// New 创建空注册表。
func New() *Registry {
	return &Registry{agents: map[string]*Agent{}}
}

// Ensure 确保某坐席存在（默认全离线），返回其指针。
func (r *Registry) Ensure(number string) *Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureLocked(number)
}

func (r *Registry) ensureLocked(number string) *Agent {
	a, ok := r.agents[number]
	if !ok {
		a = &Agent{Number: number, RegState: RegUnregistered, Status: StatusOffline, WsState: WsOffline}
		r.agents[number] = a
		r.order = append(r.order, number)
	}
	return a
}

// SetWs 更新坐席工作台 WS 连接态。
func (r *Registry) SetWs(number string, st WsState, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.ensureLocked(number)
	a.WsState = st
	a.WsErr = errMsg
	if st == WsOffline || st == WsFailed {
		if a.Status == StatusOnline {
			a.Status = StatusOffline
		}
	}
}

// SetReg 更新 SIP 注册态。
func (r *Registry) SetReg(number string, st RegState, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.ensureLocked(number)
	a.RegState = st
	a.LastErr = errMsg
	if st == RegUnregistered || st == RegFailed {
		if a.Status == StatusOnline {
			a.Status = StatusOffline
		}
	}
}

// SetStatus 设置工作状态（坐席工作台动作：上线/小休/示忙/话后等）。
func (r *Registry) SetStatus(number string, st WorkStatus) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[number]
	if !ok {
		return false
	}
	a.Status = st
	return true
}

// SetStatusBatch 批量设置一组坐席的工作状态。返回成功数。
func (r *Registry) SetStatusBatch(numbers []string, st WorkStatus) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, num := range numbers {
		if a, ok := r.agents[num]; ok {
			a.Status = st
			n++
		}
	}
	return n
}

// IsRegistered 判断坐席当前是否 SIP 已注册（用于 SIP 腿识别：被叫是坐席）。
func (r *Registry) IsRegistered(number string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[number]
	return ok && a.RegState == RegRegistered
}

// Get 取单个坐席快照。
func (r *Registry) Get(number string) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if a, ok := r.agents[number]; ok {
		return *a, true
	}
	return Agent{}, false
}

// List 返回所有坐席快照（稳定顺序）。
func (r *Registry) List() []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Agent, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, *r.agents[n])
	}
	return out
}

// ParseExtensions 解析分机表达式："1000-1004,1010" → ["1000".."1004","1010"]。
// 区间端非数字或左>右则跳过；非区间项原样保留；去重并稳定排序。
func ParseExtensions(expr string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, "-"); i > 0 {
			lo, e1 := strconv.Atoi(strings.TrimSpace(part[:i]))
			hi, e2 := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if e1 == nil && e2 == nil && lo <= hi {
				for n := lo; n <= hi; n++ {
					add(strconv.Itoa(n))
				}
			}
			continue
		}
		add(part)
	}
	sort.Slice(out, func(i, j int) bool {
		a, ea := strconv.Atoi(out[i])
		b, eb := strconv.Atoi(out[j])
		if ea == nil && eb == nil {
			return a < b
		}
		return out[i] < out[j]
	})
	return out
}
