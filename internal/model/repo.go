// Package model 定义持久化的 Repository 接口与工厂：
// repo.go 是接口唯一定义处，factory.go 按 DBType 创建 mysql/sqlite 实现（model/sql 子包）。
// 业务包（cluster/orgcfg/calltrace/callbacks/testkit）只依赖本接口，不直接持有 *gorm.DB。
package model

import (
	"context"
	"time"

	"hermes-mock/internal/entity"
)

// Repository 聚合 hermes_mock 库的全部持久化操作。
type Repository interface {
	// DB 返回底层连接（*gorm.DB），仅供 seed/迁移等基础设施代码使用。
	DB() interface{}

	// ---- 客户集群配置（行为档/客户组/个例/端口绑定）----
	ListBehaviorProfiles(ctx context.Context) ([]entity.BehaviorProfile, error)
	UpsertBehaviorProfile(ctx context.Context, p *entity.BehaviorProfile) error
	DeleteBehaviorProfile(ctx context.Context, code string) error

	ListCustomerGroups(ctx context.Context) ([]entity.CustomerGroup, error)
	UpsertCustomerGroup(ctx context.Context, g *entity.CustomerGroup) error
	DeleteCustomerGroup(ctx context.Context, code string) error

	ListCustomerOverrides(ctx context.Context) ([]entity.CustomerOverride, error)
	UpsertCustomerOverride(ctx context.Context, o *entity.CustomerOverride) error
	DeleteCustomerOverride(ctx context.Context, number string) error

	ListLineBindings(ctx context.Context) ([]entity.LineBinding, error)
	UpsertLineBinding(ctx context.Context, b *entity.LineBinding) error
	DeleteLineBinding(ctx context.Context, listenPort int) error

	// ---- 通话记录（mock 事实表 mock_call；Save 按 record_id upsert 并合并旧值）----
	SaveCallRecord(ctx context.Context, row *entity.MockCall) error
	ListCallRecords(ctx context.Context, f entity.CallRecordFilter) ([]entity.MockCall, *entity.Meta, error)

	// ---- 测试运行历史 ----
	CreateTestRun(ctx context.Context, row *entity.TestRun) error
	ListTestRuns(ctx context.Context, limit int) ([]entity.TestRun, error)

	// ---- 通话链路（单腿 mock_trace_leg：按 session_id upsert；事件批量追加；读时按 call_uuid 归并多腿）----
	SaveTraceSession(ctx context.Context, row *entity.TraceLeg) error
	CreateTraceEvents(ctx context.Context, rows []entity.TraceEvent) error
	ListTraceSessionSummaries(ctx context.Context, limit int) ([]entity.TraceSessionSummary, error)
	ListTraceSessions(ctx context.Context, limit int) ([]entity.TraceLeg, error)
	GetTraceSession(ctx context.Context, sessionID string) (*entity.TraceLeg, error)
	ListTraceLegsByCallUUID(ctx context.Context, callUUID string) ([]entity.TraceLeg, error)
	TraceIDsByCallUUIDs(ctx context.Context, callUUIDs []string) (map[string]string, error)

	// ---- Hermes 回调 ----
	CreateCallback(ctx context.Context, row *entity.Callback) error
	ListCallbacks(ctx context.Context, f entity.CallbackFilter) ([]entity.Callback, error)

	// ---- 机构 OpenAPI 接入配置 ----
	ListOrgConfigs(ctx context.Context) ([]entity.OrgConfig, error)
	UpsertOrgConfig(ctx context.Context, c *entity.OrgConfig) error
	DeleteOrgConfig(ctx context.Context, orgCode string) error

	// ---- 观测数据治理 ----
	// PruneObservations 删除 started_at/ts 早于 before 的观测行（mock_call / mock_trace_leg / mock_trace_event / mock_callback），
	// 防长期膨胀。返回各表删除行数之和。配置表不受影响。
	PruneObservations(ctx context.Context, before time.Time) (int64, error)
}
