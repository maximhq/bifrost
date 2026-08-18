package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/maximhq/bifrost/tests/cmd/seed"
)

// main runs the OSS API e2e seed command.
func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "e2e seed failed: %v\n", err)
		os.Exit(1)
	}
}

// run parses flags, connects to the target stores, and writes seed fixtures.
func run(ctx context.Context, args []string) error {
	opts := seed.DefaultOptions()
	summaryPath := ""

	fs := flag.NewFlagSet("e2eseed", flag.ContinueOnError)
	fs.StringVar(&opts.Prefix, "prefix", opts.Prefix, "stable prefix for all seeded rows")
	fs.StringVar(&opts.ConfigPath, "config-path", opts.ConfigPath, "optional Bifrost config.json path used to derive DB settings")
	fs.StringVar(&opts.EncryptionKey, "encryption-key", opts.EncryptionKey, "optional Bifrost encryption key for encrypted config rows")
	fs.StringVar(&opts.ConfigDialect, "config-db-dialect", opts.ConfigDialect, "config DB dialect: postgres or sqlite")
	fs.StringVar(&opts.ConfigDSN, "config-db-dsn", opts.ConfigDSN, "config DB DSN")
	fs.StringVar(&opts.LogsDialect, "logs-db-dialect", opts.LogsDialect, "logs DB dialect: postgres or sqlite")
	fs.StringVar(&opts.LogsDSN, "logs-db-dsn", opts.LogsDSN, "logs DB DSN")
	fs.IntVar(&opts.LogRowsPerShape, "logs-per-shape", opts.LogRowsPerShape, "number of log rows to seed per DAC ownership shape (applied to both logs and mcp_tool_logs)")
	fs.IntVar(&opts.BatchSize, "batch-size", opts.BatchSize, "log insert batch size")
	fs.StringVar(&opts.OutputEnvPath, "output-env", opts.OutputEnvPath, "path for generated environment values")
	fs.StringVar(&summaryPath, "summary", "", "optional JSON summary path")
	fs.BoolVar(&opts.DryRun, "dry-run", opts.DryRun, "build the manifest without writing rows")
	if err := fs.Parse(args); err != nil {
		return err
	}

	opts, err := seed.NormalizeOptions(opts)
	if err != nil {
		return err
	}
	seed.InitEncryption(opts)
	configDB, err := seed.OpenDB(opts.ConfigDialect, opts.ConfigDSN)
	if err != nil {
		return fmt.Errorf("open config DB: %w", err)
	}
	if sqlDB, dbErr := configDB.DB(); dbErr == nil {
		defer sqlDB.Close()
	}
	logsDB, err := seed.OpenDB(opts.LogsDialect, opts.LogsDSN)
	if err != nil {
		return fmt.Errorf("open logs DB: %w", err)
	}
	if sqlDB, dbErr := logsDB.DB(); dbErr == nil {
		defer sqlDB.Close()
	}

	summary, err := seed.SeedBase(ctx, configDB, logsDB, opts)
	if err != nil {
		return err
	}
	if summaryPath != "" {
		if err := seed.WriteJSONFile(summaryPath, summary); err != nil {
			return err
		}
	}
	fmt.Printf("seeded OSS API e2e data prefix=%s logs_per_shape=%d shapes=%d env=%s\n", summary.Prefix, summary.LogRowsPerShape, len(summary.Expected.Shapes), opts.OutputEnvPath)
	return nil
}
