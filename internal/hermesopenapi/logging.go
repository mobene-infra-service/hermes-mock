package hermesopenapi

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

// maxBodyLog 限制请求/响应体的落日志字节数：OpenAPI 体多为 JSON，2KB 足够定位，
// 又不至于让一次 500 坐席列表把日志撑爆。
const maxBodyLog = 2 << 10

// loggingTransport 包裹底层 RoundTripper，统一打印 mock→Hermes 的每次调用：
// 请求方法/URL/请求体 + 响应状态/响应体/耗时。装在 Client.http 上，
// 所有经 hermesopenapi 的调用（call/callWith/callPlain/Ping/...）自动覆盖，零散落。
//
// 不打印请求头：其中含 X-OpenApi-Key 等凭据，避免泄漏。
type loggingTransport struct {
	base http.RoundTripper
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	// req.Body 会被底层 transport 消费；用 NewRequestWithContext 自动设置的 GetBody 取一份副本，不扰真实读取。
	var reqBody string
	if req.GetBody != nil {
		if rc, err := req.GetBody(); err == nil {
			b, _ := io.ReadAll(rc)
			rc.Close()
			reqBody = clip(string(b), maxBodyLog)
		}
	}

	start := time.Now()
	resp, err := base.RoundTrip(req)
	latency := time.Since(start).Round(time.Millisecond)

	fields := logrus.Fields{
		"to":      "hermes",
		"method":  req.Method,
		"url":     req.URL.String(),
		"latency": latency.String(),
	}
	if reqBody != "" {
		fields["reqBody"] = reqBody
	}
	if err != nil {
		logrus.WithFields(fields).WithError(err).Error("调用 Hermes 失败（传输层）")
		return resp, err
	}

	// 读出响应体打日志后，再塞回一个全新 reader 供调用方正常消费。
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))

	fields["status"] = resp.StatusCode
	fields["respBody"] = clip(string(raw), maxBodyLog)
	if resp.StatusCode >= 300 {
		logrus.WithFields(fields).Warn("调用 Hermes 返回非 2xx")
	} else {
		logrus.WithFields(fields).Info("调用 Hermes")
	}
	return resp, nil
}
