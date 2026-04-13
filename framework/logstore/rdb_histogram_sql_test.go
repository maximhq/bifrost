package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBuildRankedPercentileExpr_PostgresUsesAggregatedCounts(t *testing.T) {
	expr := buildRankedPercentileExpr("postgres", "latency_rank", "latency_count", 0.90, "latency_value")

	if !strings.Contains(expr, "MIN(CASE") {
		t.Fatalf("expected percentile expr to aggregate row candidates, got %q", expr)
	}
	if !strings.Contains(expr, "latency_count IS NOT NULL") {
		t.Fatalf("expected percentile expr to guard null partition sizes, got %q", expr)
	}
	if !strings.Contains(expr, "CEIL(0.900000 * latency_count)") {
		t.Fatalf("expected postgres percentile expr to use CEIL over the partition count, got %q", expr)
	}
	if strings.Contains(expr, "latency_count = 0") {
		t.Fatalf("expected percentile expr to avoid outer non-aggregated count guards, got %q", expr)
	}
	if strings.Contains(strings.ToLower(expr), "percentile_cont") {
		t.Fatalf("expected discrete percentile expression without percentile_cont, got %q", expr)
	}
}

func TestBuildRankedPercentileExpr_NullPartitionSizeReturnsNull(t *testing.T) {
	store := newTestSQLiteStore(t)
	expr := buildRankedPercentileExpr("sqlite", "latency_rank", "latency_count", 0.90, "latency_value")
	query := fmt.Sprintf(`WITH ranked AS (
		SELECT 123.0 AS latency_value, 1 AS latency_rank, NULL AS latency_count
	)
	SELECT %s AS p90 FROM ranked`, expr)

	var result struct {
		P90 sql.NullFloat64 `gorm:"column:p90"`
	}
	if err := store.db.Raw(query).Scan(&result).Error; err != nil {
		t.Fatalf("failed to execute null partition size query: %v", err)
	}
	if result.P90.Valid {
		t.Fatalf("expected null percentile when partition size is missing, got %v", result.P90.Float64)
	}
}

func TestBuildLatencyHistogramCTE_PostgresAggregatesWindowCounts(t *testing.T) {
	cte := buildLatencyHistogramCTE("postgres", "SELECT 1 AS bucket_timestamp, 1 AS latency, 1 AS tokens_per_second, 1 AS time_to_first_token", false)

	for _, expected := range []string{
		"latency_counts AS",
		"tps_counts AS",
		"ttft_counts AS",
		"LEFT JOIN latency_counts lc ON b.bucket_timestamp = lc.bucket_timestamp",
		"LEFT JOIN tps_counts tc ON b.bucket_timestamp = tc.bucket_timestamp",
		"LEFT JOIN ttft_counts fc ON b.bucket_timestamp = fc.bucket_timestamp",
		"FROM latency_ranked GROUP BY bucket_timestamp",
		"FROM tps_ranked GROUP BY bucket_timestamp",
		"FROM ttft_ranked GROUP BY bucket_timestamp",
		"CEIL(0.900000 * latency_count)",
		"CEIL(0.900000 * tps_count)",
		"CEIL(0.900000 * ttft_count)",
	} {
		if !strings.Contains(cte, expected) {
			t.Fatalf("expected CTE to contain %q, got %q", expected, cte)
		}
	}

	for _, unexpected := range []string{
		"GROUP BY bucket_timestamp, latency_count",
		"GROUP BY bucket_timestamp, tps_count",
		"GROUP BY bucket_timestamp, ttft_count",
		"COUNT(*) OVER (PARTITION BY b.bucket_timestamp) AS latency_count",
		"COUNT(*) OVER (PARTITION BY b.bucket_timestamp) AS tps_count",
		"COUNT(*) OVER (PARTITION BY b.bucket_timestamp) AS ttft_count",
	} {
		if strings.Contains(cte, unexpected) {
			t.Fatalf("expected CTE to avoid %q, got %q", unexpected, cte)
		}
	}
}

func TestMigrationDropPerformanceMetricIndexes_IsIdempotent(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	if err := store.db.Exec("CREATE INDEX idx_logs_tokens_per_second ON logs(tokens_per_second)").Error; err != nil {
		t.Fatalf("failed to create tokens_per_second index: %v", err)
	}
	if err := store.db.Exec("CREATE INDEX idx_logs_time_to_first_token ON logs(time_to_first_token)").Error; err != nil {
		t.Fatalf("failed to create time_to_first_token index: %v", err)
	}

	if err := store.db.Exec("DELETE FROM migrations WHERE id = ?", "logs_drop_performance_metric_indexes").Error; err != nil {
		t.Fatalf("failed to clear migration record: %v", err)
	}

	if !sqliteIndexExists(t, store, "idx_logs_tokens_per_second") {
		t.Fatalf("expected idx_logs_tokens_per_second to exist before migration")
	}
	if !sqliteIndexExists(t, store, "idx_logs_time_to_first_token") {
		t.Fatalf("expected idx_logs_time_to_first_token to exist before migration")
	}

	if err := migrationDropPerformanceMetricIndexes(ctx, store.db); err != nil {
		t.Fatalf("migrationDropPerformanceMetricIndexes() error = %v", err)
	}
	if err := migrationDropPerformanceMetricIndexes(ctx, store.db); err != nil {
		t.Fatalf("second migrationDropPerformanceMetricIndexes() error = %v", err)
	}

	if sqliteIndexExists(t, store, "idx_logs_tokens_per_second") {
		t.Fatalf("expected idx_logs_tokens_per_second to be dropped")
	}
	if sqliteIndexExists(t, store, "idx_logs_time_to_first_token") {
		t.Fatalf("expected idx_logs_time_to_first_token to be dropped")
	}
}

func TestProviderLatencyHistogram_MultiProviderMultiBucketIsolation(t *testing.T) {
	store := newTestSQLiteStore(t)
	base := time.Date(2026, 3, 24, 9, 0, 5, 0, time.UTC)

	mustCreateLogs(t, store, []Log{
		{ID: "iso-a-1", Timestamp: base, Object: "chat_completion", Provider: "a", Model: "gpt", Status: "success", Latency: floatPtr(100), TokensPerSecond: floatPtr(10), TimeToFirstToken: floatPtr(1)},
		{ID: "iso-a-2", Timestamp: base.Add(5 * time.Second), Object: "chat_completion", Provider: "a", Model: "gpt", Status: "success", Latency: floatPtr(110), TokensPerSecond: floatPtr(20), TimeToFirstToken: floatPtr(2)},
		{ID: "iso-b-1", Timestamp: base.Add(10 * time.Second), Object: "chat_completion", Provider: "b", Model: "gpt", Status: "success", Latency: floatPtr(200), TokensPerSecond: floatPtr(100), TimeToFirstToken: floatPtr(10)},
		{ID: "iso-b-2", Timestamp: base.Add(15 * time.Second), Object: "chat_completion", Provider: "b", Model: "gpt", Status: "success", Latency: floatPtr(220), TokensPerSecond: floatPtr(200), TimeToFirstToken: floatPtr(20)},
		{ID: "iso-a-3", Timestamp: base.Add(60 * time.Second), Object: "chat_completion", Provider: "a", Model: "gpt", Status: "success", Latency: floatPtr(300), TokensPerSecond: floatPtr(30), TimeToFirstToken: floatPtr(3)},
		{ID: "iso-b-3", Timestamp: base.Add(65 * time.Second), Object: "chat_completion", Provider: "b", Model: "gpt", Status: "success", Latency: floatPtr(400), TokensPerSecond: floatPtr(300), TimeToFirstToken: floatPtr(30)},
	})

	start := base.Add(-1 * time.Second)
	end := base.Add(2 * time.Minute)
	res, err := store.GetProviderLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetProviderLatencyHistogram() error = %v", err)
	}

	bucket1TS := (base.Unix() / 60) * 60
	bucket2TS := ((base.Add(60 * time.Second)).Unix() / 60) * 60

	bucket1, ok := findProviderLatencyBucketByUnix(bucket1TS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d", bucket1TS)
	}
	bucket2, ok := findProviderLatencyBucketByUnix(bucket2TS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d", bucket2TS)
	}

	approxEqualFloat64(t, bucket1.ByProvider["a"].AvgLatency, 105)
	approxEqualFloat64(t, bucket1.ByProvider["b"].AvgLatency, 210)
	approxEqualFloat64(t, bucket2.ByProvider["a"].AvgLatency, 300)
	approxEqualFloat64(t, bucket2.ByProvider["b"].AvgLatency, 400)

	if bucket1.ByProvider["a"].P90TokensPerSecond == nil || bucket1.ByProvider["b"].P90TokensPerSecond == nil {
		t.Fatalf("expected provider TPS percentiles in first bucket")
	}
	if bucket2.ByProvider["a"].P90TimeToFirstToken == nil || bucket2.ByProvider["b"].P90TimeToFirstToken == nil {
		t.Fatalf("expected provider TTFT percentiles in second bucket")
	}

	approxEqualFloat64(t, *bucket1.ByProvider["a"].P90TokensPerSecond, 20)
	approxEqualFloat64(t, *bucket1.ByProvider["b"].P90TokensPerSecond, 200)
	approxEqualFloat64(t, *bucket2.ByProvider["a"].P90TimeToFirstToken, 3)
	approxEqualFloat64(t, *bucket2.ByProvider["b"].P90TimeToFirstToken, 30)
}

func TestLatencyHistogram_PostgresWindowedQueryRuns(t *testing.T) {
	store, db := setupPerfTestDB(t)
	base := time.Date(2026, 3, 24, 11, 0, 0, 0, time.UTC)

	logs := []Log{
		{ID: "pg-hist-1", Timestamp: base, Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(100), TokensPerSecond: floatPtr(10), TimeToFirstToken: floatPtr(1)},
		{ID: "pg-hist-2", Timestamp: base.Add(5 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(110), TokensPerSecond: floatPtr(20), TimeToFirstToken: floatPtr(2)},
		{ID: "pg-hist-3", Timestamp: base.Add(10 * time.Second), Object: "chat_completion", Provider: "anthropic", Model: "claude", Status: "success", Latency: floatPtr(210), TokensPerSecond: floatPtr(30), TimeToFirstToken: floatPtr(3)},
	}
	if err := db.WithContext(context.Background()).Create(&logs).Error; err != nil {
		t.Fatalf("failed to seed postgres logs: %v", err)
	}

	start := base.Add(-1 * time.Second)
	end := base.Add(1 * time.Minute)

	res, err := store.GetLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetLatencyHistogram() on postgres error = %v", err)
	}
	if len(res.Buckets) != 1 {
		t.Fatalf("expected one postgres histogram bucket, got %d", len(res.Buckets))
	}

	providerRes, err := store.GetProviderLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetProviderLatencyHistogram() on postgres error = %v", err)
	}
	if len(providerRes.Buckets) != 1 {
		t.Fatalf("expected one postgres provider histogram bucket, got %d", len(providerRes.Buckets))
	}
	if len(providerRes.Buckets[0].ByProvider) != 2 {
		t.Fatalf("expected two provider entries in postgres bucket, got %d", len(providerRes.Buckets[0].ByProvider))
	}
}

func sqliteIndexExists(t *testing.T, store *RDBLogStore, indexName string) bool {
	t.Helper()

	var count int64
	if err := store.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", indexName).Scan(&count).Error; err != nil {
		t.Fatalf("failed to check sqlite index %s: %v", indexName, err)
	}

	return count > 0
}
