package cluster

import (
	"testing"
	"time"

	"hermes-mock/internal/tracelog"
)

func TestCallRecordFromTraceSessionMarksBridgeFailure(t *testing.T) {
	now := time.Date(2026, 6, 6, 13, 42, 17, 0, time.UTC)
	s := &tracelog.Session{
		ID:        "c6d1d563",
		Kind:      "sip-call",
		CallID:    "biz-1",
		StartedAt: now,
		UpdatedAt: now.Add(8 * time.Second),
		Legs:      []string{"8613800100000", "6203"},
		Events: []tracelog.Event{
			{TS: now, Channel: tracelog.ChanSIP, Leg: "8613800100000", Dir: tracelog.DirIn, Method: "INVITE", Headers: []tracelog.HeaderKV{{Name: "Call-ID", Value: "a-leg"}, {Name: "To", Value: "<sip:8613800100000@mock>"}}},
			{TS: now.Add(10 * time.Millisecond), Channel: tracelog.ChanSIP, Leg: "8613800100000", Dir: tracelog.DirOut, Method: "200", Headers: []tracelog.HeaderKV{{Name: "CSeq", Value: "1 INVITE"}}},
			{TS: now.Add(20 * time.Millisecond), Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirIn, Method: "INVITE", Headers: []tracelog.HeaderKV{{Name: "Call-ID", Value: "b-leg"}, {Name: "To", Value: "<sip:6203@mock>"}}},
			{TS: now.Add(30 * time.Millisecond), Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirOut, Method: "180"},
			{TS: now.Add(40 * time.Millisecond), Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirIn, Method: "CANCEL", Headers: []tracelog.HeaderKV{{Name: "Reason", Value: `SIP;cause=487;text="ORIGINATOR_CANCEL"`}}},
			{TS: now.Add(50 * time.Millisecond), Channel: tracelog.ChanSIP, Leg: "8613800100000", Dir: tracelog.DirIn, Method: "BYE", Headers: []tracelog.HeaderKV{{Name: "Reason", Value: `Q.850;cause=54;text="INCOMING_CALL_BARRED"`}}},
			{TS: now.Add(60 * time.Millisecond), Channel: tracelog.ChanSIP, Leg: "6203", Dir: tracelog.DirOut, Method: "487", Headers: []tracelog.HeaderKV{{Name: "CSeq", Value: "1 INVITE"}}},
			{TS: now.Add(8 * time.Second), Channel: tracelog.ChanBridge, Leg: "customer", Method: "媒体统计", Summary: "单向：仅发不收", Detail: map[string]string{"twoWay": "false", "rxPackets": "0", "txPackets": "34"}},
			{TS: now.Add(8 * time.Second), Channel: tracelog.ChanFlow, Leg: "customer", Method: "挂断", Summary: "通话时长到，挂断"},
		},
	}

	row := CallRecordFromTraceSession(s)
	if row.Status != CallRecordStatusFailed {
		t.Fatalf("status=%s, want FAILED, row=%+v", row.Status, row)
	}
	if row.HangupCode != 487 {
		t.Fatalf("hangup=%d, want 487", row.HangupCode)
	}
	if row.Result == CallRecordStatusEnded || row.Result == "" {
		t.Fatalf("result should keep failure reason, got %q", row.Result)
	}
	if row.SignalSummary != "INVITE -> 200 OK -> 180 -> CANCEL -> BYE -> 487" {
		t.Fatalf("signal=%q", row.SignalSummary)
	}
}
