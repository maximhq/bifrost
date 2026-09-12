package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func makeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE logs (
		id TEXT PRIMARY KEY,
		content_summary TEXT,
		input_history TEXT,
		output_message TEXT,
		tools TEXT,
		token_usage TEXT,
		cache_debug TEXT
	)`)
	require.NoError(t, err)
	repeated := `[{"role":"user","content":"SECRET_MARKER repeated context repeated context repeated context"}]   `
	_, err = db.Exec(`INSERT INTO logs(id,content_summary,input_history,output_message,tools,token_usage,cache_debug) VALUES
		('id-secret-1','SECRET_MARKER summary',?,NULL,'not-json SECRET_MARKER','SECRET_MARKER token usage',''),
		('id-secret-2','summary two',?,'',?,'{}',NULL),
		('id-secret-3','unicode summary',?,?,'[]','{}','{}')`,
		repeated, repeated,
		`[{"name":"tool","description":"SECRET_MARKER dynamic tail"}]`,
		`[{"role":"user","content":"Unicode 世界 and escaped \\n"}]`,
		`[{"role":"assistant","content":"large output large output large output"}]`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO logs(id,content_summary,input_history,output_message,tools,token_usage,cache_debug) VALUES
		('id-invalid','invalid utf8',CAST(x'FFFE0078' AS TEXT),'','[]','{}','{}')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path
}

func TestAnalyzeFixtureIsAggregateAndByteExact(t *testing.T) {
	path := makeFixture(t)
	o := options{dbPath: path, snapshotMethod: "online-backup", output: "-", minFieldBytes: 8, minChunkBytes: 8}
	r, err := analyze(context.Background(), o)
	require.NoError(t, err)
	require.Equal(t, int64(4), r.Rows)
	require.True(t, r.Input.Unchanged)
	require.Equal(t, "ok", r.Input.QuickCheck)
	require.Equal(t, int64(1), r.Fields["output_message"].Null)
	require.Equal(t, int64(2), r.Fields["output_message"].Empty)
	require.Equal(t, int64(len("SECRET_MARKER token usage")+6), r.Fields["token_usage"].RawBytes)
	require.Zero(t, r.Schemes[2].RoundtripFailures)
	require.Positive(t, r.Schemes[2].CheckedFields)
	require.Equal(t, r.PayloadRawBytes, r.Schemes[0].EstimatedEncodedBytes)
	require.Positive(t, r.Schemes[1].EstimatedEncodedBytes)
	require.Positive(t, r.Schemes[2].EstimatedEncodedBytes)
	require.Less(t, r.Schemes[2].UniqueObjects, r.Schemes[2].ObjectOccurrences)

	var out bytes.Buffer
	require.NoError(t, writeReport(&out, r))
	require.NotContains(t, out.String(), "SECRET_MARKER")
	require.NotContains(t, out.String(), "id-secret")
	var decoded report
	require.NoError(t, json.Unmarshal(out.Bytes(), &decoded))
}

func TestRunRejectsUnsafeOrInvalidInputs(t *testing.T) {
	var out, stderr bytes.Buffer
	err := run(context.Background(), nil, &out, &stderr)
	require.ErrorContains(t, err, "--db is required")

	path := makeFixture(t)
	err = run(context.Background(), []string{"--db", path, "--snapshot-method", "copy"}, &out, &stderr)
	require.ErrorContains(t, err, "--snapshot-method")
	err = run(context.Background(), []string{"--db", path, "--snapshot-method", "online-backup", "--exclude-field", "token_usage"}, &out, &stderr)
	require.ErrorContains(t, err, "not CAS-eligible")

	require.NoError(t, os.WriteFile(path+"-wal", []byte("sidecar"), 0o600))
	err = run(context.Background(), []string{"--db", path, "--snapshot-method", "online-backup"}, &out, &stderr)
	require.ErrorContains(t, err, "sidecar present")
	require.NoError(t, os.Remove(path+"-wal"))

	before, err := os.ReadFile(path)
	require.NoError(t, err)
	err = run(context.Background(), []string{"--db", path, "--snapshot-method", "online-backup", "--output", path}, &out, &stderr)
	require.ErrorContains(t, err, "must not overwrite")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestAnalyzeRejectsMissingLogsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE other (id TEXT)")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = analyze(context.Background(), options{dbPath: path, snapshotMethod: "online-backup", minFieldBytes: 8, minChunkBytes: 8})
	require.ErrorContains(t, err, "logs table is missing")
}

func TestRunRefusesExistingReport(t *testing.T) {
	path := makeFixture(t)
	reportPath := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, os.WriteFile(reportPath, []byte("keep"), 0o600))
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"--db", path, "--snapshot-method", "online-backup", "--output", reportPath}, &out, &stderr)
	require.Error(t, err)
	data, readErr := os.ReadFile(reportPath)
	require.NoError(t, readErr)
	require.Equal(t, "keep", string(data))
}

func TestRunWritesPrivateJSONReport(t *testing.T) {
	path := makeFixture(t)
	reportPath := filepath.Join(t.TempDir(), "report.json")
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"--db", path, "--snapshot-method", "vacuum-into", "--output", reportPath,
		"--min-field-bytes", "8", "--min-chunk-bytes", "8",
	}, &out, &stderr)
	require.NoError(t, err)
	data, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "SECRET_MARKER")
	require.True(t, strings.Contains(string(data), `"schema_version": "1"`))
	info, err := os.Stat(reportPath)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o077)
}
