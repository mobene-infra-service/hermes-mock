package hermesopenapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// stratflow.go —— 对接 hermes-stratflow 应用层 mock 下游 OpenAPI（编排台数据面）。
//
// 契约权威源：hermes 仓 docs/reference/api-reference.md 的 hermes-stratflow Mock 小节。本文件只做 Go 侧消费：
//   - mock 管理：gate 开关 / per-node 结局配置 / 在途计划观测 / 一键清空（/openapi/mock/*）
//   - 发现：方案·版本·名单·字段·绑定·版本运行记录·物理 execution 及进度（/openapi/mock/*）
//   - 触发：导名单生成 run（/openapi/collections/{code}/import）
//
// 全部经 prodStratflow 产品前缀（gateway 模式 → /stratflow/**；direct 模式 → StratflowURL）。
// 边界：mock 后端不当被叫腿参与此路径——这是「应用层 mock 编排台」，与 SIP 被叫腿正交（见 docs/SCOPE.md）。

// ===== DTO（JSON 驼峰对齐 stratflow 实现）=====

// SfMockGateView 两层闸门视图。Mode/SchemeModes 是三态新契约；Global/Schemes 仅兼容旧客户端。
type SfMockGateView struct {
	Master           bool              `json:"master"`
	Mode             string            `json:"mode,omitempty"`
	Global           bool              `json:"global"`
	SchemeModes      map[string]string `json:"schemeModes,omitempty"`
	Schemes          map[string]bool   `json:"schemes"`
	DeliveryPaused   bool              `json:"deliveryPaused"`
	ReceiptWindowSec int64             `json:"receiptWindowSec"`
}

type SfMockCaseResult struct {
	Type              string  `json:"type"`
	Status            string  `json:"status"`
	TerminalAttemptNo *int    `json:"terminalAttemptNo,omitempty"`
	RetryRingStatus   *string `json:"retryRingStatus,omitempty"`
	RingStatus        *string `json:"ringStatus,omitempty"`
	Intention         *string `json:"intention,omitempty"`
	TalkDurationSec   *int    `json:"talkDurationSec,omitempty"`
	FailureReason     *string `json:"failureReason,omitempty"`
	ErrorCode         *string `json:"errorCode,omitempty"`
	ErrorDesc         *string `json:"errorDesc,omitempty"`
	PartCount         *int    `json:"partCount,omitempty"`
}

type SfMockCase struct {
	Key     string           `json:"key"`
	Name    string           `json:"name"`
	DelayMs int64            `json:"delayMs"`
	Result  SfMockCaseResult `json:"result"`
}

type SfMockWeightedCase struct {
	CaseKey string `json:"caseKey"`
	Weight  int    `json:"weight"`
}

type SfMockSelection struct {
	Mode    string               `json:"mode"`
	CaseKey *string              `json:"caseKey,omitempty"`
	Choices []SfMockWeightedCase `json:"choices,omitempty"`
}

type SfMockMatchCondition struct {
	Key      string  `json:"key"`
	Type     string  `json:"type"`
	ItemType *string `json:"itemType,omitempty"`
	Op       string  `json:"op"`
	Value    any     `json:"value,omitempty"`
}

type SfMockSelectionRule struct {
	Name       string                 `json:"name"`
	Priority   int                    `json:"priority"`
	Conditions []SfMockMatchCondition `json:"conditions"`
	Selection  SfMockSelection        `json:"selection"`
}

// SfMockNodeConfig 是一个节点的完整原子配置。
type SfMockNodeConfig struct {
	ForcedCaseKey    *string               `json:"forcedCaseKey"`
	Cases            []SfMockCase          `json:"cases"`
	DefaultSelection *SfMockSelection      `json:"defaultSelection"`
	Rules            []SfMockSelectionRule `json:"rules"`
}

type SfMockResultSchema struct {
	Type           string   `json:"type"`
	Statuses       []string `json:"statuses"`
	RingStatuses   []string `json:"ringStatuses"`
	Intentions     []string `json:"intentions"`
	MaxAttemptNo   *int     `json:"maxAttemptNo"`
	RetryStepGapMs *int64   `json:"retryStepGapMs"`
}

type SfMockMatchField struct {
	Key      string  `json:"key"`
	Type     string  `json:"type"`
	ItemType *string `json:"itemType"`
}

type SfMockMatchSchema struct {
	Fields    []SfMockMatchField  `json:"fields"`
	Operators map[string][]string `json:"operators"`
}

// SfMockStep 一步回执（回放器据 delayMs 排到点）。
type SfMockStep struct {
	DelayMs       int64          `json:"delayMs"`
	Status        string         `json:"status"`
	FailureReason *string        `json:"failureReason"`
	Data          map[string]any `json:"data"`
}

type SfMockCasePreview struct {
	Steps        []SfMockStep   `json:"steps"`
	ActionFinal  string         `json:"actionFinal"`
	NodePort     string         `json:"nodePort"`
	ExpectedVars map[string]any `json:"expectedVars"`
	DynamicVars  []string       `json:"dynamicVars"`
}

// SfMockNodeView 某触达节点的有效配置、可编辑 Schema 与逐 Case 编译预览。
type SfMockNodeView struct {
	NodeID       string                       `json:"nodeId"`
	Type         string                       `json:"type"`
	Channel      *string                      `json:"channel"`
	Configured   bool                         `json:"configured"`
	ResultSchema SfMockResultSchema           `json:"resultSchema"`
	MatchSchema  SfMockMatchSchema            `json:"matchSchema"`
	Config       SfMockNodeConfig             `json:"config"`
	Previews     map[string]SfMockCasePreview `json:"previews"`
}

// SfMockActionPlan per-action 计划；DEAD 仍返回，便于观测和人工恢复。
type SfMockActionPlan struct {
	ActionCode     string       `json:"actionCode"`
	RunCode        string       `json:"runCode"`
	NodeID         string       `json:"nodeId"`
	EntryCode      string       `json:"entryCode"`
	Channel        string       `json:"channel"`
	OrgCode        string       `json:"orgCode"`
	OutcomeKey     string       `json:"outcomeKey"`
	OutcomeLabel   *string      `json:"outcomeLabel"`
	SelectionMode  *string      `json:"selectionMode"`
	MatchedRule    *string      `json:"matchedRule"`
	SelectedWeight *int         `json:"selectedWeight"`
	TotalWeight    *int         `json:"totalWeight"`
	BaseMs         *int64       `json:"baseMs"`
	Idx            *int         `json:"idx"`
	Steps          []SfMockStep `json:"steps"`
	Status         string       `json:"status"`
	NextDueAt      string       `json:"nextDueAt"`
	RetryCount     int          `json:"retryCount"`
	LastError      *string      `json:"lastError"`
}

type SfMockDecision struct {
	ActionCode     string         `json:"actionCode"`
	RunCode        string         `json:"runCode"`
	NodeID         string         `json:"nodeId"`
	EntryCode      string         `json:"entryCode"`
	Channel        string         `json:"channel"`
	CaseKey        string         `json:"caseKey"`
	CaseName       *string        `json:"caseName"`
	SelectionMode  *string        `json:"selectionMode"`
	MatchedRule    *string        `json:"matchedRule"`
	SelectedWeight *int           `json:"selectedWeight"`
	TotalWeight    *int           `json:"totalWeight"`
	Status         string         `json:"status"`
	NoReceipt      bool           `json:"noReceipt"`
	ExpectedVars   map[string]any `json:"expectedVars"`
	ExpectedPort   *string        `json:"expectedPort"`
	ActualVars     map[string]any `json:"actualVars"`
	ActualPort     *string        `json:"actualPort"`
	Routed         bool           `json:"routed"`
	SelectedAt     *string        `json:"selectedAt"`
	CompletedAt    *string        `json:"completedAt"`
	LastError      *string        `json:"lastError"`
}

type SfMockDecisionPage struct {
	Records  []SfMockDecision `json:"records"`
	Total    int64            `json:"total"`
	PageNo   int64            `json:"pageNo"`
	PageSize int64            `json:"pageSize"`
}

// SfWorkflow 授权方案列表项。
type SfWorkflow struct {
	DefCode    string `json:"defCode"`
	Name       string `json:"name"`
	Status     int    `json:"status"`
	OrgEnabled bool   `json:"orgEnabled"`
}

// SfWorkflowDetail 方案详情。versionCode = 当前启用发布版本（= 新 run 实际绑定版本，非草稿）。
type SfWorkflowDetail struct {
	DefCode     string `json:"defCode"`
	Name        string `json:"name"`
	Status      int    `json:"status"`
	OrgEnabled  bool   `json:"orgEnabled"`
	VersionCode string `json:"versionCode"`
}

// SfCollection 名单集合列表项。
type SfCollection struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	Status         int    `json:"status"`
	BoundPlanCount int    `json:"boundPlanCount"`
	FieldCount     int    `json:"fieldCount"`
	EntryCount     int    `json:"entryCount"`
}

// SfCollectionField 名单字段快照（组装 import rows[].bizFields + 前端动态表单校验）。
type SfCollectionField struct {
	Key         string  `json:"key"`
	DisplayName string  `json:"displayName"`
	DataType    string  `json:"dataType"`
	Format      *string `json:"format"`
	Options     any     `json:"options"`
	ItemType    *string `json:"itemType"`
	MaxLen      *int    `json:"maxLen"`
	Scale       *int    `json:"scale"`
	Required    bool    `json:"required"`
	Sort        int     `json:"sort"`
}

// SfBinding 集合当前 active 绑定的方案。
type SfBinding struct {
	DefCode string `json:"defCode"`
	DefName string `json:"defName"`
	Status  int    `json:"status"`
}

// SfVersionRun 页面版本运行记录。一行聚合同一 collection/def/version 下全部已提交物理 Run；
// code 固定等于 versionCode，版本级不存在唯一 batchCode。
type SfVersionRun struct {
	Code                    string   `json:"code"`
	CollectionCode          string   `json:"collectionCode"`
	DefCode                 string   `json:"defCode"`
	DefName                 *string  `json:"defName"`
	BindingStatus           int      `json:"bindingStatus"`
	VersionCode             string   `json:"versionCode"`
	VersionNo               int      `json:"versionNo"`
	Result                  int      `json:"result"`
	Status                  int      `json:"status"`
	NumberCount             int64    `json:"numberCount"`
	ReachedEndCount         int64    `json:"reachedEndCount"`
	TerminalCount           int64    `json:"terminalCount"`
	ExpiredCount            int64    `json:"expiredCount"`
	CanceledCount           int64    `json:"canceledCount"`
	FirstConsumedAt         any      `json:"firstConsumedAt"`
	TerminalAt              any      `json:"terminalAt"`
	AnomalyFlags            []string `json:"anomalyFlags"`
	CreatedAt               any      `json:"createdAt"`
	UpdatedAt               any      `json:"updatedAt"`
	FailFields              []string `json:"failFields"`
	ContractFailure         any      `json:"contractFailure"`
	LocalCancelPending      bool     `json:"localCancelPending"`
	ExecutionCount          int64    `json:"executionCount"`
	UnsettledExecutionCount int64    `json:"unsettledExecutionCount"`
}

// SfVersionRunPage 对齐 MyBatis-Plus PageDTO 的 JSON 结构。
type SfVersionRunPage struct {
	Records []SfVersionRun `json:"records"`
	Total   int64          `json:"total"`
	Size    int64          `json:"size"`
	Current int64          `json:"current"`
	Pages   int64          `json:"pages"`
}

// SfRun 物理 execution 列表/进度里的漏斗汇总。code = runCode。
type SfRun struct {
	Code            string  `json:"code"`
	CollectionCode  string  `json:"collectionCode"`
	DefCode         string  `json:"defCode"`
	DefName         string  `json:"defName"`
	VersionCode     string  `json:"versionCode"`
	Status          int     `json:"status"`
	NumberCount     int     `json:"numberCount"`
	ReachedEndCount int     `json:"reachedEndCount"`
	TerminalCount   int     `json:"terminalCount"`
	ExpiredCount    int     `json:"expiredCount"`
	CanceledCount   int     `json:"canceledCount"`
	FirstConsumedAt *string `json:"firstConsumedAt"`
	TerminalAt      *string `json:"terminalAt"`
	AnomalyFlags    any     `json:"anomalyFlags"`
}

// SfRunNode 节点漏斗计数 + 边流量（断言分支落点看 edgeFlow，当前不返回 entry 级 phase 明细）。
type SfRunNode struct {
	NodeID     string         `json:"nodeId"`
	Inflow     int            `json:"inflow"`
	Processed  int            `json:"processed"`
	Processing int            `json:"processing"`
	EdgeFlow   map[string]int `json:"edgeFlow"`
}

// SfRunProgress run 进度：漏斗汇总 + 各节点边流量。
type SfRunProgress struct {
	Run                 SfRun       `json:"run"`
	Nodes               []SfRunNode `json:"nodes"`
	CallDispatchedCount int64       `json:"callDispatchedCount"`
	SmsDispatchedCount  int64       `json:"smsDispatchedCount"`
}

// SfVersionRunProgress 版本运行进度。run 是版本聚合；window 内五项计数按上传时间过滤。
type SfVersionRunProgress struct {
	Run                 SfVersionRun `json:"run"`
	Window              any          `json:"window"`
	Nodes               []SfRunNode  `json:"nodes"`
	CallDispatchedCount int64        `json:"callDispatchedCount"`
	SmsDispatchedCount  int64        `json:"smsDispatchedCount"`
}

// SfImportRow 导入行（phone 明文 tokenize 后即丢；bizFields 按集合字段类型落原生值）。
type SfImportRow struct {
	Phone     string         `json:"phone"`
	BizFields map[string]any `json:"bizFields,omitempty"`
}

// SfImportReq 导名单请求（idempotencyKey 防重复提交）。
type SfImportReq struct {
	Rows           []SfImportRow `json:"rows"`
	IdempotencyKey string        `json:"idempotencyKey,omitempty"`
}

// SfImportPlan 导入结果里每方案的生成情况。result：1=已生成 run / 2=字段契约失败(见 failFields) / 3=无可用绑定。
type SfImportPlan struct {
	DefCode     string   `json:"defCode"`
	VersionCode string   `json:"versionCode"`
	Result      int      `json:"result"`
	RunCode     string   `json:"runCode"`
	FailFields  []string `json:"failFields"`
}

// SfImportFieldError 是稳定英文 reason + 可选定位参数；原始字段值不会由 Hermes 返回。
type SfImportFieldError struct {
	FieldKey     string `json:"fieldKey"`
	Reason       string `json:"reason"`
	ItemIndex    *int   `json:"itemIndex,omitempty"`
	MaxLength    *int   `json:"maxLength,omitempty"`
	ActualLength *int   `json:"actualLength,omitempty"`
	MaxScale     *int   `json:"maxScale,omitempty"`
	ActualScale  *int   `json:"actualScale,omitempty"`
}

type SfImportRowError struct {
	RowNo  int                  `json:"rowNo"`
	Errors []SfImportFieldError `json:"errors"`
}

// SfImportResult 导入响应（批次顶层字段 + plans）。取 result==1 的 plans[].runCode 进入断言。
type SfImportResult struct {
	Code            string             `json:"code"`
	BatchCode       string             `json:"batchCode"`
	CollectionCode  string             `json:"collectionCode"`
	IdempotencyKey  string             `json:"idempotencyKey"`
	Status          int                `json:"status"`
	Total           int                `json:"total"`
	Success         int                `json:"success"`
	Fail            int                `json:"fail"`
	Plans           []SfImportPlan     `json:"plans"`
	Errors          []SfImportRowError `json:"errors"`
	ErrorsTruncated bool               `json:"errorsTruncated"`
}

// 导入结果码常量（对齐实现文档 §5）。
const (
	SfImportResultRunCreated = 1 // 已生成 run
	SfImportResultFieldFail  = 2 // 字段契约失败，见 failFields
	SfImportResultNoBinding  = 3 // 无可用绑定
)

// sfGet/sfDo 小工具：走 prodStratflow 产品前缀调用并把 data 反序列化到 out（out=nil 时忽略 data）。
func (c *Client) sfCall(ctx context.Context, method, path string, body, out any) error {
	data, err := c.call(ctx, prodStratflow, method, path, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return json.Unmarshal(data, out)
}

// ===== ① mock 管理（gate / config / plans / all）=====

// StratflowGate 查开关现状。
func (c *Client) StratflowGate(ctx context.Context) (SfMockGateView, error) {
	var v SfMockGateView
	err := c.sfCall(ctx, "GET", "/openapi/mock/gate", nil, &v)
	return v, err
}

// StratflowSetGlobalMode 设置全局 REAL/MOCK/PAUSED（仅影响尚未固化模式的新派发）。
func (c *Client) StratflowSetGlobalMode(ctx context.Context, mode string) (SfMockGateView, error) {
	var v SfMockGateView
	q := url.Values{"mode": {mode}}
	if mode == "REAL" || mode == "MOCK" {
		q.Set("enabled", strconv.FormatBool(mode == "MOCK"))
	}
	err := c.sfCall(ctx, "PUT", "/openapi/mock/gate/global?"+q.Encode(), nil, &v)
	return v, err
}

// StratflowSetSchemeMode 按方案覆盖三态模式（优先级高于 global）。
func (c *Client) StratflowSetSchemeMode(ctx context.Context, defCode, mode string) (SfMockGateView, error) {
	var v SfMockGateView
	q := url.Values{"mode": {mode}}
	if mode == "REAL" || mode == "MOCK" {
		q.Set("enabled", strconv.FormatBool(mode == "MOCK"))
	}
	path := "/openapi/mock/gate/scheme/" + url.PathEscape(defCode) + "?" + q.Encode()
	err := c.sfCall(ctx, "PUT", path, nil, &v)
	return v, err
}

// StratflowClearSchemeGate 删除单方案覆盖，回落 global。
func (c *Client) StratflowClearSchemeGate(ctx context.Context, defCode string) (SfMockGateView, error) {
	var v SfMockGateView
	err := c.sfCall(ctx, "DELETE", "/openapi/mock/gate/scheme/"+url.PathEscape(defCode), nil, &v)
	return v, err
}

// StratflowSetDeliveryPaused 暂停/恢复回放（step-through；不改派发模式）。
func (c *Client) StratflowSetDeliveryPaused(ctx context.Context, paused bool) (SfMockGateView, error) {
	var v SfMockGateView
	err := c.sfCall(ctx, "PUT", "/openapi/mock/delivery?paused="+strconv.FormatBool(paused), nil, &v)
	return v, err
}

// StratflowSetReceiptWindow 设 mock 回执窗压缩秒数（0=关闭，用真实回执窗；非 0 须 ≥60）。仅作用于 mock 派发，
// 把小时级超时窗压到秒级以实测「超时无回执」分支；超时仍由真实 ④ 回执超时判定，不合成假超时。
func (c *Client) StratflowSetReceiptWindow(ctx context.Context, seconds int64) (SfMockGateView, error) {
	var v SfMockGateView
	err := c.sfCall(ctx, "PUT", "/openapi/mock/receipt-window?seconds="+strconv.FormatInt(seconds, 10), nil, &v)
	return v, err
}

// StratflowListConfig 列某发布版本各触达节点的类型化 Case 配置、编辑 Schema 与编译预览。
func (c *Client) StratflowListConfig(ctx context.Context, versionCode string) ([]SfMockNodeView, error) {
	var out []SfMockNodeView
	err := c.sfCall(ctx, "GET", "/openapi/mock/config/"+url.PathEscape(versionCode), nil, &out)
	if err != nil {
		return nil, err
	}
	for _, node := range out {
		if err := validateTypedMockNode(node); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// validateTypedMockNode 防止旧 Hermes 的 forcedOutcome/weights 响应被新 DTO 静默解成 nil，
// 最终在前端展开节点时对 cases.map 触发整页白屏。旧协议不自动迁移，必须先同步部署 Hermes。
func validateTypedMockNode(node SfMockNodeView) error {
	if node.ResultSchema.Type == "" ||
		node.ResultSchema.Statuses == nil ||
		node.ResultSchema.RingStatuses == nil ||
		node.ResultSchema.Intentions == nil ||
		node.MatchSchema.Fields == nil ||
		node.MatchSchema.Operators == nil ||
		len(node.Config.Cases) == 0 ||
		node.Config.DefaultSelection == nil ||
		node.Config.Rules == nil ||
		node.Previews == nil {
		return fmt.Errorf(
			"Hermes StratFlow Mock 配置协议不兼容：节点 %s 未返回类型化 cases/defaultSelection/schema/previews；"+
				"请先部署配套 Hermes 后端并清理旧 sf:mock:cfg:* 配置",
			node.NodeID,
		)
	}
	return nil
}

// StratflowPutConfig 原子覆盖单节点完整配置（服务端校验 Case、规则、权重与回执窗口）。
func (c *Client) StratflowPutConfig(ctx context.Context, versionCode, nodeID string, cfg SfMockNodeConfig) error {
	path := "/openapi/mock/config/" + url.PathEscape(versionCode) + "/" + url.PathEscape(nodeID)
	return c.sfCall(ctx, "PUT", path, cfg, nil)
}

// StratflowDeleteConfig 删单节点自定义配置（回落服务端默认模板）。
func (c *Client) StratflowDeleteConfig(ctx context.Context, versionCode, nodeID string) error {
	path := "/openapi/mock/config/" + url.PathEscape(versionCode) + "/" + url.PathEscape(nodeID)
	return c.sfCall(ctx, "DELETE", path, nil, nil)
}

// StratflowClearMock 一键清空 mock 状态。plans 同时包含在途计划与 DONE 决策历史，必须由上游显式确认。
func (c *Client) StratflowClearMock(ctx context.Context, scope string, confirm bool) error {
	if scope == "" {
		scope = "all"
	}
	q := url.Values{"scope": {scope}, "confirm": {strconv.FormatBool(confirm)}}
	return c.sfCall(ctx, "DELETE", "/openapi/mock/all?"+q.Encode(), nil, nil)
}

// StratflowListPlans 查某 run 的 PENDING/DEAD mock 计划（最多 500 条）。
func (c *Client) StratflowListPlans(ctx context.Context, runCode string) ([]SfMockActionPlan, error) {
	return c.StratflowListPlansByStatus(ctx, runCode, "")
}

func (c *Client) StratflowListPlansByStatus(ctx context.Context, runCode, status string) ([]SfMockActionPlan, error) {
	var out []SfMockActionPlan
	q := url.Values{"runCode": {runCode}}
	if status != "" {
		q.Set("status", status)
	}
	err := c.sfCall(ctx, "GET", "/openapi/mock/plans?"+q.Encode(), nil, &out)
	return out, err
}

// StratflowListDecisions 分页查本次 run 每条名单的 Case 选择和实际变量/出口。
func (c *Client) StratflowListDecisions(ctx context.Context, runCode, status string, pageNo, pageSize int) (SfMockDecisionPage, error) {
	var out SfMockDecisionPage
	q := url.Values{
		"runCode":  {runCode},
		"pageNo":   {strconv.Itoa(pageNo)},
		"pageSize": {strconv.Itoa(pageSize)},
	}
	if status != "" {
		q.Set("status", status)
	}
	err := c.sfCall(ctx, "GET", "/openapi/mock/decisions?"+q.Encode(), nil, &out)
	return out, err
}

// StratflowRequeuePlan 将一条 DEAD 计划重新入队。
func (c *Client) StratflowRequeuePlan(ctx context.Context, actionCode string) error {
	return c.sfCall(ctx, "POST", "/openapi/mock/plans/"+url.PathEscape(actionCode)+"/requeue", nil, nil)
}

// ===== ② 发现（方案 / 版本 / 名单 / 字段 / 绑定 / run）=====

// StratflowWorkflows 授权且平台启用的方案列表。
func (c *Client) StratflowWorkflows(ctx context.Context) ([]SfWorkflow, error) {
	var out []SfWorkflow
	err := c.sfCall(ctx, "GET", "/openapi/mock/workflows", nil, &out)
	return out, err
}

// StratflowWorkflowDetail 方案详情（含 versionCode = 新 run 绑定版本）。
func (c *Client) StratflowWorkflowDetail(ctx context.Context, defCode string) (SfWorkflowDetail, error) {
	var v SfWorkflowDetail
	err := c.sfCall(ctx, "GET", "/openapi/mock/workflows/"+url.PathEscape(defCode), nil, &v)
	return v, err
}

// StratflowCollections 名单集合列表（可选 name 模糊 / status 过滤：1 启用 2 禁用）。
func (c *Client) StratflowCollections(ctx context.Context, name, status string) ([]SfCollection, error) {
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	if status != "" {
		q.Set("status", status)
	}
	path := "/openapi/mock/collections"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []SfCollection
	err := c.sfCall(ctx, "GET", path, nil, &out)
	return out, err
}

// StratflowCollectionFields 名单字段结构（拼 import rows）。
func (c *Client) StratflowCollectionFields(ctx context.Context, code string) ([]SfCollectionField, error) {
	var out []SfCollectionField
	err := c.sfCall(ctx, "GET", "/openapi/mock/collections/"+url.PathEscape(code)+"/fields", nil, &out)
	return out, err
}

// StratflowCollectionBindings 集合当前 active 绑定方案。
func (c *Client) StratflowCollectionBindings(ctx context.Context, code string) ([]SfBinding, error) {
	var out []SfBinding
	err := c.sfCall(ctx, "GET", "/openapi/mock/collections/"+url.PathEscape(code)+"/bindings", nil, &out)
	return out, err
}

// StratflowVersionRuns 某集合下按方案版本聚合的运行记录；code=versionCode，顶层无 batchCode。
func (c *Client) StratflowVersionRuns(ctx context.Context, code string, pageNumber, pageSize int) (SfVersionRunPage, error) {
	q := url.Values{
		"pageNumber": {strconv.Itoa(pageNumber)},
		"pageSize":   {strconv.Itoa(pageSize)},
	}
	path := "/openapi/mock/collections/" + url.PathEscape(code) + "/version-runs?" + q.Encode()
	var out SfVersionRunPage
	err := c.sfCall(ctx, "GET", path, nil, &out)
	return out, err
}

// StratflowExecutions 某集合下物理 execution 发现；Mock plan/decision 继续使用这里的 runCode。
func (c *Client) StratflowExecutions(ctx context.Context, code string) ([]SfRun, error) {
	var out []SfRun
	err := c.sfCall(ctx, "GET", "/openapi/mock/collections/"+url.PathEscape(code)+"/executions", nil, &out)
	return out, err
}

// StratflowExecutionProgress 单个物理 execution 进度（Mock 当前导入断言）。
// uploadStart/End = UTC "yyyy-MM-dd HH:mm:ss"，跨度 ≤31 天，必填。
func (c *Client) StratflowExecutionProgress(ctx context.Context, code, runCode, uploadStart, uploadEnd string) (SfRunProgress, error) {
	q := url.Values{}
	q.Set("uploadStartTime", uploadStart)
	q.Set("uploadEndTime", uploadEnd)
	path := fmt.Sprintf("/openapi/mock/collections/%s/executions/%s/progress?%s",
		url.PathEscape(code), url.PathEscape(runCode), q.Encode())
	var v SfRunProgress
	err := c.sfCall(ctx, "GET", path, nil, &v)
	return v, err
}

// StratflowVersionRunProgress 同一方案版本全部已提交物理 Run 的聚合进度。
// uploadStart/End 使用 ISO-8601 UTC instant，左闭右开，跨度最大 30 天。
func (c *Client) StratflowVersionRunProgress(
	ctx context.Context,
	code, defCode, versionCode, uploadStart, uploadEnd string,
) (SfVersionRunProgress, error) {
	q := url.Values{
		"uploadStart": {uploadStart},
		"uploadEnd":   {uploadEnd},
	}
	path := fmt.Sprintf("/openapi/mock/collections/%s/version-runs/%s/%s/progress?%s",
		url.PathEscape(code), url.PathEscape(defCode), url.PathEscape(versionCode), q.Encode())
	var v SfVersionRunProgress
	err := c.sfCall(ctx, "GET", path, nil, &v)
	return v, err
}

// ===== ③ 触发（导名单生成 run）=====

// StratflowImport 经 OpenAPI 导入名单触发 run（POST /openapi/collections/{code}/import）。
func (c *Client) StratflowImport(ctx context.Context, code string, req SfImportReq) (SfImportResult, error) {
	var v SfImportResult
	err := c.sfCall(ctx, "POST", "/openapi/collections/"+url.PathEscape(code)+"/import", req, &v)
	return v, err
}
