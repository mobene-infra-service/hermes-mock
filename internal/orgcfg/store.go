// Package orgcfg 维护「机构配置」：每个机构一套 OpenAPI 接入凭据（地址/密钥/模式）+
// hermes-ws/fs-esl 等接入地址，持久化到 hermes_mock（独立库，不碰 Hermes 表），
// 并维护「当前测试机构」选择。这是 mock 与 Hermes 交互的入口配置——切机构即切这套凭据。
// 本包是「内存缓存」领域服务：DB 读写经 model.Repository；实体定义在 internal/entity。
package orgcfg

import (
	"context"
	"errors"
	"sync"
	"time"

	"hermes-mock/internal/entity"
	"hermes-mock/internal/hermesopenapi"
	"hermes-mock/internal/model"
)

// OrgConfig 实体别名（定义已下沉 entity；消费方继续用 orgcfg.OrgConfig 旧名）。
type OrgConfig = entity.OrgConfig

// Cred 转成 OpenAPI 客户端凭据。
func credOf(o OrgConfig) hermesopenapi.Cred {
	return hermesopenapi.Cred{
		OrgCode: o.OrgCode, OrgName: o.OrgName, UserCode: o.UserCode, Mode: o.Mode,
		GatewayURL: o.GatewayURL, APIKey: o.APIKey,
		BasicURL: o.BasicURL, CallCenterURL: o.CallCenterURL, CallBotURL: o.CallBotURL, OTPURL: o.OTPURL,
	}
}

// Store 机构配置的内存缓存领域服务（写穿透 Repository，读走缓存）。
type Store struct {
	repo    model.Repository
	mu      sync.RWMutex
	configs map[string]*OrgConfig // orgCode -> config
	order   []string
	current string
}

// New 基于 Repository 构建 Store 并载入机构配置。
func New(repo model.Repository) (*Store, error) {
	if repo == nil {
		return nil, errors.New("repository 不可用（hermes_mock 库未初始化）")
	}
	s := NewMemory()
	s.repo = repo
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewMemory 纯内存 Store——**仅供单测**。
func NewMemory() *Store {
	return &Store{configs: map[string]*OrgConfig{}}
}

func (s *Store) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (s *Store) reload() error {
	if s.repo == nil {
		return nil
	}
	ctx, cancel := s.ctx()
	defer cancel()
	rows, err := s.repo.ListOrgConfigs(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs = map[string]*OrgConfig{}
	s.order = nil
	for i := range rows {
		r := rows[i]
		s.configs[r.OrgCode] = &r
		s.order = append(s.order, r.OrgCode)
	}
	if s.current == "" && len(s.order) > 0 {
		s.current = s.order[0]
	}
	return nil
}

// List 返回所有机构配置（稳定顺序）。
func (s *Store) List() []OrgConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]OrgConfig, 0, len(s.order))
	for _, code := range s.order {
		out = append(out, *s.configs[code])
	}
	return out
}

// Get 取某机构配置。
func (s *Store) Get(orgCode string) (*OrgConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.configs[orgCode]
	if !ok {
		return nil, false
	}
	cp := *c
	return &cp, true
}

// Upsert 新增/编辑机构配置（按 org_code 唯一）。
func (s *Store) Upsert(c OrgConfig) (*OrgConfig, error) {
	if c.OrgCode == "" {
		return nil, errors.New("orgCode 必填")
	}
	if c.Mode == "" {
		c.Mode = "direct"
	}
	c.GmtModified = time.Now()
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.UpsertOrgConfig(ctx, &c); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	if _, ok := s.configs[c.OrgCode]; !ok {
		s.order = append(s.order, c.OrgCode)
	}
	cp := c
	s.configs[c.OrgCode] = &cp
	if s.current == "" {
		s.current = c.OrgCode
	}
	s.mu.Unlock()
	return &c, nil
}

// Delete 删除机构配置。
func (s *Store) Delete(orgCode string) error {
	if s.repo != nil {
		ctx, cancel := s.ctx()
		defer cancel()
		if err := s.repo.DeleteOrgConfig(ctx, orgCode); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.configs, orgCode)
	for i, c := range s.order {
		if c == orgCode {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	if s.current == orgCode {
		s.current = ""
		if len(s.order) > 0 {
			s.current = s.order[0]
		}
	}
	return nil
}

// Current 返回当前机构 code。
func (s *Store) Current() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// SetCurrent 切换当前机构（须已配置）。
func (s *Store) SetCurrent(orgCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.configs[orgCode]; !ok {
		return errors.New("机构未配置: " + orgCode)
	}
	s.current = orgCode
	return nil
}

// CurrentCred 返回当前机构的 OpenAPI 凭据（未配置返回 ok=false）。
func (s *Store) CurrentCred() (hermesopenapi.Cred, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.configs[s.current]
	if !ok {
		return hermesopenapi.Cred{}, false
	}
	return credOf(*c), true
}

// CurrentConfig 返回当前机构完整配置（未配置返回 ok=false）。
func (s *Store) CurrentConfig() (OrgConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.configs[s.current]
	if !ok {
		return OrgConfig{}, false
	}
	return *c, true
}

// CredOf 返回指定机构的凭据。
func (s *Store) CredOf(orgCode string) (hermesopenapi.Cred, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.configs[orgCode]
	if !ok {
		return hermesopenapi.Cred{}, false
	}
	return credOf(*c), true
}

// Client 返回当前机构的 OpenAPI 客户端（未配置返回 nil,false）。
func (s *Store) Client() (*hermesopenapi.Client, bool) {
	cred, ok := s.CurrentCred()
	if !ok {
		return nil, false
	}
	return hermesopenapi.New(cred), true
}
