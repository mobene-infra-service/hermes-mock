package sql

import (
	"context"

	"gorm.io/gorm/clause"

	"hermes-mock/internal/entity"
)

// 客户集群配置四件套：行为档 / 客户组 / 个例 / 入口端口绑定。
// Upsert 语义：按业务唯一键（code / (group_code,number) / listen_port）用 ON CONFLICT 原子写入，
// 冲突即整行更新——消除「先 SELECT 再 Create/Save」的并发竞态（业务键已有唯一索引兜底）。

// upsertByKey 按 conflictCols（须有唯一索引）做 upsert，写后按 where 回查 id 回填到调用方缓存对象
// （OnConflict 命中更新分支时不保证回填自增 id）。
func (r *GormRepository) upsertByKey(ctx context.Context, model interface{ TableName() string }, setID func(int64), conflictCols []string, where string, args ...any) error {
	cols := make([]clause.Column, len(conflictCols))
	for i, c := range conflictCols {
		cols[i] = clause.Column{Name: c}
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   cols,
		UpdateAll: true,
	}).Create(model).Error; err != nil {
		return err
	}
	var id int64
	if err := r.db.WithContext(ctx).Table(model.TableName()).Where(where, args...).Select("id").Limit(1).Scan(&id).Error; err != nil {
		return err
	}
	setID(id)
	return nil
}

// ---- BehaviorProfile ----

func (r *GormRepository) ListBehaviorProfiles(ctx context.Context) ([]entity.BehaviorProfile, error) {
	var rows []entity.BehaviorProfile
	err := r.db.WithContext(ctx).Find(&rows).Error
	return rows, err
}

func (r *GormRepository) UpsertBehaviorProfile(ctx context.Context, p *entity.BehaviorProfile) error {
	return r.upsertByKey(ctx, p, func(id int64) { p.ID = id }, []string{"code"}, "code = ?", p.Code)
}

func (r *GormRepository) DeleteBehaviorProfile(ctx context.Context, code string) error {
	return r.db.WithContext(ctx).Where("code = ?", code).Delete(&entity.BehaviorProfile{}).Error
}

// ---- CustomerGroup ----

func (r *GormRepository) ListCustomerGroups(ctx context.Context) ([]entity.CustomerGroup, error) {
	var rows []entity.CustomerGroup
	err := r.db.WithContext(ctx).Find(&rows).Error
	return rows, err
}

func (r *GormRepository) UpsertCustomerGroup(ctx context.Context, g *entity.CustomerGroup) error {
	return r.upsertByKey(ctx, g, func(id int64) { g.ID = id }, []string{"code"}, "code = ?", g.Code)
}

func (r *GormRepository) DeleteCustomerGroup(ctx context.Context, code string) error {
	return r.db.WithContext(ctx).Where("code = ?", code).Delete(&entity.CustomerGroup{}).Error
}

// ---- CustomerOverride ----

func (r *GormRepository) ListCustomerOverrides(ctx context.Context) ([]entity.CustomerOverride, error) {
	var rows []entity.CustomerOverride
	err := r.db.WithContext(ctx).Find(&rows).Error
	return rows, err
}

func (r *GormRepository) UpsertCustomerOverride(ctx context.Context, o *entity.CustomerOverride) error {
	return r.upsertByKey(ctx, o, func(id int64) { o.ID = id }, []string{"group_code", "number"}, "group_code = ? AND number = ?", o.GroupCode, o.Number)
}

// DeleteCustomerOverride 按号码删个例。复合唯一 (group_code,number) 下同号可跨组多条，
// 这里删该号的全部个例（与 api 的 DELETE /cluster/overrides/:number 契约一致）。
func (r *GormRepository) DeleteCustomerOverride(ctx context.Context, number string) error {
	return r.db.WithContext(ctx).Where("number = ?", number).Delete(&entity.CustomerOverride{}).Error
}

// ---- LineBinding ----

func (r *GormRepository) ListLineBindings(ctx context.Context) ([]entity.LineBinding, error) {
	var rows []entity.LineBinding
	err := r.db.WithContext(ctx).Find(&rows).Error
	return rows, err
}

func (r *GormRepository) UpsertLineBinding(ctx context.Context, b *entity.LineBinding) error {
	return r.upsertByKey(ctx, b, func(id int64) { b.ID = id }, []string{"listen_port"}, "listen_port = ?", b.ListenPort)
}

func (r *GormRepository) DeleteLineBinding(ctx context.Context, listenPort int) error {
	return r.db.WithContext(ctx).Where("listen_port = ?", listenPort).Delete(&entity.LineBinding{}).Error
}
