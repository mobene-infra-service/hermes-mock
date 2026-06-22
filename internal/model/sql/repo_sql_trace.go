package sql

import (
	"context"
	"strings"

	"hermes-mock/internal/entity"
)

// 通话链路（单腿 mock_trace_leg / mock_trace_event）。
// 单腿按 session_id upsert；查询时把事件装配进 TraceLeg.Events（gorm:"-" 字段）。
// 「一通业务通话含多腿」由读时按 call_uuid 归并（api 层），写入侧严格单腿。

func (r *GormRepository) SaveTraceSession(ctx context.Context, row *entity.TraceLeg) error {
	db := r.db.WithContext(ctx)
	var id int64
	if err := db.Table(row.TableName()).Where("session_id = ?", row.SessionID).
		Select("id").Limit(1).Scan(&id).Error; err != nil {
		return err
	}
	if id != 0 {
		row.ID = id
		return db.Save(row).Error
	}
	return db.Create(row).Error
}

func (r *GormRepository) CreateTraceEvents(ctx context.Context, rows []entity.TraceEvent) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&rows).Error
}

// ListTraceSessionSummaries 最近更新在前，只返回会话摘要与事件数，不读取 raw SIP 报文。
func (r *GormRepository) ListTraceSessionSummaries(ctx context.Context, limit int) ([]entity.TraceSessionSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	db := r.db.WithContext(ctx)
	var legs []entity.TraceLeg
	if err := db.Order("updated_at DESC").Limit(limit).Find(&legs).Error; err != nil {
		return nil, err
	}
	if len(legs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(legs))
	for _, leg := range legs {
		ids = append(ids, leg.SessionID)
	}
	type eventAgg struct {
		SessionID string
		LegsCSV   string
		Count     int
	}
	var aggs []eventAgg
	if err := db.Model(&entity.TraceEvent{}).
		Select("session_id, COUNT(*) AS count, GROUP_CONCAT(DISTINCT leg) AS legs_csv").
		Where("session_id IN ?", ids).
		Group("session_id").
		Scan(&aggs).Error; err != nil {
		return nil, err
	}
	bySession := map[string]eventAgg{}
	for _, agg := range aggs {
		bySession[agg.SessionID] = agg
	}
	out := make([]entity.TraceSessionSummary, 0, len(legs))
	for _, leg := range legs {
		agg := bySession[leg.SessionID]
		out = append(out, entity.TraceSessionSummary{
			SessionID:  leg.SessionID,
			CallUUID:   leg.CallUUID,
			Kind:       leg.Kind,
			Title:      leg.Title,
			StartedAt:  leg.StartedAt,
			UpdatedAt:  leg.UpdatedAt,
			Legs:       splitCSV(agg.LegsCSV),
			EventCount: agg.Count,
		})
	}
	return out, nil
}

// ListTraceSessions 最近更新在前，带完整事件（前端 trace 页列表依赖）。
func (r *GormRepository) ListTraceSessions(ctx context.Context, limit int) ([]entity.TraceLeg, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	db := r.db.WithContext(ctx)
	var rows []entity.TraceLeg
	if err := db.Order("updated_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.SessionID)
	}
	var evRows []entity.TraceEvent
	if err := db.Where("session_id IN ?", ids).Order("session_id ASC").Order("seq ASC").Find(&evRows).Error; err != nil {
		return nil, err
	}
	bySession := map[string][]entity.TraceEvent{}
	for _, e := range evRows {
		bySession[e.SessionID] = append(bySession[e.SessionID], e)
	}
	for i := range rows {
		rows[i].Events = bySession[rows[i].SessionID]
	}
	return rows, nil
}

func (r *GormRepository) TraceIDsByCallUUIDs(ctx context.Context, callUUIDs []string) (map[string]string, error) {
	callUUIDs = cleanStrings(callUUIDs)
	out := make(map[string]string, len(callUUIDs))
	if len(callUUIDs) == 0 {
		return out, nil
	}
	type row struct {
		CallUUID  string
		SessionID string
	}
	var rows []row
	if err := r.db.WithContext(ctx).Model(&entity.TraceLeg{}).
		Select("call_uuid, session_id").
		Where("call_uuid IN ?", callUUIDs).
		Order("started_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.CallUUID != "" && r.SessionID != "" {
			if _, exists := out[r.CallUUID]; !exists {
				out[r.CallUUID] = r.SessionID
			}
		}
	}
	return out, nil
}

// GetTraceSession 单腿带事件；不存在返回 (nil, nil)。
func (r *GormRepository) GetTraceSession(ctx context.Context, sessionID string) (*entity.TraceLeg, error) {
	db := r.db.WithContext(ctx)
	var row entity.TraceLeg
	tx := db.Where("session_id = ?", sessionID).Limit(1).Find(&row)
	if tx.Error != nil {
		return nil, tx.Error
	}
	if tx.RowsAffected == 0 {
		return nil, nil
	}
	var evRows []entity.TraceEvent
	if err := db.Where("session_id = ?", sessionID).Order("seq ASC").Find(&evRows).Error; err != nil {
		return nil, err
	}
	row.Events = evRows
	return &row, nil
}

// ListTraceLegsByCallUUID 取同一通业务通话的全部单腿（按 call_uuid），各带完整事件。
// 供读时把多腿归并成「一通通话含多腿 events」的视图（api 层装配；写入侧不做跨腿聚合）。
func (r *GormRepository) ListTraceLegsByCallUUID(ctx context.Context, callUUID string) ([]entity.TraceLeg, error) {
	if callUUID == "" {
		return nil, nil
	}
	db := r.db.WithContext(ctx)
	var rows []entity.TraceLeg
	if err := db.Where("call_uuid = ?", callUUID).Order("started_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.SessionID)
	}
	var evRows []entity.TraceEvent
	if err := db.Where("session_id IN ?", ids).Order("session_id ASC").Order("seq ASC").Find(&evRows).Error; err != nil {
		return nil, err
	}
	bySession := map[string][]entity.TraceEvent{}
	for _, e := range evRows {
		bySession[e.SessionID] = append(bySession[e.SessionID], e)
	}
	for i := range rows {
		rows[i].Events = bySession[rows[i].SessionID]
	}
	return rows, nil
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
