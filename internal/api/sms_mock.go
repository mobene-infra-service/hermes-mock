package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"hermes-mock/internal/entity"
	"hermes-mock/internal/smsmock"
)

const maxSMSMockRequestBody = 1024 * 1024

func (d *Deps) listSMSMockProviders(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"providers": d.SMSMock.ProviderInfos()})
}

func (d *Deps) listSMSMocks(c *gin.Context) {
	rows := d.SMSMock.List()
	for i := range rows {
		d.enrichSMSMockURL(c, &rows[i])
	}
	c.JSON(http.StatusOK, gin.H{"endpoints": rows})
}

func (d *Deps) getSMSMock(c *gin.Context) {
	id, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	endpoint, found := d.SMSMock.GetByID(id)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "SMS Mock endpoint 不存在"})
		return
	}
	d.enrichSMSMockURL(c, endpoint)
	c.JSON(http.StatusOK, endpoint)
}

func (d *Deps) createSMSMock(c *gin.Context) {
	var endpoint smsmock.Endpoint
	if err := c.ShouldBindJSON(&endpoint); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	endpoint.ID, endpoint.Token = 0, ""
	out, err := d.SMSMock.Upsert(endpoint)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	d.enrichSMSMockURL(c, out)
	c.JSON(http.StatusCreated, out)
}

func (d *Deps) updateSMSMock(c *gin.Context) {
	id, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	var endpoint smsmock.Endpoint
	if err := c.ShouldBindJSON(&endpoint); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	endpoint.ID = id
	out, err := d.SMSMock.Upsert(endpoint)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	d.enrichSMSMockURL(c, out)
	c.JSON(http.StatusOK, out)
}

func (d *Deps) deleteSMSMock(c *gin.Context) {
	id, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	if err := d.SMSMock.Delete(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (d *Deps) listSMSMockMessages(c *gin.Context) {
	id, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	if _, found := d.SMSMock.GetByID(id); !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "SMS Mock endpoint 不存在"})
		return
	}
	rows, err := d.SMSMock.ListMessages(entity.SMSMockMessageFilter{
		EndpointID: id, Reference: c.Query("reference"), Recipient: c.Query("recipient"),
		SelectedCase: c.Query("selectedCase"), ReceiptStatus: c.Query("receiptStatus"),
		Keyword: c.Query("keyword"), Limit: atoiDefault(c.Query("limit"), 200),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": rows})
}

func (d *Deps) deleteSMSMockMessages(c *gin.Context) {
	id, ok := parsePositiveID(c, "id")
	if !ok {
		return
	}
	if _, found := d.SMSMock.GetByID(id); !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "SMS Mock endpoint 不存在"})
		return
	}
	if c.Query("confirm") != "true" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "清空短信记录必须带 confirm=true"})
		return
	}
	n, err := d.SMSMock.DeleteMessages(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": n})
}

func (d *Deps) listSMSMockAttempts(c *gin.Context) {
	messageID, ok := parsePositiveID(c, "messageId")
	if !ok {
		return
	}
	if _, err := d.SMSMock.GetMessage(messageID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "SMS Mock message 不存在"})
		return
	}
	rows, err := d.SMSMock.ListAttempts(messageID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"attempts": rows})
}

func (d *Deps) enqueueSMSMockCallback(c *gin.Context) {
	messageID, ok := parsePositiveID(c, "messageId")
	if !ok {
		return
	}
	row, err := d.SMSMock.EnqueueCallback(messageID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, row)
}

func (d *Deps) cancelSMSMockCallback(c *gin.Context) {
	messageID, ok := parsePositiveID(c, "messageId")
	if !ok {
		return
	}
	if err := d.SMSMock.CancelCallback(messageID); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func parsePositiveID(c *gin.Context, param string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(param), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": param + " 非法"})
		return 0, false
	}
	return id, true
}

func (d *Deps) enrichSMSMockURL(c *gin.Context, endpoint *smsmock.Endpoint) {
	endpoint.InvokePath = "/sms-mock/" + strings.ToLower(endpoint.Provider) + "/" + endpoint.Token
	base := strings.TrimRight(strings.TrimSpace(d.Cfg.SMSMockPublicBaseURL), "/")
	if base == "" {
		base = d.httpMockBaseURL(c)
	}
	if base != "" {
		endpoint.InvokeURL = base + endpoint.InvokePath
	}
}

func (d *Deps) invokeSMSMock(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSMSMockRequestBody)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			status = http.StatusRequestEntityTooLarge
		}
		d.writeSMSMockProtocolError(c, c.Param("provider"), status, err)
		return
	}
	invocation, err := d.SMSMock.PrepareInvocation(c.Request.Context(), smsmock.InvokeRequest{
		Provider: c.Param("provider"), Token: c.Param("token"), Method: c.Request.Method,
		Query: map[string][]string(c.Request.URL.Query()), Header: c.Request.Header.Clone(),
		Body: body, Remote: c.ClientIP(),
	})
	if err != nil {
		var invokeErr *smsmock.InvokeError
		if errors.As(err, &invokeErr) {
			d.writeSMSMockResponse(c, invokeErr.Response)
			return
		}
		d.writeSMSMockProtocolError(c, c.Param("provider"), http.StatusInternalServerError, err)
		return
	}
	response := invocation.Response
	waitMs := response.DelayMs
	if response.Action == smsmock.SubmitActionTimeout {
		waitMs = response.TimeoutMs
	}
	if waitMs > 0 {
		timer := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-c.Request.Context().Done():
			d.completeSMSMockInBackground(invocation, entity.SMSMockSubmitClientCanceled, false)
			return
		case <-timer.C:
		}
	}
	if response.Action == smsmock.SubmitActionTimeout {
		d.completeSMSMockInBackground(invocation, entity.SMSMockSubmitTimedOut, false)
		return
	}
	// 先激活持久化 DLR 再返回 Accepted：若状态推进失败，返回协议错误而不是制造“提交成功但永不回调”。
	completeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = d.SMSMock.CompleteSubmission(completeCtx, invocation, entity.SMSMockSubmitResponded, true)
	cancel()
	if err != nil {
		d.writeSMSMockProtocolError(c, c.Param("provider"), http.StatusInternalServerError, fmt.Errorf("激活 DLR 失败: %w", err))
		return
	}
	d.writeSMSMockResponse(c, response)
}

func (d *Deps) completeSMSMockInBackground(invocation *smsmock.Invocation, state string, activate bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.SMSMock.CompleteSubmission(ctx, invocation, state, activate); err != nil {
		logrus.WithError(err).WithField("state", state).Error("smsmock: 更新提交状态失败")
	}
}

func (d *Deps) writeSMSMockProtocolError(c *gin.Context, provider string, status int, cause error) {
	if response, ok := d.SMSMock.ProtocolError(provider, status, cause); ok {
		d.writeSMSMockResponse(c, response)
		return
	}
	c.JSON(status, gin.H{"error": cause.Error()})
}

func (d *Deps) writeSMSMockResponse(c *gin.Context, response smsmock.WireResponse) {
	status := response.HTTPStatus
	if status == 0 {
		status = http.StatusOK
	}
	contentType := response.ContentType
	for name, values := range response.Headers {
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}
	if value := response.Headers.Get("Content-Type"); value != "" {
		contentType = value
	}
	if contentType == "" {
		contentType = "application/json; charset=utf-8"
	}
	c.Data(status, contentType, []byte(response.Body))
}
