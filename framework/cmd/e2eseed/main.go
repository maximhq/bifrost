package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/maximhq/bifrost/framework/e2eseed"
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
	opts := e2eseed.DefaultOptions()
	summaryPath := ""

	fs := flag.NewFlagSet("e2eseed", flag.ContinueOnError)
	fs.StringVar(&opts.Prefix, "prefix", opts.Prefix, "stable prefix for all seeded rows")
	fs.StringVar(&opts.ConfigPath, "config-path", opts.ConfigPath, "optional Bifrost config.json path used to derive DB settings")
	fs.StringVar(&opts.EncryptionKey, "encryption-key", opts.EncryptionKey, "optional Bifrost encryption key for encrypted config rows")
	fs.StringVar(&opts.ConfigDialect, "config-db-dialect", opts.ConfigDialect, "config DB dialect: postgres or sqlite")
	fs.StringVar(&opts.ConfigDSN, "config-db-dsn", opts.ConfigDSN, "config DB DSN")
	fs.StringVar(&opts.LogsDialect, "logs-db-dialect", opts.LogsDialect, "logs DB dialect: postgres or sqlite")
	fs.StringVar(&opts.LogsDSN, "logs-db-dsn", opts.LogsDSN, "logs DB DSN")
	fs.IntVar(&opts.LogRows, "logs", opts.LogRows, "number of log rows to seed")
	fs.IntVar(&opts.BatchSize, "batch-size", opts.BatchSize, "log insert batch size")
	fs.StringVar(&opts.OutputEnvPath, "output-env", opts.OutputEnvPath, "path for generated environment values")
	fs.StringVar(&summaryPath, "summary", "", "optional JSON summary path")
	fs.BoolVar(&opts.DryRun, "dry-run", opts.DryRun, "build the manifest without writing rows")
	if err := fs.Parse(args); err != nil {
		return err
	}

	opts, err := e2eseed.NormalizeOptions(opts)
	if err != nil {
		return err
	}
	e2eseed.InitEncryption(opts)
	configDB, err := e2eseed.OpenDB(opts.ConfigDialect, opts.ConfigDSN)
	if err != nil {
		return fmt.Errorf("open config DB: %w", err)
	}
	if sqlDB, dbErr := configDB.DB(); dbErr == nil {
		defer sqlDB.Close()
	}
	logsDB, err := e2eseed.OpenDB(opts.LogsDialect, opts.LogsDSN)
	if err != nil {
		return fmt.Errorf("open logs DB: %w", err)
	}
	if sqlDB, dbErr := logsDB.DB(); dbErr == nil {
		defer sqlDB.Close()
	}

	summary, err := e2eseed.SeedBase(ctx, configDB, logsDB, opts)
	if err != nil {
		return err
	}
	if summaryPath != "" {
		if err := e2eseed.WriteJSONFile(summaryPath, summary); err != nil {
			return err
		}
	}
	fmt.Printf("seeded OSS API e2e data prefix=%s logs=%d env=%s\n", summary.Prefix, summary.LogRows, opts.OutputEnvPath)
	return nil
}
