package configstore

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/postgresconn"
)

// Option tunes NewConfigStore.
type Option func(*storeOptions)

// storeOptions collects the Options passed to NewConfigStore.
type storeOptions struct {
	// migrationMatViewPause, when set, replaces the default matview pause used by
	// this store's schema migrations.
	migrationMatViewPause *logstore.MatViewPauseOptions
}

// MigrationLockRetryOptions returns the lock-timeout retry options a config
// store config asks for: its migration_lock_retry_budget when cfg is a Postgres
// config store, and the package default otherwise. Downstream migrations that
// run against the config store (bifrost-enterprise) call it so one setting
// governs them.
func MigrationLockRetryOptions(cfg *Config, logger schemas.Logger) postgresconn.LockRetryOptions {
	if cfg != nil {
		if pg, ok := cfg.Config.(*PostgresConfig); ok && pg != nil {
			return postgresconn.MigrationLockRetryOptions(pg, logger)
		}
	}
	return postgresconn.LockRetryOptions{}
}

// WithMigrationMatViewPause sets how this store's schema migrations pause
// materialized-view refreshes. The config store usually shares its database with
// the log store, so callers pass the log store's migration_matview_cancel_after
// (see logstore.MigrationMatViewPauseOptions) and one setting governs both.
// Without it the default cancel delay applies.
func WithMigrationMatViewPause(pause logstore.MatViewPauseOptions) Option {
	return func(o *storeOptions) { o.migrationMatViewPause = &pause }
}
