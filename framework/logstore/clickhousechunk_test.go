package logstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForEachRMWChunk(t *testing.T) {
	t.Run("splits on the chunk boundary", func(t *testing.T) {
		entries := make([]int, chRMWBatchChunk*2+7)
		for i := range entries {
			entries[i] = i
		}

		var sizes []int
		var seen []int
		require.NoError(t, forEachRMWChunk(entries, func(chunk []int) error {
			sizes = append(sizes, len(chunk))
			seen = append(seen, chunk...)
			return nil
		}))

		assert.Equal(t, []int{chRMWBatchChunk, chRMWBatchChunk, 7}, sizes)
		assert.Equal(t, entries, seen, "every entry must be visited exactly once, in order")
	})

	t.Run("single short chunk", func(t *testing.T) {
		calls := 0
		require.NoError(t, forEachRMWChunk([]int{1, 2, 3}, func(chunk []int) error {
			calls++
			assert.Len(t, chunk, 3)
			return nil
		}))
		assert.Equal(t, 1, calls)
	})

	t.Run("empty input never calls fn", func(t *testing.T) {
		called := false
		require.NoError(t, forEachRMWChunk([]int{}, func([]int) error {
			called = true
			return nil
		}))
		assert.False(t, called)
	})

	t.Run("stops at the first error", func(t *testing.T) {
		entries := make([]int, chRMWBatchChunk*3)
		calls := 0
		err := forEachRMWChunk(entries, func([]int) error {
			calls++
			if calls == 2 {
				return fmt.Errorf("boom")
			}
			return nil
		})
		require.Error(t, err)
		assert.Equal(t, 2, calls, "later chunks must not run after a failure")
	})
}

// chCountIDsNoFinal counts the physical rows carrying any of ids, with FINAL
// disabled.
//
// Every store connection sets final=1 (see clickhouse.go), which collapses rows
// sharing the (timestamp, id) sorting key at read time. A duplicate INSERT of an
// id at its original timestamp is therefore invisible to an ordinary count or to
// FindByID: both report one row whether or not the existence check suppressed the
// second write, which would make a cardinality assertion vacuous. Overriding the
// setting per query exposes the physical rows, so these tests fail on a duplicate
// write instead of letting ReplacingMergeTree hide it.
func chCountIDsNoFinal(t *testing.T, store *ClickHouseLogStore, table string, ids []string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, store.db.Raw("SELECT count() FROM "+table+" WHERE id IN ? SETTINGS final = 0", ids).Scan(&n).Error)
	return n
}

// TestClickHouseBatchCreateSpansChunks guards the chunked lock path: a batch larger
// than chRMWBatchChunk is now written as several lock/filter/insert passes, so every
// row must still land exactly once and dedup must still hold across chunk boundaries.
func TestClickHouseBatchCreateSpansChunks(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()

	const total = chRMWBatchChunk*2 + 13
	ts := time.Now().UTC().Truncate(time.Millisecond)
	entries := make([]*Log, 0, total)
	ids := make([]string, 0, total)
	for i := range total {
		id := fmt.Sprintf("ch-chunk-%04d", i)
		entries = append(entries, chTestLog(id, ts))
		ids = append(ids, id)
	}

	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))

	assert.EqualValues(t, total, chCountIDsNoFinal(t, store, "logs", ids),
		"every row must land exactly once across the chunk seams")
	for _, i := range []int{0, chRMWBatchChunk - 1, chRMWBatchChunk, total - 1} {
		id := fmt.Sprintf("ch-chunk-%04d", i)
		got, err := store.FindByID(ctx, id)
		require.NoErrorf(t, err, "log %s should have been inserted", id)
		require.NotNil(t, got)
	}

	// Re-inserting the same batch must be a no-op, including across chunk seams.
	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))
	assert.EqualValues(t, total, chCountIDsNoFinal(t, store, "logs", ids),
		"reinserting the same batch must not add physical rows")
	for _, i := range []int{0, chRMWBatchChunk, total - 1} {
		id := fmt.Sprintf("ch-chunk-%04d", i)
		_, err := store.FindByID(ctx, id)
		require.NoErrorf(t, err, "log %s should still resolve to a single row", id)
	}
}

// TestClickHouseBatchCreateDedupsIDsAcrossChunks pins the batch-wide seen-id set.
// chFilterMissing bounds its existence probe to the chunk's [min, max] timestamp
// window, so a repeat id landing in a later chunk with a different timestamp would
// miss the row the first chunk inserted and write a second row - (timestamp, id) is
// the sorting key, so ReplacingMergeTree would not collapse them either. The SQL
// stores dedup on id alone via ON CONFLICT DO NOTHING; this must match.
func TestClickHouseBatchCreateDedupsIDsAcrossChunks(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()

	const dupID = "ch-chunk-dup"
	ts := time.Now().UTC().Truncate(time.Millisecond)
	entries := make([]*Log, 0, chRMWBatchChunk+2)
	// First chunk carries the id at ts; the second carries it an hour later, well
	// outside the first chunk's window.
	entries = append(entries, chTestLog(dupID, ts))
	for i := 1; i < chRMWBatchChunk; i++ {
		entries = append(entries, chTestLog(fmt.Sprintf("ch-dup-filler-%04d", i), ts))
	}
	entries = append(entries, chTestLog(dupID, ts.Add(time.Hour)))
	entries = append(entries, chTestLog("ch-dup-tail", ts.Add(time.Hour)))
	require.Len(t, entries, chRMWBatchChunk+2)

	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))

	assert.EqualValues(t, 1, chCountIDsNoFinal(t, store, "logs", []string{dupID}),
		"the repeat id must be written once, not once per chunk")

	// The first occurrence wins, matching ON CONFLICT DO NOTHING.
	got, err := store.FindByID(ctx, dupID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Timestamp.Equal(ts), "first occurrence must win, got %s want %s", got.Timestamp, ts)

	// Entries after the duplicate in the same chunk must still be written.
	tail, err := store.FindByID(ctx, "ch-dup-tail")
	require.NoError(t, err)
	require.NotNil(t, tail)
}
