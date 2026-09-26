package lib

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Reuse one default-boot database per test binary so config reload tests do
// not rerun the migration chain and initial catalog seeding for every case.
// Each test gets an independent file copy; subsequent LoadConfig calls still
// exercise normal opening, config reconciliation, and catalog initialization.
// The catalog HTTP responses are fixed by catalogfixture_test.go.
//
// Build without config.json so no test-specific providers, credentials, or
// governance settings leak into another test's initial state.

var (
	templateConfigDBOnce sync.Once
	// Keyed by the suffix after the base path ("" for the .db itself, "-wal",
	// "-shm"), so whatever SQLite leaves behind is reproduced faithfully.
	templateConfigDBFiles map[string][]byte
	templateConfigDBErr   error
)

// templateConfigDBNames are the database filenames this package's helpers point
// config stores at: makeConfigDataWithProvidersAndDir and friends use config.db,
// createTestSQLiteConfigStore uses test-config.db.
var templateConfigDBNames = []string{"config.db", "test-config.db"}

func buildTemplateConfigDB() {
	dir, err := os.MkdirTemp("", "bifrost-template-db-*")
	if err != nil {
		templateConfigDBErr = err
		return
	}
	defer os.RemoveAll(dir)

	ctx := context.Background()
	basePath := filepath.Join(dir, "config.db")
	// Build the template through LoadConfig rather than NewConfigStore: opening
	// the store runs the migrations, but LoadConfig also seeds the database
	// (model catalog and pricing rows among them), and that seeding - not the
	// migration chain - is what dominates a first boot. A template missing it
	// would leave every test paying for it again.
	//
	// Deliberately no config.json: the template must hold exactly what a default
	// boot produces and nothing else, or tests asserting fresh-start defaults
	// would read whatever config the template was built from instead.
	config, err := LoadConfig(ctx, dir)
	if err != nil {
		templateConfigDBErr = err
		return
	}
	// Stop catalog and credential workers before snapshotting their database.
	// Closing only the store leaves those workers running against a closed DB.
	config.Close(ctx)
	// Close before reading the files. The store opens SQLite in WAL mode, and
	// closing the last connection is what checkpoints the WAL back into the main
	// database file - copying it while open would hand out a database missing
	// every migration still sitting in the log.

	sidecars, err := filepath.Glob(basePath + "*")
	if err != nil {
		templateConfigDBErr = err
		return
	}
	files := make(map[string][]byte, len(sidecars))
	for _, path := range sidecars {
		contents, err := os.ReadFile(path)
		if err != nil {
			templateConfigDBErr = err
			return
		}
		files[strings.TrimPrefix(path, basePath)] = contents
	}
	templateConfigDBFiles = files
}

// seedMigratedConfigDB plants the pre-migrated database in dir under every name
// this package's config-store helpers use, so the LoadConfig calls that follow
// pay for opening a database rather than for building one.
func seedMigratedConfigDB(t *testing.T, dir string) {
	t.Helper()
	templateConfigDBOnce.Do(buildTemplateConfigDB)
	if templateConfigDBErr != nil {
		t.Fatalf("failed to build template config DB: %v", templateConfigDBErr)
	}
	for _, name := range templateConfigDBNames {
		for suffix, contents := range templateConfigDBFiles {
			path := filepath.Join(dir, name+suffix)
			if err := os.WriteFile(path, contents, 0o644); err != nil {
				t.Fatalf("failed to seed template config DB at %s: %v", path, err)
			}
		}
	}
}
