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
// 契约权威源：hermes 仓 docs/stratflow-mock-openapi-spec.md（已落地）。本文件只做 Go 侧消费：
//   - mock 管理：gate 开关 / per-node 结局配置 / 在途计划观测 / 一键清空（/openapi/mock/*）
//   - 发现：方案·版本·名单·字段·绑定·run 列表·run 进度（/openapi/mock/*，仅 mock-downstream.enabled 装配）
//   - 触发：导名单生成 run（/openapi/collections/{code}/import）
//
// 全部经 prodStratflow 产品前缀（gateway 模式 → /stratflow/**；direct 模式 → StratflowURL）。
// 边界：mock 后端不当被叫腿参与此路径——这是「应用层 mock 编排台」，与 SIP 被叫腿正交（见 docs/SCOPE.md）。

// ===== DTO（JSON 驼峰对齐 stratflow 实现）=====

// SfMockGateView 两层闸门视图（master 部署硬闸门 / global 运行期 / schemes 按方案覆盖 / deliveryPaused 回放暂停 / receiptWindowSec mock 回执窗压缩秒数）。
type SfMockGateView struct {
	Master           bool            `json:"master"`
	Global           bool            `json:"global"`
	Schemes          map[string]bool `json:"schemes"`
	DeliveryPaused   bool            `json:"deliveryPaused"`
	ReceiptWindowSec int64           `json:"receiptWindowSec"`
}

// SfMockOutcomeView 某触达节点某结局的展示视图（有效权重 = 配置覆盖 > 默认；steps=0 表示超时不回执）。
type SfMockOutcomeView struct {
	Key           string `json:"key"`
	Label         string `json:"label"`
	Weight        int    `json:"weight"`
	DefaultWeight int    `json:"defaultWeight"`
	Steps         int    `json:"steps"`
}

// SfMockNodeView 某触达节点的 mock 配置视图。
type SfMockNodeView struct {
	NodeID        string              `json:"nodeId"`
	Type          string              `json:"type"`
	Channel       *string             `json:"channel"`
	ForcedOutcome *string             `json:"forcedOutcome"`
	BaseDelayMs   int64               `json:"baseDelayMs"`
	Outcomes      []SfMockOutcomeView `json:"outcomes"`
}

// SfMockNodeConfig per-node mock 配置（写：weights 覆盖默认权重 / forcedOutcome 恒采 / baseDelayMs 叠加延迟）。
type SfMockNodeConfig struct {
	Weights       map[string]int `json:"weights"`
	ForcedOutcome *string        `json:"forcedOutcome"`
	BaseDelayMs   int64          `json:"baseDelayMs"`
}

// SfMockStep 一步回执（回放器据 delayMs 排到点）。
type SfMockStep struct {
	DelayMs       int64          `json:"delayMs"`
	Status        string         `json:"status"`
	FailureReason *string        `json:"failureReason"`
	Data          map[string]any `json:"data"`
}

// SfMockActionPlan per-action 在途计划（双发闸门：计划存在=本动作由 mock 派发）。
type SfMockActionPlan struct {
	ActionCode string       `json:"actionCode"`
	RunCode    string       `json:"runCode"`
	NodeID     string       `json:"nodeId"`
	EntryCode  string       `json:"entryCode"`
	Channel    string       `json:"channel"`
	OrgCode    string       `json:"orgCode"`
	OutcomeKey string       `json:"outcomeKey"`
	BaseMs     int64        `json:"baseMs"`
	Idx        int          `json:"idx"`
	Steps      []SfMockStep `json:"steps"`
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

// SfRun run 列表/进度里的漏斗汇总。code = runCode。
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
	Run   SfRun       `json:"run"`
	Nodes []SfRunNode `json:"nodes"`
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

// SfImportResult 导入响应（批次顶层字段 + plans）。取 result==1 的 plans[].runCode 进入断言。
type SfImportResult struct {
	Code           string         `json:"code"`
	BatchCode      string         `json:"batchCode"`
	CollectionCode string         `json:"collectionCode"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Status         int            `json:"status"`
	Total          int            `json:"total"`
	Success        int            `json:"success"`
	Fail           int            `json:"fail"`
	Plans          []SfImportPlan `json:"plans"`
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

// StratflowSetGlobalGate 全局 mock 开关（仅影响后续新派发）。
func (c *Client) StratflowSetGlobalGate(ctx context.Context, enabled bool) (SfMockGateView, error) {
	var v SfMockGateView
	err := c.sfCall(ctx, "PUT", "/openapi/mock/gate/global?enabled="+strconv.FormatBool(enabled), nil, &v)
	return v, err
}

// StratflowSetSchemeGate 按方案覆盖 mock 开关（优先级高于 global）。
func (c *Client) StratflowSetSchemeGate(ctx context.Context, defCode string, enabled bool) (SfMockGateView, error) {
	var v SfMockGateView
	path := "/openapi/mock/gate/scheme/" + url.PathEscape(defCode) + "?enabled=" + strconv.FormatBool(enabled)
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

// StratflowListConfig 列某发布版本各触达节点的可 mock 结局词表 + 当前有效权重。
func (c *Client) StratflowListConfig(ctx context.Context, versionCode string) ([]SfMockNodeView, error) {
	var out []SfMockNodeView
	err := c.sfCall(ctx, "GET", "/openapi/mock/config/"+url.PathEscape(versionCode), nil, &out)
	return out, err
}

// StratflowPutConfig 写单节点 mock 配置（服务端校验结局键/权重/baseDelayMs 窗口）。
func (c *Client) StratflowPutConfig(ctx context.Context, versionCode, nodeID string, cfg SfMockNodeConfig) error {
	path := "/openapi/mock/config/" + url.PathEscape(versionCode) + "/" + url.PathEscape(nodeID)
	return c.sfCall(ctx, "PUT", path, cfg, nil)
}

// StratflowDeleteConfig 删单节点 mock 配置（回落默认权重）。
func (c *Client) StratflowDeleteConfig(ctx context.Context, versionCode, nodeID string) error {
	path := "/openapi/mock/config/" + url.PathEscape(versionCode) + "/" + url.PathEscape(nodeID)
	return c.sfCall(ctx, "DELETE", path, nil, nil)
}

// StratflowClearMock 一键清空 mock 状态。scope: all|plans|config（不动 gate 开关）。
func (c *Client) StratflowClearMock(ctx context.Context, scope string) error {
	if scope == "" {
		scope = "all"
	}
	return c.sfCall(ctx, "DELETE", "/openapi/mock/all?scope="+url.QueryEscape(scope), nil, nil)
}

// StratflowListPlans 查某 run 的在途 mock 采样计划（最多 500 条）。
func (c *Client) StratflowListPlans(ctx context.Context, runCode string) ([]SfMockActionPlan, error) {
	var out []SfMockActionPlan
	err := c.sfCall(ctx, "GET", "/openapi/mock/plans?runCode="+url.QueryEscape(runCode), nil, &out)
	return out, err
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

// StratflowRuns 某集合下 run 列表（最近创建在前；code=runCode）。
func (c *Client) StratflowRuns(ctx context.Context, code string) ([]SfRun, error) {
	var out []SfRun
	err := c.sfCall(ctx, "GET", "/openapi/mock/collections/"+url.PathEscape(code)+"/runs", nil, &out)
	return out, err
}

// StratflowRunProgress run 进度（断言分支）。uploadStart/End = UTC "yyyy-MM-dd HH:mm:ss"，跨度 ≤31 天，必填。
func (c *Client) StratflowRunProgress(ctx context.Context, code, runCode, uploadStart, uploadEnd string) (SfRunProgress, error) {
	q := url.Values{}
	q.Set("uploadStartTime", uploadStart)
	q.Set("uploadEndTime", uploadEnd)
	path := fmt.Sprintf("/openapi/mock/collections/%s/runs/%s/progress?%s",
		url.PathEscape(code), url.PathEscape(runCode), q.Encode())
	var v SfRunProgress
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
