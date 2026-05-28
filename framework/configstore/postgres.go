package configstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/postgresconn"
	"gorm.io/gorm"
)

type PostgresConfig = postgresconn.Config

// newPostgresConfigStore creates a new Postgres config store.
//
// Uses a two-pool lifecycle to avoid SQLSTATE 0A000 ("cached plan must not
// change result type"): a throwaway migration pool runs DDL and is closed
// immediately, then a fresh runtime pool is opened. The runtime pool's
// connections never see pre-migration schema, so their cached prepared-plans
// stay valid for the life of the process.
func newPostgresConfigStore(ctx context.Context, config *PostgresConfig, logger schemas.Logger) (ConfigStore, error) {
	if err := postgresconn.Validate(config, false); err != nil {
		return nil, err
	}
	dsn := postgresconn.BuildDSN(config)

	// Migration-only DSN. Forces pgx into simple-query protocol on the migration
	// pool so no statement plan is ever cached server-side; that makes SQLSTATE
	// 0A000 ("cached plan must not change result type") structurally impossible
	// when a migration mixes DDL with subsequent SELECTs against the same table.
	// Runtime pools keep the default cache-statement mode for performance.
	migrationDSN := dsn + " default_query_exec_mode=simple_protocol"

	// Throwaway pool for schema migrations. Closing it before the runtime pool
	// opens guarantees no cached prepared-plan survives the DDL.
	mDb, err := postgresconn.Open(migrationDSN, config, logger)
	if err != nil {
		return nil, err
	}
	if err := triggerMigrations(ctx, mDb, logger); err != nil {
		postgresconn.Close(mDb, logger)
		return nil, err
	}
	postgresconn.Close(mDb, logger)

	// Runtime pool. Opens against post-migration schema.
	db, err := postgresconn.Open(dsn, config, logger)
	if err != nil {
		return nil, err
	}
	if err := postgresconn.ApplyPoolTuning(db, config); err != nil {
		postgresconn.Close(db, logger)
		return nil, err
	}

	d := &RDBConfigStore{logger: logger}
	d.db.Store(db)

	// migrateOnFreshFn: downstream consumers (e.g. bifrost-enterprise) run
	// their migrations via this hook on a throwaway pool that closes after fn.
	d.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		tempDB, err := postgresconn.Open(migrationDSN, config, logger)
		if err != nil {
			return err
		}
		defer postgresconn.Close(tempDB, logger)
		return fn(ctx, tempDB)
	}

	// refreshPoolFn: open fresh runtime pool first (so a failure leaves the
	// existing pool in place), swap atomically, then close the old pool.
	// sql.DB.Close blocks until in-flight queries finish, so callers already
	// using the old pool complete safely.
	d.refreshPoolFn = func(ctx context.Context) error {
		newDB, err := postgresconn.Open(dsn, config, logger)
		if err != nil {
			return fmt.Errorf("failed to open fresh runtime pool: %w", err)
		}
		if err := postgresconn.ApplyPoolTuning(newDB, config); err != nil {
			postgresconn.Close(newDB, logger)
			return fmt.Errorf("failed to tune fresh runtime pool: %w", err)
		}
		oldDB := d.db.Swap(newDB)
		if oldDB != nil {
			postgresconn.Close(oldDB, logger)
		}
		return nil
	}

	// Encrypt any plaintext rows if encryption is enabled. Runs on the
	// runtime pool — pure DML (SELECT + UPDATE), no DDL, so cached plans it
	// installs remain valid until the next external migration batch.
	if err := d.EncryptPlaintextRows(ctx); err != nil {
		postgresconn.Close(db, logger)
		return nil, fmt.Errorf("failed to encrypt plaintext rows: %w", err)
	}
	return d, nil
}
