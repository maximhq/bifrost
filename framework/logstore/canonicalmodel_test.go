package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// seedCanonicalModelLogs inserts rows covering the canonical-name cases: a
// profile-ID model mixing canonical and NULL rows, and a plain model without
// one (empty canonicalName is stored as NULL). Returns the query window.
func seedCanonicalModelLogs(t *testing.T, db *gorm.DB) SearchFilters {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	insert := func(id, provider, model, canonicalName string, offset time.Duration) {
		var canonical any
		if canonicalName != "" {
			canonical = canonicalName
		}
		err := db.Exec(`
			INSERT INTO logs (id, timestamp, object_type, provider, model, status, canonical_model_name, latency, total_tokens, cost, created_at)
			VALUES (?, ?, 'chat.completion', ?, ?, 'success', ?, 100, 10, 0.01, ?)
		`, id, now.Add(offset), provider, model, canonical, now.Add(offset)).Error
		require.NoError(t, err, "failed to insert log %q", id)
	}
	insert("cm-1", "bedrock", "4xg7dq2mkz9v", "claude-sonnet-4", -10*time.Second)
	insert("cm-2", "bedrock", "4xg7dq2mkz9v", "", -20*time.Second)
	insert("cm-3", "openai", "gpt-4o", "", -30*time.Second)

	start := now.Add(-2 * time.Hour)
	end := now.Add(time.Hour)
	return SearchFilters{StartTime: &start, EndTime: &end}
}

// assertCanonicalModelRankings verifies that rankings surface the canonical
// name for models that have one and leave it unset otherwise. A model whose
// rows mix NULL and non-NULL canonical values must resolve to the non-NULL
// name while staying grouped by the raw model.
func assertCanonicalModelRankings(t *testing.T, res *ModelRankingResult) {
	t.Helper()
	byModel := make(map[string]ModelRankingWithTrend, len(res.Rankings))
	for _, r := range res.Rankings {
		byModel[r.Model] = r
	}

	profile, ok := byModel["4xg7dq2mkz9v"]
	require.True(t, ok, "profile-ID model must be ranked")
	require.NotNil(t, profile.CanonicalModelName, "canonical name must be set when any row carries one")
	assert.Equal(t, "claude-sonnet-4", *profile.CanonicalModelName)
	assert.Equal(t, int64(2), profile.TotalRequests, "grouping must stay keyed by the raw model")

	plain, ok := byModel["gpt-4o"]
	require.True(t, ok)
	assert.Nil(t, plain.CanonicalModelName, "models without canonical rows must not get one")
}

func TestCanonicalModelRankings_SQLite(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	filters := seedCanonicalModelLogs(t, store.db)

	res, err := store.GetModelRankings(context.Background(), filters)
	require.NoError(t, err)
	assertCanonicalModelRankings(t, res)
}

// TestCanonicalModelRankings_PostgresRaw exercises the raw-table path: the
// store is built with matViewsReady unset, so rankings take the same query
// SQLite and ClickHouse use.
func TestCanonicalModelRankings_PostgresRaw(t *testing.T) {
	store, db := setupPerfTestDB(t)
	filters := seedCanonicalModelLogs(t, db)

	res, err := store.GetModelRankings(context.Background(), filters)
	require.NoError(t, err)
	assertCanonicalModelRankings(t, res)
}

// TestCanonicalModelRankings_PostgresMatView exercises the mv_logs_hourly
// path directly: canonical_model_name is a view dimension, and the rankings
// reader must re-aggregate it per raw model.
func TestCanonicalModelRankings_PostgresMatView(t *testing.T) {
	store, db := setupPerfTestDB(t)
	filters := seedCanonicalModelLogs(t, db)
	refreshTestMatViews(t, db)

	res, err := store.getModelRankingsFromMatView(context.Background(), filters)
	require.NoError(t, err)
	assertCanonicalModelRankings(t, res)
}
