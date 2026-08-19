package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"hermes-mock/internal/entity"
	"hermes-mock/internal/httpmock"
)

const maxHTTPMockRequestBody = 1024 * 1024

func (d *Deps) listHTTPMocks(c *gin.Context) {
	rows := d.HTTPMock.List()
	for i := range rows {
		d.enrichHTTPMockURL(c, &rows[i])
	}
	c.JSON(http.StatusOK, gin.H{"endpoints": rows})
}

func (d *Deps) getHTTPMock(c *gin.Context) {
	id, ok := parseHTTPMockID(c)
	if !ok {
		return
	}
	endpoint, found := d.HTTPMock.GetByID(id)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "HTTP Mock endpoint 不存在"})
		return
	}
	d.enrichHTTPMockURL(c, endpoint)
	c.JSON(http.StatusOK, endpoint)
}

func (d *Deps) createHTTPMock(c *gin.Context) {
	var endpoint httpmock.Endpoint
	if err := c.ShouldBindJSON(&endpoint); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	endpoint.ID = 0
	endpoint.Token = ""
	out, err := d.HTTPMock.Upsert(endpoint)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	d.enrichHTTPMockURL(c, out)
	c.JSON(http.StatusCreated, out)
}

func (d *Deps) updateHTTPMock(c *gin.Context) {
	id, ok := parseHTTPMockID(c)
	if !ok {
		return
	}
	var endpoint httpmock.Endpoint
	if err := c.ShouldBindJSON(&endpoint); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	endpoint.ID = id
	out, err := d.HTTPMock.Upsert(endpoint)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	d.enrichHTTPMockURL(c, out)
	c.JSON(http.StatusOK, out)
}

func (d *Deps) deleteHTTPMock(c *gin.Context) {
	id, ok := parseHTTPMockID(c)
	if !ok {
		return
	}
	if err := d.HTTPMock.Delete(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (d *Deps) listHTTPMockRequests(c *gin.Context) {
	id, ok := parseHTTPMockID(c)
	if !ok {
		return
	}
	if _, found := d.HTTPMock.GetByID(id); !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "HTTP Mock endpoint 不存在"})
		return
	}
	rows, err := d.HTTPMock.ListRequests(entity.HTTPMockRequestFilter{
		EndpointID: id,
		Method:     c.Query("method"), MatchedRule: c.Query("matchedRule"),
		SelectedCase: c.Query("selectedCase"), Keyword: c.Query("keyword"),
		Limit: atoiDefault(c.Query("limit"), 200),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"requests": rows})
}

func (d *Deps) deleteHTTPMockRequests(c *gin.Context) {
	id, ok := parseHTTPMockID(c)
	if !ok {
		return
	}
	if c.Query("confirm") != "true" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "清空调用记录必须带 confirm=true"})
		return
	}
	n, err := d.HTTPMock.DeleteRequests(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": n})
}

func parseHTTPMockID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id 非法"})
		return 0, false
	}
	return id, true
}

func (d *Deps) enrichHTTPMockURL(c *gin.Context, endpoint *httpmock.Endpoint) {
	endpoint.InvokePath = "/mock/" + endpoint.Token
	base := d.httpMockBaseURL(c)
	if base != "" {
		endpoint.InvokeURL = base + endpoint.InvokePath
	}
}

// resolveConfirmURL 让群呼/call-bot 可直接传 /mock/{token}：有显式公开基地址时优先用它，
// 否则按当前控制台请求的 forwarded proto/host 补全。完整 URL 保持原样。
func (d *Deps) resolveConfirmURL(c *gin.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("confirmUrlBeforeDial 非法: %w", err)
	}
	if ref.IsAbs() {
		return raw, nil
	}
	if ref.Host == "" && !strings.HasPrefix(ref.Path, "/") {
		ref.Path = "/" + ref.Path
	}
	base := d.httpMockBaseURL(c)
	if base == "" {
		return "", fmt.Errorf("confirmUrlBeforeDial 为相对路径，但无法从当前请求推导域名")
	}
	baseURL, err := url.Parse(base + "/")
	if err != nil {
		return "", fmt.Errorf("HTTP Mock 基地址非法: %w", err)
	}
	return baseURL.ResolveReference(ref).String(), nil
}

func (d *Deps) httpMockBaseURL(c *gin.Context) string {
	base := strings.TrimRight(strings.TrimSpace(d.Cfg.HTTPMockPublicBaseURL), "/")
	if base == "" {
		proto := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0])
		if proto == "" {
			if c.Request.TLS != nil {
				proto = "https"
			} else {
				proto = "http"
			}
		}
		host := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Host"), ",")[0])
		if host == "" {
			host = c.Request.Host
		}
		if host != "" {
			base = proto + "://" + host
		}
	}
	return base
}

// invokeHTTPMock 执行通用 Mock 数据面。响应决策只读内存配置；调用记录在完成后异步入队。
func (d *Deps) invokeHTTPMock(c *gin.Context) {
	started := time.Now()
	receivedAt := started.UTC()
	endpoint, found := d.HTTPMock.GetByToken(c.Param("token"))
	if !found {
		c.String(http.StatusNotFound, "mock endpoint not found")
		return
	}
	if !endpoint.Enabled {
		d.recordHTTPMockRequest(c, endpoint, receivedAt, nil, httpmock.Decision{
			Response: httpmock.ResponseSpec{Action: httpmock.ActionRespond, Status: http.StatusNotFound, ContentType: "text/plain; charset=utf-8", Body: "mock endpoint disabled"},
		}, started, false)
		c.String(http.StatusNotFound, "mock endpoint disabled")
		return
	}
	if !endpoint.Config.MethodAllowed(c.Request.Method) {
		decision := httpmock.Decision{Response: httpmock.ResponseSpec{
			Action: httpmock.ActionRespond, Status: http.StatusMethodNotAllowed,
			ContentType: "text/plain; charset=utf-8", Body: "method not allowed",
		}}
		d.recordHTTPMockRequest(c, endpoint, receivedAt, nil, decision, started, false)
		c.String(http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxHTTPMockRequestBody)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			status = http.StatusRequestEntityTooLarge
		}
		decision := httpmock.Decision{Response: httpmock.ResponseSpec{
			Action: httpmock.ActionRespond, Status: status,
			ContentType: "text/plain; charset=utf-8", Body: err.Error(),
		}}
		d.recordHTTPMockRequest(c, endpoint, receivedAt, body, decision, started, false)
		c.String(status, err.Error())
		return
	}
	var jsonBody any
	if len(bytes.TrimSpace(body)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		_ = decoder.Decode(&jsonBody) // 非 JSON body 仍可由 rawBody 规则匹配。
	}
	request := httpmock.IncomingRequest{
		Method: c.Request.Method, Query: map[string][]string(c.Request.URL.Query()),
		Header: c.Request.Header.Clone(), RawBody: body, JSONBody: jsonBody,
	}
	decision, err := d.HTTPMock.Resolve(endpoint.ID, request)
	if err != nil {
		decision = httpmock.Decision{Response: httpmock.ResponseSpec{
			Action: httpmock.ActionRespond, Status: http.StatusBadRequest,
			ContentType: "text/plain; charset=utf-8", Body: err.Error(),
		}}
		d.recordHTTPMockRequest(c, endpoint, receivedAt, body, decision, started, false)
		c.String(http.StatusBadRequest, err.Error())
		return
	}

	response := decision.Response
	waitMs := response.DelayMs
	if response.Action == httpmock.ActionTimeout {
		waitMs = response.TimeoutMs
	}
	if waitMs > 0 {
		timer := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-c.Request.Context().Done():
			d.recordHTTPMockRequest(c, endpoint, receivedAt, body, decision, started, true)
			return
		case <-timer.C:
		}
	}
	d.writeHTTPMockResponse(c, response)
	d.recordHTTPMockRequest(c, endpoint, receivedAt, body, decision, started, false)
}

func (d *Deps) writeHTTPMockResponse(c *gin.Context, response httpmock.ResponseSpec) {
	for key, value := range response.Headers {
		c.Header(key, value)
	}
	if response.Status == http.StatusNoContent || response.Status == http.StatusNotModified {
		c.Status(response.Status)
		return
	}
	c.Data(response.Status, response.ContentType, []byte(response.Body))
}

func (d *Deps) recordHTTPMockRequest(c *gin.Context, endpoint *httpmock.Endpoint, receivedAt time.Time, body []byte, decision httpmock.Decision, started time.Time, canceled bool) {
	queryJSON, _ := json.Marshal(redactHTTPMockQuery(c.Request.URL.Query()))
	headersJSON, _ := json.Marshal(redactHTTPMockHeaders(c.Request.Header))
	responseHeadersJSON, _ := json.Marshal(decision.Response.Headers)
	overrideJSON, _ := json.Marshal(decision.Overrides)
	waitMs := decision.Response.DelayMs
	if decision.Response.Action == httpmock.ActionTimeout {
		waitMs = decision.Response.TimeoutMs
	}
	d.HTTPMock.RecordAsync(entity.HTTPMockRequest{
		EndpointID: endpoint.ID, Token: endpoint.Token, ReceivedAt: receivedAt,
		Remote: c.ClientIP(), Method: c.Request.Method, Path: c.Request.URL.Path,
		QueryJSON: string(queryJSON), HeadersJSON: string(headersJSON), RequestBody: string(body),
		MatchedRule: decision.MatchedRule, SelectedCase: decision.SelectedCase,
		SelectionMode: decision.SelectionMode, SelectedWeight: decision.SelectedWeight, TotalWeight: decision.TotalWeight,
		OverrideJSON:   string(overrideJSON),
		ResponseAction: decision.Response.Action, ResponseStatus: decision.Response.Status,
		ResponseHeadersJSON: string(responseHeadersJSON), ResponseBody: decision.Response.Body,
		DelayMs: waitMs, DurationMs: time.Since(started).Milliseconds(), ClientCanceled: canceled,
	})
}

func redactHTTPMockHeaders(headers http.Header) http.Header {
	copy := headers.Clone()
	for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-OpenApi-Key", "X-Api-Key", "Api-Key"} {
		if copy.Get(key) != "" {
			copy.Set(key, "[REDACTED]")
		}
	}
	return copy
}

func redactHTTPMockQuery(query url.Values) url.Values {
	out := make(url.Values, len(query))
	for key, values := range query {
		lower := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		switch lower {
		case "authorization", "api_key", "apikey", "access_token", "password", "secret":
			out[key] = []string{"[REDACTED]"}
		default:
			out[key] = append([]string(nil), values...)
		}
	}
	return out
}
