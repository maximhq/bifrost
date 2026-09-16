package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func materializeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source snapshot.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE logs (
		id TEXT PRIMARY KEY,
		has_object INTEGER NOT NULL DEFAULT 0,
		content_hidden INTEGER NOT NULL DEFAULT 0,
		content_summary TEXT,
		input_history TEXT,
		output_message TEXT,
		tools TEXT,
		token_usage TEXT,
		cache_debug TEXT
	)`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE INDEX idx_logs_summary ON logs(content_summary)`)
	require.NoError(t, err)
	repeated := `[{"role":"system","content":"SECRET_MATERIALIZE fixed context"},{"role":"assistant","content":"earlier response"},{"role":"user","content":"SECRET_MATERIALIZE repeated repeated repeated"}]  `
	_, err = db.Exec(`INSERT INTO logs VALUES
		('private-id-1',0,0,'SECRET_MATERIALIZE summary',?,NULL,'not-json SECRET_MATERIALIZE','{}',''),
		('private-id-2',0,0,'summary two',?,'',?,'{}',NULL),
		('private-id-3',0,0,'summary three',CAST(x'FFFE0078' AS TEXT),?,'[]','{}','{}'),
		('private-id-hidden',0,1,'must clear',?,NULL,'[{"name":"tiny"}]','{"total_tokens":1}','cache')`,
		repeated, repeated,
		`[{"name":"tool","description":"SECRET_MATERIALIZE dynamic"}]`,
		`[{"role":"assistant","content":"output output output"}]`,
		repeated)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path
}

func TestMaterializeAllSchemesAndVerify(t *testing.T) {
	source := materializeFixture(t)
	work := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.Mkdir(work, 0o700))
	reportPath := filepath.Join(t.TempDir(), "report.json")
	err := run(context.Background(), []string{
		"--db", source,
		"--snapshot-method", "online-backup",
		"--work-dir", work,
		"--output", reportPath,
		"--min-field-bytes", "8",
		"--min-chunk-bytes", "8",
	})
	require.NoError(t, err)

	for _, name := range []string{"current.db", "field-zstd.db", "cas-manifest.db"} {
		path := filepath.Join(work, name)
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		require.Zero(t, info.Mode().Perm()&0o077)
		db, openErr := sql.Open("sqlite3", readDSN(path))
		require.NoError(t, openErr)
		require.NoError(t, quickCheck(db))
		var rows int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&rows))
		require.Equal(t, 4, rows)
		var indexCount int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_logs_summary'").Scan(&indexCount))
		require.Equal(t, 1, indexCount)
		require.NoError(t, db.Close())
	}

	var r report
	data, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &r))
	require.NotContains(t, string(data), "SECRET_MATERIALIZE")
	require.NotContains(t, string(data), "private-id")
	require.Len(t, r.Schemes, 3)
	require.Equal(t, int64(4), r.Rows)
	require.True(t, r.InputUnchanged)
	require.Positive(t, r.Schemes[1].Verification.CheckedFields)
	require.Zero(t, r.Schemes[1].Verification.Mismatches)
	require.Positive(t, r.Schemes[2].Blobs)
	require.Positive(t, r.Schemes[2].Pointers)
	require.Zero(t, r.Schemes[2].Verification.Mismatches)

	casDB, err := sql.Open("sqlite3", readDSN(filepath.Join(work, "cas-manifest.db")))
	require.NoError(t, err)
	defer casDB.Close()
	var preview string
	require.NoError(t, casDB.QueryRow("SELECT input_history FROM logs WHERE id='private-id-1'").Scan(&preview))
	require.Empty(t, preview)
	sourceDB, err := sql.Open("sqlite3", readDSN(source))
	require.NoError(t, err)
	var originalHistory string
	require.NoError(t, sourceDB.QueryRow("SELECT input_history FROM logs WHERE id='private-id-1'").Scan(&originalHistory))
	require.NoError(t, sourceDB.Close())
	require.NotEqual(t, originalHistory, preview)
	var hasObject bool
	require.NoError(t, casDB.QueryRow("SELECT has_object FROM logs WHERE id='private-id-1'").Scan(&hasObject))
	require.True(t, hasObject)
	var hiddenSummary, hiddenTools, hiddenUsage, hiddenCache string
	require.NoError(t, casDB.QueryRow("SELECT content_summary,tools,token_usage,cache_debug FROM logs WHERE id='private-id-hidden'").Scan(&hiddenSummary, &hiddenTools, &hiddenUsage, &hiddenCache))
	require.Empty(t, hiddenSummary)
	require.Empty(t, hiddenTools)
	require.NotEmpty(t, hiddenUsage)
	require.NotEmpty(t, hiddenCache)
	var hiddenToolPointer int
	require.NoError(t, casDB.QueryRow("SELECT COUNT(*) FROM cas_payloads WHERE log_id='private-id-hidden' AND field='tools'").Scan(&hiddenToolPointer))
	require.Equal(t, 1, hiddenToolPointer)
}

func TestVerifyExistingSchemes(t *testing.T) {
	source := materializeFixture(t)
	work := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.Mkdir(work, 0o700))
	firstReport := filepath.Join(t.TempDir(), "first.json")
	args := []string{"--db", source, "--snapshot-method", "online-backup", "--work-dir", work, "--output", firstReport, "--min-field-bytes", "8", "--min-chunk-bytes", "8"}
	require.NoError(t, run(context.Background(), args))
	secondReport := filepath.Join(t.TempDir(), "second.json")
	verifyArgs := []string{"--db", source, "--snapshot-method", "online-backup", "--work-dir", work, "--output", secondReport, "--min-field-bytes", "8", "--min-chunk-bytes", "8", "--verify-existing"}
	require.NoError(t, run(context.Background(), verifyArgs))
	var r report
	data, err := os.ReadFile(secondReport)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &r))
	require.Len(t, r.Schemes, 3)
	for _, scheme := range r.Schemes {
		require.Zero(t, scheme.Verification.Mismatches)
		require.Equal(t, int64(4), scheme.Rows)
	}
}

func TestMaterializeRejectsUnsafeInputs(t *testing.T) {
	source := materializeFixture(t)
	work := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.Mkdir(work, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(work, "keep"), []byte("x"), 0o600))
	err := run(context.Background(), []string{"--db", source, "--snapshot-method", "online-backup", "--work-dir", work, "--output", filepath.Join(t.TempDir(), "r.json")})
	require.ErrorContains(t, err, "work-dir must be empty")

	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.Mkdir(empty, 0o700))
	require.NoError(t, os.WriteFile(source+"-wal", []byte("x"), 0o600))
	err = run(context.Background(), []string{"--db", source, "--snapshot-method", "online-backup", "--work-dir", empty, "--output", filepath.Join(t.TempDir(), "r.json")})
	require.ErrorContains(t, err, "sidecar")
}
