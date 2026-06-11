package entity

import "time"

// Meta 分页元信息。
type Meta struct {
	Page     int64 `json:"page"`
	PageSize int64 `json:"page_size"`
	Total    int64 `json:"total"`
}

// Resolved 解析一通呼叫得到的有效行为（组/个例合并后）——cluster 解析链的输出。
type Resolved struct {
	GroupCode string           `json:"groupCode"`
	Number    string           `json:"number"`
	Profile   *BehaviorProfile `json:"profile"`
	Disabled  bool             `json:"disabled"` // 组/个例状态为禁用
}

// CallRecordFilter 通话记录查询过滤器（字段空=不过滤；字符串字段 LIKE 匹配）。
type CallRecordFilter struct {
	Scenario       string
	Status         string
	OrgCode        string
	RunID          string
	TaskName       string
	TaskCode       string
	CustomerGroup  string
	CustomerNumber string
	AgentGroupCode string
	AgentNumber    string
	LineCode       string
	TraceID        string
	CallUUID       string
	Keyword        string
	StartedFrom    *time.Time
	StartedTo      *time.Time
	Page           int
	PageSize       int
}

// CallbackFilter 回调查询过滤器。
type CallbackFilter struct {
	Source   string
	Event    string
	OrgCode  string
	CallUUID string
	Keyword  string
	Limit    int
}
