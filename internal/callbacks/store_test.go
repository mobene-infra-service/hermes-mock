package callbacks

import "testing"

func TestRecordAndExtract(t *testing.T) {
	s := New(nil)
	r := s.Record("callbot", "1.2.3.4", []byte(`{"event":"CALL_END","orgCode":"org001","data":{"callUuid":"abc123"}}`))
	if r.Event != "CALL_END" || r.OrgCode != "org001" || r.CallUUID != "abc123" {
		t.Errorf("提取错: %+v", r)
	}
}

func TestQueryFilters(t *testing.T) {
	s := New(nil)
	s.Record("callbot", "ip", []byte(`{"event":"E1","orgCode":"org001","callUuid":"u1"}`))
	s.Record("autocall", "ip", []byte(`{"event":"E2","orgCode":"org002","callUuid":"u2","note":"hello"}`))
	if got := s.Query(Filter{Source: "callbot"}); len(got) != 1 || got[0].Event != "E1" {
		t.Errorf("source 筛选错: %+v", got)
	}
	if got := s.Query(Filter{OrgCode: "org002"}); len(got) != 1 || got[0].CallUUID != "u2" {
		t.Errorf("org 筛选错: %+v", got)
	}
	if got := s.Query(Filter{CallUUID: "u1"}); len(got) != 1 {
		t.Errorf("callUuid 筛选错: %d", len(got))
	}
	if got := s.Query(Filter{Keyword: "HELLO"}); len(got) != 1 { // 大小写无关
		t.Errorf("keyword 筛选错: %d", len(got))
	}
	if got := s.Query(Filter{}); len(got) != 2 {
		t.Errorf("无筛选应返回全部, got %d", len(got))
	}
}
