package hermesopenapi

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ===== basic：坐席 OpenAPI（/openapi/agent）=====

// Agent 坐席（OpenAPI page 返回的子集）。
type Agent struct {
	AgentCode         string   `json:"agentCode"`
	AgentName         string   `json:"agentName"`
	Number            string   `json:"number"`
	Password          string   `json:"password,omitempty"`
	OrgCode           string   `json:"orgCode"`
	DepCode           string   `json:"depCode"`
	AgentGroupCode    string   `json:"agentGroupCode"`
	AgentRoleCodeList []string `json:"agentRoleCodeList,omitempty"`
	CallProcessTime   int      `json:"callProcessTime,omitempty"`
	Remark            string   `json:"remark,omitempty"`
	State             any      `json:"state,omitempty"`
	CallBarState      any      `json:"callBarState,omitempty"`
	Status            any      `json:"status"`
}

type agentPage struct {
	Records []Agent `json:"records"`
	Total   int64   `json:"total"`
}

type AgentFilter struct {
	AgentName      string
	Number         string
	AgentGroupCode string
	DepCode        string
	Status         string
}

// ListAgents 分页查机构坐席（POST /openapi/agent/page）。
func (c *Client) ListAgents(ctx context.Context, pageNum, pageSize int) ([]Agent, int64, error) {
	return c.ListAgentsWithFilter(ctx, pageNum, pageSize, AgentFilter{})
}

// ListAgentsWithFilter 分页查机构坐席，筛选条件透传 Hermes AgentSearchReq。
func (c *Client) ListAgentsWithFilter(ctx context.Context, pageNum, pageSize int, f AgentFilter) ([]Agent, int64, error) {
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	body := map[string]any{"pageNum": pageNum, "pageSize": pageSize}
	if f.AgentName != "" {
		body["agentName"] = f.AgentName
	}
	if f.Number != "" {
		body["number"] = f.Number
	}
	if f.AgentGroupCode != "" {
		body["agentGroupCode"] = f.AgentGroupCode
	}
	if f.DepCode != "" {
		body["depCode"] = f.DepCode
	}
	if f.Status != "" {
		body["status"] = f.Status
	}
	data, err := c.call(ctx, prodBasic, "POST", "/openapi/agent/page", body)
	if err != nil {
		return nil, 0, err
	}
	var p agentPage
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, 0, err
	}
	return p.Records, p.Total, nil
}

// BatchAddAgentsReq 批量建坐席（POST /openapi/agent/batch/add，对照 BatchAddAgentReq）。
type BatchAddAgentsReq struct {
	AddQuantity   int    `json:"addQuantity"`
	AgentRoleCode string `json:"agentRoleCode,omitempty"`
	Status        string `json:"status,omitempty"` // ENABLED/DISABLED
}

// BatchAddAgents 批量创建真实坐席（写 t_agent 由 Hermes 完成，mock 只调 OpenAPI）。
func (c *Client) BatchAddAgents(ctx context.Context, req BatchAddAgentsReq) (json.RawMessage, error) {
	if req.Status == "" {
		req.Status = "ENABLED"
	}
	return c.call(ctx, prodBasic, "POST", "/openapi/agent/batch/add", req)
}

// AddAgentReq 单个建坐席（POST /openapi/agent/add）。
//
// 坐席号（number）由 Hermes 服务端自动生成（getNextAgentNumbers），本接口不接收 number。
// Hermes 的 Kotlin DTO AddAgentReq 里 depCode / agentRoleCode 是「无默认值」的可空构造参数：
// Jackson-Kotlin 反序列化时这两个 key 缺失会抛 HttpMessageNotReadableException
// → 全局异常处理回 code=1000「Parameters are missing」。因此 depCode / agentRoleCode
// 必须始终下发 key（即便为空串），不能加 omitempty。password 为必填非空。
type AddAgentReq struct {
	AgentName       string `json:"agentName,omitempty"`
	Password        string `json:"password"`
	AgentGroupCode  string `json:"agentGroupCode,omitempty"`
	DepCode         string `json:"depCode"`       // 无默认值的可空参数：必须始终下发 key
	AgentRoleCode   string `json:"agentRoleCode"` // 无默认值的可空参数：必须始终下发 key
	PhoneCode       string `json:"phoneCode,omitempty"`
	CallProcessTime int    `json:"callProcessTime,omitempty"`
	// Status = 账号启用状态 StatusEnum（@JsonValue code：0=DISABLED / 1=ENABLED），**不是**工作态(ONLINE/RESTING/BUSY)。
	// 必须传数字（1/0）——传字符串 "ENABLED" 会反序列化失败、被全局异常回 code=1000「Parameters are missing」。
	// 用 *int：nil 不下发 → Hermes 默认 ENABLED；前端选启用/停用则下发 1/0。
	Status *int   `json:"status,omitempty"`
	Remark string `json:"remark,omitempty"`
}

// AddAgent 单建坐席。
func (c *Client) AddAgent(ctx context.Context, req AddAgentReq) (Agent, error) {
	data, err := c.call(ctx, prodBasic, "POST", "/openapi/agent/add", req)
	if err != nil {
		return Agent{}, err
	}
	var a Agent
	if err := json.Unmarshal(data, &a); err != nil {
		return Agent{}, err
	}
	return a, nil
}

// UpdateAgentReq 修改坐席（PUT /openapi/agent/update）。AgentNumber 是真实分机号。
type UpdateAgentReq struct {
	AgentNumber     string `json:"agentNumber"`
	AgentName       string `json:"agentName,omitempty"`
	DepCode         string `json:"depCode,omitempty"`
	AgentRoleCode   string `json:"agentRoleCode,omitempty"`
	CallProcessTime int    `json:"callProcessTime,omitempty"`
	Status          string `json:"status,omitempty"` // ENABLED/DISABLED
	AgentGroupCode  string `json:"agentGroupCode,omitempty"`
}

// UpdateAgent 修改真实 Hermes 坐席。
func (c *Client) UpdateAgent(ctx context.Context, req UpdateAgentReq) error {
	_, err := c.call(ctx, prodBasic, "PUT", "/openapi/agent/update", req)
	return err
}

// DeleteAgent 删坐席（DELETE /openapi/agent/delete/{number}）。
func (c *Client) DeleteAgent(ctx context.Context, number string) error {
	_, err := c.call(ctx, prodBasic, "DELETE", "/openapi/agent/delete/"+number, nil)
	return err
}

// SetAgentEnabled 经 Hermes basic 管理接口启停坐席。Hermes 删除启用坐席前要求先停用。
func (c *Client) SetAgentEnabled(ctx context.Context, agentCodes []string, enabled bool) error {
	status := 0
	if enabled {
		status = 1
	}
	_, err := c.call(ctx, prodBasic, "PUT", "/agent/batchUpdateAgentStatus", map[string]any{
		"agentCodes": agentCodes,
		"status":     status,
	})
	return err
}

// TtsVoice 一个可用 TTS 模板（OpenAPI available 返回的子集）。
type TtsVoice struct {
	TtsCode string `json:"ttsCode"`
	Name    string `json:"name"`
	Lang    string `json:"lang"`
}

type ttsPage struct {
	Records []map[string]any `json:"records"`
}

// ListTts 查机构可用 TTS 模板（POST /openapi/tts/available），用于群呼表单联动选择。
// 字段名各版本可能不同，这里宽松映射 ttsCode/code + name。
func (c *Client) ListTts(ctx context.Context) ([]TtsVoice, error) {
	data, err := c.call(ctx, prodBasic, "POST", "/openapi/tts/available", map[string]any{"pageNum": 1, "pageSize": 100})
	if err != nil {
		return nil, err
	}
	var p ttsPage
	if json.Unmarshal(data, &p) != nil {
		// data 可能直接是数组
		var arr []map[string]any
		if json.Unmarshal(data, &arr) == nil {
			p.Records = arr
		}
	}
	out := make([]TtsVoice, 0, len(p.Records))
	for _, r := range p.Records {
		v := TtsVoice{}
		for _, k := range []string{"ttsCode", "code", "voiceCode"} {
			if s, ok := r[k].(string); ok && s != "" {
				v.TtsCode = s
				break
			}
		}
		for _, k := range []string{"name", "ttsName", "voiceName"} {
			if s, ok := r[k].(string); ok && s != "" {
				v.Name = s
				break
			}
		}
		if s, ok := r["lang"].(string); ok {
			v.Lang = s
		}
		if v.TtsCode != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

// ===== call-center：群呼任务 + 对话 =====

// CreateCallCenterTask 创建并导入号码（POST /openapi/task/createAndImport）。
func (c *Client) CreateCallCenterTask(ctx context.Context, body any) (json.RawMessage, error) {
	return c.call(ctx, prodCallCenter, "POST", "/openapi/task/createAndImport", body)
}

// StartCallCenterTask 启动任务（POST /openapi/task/status/start/{taskCode}）。
func (c *Client) StartCallCenterTask(ctx context.Context, taskCode string) (json.RawMessage, error) {
	return c.call(ctx, prodCallCenter, "POST", "/openapi/task/status/start/"+taskCode, map[string]any{})
}

// ConvMessage 一条对话消息（ASR/TTS/系统），字段宽松映射。
type ConvMessage struct {
	Role    string `json:"role"`    // robot/customer/agent/system
	Type    string `json:"type"`    // ASR/TTS/...
	Content string `json:"content"` // 文本
	Time    string `json:"time"`
}

// GetConversation 拉取一通通话的对话消息（GET /openapi/conversation/{callUuid}/messages）。
func (c *Client) GetConversation(ctx context.Context, callUUID string) (json.RawMessage, error) {
	return c.call(ctx, prodCallCenter, "GET", "/openapi/conversation/"+callUUID+"/messages", nil)
}

// AgentStatus 真实坐席状态（call-center /openapi/agent/info 子集）。
type AgentStatus struct {
	AgentNumber string `json:"agentNumber"`
	AgentName   string `json:"agentName"`
	CallStatus  any    `json:"callStatus"`
	Department  string `json:"department"`
}

// GetAgentStatus 查一组坐席的真实 Hermes 状态（POST call-center /openapi/agent/info）。
// Hermes call-center 单次 agents 参数最多 100 条，这里分片避免列表页轮询打出校验错误日志。
func (c *Client) GetAgentStatus(ctx context.Context, numbers []string) (map[string]AgentStatus, error) {
	if len(numbers) == 0 {
		return map[string]AgentStatus{}, nil
	}
	const batchSize = 100
	out := make(map[string]AgentStatus, len(numbers))
	for start := 0; start < len(numbers); start += batchSize {
		end := start + batchSize
		if end > len(numbers) {
			end = len(numbers)
		}
		data, err := c.call(ctx, prodCallCenter, "POST", "/openapi/agent/info", map[string]any{"agents": numbers[start:end]})
		if err != nil {
			return nil, err
		}
		var arr []AgentStatus
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil, err
		}
		for _, a := range arr {
			out[a.AgentNumber] = a
		}
	}
	return out, nil
}

// ===== call-bot：自动外呼 + 通话轨迹 =====

// AutoCall 直接自动外呼（POST /openapi/autocall/originate）。
func (c *Client) AutoCall(ctx context.Context, body any) (json.RawMessage, error) {
	return c.call(ctx, prodCallBot, "POST", "/openapi/autocall/originate", body)
}

// CreateCallBotTask call-bot 建任务并导入（POST /openapi/task/create-and-import）。
func (c *Client) CreateCallBotTask(ctx context.Context, body any) (json.RawMessage, error) {
	return c.call(ctx, prodCallBot, "POST", "/openapi/task/create-and-import", body)
}

// GetCallBotTrace call-bot 通话轨迹（GET /openapi/call-trace/{callUuid}）——含机器人对话。
func (c *Client) GetCallBotTrace(ctx context.Context, callUUID string) (json.RawMessage, error) {
	return c.call(ctx, prodCallBot, "GET", "/openapi/call-trace/"+callUUID, nil)
}

// Ping 用一个轻量 OpenAPI 调用验证机构凭据是否可用（坐席分页 1 条）。
func (c *Client) Ping(ctx context.Context) error {
	_, _, err := c.ListAgents(ctx, 1, 1)
	return err
}

// ===== otp：语音验证码 =====

// SendOTP 经 hermes-otp 下发语音验证码（POST /otp/openapi/send）。
func (c *Client) SendOTP(ctx context.Context, to, templateCode string, params map[string]string) (json.RawMessage, error) {
	body := map[string]any{"to": to, "templateCode": templateCode, "encrypted": false}
	if params != nil {
		body["params"] = params
	}
	return c.call(ctx, "otp", "POST", "/openapi/send", body)
}

// ===== 坐席工作台操作（agent-workbench SDK；direct 模式注入 ORG+AGENT 身份头）=====

// AgentWorkbench 经坐席工作台 SDK 触发坐席操作。path 形如 /agent-workbench/sdk/agent/call/transfer。
// 注意：这是 SDK 而非 /openapi/，direct 模式额外注入 AGENT_NUMBER_KEY/AGENT_CODE_KEY。
func (c *Client) AgentWorkbench(ctx context.Context, agentNumber, path string, body any) (json.RawMessage, error) {
	urlStr, headers, err := c.endpoint(prodCallCenter, path)
	if err != nil {
		return nil, err
	}
	if c.cred.Mode != "gateway" {
		headers[hdrAgentNumber] = agentNumber
		headers[hdrAgentCode] = agentNumber
	}
	return c.callWith(ctx, "POST", urlStr, headers, body)
}

// AgentStatusCode 对齐 call-center AgentStatusEnum 的 @JsonValue code。
func AgentStatusCode(status string) (int, bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "OFFLINE":
		return 1, true
	case "ONLINE":
		return 2, true
	case "RINGING":
		return 3, true
	case "CALLING":
		return 4, true
	case "DIALING":
		return 5, true
	case "RESTING":
		return 6, true
	case "BUSY":
		return 7, true
	case "WRAP_UP":
		return 8, true
	case "AUTO_OUTBOUND":
		return 9, true
	default:
		return 0, false
	}
}

// AgentStatusName 把 call-center AgentStatusEnum code 还原为前端展示枚举。
func AgentStatusName(code int) string {
	switch code {
	case 1:
		return "OFFLINE"
	case 2:
		return "ONLINE"
	case 3:
		return "RINGING"
	case 4:
		return "CALLING"
	case 5:
		return "DIALING"
	case 6:
		return "RESTING"
	case 7:
		return "BUSY"
	case 8:
		return "WRAP_UP"
	case 9:
		return "AUTO_OUTBOUND"
	default:
		return ""
	}
}

// SwitchAgentStatus 经工作台 SDK 切换坐席工作状态。Hermes 接口要求 action 为 AgentStatusEnum code。
func (c *Client) SwitchAgentStatus(ctx context.Context, agentNumber, status string) (json.RawMessage, error) {
	code, ok := AgentStatusCode(status)
	if !ok {
		return nil, fmt.Errorf("未知 Hermes 坐席状态 %q", status)
	}
	return c.AgentWorkbench(ctx, agentNumber, "/agent-workbench/sdk/agent/status/switch", map[string]any{"action": code})
}

// PrepareAgentPhoneReady 触发 call-center 的 SIP 鉴权 ready 条件。
//
// Hermes 当前 agentIsReady 同时要求工作台 WS 在线与电话端 SIP ready；这里按 /public/auth/sip
// 的 digest 规则提交一次 REGISTER 鉴权，不直写状态。
func (c *Client) PrepareAgentPhoneReady(ctx context.Context, agentNumber, password string) error {
	agentNumber = strings.TrimSpace(agentNumber)
	if agentNumber == "" || password == "" {
		return fmt.Errorf("需提供坐席号和口令")
	}
	realm := "hermes-mock"
	nonce := fmt.Sprintf("%d", time.Now().UnixNano())
	uri := "sip:" + agentNumber + "@hermes"
	resp := sipDigestResponse(agentNumber, password, realm, nonce, uri)

	urlStr, headers, err := c.endpoint(prodCallCenter, "/public/auth/sip")
	if err != nil {
		return err
	}
	body := map[string]any{
		"authorization": "",
		"algorithm":     "MD5",
		"username":      agentNumber,
		"realm":         realm,
		"nonce":         nonce,
		"uri":           uri,
		"response":      resp,
	}
	raw, err := c.callPlain(ctx, "POST", urlStr, headers, body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != "1" {
		return fmt.Errorf("Hermes SIP ready 鉴权失败: %s", clip(string(raw), 200))
	}
	return nil
}

func sipDigestResponse(username, password, realm, nonce, uri string) string {
	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex("REGISTER:" + uri)
	return md5Hex(ha1 + ":" + nonce + ":" + ha2)
}

func md5Hex(s string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(s)))
}
