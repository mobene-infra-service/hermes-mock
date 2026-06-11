package sql

import (
	"context"

	"hermes-mock/internal/entity"
)

// 测试运行历史（mock_test_run）。

func (r *GormRepository) CreateTestRun(ctx context.Context, row *entity.TestRun) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// ListTestRuns 最新在前（重启后内存清空时回读用）。
func (r *GormRepository) ListTestRuns(ctx context.Context, limit int) ([]entity.TestRun, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	var rows []entity.TestRun
	err := r.db.WithContext(ctx).Order("started_at DESC").Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
