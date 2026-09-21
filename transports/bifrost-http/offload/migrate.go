// Package offload implements the `bifrost migrate-offload` maintenance command.
//
// It runs a one-time backfill that moves existing DB-resident log payloads into
// the object storage configured under logs_store.object_storage, converting
// rows to the lightweight hybrid form. Rows that already carry has_object=true
// are skipped, so the command is safe to re-run after an interrupted pass.
package offload

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bifrostServer "github.com/maximhq/bifrost/transports/bifrost-http/server"
)

// RunMigrateOffload is the entry point for `bifrost migrate-offload`. It
// returns a process exit code.
func RunMigrateOffload(args []string) int {
	fs := flag.NewFlagSet("migrate-offload", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: bifrost migrate-offload [flags]\n\nMigrates existing log payloads from the database to the configured object storage (S3/GCS), matching what the online hybrid write path produces.")
		fs.PrintDefaults()
	}
	var (
		appDir      = fs.String("app-dir", bifrostServer.DefaultAppDir, "Application data directory (contains config.json); defaults to the gateway config directory")
		batchSize   = fs.Int("batch-size", logstore.DefaultBackfillBatchSize, "Rows scanned per DB batch")
		concurrency = fs.Int("concurrency", logstore.DefaultBackfillConcurrency, "Parallel upload workers")
		olderThan   = fs.Duration("older-than", 0, "Only migrate logs older than this duration (e.g. 24h); 0 migrates everything")
		noMCP       = fs.Bool("no-mcp", false, "Skip MCP tool logs")
		dryRun      = fs.Bool("dry-run", false, "Scan and report without uploading or modifying rows")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	// lib.LoadConfig uses the package logger, which is nil until set.
	lib.SetLogger(logger)

	// Resolve the directory exactly like the gateway, including its OS-specific
	// default when -app-dir is omitted.
	dir := bifrostServer.GetDefaultConfigDir(*appDir)

	// Load the same config the gateway uses, so object_storage and
	// object_storage_exclude_fields are guaranteed to match the online path.
	cfg, err := lib.LoadConfig(context.Background(), dir)
	if err != nil {
		logger.Error("failed to load config from %s: %v", *appDir, err)
		return 1
	}
	if cfg.LogsStoreConfig == nil || !cfg.LogsStoreConfig.Enabled {
		logger.Error("logs_store is not enabled in %s/config.json; nothing to migrate", *appDir)
		return 1
	}
	if cfg.LogsStoreConfig.ObjectStorage == nil {
		logger.Error("logs_store.object_storage is not configured; enable it first, then run this command")
		return 1
	}

	// LoadConfig already built the log store through the same factory the
	// server uses: this yields a HybridLogStore when object_storage is set.
	store := cfg.LogsStore
	if store == nil {
		logger.Error("failed to open log store")
		return 1
	}
	hybrid, ok := store.(*logstore.HybridLogStore)
	if !ok {
		logger.Error("log store %s is not object-storage-backed (hybrid mode requires object_storage); nothing to migrate", cfg.LogsStoreConfig.Type)
		_ = store.Close(context.Background())
		return 1
	}
	defer hybrid.Close(context.Background())

	// Ctrl-C stops the scan gracefully: already-queued rows finish, nothing is
	// left half-migrated, and a re-run picks up where this one stopped.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := logstore.BackfillOptions{
		BatchSize:   *batchSize,
		Concurrency: *concurrency,
		OlderThan:   *olderThan,
		IncludeMCP:  nil,
		DryRun:      *dryRun,
	}
	if *noMCP {
		f := false
		opts.IncludeMCP = &f
	}

	if *dryRun {
		fmt.Println("dry run: no log rows or object-storage payloads will be modified")
	}

	result, err := hybrid.BackfillObjects(ctx, opts)
	if err != nil {
		logger.Error("backfill aborted: %v", err)
		report(logger, result, *dryRun)
		return 1
	}
	report(logger, result, *dryRun)
	if result.Failed > 0 {
		return 1
	}
	return 0
}

func report(logger schemas.Logger, result *logstore.BackfillResult, dryRun bool) {
	if result == nil {
		return
	}
	mode := "migrated"
	bytesVerb := "offloaded"
	if dryRun {
		mode = "would migrate"
		bytesVerb = "would be offloaded"
	}
	if result.Duration == 0 {
		return
	}
	logger.Info("%s: %d rows %s (%d skipped, %d failed), %s %s in %s",
		"migrate-offload", result.Migrated, mode, result.Skipped, result.Failed,
		humanBytes(result.BytesOffloaded), bytesVerb, result.Duration.Truncate(time.Millisecond))
	for _, e := range result.Errors {
		logger.Warn("%v", e)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
