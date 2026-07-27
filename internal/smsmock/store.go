package smsmock

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"hermes-mock/internal/entity"
	"hermes-mock/internal/httpmock"
	"hermes-mock/internal/model"
)

const (
	callbackWorkerCount = 4
	callbackResponseMax = 64 * 1024
	// lease 必须覆盖 Adapter 可配置的最长单次 HTTP 超时，再留 DB 状态落库余量。
	callbackClaimLease       = time.Duration(maxWaitMs)*time.Millisecond + 30*time.Second
	callbackRecoveryInterval = 30 * time.Second
)

// Store 同时承担 Endpoint 缓存和持久化 DLR worker。SMS 数据面会以 DB 为准刷新单个
// Endpoint，避免多副本控制面更新后其它副本继续使用陈旧配置；缓存服务于列表和本地写穿透。
type Store struct {
	repo         model.Repository
	registry     *Registry
	allowedHosts []string

	mu      sync.RWMutex
	byID    map[int64]*Endpoint
	byToken map[string]*Endpoint
	// taskMu 让 Endpoint/消息删除与提交落库、实际 DLR 投递互斥：删除返回后不会再有旧任务发出回调。
	taskMu sync.RWMutex

	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	wake          chan struct{}
	callbackQueue chan entity.SMSMockMessage
	// callbackSlots 一枚 token 对应一个可立即接活的 worker。scheduler 必须先拿 token
	// 再 claim DB 任务，防止慢回调在内存队列里等到 lease 过期后被其它实例重复认领。
	callbackSlots chan struct{}
	httpClient    *http.Client
}

func New(repo model.Repository, registry *Registry, allowedHosts string) (*Store, error) {
	if repo == nil {
		return nil, fmt.Errorf("smsmock: repository 必填")
	}
	if registry == nil {
		registry = DefaultRegistry()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Store{
		repo: repo, registry: registry, allowedHosts: parseAllowedHosts(allowedHosts),
		byID: map[int64]*Endpoint{}, byToken: map[string]*Endpoint{},
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		callbackQueue: make(chan entity.SMSMockMessage, callbackWorkerCount),
		callbackSlots: make(chan struct{}, callbackWorkerCount),
		httpClient: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer loadCancel()
	rows, err := repo.ListSMSMockEndpoints(loadCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	for _, row := range rows {
		endpoint, err := s.endpointFromEntity(row)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("加载 SMS Mock endpoint %d/%s 失败: %w", row.ID, row.Token, err)
		}
		s.byID[endpoint.ID] = endpoint
		s.byToken[endpoint.Token] = endpoint
	}
	now := time.Now().UTC()
	aborted, err := repo.RecoverSMSMockSubmissions(loadCtx, now.Add(-callbackClaimLease), now)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("恢复 SMS Mock 同步提交失败: %w", err)
	}
	if aborted > 0 {
		logrus.Warnf("smsmock: 已终止 %d 条重启前未完成的同步提交，不会激活其 DLR", aborted)
	}
	recovered, err := repo.RecoverSMSMockCallbacks(loadCtx, now.Add(-callbackClaimLease), now)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("恢复 SMS Mock DLR 失败: %w", err)
	}
	if recovered > 0 {
		logrus.Warnf("smsmock: 已恢复 %d 条重启前未确认的 DLR，按厂商 at-least-once 语义重新投递", recovered)
	}
	for i := 0; i < callbackWorkerCount; i++ {
		s.wg.Add(1)
		go s.callbackWorker()
	}
	s.wg.Add(1)
	go s.scheduler()
	s.signal()
	return s, nil
}

func (s *Store) Close() {
	s.cancel()
	s.wg.Wait()
}

func (s *Store) ProviderInfos() []ProviderInfo { return s.registry.Infos() }

func (s *Store) ProtocolError(provider string, status int, cause error) (WireResponse, bool) {
	adapter, ok := s.adapterForProviderPath(provider)
	if !ok {
		return WireResponse{}, false
	}
	return adapter.BuildProtocolError(status, cause), true
}

func (s *Store) List() []Endpoint {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	rows, err := s.repo.ListSMSMockEndpoints(ctx)
	cancel()
	if err == nil {
		fresh := make([]Endpoint, 0, len(rows))
		valid := true
		for _, row := range rows {
			endpoint, parseErr := s.endpointFromEntity(row)
			if parseErr != nil {
				logrus.WithError(parseErr).WithField("endpoint_id", row.ID).Warn("smsmock: 刷新 Endpoint 列表失败，继续使用上一份缓存")
				valid = false
				break
			}
			fresh = append(fresh, *endpoint)
		}
		if valid {
			s.replaceEndpointCache(fresh)
		}
	} else {
		logrus.WithError(err).Warn("smsmock: 查询 Endpoint 列表失败，继续使用上一份缓存")
	}
	return s.cachedList()
}

func (s *Store) cachedList() []Endpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Endpoint, 0, len(s.byID))
	for _, endpoint := range s.byID {
		out = append(out, cloneEndpoint(endpoint))
	}
	sortEndpoints(out)
	return out
}

func (s *Store) replaceEndpointCache(rows []Endpoint) {
	byID := make(map[int64]*Endpoint, len(rows))
	byToken := make(map[string]*Endpoint, len(rows))
	for i := range rows {
		copy := cloneEndpoint(&rows[i])
		byID[copy.ID], byToken[copy.Token] = &copy, &copy
	}
	s.mu.Lock()
	s.byID, s.byToken = byID, byToken
	s.mu.Unlock()
}

func sortEndpoints(rows []Endpoint) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].ID < rows[j-1].ID; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func (s *Store) GetByID(id int64) (*Endpoint, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	row, err := s.repo.GetSMSMockEndpoint(ctx, id)
	cancel()
	if err != nil {
		logrus.WithError(err).WithField("endpoint_id", id).Warn("smsmock: 查询 Endpoint 失败")
		return s.cachedByID(id)
	}
	if row == nil {
		s.evictEndpointByID(id)
		return nil, false
	}
	endpoint, err := s.endpointFromEntity(*row)
	if err != nil {
		logrus.WithError(err).WithField("endpoint_id", id).Warn("smsmock: Endpoint 配置非法")
		return nil, false
	}
	s.cacheEndpoint(endpoint)
	copy := cloneEndpoint(endpoint)
	return &copy, true
}

func (s *Store) GetByToken(token string) (*Endpoint, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint, err := s.loadEndpointByToken(ctx, token)
	if err != nil {
		logrus.WithError(err).Warn("smsmock: 按 token 查询 Endpoint 失败")
		return nil, false
	}
	return endpoint, endpoint != nil
}

func (s *Store) cachedByID(id int64) (*Endpoint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	copy := cloneEndpoint(row)
	return &copy, true
}

func (s *Store) loadEndpointByToken(ctx context.Context, token string) (*Endpoint, error) {
	row, err := s.repo.GetSMSMockEndpointByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if row == nil {
		s.evictEndpointByToken(token)
		return nil, nil
	}
	endpoint, err := s.endpointFromEntity(*row)
	if err != nil {
		return nil, err
	}
	s.cacheEndpoint(endpoint)
	copy := cloneEndpoint(endpoint)
	return &copy, nil
}

func (s *Store) cacheEndpoint(endpoint *Endpoint) {
	copy := cloneEndpoint(endpoint)
	s.mu.Lock()
	if previous, exists := s.byID[copy.ID]; exists && previous.Token != copy.Token {
		delete(s.byToken, previous.Token)
	}
	s.byID[copy.ID], s.byToken[copy.Token] = &copy, &copy
	s.mu.Unlock()
}

func (s *Store) evictEndpointByID(id int64) {
	s.mu.Lock()
	if endpoint, exists := s.byID[id]; exists {
		delete(s.byToken, endpoint.Token)
	}
	delete(s.byID, id)
	s.mu.Unlock()
}

func (s *Store) evictEndpointByToken(token string) {
	s.mu.Lock()
	if endpoint, exists := s.byToken[token]; exists {
		delete(s.byID, endpoint.ID)
	}
	delete(s.byToken, token)
	s.mu.Unlock()
}

func cloneEndpoint(endpoint *Endpoint) Endpoint {
	copy := *endpoint
	configJSON, _ := json.Marshal(endpoint.Config)
	_ = json.Unmarshal(configJSON, &copy.Config)
	return copy
}

func (s *Store) Upsert(endpoint Endpoint) (*Endpoint, error) {
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()
	if endpoint.ID > 0 {
		current, ok := s.GetByID(endpoint.ID)
		if !ok {
			return nil, fmt.Errorf("SMS Mock endpoint %d 不存在", endpoint.ID)
		}
		if adapterKey(endpoint.Provider, endpoint.ProtocolVersion) != adapterKey(current.Provider, current.ProtocolVersion) {
			return nil, fmt.Errorf("provider/protocolVersion 创建后不可修改；请新建 Endpoint 以保留旧协议用例")
		}
		endpoint.Token = current.Token
		endpoint.Provider, endpoint.ProtocolVersion = current.Provider, current.ProtocolVersion
		endpoint.GmtCreate = current.GmtCreate
	} else {
		token, err := generateToken()
		if err != nil {
			return nil, err
		}
		endpoint.Token = token
	}
	adapter, ok := s.registry.Get(endpoint.Provider, endpoint.ProtocolVersion)
	if !ok {
		return nil, fmt.Errorf("不支持短信厂商协议 %s/%s", endpoint.Provider, endpoint.ProtocolVersion)
	}
	if err := normalizeEndpoint(&endpoint, adapter, s.validateCallbackURL); err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(endpoint.Config)
	if err != nil {
		return nil, err
	}
	row := entity.SMSMockEndpoint{
		ID: endpoint.ID, Token: endpoint.Token, Name: endpoint.Name, Enabled: endpoint.Enabled,
		Provider: endpoint.Provider, ProtocolVersion: endpoint.ProtocolVersion,
		ConfigJSON: string(configJSON), Remark: endpoint.Remark,
		GmtCreate: endpoint.GmtCreate, GmtModified: endpoint.GmtModified,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.repo.UpsertSMSMockEndpoint(ctx, &row); err != nil {
		return nil, err
	}
	endpoint.ID, endpoint.GmtCreate, endpoint.GmtModified = row.ID, row.GmtCreate, row.GmtModified
	copy := cloneEndpoint(&endpoint)
	s.cacheEndpoint(&copy)
	out, _ := s.GetByID(copy.ID)
	return out, nil
}

func (s *Store) Delete(id int64) error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	_, ok := s.GetByID(id)
	if !ok {
		return fmt.Errorf("SMS Mock endpoint %d 不存在", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.repo.DeleteSMSMockEndpoint(ctx, id); err != nil {
		return err
	}
	s.evictEndpointByID(id)
	return nil
}

func (s *Store) ListMessages(filter entity.SMSMockMessageFilter) ([]entity.SMSMockMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.repo.ListSMSMockMessages(ctx, filter)
}

func (s *Store) GetMessage(id int64) (*entity.SMSMockMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.repo.GetSMSMockMessage(ctx, id)
}

func (s *Store) DeleteMessages(endpointID int64) (int64, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.repo.DeleteSMSMockMessages(ctx, endpointID)
}

func (s *Store) ListAttempts(messageID int64) ([]entity.SMSMockCallbackAttempt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.repo.ListSMSMockCallbackAttempts(ctx, messageID, 300)
}

func (s *Store) EnqueueCallback(id int64) (*entity.SMSMockMessage, error) {
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, err := s.repo.EnqueueSMSMockCallback(ctx, id, time.Now().UTC())
	if err == nil {
		s.signal()
	}
	return row, err
}

func (s *Store) CancelCallback(id int64) error {
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.repo.CancelSMSMockCallback(ctx, id)
}

// PrepareInvocation 解析厂商请求、按每条规范化消息选择 Case、生成线响应和 DLR，
// 并在返回前同步持久化。持久化失败时绝不向调用方谎报 Accepted。
func (s *Store) PrepareInvocation(ctx context.Context, request InvokeRequest) (*Invocation, error) {
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()
	lookupCtx, lookupCancel := context.WithTimeout(ctx, 5*time.Second)
	endpoint, lookupErr := s.loadEndpointByToken(lookupCtx, request.Token)
	lookupCancel()
	if lookupErr != nil {
		if adapter, found := s.adapterForProviderPath(request.Provider); found {
			cause := fmt.Errorf("查询 SMS Mock endpoint 失败: %w", lookupErr)
			return nil, &InvokeError{Response: adapter.BuildProtocolError(http.StatusInternalServerError, cause), Err: cause}
		}
		return nil, lookupErr
	}
	if endpoint == nil {
		if adapter, found := s.adapterForProviderPath(request.Provider); found {
			cause := fmt.Errorf("SMS Mock endpoint 不存在")
			return nil, &InvokeError{Response: adapter.BuildProtocolError(http.StatusNotFound, cause), Err: cause}
		}
		return nil, fmt.Errorf("SMS Mock endpoint 不存在")
	}
	adapter, ok := s.registry.Get(endpoint.Provider, endpoint.ProtocolVersion)
	if !ok {
		return nil, fmt.Errorf("Endpoint 协议适配器 %s/%s 未注册", endpoint.Provider, endpoint.ProtocolVersion)
	}
	protocolError := func(status int, err error) error {
		return &InvokeError{Response: adapter.BuildProtocolError(status, err), Err: err}
	}
	if !strings.EqualFold(request.Provider, endpoint.Provider) {
		return nil, protocolError(http.StatusNotFound, fmt.Errorf("Endpoint provider 为 %s，不接受 %s 路径", endpoint.Provider, request.Provider))
	}
	if !endpoint.Enabled {
		return nil, protocolError(http.StatusNotFound, fmt.Errorf("SMS Mock endpoint 已禁用"))
	}
	submission, err := adapter.ParseSubmission(request)
	if err != nil {
		status := http.StatusBadRequest
		var requestErr *RequestError
		if errors.As(err, &requestErr) && requestErr.Status >= 400 && requestErr.Status <= 599 {
			status = requestErr.Status
		}
		return nil, protocolError(status, err)
	}
	outcomes := make([]MessageOutcome, 0, len(submission.Messages))
	selections := make([]Selection, 0, len(submission.Messages))
	for _, message := range submission.Messages {
		selection, err := resolveCase(endpoint.Config, submission, message, request)
		if err != nil {
			return nil, protocolError(http.StatusBadRequest, err)
		}
		outcomes = append(outcomes, MessageOutcome{Message: message, Case: selection.Case, Spec: endpoint.Config.Cases[selection.Case]})
		selections = append(selections, selection)
	}
	response, err := adapter.BuildSubmissionResponse(outcomes)
	if err != nil {
		return nil, protocolError(http.StatusInternalServerError, err)
	}
	receivedAt := time.Now().UTC()
	sanitized := adapter.SanitizeSubmission(request)
	rows := make([]entity.SMSMockMessage, 0, len(outcomes))
	for i, outcome := range outcomes {
		spec := outcome.Spec
		receiptStatus := entity.SMSMockReceiptNotScheduled
		callback := WireCallback{}
		callbackHeadersJSON := ""
		if spec.Receipt.Enabled {
			receiptStatus = entity.SMSMockReceiptWaiting
			callbackAt := receivedAt.Add(time.Duration(response.DelayMs+spec.Receipt.DelayMs) * time.Millisecond)
			callback, err = adapter.BuildReceipt(outcome.Message, spec.Receipt, callbackAt)
			if err != nil {
				return nil, protocolError(http.StatusInternalServerError, err)
			}
			callback.Method = strings.TrimSpace(callback.Method)
			if callback.Method == "" {
				callback.Method = http.MethodPost
			}
			headers, marshalErr := json.Marshal(callback.Headers)
			if marshalErr != nil {
				return nil, protocolError(http.StatusInternalServerError, fmt.Errorf("序列化 DLR headers 失败: %w", marshalErr))
			}
			callbackHeadersJSON = string(headers)
		}
		selection := selections[i]
		rows = append(rows, entity.SMSMockMessage{
			EndpointID: endpoint.ID, Token: endpoint.Token, Provider: endpoint.Provider, ProtocolVersion: endpoint.ProtocolVersion,
			Reference: outcome.Message.Reference, Recipient: outcome.Message.Recipient, Sender: outcome.Message.Sender, Content: outcome.Message.Content,
			ReceivedAt: receivedAt, Remote: request.Remote, RequestBody: sanitized,
			MatchedRule: selection.MatchedRule, SelectedCase: selection.Case, SelectionMode: selection.SelectionMode,
			SelectedWeight: selection.SelectedWeight, TotalWeight: selection.TotalWeight,
			SubmitAction: response.Action, SubmitState: entity.SMSMockSubmitPending,
			SubmitHTTPStatus: response.HTTPStatus, SubmitResponseBody: response.Body,
			ReceiptEnabled: spec.Receipt.Enabled, ReceiptStatus: receiptStatus, ReceiptDelayMs: spec.Receipt.DelayMs,
			ReceiptTargetCount: spec.Receipt.Repeat, ReceiptRepeatIntervalMs: spec.Receipt.RepeatIntervalMs,
			CallbackURL: endpoint.Config.CallbackURL, CallbackMethod: callback.Method,
			CallbackHeadersJSON: callbackHeadersJSON, CallbackBody: callback.Body,
			CallbackTimeoutMs: endpoint.Config.CallbackTimeoutMs, CallbackMaxAttempts: endpoint.Config.CallbackMaxAttempts,
			CallbackRetryBackoffMs: endpoint.Config.CallbackRetryBackoffMs,
		})
	}
	if err := s.repo.CreateSMSMockMessages(ctx, rows); err != nil {
		return nil, protocolError(http.StatusInternalServerError, fmt.Errorf("持久化短信 Mock 任务失败: %w", err))
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return &Invocation{Endpoint: endpoint, Response: response, MessageIDs: ids}, nil
}

func resolveCase(cfg EndpointConfig, submission CanonicalSubmission, message CanonicalMessage, request InvokeRequest) (Selection, error) {
	fakeCases := make(map[string]httpmock.ResponseSpec, len(cfg.Cases))
	for name := range cfg.Cases {
		fakeCases[name] = httpmock.ResponseSpec{Status: 200, Body: name}
	}
	override := httpmock.OverrideNone
	if cfg.AllowCaseOverride {
		override = httpmock.OverrideCaseOnly
	}
	decision, err := httpmock.Resolve(httpmock.EndpointConfig{
		OverridePolicy:       override,
		DefaultResponse:      httpmock.ResponseSpec{Status: 200, Body: cfg.DefaultCase},
		DefaultWeightedCases: cfg.DefaultWeightedCases,
		Cases:                fakeCases, Rules: cfg.Rules,
	}, httpmock.IncomingRequest{
		Method: request.Method, Query: request.Query, Header: request.Header, RawBody: request.Body,
		JSONBody: map[string]any{
			"reference": message.Reference, "recipient": message.Recipient, "sender": message.Sender,
			"content": message.Content, "productToken": submission.ProductToken,
			"metadata": message.Metadata, "submissionMetadata": submission.Metadata,
		},
	})
	if err != nil {
		return Selection{}, err
	}
	caseName := decision.SelectedCase
	if caseName == "" || caseName == "default" {
		caseName = decision.Response.Body
	}
	if _, ok := cfg.Cases[caseName]; !ok {
		return Selection{}, fmt.Errorf("选择了不存在的 case %q", caseName)
	}
	return Selection{
		Case: caseName, MatchedRule: decision.MatchedRule, SelectionMode: decision.SelectionMode,
		SelectedWeight: decision.SelectedWeight, TotalWeight: decision.TotalWeight,
	}, nil
}

func (s *Store) CompleteSubmission(ctx context.Context, invocation *Invocation, state string, activateReceipt bool) error {
	if invocation == nil {
		return nil
	}
	s.taskMu.RLock()
	defer s.taskMu.RUnlock()
	completedAt := time.Now().UTC()
	if err := s.repo.CompleteSMSMockSubmission(ctx, invocation.MessageIDs, state, completedAt, activateReceipt); err != nil {
		return err
	}
	if activateReceipt {
		s.signal()
	}
	return nil
}

func (s *Store) adapterForProviderPath(provider string) (Adapter, bool) {
	for _, info := range s.registry.Infos() {
		if strings.EqualFold(info.Provider, provider) {
			return s.registry.Get(info.Provider, info.ProtocolVersion)
		}
	}
	return nil, false
}

func (s *Store) endpointFromEntity(row entity.SMSMockEndpoint) (*Endpoint, error) {
	var config EndpointConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &config); err != nil {
		return nil, err
	}
	endpoint := &Endpoint{
		ID: row.ID, Token: row.Token, Name: row.Name, Enabled: row.Enabled,
		Provider: row.Provider, ProtocolVersion: row.ProtocolVersion, Config: config, Remark: row.Remark,
		GmtCreate: row.GmtCreate, GmtModified: row.GmtModified,
	}
	adapter, ok := s.registry.Get(endpoint.Provider, endpoint.ProtocolVersion)
	if !ok {
		return nil, fmt.Errorf("协议适配器 %s/%s 未注册", endpoint.Provider, endpoint.ProtocolVersion)
	}
	if err := normalizeEndpoint(endpoint, adapter, s.validateCallbackURL); err != nil {
		return nil, err
	}
	return endpoint, nil
}

func generateToken() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func parseAllowedHosts(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if value := strings.ToLower(strings.TrimSpace(part)); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (s *Store) validateCallbackURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("callbackUrl 必须是完整 http/https URL")
	}
	if u.User != nil {
		return fmt.Errorf("callbackUrl 不允许携带 userinfo")
	}
	if len(s.allowedHosts) == 0 {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range s.allowedHosts {
		if allowed == "*" || host == allowed || (strings.HasPrefix(allowed, "*.") && strings.HasSuffix(host, allowed[1:]) && host != allowed[2:]) {
			return nil
		}
	}
	return fmt.Errorf("callbackUrl host %q 不在 SMS_MOCK_CALLBACK_ALLOWED_HOSTS 白名单", host)
}

func (s *Store) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Store) scheduler() {
	defer s.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	recoveryTicker := time.NewTicker(callbackRecoveryInterval)
	defer recoveryTicker.Stop()
	for {
		recoverStale := false
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		case <-recoveryTicker.C:
			recoverStale = true
		}
		if recoverStale {
			s.recoverStaleSubmissions()
			s.recoverStaleCallbacks()
		}
		available := len(s.callbackSlots)
		if available <= 0 {
			continue
		}
		if available > 32 {
			available = 32
		}
		acquired := 0
	acquireSlots:
		for acquired < available {
			select {
			case <-s.callbackSlots:
				acquired++
			default:
				break acquireSlots
			}
		}
		if acquired == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		rows, err := s.repo.ClaimDueSMSMockCallbacks(ctx, time.Now().UTC(), acquired)
		cancel()
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				logrus.WithError(err).Warn("smsmock: claim due DLR 失败")
			}
		}
		queued := 0
		for _, row := range rows {
			select {
			case s.callbackQueue <- row:
				queued++
			case <-s.ctx.Done():
				return
			}
		}
		// 已声明 ready 但本轮没有 DB 任务的 worker 收到零值后释放 taskMu，
		// 让等待中的删除/清空 writer 能取得锁；Go RWMutex 会优先已等待 writer。
		for i := queued; i < acquired; i++ {
			select {
			case s.callbackQueue <- entity.SMSMockMessage{}:
			case <-s.ctx.Done():
				return
			}
		}
	}
}

func (s *Store) recoverStaleSubmissions() {
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	recovered, err := s.repo.RecoverSMSMockSubmissions(ctx, now.Add(-callbackClaimLease), now)
	cancel()
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logrus.WithError(err).Warn("smsmock: 恢复过期同步提交失败")
		}
		return
	}
	if recovered > 0 {
		logrus.Warnf("smsmock: 已终止 %d 条超过提交 lease 的同步请求，不会激活其 DLR", recovered)
	}
}

func (s *Store) recoverStaleCallbacks() {
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	recovered, err := s.repo.RecoverSMSMockCallbacks(ctx, now.Add(-callbackClaimLease), now)
	cancel()
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logrus.WithError(err).Warn("smsmock: 恢复过期 DLR claim 失败")
		}
		return
	}
	if recovered > 0 {
		logrus.Warnf("smsmock: 已恢复 %d 条超过 claim lease 的 DLR", recovered)
		s.signal()
	}
}

func (s *Store) callbackWorker() {
	defer s.wg.Done()
	hadTask := false
	for {
		// 先拿删除互斥锁再声明 ready，确保 scheduler claim 后任务能立即执行，
		// 不会因排队等锁超过 callback lease。无 due task 时 scheduler 会发零值释放锁。
		s.taskMu.RLock()
		select {
		case <-s.ctx.Done():
			s.taskMu.RUnlock()
			return
		case s.callbackSlots <- struct{}{}:
			if hadTask {
				s.signal()
				hadTask = false
			}
		}
		select {
		case <-s.ctx.Done():
			s.taskMu.RUnlock()
			return
		case row := <-s.callbackQueue:
			if row.ID > 0 {
				s.deliverCallback(row)
				hadTask = true
			}
			s.taskMu.RUnlock()
		}
	}
}

func (s *Store) deliverCallback(row entity.SMSMockMessage) {
	// 队列里是 claim 时的快照。删除/清空或其它状态变更后必须重新确认，不能用陈旧任务发外部请求。
	reloadCtx, reloadCancel := context.WithTimeout(s.ctx, 5*time.Second)
	current, err := s.repo.GetSMSMockMessage(reloadCtx, row.ID)
	reloadCancel()
	if err != nil || current.ReceiptStatus != entity.SMSMockReceiptDelivering {
		return
	}
	row = *current
	startedAt := time.Now().UTC()
	attemptNo := row.CallbackCurrentAttempt + 1
	deliveryNo := row.ReceiptSentCount + 1
	attempt := entity.SMSMockCallbackAttempt{
		MessageID: row.ID, Reference: row.Reference, DeliveryNo: deliveryNo, AttemptNo: attemptNo,
		StartedAt: startedAt, URL: row.CallbackURL, Method: row.CallbackMethod,
		RequestHeadersJSON: row.CallbackHeadersJSON, RequestBody: row.CallbackBody,
	}
	var responseStatus int
	var responseBody, lastError string
	method := strings.TrimSpace(row.CallbackMethod)
	if method == "" {
		method = http.MethodPost
	}
	attempt.Method = method
	headers := make(http.Header)
	if strings.TrimSpace(row.CallbackHeadersJSON) != "" {
		if err := json.Unmarshal([]byte(row.CallbackHeadersJSON), &headers); err != nil {
			lastError = fmt.Sprintf("解析持久化 DLR headers 失败: %v", err)
		}
	}
	if lastError == "" {
		if err := s.validateCallbackURL(row.CallbackURL); err != nil {
			lastError = err.Error()
		}
	}
	if lastError == "" {
		timeout := time.Duration(row.CallbackTimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		ctx, cancel := context.WithTimeout(s.ctx, timeout)
		req, err := http.NewRequestWithContext(ctx, method, row.CallbackURL, bytes.NewBufferString(row.CallbackBody))
		if err == nil {
			req.Header = headers.Clone()
			resp, doErr := s.httpClient.Do(req)
			if doErr != nil {
				err = doErr
			} else {
				responseStatus = resp.StatusCode
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, callbackResponseMax+1))
				_ = resp.Body.Close()
				if readErr != nil {
					err = readErr
				} else if len(body) > callbackResponseMax {
					err = fmt.Errorf("回调响应体超过 %d bytes", callbackResponseMax)
				} else {
					responseBody = string(body)
					if responseStatus < 200 || responseStatus >= 300 {
						err = fmt.Errorf("回调返回 HTTP %d", responseStatus)
					}
				}
			}
		}
		cancel()
		if err != nil {
			lastError = err.Error()
		}
	}
	completedAt := time.Now().UTC()
	attempt.CompletedAt = &completedAt
	attempt.HTTPStatus, attempt.ResponseBody, attempt.Error = responseStatus, responseBody, lastError
	attempt.Success = lastError == ""
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := s.repo.CreateSMSMockCallbackAttempt(ctx, &attempt); err != nil {
		logrus.WithError(err).WithField("message_id", row.ID).Warn("smsmock: 保存 DLR attempt 失败")
	}
	update := entity.SMSMockCallbackUpdate{
		MessageID: row.ID, HTTPStatus: responseStatus, ResponseBody: responseBody, LastError: lastError,
	}
	if lastError == "" {
		update.IncrementSent = true
		update.ResetCurrentAttempt = true
		if row.ReceiptSentCount+1 < row.ReceiptTargetCount {
			next := completedAt.Add(time.Duration(row.ReceiptRepeatIntervalMs) * time.Millisecond)
			update.NextStatus, update.NextDueAt = entity.SMSMockReceiptPending, &next
		} else {
			update.NextStatus, update.CompletedAt = entity.SMSMockReceiptSucceeded, &completedAt
		}
	} else if attemptNo < row.CallbackMaxAttempts {
		backoff := retryBackoff(row.CallbackRetryBackoffMs, attemptNo)
		next := completedAt.Add(backoff)
		update.NextStatus, update.NextDueAt = entity.SMSMockReceiptRetry, &next
	} else {
		update.NextStatus, update.CompletedAt = entity.SMSMockReceiptFailed, &completedAt
	}
	if err := s.repo.UpdateSMSMockCallback(ctx, update); err != nil {
		logrus.WithError(err).WithField("message_id", row.ID).Warn("smsmock: 推进 DLR 状态失败")
	}
	cancel()
	if update.NextDueAt != nil {
		s.signal()
	}
	entry := logrus.WithFields(logrus.Fields{
		"message_id": row.ID, "reference": row.Reference, "delivery_no": deliveryNo,
		"attempt_no": attemptNo, "http_status": responseStatus, "next_status": update.NextStatus,
	})
	if lastError != "" {
		entry = entry.WithError(errors.New(lastError))
	}
	entry.Info("smsmock: DLR 投递完成")
}

func retryBackoff(baseMs, attempt int) time.Duration {
	if baseMs <= 0 {
		baseMs = 1000
	}
	multiplier := 1 << max(0, attempt-1)
	value := time.Duration(baseMs*multiplier) * time.Millisecond
	if value > time.Duration(maxWaitMs)*time.Millisecond {
		return time.Duration(maxWaitMs) * time.Millisecond
	}
	return value
}
