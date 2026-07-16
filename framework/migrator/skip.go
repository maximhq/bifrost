package migrator

import "sync/atomic"

// skipStartupMigrations is a process-wide switch (set by the --no-migrate
// transport flag) meaning: this process must not run schema migrations —
// they are applied out of band, e.g. by a pod running with --migrate-only.
var skipStartupMigrations atomic.Bool

// SetSkipStartupMigrations toggles the process-wide "do not run schema
// migrations" switch. Call once at startup, before any store is opened.
func SetSkipStartupMigrations(skip bool) { skipStartupMigrations.Store(skip) }

// SkipStartupMigrations reports whether schema migrations are disabled for
// this process.
func SkipStartupMigrations() bool { return skipStartupMigrations.Load() }
