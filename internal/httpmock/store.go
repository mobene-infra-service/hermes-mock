package httpmock

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"hermes-mock/internal/entity"
	"hermes-mock/internal/model"
)

const (
	recordQueueSize = 4096
	memoryRecordMax = 1000
)

// Store 持有 Endpoint 的不可变内存快照，并异步持久化调用记录。
type Store struct {
	repo model.Repository

	mu      sync.RWMutex
	byID    map[int64]*Endpoint
	byToken map[string]*Endpoint
	nextID  int64

	recordQueue chan entity.HTTPMockRequest
	closeOnce   sync.Once
	dropped     atomic.Uint64

	memoryRecords []entity.HTTPMockRequest
	nextRecordID  int64
}

// New 创建 Store。repo=nil 仅用于单测，此时配置/记录都保存在内存。
func New(repo model.Repository) (*Store, error) {
	s := &Store{repo: repo, byID: map[int64]*Endpoint{}, byToken: map[string]*Endpoint{}}
	if repo == nil {
		return s, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := repo.ListHTTPMockEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		endpoint, err := endpointFromEntity(row)
		if err != nil {
			return nil, fmt.Errorf("加载 HTTP Mock endpoint %d/%s 失败: %w", row.ID, row.Token, err)
		}
		s.byID[endpoint.ID] = endpoint
		s.byToken[endpoint.Token] = endpoint
		if endpoint.ID > s.nextID {
			s.nextID = endpoint.ID
		}
	}
	s.recordQueue = make(chan entity.HTTPMockRequest, recordQueueSize)
	go s.recordWorker()
	return s, nil
}

func (s *Store) Close() {
	if s.recordQueue == nil {
		return
	}
	s.closeOnce.Do(func() { close(s.recordQueue) })
}

func (s *Store) List() []Endpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Endpoint, 0, len(s.byID))
	for _, endpoint := range s.byID {
		out = append(out, *endpoint)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) GetByID(id int64) (*Endpoint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	endpoint, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	copy := *endpoint
	return &copy, true
}

func (s *Store) GetByToken(token string) (*Endpoint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	endpoint, ok := s.byToken[token]
	if !ok {
		return nil, false
	}
	copy := *endpoint
	return &copy, true
}

func (s *Store) Upsert(endpoint Endpoint) (*Endpoint, error) {
	if endpoint.ID > 0 {
		current, ok := s.GetByID(endpoint.ID)
		if !ok {
			return nil, fmt.Errorf("HTTP Mock endpoint %d 不存在", endpoint.ID)
		}
		endpoint.Token = current.Token
		endpoint.GmtCreate = current.GmtCreate
	} else {
		token, err := generateToken()
		if err != nil {
			return nil, err
		}
		endpoint.Token = token
	}
	if err := endpoint.normalizeAndValidate(); err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(endpoint.Config)
	if err != nil {
		return nil, err
	}
	row := entity.HTTPMockEndpoint{
		ID: endpoint.ID, Token: endpoint.Token, Name: endpoint.Name, Enabled: endpoint.Enabled,
		ConfigJSON: string(configJSON), Remark: endpoint.Remark,
		GmtCreate: endpoint.GmtCreate, GmtModified: endpoint.GmtModified,
	}
	if s.repo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.repo.UpsertHTTPMockEndpoint(ctx, &row); err != nil {
			return nil, err
		}
		endpoint.ID = row.ID
		endpoint.GmtCreate = row.GmtCreate
		endpoint.GmtModified = row.GmtModified
	} else {
		s.mu.Lock()
		if endpoint.ID == 0 {
			s.nextID++
			endpoint.ID = s.nextID
			endpoint.GmtCreate = time.Now()
		}
		endpoint.GmtModified = time.Now()
		s.mu.Unlock()
	}
	copy := endpoint
	s.mu.Lock()
	s.byID[copy.ID] = &copy
	s.byToken[copy.Token] = &copy
	s.mu.Unlock()
	out, _ := s.GetByID(copy.ID)
	return out, nil
}

func (s *Store) Delete(id int64) error {
	endpoint, ok := s.GetByID(id)
	if !ok {
		return fmt.Errorf("HTTP Mock endpoint %d 不存在", id)
	}
	if s.repo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.repo.DeleteHTTPMockEndpoint(ctx, id); err != nil {
			return err
		}
	}
	s.mu.Lock()
	delete(s.byID, id)
	delete(s.byToken, endpoint.Token)
	if s.repo == nil {
		filtered := s.memoryRecords[:0]
		for _, row := range s.memoryRecords {
			if row.EndpointID != id {
				filtered = append(filtered, row)
			}
		}
		s.memoryRecords = filtered
	}
	s.mu.Unlock()
	return nil
}

// RecordAsync 异步记录调用。队列满时丢观测记录并告警，绝不阻塞响应。
func (s *Store) RecordAsync(row entity.HTTPMockRequest) {
	// Endpoint 已删除时不再生成孤儿观测记录；删除与在途请求竞态下以配置不存在为准。
	s.mu.RLock()
	_, endpointExists := s.byID[row.EndpointID]
	s.mu.RUnlock()
	if !endpointExists {
		return
	}
	if s.repo == nil {
		s.mu.Lock()
		s.nextRecordID++
		row.ID = s.nextRecordID
		s.memoryRecords = append(s.memoryRecords, row)
		if len(s.memoryRecords) > memoryRecordMax {
			s.memoryRecords = s.memoryRecords[len(s.memoryRecords)-memoryRecordMax:]
		}
		s.mu.Unlock()
		return
	}
	select {
	case s.recordQueue <- row:
	default:
		n := s.dropped.Add(1)
		if n == 1 || n%100 == 0 {
			logrus.Warnf("HTTP Mock 调用记录队列已满：累计丢弃 %d 条观测记录（实际响应未受影响）", n)
		}
	}
}

func (s *Store) ListRequests(filter entity.HTTPMockRequestFilter) ([]entity.HTTPMockRequest, error) {
	if s.repo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.repo.ListHTTPMockRequests(ctx, filter)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	out := make([]entity.HTTPMockRequest, 0, limit)
	for i := len(s.memoryRecords) - 1; i >= 0 && len(out) < limit; i-- {
		row := s.memoryRecords[i]
		if filter.EndpointID > 0 && row.EndpointID != filter.EndpointID {
			continue
		}
		if filter.Token != "" && row.Token != filter.Token {
			continue
		}
		if filter.Method != "" && row.Method != filter.Method {
			continue
		}
		if filter.MatchedRule != "" && row.MatchedRule != filter.MatchedRule {
			continue
		}
		if filter.SelectedCase != "" && row.SelectedCase != filter.SelectedCase {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Store) DeleteRequests(endpointID int64) (int64, error) {
	if s.repo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.repo.DeleteHTTPMockRequests(ctx, endpointID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	filtered := s.memoryRecords[:0]
	for _, row := range s.memoryRecords {
		if row.EndpointID == endpointID {
			removed++
			continue
		}
		filtered = append(filtered, row)
	}
	s.memoryRecords = filtered
	return removed, nil
}

func (s *Store) recordWorker() {
	for row := range s.recordQueue {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.repo.CreateHTTPMockRequest(ctx, &row)
		cancel()
		if err != nil {
			logrus.Warnf("HTTP Mock 调用记录落库失败 endpoint=%d token=%s: %v", row.EndpointID, row.Token, err)
		}
	}
}

func endpointFromEntity(row entity.HTTPMockEndpoint) (*Endpoint, error) {
	var config EndpointConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &config); err != nil {
		return nil, err
	}
	endpoint := &Endpoint{
		ID: row.ID, Token: row.Token, Name: row.Name, Enabled: row.Enabled,
		Config: config, Remark: row.Remark, GmtCreate: row.GmtCreate, GmtModified: row.GmtModified,
	}
	if err := endpoint.normalizeAndValidate(); err != nil {
		return nil, err
	}
	return endpoint, nil
}

func generateToken() (string, error) {
	buf := make([]byte, 12) // 96 bit；base64url 后 16 字符，短且不可枚举。
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
