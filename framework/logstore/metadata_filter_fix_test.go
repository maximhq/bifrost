package logstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertLogWithMetadata inserts a log row with the given JSON metadata string via raw SQL,
// bypassing GORM serialization so the exact byte sequence reaches the database.
func insertLogWithMetadata(t *testing.T, store *RDBLogStore, id string, metadataJSON string, ts time.Time) {
	t.Helper()
	err := store.db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, metadata, created_at)
		VALUES (?, ?, 'chat.completion', 'openai', 'gpt-4o', 'success', ?, ?)
	`, id, ts, metadataJSON, ts).Error
	require.NoError(t, err, "failed to insert log %q", id)
}

// runMetadataStringMatchingSuite verifies that metadata filter values that look like numbers
// are matched as JSON strings. Both SQLite and Postgres store all metadata values as JSON
// strings (from HTTP headers), so both dialects must match them correctly.
//
// Boolean values ("true"/"false") are excluded: SQLite intentionally uses json_type to match
// JSON booleans for those values, while Postgres always matches as a JSON string.
func runMetadataStringMatchingSuite(t *testing.T, store *RDBLogStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	insertLogWithMetadata(t, store, "meta-num", `{"chat-id": "4000126002"}`, now.Add(-1*time.Second))
	insertLogWithMetadata(t, store, "meta-float", `{"score": "3.14"}`, now.Add(-2*time.Second))
	insertLogWithMetadata(t, store, "meta-str", `{"env": "production"}`, now.Add(-3*time.Second))
	// Row with different value — must NOT appear in filtered results.
	insertLogWithMetadata(t, store, "meta-other", `{"chat-id": "9999"}`, now.Add(-4*time.Second))

	tests := []struct {
		name     string
		filters  map[string]string
		wantIDs  []string
		wantMiss []string
	}{
		{
			name:     "numeric_string_value_matches",
			filters:  map[string]string{"chat-id": "4000126002"},
			wantIDs:  []string{"meta-num"},
			wantMiss: []string{"meta-other"},
		},
		{
			name:    "float_string_matches",
			filters: map[string]string{"score": "3.14"},
			wantIDs: []string{"meta-float"},
		},
		{
			name:    "plain_string_matches",
			filters: map[string]string{"env": "production"},
			wantIDs: []string{"meta-str"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := store.SearchLogs(ctx, SearchFilters{MetadataFilters: tc.filters}, PaginationOptions{Limit: 100})
			require.NoError(t, err)
			require.NotNil(t, result)

			gotIDs := make(map[string]bool, len(result.Logs))
			for _, l := range result.Logs {
				gotIDs[l.ID] = true
			}
			for _, wantID := range tc.wantIDs {
				assert.True(t, gotIDs[wantID], "expected log %q to be returned by filter %v", wantID, tc.filters)
			}
			for _, missID := range tc.wantMiss {
				assert.False(t, gotIDs[missID], "log %q should NOT be returned by filter %v", missID, tc.filters)
			}
		})
	}
}

// runPostgresMetadataBooleanStringMatchingSuite verifies that values like "true" and "false"
// that come from HTTP headers are stored as JSON strings and match as strings via JSONB @>
// containment. The old code emitted a JSON boolean fragment {"active": true} which never
// matched the stored string "true".
func runPostgresMetadataBooleanStringMatchingSuite(t *testing.T, store *RDBLogStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	insertLogWithMetadata(t, store, "pg-bool-true", `{"active": "true"}`, now.Add(-1*time.Second))
	insertLogWithMetadata(t, store, "pg-bool-false", `{"active": "false"}`, now.Add(-2*time.Second))

	t.Run("boolean_true_string_matches", func(t *testing.T) {
		result, err := store.SearchLogs(ctx, SearchFilters{MetadataFilters: map[string]string{"active": "true"}}, PaginationOptions{Limit: 100})
		require.NoError(t, err)
		require.NotNil(t, result)
		gotIDs := make(map[string]bool)
		for _, l := range result.Logs {
			gotIDs[l.ID] = true
		}
		assert.True(t, gotIDs["pg-bool-true"], "stored string 'true' must match filter active=true on Postgres")
		assert.False(t, gotIDs["pg-bool-false"], "log with active='false' must not match filter active=true")
	})

	t.Run("boolean_false_string_matches", func(t *testing.T) {
		result, err := store.SearchLogs(ctx, SearchFilters{MetadataFilters: map[string]string{"active": "false"}}, PaginationOptions{Limit: 100})
		require.NoError(t, err)
		require.NotNil(t, result)
		gotIDs := make(map[string]bool)
		for _, l := range result.Logs {
			gotIDs[l.ID] = true
		}
		assert.True(t, gotIDs["pg-bool-false"], "stored string 'false' must match filter active=false on Postgres")
		assert.False(t, gotIDs["pg-bool-true"], "log with active='true' must not match filter active=false")
	})
}

// runPaginationTotalCountSuite verifies that SearchLogs sets pagination.TotalCount correctly.
// Previously, totalCount was computed but only stored in Stats.TotalRequests — never
// assigned to Pagination.TotalCount — so every response returned total_count: 0.
func runPaginationTotalCountSuite(t *testing.T, store *RDBLogStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	const total = 5
	const pageLimit = 2

	for i := 0; i < total; i++ {
		err := store.Create(ctx, &Log{
			ID:        fmt.Sprintf("%s-log-%d", t.Name(), i),
			Timestamp: now.Add(-time.Duration(i) * time.Second),
			Object:    "chat.completion",
			Provider:  "openai",
			Model:     "gpt-4o",
			Status:    "success",
			CreatedAt: now,
		})
		require.NoError(t, err, "failed to insert log %d", i)
	}

	// Page 1: limit < total — TotalCount must equal total, not just the page size.
	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: pageLimit})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, int64(total), result.Pagination.TotalCount,
		"TotalCount should equal total rows (%d), not page size (%d)", total, pageLimit)
	assert.Len(t, result.Logs, pageLimit, "result page should have %d rows", pageLimit)

	// Page 2: with offset — TotalCount must still equal total.
	result2, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: pageLimit, Offset: pageLimit})
	require.NoError(t, err)
	require.NotNil(t, result2)
	assert.Equal(t, int64(total), result2.Pagination.TotalCount,
		"TotalCount should be stable across pages")

	// No results: TotalCount should be 0.
	result3, err := store.SearchLogs(ctx, SearchFilters{Models: []string{"nonexistent-model-xyz"}}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	require.NotNil(t, result3)
	assert.Equal(t, int64(0), result3.Pagination.TotalCount,
		"TotalCount should be 0 when no rows match")
}

// TestSearchLogs_MetadataFilter_StringMatching_SQLite exercises the metadata string matching fix on SQLite.
func TestSearchLogs_MetadataFilter_StringMatching_SQLite(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	runMetadataStringMatchingSuite(t, store)
}

// TestSearchLogs_MetadataFilter_StringMatching_Postgres exercises the metadata string matching fix on Postgres,
// where the old code generated JSONB number/boolean fragments that never matched stored string values.
func TestSearchLogs_MetadataFilter_StringMatching_Postgres(t *testing.T) {
	store, _ := setupPerfTestDB(t)
	runMetadataStringMatchingSuite(t, store)
	runPostgresMetadataBooleanStringMatchingSuite(t, store)
}

// TestSearchLogs_PaginationTotalCount_SQLite verifies pagination.TotalCount on SQLite.
func TestSearchLogs_PaginationTotalCount_SQLite(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	runPaginationTotalCountSuite(t, store)
}

// TestSearchLogs_PaginationTotalCount_Postgres verifies pagination.TotalCount on Postgres.
func TestSearchLogs_PaginationTotalCount_Postgres(t *testing.T) {
	store, _ := setupPerfTestDB(t)
	runPaginationTotalCountSuite(t, store)
}
