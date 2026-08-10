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

// oneShotMaintenance is a process-wide switch (set by the --migrate-only and
// --matview-refresh-only transport flags) meaning: this is a one-shot
// maintenance job. Stores run boot maintenance that is normally backgrounded
// (logstore index builds, matview create/refresh) synchronously so it
// completes before the process exits, and skip periodic background work.
var oneShotMaintenance atomic.Bool

// SetOneShotMaintenance toggles the process-wide "one-shot maintenance job"
// switch. Call once at startup, before any store is opened.
func SetOneShotMaintenance(v bool) { oneShotMaintenance.Store(v) }

// OneShotMaintenance reports whether this process is a one-shot maintenance job.
func OneShotMaintenance() bool { return oneShotMaintenance.Load() }
