package sql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"hermes-mock/internal/entity"
)

// 通话记录：mock 事实表 mock_call。SaveCallRecord 按 record_id upsert，
// 并把已有行与新行做字段级合并（首值优先 + 状态只进不退）。
// 被叫腿的 record_id = call_uuid（sipagent 传入），同一通话 INVITE 重传幂等合并到一行。

func (r *GormRepository) SaveCallRecord(ctx context.Context, row *entity.MockCall) error {
	normalizeCallRecord(row)
	if row.RecordID == "" {
		return errors.New("record_id 必填")
	}
	db := r.db.WithContext(ctx)
	var id int64
	if err := db.Table(row.TableName()).Where("record_id = ?", row.RecordID).Select("id").Limit(1).Scan(&id).Error; err != nil {
		return err
	}
	if id != 0 {
		var existing entity.MockCall
		if err := db.First(&existing, id).Error; err == nil {
			*row = mergeCallRecord(existing, *row)
		}
		row.ID = id
		return db.Save(row).Error
	}
	return db.Create(row).Error
}

func (r *GormRepository) ListCallRecords(ctx context.Context, f entity.CallRecordFilter) ([]entity.MockCall, *entity.Meta, error) {
	page := f.Page
	if page <= 0 {
		page = 1
	}
	pageSize := f.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	q := r.db.WithContext(ctx).Model(&entity.MockCall{})
	if scenarios := cleanStrings(f.Scenarios); len(scenarios) > 0 {
		q = q.Where("scenario IN ?", scenarios)
	} else {
		q = eq(q, "scenario", f.Scenario)
	}
	q = eq(q, "source", f.Source)
	q = eq(q, "status", f.Status)
	q = eq(q, "org_code", f.OrgCode)
	q = eq(q, "run_id", f.RunID)
	q = eq(q, "task_name", f.TaskName)
	q = eq(q, "task_code", f.TaskCode)
	q = eq(q, "customer_group", f.CustomerGroup)
	q = eq(q, "customer_number", f.CustomerNumber)
	q = eq(q, "agent_group_code", f.AgentGroupCode)
	q = eq(q, "agent_number", f.AgentNumber)
	q = eq(q, "line_code", f.LineCode)
	q = eq(q, "trace_id", f.TraceID)
	q = eq(q, "call_uuid", f.CallUUID)
	if f.StartedFrom != nil {
		q = q.Where("started_at >= ?", *f.StartedFrom)
	}
	if f.StartedTo != nil {
		q = q.Where("started_at <= ?", *f.StartedTo)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		pat := "%" + kw + "%"
		q = q.Where(`customer_number LIKE ? OR agent_number LIKE ? OR task_name LIKE ? OR call_uuid LIKE ? OR trace_id LIKE ? OR detail_json LIKE ? OR last_summary LIKE ?`,
			pat, pat, pat, pat, pat, pat, pat)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, nil, err
	}
	var rows []entity.MockCall
	err := q.Order("started_at DESC").Order("id DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error
	return rows, r.calculatePagination(total, page, pageSize), err
}

func normalizeCallRecord(r *entity.MockCall) {
	r.RecordID = strings.TrimSpace(r.RecordID)
	r.Scenario = strings.TrimSpace(r.Scenario)
	if r.Scenario == "" {
		r.Scenario = "unknown"
	}
	r.Source = strings.TrimSpace(r.Source)
	if r.Source == "" {
		r.Source = "mock"
	}
	r.Status = strings.TrimSpace(r.Status)
	if r.Status == "" {
		r.Status = entity.CallRecordStatusPending
	}
	if r.Direction == "" {
		r.Direction = "HERMES_TO_MOCK"
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	if r.LastEventAt.IsZero() {
		r.LastEventAt = r.StartedAt
	}
	if r.AnsweredAt != nil && r.EndedAt != nil && r.DurationMs == 0 && r.EndedAt.After(*r.AnsweredAt) {
		r.DurationMs = r.EndedAt.Sub(*r.AnsweredAt).Milliseconds()
	}
	if r.StepsJSON == "" {
		r.StepsJSON = "[]"
	}
	if r.DetailJSON == "" {
		r.DetailJSON = "{}"
	}
}

func eq(q *gorm.DB, col, val string) *gorm.DB {
	val = strings.TrimSpace(val)
	if val == "" {
		return q
	}
	return q.Where(col+" = ?", val)
}

func cleanStrings(vals []string) []string {
	out := make([]string, 0, len(vals))
	seen := map[string]bool{}
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func mergeCallRecord(existing, next entity.MockCall) entity.MockCall {
	out := next
	out.ID = existing.ID
	if out.RecordID == "" {
		out.RecordID = existing.RecordID
	}
	if out.Scenario == "" || out.Scenario == "unknown" || (out.Scenario == "sip-call" && existing.Scenario != "") {
		out.Scenario = existing.Scenario
	}
	if out.Source == "" || (out.Source == "sip" && existing.Source != "") {
		out.Source = firstValue(existing.Source, out.Source)
	}
	out.RunID = firstValue(out.RunID, existing.RunID)
	out.OrgCode = firstValue(out.OrgCode, existing.OrgCode)
	out.TaskName = firstValue(out.TaskName, existing.TaskName)
	out.TaskCode = firstValue(out.TaskCode, existing.TaskCode)
	out.CustomerGroup = firstValue(out.CustomerGroup, existing.CustomerGroup)
	out.CustomerNumber = firstValue(out.CustomerNumber, existing.CustomerNumber)
	out.AgentGroupCode = firstValue(out.AgentGroupCode, existing.AgentGroupCode)
	out.AgentNumber = firstValue(out.AgentNumber, existing.AgentNumber)
	out.LineCode = firstValue(out.LineCode, existing.LineCode)
	out.LineAddress = firstValue(out.LineAddress, existing.LineAddress)
	out.LineName = firstValue(out.LineName, existing.LineName)
	out.Direction = firstValue(out.Direction, existing.Direction)
	out.CallType = firstValue(out.CallType, existing.CallType)
	out.ExpectOutcome = firstValue(out.ExpectOutcome, existing.ExpectOutcome)
	out.Result = firstValue(out.Result, existing.Result)
	out.TraceID = firstValue(out.TraceID, existing.TraceID)
	out.CallUUID = firstValue(out.CallUUID, existing.CallUUID)
	out.LastSummary = firstValue(out.LastSummary, existing.LastSummary)
	if statusRank(out.Status) < statusRank(existing.Status) {
		out.Status = existing.Status
	}
	if out.HangupCode == 0 {
		out.HangupCode = existing.HangupCode
	}
	if out.StartedAt.IsZero() || (!existing.StartedAt.IsZero() && existing.StartedAt.Before(out.StartedAt)) {
		out.StartedAt = existing.StartedAt
	}
	if out.AnsweredAt == nil {
		out.AnsweredAt = existing.AnsweredAt
	}
	if out.EndedAt == nil {
		out.EndedAt = existing.EndedAt
	}
	if out.DurationMs == 0 {
		out.DurationMs = existing.DurationMs
	}
	if out.LastEventAt.IsZero() || existing.LastEventAt.After(out.LastEventAt) {
		out.LastEventAt = existing.LastEventAt
	}
	if out.StepsJSON == "[]" || out.StepsJSON == "" {
		out.StepsJSON = existing.StepsJSON
	}
	out.DetailJSON = mergeJSONObjects(existing.DetailJSON, out.DetailJSON)
	normalizeCallRecord(&out)
	return out
}

func firstValue(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func statusRank(status string) int {
	switch status {
	case entity.CallRecordStatusFailed:
		return 7
	case entity.CallRecordStatusRejected:
		return 6
	case entity.CallRecordStatusEnded:
		return 5
	case entity.CallRecordStatusAnswered:
		return 4
	case entity.CallRecordStatusRinging:
		return 3
	case entity.CallRecordStatusPending:
		return 1
	default:
		return 0
	}
}

func mergeJSONObjects(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	if existing == "" || existing == "{}" {
		return next
	}
	if next == "" || next == "{}" {
		return existing
	}
	var a, b map[string]any
	if json.Unmarshal([]byte(existing), &a) != nil || json.Unmarshal([]byte(next), &b) != nil {
		return next
	}
	if len(a) == 0 {
		return next
	}
	for k, v := range b {
		a[k] = v
	}
	out, err := json.Marshal(a)
	if err != nil {
		return next
	}
	return string(out)
}
