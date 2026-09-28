package vectorstore

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Paging reads carry vectors only on request: a listing that wants ids must
// not pay for thousands of floats per row, and a clustering job that wants
// the vectors must not have to fetch each entry a second time.
func TestIncludeVectorsIsOptIn(t *testing.T) {
	require.False(t, IncludeVectorsRequested(context.Background()))
	require.False(t, IncludeVectorsRequested(nil))
	require.True(t, IncludeVectorsRequested(WithIncludeVectors(context.Background())))
	require.True(t, IncludeVectorsRequested(WithIncludeVectors(nil)))
}

// GraphQL decodes numbers as float64 inside []interface{}; anything else in
// the list means the backend did not return a vector, and nil is the honest
// answer rather than a partial one.
func TestVectorFromAdditional(t *testing.T) {
	require.Equal(t, []float32{0.5, -1, 2}, VectorFromAdditional([]interface{}{0.5, float64(-1), 2}))
	require.Nil(t, VectorFromAdditional(nil))
	require.Nil(t, VectorFromAdditional("not a vector"))
	require.Nil(t, VectorFromAdditional([]interface{}{}))
	require.Nil(t, VectorFromAdditional([]interface{}{1.0, "x"}))
}

// includeVectorsTestProperties is the schema the paging-read contract writes.
var includeVectorsTestProperties = map[string]VectorStoreProperties{
	"marker": {DataType: VectorStorePropertyTypeString, Description: "include-vectors test marker"},
}

// TestVectorStoreGetAllReturnsVectorsOnRequest pins, for every backend, what a
// caller that reads a namespace back out depends on: asked for vectors, a
// paging read returns each entry once, across as many pages as it takes, with
// the vector that was stored; not asked, it returns none.
//
// Topic clustering is that caller. It worked on the one backend that honoured
// the request and reported an empty window on the rest, because an entry that
// comes back without its vector cannot be clustered.
func TestVectorStoreGetAllReturnsVectorsOnRequest(t *testing.T) {
	const entries, pageSize = 25, 10
	for _, backend := range vectorStoreTestBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, ctx, namespace := backend.setup(t)
			require.NoError(t, store.CreateNamespace(ctx, namespace, backend.dimension, includeVectorsTestProperties))

			stored := map[string][]float32{}
			for index := range entries {
				id := generateUUID()
				// Unit length and unlike any other entry's, so a vector handed
				// back under the wrong id cannot pass for the right one.
				vector := make([]float32, backend.dimension)
				vector[index] = 0.8
				vector[backend.dimension-1-index] = 0.6
				stored[id] = vector
				require.NoError(t, store.Add(ctx, namespace, id, vector, map[string]interface{}{"marker": fmt.Sprintf("entry-%d", index)}))
			}
			t.Cleanup(func() {
				for id := range stored {
					_ = store.Delete(ctx, namespace, id)
				}
			})
			// Several backends index asynchronously.
			time.Sleep(time.Second)

			page := func(ctx context.Context) map[string][]SearchResult {
				seen := map[string][]SearchResult{}
				var cursor *string
				for pages := 0; ; pages++ {
					require.Less(t, pages, 1000, "paging did not end")
					results, next, err := store.GetAll(ctx, namespace, nil, []string{"marker"}, cursor, pageSize)
					require.NoError(t, err)
					for _, result := range results {
						if _, ours := stored[result.ID]; ours {
							seen[result.ID] = append(seen[result.ID], result)
						}
					}
					if next == nil || len(results) == 0 {
						return seen
					}
					cursor = next
				}
			}

			withVectors := page(WithIncludeVectors(ctx))
			require.Len(t, withVectors, entries, "every entry is reached, past the first page")
			for id, results := range withVectors {
				require.Len(t, results, 1, "entry %s is returned once, not once per page boundary", id)
				require.Len(t, results[0].Vector, backend.dimension)
				require.InDelta(t, 1, cosineOf(stored[id], results[0].Vector), 1e-4, "entry %s comes back with its own vector", id)
				require.NotEmpty(t, results[0].Properties["marker"], "and with the properties that were asked for")
			}

			for id, results := range page(ctx) {
				require.Nil(t, results[0].Vector, "entry %s carries no vector unless asked", id)
			}
		})
	}
}

func cosineOf(a, b []float32) float64 {
	var dot, normA, normB float64
	for index := range a {
		dot += float64(a[index]) * float64(b[index])
		normA += float64(a[index]) * float64(a[index])
		normB += float64(b[index]) * float64(b[index])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / math.Sqrt(normA*normB)
}

// filteredDeleteTestProperties is the schema the filtered-delete contract
// writes. Declared, because Redis can only filter on a field it indexed.
var filteredDeleteTestProperties = map[string]VectorStoreProperties{
	"marker": {DataType: VectorStorePropertyTypeString, Description: "filtered delete test marker"},
	"batch":  {DataType: VectorStorePropertyTypeString, Description: "filtered delete test batch"},
	"rank":   {DataType: VectorStorePropertyTypeInteger, Description: "filtered delete test rank"},
}

// TestVectorStoreDeleteAllHonoursItsFilter pins, for every backend, that a
// filtered delete removes what the filter selects and nothing else, for each
// comparison a caller uses to tell its own entries from older ones.
//
// Replacing a set of entries depends on it: the new ones are written, then
// the rest are deleted by filter. Pinecone's serverless indexes refuse a
// filtered delete outright, which left every previous set in place.
func TestVectorStoreDeleteAllHonoursItsFilter(t *testing.T) {
	for _, backend := range vectorStoreTestBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, ctx, _ := backend.setup(t)
			// A namespace of its own: the shared test namespaces already exist,
			// and a backend only takes a schema when it creates one. A field it
			// was never told about is typed by guesswork or not indexed at all.
			namespace := fmt.Sprintf("FilteredDelete%d", time.Now().UnixNano())
			require.NoError(t, store.CreateNamespace(ctx, namespace, backend.dimension, filteredDeleteTestProperties))
			t.Cleanup(func() { _ = store.DeleteNamespace(context.Background(), namespace) })
			marker := "filtered-delete-" + generateUUID()
			mine := Query{Field: "marker", Operator: QueryOperatorEqual, Value: marker}

			ids := map[string]string{}
			write := func(name, batch string, rank int64) {
				id := generateUUID()
				ids[name] = id
				require.NoError(t, store.Add(ctx, namespace, id, generateTestEmbedding(backend.dimension), map[string]interface{}{"marker": marker, "batch": batch, "rank": rank}))
			}
			remaining := func() []string {
				// Several backends apply a delete asynchronously.
				time.Sleep(time.Second)
				names := []string{}
				for name, id := range ids {
					result, err := store.GetChunk(ctx, namespace, id)
					if err == nil && result.ID != "" && len(result.Properties) > 0 {
						names = append(names, name)
					}
				}
				return names
			}
			t.Cleanup(func() {
				for _, id := range ids {
					_ = store.Delete(ctx, namespace, id)
				}
			})

			write("old-1", "old", 10)
			write("old-2", "old", 20)
			write("new-1", "new", 30)
			write("new-2", "new", 40)
			require.ElementsMatch(t, []string{"old-1", "old-2", "new-1", "new-2"}, remaining())

			_, err := store.DeleteAll(ctx, namespace, []Query{mine, {Field: "rank", Operator: QueryOperatorLessThan, Value: int64(20)}})
			require.NoError(t, err)
			require.ElementsMatch(t, []string{"old-2", "new-1", "new-2"}, remaining(), "less than")

			_, err = store.DeleteAll(ctx, namespace, []Query{mine, {Field: "batch", Operator: QueryOperatorNotEqual, Value: "new"}})
			require.NoError(t, err)
			require.ElementsMatch(t, []string{"new-1", "new-2"}, remaining(), "not equal")

			_, err = store.DeleteAll(ctx, namespace, []Query{mine, {Field: "batch", Operator: QueryOperatorEqual, Value: "new"}})
			require.NoError(t, err)
			require.Empty(t, remaining(), "equal")
		})
	}
}

// rangeReadTestProperties is the schema the filtered-read contract writes.
var rangeReadTestProperties = map[string]VectorStoreProperties{
	"marker": {DataType: VectorStorePropertyTypeString, Description: "range read test marker"},
	"at":     {DataType: VectorStorePropertyTypeInteger, Description: "range read test time"},
	"kept":   {DataType: VectorStorePropertyTypeBoolean, Description: "range read test flag"},
}

// TestVectorStoreGetAllPagesAFilteredReadWithVectors pins, for every backend,
// the read a caller makes to take one slice of a namespace out: a range on a
// number and a flag, with vectors, paged. It must return exactly the entries
// the filter selects, each once, however many pages that takes.
//
// Reading a namespace whole and discarding what falls outside the range costs
// the size of the namespace, not of the range, and stops at whatever ceiling
// the store puts on one read. Slices are how a large window is read at all -
// and the backends disagreed on whether a filter and a cursor could be
// combined in the first place.
func TestVectorStoreGetAllPagesAFilteredReadWithVectors(t *testing.T) {
	const entries, pageSize = 60, 10
	for _, backend := range vectorStoreTestBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, ctx, _ := backend.setup(t)
			namespace := fmt.Sprintf("RangeRead%d", time.Now().UnixNano())
			require.NoError(t, store.CreateNamespace(ctx, namespace, backend.dimension, rangeReadTestProperties))
			t.Cleanup(func() { _ = store.DeleteNamespace(context.Background(), namespace) })

			const base = int64(1788000000)
			stored := map[string][]float32{}
			at := map[string]int64{}
			kept := map[string]bool{}
			for index := range entries {
				id := generateUUID()
				vector := make([]float32, backend.dimension)
				vector[index] = 0.8
				vector[backend.dimension-1-index] = 0.6
				stored[id], at[id], kept[id] = vector, base+int64(index), index%5 != 0
				require.NoError(t, store.Add(ctx, namespace, id, vector, map[string]interface{}{"marker": "entry", "at": at[id], "kept": kept[id]}))
			}
			time.Sleep(time.Second)

			// Half-open, as slices laid end to end have to be.
			from, to := base+10, base+45
			queries := []Query{
				{Field: "at", Operator: QueryOperatorGreaterThanOrEqual, Value: from},
				{Field: "at", Operator: QueryOperatorLessThan, Value: to},
				{Field: "kept", Operator: QueryOperatorEqual, Value: true},
			}
			wanted := map[string]struct{}{}
			for id := range stored {
				if at[id] >= from && at[id] < to && kept[id] {
					wanted[id] = struct{}{}
				}
			}
			require.Len(t, wanted, 28)

			seen := map[string]int{}
			var cursor *string
			for pages := 0; ; pages++ {
				require.Less(t, pages, 100, "paging did not end")
				results, next, err := store.GetAll(WithIncludeVectors(ctx), namespace, queries, []string{"at", "kept"}, cursor, pageSize)
				require.NoError(t, err)
				for _, result := range results {
					seen[result.ID]++
					require.Contains(t, wanted, result.ID, "an entry outside the filter came back")
					require.InDelta(t, 1, cosineOf(stored[result.ID], result.Vector), 1e-4, "entry %s comes back with its own vector", result.ID)
				}
				if next == nil || len(results) == 0 {
					break
				}
				cursor = next
			}
			require.Len(t, seen, len(wanted), "every entry the filter selects is reached")
			for id, times := range seen {
				require.Equal(t, 1, times, "entry %s is returned once", id)
			}
		})
	}
}
