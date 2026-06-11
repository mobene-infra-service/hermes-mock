package sql

import (
	"context"

	"hermes-mock/internal/entity"
)

// 客户集群配置四件套：行为档 / 客户组 / 个例 / 线路绑定。
// Upsert 语义：按业务键（code/number/line_code）查现有 id，有则 Save 无则 Create——
// 与原 cluster.Store.upsert 行为一致（业务键无唯一索引，靠应用层保证）。

func (r *GormRepository) upsertByKey(ctx context.Context, model interface{ TableName() string }, setID func(int64), where string, args ...any) error {
	var id int64
	if err := r.db.WithContext(ctx).Table(model.TableName()).Where(where, args...).Select("id").Limit(1).Scan(&id).Error; err != nil {
		return err
	}
	if id != 0 {
		setID(id)
		return r.db.WithContext(ctx).Save(model).Error
	}
	return r.db.WithContext(ctx).Create(model).Error
}

// ---- BehaviorProfile ----

func (r *GormRepository) ListBehaviorProfiles(ctx context.Context) ([]entity.BehaviorProfile, error) {
	var rows []entity.BehaviorProfile
	err := r.db.WithContext(ctx).Find(&rows).Error
	return rows, err
}

func (r *GormRepository) UpsertBehaviorProfile(ctx context.Context, p *entity.BehaviorProfile) error {
	return r.upsertByKey(ctx, p, func(id int64) { p.ID = id }, "code = ?", p.Code)
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
	return r.upsertByKey(ctx, g, func(id int64) { g.ID = id }, "code = ?", g.Code)
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
	return r.upsertByKey(ctx, o, func(id int64) { o.ID = id }, "number = ?", o.Number)
}

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
	return r.upsertByKey(ctx, b, func(id int64) { b.ID = id }, "line_code = ?", b.LineCode)
}

func (r *GormRepository) DeleteLineBinding(ctx context.Context, lineCode string) error {
	return r.db.WithContext(ctx).Where("line_code = ?", lineCode).Delete(&entity.LineBinding{}).Error
}
