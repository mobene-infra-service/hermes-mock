package sql

import (
	"context"
	"errors"
	"time"

	"hermes-mock/internal/entity"
)

// 观测数据治理：周期清理早于 TTL 的观测行，防长期膨胀。配置表（行为档/客户组/个例/绑定/机构）不受影响。

// pruneBatch 单批删除行数。分批删（DELETE ... LIMIT）避免在大表（千万行级）上单条 DELETE
// 全表扫 + 大事务锁表/超时——这正是历史上 mock_trace_event 被扫爆后清理删不动的根因。
const pruneBatch = 5000

// PruneObservations 删除 started_at/ts 早于 before 的观测行，返回删除总行数。
// 每张表分批循环删，直到删完或 ctx 到期（到期则本轮删多少算多少，余量下轮继续，不算失败）。
// 删除顺序：先事件（量最大）→ HTTP Mock 调用记录 → 呼叫记录 → 链路腿 → 回调。
func (r *GormRepository) PruneObservations(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	steps := []struct {
		model any
		cond  string
	}{
		{&entity.TraceEvent{}, "ts < ?"},               // 按 ts（已加 idx_event_ts，走索引）
		{&entity.HTTPMockRequest{}, "received_at < ?"}, // 按 received_at（idx_http_req_received）
		{&entity.MockCall{}, "started_at < ?"},         // 按 started_at（有复合索引含 started_at）
		{&entity.TraceLeg{}, "started_at < ?"},         // 按 started_at（idx_leg_time）
		{&entity.Callback{}, "ts < ?"},                 // 按 ts（idx_cb_ts）
	}
	for _, s := range steps {
		n, err := r.pruneTable(ctx, s.model, s.cond, before)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// pruneTable 对单表分批删除满足 cond 的行；ctx 到期即停（返回已删行数，不报错，余量下轮继续）。
func (r *GormRepository) pruneTable(ctx context.Context, model any, cond string, arg any) (int64, error) {
	var sub int64
	for {
		if ctx.Err() != nil {
			return sub, nil // ctx 到期：本轮删到此为止，余量下个周期继续
		}
		res := r.db.WithContext(ctx).Where(cond, arg).Limit(pruneBatch).Delete(model)
		if res.Error != nil {
			// 批执行中途 ctx 到期同样算正常停（与批间检查语义一致），不当失败上报。
			if errors.Is(res.Error, context.DeadlineExceeded) || errors.Is(res.Error, context.Canceled) || ctx.Err() != nil {
				return sub, nil
			}
			return sub, res.Error
		}
		sub += res.RowsAffected
		if res.RowsAffected < pruneBatch {
			return sub, nil // 不足一批 → 已删完
		}
	}
}
