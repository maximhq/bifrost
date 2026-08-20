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

// TestLatencyOverheadHistogramMath verifies GetLatencyHistogram returns overhead
// avg/percentiles alongside latency, and that a row with a latency but no measured
// overhead (an untraced or pre-migration row) still counts toward latency and
// TotalRequests while being excluded from the overhead aggregation.
func TestLatencyOverheadHistogramMath(t *testing.T) {
	ctx := context.Background()
	sq, err := newSqliteLogStore(ctx, &SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "latency_overhead.db"),
	}, testLogger{})
	require.NoError(t, err)

	base := time.Now().UTC().Truncate(time.Second)
	// All rows land in the same 1h bucket.
	mk := func(id string, latencyMs float64, overheadMs *float64) *Log {
		l := &Log{
			ID:            id,
			Timestamp:     base,
			Object:        "chat.completion",
			Provider:      "openai",
			Model:         "gpt-4o",
			Status:        "success",
			SelectedKeyID: "sk1",
			Latency:       f64PtrP(latencyMs),
			CreatedAt:     base,
		}
		l.OverheadLatency = overheadMs
		return l
	}

	// Five rows carry both latency and overhead; one carries latency only (overhead
	// unmeasured). Latency aggregates over all six; overhead over the five measured.
	require.NoError(t, sq.Create(ctx, mk("l1", 100, f64PtrP(10))))
	require.NoError(t, sq.Create(ctx, mk("l2", 200, f64PtrP(20))))
	require.NoError(t, sq.Create(ctx, mk("l3", 300, f64PtrP(30))))
	require.NoError(t, sq.Create(ctx, mk("l4", 400, f64PtrP(40))))
	require.NoError(t, sq.Create(ctx, mk("l5", 500, f64PtrP(50))))
	require.NoError(t, sq.Create(ctx, mk("l6", 600, nil)))

	window := SearchFilters{
		StartTime: timePtrP(base.Add(-time.Hour)),
		EndTime:   timePtrP(base.Add(time.Hour)),
	}

	res, err := sq.GetLatencyHistogram(ctx, window, 3600)
	require.NoError(t, err)
	var got *LatencyHistogramBucket
	for i := range res.Buckets {
		if res.Buckets[i].TotalRequests > 0 {
			got = &res.Buckets[i]
			break
		}
	}
	require.NotNil(t, got, "expected a non-empty bucket")
	require.Equal(t, int64(6), got.TotalRequests, "the overhead-less row still counts")

	// Latency over [100,200,300,400,500,600] (n=6): computePercentile uses linear
	// interpolation at rank p*(n-1).
	require.InDelta(t, 350.0, got.AvgLatency, 1e-6)
	require.InDelta(t, 550.0, got.P90Latency, 1e-6) // rank 4.5 -> 500*0.5 + 600*0.5
	require.InDelta(t, 575.0, got.P95Latency, 1e-6) // rank 4.75 -> 500*0.25 + 600*0.75
	require.InDelta(t, 595.0, got.P99Latency, 1e-6) // rank 4.95 -> 500*0.05 + 600*0.95

	// Overhead over [10,20,30,40,50] (n=5) only: the nil-overhead row is skipped.
	require.InDelta(t, 30.0, got.AvgOverhead, 1e-6)
	require.InDelta(t, 46.0, got.P90Overhead, 1e-6) // rank 3.6 -> 40*0.4 + 50*0.6
	require.InDelta(t, 48.0, got.P95Overhead, 1e-6) // rank 3.8 -> 40*0.2 + 50*0.8
	require.InDelta(t, 49.6, got.P99Overhead, 1e-6) // rank 3.96 -> 40*0.04 + 50*0.96
}
