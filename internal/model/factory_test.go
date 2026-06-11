package model

import (
	"context"
	"testing"

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
	r1 := entity.CallRecord{RecordID: "rec1", Scenario: "sip-inbound", CustomerNumber: "8613800000001", Status: entity.CallRecordStatusRinging}
	if err := repo.SaveCallRecord(ctx, &r1); err != nil {
		t.Fatal(err)
	}
	// 同 record_id 再写：状态推进到 ANSWERED，customer 不丢
	r2 := entity.CallRecord{RecordID: "rec1", Status: entity.CallRecordStatusAnswered}
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
	// 状态回退保护：再写 RINGING 不应把 ANSWERED 退回去
	r3 := entity.CallRecord{RecordID: "rec1", Status: entity.CallRecordStatusRinging}
	_ = repo.SaveCallRecord(ctx, &r3)
	rows, _, _ = repo.ListCallRecords(ctx, entity.CallRecordFilter{PageSize: 10})
	if rows[0].Status != entity.CallRecordStatusAnswered {
		t.Errorf("状态不应回退: %s", rows[0].Status)
	}
}

// 链路：会话 upsert + 事件装配。
func TestTraceSessionRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveTraceSession(ctx, &entity.TraceSession{SessionID: "s1", Kind: "call", Title: "呼入"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateTraceEvents(ctx, []entity.TraceEvent{
		{SessionID: "s1", Seq: 1, Method: "INVITE"},
		{SessionID: "s1", Seq: 2, Method: "200"},
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
