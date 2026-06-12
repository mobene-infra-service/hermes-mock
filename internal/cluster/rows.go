package cluster

import "hermes-mock/internal/entity"

// 持久化行类型别名：实体已下沉 internal/entity，消费方（calltrace/callbacks/testkit/api/main）
// 继续用 cluster.XxxRow 旧名。状态常量同理转发。
type (
	CallRecordRow    = entity.MockCall
	CallRecordFilter = entity.CallRecordFilter
	TestRunRow       = entity.TestRun
	TraceSessionRow  = entity.TraceLeg
	TraceEventRow    = entity.TraceEvent
	CallbackRow      = entity.Callback
	CallbackQuery    = entity.CallbackFilter
)

const (
	CallRecordStatusPending  = entity.CallRecordStatusPending
	CallRecordStatusRinging  = entity.CallRecordStatusRinging
	CallRecordStatusAnswered = entity.CallRecordStatusAnswered
	CallRecordStatusEnded    = entity.CallRecordStatusEnded
	CallRecordStatusRejected = entity.CallRecordStatusRejected
	CallRecordStatusFailed   = entity.CallRecordStatusFailed
)

// CallRecordPage 分页结果（api 层响应结构，保留旧形状）。
type CallRecordPage struct {
	Records  []CallRecordRow `json:"records"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
}
