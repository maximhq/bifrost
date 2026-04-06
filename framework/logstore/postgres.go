package logstore

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// PostgresConfig represents the configuration for a Postgres database.
type PostgresConfig struct {
	Host         *schemas.EnvVar `json:"host"`
	Port         *schemas.EnvVar `json:"port"`
	User         *schemas.EnvVar `json:"user"`
	Password     *schemas.EnvVar `json:"password"`
	DBName       *schemas.EnvVar `json:"db_name"`
	SSLMode      *schemas.EnvVar `json:"ssl_mode"`
	MaxIdleConns int             `json:"max_idle_conns"`
	MaxOpenConns int             `json:"max_open_conns"`
}

// newPostgresLogStore creates a new Postgres log store.
func newPostgresLogStore(ctx context.Context, config *PostgresConfig, logger schemas.Logger) (LogStore, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	// Validate required config
	if config.Host == nil || config.Host.GetValue() == "" {
		return nil, fmt.Errorf("postgres host is required")
	}
	if config.Port == nil || config.Port.GetValue() == "" {
		return nil, fmt.Errorf("postgres port is required")
	}
	if config.User == nil || config.User.GetValue() == "" {
		return nil, fmt.Errorf("postgres user is required")
	}
	if config.Password == nil || config.Password.GetValue() == "" {
		return nil, fmt.Errorf("postgres password is required")
	}
	if config.DBName == nil || config.DBName.GetValue() == "" {
		return nil, fmt.Errorf("postgres db name is required")
	}
	if config.SSLMode == nil || config.SSLMode.GetValue() == "" {
		return nil, fmt.Errorf("postgres ssl mode is required")
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s", config.Host.GetValue(), config.Port.GetValue(), config.User.GetValue(), config.Password.GetValue(), config.DBName.GetValue(), config.SSLMode.GetValue())
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: dsn,
	}), &gorm.Config{
		Logger: newGormLogger(logger),
	})
	if err != nil {
		return nil, err
	}

	// Configure connection pool
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// Set MaxIdleConns (default: 5)
	maxIdleConns := config.MaxIdleConns
	if maxIdleConns == 0 {
		maxIdleConns = 5
	}
	sqlDB.SetMaxIdleConns(maxIdleConns)

	// Set MaxOpenConns (default: 50)
	maxOpenConns := config.MaxOpenConns
	if maxOpenConns == 0 {
		maxOpenConns = 50
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	d := &RDBLogStore{db: db, logger: logger}

	// Check version of postgres, if is lower than 16, throw fatal error
	var pgVersionNum int
	if err := db.Raw("SELECT current_setting('server_version_num')::int").Scan(&pgVersionNum).Error; err != nil {
		sqlDB.Close()
		return nil, err
	}
	if pgVersionNum < 160000 {
		sqlDB.Close()
		return nil, fmt.Errorf("postgres version is lower than 16, please upgrade to 16 or higher")
	}

	// Run migrations
	if err := triggerMigrations(ctx, db); err != nil {
		if sqlDB, sqlErr := db.DB(); sqlErr == nil {
			sqlDB.Close()
		}
		return nil, err
	}

	// Run all index builds sequentially in a single goroutine to prevent
	// deadlocks from concurrent CREATE INDEX CONCURRENTLY on the same table.
	// Each function is idempotent and acquires its own advisory lock for
	// cross-node serialization. Running in a goroutine avoids blocking pod startup.
	go func() {
		if db.Dialector.Name() != "postgres" {
			return
		}
		// Acquire advisory lock to serialize GIN index builds across cluster nodes.
		lock, err := acquireIndexLock(context.Background(), db)
		if err != nil {
			// Lock is taken by another node, so we will skip the index build
			return
		}
		defer lock.release(context.Background())

		if err := ensureMetadataGINIndex(context.Background(), lock.conn); err != nil {
			logger.Warn(fmt.Sprintf("logstore: metadata GIN index build failed: %s (queries will still work without the index)", err))
		} else {
			logger.Info("logstore: metadata GIN index is ready")
		}

		if err := ensureDashboardEnhancements(context.Background(), lock.conn); err != nil {
			logger.Warn(fmt.Sprintf("logstore: dashboard enhancements failed: %s (dashboard will still work with partial data)", err))
		} else {
			logger.Info("logstore: dashboard enhancements completed")
		}

		if err := ensurePerformanceIndexes(context.Background(), lock.conn); err != nil {
			logger.Warn(fmt.Sprintf("logstore: performance index build failed: %s (queries will still work without the indexes)", err))
		} else {
			logger.Info("logstore: performance indexes are ready")
		}
	}()

	// Create materialized views and start periodic refresh for dashboard queries.
	go func() {
		if db.Dialector.Name() != "postgres" {
			return
		}
		if err := ensureMatViews(context.Background(), db); err != nil {
			logger.Warn(fmt.Sprintf("logstore: matview creation failed: %s (dashboard queries will use raw tables)", err))
			return
		}
		if err := refreshMatViews(context.Background(), db); err != nil {
			logger.Warn(fmt.Sprintf("logstore: initial matview refresh failed: %s", err))
		} else {
			logger.Info("logstore: materialized views are ready")
			// Signal that matviews are ready for query use. Until this point,
			// canUseMatView() returns false so all queries use raw tables.
			d.matViewsReady.Store(true)
		}
		startMatViewRefresher(context.Background(), db, 30*time.Second, logger, &d.matViewsReady)
	}()

	return d, nil
}
