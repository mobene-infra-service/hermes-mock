package cluster

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"hermes-mock/internal/tracelog"
)

// CallRecordFromTraceSession 从 mock 亲历的链路事件聚合一条电话记录。
func CallRecordFromTraceSession(s *tracelog.Session) CallRecordRow {
	if s == nil {
		return CallRecordRow{}
	}
	row := CallRecordRow{
		RecordID:    "trace:" + s.ID,
		Scenario:    "sip-call",
		Source:      "sip",
		TraceID:     s.ID,
		CallUUID:    s.CallID,
		StartedAt:   s.StartedAt.UTC(),
		LastEventAt: s.UpdatedAt.UTC(),
		Status:      CallRecordStatusPending,
		CallType:    s.Kind,
		Direction:   "HERMES_TO_MOCK",
	}
	detail := map[string]any{
		"traceTitle": s.Title,
		"traceKind":  s.Kind,
		"legs":       s.Legs,
	}
	methods := make([]string, 0, len(s.Events))
	var answeredAt, endedAt *time.Time
	var firstInviteAt time.Time
	lastStatusCode := 0
	hasCancel := false
	hasIncomingBarred := false
	hasMediaFailure := false
	failReasons := make([]string, 0, 4)
	for _, leg := range s.Legs {
		if strings.HasPrefix(leg, "agent:") {
			row.AgentNumber = strings.TrimPrefix(leg, "agent:")
			continue
		}
		if leg != "" && leg != "customer" && row.CustomerNumber == "" {
			row.CustomerNumber = leg
		}
	}
	for _, e := range s.Events {
		if e.TS.After(row.LastEventAt) {
			row.LastEventAt = e.TS.UTC()
		}
		if e.Summary != "" {
			row.LastSummary = e.Summary
		}
		if e.Channel == tracelog.ChanWS {
			row.CallbackSummary = e.Summary
			if row.AgentNumber == "" {
				row.AgentNumber = firstNonEmpty(e.Detail["agent"], strings.TrimPrefix(e.Leg, "agent:"))
			}
		}
		if e.Channel == tracelog.ChanBridge && e.Method == "媒体统计" {
			row.MediaSummary = e.Summary
			for k, v := range e.Detail {
				detail["media."+k] = v
			}
			if strings.EqualFold(e.Detail["twoWay"], "false") {
				hasMediaFailure = true
				failReasons = appendUnique(failReasons, "媒体非双向")
			}
		}
		if e.Channel == tracelog.ChanFlow {
			for k, v := range e.Detail {
				detail["flow."+k] = v
			}
			switch {
			case strings.Contains(e.Method, "应答") || strings.Contains(e.Summary, "应答"):
				t := e.TS.UTC()
				if answeredAt == nil {
					answeredAt = &t
				}
				if !isTerminalFailure(row.Status) && row.Status != CallRecordStatusEnded {
					row.Status = CallRecordStatusAnswered
				}
			case strings.Contains(e.Summary, "拒接") || strings.Contains(e.Summary, "不可用") || strings.Contains(e.Summary, "失败"):
				row.Status = CallRecordStatusFailed
				failReasons = appendUnique(failReasons, e.Summary)
			case strings.Contains(e.Method, "挂断") || strings.Contains(e.Summary, "挂断"):
				t := e.TS.UTC()
				endedAt = &t
				if !isTerminalFailure(row.Status) {
					row.Status = CallRecordStatusEnded
				}
			}
		}
		if e.Channel != tracelog.ChanSIP {
			continue
		}
		methods = append(methods, e.Method)
		if row.SIPCallID == "" {
			row.SIPCallID = headerValue(e.Headers, "Call-ID", "i")
		}
		if headerContainsValue(e.Headers, "Reason", "INCOMING_CALL_BARRED") {
			hasIncomingBarred = true
			failReasons = appendUnique(failReasons, "INCOMING_CALL_BARRED")
		}
		if row.CallUUID == "" || row.CallUUID == row.SIPCallID {
			row.CallUUID = headerValue(e.Headers, "X-CALL-UUID", "X-Call-UUID", "X-CALLID", "X-JCALLID", "X-CALL-ID", "X-SESSION-ID", "X-SESSION_ID")
		}
		if row.LineName == "" {
			row.LineName = headerValue(e.Headers, "X-Line-Name", "x-line-name")
		}
		if row.LineCode == "" {
			row.LineCode = headerValue(e.Headers, "X-Line-Code", "X-LineCode", "X-Line")
		}
		if row.LineAddress == "" {
			row.LineAddress = headerValue(e.Headers, "X-Line-Address", "X-Caller-Address", "X-CallerAddress")
		}
		if row.CustomerNumber == "" && e.Leg != "" && e.Leg != "customer" && !strings.HasPrefix(e.Leg, "agent:") {
			row.CustomerNumber = e.Leg
		}
		switch {
		case e.Method == "INVITE":
			if firstInviteAt.IsZero() {
				firstInviteAt = e.TS.UTC()
				row.StartedAt = firstInviteAt
			}
			if row.Status == CallRecordStatusPending {
				row.Status = CallRecordStatusRinging
			}
			row.SignalSummary = appendSignal(row.SignalSummary, "INVITE")
			fillSIPIdentity(&row, e.Headers)
		case e.Method == "180" || e.Method == "183":
			if row.Status == CallRecordStatusPending {
				row.Status = CallRecordStatusRinging
			}
			row.SignalSummary = appendSignal(row.SignalSummary, e.Method)
		case e.Method == "200" && isInviteResponse(e.Headers):
			t := e.TS.UTC()
			if answeredAt == nil {
				answeredAt = &t
			}
			if !isTerminalFailure(row.Status) && row.Status != CallRecordStatusEnded {
				row.Status = CallRecordStatusAnswered
			}
			row.SignalSummary = appendSignal(row.SignalSummary, "200 OK")
		case e.Method == "CANCEL":
			hasCancel = true
			row.SignalSummary = appendSignal(row.SignalSummary, "CANCEL")
			failReasons = appendUnique(failReasons, "CANCEL")
		case e.Method == "BYE":
			t := e.TS.UTC()
			endedAt = &t
			if !isTerminalFailure(row.Status) {
				row.Status = CallRecordStatusEnded
			}
			row.SignalSummary = appendSignal(row.SignalSummary, "BYE")
		default:
			if code, ok := sipStatusCode(e.Method); ok {
				lastStatusCode = code
				if code >= 400 {
					row.HangupCode = code
					t := e.TS.UTC()
					endedAt = &t
					row.Status = CallRecordStatusRejected
					failReasons = appendUnique(failReasons, "SIP_"+e.Method)
					row.SignalSummary = appendSignal(row.SignalSummary, e.Method)
				}
			}
		}
	}
	row.AnsweredAt = answeredAt
	row.EndedAt = endedAt
	if row.Status == CallRecordStatusPending && len(methods) > 0 {
		row.Status = CallRecordStatusRinging
	}
	switch {
	case hasIncomingBarred || hasMediaFailure:
		row.Status = CallRecordStatusFailed
		row.Result = strings.Join(failReasons, "; ")
	case hasCancel && row.Status == CallRecordStatusEnded:
		row.Status = CallRecordStatusRejected
		row.Result = strings.Join(failReasons, "; ")
	case row.Status == CallRecordStatusRejected && lastStatusCode >= 500:
		row.Result = "SIP_" + strconv.Itoa(lastStatusCode)
	default:
		row.Result = row.Status
	}
	if row.Result == "" {
		row.Result = row.Status
	}
	if len(failReasons) > 0 {
		detail["failureReasons"] = failReasons
		if row.LastSummary == "" || row.Status == CallRecordStatusFailed || row.Status == CallRecordStatusRejected {
			row.LastSummary = strings.Join(failReasons, "; ")
		}
	}
	if row.CallUUID == "" {
		row.CallUUID = s.CallID
	}
	if row.RecordID == "trace:" {
		row.RecordID = "trace:" + firstNonEmpty(row.CallUUID, row.SIPCallID, s.ID)
	}
	detail["methods"] = methods
	detail["sipCallId"] = row.SIPCallID
	b, _ := json.Marshal(detail)
	row.DetailJSON = string(b)
	// 兜底字段（scenario/source/status/时间）由 Repository.SaveCallRecord 落库时统一 normalize。
	return row
}

func fillSIPIdentity(row *CallRecordRow, headers []tracelog.HeaderKV) {
	toUser := sipUser(headerValue(headers, "To", "t"))
	fromUser := sipUser(headerValue(headers, "From", "f"))
	if row.CustomerNumber == "" {
		row.CustomerNumber = toUser
	}
	if row.AgentNumber == "" && strings.HasPrefix(fromUser, "agent:") {
		row.AgentNumber = strings.TrimPrefix(fromUser, "agent:")
	}
}

func isInviteResponse(headers []tracelog.HeaderKV) bool {
	return strings.Contains(strings.ToUpper(headerValue(headers, "CSeq")), "INVITE")
}

func isTerminalFailure(status string) bool {
	return status == CallRecordStatusFailed || status == CallRecordStatusRejected
}

func sipStatusCode(method string) (int, bool) {
	if len(method) != 3 {
		return 0, false
	}
	n, err := strconv.Atoi(method)
	return n, err == nil
}

func headerValue(headers []tracelog.HeaderKV, names ...string) string {
	for _, want := range names {
		for _, h := range headers {
			if strings.EqualFold(h.Name, want) {
				return strings.TrimSpace(h.Value)
			}
		}
	}
	return ""
}

func headerContainsValue(headers []tracelog.HeaderKV, name, want string) bool {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) && strings.Contains(strings.ToUpper(h.Value), strings.ToUpper(want)) {
			return true
		}
	}
	return false
}

func sipUser(addr string) string {
	i := strings.Index(strings.ToLower(addr), "sip:")
	step := 4
	if i < 0 {
		i = strings.Index(strings.ToLower(addr), "sips:")
		step = 5
	}
	if i < 0 {
		return ""
	}
	rest := addr[i+step:]
	if j := strings.IndexAny(rest, "@>;"); j >= 0 {
		return rest[:j]
	}
	return rest
}

func appendSignal(current, next string) string {
	if next == "" || strings.Contains(current, next) {
		return current
	}
	if current == "" {
		return next
	}
	if len(current)+len(next)+4 > 512 {
		return current
	}
	return current + " -> " + next
}

func appendUnique(values []string, next string) []string {
	next = strings.TrimSpace(next)
	if next == "" {
		return values
	}
	for _, v := range values {
		if v == next {
			return values
		}
	}
	return append(values, next)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
