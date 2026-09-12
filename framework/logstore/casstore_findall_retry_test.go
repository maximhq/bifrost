package logstore

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A separate SQLite connection pool shares the same WAL database. Its CAS
// updates commit while the reader holds its root-query snapshot, including
// reclaiming obsolete manifests. A split root/CAS implementation fails these
// tests deterministically instead of depending on scheduler luck.
func casSnapshotWriter(t *testing.T, cas *CasLogStore) *CasLogStore {
	t.Helper()
	var databases []struct {
		Name string
		File string
	}
	require.NoError(t, cas.db.Raw("PRAGMA database_list").Scan(&databases).Error)
	var path string
	for _, db := range databases {
		if db.Name == "main" {
			path = db.File
		}
	}
	require.NotEmpty(t, path)
	inner, err := newSqliteLogStore(context.Background(), &SQLiteConfig{Path: path}, hybridTestLogger{})
	require.NoError(t, err)
	writer, err := newCasLogStore(context.Background(), inner, &ContentAddressedConfig{Enabled: true, MinFieldBytes: 64, MinChunkBytes: 32}, hybridTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Close(context.Background())) })
	return writer
}

// The callback is a synchronous barrier after the root SELECT has materialized
// and closed its cursor, before hydration can start. Writer has independent
// callbacks and connections. Requiring fired proves this interleave occurred.
func casAfterRootQuery(t *testing.T, cas *CasLogStore, write func()) *bool {
	t.Helper()
	fired := false
	const name = "test:cas_snapshot_writer"
	require.NoError(t, cas.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if !fired && tx.Error == nil && tx.Statement.Table == "logs" {
			fired = true
			write()
		}
	}))
	t.Cleanup(func() { require.NoError(t, cas.db.Callback().Query().Remove(name)) })
	return &fired
}

func TestCas_FindAllRetryRereadsRootRow(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()
	entry := bigChatEntry("findall-retry-1", strings.Repeat("revision alpha ", 40))
	entry.UserID = strPtr("rev-a")
	entry.Model = "model-a"
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	writer := casSnapshotWriter(t, cas)
	contentB := projAuthHistoryJSON(t, strings.Repeat("revision beta ", 40))
	fired := casAfterRootQuery(t, cas, func() {
		require.NoError(t, writer.Update(ctx, entry.ID, map[string]any{"input_history": contentB, "user_id": "rev-b", "model": "model-b"}))
	})
	logs, err := cas.FindAll(ctx, map[string]any{"id": entry.ID})
	require.NoError(t, err)
	require.True(t, *fired)
	require.Len(t, logs, 1, "a healthy matching row must never be dropped")
	require.Equal(t, "rev-a", *logs[0].UserID)
	require.Equal(t, "model-a", logs[0].Model)
	require.Equal(t, entry.InputHistory, logs[0].InputHistory)
	logs, err = cas.FindAll(ctx, map[string]any{"id": entry.ID})
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, "rev-b", *logs[0].UserID)
	require.Equal(t, "model-b", logs[0].Model)
	require.Equal(t, contentB, logs[0].InputHistory)
}

func TestCas_FindAllRetryAcceptsDeletedRow(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()
	entry := bigChatEntry("findall-retry-2", strings.Repeat("old retained snapshot ", 40))
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	writer := casSnapshotWriter(t, cas)
	fired := casAfterRootQuery(t, cas, func() { require.NoError(t, writer.DeleteLog(ctx, entry.ID)) })
	logs, err := cas.FindAll(ctx, map[string]any{"id": entry.ID})
	require.NoError(t, err)
	require.True(t, *fired)
	require.Len(t, logs, 1, "deletion after root query must not tear the active snapshot")
	require.Equal(t, entry.InputHistory, logs[0].InputHistory)
	logs, err = cas.FindAll(ctx, map[string]any{"id": entry.ID})
	require.NoError(t, err)
	require.Empty(t, logs, "only the next snapshot observes deletion")
}

// Replaces the old retry-exhaustion test: even several committed revisions
// cannot exhaust a retry budget or silently lose a healthy snapshot row.
// Distractors prove that BOTH the original query and scope are honored, and
// unprojected model/ownership/output prove hydration preserves projection.
func TestCas_FindAllSnapshotPreservesQueryScopeProjection(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()
	for _, spec := range []struct{ id, user, model string }{
		{"target", "A", "match"}, {"wrong-query", "A", "other"}, {"wrong-scope", "B", "match"},
	} {
		entry := bigChatEntry(spec.id, strings.Repeat("history "+spec.id+" ", 40))
		entry.UserID = strPtr(spec.user)
		entry.Model = spec.model
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, cas.CreateIfNotExists(ctx, entry))
	}
	writer := casSnapshotWriter(t, cas)
	scopedCtx := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("user_id = ?", "A") })
	fired := casAfterRootQuery(t, cas, func() {
		for i := 0; i < 3; i++ {
			require.NoError(t, writer.Update(ctx, "target", map[string]any{"user_id": "B", "model": "other", "input_history": projAuthHistoryJSON(t, strings.Repeat("private B ", 40+i))}))
		}
	})
	logs, err := cas.FindAll(scopedCtx, map[string]any{"model": "match"}, "input_history")
	require.NoError(t, err)
	require.True(t, *fired)
	require.Len(t, logs, 1)
	require.Equal(t, "target", logs[0].ID)
	require.Contains(t, logs[0].InputHistory, "history target")
	require.NotContains(t, logs[0].InputHistory, "private B")
	require.Nil(t, logs[0].UserID)
	require.Empty(t, logs[0].Model)
	require.Empty(t, logs[0].OutputMessage)
	logs, err = cas.FindAll(scopedCtx, map[string]any{"model": "match"}, "input_history")
	require.NoError(t, err)
	require.Empty(t, logs)
}
