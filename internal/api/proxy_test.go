package api

import "testing"

func TestJoinProxyPathKeepsTargetBasePath(t *testing.T) {
	got := joinProxyPath("/call-center", "/agent-workbench/sdk/agent/webrtc/addr")
	want := "/call-center/agent-workbench/sdk/agent/webrtc/addr"
	if got != want {
		t.Fatalf("joinProxyPath()=%q, want %q", got, want)
	}
}

func TestJoinProxyPathWithoutTargetBasePath(t *testing.T) {
	got := joinProxyPath("", "/agent-workbench/sdk/agent/webrtc/addr")
	want := "/agent-workbench/sdk/agent/webrtc/addr"
	if got != want {
		t.Fatalf("joinProxyPath()=%q, want %q", got, want)
	}
}
