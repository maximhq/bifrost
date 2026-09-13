package main

import (
	"context"
	"database/sql"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type quiet struct{}

func (quiet) Debug(string, ...any)                   {}
func (quiet) Info(string, ...any)                    {}
func (quiet) Warn(string, ...any)                    {}
func (quiet) Error(string, ...any)                   {}
func (quiet) Fatal(string, ...any)                   {}
func (quiet) SetLevel(schemas.LogLevel)              {}
func (quiet) SetOutputType(schemas.LoggerOutputType) {}
func (quiet) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}
func fixture(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0700))
	live := filepath.Join(dir, "live.db")
	store, e := logstore.NewLogStore(ctx, &logstore.Config{Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: live}, ContentAddressed: &logstore.ContentAddressedConfig{Enabled: true, MinFieldBytes: 16, MinChunkBytes: 8}}, quiet{})
	require.NoError(t, e)
	for _, id := range []string{"new", "updated", "deleted", "hidden"} {
		content := strings.Repeat(id, 60)
		entry := &logstore.Log{ID: id, Timestamp: time.Now().UTC(), Provider: "openai", Model: "recovery-test", Status: "success", Object: "chat.completion", ContentHidden: id == "hidden", InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &content}}}}
		require.NoError(t, store.Create(ctx, entry))
	}
	raw := " \n[ {\"role\":\"user\", \"content\":\"" + strings.Repeat("updated", 60) + "\"} ]\t "
	require.NoError(t, store.Update(ctx, "updated", map[string]interface{}{"input_history": raw}))
	require.NoError(t, store.DeleteLog(ctx, "deleted"))
	db, e := sql.Open("sqlite3", live)
	require.NoError(t, e)
	snapshot := filepath.Join(dir, "snapshot.db")
	_, e = db.Exec("VACUUM INTO ?", snapshot)
	require.NoError(t, e)
	require.NoError(t, db.Close())
	require.NoError(t, store.Close(ctx))
	return snapshot, raw
}
func TestProductionBackupRecovery(t *testing.T) {
	source, raw := fixture(t)
	before, e := hashFile(source)
	require.NoError(t, e)
	out := filepath.Join(filepath.Dir(source), "restored.db")
	require.NoError(t, restore(context.Background(), source, out))
	after, e := hashFile(source)
	require.NoError(t, e)
	require.Equal(t, before, after)
	st, e := os.Stat(out)
	require.NoError(t, e)
	require.Equal(t, os.FileMode(0600), st.Mode().Perm())
	db, e := sql.Open("sqlite3", out)
	require.NoError(t, e)
	defer db.Close()
	var got string
	require.NoError(t, db.QueryRow("SELECT input_history FROM logs WHERE id='updated'").Scan(&got))
	require.Equal(t, raw, got)
	var n int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM logs").Scan(&n))
	require.Equal(t, 3, n)
	require.NoError(t, db.QueryRow("SELECT count(*) FROM logs WHERE id='new' AND has_object=0 AND length(input_history)>100 AND model='recovery-test'").Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, db.QueryRow("SELECT count(*) FROM cas_payloads WHERE log_id='hidden'").Scan(&n))
	require.Positive(t, n)
	require.NoError(t, db.QueryRow("SELECT input_history FROM logs WHERE id='hidden'").Scan(&got))
	require.Empty(t, got)
	for _, cas := range []bool{false, true} {
		cfg := &logstore.Config{Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: out}}
		if cas {
			cfg.ContentAddressed = &logstore.ContentAddressedConfig{Enabled: true}
		}
		store, e := logstore.NewLogStore(context.Background(), cfg, quiet{})
		require.NoError(t, e)
		row, e := store.FindByID(context.Background(), "updated")
		require.NoError(t, e)
		require.Equal(t, raw, row.InputHistory)
		row, e = store.FindByID(context.Background(), "hidden")
		require.NoError(t, e)
		require.True(t, row.ContentHidden)
		require.Empty(t, row.InputHistory)
		require.NoError(t, store.Close(context.Background()))
	}
	require.Error(t, restore(context.Background(), source, out))
}
func TestFailClosed(t *testing.T) {
	for _, kind := range []string{"codec", "length", "digest", "missing", "pointer", "unknown", "hidden-corrupt", "wal", "shm", "journal", "overwrite", "symlink", "unsafe-dir"} {
		t.Run(kind, func(t *testing.T) {
			source, _ := fixture(t)
			out := filepath.Join(filepath.Dir(source), "result.db")
			db, e := sql.Open("sqlite3", source)
			require.NoError(t, e)
			switch kind {
			case "codec":
				_, e = db.Exec("UPDATE cas_blobs SET codec='invalid'")
			case "length":
				_, e = db.Exec("UPDATE cas_blobs SET orig_len=orig_len+1")
			case "digest":
				_, e = db.Exec("UPDATE cas_blobs SET data=x'00'")
			case "missing":
				_, e = db.Exec("DELETE FROM cas_blobs")
			case "pointer":
				_, e = db.Exec("DELETE FROM cas_payloads WHERE log_id='new'")
			case "unknown":
				_, e = db.Exec("UPDATE cas_payloads SET field='unknown' WHERE field='input_history'")
			case "hidden-corrupt":
				_, e = db.Exec("DELETE FROM cas_blobs WHERE hash IN (SELECT blob_hash FROM cas_payloads WHERE log_id='hidden')")
			}
			require.NoError(t, e)
			require.NoError(t, db.Close())
			switch kind {
			case "wal", "shm", "journal":
				require.NoError(t, os.WriteFile(source+"-"+kind, nil, 0600))
			case "overwrite":
				require.NoError(t, os.WriteFile(out, []byte("sentinel"), 0600))
			case "symlink":
				require.NoError(t, os.Symlink(source, out))
			case "unsafe-dir":
				require.NoError(t, os.Chmod(filepath.Dir(out), 0755))
			}
			before, e := hashFile(source)
			require.NoError(t, e)
			require.Error(t, restore(context.Background(), source, out))
			after, e := hashFile(source)
			require.NoError(t, e)
			require.Equal(t, before, after)
			if kind == "overwrite" {
				b, e := os.ReadFile(out)
				require.NoError(t, e)
				require.Equal(t, "sentinel", string(b))
			} else if kind != "symlink" {
				_, e = os.Lstat(out)
				require.True(t, os.IsNotExist(e))
			}
		})
	}
}
