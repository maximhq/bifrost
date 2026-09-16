package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/maximhq/bifrost/framework/logstore"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func run() error {
	path := flag.String("db", "", "stopped SQLite COPY to verify or compact (never a live database)")
	clear := flag.Bool("clear-inline", false, "clear CAS-selected inline fields after full verification")
	refs := flag.Bool("compact-refs", false, "migrate cas_refs to WITHOUT ROWID atomically")
	vacuum := flag.Bool("vacuum", false, "reclaim free pages after successful transaction")
	flag.Parse()
	if *path == "" {
		return fmt.Errorf("-db is required")
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("database must be a regular file, not a symlink")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(abs + suffix); !os.IsNotExist(err) {
			return fmt.Errorf("database sidecar exists or is inaccessible; use a stopped clean copy")
		}
	}
	mode := "ro"
	if *clear || *refs || *vacuum {
		mode = "rw"
	}
	uri := (&url.URL{Scheme: "file", Path: abs, RawQuery: "mode=" + mode + "&_busy_timeout=5000&_txlock=immediate"}).String()
	if mode == "ro" {
		uri = (&url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&_query_only=1"}).String()
	}
	db, err := gorm.Open(sqlite.Open(uri), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	report, err := logstore.CompactCAS(context.Background(), db, *clear, *refs)
	if err != nil {
		return err
	}
	if *vacuum {
		if err := db.Exec("VACUUM").Error; err != nil {
			return fmt.Errorf("compaction committed but VACUUM failed: %w", err)
		}
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
