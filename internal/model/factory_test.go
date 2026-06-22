package model

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"hermes-mock/internal/entity"
	repsql "hermes-mock/internal/model/sql"
)

// newTestRepo SQLite 内存库 Repository（repository 单测不依赖外部 MySQL）。
func newTestRepo(t *testing.T) Repository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := (&RepositoryFactory{}).migrateSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repsql.NewGormRepository(db)
}

// 行为档 upsert：同 code 二次写为更新（不翻倍），删除生效。
func TestBehaviorProfileUpsertIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	p := entity.BehaviorProfile{Code: "p1", Name: "接听", Outcome: "ANSWER", AnswerRatio: 100}
	if err := repo.UpsertBehaviorProfile(ctx, &p); err != nil {
		t.Fatal(err)
	}
	p2 := entity.BehaviorProfile{Code: "p1", Name: "改名", Outcome: "REJECT", AnswerRatio: 100}
	if err := repo.UpsertBehaviorProfile(ctx, &p2); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListBehaviorProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "改名" || rows[0].Outcome != "REJECT" {
		t.Errorf("upsert 应更新而非新增: %+v", rows)
	}
	if err := repo.DeleteBehaviorProfile(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if rows, _ = repo.ListBehaviorProfiles(ctx); len(rows) != 0 {
		t.Errorf("删除后应为空, got %d", len(rows))
	}
}

// 通话记录：按 record_id upsert + 字段合并（状态只进不退）；过滤查询分页。
func TestCallRecordSaveMergeAndList(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	r1 := entity.MockCall{
		RecordID: "rec1", Scenario: "sip-inbound", Source: "sip", CustomerNumber: "8613800000001",
		TaskName: "任务 Alpha", TraceID: "trace-exact", CallUUID: "call-exact", Status: entity.CallRecordStatusRinging,
	}
	if err := repo.SaveCallRecord(ctx, &r1); err != nil {
		t.Fatal(err)
	}
	// 同 record_id 再写：状态推进到 ANSWERED，customer 不丢
	r2 := entity.MockCall{RecordID: "rec1", Status: entity.CallRecordStatusAnswered}
	if err := repo.SaveCallRecord(ctx, &r2); err != nil {
		t.Fatal(err)
	}
	rows, meta, err := repo.ListCallRecords(ctx, entity.CallRecordFilter{Scenario: "sip-inbound", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Total != 1 || len(rows) != 1 {
		t.Fatalf("应恰 1 条, got total=%d len=%d", meta.Total, len(rows))
	}
	if rows[0].Status != entity.CallRecordStatusAnswered || rows[0].CustomerNumber != "8613800000001" {
		t.Errorf("合并错: %+v", rows[0])
	}
	// 结构化字段是精确匹配，不再用 LIKE：agent 不应命中 agent-call。
	if err := repo.SaveCallRecord(ctx, &entity.MockCall{RecordID: "agent-out", Scenario: "agent-call", Source: "agent", CustomerNumber: "8613800000002", Status: entity.CallRecordStatusEnded}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveCallRecord(ctx, &entity.MockCall{RecordID: "agent-in", Scenario: "agent-inbound", Source: "agent", CustomerNumber: "8613800000003", Status: entity.CallRecordStatusEnded}); err != nil {
		t.Fatal(err)
	}
	rows, meta, err = repo.ListCallRecords(ctx, entity.CallRecordFilter{Scenario: "agent", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Total != 0 || len(rows) != 0 {
		t.Fatalf("scenario 精确匹配不应命中 agent-*，got total=%d rows=%+v", meta.Total, rows)
	}
	rows, meta, err = repo.ListCallRecords(ctx, entity.CallRecordFilter{Scenarios: []string{"agent-call", "agent-inbound"}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Total != 2 || len(rows) != 2 {
		t.Fatalf("scenarios IN 应命中两类坐席记录, got total=%d len=%d", meta.Total, len(rows))
	}
	rows, meta, err = repo.ListCallRecords(ctx, entity.CallRecordFilter{Keyword: "Alpha", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Total != 1 || rows[0].RecordID != "rec1" {
		t.Fatalf("keyword 应保留模糊搜索, got total=%d rows=%+v", meta.Total, rows)
	}
	rows, meta, err = repo.ListCallRecords(ctx, entity.CallRecordFilter{CallUUID: "call", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Total != 0 || len(rows) != 0 {
		t.Fatalf("call_uuid 应精确匹配，不应按前缀命中: total=%d rows=%+v", meta.Total, rows)
	}
	// 状态回退保护：再写 RINGING 不应把 ANSWERED 退回去
	r3 := entity.MockCall{RecordID: "rec1", Status: entity.CallRecordStatusRinging}
	_ = repo.SaveCallRecord(ctx, &r3)
	rows, _, _ = repo.ListCallRecords(ctx, entity.CallRecordFilter{Scenario: "sip-inbound", PageSize: 10})
	if rows[0].Status != entity.CallRecordStatusAnswered {
		t.Errorf("状态不应回退: %s", rows[0].Status)
	}
}

// 链路：单腿 upsert + 事件装配。
func TestTraceSessionRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveTraceSession(ctx, &entity.TraceLeg{SessionID: "s1", CallUUID: "u1", LegRole: "customer", Kind: "call", Title: "呼入"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTraceEvents(ctx, []entity.TraceEvent{
		{SessionID: "s1", Seq: 1, Leg: "customer", Method: "INVITE"},
		{SessionID: "s1", Seq: 2, Leg: "customer", Method: "200"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetTraceSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Events) != 2 || got.Events[1].Method != "200" {
		t.Errorf("会话装配错: %+v", got)
	}
	if missing, _ := repo.GetTraceSession(ctx, "nope"); missing != nil {
		t.Error("不存在的会话应返回 nil")
	}
	summaries, err := repo.ListTraceSessionSummaries(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].SessionID != "s1" || summaries[0].EventCount != 2 {
		t.Fatalf("摘要应带事件计数但不装事件原文: %+v", summaries)
	}
	if len(summaries[0].Legs) != 1 || summaries[0].Legs[0] != "customer" {
		t.Fatalf("摘要应聚合 leg 列表: %+v", summaries[0].Legs)
	}
}

// 多腿按 call_uuid 归并：同一通业务通话的多条单腿（不同 session_id、同 call_uuid）各带事件。
func TestTraceLegsByCallUUID(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveTraceSession(ctx, &entity.TraceLeg{SessionID: "legCust", CallUUID: "uuidX", LegRole: "customer", Kind: "call"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveTraceSession(ctx, &entity.TraceLeg{SessionID: "legAgent", CallUUID: "uuidX", LegRole: "agent", Kind: "call"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTraceEvents(ctx, []entity.TraceEvent{
		{SessionID: "legCust", Seq: 1, Leg: "customer", Method: "INVITE"},
		{SessionID: "legAgent", Seq: 1, Leg: "agent", Method: "INVITE"},
	}); err != nil {
		t.Fatal(err)
	}
	legs, err := repo.ListTraceLegsByCallUUID(ctx, "uuidX")
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 2 {
		t.Fatalf("同 call_uuid 应回 2 腿, got %d", len(legs))
	}
	for _, l := range legs {
		if len(l.Events) != 1 {
			t.Errorf("腿 %s 应带 1 事件, got %d", l.SessionID, len(l.Events))
		}
	}
	if none, _ := repo.ListTraceLegsByCallUUID(ctx, "nope"); none != nil {
		t.Error("无匹配 call_uuid 应返回 nil")
	}
	ids, err := repo.TraceIDsByCallUUIDs(ctx, []string{"uuidX", "uuidX", "", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if ids["uuidX"] == "" {
		t.Fatalf("应批量查到 uuidX 的 trace id: %+v", ids)
	}
	if _, ok := ids["nope"]; ok {
		t.Fatalf("不存在 call_uuid 不应返回 trace id: %+v", ids)
	}
}

// 机构配置：org_code 唯一 upsert。
func TestOrgConfigUpsert(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.UpsertOrgConfig(ctx, &entity.OrgConfig{OrgCode: "org001", OrgName: "A", Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOrgConfig(ctx, &entity.OrgConfig{OrgCode: "org001", OrgName: "A2", Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	rows, _ := repo.ListOrgConfigs(ctx)
	if len(rows) != 1 || rows[0].OrgName != "A2" {
		t.Errorf("org upsert 应更新而非新增: %+v", rows)
	}
}

// 观测数据 TTL 清理：早于 before 的呼叫记录/链路/事件被删，之后的保留；配置表不受影响。
func TestPruneObservations(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	fresh := now.Add(-1 * time.Hour)

	// 旧+新各一条呼叫记录
	if err := repo.SaveCallRecord(ctx, &entity.MockCall{RecordID: "oldCall", Status: entity.CallRecordStatusEnded, StartedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveCallRecord(ctx, &entity.MockCall{RecordID: "freshCall", Status: entity.CallRecordStatusEnded, StartedAt: fresh}); err != nil {
		t.Fatal(err)
	}
	// 旧+新各一条单腿 + 各一条事件
	if err := repo.SaveTraceSession(ctx, &entity.TraceLeg{SessionID: "oldLeg", StartedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveTraceSession(ctx, &entity.TraceLeg{SessionID: "freshLeg", StartedAt: fresh}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTraceEvents(ctx, []entity.TraceEvent{
		{SessionID: "oldLeg", Seq: 1, TS: old, Method: "INVITE"},
		{SessionID: "freshLeg", Seq: 1, TS: fresh, Method: "INVITE"},
	}); err != nil {
		t.Fatal(err)
	}
	// 一条保留期配置（不应被删）
	if err := repo.UpsertBehaviorProfile(ctx, &entity.BehaviorProfile{Code: "keep", Outcome: "ANSWER", AnswerRatio: 100}); err != nil {
		t.Fatal(err)
	}

	before := now.Add(-7 * 24 * time.Hour)
	n, err := repo.PruneObservations(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	// 删除：oldCall + oldLeg + oldLeg 的事件 = 3 行
	if n != 3 {
		t.Errorf("应删 3 行（旧呼叫+旧腿+旧事件），got %d", n)
	}
	calls, _, _ := repo.ListCallRecords(ctx, entity.CallRecordFilter{PageSize: 10})
	if len(calls) != 1 || calls[0].RecordID != "freshCall" {
		t.Errorf("应只剩 freshCall: %+v", calls)
	}
	if old, _ := repo.GetTraceSession(ctx, "oldLeg"); old != nil {
		t.Error("旧腿应被删")
	}
	if fr, _ := repo.GetTraceSession(ctx, "freshLeg"); fr == nil || len(fr.Events) != 1 {
		t.Error("新腿及其事件应保留")
	}
	if profs, _ := repo.ListBehaviorProfiles(ctx); len(profs) != 1 {
		t.Errorf("配置表不应被清理, got %d", len(profs))
	}
}
