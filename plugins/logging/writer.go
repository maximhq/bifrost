// Package logging provides the trace/span writer for the logging plugin.
// The actual write logic is in enqueueTraceEntry (main.go) which calls
// store.CreateRootSpanWithChildren directly in a goroutine.
// This file is intentionally minimal — the old batch writer has been removed
// as part of the migration to the trace/span architecture.
package logging
