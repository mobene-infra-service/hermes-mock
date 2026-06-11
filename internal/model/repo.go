// Package model 定义持久化的 Repository 接口与工厂：
// repo.go 是接口唯一定义处，factory.go 按 DBType 创建 mysql/sqlite 实现（model/sql 子包）。
// 业务包（cluster/orgcfg/calltrace/callbacks/testkit）只依赖本接口，不直接持有 *gorm.DB。
package model

import (
	"context"

	"hermes-mock/internal/entity"
)

// Repository 聚合 hermes_mock 库的全部持久化操作。
type Repository interface {
	// DB 返回底层连接（*gorm.DB），仅供 seed/迁移等基础设施代码使用。
	DB() interface{}

	// ---- 客户集群配置（行为档/客户组/个例/线路绑定）----
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
	DeleteLineBinding(ctx context.Context, lineCode string) error

	// ---- 通话记录（mock 事实表；Save 按 trace_id/record_id upsert 并合并旧值）----
	SaveCallRecord(ctx context.Context, row *entity.CallRecord) error
	ListCallRecords(ctx context.Context, f entity.CallRecordFilter) ([]entity.CallRecord, *entity.Meta, error)

	// ---- 测试运行历史 ----
	CreateTestRun(ctx context.Context, row *entity.TestRun) error
	ListTestRuns(ctx context.Context, limit int) ([]entity.TestRun, error)

	// ---- 通话链路（会话按 session_id upsert；事件批量追加）----
	SaveTraceSession(ctx context.Context, row *entity.TraceSession) error
	CreateTraceEvents(ctx context.Context, rows []entity.TraceEvent) error
	ListTraceSessions(ctx context.Context, limit int) ([]entity.TraceSession, error)
	GetTraceSession(ctx context.Context, sessionID string) (*entity.TraceSession, error)

	// ---- Hermes 回调 ----
	CreateCallback(ctx context.Context, row *entity.Callback) error
	ListCallbacks(ctx context.Context, f entity.CallbackFilter) ([]entity.Callback, error)

	// ---- 机构 OpenAPI 接入配置 ----
	ListOrgConfigs(ctx context.Context) ([]entity.OrgConfig, error)
	UpsertOrgConfig(ctx context.Context, c *entity.OrgConfig) error
	DeleteOrgConfig(ctx context.Context, orgCode string) error
}
