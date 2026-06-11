package sql

import (
	"context"

	"hermes-mock/internal/entity"
)

// 通话链路（mock_trace_session / mock_trace_event）。
// 会话按 session_id upsert；查询时把事件装配进 Session.Events（gorm:"-" 字段）。

func (r *GormRepository) SaveTraceSession(ctx context.Context, row *entity.TraceSession) error {
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

// ListTraceSessions 最近更新在前，带完整事件（前端 trace 页列表依赖）。
func (r *GormRepository) ListTraceSessions(ctx context.Context, limit int) ([]entity.TraceSession, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	db := r.db.WithContext(ctx)
	var rows []entity.TraceSession
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

// GetTraceSession 单条会话带事件；不存在返回 (nil, nil)。
func (r *GormRepository) GetTraceSession(ctx context.Context, sessionID string) (*entity.TraceSession, error) {
	db := r.db.WithContext(ctx)
	var row entity.TraceSession
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
