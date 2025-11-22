package logstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"

	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
)

type ClickHouseConfig struct {
	Host         string `json:"host"`
	Port         string `json:"port"`
	DBName       string `json:"db_name"`
	User         string `json:"user"`
	Password     string `json:"password"`
	MaxIdleConns int    `json:"max_idle_conns"`
	MaxOpenConns int    `json:"max_open_conns"`
}

func newClickHouseLogStore(ctx context.Context, config *ClickHouseConfig, logger schemas.Logger) (LogStore, error) {
	dsn := fmt.Sprintf("clickhouse://%s:%s@%s:%s/%s",
		config.User, config.Password, config.Host, config.Port, config.DBName)

	db, err := gorm.Open(clickhouse.Open(dsn), &gorm.Config{
		Logger: newGormLogger(logger),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	maxIdleConns := config.MaxIdleConns
	if maxIdleConns == 0 {
		maxIdleConns = 5
	}
	sqlDB.SetMaxIdleConns(maxIdleConns)

	maxOpenConns := config.MaxOpenConns
	if maxOpenConns == 0 {
		maxOpenConns = 50
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)

	d := &RDBLogStore{db: db, logger: logger}
	if err := triggerClickhouseMigrations(ctx, db); err != nil {
		if sqlDB, sqlErr := db.DB(); sqlErr == nil {
			sqlDB.Close()
		}
		return nil, err
	}
	return d, nil
}
