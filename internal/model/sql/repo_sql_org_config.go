package sql

import (
	"context"
	"time"

	"gorm.io/gorm"

	"hermes-mock/internal/entity"
)

// 机构 OpenAPI 接入配置（mock_org_config）。

func (r *GormRepository) ListOrgConfigs(ctx context.Context) ([]entity.OrgConfig, error) {
	var rows []entity.OrgConfig
	err := r.db.WithContext(ctx).Order("id").Find(&rows).Error
	return rows, err
}

// UpsertOrgConfig 按 org_code 唯一 upsert。
func (r *GormRepository) UpsertOrgConfig(ctx context.Context, c *entity.OrgConfig) error {
	c.GmtModified = time.Now()
	db := r.db.WithContext(ctx)
	var existing entity.OrgConfig
	err := db.Where("org_code = ?", c.OrgCode).First(&existing).Error
	if err == gorm.ErrRecordNotFound {
		return db.Create(c).Error
	}
	if err != nil {
		return err
	}
	c.ID = existing.ID
	return db.Model(&entity.OrgConfig{}).Where("id = ?", existing.ID).Save(c).Error
}

func (r *GormRepository) DeleteOrgConfig(ctx context.Context, orgCode string) error {
	return r.db.WithContext(ctx).Where("org_code = ?", orgCode).Delete(&entity.OrgConfig{}).Error
}
