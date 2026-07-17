package logstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestThroughputHistogramMath verifies the aggregate tokens/sec computation:
// SUM(completion_tokens) / (SUM(latency_ms)/1000), including the per-provider
// breakdown, and that rows without a latency are excluded from the rate.
func TestThroughputHistogramMath(t *testing.T) {
	ctx := context.Background()
	sq, err := newSqliteLogStore(ctx, &SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "throughput.db"),
	}, testLogger{})
	require.NoError(t, err)

	base := time.Now().UTC().Truncate(time.Second)
	// All rows land in the same 1h bucket.
	mk := func(id, provider, status string, latencyMs float64, completion int, withLatency bool) *Log {
		l := &Log{
			ID:               id,
			Timestamp:        base,
			Object:           "chat.completion",
			Provider:         provider,
			Model:            "gpt-4o",
			Status:           status,
			SelectedKeyID:    "sk1",
			CompletionTokens: completion,
			TotalTokens:      completion,
			CreatedAt:        base,
		}
		if withLatency {
			l.Latency = f64PtrP(latencyMs)
		}
		return l
	}

	// openai: 100 tok / 1000ms and 300 tok / 1000ms => 400 tok / 2s = 200 tok/s
	require.NoError(t, sq.Create(ctx, mk("t1", "openai", "success", 1000, 100, true)))
	require.NoError(t, sq.Create(ctx, mk("t2", "openai", "success", 1000, 300, true)))
	// anthropic: 50 tok / 500ms => 50 / 0.5s = 100 tok/s
	require.NoError(t, sq.Create(ctx, mk("t3", "anthropic", "success", 500, 50, true)))
	// row without latency must be ignored (would otherwise inflate token count)
	require.NoError(t, sq.Create(ctx, mk("t4", "openai", "success", 0, 9999, false)))
	// success row with a recorded latency of exactly 0 must ALSO be excluded: its
	// tokens would land in the numerator with nothing in the denominator and
	// inflate the rate to infinity. This mirrors the matview's throughput_* columns,
	// which are computed with the same latency > 0 filter (see matviews.go).
	require.NoError(t, sq.Create(ctx, mk("t7", "openai", "success", 0, 8888, true)))
	// failed/cancelled rows must be excluded from BOTH numerator and denominator:
	// they burn latency but generate no (or partial) tokens, which would otherwise
	// deflate the rate. Each spends 10s here — enough to skew the result if counted.
	require.NoError(t, sq.Create(ctx, mk("t5", "openai", "error", 10000, 0, true)))
	require.NoError(t, sq.Create(ctx, mk("t6", "anthropic", "cancelled", 10000, 5, true)))

	window := SearchFilters{
		StartTime: timePtrP(base.Add(-time.Hour)),
		EndTime:   timePtrP(base.Add(time.Hour)),
	}

	// Overall: (400 + 50) tokens / (2000 + 500 ms) = 450 / 2.5s = 180 tok/s
	overall, err := sq.GetThroughputHistogram(ctx, window, 3600)
	require.NoError(t, err)
	var got *ThroughputHistogramBucket
	for i := range overall.Buckets {
		if overall.Buckets[i].TotalRequests > 0 {
			got = &overall.Buckets[i]
			break
		}
	}
	require.NotNil(t, got, "expected a non-empty bucket")
	require.InDelta(t, 180.0, got.TokensPerSecond, 1e-6)
	require.Equal(t, int64(450), got.TotalCompletionTokens)
	require.Equal(t, int64(3), got.TotalRequests, "latency-less, error, and cancelled rows must be excluded")

	// Per-provider breakdown.
	byProv, err := sq.GetProviderThroughputHistogram(ctx, window, 3600)
	require.NoError(t, err)
	var stats map[string]ProviderThroughputStats
	for i := range byProv.Buckets {
		if len(byProv.Buckets[i].ByProvider) > 0 {
			stats = byProv.Buckets[i].ByProvider
			break
		}
	}
	require.NotNil(t, stats)
	require.InDelta(t, 200.0, stats["openai"].TokensPerSecond, 1e-6)
	require.Equal(t, int64(2), stats["openai"].TotalRequests)
	require.InDelta(t, 100.0, stats["anthropic"].TokensPerSecond, 1e-6)
	require.Equal(t, int64(1), stats["anthropic"].TotalRequests)

	// Model rankings expose the same per-model throughput. All rows use model
	// "gpt-4o", so ranking groups by (model, provider): openai => 200 tok/s,
	// anthropic => 100 tok/s. The latency-less / zero-latency / non-success rows
	// (t4, t7, t5, t6) must be excluded from the rate exactly as above.
	rankings, err := sq.GetModelRankings(ctx, window)
	require.NoError(t, err)
	tpByProvider := make(map[string]float64)
	for _, r := range rankings.Rankings {
		require.Equal(t, "gpt-4o", r.Model)
		tpByProvider[r.Provider] = r.Throughput
	}
	require.InDelta(t, 200.0, tpByProvider["openai"], 1e-6)
	require.InDelta(t, 100.0, tpByProvider["anthropic"], 1e-6)
}
