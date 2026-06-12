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

// CallCenterTaskReq 创建 call-center 群呼任务的完整参数（对照 Hermes
// com.hermes.call.center.entity.request.calltask.AddCallTaskAndImportNumberReq）。
// orgCode 不进 Hermes 请求体（由凭据头 ORG_CODE 取机构），此处仅作编排/展示透传。
// 组合约束（Hermes CallTaskService.validateAddRequest）：
//   - ModeStrategy=1(比例)：必须给 Proportion(1-10)。
//   - ModeStrategy=2(PID)：必须给 LossRate(0-99) + HistoricalConnectionRate(1-100)。
//   - 坐席分配二选一：AgentNumbers(指定坐席号 max500) 或 AgentGroupCodes(技能组，恰好 1 个)。
type CallCenterTaskReq struct {
	OrgCode string   `json:"orgCode"`
	Name    string   `json:"name"`
	Numbers []string `json:"numbers"`

	AgentGroupCodes []string `json:"agentGroupCodes"`
	AgentNumbers    []string `json:"agentNumbers"`

	TTSCode string `json:"ttsCode"`
	TTSText string `json:"ttsText"`

	// 模式策略：1=比例(PROPORTION) 2=PID。配套字段见上方组合约束。
	ModeStrategy             int `json:"modeStrategy"`
	Proportion               int `json:"proportion"`
	LossRate                 int `json:"lossRate"`
	HistoricalConnectionRate int `json:"historicalConnectionRate"`

	SortMethod     int  `json:"sortMethod"`     // 1=优先首呼 2=优先重呼（必填）
	IsPriorityTask bool `json:"isPriorityTask"` // 优先任务
	IsVmHangup     bool `json:"isVmHangup"`     // 检测语音信箱即挂（Hermes 默认 true）

	MaxRedialTimes       int    `json:"maxRedialTimes"`       // 最大重拨次数 1-5（0=不传）
	RedialInterval       int    `json:"redialInterval"`       // 重拨间隔分钟 0-60
	BestRingDuration     int    `json:"bestRingDuration"`     // 最佳响铃时长秒 10-60（默认 40）
	AgentMaxRingDuration int    `json:"agentMaxRingDuration"` // 坐席最大响铃秒 1-60（0=不传）
	AssignDelaySeconds   int    `json:"assignDelaySeconds"`   // 分配延迟秒 0-60
	TransferType         string `json:"transferType"`         // ai-only / human_only / 空
	Description          string `json:"description"`          // 任务描述 max300

	StartDate      string   `json:"startDate"`
	EndDate        string   `json:"endDate"`
	DialTimePeriod []string `json:"dialTimePeriod"`
	// LineType 线路类型（Hermes 7cbb285：任务期间仅用该 type 线路选号；空=默认 base）。
	LineType string `json:"lineType"`
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
