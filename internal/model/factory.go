package model

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"hermes-mock/internal/config"
	"hermes-mock/internal/entity"
	"hermes-mock/internal/model/sql"
)

// 支持的数据库类型（默认 mysql，本地零依赖跑 sqlite）。
const (
	DBTypeMySQL  = "mysql"
	DBTypeSQLite = "sqlite"
)

// RepositoryFactory 按配置创建 Repository。
type RepositoryFactory struct{}

func NewRepositoryFactory() *RepositoryFactory { return &RepositoryFactory{} }

// InitRepository 入口：按 cfg.DBType 创建 Repository。
// hermes_mock 是 mock 的唯一持久化（配置/记录/链路/回调都在库里），必配——失败直接返回错误。
func InitRepository(cfg *config.Config) (Repository, error) {
	return NewRepositoryFactory().CreateRepository(cfg)
}

// CreateRepository 按 DBType 分发。
func (f *RepositoryFactory) CreateRepository(cfg *config.Config) (Repository, error) {
	switch cfg.DBType {
	case DBTypeMySQL, "":
		return f.createMySQLRepository(cfg)
	case DBTypeSQLite:
		return f.createSQLiteRepository(cfg)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", cfg.DBType)
	}
}

func (f *RepositoryFactory) createMySQLRepository(cfg *config.Config) (Repository, error) {
	dsn := cfg.DSNURL
	if dsn == "" {
		if cfg.DBAddr == "" {
			return nil, fmt.Errorf("hermes_mock 库未配置（DSN_URL 整串，或 DBAddr + MYSQL_MASTER_PASSWORD 组件拼装；本地零依赖可 DBType=sqlite）")
		}
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=UTC",
			cfg.DBUser, cfg.DBPassword, cfg.DBAddr, cfg.DBPort, cfg.DBName)
	}
	db, err := f.openGormDB(mysql.Open(dsn))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MySQL: %w", err)
	}
	if err := f.migrateSchema(db); err != nil {
		return nil, fmt.Errorf("failed to migrate schema: %w", err)
	}
	return sql.NewGormRepository(db), nil
}

func (f *RepositoryFactory) createSQLiteRepository(cfg *config.Config) (Repository, error) {
	filePath := cfg.DBPath
	if filePath == "" {
		filePath = "datas/hermes-mock.db"
	}
	// SQLite 连接时自动建 .db 文件，但目录必须先存在。
	if dir := filepath.Dir(filePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create directory %q: %w", dir, err)
		}
	}
	db, err := f.openGormDB(sqlite.Open(filePath))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SQLite: %w", err)
	}
	if err := f.migrateSchema(db); err != nil {
		return nil, fmt.Errorf("failed to migrate schema: %w", err)
	}
	return sql.NewGormRepository(db), nil
}

// openGormDB 统一连接配置（慢查询阈值/连接池）。
func (f *RepositoryFactory) openGormDB(dialector gorm.Dialector) (*gorm.DB, error) {
	gormLogger := logger.New(
		log.New(log.Writer(), "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             time.Second * 5,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)
	db, err := gorm.Open(dialector, &gorm.Config{
		Logger:                                   gormLogger,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)
	return db, nil
}

// migrateSchema AutoMigrate 全部实体。mock 是测试工具、库为独立 hermes_mock，
// 启动时无条件迁移（不像业务系统区分 DEVM）——保证新列随版本自动到位。
func (f *RepositoryFactory) migrateSchema(db *gorm.DB) error {
	return db.AutoMigrate(
		&entity.BehaviorProfile{},
		&entity.CustomerGroup{},
		&entity.CustomerOverride{},
		&entity.LineBinding{},
		&entity.CallRecord{},
		&entity.TestRun{},
		&entity.TraceSession{},
		&entity.TraceEvent{},
		&entity.Callback{},
		&entity.OrgConfig{},
	)
}
