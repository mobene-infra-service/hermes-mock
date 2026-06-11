// Package sql 是 Repository 的 GORM 实现（model/sql 子包）。
// 按表分文件：repo_sql_<table>.go；本文件只放基类与公共分页。
package sql

import (
	"gorm.io/gorm"

	"hermes-mock/internal/entity"
)

// GormRepository 基于 *gorm.DB 的 Repository 实现。
type GormRepository struct {
	db *gorm.DB
}

func NewGormRepository(db *gorm.DB) *GormRepository {
	return &GormRepository{db: db}
}

// DB 返回底层连接（仅供 seed/迁移等基础设施代码使用）。
func (r *GormRepository) DB() interface{} { return r.db }

// calculatePagination 统一分页元信息。
func (r *GormRepository) calculatePagination(totalCount int64, page, pageSize int) *entity.Meta {
	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	return &entity.Meta{Total: totalCount, Page: int64(page), PageSize: int64(pageSize)}
}
