package sql

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"hermes-mock/internal/entity"
)

// 机构 OpenAPI 接入配置（mock_org_config）。

func (r *GormRepository) ListOrgConfigs(ctx context.Context) ([]entity.OrgConfig, error) {
	var rows []entity.OrgConfig
	err := r.db.WithContext(ctx).Order("id").Find(&rows).Error
	return rows, err
}

// UpsertOrgConfig 按 org_code 唯一 upsert（ON CONFLICT，消除先查后写竞态）。
func (r *GormRepository) UpsertOrgConfig(ctx context.Context, c *entity.OrgConfig) error {
	c.GmtModified = time.Now()
	db := r.db.WithContext(ctx)
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "org_code"}},
		UpdateAll: true,
	}).Create(c).Error; err != nil {
		return err
	}
	if c.ID == 0 {
		var id int64
		if err := db.Table(c.TableName()).Where("org_code = ?", c.OrgCode).Select("id").Limit(1).Scan(&id).Error; err != nil {
			return err
		}
		c.ID = id
	}
	return nil
}

func (r *GormRepository) DeleteOrgConfig(ctx context.Context, orgCode string) error {
	return r.db.WithContext(ctx).Where("org_code = ?", orgCode).Delete(&entity.OrgConfig{}).Error
}
