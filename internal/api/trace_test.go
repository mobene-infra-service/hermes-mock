package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"hermes-mock/internal/cluster"
	"hermes-mock/internal/tracelog"
)

// 构造一个带一通通话的 Bus：jssip callId 仅出现在 event 的原始 SIP 报文里（模拟坐席软电话的匹配键
// 藏在 events 中——这正是列表瘦身的命门），会话级 callId 则是 mock 被叫腿的 SIP Call-ID。
func busWithOneCall(jssipToken, fsCallID string) *tracelog.Bus {
	bus := tracelog.New()
	sid := bus.OpenSession("call", "外呼 8613800138000 → 坐席 5002")
	raw := "INVITE sip:8613800138000@fs SIP/2.0\r\nCall-ID: " + fsCallID + "\r\nX-JSSIP-Call-Id: " + jssipToken + "\r\n\r\n"
	bus.EmitSIP(sid, "customer", tracelog.DirIn, "INVITE", "INVITE 被叫腿", nil, raw, fsCallID, "1.1.1.1:5060", "2.2.2.2:5060")
	return bus
}

func doTraceSessions(d *Deps, target string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	d.traceSessions(c)
	return w
}

// 无 match：响应是摘要列表，不含 events 字段，eventCount 正确，且 events 里的 jssip token 不会泄漏到列表。
func TestTraceSessionsSummaryStripsEvents(t *testing.T) {
	d := &Deps{Bus: busWithOneCall("jssip-ABC123", "fs-callid@2.2.2.2")}
	w := doTraceSessions(d, "/api/trace/sessions")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	if _, ok := rows[0]["events"]; ok {
		t.Errorf("摘要列表不应包含 events 字段")
	}
	var ec int
	if err := json.Unmarshal(rows[0]["eventCount"], &ec); err != nil || ec != 1 {
		t.Errorf("eventCount=%d (err=%v), want 1", ec, err)
	}
	if got := w.Body.String(); contains(got, "jssip-ABC123") {
		t.Errorf("列表响应不应泄漏藏在 event 里的 jssip token: %s", got)
	}
}

// ?match=<token>：服务端在完整 session（含 events）上做子串匹配，命中则回完整 session。
func TestTraceSessionsMatchReturnsFull(t *testing.T) {
	d := &Deps{Bus: busWithOneCall("jssip-ABC123", "fs-callid@2.2.2.2")}

	// 命中：token 藏在 raw 里也能匹配，且回的是完整 session（含 events）。
	w := doTraceSessions(d, "/api/trace/sessions?match=jssip-ABC123")
	var hits []map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &hits); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("match 命中数=%d, want 1", len(hits))
	}
	if _, ok := hits[0]["events"]; !ok {
		t.Errorf("match 结果应包含完整 events")
	}

	// 不命中：返回空数组。
	w2 := doTraceSessions(d, "/api/trace/sessions?match=nonexistent-token")
	var none []map[string]json.RawMessage
	if err := json.Unmarshal(w2.Body.Bytes(), &none); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("不存在的 token 匹配数=%d, want 0", len(none))
	}
}

func TestEnrichCallRecordTraceIDsFromBus(t *testing.T) {
	callUUID := "CCMDLabc123"
	bus := busWithOneCall("jssip-ABC123", callUUID)
	wantTraceID := bus.Sessions()[0].ID
	rows := []cluster.CallRecordRow{{RecordID: "agent-call:abc123", CallUUID: callUUID}}

	d := &Deps{Bus: bus}
	d.enrichCallRecordTraceIDs(context.Background(), rows)

	if rows[0].TraceID != wantTraceID {
		t.Fatalf("TraceID=%q, want %q", rows[0].TraceID, wantTraceID)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
