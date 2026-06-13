package hermesopenapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// fakeRT 用假响应替代真实网络，避免沙箱禁绑端口。
type fakeRT struct {
	status int
	body   string
	err    error
}

func (f *fakeRT) RoundTrip(*http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

func TestLoggingTransportLogsAndPreservesBodies(t *testing.T) {
	hook := logtest.NewLocal(logrus.StandardLogger())
	defer hook.Reset()
	prev := logrus.GetLevel()
	logrus.SetLevel(logrus.DebugLevel)
	defer logrus.SetLevel(prev)

	cases := []struct {
		name      string
		status    int
		respBody  string
		wantLevel logrus.Level
	}{
		{"2xx 打 Info", 200, `{"code":0,"data":{}}`, logrus.InfoLevel},
		{"非2xx 打 Warn", 500, `{"code":1,"msg":"boom"}`, logrus.WarnLevel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook.Reset()
			tr := &loggingTransport{base: &fakeRT{status: tc.status, body: tc.respBody}}
			req, _ := http.NewRequest(http.MethodPost, "http://hermes/openapi/x", strings.NewReader(`{"req":"hello"}`))

			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			// 响应体必须仍可被调用方完整读出（transport 读过后回灌）。
			got, _ := io.ReadAll(resp.Body)
			if string(got) != tc.respBody {
				t.Errorf("调用方读到的响应体=%q, want %q", got, tc.respBody)
			}

			e := hook.LastEntry()
			if e == nil {
				t.Fatal("无日志条目")
			}
			if e.Level != tc.wantLevel {
				t.Errorf("level=%v, want %v", e.Level, tc.wantLevel)
			}
			if e.Data["status"] != tc.status {
				t.Errorf("status 字段=%v, want %d", e.Data["status"], tc.status)
			}
			if rb, _ := e.Data["reqBody"].(string); !strings.Contains(rb, "hello") {
				t.Errorf("reqBody=%q 未含请求体", rb)
			}
			if rb, _ := e.Data["respBody"].(string); !strings.Contains(rb, tc.respBody) {
				t.Errorf("respBody=%q 未含响应体", rb)
			}
			if e.Data["to"] != "hermes" {
				t.Errorf("缺少 to=hermes 标记: %v", e.Data["to"])
			}
		})
	}
}

func TestLoggingTransportLogsTransportError(t *testing.T) {
	hook := logtest.NewLocal(logrus.StandardLogger())
	defer hook.Reset()

	tr := &loggingTransport{base: &fakeRT{err: io.ErrUnexpectedEOF}}
	req, _ := http.NewRequest(http.MethodGet, "http://hermes/openapi/x", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("传输错误应原样返回")
	}
	e := hook.LastEntry()
	if e == nil || e.Level != logrus.ErrorLevel {
		t.Fatalf("传输错误应打 Error 日志, got %v", e)
	}
}
