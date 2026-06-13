package api

import (
	"bytes"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// maxRespCapture 限制错误响应体的采集字节数：错误体（{"error":"..."}）都很小，
// 4KB 足够，且避免大列表等成功响应被无谓缓冲。
const maxRespCapture = 4 << 10

// respCapture 包装 gin.ResponseWriter，仅在状态码 >= 400 时把响应体抄一份到 buf，
// 供 RequestLogger 把「这个接口到底报了什么错」打进日志。成功响应零拷贝。
type respCapture struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (w *respCapture) capture(b []byte) {
	if w.Status() < http.StatusBadRequest {
		return
	}
	if n := maxRespCapture - w.buf.Len(); n > 0 {
		if len(b) > n {
			b = b[:n]
		}
		w.buf.Write(b)
	}
}

func (w *respCapture) Write(b []byte) (int, error) {
	w.capture(b)
	return w.ResponseWriter.Write(b)
}

func (w *respCapture) WriteString(s string) (int, error) {
	w.capture([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

// RequestLogger 统一请求日志中间件：每个请求处理完后按状态码落日志，
// 让「接口报错却没日志」彻底消失——5xx 打 Error、4xx 打 Warn（均带错误响应体），
// 2xx/3xx 仅 Debug（默认 info 级不刷屏）。健康检查 /api/health 被 k8s 探针高频轮询，跳过。
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.Path == "/api/health" {
			c.Next()
			return
		}
		start := time.Now()
		cap := &respCapture{ResponseWriter: c.Writer}
		c.Writer = cap

		c.Next()

		status := c.Writer.Status()
		fields := logrus.Fields{
			"method":  c.Request.Method,
			"path":    c.Request.URL.Path,
			"status":  status,
			"latency": time.Since(start).Round(time.Millisecond).String(),
			"ip":      c.ClientIP(),
		}
		if q := c.Request.URL.RawQuery; q != "" {
			fields["query"] = q
		}
		// 处理链内经 c.Error() 累积的错误（当前 handler 多直接 c.JSON，这里兜底未来用法）。
		if len(c.Errors) > 0 {
			fields["errors"] = c.Errors.String()
		}
		entry := logrus.WithFields(fields)
		switch {
		case status >= http.StatusInternalServerError:
			entry.WithField("resp", cap.buf.String()).Error("请求失败")
		case status >= http.StatusBadRequest:
			entry.WithField("resp", cap.buf.String()).Warn("请求被拒")
		default:
			entry.Debug("请求")
		}
	}
}

// Recovery 替代 gin.Recovery：panic 经 logrus 落 Error（带堆栈），并返回 500 JSON。
// 放在 RequestLogger 之后（更内层），使 panic 被本中间件就地恢复后，外层 RequestLogger
// 仍能记录到最终的 500 状态。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				logrus.WithFields(logrus.Fields{
					"method": c.Request.Method,
					"path":   c.Request.URL.Path,
					"panic":  rec,
					"stack":  string(debug.Stack()),
				}).Error("panic 已恢复")
				if !c.Writer.Written() {
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
				}
			}
		}()
		c.Next()
	}
}
