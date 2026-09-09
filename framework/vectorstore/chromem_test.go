package vectorstore

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ChromemTestTimeout   = 30 * time.Second
	ChromemTestNamespace = "chromem_test_collection"
	ChromemTestDimension = 384
)

// ChromemTestSetup provides utilities for chromem store testing.
// Chromem is embedded, so unlike the other backends these tests need no
// external server and no testing.Short() gating.
type ChromemTestSetup struct {
	Store  *ChromemStore
	Logger schemas.Logger
	Config ChromemConfig
	ctx    context.Context
	cancel context.CancelFunc
}

// NewChromemTestSetup creates a new memory-only test setup for chromem tests.
func NewChromemTestSetup(t *testing.T) *ChromemTestSetup {
	return newChromemTestSetupWithConfig(t, ChromemConfig{})
}

func newChromemTestSetupWithConfig(t *testing.T, config ChromemConfig) *ChromemTestSetup {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx, cancel := context.WithTimeout(context.Background(), ChromemTestTimeout)

	store, err := newChromemStore(ctx, &config, logger)
	require.NoError(t, err, "Failed to create chromem store")

	setup := &ChromemTestSetup{
		Store:  store,
		Logger: logger,
		Config: config,
		ctx:    ctx,
		cancel: cancel,
	}
	setup.ensureNamespaceExists(t)
	return setup
}

// Cleanup releases test resources.
func (ts *ChromemTestSetup) Cleanup(t *testing.T) {
	defer ts.cancel()
	err := ts.Store.DeleteNamespace(ts.ctx, ChromemTestNamespace)
	assert.NoError(t, err, "Failed to delete test namespace")
	err = ts.Store.Close(ts.ctx, "")
	assert.NoError(t, err, "Failed to close store")
}

func (ts *ChromemTestSetup) ensureNamespaceExists(t *testing.T) {
	err := ts.Store.CreateNamespace(ts.ctx, ChromemTestNamespace, ChromemTestDimension, map[string]VectorStoreProperties{
		"key":      {DataType: VectorStorePropertyTypeString, Description: "Cache key"},
		"type":     {DataType: VectorStorePropertyTypeString, Description: "Entry type"},
		"size":     {DataType: VectorStorePropertyTypeInteger, Description: "Entry size"},
		"public":   {DataType: VectorStorePropertyTypeBoolean, Description: "Is public"},
		"tags":     {DataType: VectorStorePropertyTypeStringArray, Description: "Tags"},
		"content":  {DataType: VectorStorePropertyTypeString, Description: "Content"},
		"category": {DataType: VectorStorePropertyTypeString, Description: "Category"},
	})
	require.NoError(t, err, "Failed to create test namespace")
}

func TestChromemConfig_Validation(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	tests := []struct {
		name        string
		config      *ChromemConfig
		expectError bool
	}{
		{name: "nil config", config: nil, expectError: true},
		{name: "empty config (memory-only)", config: &ChromemConfig{}, expectError: false},
		{name: "persistent path", config: &ChromemConfig{Path: filepath.Join(t.TempDir(), "chromem")}, expectError: false},
		{name: "compressed persistent path", config: &ChromemConfig{Path: filepath.Join(t.TempDir(), "chromem"), Compress: true}, expectError: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := newChromemStore(ctx, tt.config, logger)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.NoError(t, store.Ping(ctx))
		})
	}

	t.Run("path is an existing file", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "not-a-dir")
		require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o600))
		_, err := newChromemStore(ctx, &ChromemConfig{Path: filePath}, logger)
		assert.Error(t, err)
	})
}

func TestChromemStore_Integration(t *testing.T) {
	ts := NewChromemTestSetup(t)
	defer ts.Cleanup(t)

	id1 := generateUUID()
	id2 := generateUUID()
	embedding1 := generateTestEmbedding(ChromemTestDimension)
	embedding2 := generateSimilarEmbedding(embedding1, 0.9)

	err := ts.Store.Add(ts.ctx, ChromemTestNamespace, id1, embedding1, map[string]interface{}{
		"key":     "entry-1",
		"type":    "chat",
		"size":    int64(128),
		"public":  true,
		"tags":    []string{"alpha", "beta"},
		"content": "hello world",
	})
	require.NoError(t, err)

	err = ts.Store.Add(ts.ctx, ChromemTestNamespace, id2, embedding2, map[string]interface{}{
		"key":    "entry-2",
		"type":   "completion",
		"size":   int64(256),
		"public": false,
	})
	require.NoError(t, err)

	// GetChunk round-trips typed metadata.
	result, err := ts.Store.GetChunk(ts.ctx, ChromemTestNamespace, id1)
	require.NoError(t, err)
	assert.Equal(t, id1, result.ID)
	assert.Equal(t, "entry-1", result.Properties["key"])
	assert.Equal(t, int64(128), result.Properties["size"])
	assert.Equal(t, true, result.Properties["public"])
	assert.Equal(t, []string{"alpha", "beta"}, result.Properties["tags"])

	// GetChunks skips missing IDs.
	results, err := ts.Store.GetChunks(ts.ctx, ChromemTestNamespace, []string{id1, "missing-id", id2})
	require.NoError(t, err)
	assert.Len(t, results, 2)

	// GetAll returns both entries.
	all, cursor, err := ts.Store.GetAll(ts.ctx, ChromemTestNamespace, nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Nil(t, cursor)
	assert.Len(t, all, 2)

	// GetAll with select fields projects properties.
	all, _, err = ts.Store.GetAll(ts.ctx, ChromemTestNamespace, nil, []string{"key"}, nil, 100)
	require.NoError(t, err)
	for _, res := range all {
		assert.Contains(t, res.Properties, "key")
		assert.NotContains(t, res.Properties, "type")
	}

	// Upsert overwrites in place.
	err = ts.Store.Add(ts.ctx, ChromemTestNamespace, id1, embedding1, map[string]interface{}{"key": "entry-1-updated"})
	require.NoError(t, err)
	result, err = ts.Store.GetChunk(ts.ctx, ChromemTestNamespace, id1)
	require.NoError(t, err)
	assert.Equal(t, "entry-1-updated", result.Properties["key"])
}

func TestChromemStore_Filtering(t *testing.T) {
	ts := NewChromemTestSetup(t)
	defer ts.Cleanup(t)

	base := generateTestEmbedding(ChromemTestDimension)
	for i, meta := range []map[string]interface{}{
		{"key": "a", "type": "chat", "size": int64(10), "public": true, "tags": []string{"x", "y"}, "content": "the quick brown fox"},
		{"key": "b", "type": "chat", "size": int64(20), "public": false, "tags": []string{"y", "z"}, "content": "lazy dog"},
		{"key": "c", "type": "completion", "size": int64(30), "public": true, "content": "quick start guide"},
	} {
		require.NoError(t, ts.Store.Add(ts.ctx, ChromemTestNamespace, fmt.Sprintf("doc-%d", i), generateSimilarEmbedding(base, 0.95), meta))
	}

	tests := []struct {
		name     string
		queries  []Query
		expected int
	}{
		{"equal", []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "chat"}}, 2},
		{"not equal", []Query{{Field: "type", Operator: QueryOperatorNotEqual, Value: "chat"}}, 1},
		{"greater than", []Query{{Field: "size", Operator: QueryOperatorGreaterThan, Value: 10}}, 2},
		{"less than or equal", []Query{{Field: "size", Operator: QueryOperatorLessThanOrEqual, Value: 20}}, 2},
		{"boolean equal", []Query{{Field: "public", Operator: QueryOperatorEqual, Value: true}}, 2},
		{"like substring", []Query{{Field: "content", Operator: QueryOperatorLike, Value: "quick"}}, 2},
		{"like wildcard", []Query{{Field: "content", Operator: QueryOperatorLike, Value: "the%fox"}}, 1},
		{"contains any", []Query{{Field: "tags", Operator: QueryOperatorContainsAny, Value: []string{"z", "nope"}}}, 1},
		{"contains all", []Query{{Field: "tags", Operator: QueryOperatorContainsAll, Value: []string{"x", "y"}}}, 1},
		{"is null", []Query{{Field: "tags", Operator: QueryOperatorIsNull}}, 1},
		{"is not null", []Query{{Field: "tags", Operator: QueryOperatorIsNotNull}}, 2},
		{"combined", []Query{
			{Field: "type", Operator: QueryOperatorEqual, Value: "chat"},
			{Field: "public", Operator: QueryOperatorEqual, Value: true},
		}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, _, err := ts.Store.GetAll(ts.ctx, ChromemTestNamespace, tt.queries, nil, nil, 100)
			require.NoError(t, err)
			assert.Len(t, results, tt.expected)
		})
	}
}

func TestChromemStore_VectorSearch(t *testing.T) {
	ts := NewChromemTestSetup(t)
	defer ts.Cleanup(t)

	// Deterministic vectors: cosine similarity is exactly 0.9 for "near"
	// (0.9*e0 + sqrt(1-0.81)*e1) and exactly 0 for the orthogonal "far" (e1).
	queryVector := make([]float32, ChromemTestDimension)
	queryVector[0] = 1
	near := make([]float32, ChromemTestDimension)
	near[0] = 0.9
	near[1] = 0.43589
	far := make([]float32, ChromemTestDimension)
	far[1] = 1

	require.NoError(t, ts.Store.Add(ts.ctx, ChromemTestNamespace, "near", near, map[string]interface{}{"key": "near", "type": "chat"}))
	require.NoError(t, ts.Store.Add(ts.ctx, ChromemTestNamespace, "far", far, map[string]interface{}{"key": "far", "type": "chat"}))

	results, err := ts.Store.GetNearest(ts.ctx, ChromemTestNamespace, queryVector, nil, nil, -1.0, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "near", results[0].ID, "nearest result should rank first")
	require.NotNil(t, results[0].Score)

	// A threshold between the two similarities excludes the orthogonal vector.
	results, err = ts.Store.GetNearest(ts.ctx, ChromemTestNamespace, queryVector, nil, nil, 0.5, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "near", results[0].ID)
	require.NotNil(t, results[0].Score)
	assert.InDelta(t, 0.9, *results[0].Score, 0.01)

	// Filters apply on top of similarity.
	results, err = ts.Store.GetNearest(ts.ctx, ChromemTestNamespace, queryVector,
		[]Query{{Field: "key", Operator: QueryOperatorEqual, Value: "far"}}, nil, -1.0, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "far", results[0].ID)

	// Empty namespace returns no results, not an error.
	require.NoError(t, ts.Store.CreateNamespace(ts.ctx, "chromem_empty_ns", ChromemTestDimension, nil))
	defer func() { _ = ts.Store.DeleteNamespace(ts.ctx, "chromem_empty_ns") }()
	results, err = ts.Store.GetNearest(ts.ctx, "chromem_empty_ns", queryVector, nil, nil, -1.0, 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestChromemStore_Pagination(t *testing.T) {
	ts := NewChromemTestSetup(t)
	defer ts.Cleanup(t)

	base := generateTestEmbedding(ChromemTestDimension)
	for i := 0; i < 5; i++ {
		require.NoError(t, ts.Store.Add(ts.ctx, ChromemTestNamespace, fmt.Sprintf("page-doc-%d", i),
			generateSimilarEmbedding(base, 0.9), map[string]interface{}{"key": fmt.Sprintf("k-%d", i)}))
	}

	seen := make(map[string]bool)
	var cursor *string
	pages := 0
	for {
		results, next, err := ts.Store.GetAll(ts.ctx, ChromemTestNamespace, nil, nil, cursor, 2)
		require.NoError(t, err)
		for _, res := range results {
			assert.False(t, seen[res.ID], "duplicate result across pages: %s", res.ID)
			seen[res.ID] = true
		}
		pages++
		if next == nil {
			break
		}
		cursor = next
	}
	assert.Len(t, seen, 5)
	assert.Equal(t, 3, pages)

	// Invalid cursor errors instead of silently rescanning.
	badCursor := "not-a-number"
	_, _, err := ts.Store.GetAll(ts.ctx, ChromemTestNamespace, nil, nil, &badCursor, 2)
	assert.Error(t, err)

	// A caller-supplied limit is unbounded, so offset+limit can overflow int64
	// and wrap negative. Nothing downstream catches that: the end > total check
	// is false for a negative end, and the slice bounds then panic. Every page
	// past the first is reachable this way, since it needs a non-zero cursor.
	hugeLimit := int64(math.MaxInt64)
	firstCursor := "1"
	results, next, err := ts.Store.GetAll(ts.ctx, ChromemTestNamespace, nil, nil, &firstCursor, hugeLimit)
	require.NoError(t, err)
	assert.Nil(t, next, "a limit past the end leaves nothing to page to")
	assert.Len(t, results, 4, "an oversized limit returns the remainder, not a panic")
}

func TestChromemStore_DimensionHandling(t *testing.T) {
	ts := NewChromemTestSetup(t)
	defer ts.Cleanup(t)

	namespace := "chromem_dimension_test"
	require.NoError(t, ts.Store.CreateNamespace(ts.ctx, namespace, 512, nil))

	// Mismatched embedding dimension is rejected.
	err := ts.Store.Add(ts.ctx, namespace, "doc-1", generateTestEmbedding(384), nil)
	assert.Error(t, err)

	// Recreate at a different dimension.
	require.NoError(t, ts.Store.DeleteNamespace(ts.ctx, namespace))
	require.NoError(t, ts.Store.CreateNamespace(ts.ctx, namespace, 384, nil))
	defer func() { _ = ts.Store.DeleteNamespace(ts.ctx, namespace) }()

	embedding := generateTestEmbedding(384)
	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "doc-1", embedding, map[string]interface{}{"key": "v"}))
	results, err := ts.Store.GetNearest(ts.ctx, namespace, embedding, nil, nil, 0.0, 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)

	// Invalid dimensions are rejected at namespace creation.
	assert.Error(t, ts.Store.CreateNamespace(ts.ctx, "chromem_bad_dim", 0, nil))
	assert.Error(t, ts.Store.CreateNamespace(ts.ctx, "chromem_bad_dim", -1, nil))
}

func TestChromemStore_ErrorHandling(t *testing.T) {
	ts := NewChromemTestSetup(t)
	defer ts.Cleanup(t)

	// Empty IDs are rejected.
	err := ts.Store.Add(ts.ctx, ChromemTestNamespace, "", generateTestEmbedding(ChromemTestDimension), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")

	_, err = ts.Store.GetChunk(ts.ctx, ChromemTestNamespace, "")
	assert.Error(t, err)

	err = ts.Store.Delete(ts.ctx, ChromemTestNamespace, "")
	assert.Error(t, err)

	// Missing embedding is rejected (RequiresVectors is true).
	err = ts.Store.Add(ts.ctx, ChromemTestNamespace, "doc-x", nil, nil)
	assert.Error(t, err)

	// Missing chunk surfaces ErrNotFound.
	_, err = ts.Store.GetChunk(ts.ctx, ChromemTestNamespace, "missing-id")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "not found")

	// Deleting a missing document is idempotent.
	assert.NoError(t, ts.Store.Delete(ts.ctx, ChromemTestNamespace, "missing-id"))

	// Deleting a missing namespace is idempotent.
	assert.NoError(t, ts.Store.DeleteNamespace(ts.ctx, "chromem_never_created"))

	// Enumerating a missing namespace is ErrNotFound, not an empty result: a
	// namespace that was never created must not read back as an empty one.
	_, _, err = ts.Store.GetAll(ts.ctx, "chromem_never_created", nil, nil, nil, 100)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = ts.Store.DeleteAll(ts.ctx, "chromem_never_created", nil)
	assert.ErrorIs(t, err, ErrNotFound)

	// An existing but empty namespace still enumerates to nothing.
	require.NoError(t, ts.Store.CreateNamespace(ts.ctx, "chromem_empty_ns", ChromemTestDimension, nil))
	empty, cursor, err := ts.Store.GetAll(ts.ctx, "chromem_empty_ns", nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Empty(t, empty)
	assert.Nil(t, cursor)
}

func TestChromemStore_DeleteOperations(t *testing.T) {
	ts := NewChromemTestSetup(t)
	defer ts.Cleanup(t)

	base := generateTestEmbedding(ChromemTestDimension)
	for i := 0; i < 4; i++ {
		entryType := "chat"
		if i%2 == 1 {
			entryType = "completion"
		}
		require.NoError(t, ts.Store.Add(ts.ctx, ChromemTestNamespace, fmt.Sprintf("del-doc-%d", i),
			generateSimilarEmbedding(base, 0.9), map[string]interface{}{"type": entryType}))
	}

	// Single delete.
	require.NoError(t, ts.Store.Delete(ts.ctx, ChromemTestNamespace, "del-doc-0"))
	_, err := ts.Store.GetChunk(ts.ctx, ChromemTestNamespace, "del-doc-0")
	assert.ErrorIs(t, err, ErrNotFound)

	// Filtered DeleteAll only removes matches.
	results, err := ts.Store.DeleteAll(ts.ctx, ChromemTestNamespace, []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "completion"}})
	require.NoError(t, err)
	assert.Len(t, results, 2)
	for _, res := range results {
		assert.Equal(t, DeleteStatusSuccess, res.Status)
	}

	// A non-matching filter must not delete anything.
	results, err = ts.Store.DeleteAll(ts.ctx, ChromemTestNamespace, []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "no-such-type"}})
	require.NoError(t, err)
	assert.Empty(t, results)

	remaining, _, err := ts.Store.GetAll(ts.ctx, ChromemTestNamespace, nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Len(t, remaining, 1)

	// Unfiltered DeleteAll clears the namespace.
	_, err = ts.Store.DeleteAll(ts.ctx, ChromemTestNamespace, nil)
	require.NoError(t, err)
	remaining, _, err = ts.Store.GetAll(ts.ctx, ChromemTestNamespace, nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Empty(t, remaining)
}

func TestChromemStore_Persistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chromem-data")
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	embedding := generateTestEmbedding(ChromemTestDimension)

	store1, err := newChromemStore(ctx, &ChromemConfig{Path: path}, logger)
	require.NoError(t, err)
	require.NoError(t, store1.CreateNamespace(ctx, ChromemTestNamespace, ChromemTestDimension, nil))
	require.NoError(t, store1.Add(ctx, ChromemTestNamespace, "persistent-doc", embedding, map[string]interface{}{
		"key":  "persisted",
		"size": int64(42),
	}))
	require.NoError(t, store1.Close(ctx, ""))

	// Reopen from disk with a fresh store.
	store2, err := newChromemStore(ctx, &ChromemConfig{Path: path}, logger)
	require.NoError(t, err)

	// GetChunk works before CreateNamespace (no enumeration needed).
	result, err := store2.GetChunk(ctx, ChromemTestNamespace, "persistent-doc")
	require.NoError(t, err)
	assert.Equal(t, "persisted", result.Properties["key"])
	assert.Equal(t, int64(42), result.Properties["size"])

	// Enumeration requires the dimension, which is only known after
	// CreateNamespace — the documented startup contract.
	_, _, err = store2.GetAll(ctx, ChromemTestNamespace, nil, nil, nil, 100)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dimension not set")

	require.NoError(t, store2.CreateNamespace(ctx, ChromemTestNamespace, ChromemTestDimension, nil))
	all, _, err := store2.GetAll(ctx, ChromemTestNamespace, nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Len(t, all, 1)

	results, err := store2.GetNearest(ctx, ChromemTestNamespace, embedding, nil, nil, 0.9, 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

func TestChromemStore_InterfaceCompliance(t *testing.T) {
	var _ VectorStore = (*ChromemStore)(nil)
}

func TestVectorStoreFactory_Chromem(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	config := &Config{
		Enabled: true,
		Type:    VectorStoreTypeChromem,
		Config:  ChromemConfig{},
	}
	store, err := NewVectorStore(ctx, config, logger)
	require.NoError(t, err)
	_, ok := store.(*ChromemStore)
	assert.True(t, ok, "expected *ChromemStore from factory")
	assert.True(t, store.RequiresVectors())

	// JSON round-trip through Config.UnmarshalJSON, including a missing
	// config block (valid for chromem).
	var parsed Config
	require.NoError(t, parsed.UnmarshalJSON([]byte(`{"enabled": true, "type": "chromem", "config": {"path": "", "compress": false}}`)))
	assert.Equal(t, VectorStoreTypeChromem, parsed.Type)
	_, ok = parsed.Config.(ChromemConfig)
	assert.True(t, ok)

	var parsedNoConfig Config
	require.NoError(t, parsedNoConfig.UnmarshalJSON([]byte(`{"enabled": true, "type": "chromem"}`)))
	store, err = NewVectorStore(ctx, &parsedNoConfig, logger)
	require.NoError(t, err)
	assert.NotNil(t, store)
}
