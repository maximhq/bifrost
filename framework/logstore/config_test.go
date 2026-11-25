package logstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfig_UnmarshalCaptureClientIP keeps client IP capture opt-in: absent means off, and the flag survives a disabled store and a config-store round trip.
func TestConfig_UnmarshalCaptureClientIP(t *testing.T) {
	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"type":"sqlite","config":{"path":"logs.db"}}`), &cfg))
	assert.False(t, cfg.CaptureClientIP, "client IP capture is off unless configured")

	cfg = Config{}
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":false,"capture_client_ip":true}`), &cfg))
	assert.True(t, cfg.CaptureClientIP)

	cfg = Config{}
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"type":"sqlite","config":{"path":"logs.db"},"capture_client_ip":true}`), &cfg))
	data, err := json.Marshal(&cfg)
	require.NoError(t, err)
	var roundTripped Config
	require.NoError(t, json.Unmarshal(data, &roundTripped))
	assert.True(t, roundTripped.CaptureClientIP, "the flag persists through the config store's JSON column")
}

// The log database holds request and response content, so a database file the store creates is
// readable and writable by the process owner only. A file that already exists keeps its mode.
func TestSQLiteLogStoreCreatesOwnerOnlyDatabaseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	ctx := context.Background()

	created := filepath.Join(t.TempDir(), "logs.db")
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: created}, asyncTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })
	info, err := os.Stat(created)
	require.NoError(t, err)
	assert.Zero(t, info.Mode().Perm()&0o077, "no group or other access, whatever the umask: mode %o", info.Mode().Perm())

	existing := filepath.Join(t.TempDir(), "logs.db")
	require.NoError(t, os.WriteFile(existing, nil, 0o640))
	require.NoError(t, os.Chmod(existing, 0o640))
	kept, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: existing}, asyncTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { kept.Close(ctx) })
	info, err = os.Stat(existing)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

// The driver treats everything after a "?" in the path as options, so such a path would open a
// different file from the one created here. It is refused instead of silently using another file.
func TestSQLiteLogStoreRefusesAPathWithAQueryMarker(t *testing.T) {
	dir := t.TempDir()
	_, err := newSqliteLogStore(context.Background(), &SQLiteConfig{Path: filepath.Join(dir, "logs.db?x")}, asyncTestLogger{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "?")
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "no file is created for a refused path")
}

// A path written as a SQLite URI ("file:logs.db") names two different files: the one created
// here would be literally "file:logs.db", while the driver opens "logs.db" and creates it with
// its own default mode. It is refused, so the database is always the file made owner-only.
func TestSQLiteLogStoreRefusesAFileURIPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, path := range []string{"file:logs.db", "FILE:logs.db", "file:" + filepath.Join(dir, "logs.db")} {
		_, err := newSqliteLogStore(context.Background(), &SQLiteConfig{Path: path}, asyncTestLogger{})
		require.Error(t, err, "path %q", path)
		require.Contains(t, err.Error(), "file:", "path %q", path)
	}
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Empty(t, entries, "no file is created for a refused path")
}
