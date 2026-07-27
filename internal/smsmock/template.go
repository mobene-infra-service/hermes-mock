package smsmock

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type templateData struct {
	Message          CanonicalMessage
	Parts            int
	ReceivedAt       time.Time
	StatusCode       string
	ErrorCode        string
	ErrorDescription string
	Operator         string
}

// renderTemplate 是刻意受限的占位符替换，不执行函数/表达式。字符串变量按 JSON string
// 内容转义（不含外围引号），${parts} 为数字；Adapter 负责校验最终线格式。
func renderTemplate(raw string, data templateData, allowed []string) (string, error) {
	values := map[string]string{
		"reference":        jsonStringContent(data.Message.Reference),
		"recipient":        jsonStringContent(data.Message.Recipient),
		"sender":           jsonStringContent(data.Message.Sender),
		"content":          jsonStringContent(data.Message.Content),
		"parts":            strconv.Itoa(data.Parts),
		"receivedAt":       jsonStringContent(data.ReceivedAt.UTC().Format("2006-01-02T15:04:05")),
		"statusCode":       jsonStringContent(data.StatusCode),
		"errorCode":        jsonStringContent(data.ErrorCode),
		"errorDescription": jsonStringContent(data.ErrorDescription),
		"operator":         jsonStringContent(data.Operator),
	}
	out := raw
	for _, key := range allowed {
		value, ok := values[key]
		if !ok {
			return "", fmt.Errorf("Adapter 声明了未知模板变量 %s", key)
		}
		out = strings.ReplaceAll(out, "${"+key+"}", value)
	}
	if start := strings.Index(out, "${"); start >= 0 {
		end := strings.Index(out[start:], "}")
		if end >= 0 {
			return "", fmt.Errorf("未知模板变量 %s", out[start:start+end+1])
		}
	}
	return out, nil
}

func jsonStringContent(value string) string {
	quoted, _ := json.Marshal(value)
	if len(quoted) < 2 {
		return ""
	}
	return string(quoted[1 : len(quoted)-1])
}
