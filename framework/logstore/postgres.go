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
//
// Uses a two-pool lifecycle to avoid SQLSTATE 0A000 ("cached plan must not
// change result type"): a throwaway pool runs the version check and schema
// migrations and is closed immediately, then a fresh runtime pool is opened
// for query traffic and the async index / matview builders. The runtime
// pool's connections never see pre-migration schema, so their cached
// prepared-plans stay valid for the life of the process.
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

	// Migration-only DSN. Forces pgx into simple-query protocol on the throwaway
	// migration pool so no statement plan is ever cached server-side; that makes
	// SQLSTATE 0A000 ("cached plan must not change result type") structurally
	// impossible when a migration mixes DDL with subsequent SELECTs against the
	// same table. Runtime pool keeps the default cache-statement mode.
	migrationDSN := dsn + " default_query_exec_mode=simple_protocol"

	openPool := func(connDSN string) (*gorm.DB, error) {
		return gorm.Open(postgres.New(postgres.Config{DSN: connDSN}), &gorm.Config{
			Logger: newGormLogger(logger),
		})
	}

	// closePoolStrict returns the close error so callers can abort startup
	// when the throwaway migration pool doesn't tear down cleanly — a half-
	// closed pool weakens the guarantee that no cached plans survive DDL.
	closePool := func(db *gorm.DB) error {
		if db == nil {
			return nil
		}
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}

	// Throwaway pool for the version gate and schema migrations. Closing it
	// before the runtime pool opens guarantees no cached plan survives DDL.
	mDb, err := openPool(migrationDSN)
	if err != nil {
		return nil, err
	}

	// Postgres version gate: refuse to start below 16 (matviews, partitioning,
	// and some JSON operators we rely on depend on 16+).
	var pgVersionNum int
	if err := mDb.Raw("SELECT current_setting('server_version_num')::int").Scan(&pgVersionNum).Error; err != nil {
		_ = closePool(mDb)
		return nil, err
	}
	if pgVersionNum < 160000 {
		_ = closePool(mDb)
		return nil, fmt.Errorf("postgres version is lower than 16, please upgrade to 16 or higher")
	}

	if err := triggerMigrations(ctx, mDb); err != nil {
		_ = closePool(mDb)
		return nil, err
	}
	if err := closePool(mDb); err != nil {
		return nil, fmt.Errorf("close migration db connection: %w", err)
	}

	// Runtime pool. Opens against post-migration schema.
	db, err := openPool(dsn)
	if err != nil {
		return nil, err
	}

	// Configure connection pool
	sqlDB, err := db.DB()
	if err != nil {
		closePool(db)
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
