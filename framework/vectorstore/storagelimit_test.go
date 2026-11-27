package vectorstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storageLimitTestNow is on the hour, so the hourly cutoff lands on the vectors.
var storageLimitTestNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// storageLimitTestIndexes stores two indexes in one Chromem store: "warp", four wide,
// with a vector every hour from 1 to 10 hours ago, and "capture", eight wide
// and left for the limit to measure, every other hour from 2 to 10. The warp
// namespace also holds an old vector that is not Warp's.
func storageLimitTestIndexes(t *testing.T) (VectorStore, []StorageLimitIndex) {
	t.Helper()
	setup := NewChromemTestSetup(t)
	t.Cleanup(func() { setup.Cleanup(t) })
	store, ctx := setup.Store, context.Background()
	properties := map[string]VectorStoreProperties{
		"warp_log":  {DataType: VectorStorePropertyTypeBoolean},
		"kind":      {DataType: VectorStorePropertyTypeString},
		"timestamp": {DataType: VectorStorePropertyTypeInteger},
	}
	require.NoError(t, store.CreateNamespace(ctx, "warp", 4, properties))
	require.NoError(t, store.CreateNamespace(ctx, "capture", 8, properties))
	at := func(hours int) int64 { return storageLimitTestNow.Add(-time.Duration(hours) * time.Hour).Unix() }
	for hour := 1; hour <= 10; hour++ {
		require.NoError(t, store.Add(ctx, "warp", fmt.Sprintf("warp-%02d", hour), []float32{1, 0, 0, 0}, map[string]interface{}{"warp_log": true, "timestamp": at(hour)}))
		if hour%2 == 0 {
			require.NoError(t, store.Add(ctx, "capture", fmt.Sprintf("capture-%02d", hour), []float32{1, 0, 0, 0, 0, 0, 0, 0}, map[string]interface{}{"kind": "request", "timestamp": at(hour)}))
		}
	}
	require.NoError(t, store.Add(ctx, "warp", "not-warps", []float32{0, 1, 0, 0}, map[string]interface{}{"timestamp": at(100)}))
	return store, []StorageLimitIndex{
		{Name: "warp", Store: store, Namespace: "warp", Dimension: 4, TimestampField: "timestamp",
			Filter: []Query{{Field: "warp_log", Operator: QueryOperatorEqual, Value: true}}},
		{Name: "capture", Store: store, Namespace: "capture", TimestampField: "timestamp",
			Filter: []Query{{Field: "kind", Operator: QueryOperatorEqual, Value: "request"}}},
	}
}

func storageLimitTestIDs(t *testing.T, store VectorStore, namespace string) []string {
	t.Helper()
	results, _, err := store.GetAll(context.Background(), namespace, nil, nil, nil, 100)
	require.NoError(t, err)
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return ids
}

// The estimate is 10 warp vectors of 4*4+1024 bytes and 5 captures of
// 8*4+1024, the capture width read off a stored vector: 15,680 bytes, which
// is exactly 80% of 19,600, so nothing is trimmed yet.
func TestEnforceStorageLimitLeavesIndexesUnderTheHighWatermark(t *testing.T) {
	store, indexes := storageLimitTestIndexes(t)
	result, err := EnforceStorageLimit(context.Background(), 19600, indexes, storageLimitTestNow)
	require.NoError(t, err)
	assert.EqualValues(t, 15680, result.EstimatedBytes)
	assert.Zero(t, result.Deleted)
	assert.True(t, result.Cutoff.IsZero())
	assert.Len(t, storageLimitTestIDs(t, store, "warp"), 11)
}

// Past 80% of the limit, one cutoff applies to every index: 65% of 10,000
// bytes keeps the last four hours (4 warp vectors and 2 captures, 6,272
// bytes; five hours would be 7,312), and everything older goes from both,
// though 15,680 bytes was still under the limit itself. What is not an
// index's own stays.
func TestEnforceStorageLimitDeletesTheOldestAcrossIndexes(t *testing.T) {
	store, indexes := storageLimitTestIndexes(t)
	result, err := EnforceStorageLimit(context.Background(), 10000, indexes, storageLimitTestNow)
	require.NoError(t, err)
	assert.Equal(t, storageLimitTestNow.Add(-5*time.Hour+time.Second), result.Cutoff, "just after the newest vector that goes")
	assert.Equal(t, 9, result.Deleted)
	assert.ElementsMatch(t, []string{"warp-01", "warp-02", "warp-03", "warp-04", "not-warps"}, storageLimitTestIDs(t, store, "warp"))
	assert.ElementsMatch(t, []string{"capture-02", "capture-04"}, storageLimitTestIDs(t, store, "capture"))

	again, err := EnforceStorageLimit(context.Background(), 10000, indexes, storageLimitTestNow)
	require.NoError(t, err)
	assert.Zero(t, again.Deleted, "once under the high watermark, the next pass leaves it")
}

// failingCounts is a store that is down: every count fails.
type failingCounts struct{ VectorStore }

func (failingCounts) CountVectors(context.Context, string, []Query) (int64, error) {
	return 0, errors.New("store is down")
}

// An index that cannot be counted is left out and reported, and the others are
// still trimmed: leaving it out makes the estimate smaller, so they lose less
// than the storage limit would ask, never more.
func TestEnforceStorageLimitSkipsAnIndexItCannotCount(t *testing.T) {
	store, indexes := storageLimitTestIndexes(t)
	indexes[1].Store = failingCounts{store}
	result, err := EnforceStorageLimit(context.Background(), 6000, indexes, storageLimitTestNow)
	require.NoError(t, err)
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0], "capture")
	assert.Contains(t, result.Skipped[0], "store is down")
	assert.EqualValues(t, 10400, result.EstimatedBytes, "the warp vectors alone")
	assert.Len(t, storageLimitTestIDs(t, store, "capture"), 5, "the uncounted index is not touched")
	// 65% of 6,000 keeps three warp vectors.
	assert.ElementsMatch(t, []string{"warp-01", "warp-02", "warp-03", "not-warps"}, storageLimitTestIDs(t, store, "warp"))
}

// deletingOnFirstCount deletes vectors once the pass has taken its first
// count, as retention or another node's pass can between two counts.
type deletingOnFirstCount struct {
	VectorStore
	once   sync.Once
	delete func()
}

func (s *deletingOnFirstCount) CountVectors(ctx context.Context, namespace string, queries []Query) (int64, error) {
	count, err := s.VectorStore.CountVectors(ctx, namespace, queries)
	s.once.Do(s.delete)
	return count, err
}

// Vectors deleted after the first count are not counted as kept, so the cutoff
// keeps what fits rather than deleting more to make room for vectors already
// gone: of ten hourly vectors, the three oldest go elsewhere mid-pass, and a
// limit whose 65% fits five still keeps five.
func TestEnforceStorageLimitCountsWhatIsKeptAfterOtherDeletes(t *testing.T) {
	store, indexes := storageLimitTestIndexes(t)
	warp := indexes[0]
	shrinking := &deletingOnFirstCount{VectorStore: store, delete: func() {
		for _, id := range []string{"warp-08", "warp-09", "warp-10"} {
			require.NoError(t, store.Delete(context.Background(), "warp", id))
		}
	}}
	warp.Store = shrinking
	perVector := float64(4*4 + StorageLimitBytesPerVectorOverhead)
	result, err := EnforceStorageLimit(context.Background(), int64(5.5*perVector/StorageLimitLowWatermark), []StorageLimitIndex{warp}, storageLimitTestNow)
	require.NoError(t, err)
	assert.Equal(t, storageLimitTestNow.Add(-6*time.Hour+time.Second), result.Cutoff, "just after the newest vector that goes")
	assert.Equal(t, 2, result.Deleted)
	assert.ElementsMatch(t, []string{"warp-01", "warp-02", "warp-03", "warp-04", "warp-05", "not-warps"}, storageLimitTestIDs(t, store, "warp"))
}

// failingDeletes reports every delete as failed, with no error for the call,
// as a store does per entry.
type failingDeletes struct{ VectorStore }

func (s failingDeletes) DeleteAll(_ context.Context, _ string, _ []Query) ([]DeleteResult, error) {
	return []DeleteResult{{ID: "a", Status: DeleteStatusError, Error: "refused"}, {ID: "b", Status: DeleteStatusError, Error: "refused"}}, nil
}

// Only confirmed deletes count, and failed ones are an error even when the
// call itself returned none; the other indexes are still trimmed.
func TestEnforceStorageLimitReportsDeletesThatFailed(t *testing.T) {
	store, indexes := storageLimitTestIndexes(t)
	indexes[1].Store = failingDeletes{store}
	result, err := EnforceStorageLimit(context.Background(), 10000, indexes, storageLimitTestNow)
	require.ErrorContains(t, err, "trim capture: 2 of 2 deletes failed: refused")
	assert.Equal(t, 6, result.Deleted, "the six warp deletes, not the two that failed")
	assert.Len(t, storageLimitTestIDs(t, store, "warp"), 5)
}

// No limit, or nothing under it, is no pass at all.
func TestEnforceStorageLimitWithoutALimit(t *testing.T) {
	store, indexes := storageLimitTestIndexes(t)
	result, err := EnforceStorageLimit(context.Background(), 0, indexes, storageLimitTestNow)
	require.NoError(t, err)
	assert.Zero(t, result)
	assert.Len(t, storageLimitTestIDs(t, store, "warp"), 11)
}

// The storage limit on every backend: ten hourly vectors in one index, a limit whose
// 65% fits five, and the five newest are what is left. It leans on each
// store's filtered count agreeing with its filtered delete.
func TestEnforceStorageLimitOnEveryBackend(t *testing.T) {
	for _, backend := range vectorStoreTestBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, ctx, shared := backend.setup(t)
			namespace := shared + "StorageLimit"
			require.NoError(t, store.CreateNamespace(ctx, namespace, backend.dimension, map[string]VectorStoreProperties{
				"warp_log":  {DataType: VectorStorePropertyTypeBoolean},
				"timestamp": {DataType: VectorStorePropertyTypeInteger},
			}))
			t.Cleanup(func() { _ = store.DeleteNamespace(ctx, namespace) })
			ids := map[int]string{}
			for hour := 1; hour <= 10; hour++ {
				ids[hour] = generateUUID()
				require.NoError(t, store.Add(ctx, namespace, ids[hour], generateTestEmbedding(backend.dimension), map[string]interface{}{
					"warp_log": true, "timestamp": storageLimitTestNow.Add(-time.Duration(hour) * time.Hour).Unix(),
				}))
			}
			index := StorageLimitIndex{Name: backend.name, Store: store, Namespace: namespace, Dimension: backend.dimension, TimestampField: "timestamp",
				Filter: []Query{{Field: "warp_log", Operator: QueryOperatorEqual, Value: true}}}
			require.Eventually(t, func() bool {
				n, err := store.CountVectors(ctx, namespace, index.Filter)
				return err == nil && n == 10
			}, 10*time.Second, 200*time.Millisecond)

			perVector := float64(backend.dimension*4 + StorageLimitBytesPerVectorOverhead)
			result, err := EnforceStorageLimit(ctx, int64(5.5*perVector/StorageLimitLowWatermark), []StorageLimitIndex{index}, storageLimitTestNow)
			require.NoError(t, err)
			assert.Equal(t, storageLimitTestNow.Add(-6*time.Hour+time.Second), result.Cutoff, "just after the newest vector that goes")
			require.Eventually(t, func() bool {
				n, err := store.CountVectors(ctx, namespace, index.Filter)
				return err == nil && n == 5
			}, 10*time.Second, 200*time.Millisecond, "the five newest are left")
			for hour := 1; hour <= 5; hour++ {
				_, err := store.GetChunk(ctx, namespace, ids[hour])
				assert.NoError(t, err, "hour %d is kept", hour)
			}
		})
	}
}

// A persisted Chromem namespace not yet opened with CreateNamespace since a
// restart cannot be enumerated, so a filtered count of it fails, as the
// backend's contract says. The pass skips it rather than failing the pass;
// once its owner opens it, it is counted again.
func TestEnforceStorageLimitSkipsAChromemNamespaceNotOpenedSinceARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	before, err := newChromemStore(ctx, &ChromemConfig{Path: dir}, nil)
	require.NoError(t, err)
	require.NoError(t, before.CreateNamespace(ctx, "warp", 4, nil))
	for hour := 1; hour <= 3; hour++ {
		require.NoError(t, before.Add(ctx, "warp", fmt.Sprintf("warp-%02d", hour), []float32{1, 0, 0, 0}, map[string]interface{}{
			"warp_log": true, "timestamp": storageLimitTestNow.Add(-time.Duration(hour) * time.Hour).Unix(),
		}))
	}

	after, err := newChromemStore(ctx, &ChromemConfig{Path: dir}, nil)
	require.NoError(t, err)
	index := StorageLimitIndex{Name: "warp", Store: after, Namespace: "warp", Dimension: 4, TimestampField: "timestamp",
		Filter: []Query{{Field: "warp_log", Operator: QueryOperatorEqual, Value: true}}}
	result, err := EnforceStorageLimit(ctx, 100, []StorageLimitIndex{index}, storageLimitTestNow)
	require.NoError(t, err)
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0], "CreateNamespace")
	assert.Zero(t, result.Deleted)

	require.NoError(t, after.CreateNamespace(ctx, "warp", 4, nil))
	result, err = EnforceStorageLimit(ctx, 100, []StorageLimitIndex{index}, storageLimitTestNow)
	require.NoError(t, err)
	assert.Empty(t, result.Skipped)
	assert.Equal(t, 3, result.Deleted)
}

// Vectors written within one hour are not all kept or all deleted together:
// the cutoff falls between them. Ten written a minute apart, under a limit
// whose 65% fits five, keep the five newest.
func TestEnforceStorageLimitSplitsVectorsWithinOneHour(t *testing.T) {
	setup := NewChromemTestSetup(t)
	t.Cleanup(func() { setup.Cleanup(t) })
	store, ctx := setup.Store, context.Background()
	require.NoError(t, store.CreateNamespace(ctx, "warp", 4, map[string]VectorStoreProperties{
		"warp_log":  {DataType: VectorStorePropertyTypeBoolean},
		"timestamp": {DataType: VectorStorePropertyTypeInteger},
	}))
	for minute := 1; minute <= 10; minute++ {
		require.NoError(t, store.Add(ctx, "warp", fmt.Sprintf("warp-%02d", minute), []float32{1, 0, 0, 0}, map[string]interface{}{
			"warp_log": true, "timestamp": storageLimitTestNow.Add(-time.Duration(minute) * time.Minute).Unix(),
		}))
	}
	index := StorageLimitIndex{Name: "warp", Store: store, Namespace: "warp", Dimension: 4, TimestampField: "timestamp",
		Filter: []Query{{Field: "warp_log", Operator: QueryOperatorEqual, Value: true}}}
	perVector := float64(4*4 + StorageLimitBytesPerVectorOverhead)
	result, err := EnforceStorageLimit(ctx, int64(5.5*perVector/StorageLimitLowWatermark), []StorageLimitIndex{index}, storageLimitTestNow)
	require.NoError(t, err)
	assert.Equal(t, 5, result.Deleted)
	assert.ElementsMatch(t, []string{"warp-01", "warp-02", "warp-03", "warp-04", "warp-05"}, storageLimitTestIDs(t, store, "warp"))
}
