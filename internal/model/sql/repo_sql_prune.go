package sql

import (
	"context"
	"time"

	"hermes-mock/internal/entity"
)

// 观测数据治理：周期清理早于 TTL 的观测行，防长期膨胀。配置表（行为档/客户组/个例/绑定/机构）不受影响。

// PruneObservations 删除 started_at/ts 早于 before 的观测行，返回删除总行数。
func (r *GormRepository) PruneObservations(ctx context.Context, before time.Time) (int64, error) {
	db := r.db.WithContext(ctx)
	var total int64

	// mock_call：按 started_at
	res := db.Where("started_at < ?", before).Delete(&entity.MockCall{})
	if res.Error != nil {
		return total, res.Error
	}
	total += res.RowsAffected

	// mock_trace_event：按 ts（先删事件，再删腿，避免悬挂事件）
	res = db.Where("ts < ?", before).Delete(&entity.TraceEvent{})
	if res.Error != nil {
		return total, res.Error
	}
	total += res.RowsAffected

	// mock_trace_leg：按 started_at
	res = db.Where("started_at < ?", before).Delete(&entity.TraceLeg{})
	if res.Error != nil {
		return total, res.Error
	}
	total += res.RowsAffected

	// mock_callback：按 ts
	res = db.Where("ts < ?", before).Delete(&entity.Callback{})
	if res.Error != nil {
		return total, res.Error
	}
	total += res.RowsAffected

	return total, nil
}
