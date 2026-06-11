// Package cluster 实现 mock 的核心抽象：可批量编排的虚拟客户集群。
//
//	行为档 BehaviorProfile  ← 一组可复用的应答行为（接听/拒接/振铃/放音/DTMF/故障/接通率）
//	客户组 CustomerGroup    ← 号段批量 N 个客户，引用行为档，绑定线路
//	客户个例 CustomerOverride ← 组内个别号码的例外行为/状态
//	线路绑定 LineBinding    ← 客户组 ↔ Hermes 线路(t_line.address→mock)
//
// 解析一通呼叫的行为：被叫号 → 命中客户组(号段) → 若有个例覆盖用个例，否则用组行为档。
// 本包是「内存缓存 + 解析」领域服务：DB 读写经 model.Repository（写穿透、读走缓存——
// Resolve* 是 SIP 来话热路径，绝不查库）。实体定义在 internal/entity。
package cluster

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"time"

	"hermes-mock/internal/entity"
	"hermes-mock/internal/model"
)

// 实体别名：消费方（api/sipagent/bootstrap/testkit）继续用 cluster.Xxx 旧名，定义已下沉 entity。
type (
	BehaviorProfile  = entity.BehaviorProfile
	CustomerGroup    = entity.CustomerGroup
	CustomerOverride = entity.CustomerOverride
	LineBinding      = entity.LineBinding
	Resolved         = entity.Resolved
)

// normalizeLineName 规范化线路名以匹配 FS 注入的 X-Line-Name：
// 对照 Hermes Bridge.kt 的 `lineName.replace("-","").lowercase()`（去横杠 + 转小写）。
func normalizeLineName(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

// Store 客户集群配置的内存缓存 + 解析服务。写操作穿透到 repo 再更新缓存；
// 读（含 SIP 热路径 Resolve*）只走缓存。repo 为 nil 仅出现在单测（纯内存）。
type Store struct {
	mu   sync.RWMutex
	repo model.Repository

	profiles  map[string]*BehaviorProfile  // by code
	groups    map[string]*CustomerGroup    // by code
	overrides map[string]*CustomerOverride // by number
	bindings  map[string]*LineBinding      // by line_code（也按 address 索引）

	takeMu     sync.Mutex     // 保护 takeCursor
	takeCursor map[string]int // 每组取号游标（多次测试错开取号、避免撞号）
}

// New 基于 Repository 构建 Store 并全量加载缓存。
func New(repo model.Repository) (*Store, error) {
	if repo == nil {
		return nil, errors.New("repository 不可用（hermes_mock 库未初始化）")
	}
	s := NewMemory()
	s.repo = repo
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewMemory 纯内存 Store——**仅供单测**（无 Repository，写操作只进缓存）。
func NewMemory() *Store {
	return &Store{
		profiles:   map[string]*BehaviorProfile{},
		groups:     map[string]*CustomerGroup{},
		overrides:  map[string]*CustomerOverride{},
		bindings:   map[string]*LineBinding{},
		takeCursor: map[string]int{},
	}
}

func (s *Store) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// Reload 从 DB 全量重载配置到内存缓存。
func (s *Store) Reload() error {
	if s.repo == nil {
		return nil
	}
	ctx, cancel := s.ctx()
	defer cancel()
	profiles, err := s.repo.ListBehaviorProfiles(ctx)
	if err != nil {
		return err
	}
	groups, err := s.repo.ListCustomerGroups(ctx)
	if err != nil {
		return err
	}
	overrides, err := s.repo.ListCustomerOverrides(ctx)
	if err != nil {
		return err
	}
	bindings, err := s.repo.ListLineBindings(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles = indexBy(profiles, func(p *BehaviorProfile) string { return p.Code })
	s.groups = indexBy(groups, func(g *CustomerGroup) string { return g.Code })
	s.overrides = indexBy(overrides, func(o *CustomerOverride) string { return o.Number })
	s.bindings = indexBy(bindings, func(b *LineBinding) string { return b.LineCode })
	return nil
}

func indexBy[T any](items []T, key func(*T) string) map[string]*T {
	m := make(map[string]*T, len(items))
	for i := range items {
		it := items[i]
		m[key(&it)] = &it
	}
	return m
}

// ---- 解析（核心热路径）：一通呼叫 → 有效行为。只读缓存，不查库 ----

// ResolveByNumber 按被叫号直接命中客户组（号段），合并个例覆盖 → 有效行为。
func (s *Store) ResolveByNumber(number string) *Resolved {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// 1) 个例优先
	if ov, ok := s.overrides[number]; ok {
		r := &Resolved{Number: number, GroupCode: ov.GroupCode, Disabled: ov.State == "DISABLED"}
		code := ov.BehaviorCode
		if code == "" {
			if g := s.groups[ov.GroupCode]; g != nil {
				code = g.BehaviorCode
			}
		}
		r.Profile = s.profiles[code]
		return r
	}
	// 2) 命中号段组
	for _, g := range s.groups {
		if g.Contains(number) {
			return &Resolved{
				Number: number, GroupCode: g.Code,
				Disabled: g.State == "DISABLED",
				Profile:  s.profiles[g.BehaviorCode],
			}
		}
	}
	return nil
}

// ResolveByLine 按线路(line_code 或 address)找到绑定的客户组，再按被叫号解析。
func (s *Store) ResolveByLine(lineCodeOrAddr, number string) *Resolved {
	s.mu.RLock()
	var groupCode string
	if b, ok := s.bindings[lineCodeOrAddr]; ok && b.Enabled != 0 {
		groupCode = b.GroupCode
	} else {
		norm := normalizeLineName(lineCodeOrAddr)
		for _, b := range s.bindings {
			if b.Enabled == 0 {
				continue
			}
			// 匹配 line_code / line_address 原值，或规范化后的 line_name（FS 注入的 X-Line-Name）。
			if b.LineAddress == lineCodeOrAddr || b.LineCode == lineCodeOrAddr ||
				(b.LineName != "" && normalizeLineName(b.LineName) == norm) {
				groupCode = b.GroupCode
				break
			}
		}
	}
	s.mu.RUnlock()
	// 优先用号码解析（含个例）；若号码没命中但线路绑了组，用组行为兜底
	if r := s.ResolveByNumber(number); r != nil {
		return r
	}
	if groupCode != "" {
		s.mu.RLock()
		defer s.mu.RUnlock()
		if g := s.groups[groupCode]; g != nil {
			return &Resolved{Number: number, GroupCode: g.Code, Disabled: g.State == "DISABLED", Profile: s.profiles[g.BehaviorCode]}
		}
	}
	return nil
}

// ---- 查询（读缓存）----

func (s *Store) ListProfiles() []BehaviorProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return values(s.profiles)
}
func (s *Store) ListGroups() []CustomerGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return values(s.groups)
}
func (s *Store) ListOverrides() []CustomerOverride {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return values(s.overrides)
}
func (s *Store) ListBindings() []LineBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return values(s.bindings)
}

func values[T any](m map[string]*T) []T {
	out := make([]T, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	return out
}

// TakeNumbers 从客户组按全局游标错开取 limit 个号（带锁），让多次/并发测试不撞同一批号码。
// 组不存在或为空返回 nil。游标按组累加并对组容量取模环绕。
func (s *Store) TakeNumbers(groupCode string, limit int) []string {
	if groupCode == "" || limit <= 0 {
		return nil
	}
	s.mu.RLock()
	g := s.groups[groupCode]
	s.mu.RUnlock()
	if g == nil || g.Count <= 0 {
		return nil
	}
	s.takeMu.Lock()
	off := s.takeCursor[groupCode]
	s.takeCursor[groupCode] = (off + limit) % g.Count
	s.takeMu.Unlock()
	return g.NumbersFrom(off, limit)
}

// ---- CRUD（写 repo + 更新缓存）----

// UpsertProfile 写行为档（repo upsert by code + 更新缓存）。
func (s *Store) UpsertProfile(p BehaviorProfile) (*BehaviorProfile, error) {
	if p.Code == "" {
		return nil, errors.New("code 必填")
	}
	if p.AnswerRatio == 0 {
		p.AnswerRatio = 100
	}
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.UpsertBehaviorProfile(ctx, &p); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.profiles[p.Code] = &p
	s.mu.Unlock()
	return &p, nil
}

// UpsertGroup 写客户组。
func (s *Store) UpsertGroup(g CustomerGroup) (*CustomerGroup, error) {
	if g.Code == "" {
		return nil, errors.New("code 必填")
	}
	if g.State == "" {
		g.State = "ENABLED"
	}
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.UpsertCustomerGroup(ctx, &g); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.groups[g.Code] = &g
	s.mu.Unlock()
	return &g, nil
}

// SetGroupState 一键切换客户组在线态（ENABLED=在线接听 / DISABLED=离线，呼叫返回 503）。
func (s *Store) SetGroupState(code, state string) error {
	s.mu.Lock()
	g := s.groups[code]
	s.mu.Unlock()
	if g == nil {
		return errors.New("客户组不存在: " + code)
	}
	updated := *g
	updated.State = state
	_, err := s.UpsertGroup(updated)
	return err
}

// SetOverrideState 切换单个客户个例在线态（个例优先于组）。number 不存在个例则创建一条。
func (s *Store) SetOverrideState(number, groupCode, state string) error {
	if number == "" {
		return errors.New("number 必填")
	}
	_, err := s.UpsertOverride(CustomerOverride{Number: number, GroupCode: groupCode, State: state})
	return err
}

// UpsertOverride 写客户个例。
func (s *Store) UpsertOverride(o CustomerOverride) (*CustomerOverride, error) {
	if o.Number == "" {
		return nil, errors.New("number 必填")
	}
	if o.State == "" {
		o.State = "ENABLED"
	}
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.UpsertCustomerOverride(ctx, &o); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.overrides[o.Number] = &o
	s.mu.Unlock()
	return &o, nil
}

// UpsertBinding 写线路绑定。
func (s *Store) UpsertBinding(b LineBinding) (*LineBinding, error) {
	if b.LineCode == "" && b.LineAddress == "" {
		return nil, errors.New("line_code 或 line_address 必填")
	}
	if b.Enabled == 0 {
		b.Enabled = 1
	}
	key := b.LineCode
	if key == "" {
		key = b.LineAddress
	}
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.UpsertLineBinding(ctx, &b); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.bindings[key] = &b
	s.mu.Unlock()
	return &b, nil
}

// DeleteProfile 按 code 删行为档（repo + 缓存）。
func (s *Store) DeleteProfile(code string) error {
	if code == "" {
		return errors.New("code 必填")
	}
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.DeleteBehaviorProfile(ctx, code); err != nil {
			return err
		}
	}
	s.mu.Lock()
	delete(s.profiles, code)
	s.mu.Unlock()
	return nil
}

// DeleteGroup 按 code 删客户组。
func (s *Store) DeleteGroup(code string) error {
	if code == "" {
		return errors.New("code 必填")
	}
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.DeleteCustomerGroup(ctx, code); err != nil {
			return err
		}
	}
	s.mu.Lock()
	delete(s.groups, code)
	s.mu.Unlock()
	return nil
}

// DeleteOverride 按 number 删客户个例。
func (s *Store) DeleteOverride(number string) error {
	if number == "" {
		return errors.New("number 必填")
	}
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.DeleteCustomerOverride(ctx, number); err != nil {
			return err
		}
	}
	s.mu.Lock()
	delete(s.overrides, number)
	s.mu.Unlock()
	return nil
}

// DeleteBinding 按 line_code 删线路绑定。
func (s *Store) DeleteBinding(lineCode string) error {
	if lineCode == "" {
		return errors.New("line_code 必填")
	}
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.DeleteLineBinding(ctx, lineCode); err != nil {
			return err
		}
	}
	s.mu.Lock()
	delete(s.bindings, lineCode)
	s.mu.Unlock()
	return nil
}

// ---- 接通率随机（行为档 AnswerRatio）----

// rng 给接通率随机用（可注入以便测试确定性）。
var rng = rand.New(rand.NewSource(time.Now().UnixNano()))
var rngMu sync.Mutex

// RollAnswer 按接通率决定本次是否接通（answerRatio>=100 恒接，<=0 恒不接）。
func RollAnswer(answerRatio int) bool {
	if answerRatio >= 100 {
		return true
	}
	if answerRatio <= 0 {
		return false
	}
	rngMu.Lock()
	defer rngMu.Unlock()
	return rng.Intn(100) < answerRatio
}
