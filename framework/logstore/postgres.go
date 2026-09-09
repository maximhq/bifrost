package logstore

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/postgresconn"

	"gorm.io/gorm"
)

type PostgresConfig struct {
	postgresconn.Config
	// MatViewRefreshInterval controls how often the materialized views backing
	// /api/logs/stats and the dashboard histograms are refreshed. Accepts any
	// Go duration string ("30s", "5m", "1h"). Empty / unset uses the default
	// (defaultMatViewRefreshInterval). Raise this when refresh CPU cost is
	// material on the database instance — the matview path already has
	// activity-gated short-circuiting (see matViewRefreshGate), so the longer
	// interval mostly affects how quickly idle clusters notice the rolling
	// 30-day filter window has aged. Set "off" (or any non-positive duration)
	// to disable materialized-view maintenance entirely: views are neither
	// created nor refreshed and dashboard queries use the raw tables.
	MatViewRefreshInterval string `json:"matview_refresh_interval,omitempty"`

	// MatViewRefreshTimeout bounds a single refresh pass. A refresh holds a pooled
	// connection and a session-scoped advisory lock across every view; if it never
	// finishes, every replica's pg_try_advisory_lock fails from then on and the
	// views go permanently stale while still being served. Empty / unset derives a
	// budget from MatViewRefreshInterval.
	MatViewRefreshTimeout string `json:"matview_refresh_timeout,omitempty"`
}

func toPostgresConnConfig(config *PostgresConfig) *postgresconn.Config {
	if config == nil {
		return nil
	}
	return &config.Config
}

// defaultMatViewRefreshInterval is used when MatViewRefreshInterval is unset
// or unparseable.
const defaultMatViewRefreshInterval = time.Minute

// minMatViewRefreshInterval is a floor to prevent pathological configs that
// would refresh more often than the refresh itself takes — anything below
// this is clamped up. The activity-gate skip would mostly absorb the damage,
// but the floor stops misconfig from becoming a foot-gun.
const minMatViewRefreshInterval = 5 * time.Second

// matview refresh timeout bounds. A tick that outlives its budget is cancelled so
// the pooled connection and the session advisory lock are released; the next tick
// retries from scratch.
const (
	minMatViewRefreshTimeout = 30 * time.Second
	maxMatViewRefreshTimeout = 30 * time.Minute
	// matViewRefreshTimeoutFloor is the lower bound used when deriving a default
	// from the refresh interval.
	matViewRefreshTimeoutFloor = 5 * time.Minute
	// matViewRefreshTimeoutIntervalFactor scales the interval into a default budget;
	// a refresh is expected to finish well inside a few ticks.
	matViewRefreshTimeoutIntervalFactor = 5
)

// resolveMatViewRefreshTimeout parses the configured per-tick refresh timeout.
// When unset it derives a budget from the refresh interval rather than picking a
// fixed number, so a deliberately slow refresh cadence gets a proportionate one.
func resolveMatViewRefreshTimeout(raw string, interval time.Duration, logger schemas.Logger) time.Duration {
	derived := func() time.Duration {
		d := interval * matViewRefreshTimeoutIntervalFactor
		if d < matViewRefreshTimeoutFloor {
			d = matViewRefreshTimeoutFloor
		}
		if d > maxMatViewRefreshTimeout {
			d = maxMatViewRefreshTimeout
		}
		return d
	}

	if raw == "" {
		return derived()
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		fallback := derived()
		logger.Warn(fmt.Sprintf("logstore: invalid matview_refresh_timeout %q (%s); using %s", raw, err, fallback))
		return fallback
	}
	if d < minMatViewRefreshTimeout {
		logger.Warn(fmt.Sprintf("logstore: matview_refresh_timeout %s is below floor %s; clamping to %s", d, minMatViewRefreshTimeout, minMatViewRefreshTimeout))
		return minMatViewRefreshTimeout
	}
	if d > maxMatViewRefreshTimeout {
		logger.Warn(fmt.Sprintf("logstore: matview_refresh_timeout %s is above cap %s; clamping to %s", d, maxMatViewRefreshTimeout, maxMatViewRefreshTimeout))
		return maxMatViewRefreshTimeout
	}
	logger.Info(fmt.Sprintf("logstore: matview refresh timeout set to %s", d))
	return d
}

// resolveMatViewRefreshInterval parses the configured duration string with
// fallback + clamp. Logs a warning on a bad string so misconfig is noticed.
// Returns 0 when maintenance is disabled ("off" or a non-positive duration).
func resolveMatViewRefreshInterval(raw string, logger schemas.Logger) time.Duration {
	if raw == "" {
		return defaultMatViewRefreshInterval
	}
	if raw == "off" {
		logger.Info("logstore: matview maintenance disabled via config")
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		logger.Warn(fmt.Sprintf("logstore: invalid matview_refresh_interval %q (%s); using default %s", raw, err, defaultMatViewRefreshInterval))
		return defaultMatViewRefreshInterval
	}
	if d <= 0 {
		logger.Info("logstore: matview maintenance disabled via config")
		return 0
	}
	if d < minMatViewRefreshInterval {
		logger.Warn(fmt.Sprintf("logstore: matview_refresh_interval %s is below floor %s; clamping to %s", d, minMatViewRefreshInterval, minMatViewRefreshInterval))
		return minMatViewRefreshInterval
	}
	logger.Info(fmt.Sprintf("logstore: matview refresh interval set to %s", d))
	return d
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
	pgConfig := toPostgresConnConfig(config)
	if err := postgresconn.Validate(pgConfig, true); err != nil {
		return nil, err
	}
	dsn := postgresconn.BuildDSN(pgConfig)

	// Migration-only DSN. Forces pgx into simple-query protocol on the throwaway
	// migration pool so no statement plan is ever cached server-side; that makes
	// SQLSTATE 0A000 ("cached plan must not change result type") structurally
	// impossible when a migration mixes DDL with subsequent SELECTs against the
	// same table. Runtime pool keeps the default cache-statement mode.
	migrationDSN := dsn + " default_query_exec_mode=simple_protocol"

	openPool := func(connDSN string) (*gorm.DB, error) {
		return postgresconn.Open(connDSN, pgConfig, newGormLogger(logger))
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

	logger.Debug("logstore: postgres target host=%s port=%s db=%s sslmode=%s",
		config.Host.GetValue(), config.Port.GetValue(), config.DBName.GetValue(), config.SSLMode.GetValue())

	// Throwaway pool for the version gate and schema migrations. Closing it
	// before the runtime pool opens guarantees no cached plan survives DDL.
	logger.Info("logstore: opening migration connection pool (if this step hangs, the database host/port is likely unreachable)")
	mDb, err := openPool(migrationDSN)
	if err != nil {
		logger.Error("logstore: failed to open migration connection pool: %v", err)
		return nil, err
	}
	// Pin the throwaway pool small: migrations are serial DDL, and an untuned pool
	// inherits database/sql's unlimited MaxOpenConns.
	if err := postgresconn.ApplyMigrationPoolTuning(mDb); err != nil {
		logger.Error("logstore: failed to tune migration connection pool: %v", err)
		_ = closePool(mDb)
		return nil, err
	}

	// Postgres version gate: refuse to start below 16 (matviews, partitioning,
	// and some JSON operators we rely on depend on 16+).
	logger.Info("logstore: checking postgres server version (requires 16+)")
	var pgVersionNum int
	if err := mDb.Raw("SELECT current_setting('server_version_num')::int").Scan(&pgVersionNum).Error; err != nil {
		logger.Error("logstore: failed to read postgres server version: %v", err)
		_ = closePool(mDb)
		return nil, err
	}
	if pgVersionNum < 160000 {
		logger.Error("logstore: postgres server_version_num=%d is below the required minimum of 160000 (Postgres 16)", pgVersionNum)
		_ = closePool(mDb)
		return nil, fmt.Errorf("postgres version is lower than 16, please upgrade to 16 or higher")
	}
	logger.Info("logstore: postgres server_version_num=%d; running schema migrations (may block on a cross-node advisory lock if another pod is migrating)", pgVersionNum)

	if err := triggerMigrations(ctx, mDb, logger); err != nil {
		logger.Error("logstore: schema migrations failed: %v", err)
		_ = closePool(mDb)
		return nil, err
	}
	logger.Info("logstore: schema migrations complete; closing migration pool")
	if err := closePool(mDb); err != nil {
		return nil, fmt.Errorf("close migration db connection: %w", err)
	}

	// Runtime pool. Opens against post-migration schema.
	logger.Info("logstore: opening runtime connection pool")
	db, err := openPool(dsn)
	if err != nil {
		logger.Error("logstore: failed to open runtime connection pool: %v", err)
		return nil, err
	}

	if err := postgresconn.ApplyPoolTuning(db, pgConfig, logger); err != nil {
		closePool(db)
		return nil, err
	}
	logger.Info("logstore: runtime connection pool ready")
	refreshInterval := resolveMatViewRefreshInterval(config.MatViewRefreshInterval, logger)
	d := &RDBLogStore{db: db, logger: logger, matViewMaintenanceDisabled: refreshInterval <= 0}

	// Run all index builds sequentially in a single goroutine to prevent
	// deadlocks from concurrent CREATE INDEX CONCURRENTLY on the same table.
	// Each function is idempotent and acquires its own advisory lock for
	// cross-node serialization. Running in a goroutine avoids blocking pod startup.
	go func() {
		if db.Dialector.Name() != "postgres" {
			return
		}
		// Acquire advisory lock to serialize GIN index builds across cluster nodes.
		lock, err := acquireIndexLock(context.Background(), db, logger)
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

		if err := ensureMultiTeamBusinessUnitGINIndexes(context.Background(), lock.conn); err != nil {
			logger.Warn(fmt.Sprintf("logstore: team/business-unit GIN index build failed: %s (filtering will still work without the index)", err))
		} else {
			logger.Info("logstore: team/business-unit GIN indexes are ready")
		}

		if err := ensureDashboardEnhancements(context.Background(), lock.conn); err != nil {
			logger.Warn(fmt.Sprintf("logstore: dashboard enhancements failed: %s (dashboard will still work with partial data)", err))
		} else {
			logger.Info("logstore: dashboard enhancements completed")
		}

		if err := ensurePerformanceIndexes(context.Background(), lock.conn, logger); err != nil {
			logger.Warn(fmt.Sprintf("logstore: performance index build failed: %s (queries will still work without the indexes)", err))
		} else {
			logger.Info("logstore: performance indexes are ready")
		}
	}()

	// Create materialized views and start periodic refresh for dashboard queries.
	go func() {
		if db.Dialector.Name() != "postgres" || refreshInterval <= 0 {
			return
		}
		if err := ensureMatViews(context.Background(), db); err != nil {
			logger.Warn(fmt.Sprintf("logstore: matview creation failed: %s (dashboard queries will use raw tables)", err))
			return
		}
		refreshTimeout := resolveMatViewRefreshTimeout(config.MatViewRefreshTimeout, refreshInterval, logger)

		// The initial refresh gets the same budget as a periodic tick; on a large
		// logs table it can be slow, and it must not hold the advisory lock forever.
		initialCtx, cancelInitial := context.WithTimeout(context.Background(), refreshTimeout)
		err := refreshMatViews(initialCtx, db)
		cancelInitial()
		if err != nil {
			logger.Warn(fmt.Sprintf("logstore: initial matview refresh failed: %s", err))
		} else {
			logger.Info("logstore: materialized views are ready")
			// Signal that matviews are ready for query use. Until this point,
			// canUseMatView() returns false so all queries use raw tables.
			d.matViewsReady.Store(true)
		}
		startMatViewRefresher(context.Background(), db, refreshInterval, refreshTimeout, logger, &d.matViewsReady)
	}()

	return d, nil
}
