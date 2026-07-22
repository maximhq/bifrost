package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearchLogs_InverseFilters verifies that SearchFilters.Inverse turns each
// categorical filter into an exclusion (NOT IN) while keeping rows whose column
// is NULL/unset — the key correctness property for nullable dimensions like
// user_id. Runs on SQLite (which exercises the same applyFilters path used by
// Postgres and ClickHouse).
func TestSearchLogs_InverseFilters(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	ctx := context.Background()
	now := time.Now().UTC()

	entries := []*Log{
		{ID: "chat-alice", Timestamp: now.Add(-1 * time.Second), Object: "chat.completion", Provider: "openai", Model: "gpt-4o", Status: "success", UserID: strPtr("alice"), CreatedAt: now},
		{ID: "listmodels-bob", Timestamp: now.Add(-2 * time.Second), Object: "list_models", Provider: "openai", Model: "gpt-4o", Status: "success", UserID: strPtr("bob"), CreatedAt: now},
		{ID: "embedding-nouser", Timestamp: now.Add(-3 * time.Second), Object: "embedding", Provider: "anthropic", Model: "claude", Status: "success", UserID: nil, CreatedAt: now},
	}
	for _, e := range entries {
		require.NoError(t, store.Create(ctx, e), "insert %s", e.ID)
	}

	search := func(f SearchFilters) map[string]bool {
		result, err := store.SearchLogs(ctx, f, PaginationOptions{Limit: 100})
		require.NoError(t, err)
		got := make(map[string]bool, len(result.Logs))
		for _, l := range result.Logs {
			got[l.ID] = true
		}
		return got
	}

	t.Run("exclude_object_type", func(t *testing.T) {
		got := search(SearchFilters{Objects: []string{"list_models"}, Inverse: true})
		assert.False(t, got["listmodels-bob"], "list_models row must be excluded")
		assert.True(t, got["chat-alice"], "non-excluded row must remain")
		assert.True(t, got["embedding-nouser"], "non-excluded row must remain")
	})

	t.Run("exclude_user_keeps_null_user", func(t *testing.T) {
		got := search(SearchFilters{UserIDs: []string{"alice"}, Inverse: true})
		assert.False(t, got["chat-alice"], "user alice must be excluded")
		assert.True(t, got["listmodels-bob"], "other user must remain")
		assert.True(t, got["embedding-nouser"], "row with NULL user_id must remain when excluding a specific user")
	})

	t.Run("inclusion_still_works", func(t *testing.T) {
		got := search(SearchFilters{Objects: []string{"list_models"}})
		assert.True(t, got["listmodels-bob"])
		assert.False(t, got["chat-alice"])
		assert.False(t, got["embedding-nouser"])
	})
}

// TestSearchLogs_InverseMetadataFilter verifies that inverse metadata filtering
// excludes rows that have the key=value and keeps both non-matching rows and
// rows with no metadata at all.
func TestSearchLogs_InverseMetadataFilter(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	ctx := context.Background()
	now := time.Now().UTC()

	insertLogWithMetadata(t, store, "meta-prod", `{"env": "production"}`, now.Add(-1*time.Second))
	insertLogWithMetadata(t, store, "meta-dev", `{"env": "development"}`, now.Add(-2*time.Second))
	require.NoError(t, store.Create(ctx, &Log{
		ID: "meta-none", Timestamp: now.Add(-3 * time.Second), Object: "chat.completion",
		Provider: "openai", Model: "gpt-4o", Status: "success", CreatedAt: now,
	}), "insert no-metadata row")

	result, err := store.SearchLogs(ctx, SearchFilters{MetadataFilters: map[string]string{"env": "production"}, Inverse: true}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	got := make(map[string]bool, len(result.Logs))
	for _, l := range result.Logs {
		got[l.ID] = true
	}
	assert.False(t, got["meta-prod"], "row with env=production must be excluded")
	assert.True(t, got["meta-dev"], "row with a different metadata value must remain")
	assert.True(t, got["meta-none"], "row with no metadata must remain when excluding a metadata value")
}
