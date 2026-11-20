package logstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelRankingsManyPairsSQLite pins that an unlimited model ranking over
// more (model, provider) pairs than SQLite's expression depth allows still
// answers, with trends. The previous-period query once filtered with one OR
// term per pair, which SQLite rejects past about 1,000 terms; above a modest
// count the filter is dropped, since previous rows are matched to the current
// ones by pair anyway.
func TestModelRankingsManyPairsSQLite(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	now := time.Now().UTC().Truncate(time.Second)
	const models = 1200
	tx := store.db.Begin()
	for i := 0; i < models; i++ {
		for _, offset := range []time.Duration{-30 * time.Minute, -90 * time.Minute} {
			require.NoError(t, tx.Exec(`
				INSERT INTO logs (id, timestamp, object_type, provider, model, status, latency, total_tokens, cost, created_at)
				VALUES (?, ?, 'chat.completion', 'openai', ?, 'success', 100, 10, 0.01, ?)
			`, fmt.Sprintf("rp-%d-%d", i, offset), now.Add(offset), fmt.Sprintf("model-%04d", i), now.Add(offset)).Error)
		}
	}
	require.NoError(t, tx.Commit().Error)

	start, end := now.Add(-time.Hour), now
	all := 0
	res, err := store.GetModelRankings(context.Background(), SearchFilters{StartTime: &start, EndTime: &end, RankingLimit: &all})
	require.NoError(t, err, "a ranking over many pairs still answers")
	assert.Len(t, res.Rankings, models)
	for _, r := range res.Rankings {
		require.Equal(t, int64(1), r.TotalRequests)
	}
}
