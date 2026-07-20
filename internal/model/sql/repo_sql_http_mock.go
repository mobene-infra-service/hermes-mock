package sql

import (
	"context"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"hermes-mock/internal/entity"
)

// 通用 HTTP Mock 配置与调用记录。

func (r *GormRepository) ListHTTPMockEndpoints(ctx context.Context) ([]entity.HTTPMockEndpoint, error) {
	var rows []entity.HTTPMockEndpoint
	err := r.db.WithContext(ctx).Order("id ASC").Find(&rows).Error
	return rows, err
}

func (r *GormRepository) UpsertHTTPMockEndpoint(ctx context.Context, row *entity.HTTPMockEndpoint) error {
	if row.ID > 0 {
		return r.db.WithContext(ctx).Save(row).Error
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "token"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "enabled", "config_json", "remark", "gmt_modified"}),
	}).Create(row).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Where("token = ?", row.Token).First(row).Error
}

func (r *GormRepository) DeleteHTTPMockEndpoint(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("endpoint_id = ?", id).Delete(&entity.HTTPMockRequest{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&entity.HTTPMockEndpoint{}).Error
	})
}

func (r *GormRepository) CreateHTTPMockRequest(ctx context.Context, row *entity.HTTPMockRequest) error {
	return r.db.WithContext(ctx).Create(row).Error
}

func (r *GormRepository) ListHTTPMockRequests(ctx context.Context, f entity.HTTPMockRequestFilter) ([]entity.HTTPMockRequest, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	db := r.db.WithContext(ctx).Model(&entity.HTTPMockRequest{})
	if f.EndpointID > 0 {
		db = db.Where("endpoint_id = ?", f.EndpointID)
	}
	if v := strings.TrimSpace(f.Token); v != "" {
		db = db.Where("token = ?", v)
	}
	if v := strings.TrimSpace(f.Method); v != "" {
		db = db.Where("method = ?", strings.ToUpper(v))
	}
	if v := strings.TrimSpace(f.MatchedRule); v != "" {
		db = db.Where("matched_rule = ?", v)
	}
	if v := strings.TrimSpace(f.SelectedCase); v != "" {
		db = db.Where("selected_case = ?", v)
	}
	if v := strings.TrimSpace(f.Keyword); v != "" {
		like := "%" + v + "%"
		db = db.Where("request_body LIKE ? OR query_json LIKE ? OR response_body LIKE ?", like, like, like)
	}
	var rows []entity.HTTPMockRequest
	err := db.Order("received_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (r *GormRepository) DeleteHTTPMockRequests(ctx context.Context, endpointID int64) (int64, error) {
	db := r.db.WithContext(ctx).Where("endpoint_id = ?", endpointID).Delete(&entity.HTTPMockRequest{})
	return db.RowsAffected, db.Error
}
