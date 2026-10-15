package configstore

import "github.com/maximhq/bifrost/framework/logstore"

// Option tunes NewConfigStore.
type Option func(*storeOptions)

// storeOptions collects the Options passed to NewConfigStore.
type storeOptions struct {
	// migrationMatViewPause, when set, replaces the default matview pause used by
	// this store's schema migrations.
	migrationMatViewPause *logstore.MatViewPauseOptions
}

// WithMigrationMatViewPause sets how this store's schema migrations pause
// materialized-view refreshes. The config store usually shares its database with
// the log store, so callers pass the log store's migration_matview_cancel_after
// (see logstore.MigrationMatViewPauseOptions) and one setting governs both.
// Without it the default cancel delay applies.
func WithMigrationMatViewPause(pause logstore.MatViewPauseOptions) Option {
	return func(o *storeOptions) { o.migrationMatViewPause = &pause }
}
