package logstore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newPassthroughStore seeds an in-memory sqlite store with a typed chat row, a passthrough row stored
// under its inferred type, and a legacy/unknown-route passthrough row. See newRootsOnlyStore for why
// the DSN is a named shared cache and closed on cleanup.
func newPassthroughStore(t *testing.T) *RDBLogStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&Log{}))

	now := time.Now()
	seed := []Log{
		{ID: "typed-chat", Timestamp: now, Status: "success", Provider: "openai", Object: "chat_completion"},
		{ID: "passthrough-chat", Timestamp: now.Add(-time.Second), Status: "success", Provider: "openai", Object: "chat_completion", IsPassthrough: true},
		{ID: "passthrough-raw", Timestamp: now.Add(-2 * time.Second), Status: "success", Provider: "openai", Object: "passthrough", IsPassthrough: true},
		{ID: "embedding", Timestamp: now.Add(-3 * time.Second), Status: "success", Provider: "openai", Object: "embedding"},
		{ID: "passthrough-chat-stream", Timestamp: now.Add(-4 * time.Second), Status: "success", Provider: "openai", Object: "chat_completion_stream", IsPassthrough: true, Stream: true},
		{ID: "legacy-passthrough-stream", Timestamp: now.Add(-5 * time.Second), Status: "success", Provider: "openai", Object: "passthrough_stream", Stream: true},
	}
	for i := range seed {
		require.NoError(t, seed[i].SerializeFields())
		require.NoError(t, db.Create(&seed[i]).Error)
	}
	return &RDBLogStore{db: db}
}

// TestSearchLogsObjectsFilterMatchesTypedPassthrough pins that a passthrough call stored under its
// inferred type counts as that type, and that asking for passthrough still finds it.
func TestSearchLogsObjectsFilterMatchesTypedPassthrough(t *testing.T) {
	store := newPassthroughStore(t)

	require.Equal(t, []string{"typed-chat", "passthrough-chat"}, searchIDs(t, store, SearchFilters{Objects: []string{"chat_completion"}}))
	// passthrough matches every passthrough call, streamed or not, typed or not.
	require.Equal(t, []string{"passthrough-chat", "passthrough-raw", "passthrough-chat-stream", "legacy-passthrough-stream"}, searchIDs(t, store, SearchFilters{Objects: []string{"passthrough"}}))
	require.Equal(t, []string{"passthrough-chat", "passthrough-raw", "embedding", "passthrough-chat-stream", "legacy-passthrough-stream"}, searchIDs(t, store, SearchFilters{Objects: []string{"passthrough", "embedding"}}))
	// passthrough_stream alone matches only the streamed ones.
	require.Equal(t, []string{"passthrough-chat-stream", "legacy-passthrough-stream"}, searchIDs(t, store, SearchFilters{Objects: []string{"passthrough_stream"}}))
	require.Equal(t, []string{"passthrough-chat", "passthrough-raw", "passthrough-chat-stream", "legacy-passthrough-stream"}, searchIDs(t, store, SearchFilters{Objects: []string{"passthrough", "passthrough_stream"}}))

	res, err := store.SearchLogs(t.Context(), SearchFilters{RequestID: "passthrough-chat"}, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, res.Logs, 1)
	require.True(t, res.Logs[0].IsPassthrough, "the list query must return is_passthrough so the UI can tag the row")
}

func TestCanUseMatViewFiltersRejectsPassthroughObjects(t *testing.T) {
	require.True(t, canUseMatViewFilters(SearchFilters{Objects: []string{"chat_completion"}}))
	require.False(t, canUseMatViewFilters(SearchFilters{Objects: []string{"passthrough"}}))
	require.False(t, canUseMatViewFilters(SearchFilters{Objects: []string{"embedding", "passthrough_stream"}}))
}
