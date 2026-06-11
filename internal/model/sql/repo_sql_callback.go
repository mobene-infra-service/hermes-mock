package sql

import (
	"context"
	"strings"

	"hermes-mock/internal/entity"
)

// Hermes 回调（mock_callback）。

func (r *GormRepository) CreateCallback(ctx context.Context, row *entity.Callback) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// ListCallbacks 按条件倒序返回（新→旧）。
func (r *GormRepository) ListCallbacks(ctx context.Context, f entity.CallbackFilter) ([]entity.Callback, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	db := r.db.WithContext(ctx).Model(&entity.Callback{})
	if v := strings.TrimSpace(f.Source); v != "" {
		db = db.Where("source = ?", v)
	}
	if v := strings.TrimSpace(f.Event); v != "" {
		db = db.Where("event = ?", v)
	}
	if v := strings.TrimSpace(f.OrgCode); v != "" {
		db = db.Where("org_code = ?", v)
	}
	if v := strings.TrimSpace(f.CallUUID); v != "" {
		db = db.Where("call_uuid = ?", v)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		db = db.Where("payload_json LIKE ?", "%"+kw+"%")
	}
	var rows []entity.Callback
	err := db.Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
