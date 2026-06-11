// Package hermesopenapi 是 mock 调用 Hermes 的**唯一**正当通道：只走 OpenAPI/SDK，
// 绝不直接读写 Hermes 任何数据库表。
//
// 鉴权对照真实网关 hermes-gateway/OpenApiAuthFilter：
//   - 网关模式(gateway)：请求带 X-OpenApi-Key，网关查 t_secret_key 得机构，注入 ORG_CODE_KEY/ORG_NAME_KEY 转发。
//     mock 配置 {gatewayURL, apiKey} 即可，路径形如 /{product}/openapi/**。
//   - 直连模式(direct)：本地无网关时，直接打服务的 /openapi/**，由 mock 注入网关本会注入的
//     ORG_CODE_KEY/ORG_NAME_KEY 头（服务只读头、不二次校验）。mock 配置各服务 baseURL + orgCode。
//
// 机构密钥/地址由「机构配置」维护，切换机构即切换这套凭据。
package hermesopenapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Hermes 网关/服务的身份头（对照 common CommonConstant）。
const (
	hdrOrgCode     = "ORG_CODE_KEY"
	hdrOrgName     = "ORG_NAME_KEY"
	hdrUserCode    = "USER_CODE_KEY"
	hdrUserName    = "USER_NAME_KEY"
	hdrAgentNumber = "AGENT_NUMBER_KEY"
	hdrAgentCode   = "AGENT_CODE_KEY"
	hdrOpenAPIKey  = "X-OpenApi-Key"

	// 网关产品前缀（OpenApiAuthFilter PRODUCT_PATH_MAPPING / 路由）。
	prodBasic      = "basic"
	prodCallCenter = "call-center"
	prodCallBot    = "call-bot"
	prodOTP        = "otp"
)

// Cred 一套机构的 OpenAPI 接入凭据（来自「机构配置」）。
type Cred struct {
	OrgCode  string `json:"orgCode"`
	OrgName  string `json:"orgName"`
	UserCode string `json:"userCode"` // 直连模式下注入的操作人（审计用，可空）
	Mode     string `json:"mode"`     // "gateway" | "direct"
	// gateway 模式
	GatewayURL string `json:"gatewayUrl"`
	APIKey     string `json:"apiKey"`
	// direct 模式（本地无网关，直连各服务）
	BasicURL      string `json:"basicUrl"`
	CallCenterURL string `json:"callCenterUrl"`
	CallBotURL    string `json:"callBotUrl"`
	OTPURL        string `json:"otpUrl"`
}

// Client 针对一套机构凭据的 OpenAPI 客户端。
type Client struct {
	cred Cred
	http *http.Client
}

func New(cred Cred) *Client {
	// 60s：包住 orchestrator 的 45s ctx（http 层不应先于 ctx 超时）；call-center 过载时重接口慢。
	return &Client{cred: cred, http: &http.Client{Timeout: 60 * time.Second}}
}

// Resp Hermes 统一响应包络（code=0 成功）。
type Resp struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// endpoint 按模式拼 URL + 头。product=basic/call-center/call-bot；path 形如 /openapi/agent/page。
func (c *Client) endpoint(product, path string) (urlStr string, headers map[string]string, err error) {
	headers = map[string]string{"Content-Type": "application/json"}
	if c.cred.Mode == "gateway" {
		if c.cred.GatewayURL == "" || c.cred.APIKey == "" {
			return "", nil, fmt.Errorf("网关模式需配置 gatewayUrl + apiKey")
		}
		headers[hdrOpenAPIKey] = c.cred.APIKey
		return strings.TrimRight(c.cred.GatewayURL, "/") + "/" + product + path, headers, nil
	}
	// direct 模式：注入网关本会注入的身份头
	if c.cred.OrgCode == "" {
		return "", nil, fmt.Errorf("直连模式需配置 orgCode")
	}
	headers[hdrOrgCode] = c.cred.OrgCode
	headers[hdrOrgName] = url.QueryEscape(orStr(c.cred.OrgName, c.cred.OrgCode))
	if c.cred.UserCode != "" {
		headers[hdrUserCode] = c.cred.UserCode
		headers[hdrUserName] = url.QueryEscape(c.cred.UserCode)
	}
	var base string
	switch product {
	case prodBasic:
		base = c.cred.BasicURL
	case prodCallCenter:
		base = c.cred.CallCenterURL
	case prodCallBot:
		base = c.cred.CallBotURL
	case prodOTP:
		base = c.cred.OTPURL
	}
	if base == "" {
		return "", nil, fmt.Errorf("直连模式未配置 %s 服务地址", product)
	}
	return strings.TrimRight(base, "/") + path, headers, nil
}

// call 执行一次 OpenAPI 调用，校验包络 code==0，返回 data 原文。
func (c *Client) call(ctx context.Context, product, method, path string, body any) (json.RawMessage, error) {
	urlStr, headers, err := c.endpoint(product, path)
	if err != nil {
		return nil, err
	}
	return c.callWith(ctx, method, urlStr, headers, body)
}

// callWith 用给定 url+headers 执行请求并校验统一包络（供 call 与 AgentWorkbench 复用）。
func (c *Client) callWith(ctx context.Context, method, urlStr string, headers map[string]string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, rdr)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 %s 失败: %w", urlStr, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, clip(string(raw), 200))
	}
	var env Resp
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("响应非标准包络: %s", clip(string(raw), 200))
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("业务失败 code=%d: %s", env.Code, env.Msg)
	}
	return env.Data, nil
}

func (c *Client) callPlain(ctx context.Context, method, urlStr string, headers map[string]string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, rdr)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 %s 失败: %w", urlStr, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, clip(string(raw), 200))
	}
	return raw, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
