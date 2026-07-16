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

// migrateOnly is a process-wide switch (set by the --migrate-only transport
// flag) meaning: this is a one-shot migration job. Stores run maintenance
// that is normally backgrounded (e.g. logstore index builds) synchronously,
// so it completes before the process exits.
var migrateOnly atomic.Bool

// SetMigrateOnly toggles the process-wide "one-shot migration job" switch.
// Call once at startup, before any store is opened.
func SetMigrateOnly(v bool) { migrateOnly.Store(v) }

// MigrateOnly reports whether this process is a one-shot migration job.
func MigrateOnly() bool { return migrateOnly.Load() }
