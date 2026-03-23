package logstore

import (
	"context"
	"math"
	"testing"
	"time"
)

func floatPtr(v float64) *float64 {
	vv := v
	return &vv
}

func approxEqualFloat64(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("value mismatch: got=%v want=%v", got, want)
	}
}

func mustCreateLogs(t *testing.T, store *RDBLogStore, logs []Log) {
	t.Helper()
	if err := store.db.WithContext(context.Background()).Create(&logs).Error; err != nil {
		t.Fatalf("failed to seed logs: %v", err)
	}
}

func findLatencyBucketByUnix(ts int64, buckets []LatencyHistogramBucket) (LatencyHistogramBucket, bool) {
	for _, b := range buckets {
		if b.Timestamp.Unix() == ts {
			return b, true
		}
	}
	return LatencyHistogramBucket{}, false
}

func findProviderLatencyBucketByUnix(ts int64, buckets []ProviderLatencyHistogramBucket) (ProviderLatencyHistogramBucket, bool) {
	for _, b := range buckets {
		if b.Timestamp.Unix() == ts {
			return b, true
		}
	}
	return ProviderLatencyHistogramBucket{}, false
}

func TestLatencyHistogram_AllNullTPSAndTTFTRemainNil(t *testing.T) {
	store := newTestSQLiteStore(t)
	base := time.Date(2026, 3, 23, 12, 0, 30, 0, time.UTC)
	latA := 100.0
	latB := 200.0

	mustCreateLogs(t, store, []Log{
		{ID: "h-null-1", Timestamp: base, Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: &latA},
		{ID: "h-null-2", Timestamp: base.Add(10 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: &latB},
	})

	start := base.Add(-10 * time.Second)
	end := base.Add(20 * time.Second)
	res, err := store.GetLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetLatencyHistogram() error = %v", err)
	}

	bucketTS := (base.Unix() / 60) * 60
	bucket, ok := findLatencyBucketByUnix(bucketTS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d to exist", bucketTS)
	}
	if bucket.AvgTokensPerSecond != nil {
		t.Fatalf("expected avg_tokens_per_second nil, got %v", *bucket.AvgTokensPerSecond)
	}
	if bucket.P90TokensPerSecond != nil {
		t.Fatalf("expected p90_tokens_per_second nil, got %v", *bucket.P90TokensPerSecond)
	}
	if bucket.AvgTimeToFirstToken != nil {
		t.Fatalf("expected avg_time_to_first_token nil, got %v", *bucket.AvgTimeToFirstToken)
	}
	if bucket.P90TimeToFirstToken != nil {
		t.Fatalf("expected p90_time_to_first_token nil, got %v", *bucket.P90TimeToFirstToken)
	}
}

func TestLatencyHistogram_MixedNullAndValuesPercentileUsesNonNullSet(t *testing.T) {
	store := newTestSQLiteStore(t)
	base := time.Date(2026, 3, 23, 13, 1, 20, 0, time.UTC)

	mustCreateLogs(t, store, []Log{
		{ID: "h-mixed-1", Timestamp: base, Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(100)},
		{ID: "h-mixed-2", Timestamp: base.Add(1 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(110), TokensPerSecond: floatPtr(10)},
		{ID: "h-mixed-3", Timestamp: base.Add(2 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(120), TokensPerSecond: floatPtr(20)},
		{ID: "h-mixed-4", Timestamp: base.Add(3 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(130), TokensPerSecond: floatPtr(30)},
	})

	start := base.Add(-1 * time.Second)
	end := base.Add(10 * time.Second)
	res, err := store.GetLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetLatencyHistogram() error = %v", err)
	}

	bucketTS := (base.Unix() / 60) * 60
	bucket, ok := findLatencyBucketByUnix(bucketTS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d to exist", bucketTS)
	}
	if bucket.AvgTokensPerSecond == nil {
		t.Fatalf("expected avg_tokens_per_second to be non-nil")
	}
	if bucket.P90TokensPerSecond == nil {
		t.Fatalf("expected p90_tokens_per_second to be non-nil")
	}
	approxEqualFloat64(t, *bucket.AvgTokensPerSecond, 20)
	approxEqualFloat64(t, *bucket.P90TokensPerSecond, 30)
}

func TestLatencyHistogram_SingleValuePercentilesMatchValue(t *testing.T) {
	store := newTestSQLiteStore(t)
	base := time.Date(2026, 3, 23, 14, 2, 5, 0, time.UTC)

	mustCreateLogs(t, store, []Log{
		{
			ID:               "h-single-1",
			Timestamp:        base,
			Object:           "chat_completion",
			Provider:         "openai",
			Model:            "gpt",
			Status:           "success",
			Latency:          floatPtr(123),
			TokensPerSecond:  floatPtr(42),
			TimeToFirstToken: floatPtr(5),
		},
	})

	start := base.Add(-1 * time.Second)
	end := base.Add(5 * time.Second)
	res, err := store.GetLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetLatencyHistogram() error = %v", err)
	}

	bucketTS := (base.Unix() / 60) * 60
	bucket, ok := findLatencyBucketByUnix(bucketTS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d to exist", bucketTS)
	}
	approxEqualFloat64(t, bucket.P90Latency, 123)
	approxEqualFloat64(t, bucket.P95Latency, 123)
	approxEqualFloat64(t, bucket.P99Latency, 123)
	if bucket.P90TokensPerSecond == nil || bucket.P90TimeToFirstToken == nil {
		t.Fatalf("expected TPS/TTFT p90 values to be non-nil")
	}
	approxEqualFloat64(t, *bucket.P90TokensPerSecond, 42)
	approxEqualFloat64(t, *bucket.P90TimeToFirstToken, 5)
}

func TestLatencyHistogram_MultipleBucketsNoLeakage(t *testing.T) {
	store := newTestSQLiteStore(t)
	base1 := time.Date(2026, 3, 23, 15, 10, 5, 0, time.UTC)
	base2 := base1.Add(1 * time.Minute)

	mustCreateLogs(t, store, []Log{
		{ID: "h-b1-1", Timestamp: base1, Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(100), TokensPerSecond: floatPtr(10)},
		{ID: "h-b1-2", Timestamp: base1.Add(10 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(120), TokensPerSecond: floatPtr(20)},
		{ID: "h-b2-1", Timestamp: base2, Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(200), TokensPerSecond: floatPtr(100)},
		{ID: "h-b2-2", Timestamp: base2.Add(10 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(220), TokensPerSecond: floatPtr(200)},
	})

	start := base1.Add(-1 * time.Second)
	end := base2.Add(20 * time.Second)
	res, err := store.GetLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetLatencyHistogram() error = %v", err)
	}

	bucket1TS := (base1.Unix() / 60) * 60
	bucket2TS := (base2.Unix() / 60) * 60
	bucket1, ok := findLatencyBucketByUnix(bucket1TS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d", bucket1TS)
	}
	bucket2, ok := findLatencyBucketByUnix(bucket2TS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d", bucket2TS)
	}
	if bucket1.AvgTokensPerSecond == nil || bucket2.AvgTokensPerSecond == nil {
		t.Fatalf("expected non-nil avg_tokens_per_second values")
	}
	approxEqualFloat64(t, *bucket1.AvgTokensPerSecond, 15)
	approxEqualFloat64(t, *bucket2.AvgTokensPerSecond, 150)
}

func TestProviderLatencyHistogram_ProviderPartitioning(t *testing.T) {
	store := newTestSQLiteStore(t)
	base := time.Date(2026, 3, 23, 16, 20, 15, 0, time.UTC)

	mustCreateLogs(t, store, []Log{
		{ID: "h-pa-1", Timestamp: base, Object: "chat_completion", Provider: "a", Model: "gpt", Status: "success", Latency: floatPtr(100), TokensPerSecond: floatPtr(10)},
		{ID: "h-pa-2", Timestamp: base.Add(1 * time.Second), Object: "chat_completion", Provider: "a", Model: "gpt", Status: "success", Latency: floatPtr(120), TokensPerSecond: floatPtr(20)},
		{ID: "h-pb-1", Timestamp: base.Add(2 * time.Second), Object: "chat_completion", Provider: "b", Model: "gpt", Status: "success", Latency: floatPtr(200), TokensPerSecond: floatPtr(100)},
		{ID: "h-pb-2", Timestamp: base.Add(3 * time.Second), Object: "chat_completion", Provider: "b", Model: "gpt", Status: "success", Latency: floatPtr(220), TokensPerSecond: floatPtr(200)},
	})

	start := base.Add(-1 * time.Second)
	end := base.Add(10 * time.Second)
	res, err := store.GetProviderLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetProviderLatencyHistogram() error = %v", err)
	}

	bucketTS := (base.Unix() / 60) * 60
	bucket, ok := findProviderLatencyBucketByUnix(bucketTS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d", bucketTS)
	}

	statsA, ok := bucket.ByProvider["a"]
	if !ok {
		t.Fatalf("expected provider a stats")
	}
	statsB, ok := bucket.ByProvider["b"]
	if !ok {
		t.Fatalf("expected provider b stats")
	}

	if statsA.P90TokensPerSecond == nil || statsB.P90TokensPerSecond == nil {
		t.Fatalf("expected provider p90_tokens_per_second values")
	}
	approxEqualFloat64(t, *statsA.P90TokensPerSecond, 20)
	approxEqualFloat64(t, *statsB.P90TokensPerSecond, 200)
}

func TestLatencyHistogram_DuplicateValuesPercentileCorrect(t *testing.T) {
	store := newTestSQLiteStore(t)
	base := time.Date(2026, 3, 23, 17, 30, 10, 0, time.UTC)

	mustCreateLogs(t, store, []Log{
		{ID: "h-dup-1", Timestamp: base, Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(100), TokensPerSecond: floatPtr(10)},
		{ID: "h-dup-2", Timestamp: base.Add(1 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(110), TokensPerSecond: floatPtr(10)},
		{ID: "h-dup-3", Timestamp: base.Add(2 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(120), TokensPerSecond: floatPtr(10)},
		{ID: "h-dup-4", Timestamp: base.Add(3 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(130), TokensPerSecond: floatPtr(20)},
	})

	start := base.Add(-1 * time.Second)
	end := base.Add(10 * time.Second)
	res, err := store.GetLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetLatencyHistogram() error = %v", err)
	}

	bucketTS := (base.Unix() / 60) * 60
	bucket, ok := findLatencyBucketByUnix(bucketTS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d to exist", bucketTS)
	}
	if bucket.P90TokensPerSecond == nil {
		t.Fatalf("expected p90_tokens_per_second to be non-nil")
	}
	// Sorted TPS values = [10, 10, 10, 20], N=4, CEIL(0.9*4)=4 => 20
	approxEqualFloat64(t, *bucket.P90TokensPerSecond, 20)
}

func TestLatencyHistogram_NullsExcludedBeforeTPSRanking(t *testing.T) {
	store := newTestSQLiteStore(t)
	base := time.Date(2026, 3, 23, 18, 5, 0, 0, time.UTC)

	mustCreateLogs(t, store, []Log{
		{ID: "h-nrank-1", Timestamp: base, Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(100)},
		{ID: "h-nrank-2", Timestamp: base.Add(1 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(110)},
		{ID: "h-nrank-3", Timestamp: base.Add(2 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(120), TokensPerSecond: floatPtr(10)},
		{ID: "h-nrank-4", Timestamp: base.Add(3 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(130), TokensPerSecond: floatPtr(20)},
		{ID: "h-nrank-5", Timestamp: base.Add(4 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(140), TokensPerSecond: floatPtr(30)},
	})

	start := base.Add(-1 * time.Second)
	end := base.Add(10 * time.Second)
	res, err := store.GetLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetLatencyHistogram() error = %v", err)
	}

	bucketTS := (base.Unix() / 60) * 60
	bucket, ok := findLatencyBucketByUnix(bucketTS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d to exist", bucketTS)
	}
	if bucket.P90TokensPerSecond == nil {
		t.Fatalf("expected p90_tokens_per_second to be non-nil")
	}
	// Non-null TPS set = [10, 20, 30], N=3, CEIL(0.9*3)=3 => 30.
	approxEqualFloat64(t, *bucket.P90TokensPerSecond, 30)
}

func TestLatencyHistogram_AllNullPlusOneValuePercentile(t *testing.T) {
	store := newTestSQLiteStore(t)
	base := time.Date(2026, 3, 23, 18, 40, 0, 0, time.UTC)

	mustCreateLogs(t, store, []Log{
		{ID: "h-n1-1", Timestamp: base, Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(100)},
		{ID: "h-n1-2", Timestamp: base.Add(1 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(110)},
		{ID: "h-n1-3", Timestamp: base.Add(2 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt", Status: "success", Latency: floatPtr(120), TokensPerSecond: floatPtr(50)},
	})

	start := base.Add(-1 * time.Second)
	end := base.Add(10 * time.Second)
	res, err := store.GetLatencyHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 60)
	if err != nil {
		t.Fatalf("GetLatencyHistogram() error = %v", err)
	}

	bucketTS := (base.Unix() / 60) * 60
	bucket, ok := findLatencyBucketByUnix(bucketTS, res.Buckets)
	if !ok {
		t.Fatalf("expected bucket %d to exist", bucketTS)
	}
	if bucket.P90TokensPerSecond == nil {
		t.Fatalf("expected p90_tokens_per_second to be non-nil")
	}
	// Non-null TPS set = [50], N=1, CEIL(0.9*1)=1 => 50
	approxEqualFloat64(t, *bucket.P90TokensPerSecond, 50)
}
