package logstore

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/migrator"
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
	// 30-day filter window has aged. Set to "off" (or "0") to disable the
	// periodic refresher entirely and refresh out of band instead (a
	// --matview-refresh-only cron job or pg_cron); the boot-time
	// create/repair and initial refresh still run.
	MatViewRefreshInterval string `json:"matview_refresh_interval,omitempty"`
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

// resolveMatViewRefreshInterval parses the configured duration string with
// fallback + clamp. Logs a warning on a bad string so misconfig is noticed.
func resolveMatViewRefreshInterval(raw string, logger schemas.Logger) time.Duration {
	if raw == "" {
		return defaultMatViewRefreshInterval
	}
	if raw == "off" || raw == "0" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		logger.Warn("logstore: invalid matview_refresh_interval %q (%v); using default %s", raw, err, defaultMatViewRefreshInterval)
		return defaultMatViewRefreshInterval
	}
	if d == 0 {
		return 0
	}
	if d < minMatViewRefreshInterval {
		logger.Warn("logstore: matview_refresh_interval %s is below floor %s; clamping to %s", d, minMatViewRefreshInterval, minMatViewRefreshInterval)
		return minMatViewRefreshInterval
	}
	logger.Info("logstore: matview refresh interval set to %s", d)
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

	if err := postgresconn.ApplyPoolTuning(db, pgConfig); err != nil {
		closePool(db)
		return nil, err
	}
	logger.Info("logstore: runtime connection pool ready")
	d := &RDBLogStore{db: db, logger: logger}

	// Run all index builds sequentially to prevent deadlocks from concurrent
	// CREATE INDEX CONCURRENTLY on the same table. Each function is idempotent
	// and the advisory lock serializes builds across cluster nodes.
	runIndexMaintenance := func() {
		if db.Dialector.Name() != "postgres" {
			return
		}
		// Acquire advisory lock to serialize GIN index builds across cluster nodes.
		lock, err := acquireIndexLock(context.Background(), db, logger)
		if err != nil {
			// Lock is taken by another node, so we will skip the index build
			logger.Warn("logstore: skipping index maintenance, could not acquire index lock: %v", err)
			return
		}
		defer lock.release(context.Background())

		if err := ensureMetadataGINIndex(context.Background(), lock.conn); err != nil {
			logger.Warn("logstore: metadata GIN index build failed: %v (queries will still work without the index)", err)
		} else {
			logger.Info("logstore: metadata GIN index is ready")
		}

		if err := ensureMultiTeamBusinessUnitGINIndexes(context.Background(), lock.conn); err != nil {
			logger.Warn("logstore: team/business-unit GIN index build failed: %v (filtering will still work without the index)", err)
		} else {
			logger.Info("logstore: team/business-unit GIN indexes are ready")
		}

		if err := ensureDashboardEnhancements(context.Background(), lock.conn); err != nil {
			logger.Warn("logstore: dashboard enhancements failed: %v (dashboard will still work with partial data)", err)
		} else {
			logger.Info("logstore: dashboard enhancements completed")
		}

		if err := ensurePerformanceIndexes(context.Background(), lock.conn, logger); err != nil {
			logger.Warn("logstore: performance index build failed: %v (queries will still work without the indexes)", err)
		} else {
			logger.Info("logstore: performance indexes are ready")
		}
	}

	switch {
	case migrator.SkipStartupMigrations():
		// Processes started with --no-migrate (server pods, the
		// --matview-refresh-only job) leave index maintenance to the
		// out-of-band --migrate-only job.
		logger.Info("logstore: migrations disabled for this process; skipping index maintenance (owned by the --migrate-only job)")
	case migrator.OneShotMaintenance():
		// One-shot migration job owns index maintenance: run it synchronously
		// so the process doesn't exit mid-CREATE INDEX CONCURRENTLY (a killed
		// build leaves an INVALID index behind for the next run to rebuild).
		logger.Info("logstore: running index maintenance synchronously (--migrate-only)")
		runIndexMaintenance()
	default:
		// Default single-process mode: build in the background so index work
		// never blocks startup.
		go runIndexMaintenance()
	}

	// Create materialized views, run one initial refresh, and start the
	// periodic refresher for dashboard queries (unless disabled).
	runMatViewBoot := func() {
		if db.Dialector.Name() != "postgres" {
			return
		}
		maintained, err := ensureMatViews(context.Background(), db)
		if err != nil {
			logger.Warn(fmt.Sprintf("logstore: matview creation failed: %s (dashboard queries will use raw tables)", err))
			return
		}
		if !maintained {
			// Another replica owns the create/repair and its rebuild may not have
			// landed, so the views we see can still be an older schema version.
			// Confirm the shape before enabling the read path; if it isn't current
			// the refresher flips the flag once it is.
			shapesOK, err := matViewShapesReady(context.Background(), db)
			if err != nil {
				logger.Warn(fmt.Sprintf("logstore: matview shape check failed: %s (dashboard queries will use raw tables)", err))
			}
			if err != nil || !shapesOK {
				logger.Info("logstore: matview maintenance is owned by another replica and views are not current yet (dashboard queries will use raw tables until they are)")
				startMatViewRefresher(context.Background(), db, resolveMatViewRefreshInterval(config.MatViewRefreshInterval, logger), logger, &d.matViewsReady)
				return
			}
		}
		if err := refreshMatViews(context.Background(), db); err != nil {
			logger.Warn(fmt.Sprintf("logstore: initial matview refresh failed: %s", err))
		} else {
			logger.Info("logstore: materialized views are ready")
			// Signal that matviews are ready for query use. Until this point,
			// canUseMatView() returns false so all queries use raw tables.
			d.matViewsReady.Store(true)
		}
		if migrator.OneShotMaintenance() {
			// One-shot job: the ensure + refresh above is the whole job; no ticker.
			return
		}
		interval := resolveMatViewRefreshInterval(config.MatViewRefreshInterval, logger)
		if interval == 0 {
			logger.Info("logstore: periodic matview refresh disabled (matview_refresh_interval=off); refresh out of band, e.g. a --matview-refresh-only job or pg_cron")
			return
		}
		startMatViewRefresher(context.Background(), db, interval, logger, &d.matViewsReady)
	}
	if migrator.OneShotMaintenance() {
		// Run synchronously so the one-shot process (--migrate-only /
		// --matview-refresh-only) completes the pass before exiting instead
		// of killing it mid-DDL when the store closes.
		runMatViewBoot()
	} else {
		go runMatViewBoot()
	}

	return d, nil
}
