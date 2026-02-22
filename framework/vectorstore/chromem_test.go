package vectorstore

import (
	"context"
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
	ChromemTestNamespace = "bifrost-chromem-test"
	ChromemTestDimension = 128
)

// ChromemTestSetup provides common test infrastructure.
// chromem-go is embeddable and in-process, so no external service is needed.
type ChromemTestSetup struct {
	Store  *ChromemStore
	Logger schemas.Logger
	Config ChromemConfig
	ctx    context.Context
	cancel context.CancelFunc
}

func NewChromemTestSetup(t *testing.T) *ChromemTestSetup {
	t.Helper()

	config := ChromemConfig{}
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx, cancel := context.WithTimeout(context.Background(), ChromemTestTimeout)

	store, err := newChromemStore(ctx, &config, logger)
	require.NoError(t, err, "failed to create chromem store")

	setup := &ChromemTestSetup{
		Store:  store,
		Logger: logger,
		Config: config,
		ctx:    ctx,
		cancel: cancel,
	}

	// Ensure the namespace exists and dimension is set so GetAll works.
	setup.ensureNamespaceExists(t)

	return setup
}

func (ts *ChromemTestSetup) ensureNamespaceExists(t *testing.T) {
	t.Helper()
	properties := map[string]VectorStoreProperties{
		"type":                               {DataType: VectorStorePropertyTypeString},
		"author":                             {DataType: VectorStorePropertyTypeString},
		"size":                               {DataType: VectorStorePropertyTypeInteger},
		"public":                             {DataType: VectorStorePropertyTypeBoolean},
		"category":                           {DataType: VectorStorePropertyTypeString},
		"content":                            {DataType: VectorStorePropertyTypeString},
		"request_hash":                       {DataType: VectorStorePropertyTypeString},
		"user":                               {DataType: VectorStorePropertyTypeString},
		"lang":                               {DataType: VectorStorePropertyTypeString},
		"response":                           {DataType: VectorStorePropertyTypeString},
		"from_bifrost_semantic_cache_plugin": {DataType: VectorStorePropertyTypeBoolean},
	}
	err := ts.Store.CreateNamespace(ts.ctx, ChromemTestNamespace, ChromemTestDimension, properties)
	require.NoError(t, err, "failed to create namespace")
}

func (ts *ChromemTestSetup) Cleanup(t *testing.T) {
	t.Helper()
	defer ts.cancel()

	if err := ts.Store.DeleteNamespace(ts.ctx, ChromemTestNamespace); err != nil {
		t.Logf("Warning: failed to delete namespace: %v", err)
	}

	if err := ts.Store.Close(ts.ctx, ChromemTestNamespace); err != nil {
		t.Logf("Warning: failed to close store: %v", err)
	}
}

// ============================================================================
// UNIT TESTS — pure functions, no external services
// ============================================================================

func TestChromemConfig_Creation(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	t.Run("in-memory store (no persist dir)", func(t *testing.T) {
		config := &ChromemConfig{}
		store, err := newChromemStore(ctx, config, logger)
		assert.NoError(t, err)
		assert.NotNil(t, store)
		assert.NotNil(t, store.db)
	})

	t.Run("in-memory store with explicit empty persist dir", func(t *testing.T) {
		empty := schemas.NewEnvVar("")
		config := &ChromemConfig{PersistDirectory: empty}
		store, err := newChromemStore(ctx, config, logger)
		assert.NoError(t, err)
		assert.NotNil(t, store)
	})

	t.Run("persistent store with temp dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		dir := schemas.NewEnvVar(tmpDir)
		config := &ChromemConfig{PersistDirectory: dir, Compress: false}
		store, err := newChromemStore(ctx, config, logger)
		assert.NoError(t, err)
		assert.NotNil(t, store)
	})

	t.Run("persistent store with compression", func(t *testing.T) {
		tmpDir := t.TempDir()
		dir := schemas.NewEnvVar(tmpDir)
		config := &ChromemConfig{PersistDirectory: dir, Compress: true}
		store, err := newChromemStore(ctx, config, logger)
		assert.NoError(t, err)
		assert.NotNil(t, store)
	})
}

func TestBuildChromemWhere(t *testing.T) {
	tests := []struct {
		name     string
		queries  []Query
		expected map[string]string
	}{
		{
			name:     "empty queries returns nil",
			queries:  []Query{},
			expected: nil,
		},
		{
			name: "equal operator maps to where",
			queries: []Query{
				{Field: "type", Operator: QueryOperatorEqual, Value: "document"},
			},
			expected: map[string]string{"type": "document"},
		},
		{
			name: "multiple equal operators",
			queries: []Query{
				{Field: "type", Operator: QueryOperatorEqual, Value: "pdf"},
				{Field: "author", Operator: QueryOperatorEqual, Value: "alice"},
			},
			expected: map[string]string{"type": "pdf", "author": "alice"},
		},
		{
			name: "non-equal operators are excluded (handled in-memory)",
			queries: []Query{
				{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1024},
			},
			expected: nil,
		},
		{
			name: "mix of equal and range: only equal appears in where",
			queries: []Query{
				{Field: "type", Operator: QueryOperatorEqual, Value: "pdf"},
				{Field: "size", Operator: QueryOperatorGreaterThan, Value: 500},
			},
			expected: map[string]string{"type": "pdf"},
		},
		{
			name: "not-equal operator is excluded",
			queries: []Query{
				{Field: "type", Operator: QueryOperatorNotEqual, Value: "deleted"},
			},
			expected: nil,
		},
		{
			name: "nil value for equal operator is excluded",
			queries: []Query{
				{Field: "type", Operator: QueryOperatorEqual, Value: nil},
			},
			expected: nil,
		},
		{
			name: "boolean value stringified",
			queries: []Query{
				{Field: "public", Operator: QueryOperatorEqual, Value: true},
			},
			expected: map[string]string{"public": "true"},
		},
		{
			name: "numeric value stringified",
			queries: []Query{
				{Field: "priority", Operator: QueryOperatorEqual, Value: 42},
			},
			expected: map[string]string{"priority": "42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildChromemWhere(tt.queries)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMapToChromemMetadata(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]string
	}{
		{
			name:     "nil input returns nil",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty map",
			input:    map[string]interface{}{},
			expected: map[string]string{},
		},
		{
			name: "string values pass through",
			input: map[string]interface{}{
				"type": "document",
			},
			expected: map[string]string{"type": "document"},
		},
		{
			name: "integer values become strings",
			input: map[string]interface{}{
				"size": 1024,
			},
			expected: map[string]string{"size": "1024"},
		},
		{
			name: "boolean values become strings",
			input: map[string]interface{}{
				"public": true,
				"draft":  false,
			},
			expected: map[string]string{"public": "true", "draft": "false"},
		},
		{
			name: "nil values are skipped",
			input: map[string]interface{}{
				"type":   "doc",
				"author": nil,
			},
			expected: map[string]string{"type": "doc"},
		},
		{
			name: "mixed types",
			input: map[string]interface{}{
				"name":  "bifrost",
				"count": 7,
				"valid": true,
			},
			expected: map[string]string{"name": "bifrost", "count": "7", "valid": "true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapToChromemMetadata(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestChromemMetadataToMap(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		expected map[string]interface{}
	}{
		{
			name:     "nil input returns empty map",
			input:    nil,
			expected: map[string]interface{}{},
		},
		{
			name:     "empty map",
			input:    map[string]string{},
			expected: map[string]interface{}{},
		},
		{
			name: "values are kept as strings",
			input: map[string]string{
				"type": "document",
				"size": "1024",
			},
			expected: map[string]interface{}{"type": "document", "size": "1024"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := chromemMetadataToMap(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFilterPropertiesChromem(t *testing.T) {
	props := map[string]interface{}{
		"type":   "document",
		"author": "alice",
		"size":   "1024",
		"public": "true",
	}

	tests := []struct {
		name         string
		selectFields []string
		expected     map[string]interface{}
	}{
		{
			name:         "empty selectFields returns all props",
			selectFields: []string{},
			expected:     props,
		},
		{
			name:         "select single field",
			selectFields: []string{"type"},
			expected:     map[string]interface{}{"type": "document"},
		},
		{
			name:         "select multiple fields",
			selectFields: []string{"type", "author"},
			expected:     map[string]interface{}{"type": "document", "author": "alice"},
		},
		{
			name:         "select non-existent field returns empty map",
			selectFields: []string{"missing"},
			expected:     map[string]interface{}{},
		},
		{
			name:         "mix of existing and missing fields",
			selectFields: []string{"type", "missing"},
			expected:     map[string]interface{}{"type": "document"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterPropertiesChromem(props, tt.selectFields)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ============================================================================
// FUNCTIONAL TESTS — chromem-go is in-process; no external service required
// ============================================================================

func TestChromemStore_Ping(t *testing.T) {
	setup := NewChromemTestSetup(t)
	defer setup.Cleanup(t)

	// chromem is in-process; Ping always succeeds
	err := setup.Store.Ping(setup.ctx)
	assert.NoError(t, err)
}

func TestChromemStore_RequiresVectors(t *testing.T) {
	setup := NewChromemTestSetup(t)
	defer setup.Cleanup(t)

	assert.True(t, setup.Store.RequiresVectors())
}

func TestChromemStore_Namespace(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	store, err := newChromemStore(ctx, &ChromemConfig{}, logger)
	require.NoError(t, err)

	t.Run("CreateNamespace succeeds", func(t *testing.T) {
		err := store.CreateNamespace(ctx, "test-ns-create", 64, map[string]VectorStoreProperties{
			"type": {DataType: VectorStorePropertyTypeString},
		})
		assert.NoError(t, err)
	})

	t.Run("CreateNamespace is idempotent", func(t *testing.T) {
		props := map[string]VectorStoreProperties{
			"type": {DataType: VectorStorePropertyTypeString},
		}
		err := store.CreateNamespace(ctx, "test-ns-idempotent", 64, props)
		require.NoError(t, err)
		// Creating again should not fail
		err = store.CreateNamespace(ctx, "test-ns-idempotent", 64, props)
		assert.NoError(t, err)
	})

	t.Run("DeleteNamespace removes collection", func(t *testing.T) {
		err := store.CreateNamespace(ctx, "test-ns-delete", 64, nil)
		require.NoError(t, err)

		err = store.DeleteNamespace(ctx, "test-ns-delete")
		assert.NoError(t, err)
	})

	t.Run("GetAll without CreateNamespace returns error (dimension not set)", func(t *testing.T) {
		freshStore, err := newChromemStore(ctx, &ChromemConfig{}, logger)
		require.NoError(t, err)

		// Add a document directly (bypassing CreateNamespace) so count > 0.
		// GetAll short-circuits with no error when the collection is empty, so
		// a document is required to reach the dimension guard.
		err = freshStore.Add(ctx, "no-dimension-ns", generateUUID(), generateTestEmbedding(32), map[string]interface{}{"type": "test"})
		require.NoError(t, err)

		// Explicitly clear dimension to confirm the guard triggers.
		freshStore.dimension = 0

		_, _, err = freshStore.GetAll(ctx, "no-dimension-ns", nil, nil, nil, 10)
		require.Error(t, err) // require so the Contains below never runs on nil
		assert.Contains(t, err.Error(), "dimension not set")
	})
}

func TestChromemStore_Integration(t *testing.T) {
	setup := NewChromemTestSetup(t)
	defer setup.Cleanup(t)

	t.Run("Add and GetChunk", func(t *testing.T) {
		key := generateUUID()
		embedding := generateTestEmbedding(ChromemTestDimension)
		metadata := map[string]interface{}{
			"type":   "document",
			"author": "alice",
		}

		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, key, embedding, metadata)
		require.NoError(t, err)

		result, err := setup.Store.GetChunk(setup.ctx, ChromemTestNamespace, key)
		require.NoError(t, err)
		assert.Equal(t, key, result.ID)
		// chromem stores metadata as strings
		assert.Equal(t, "document", result.Properties["type"])
		assert.Equal(t, "alice", result.Properties["author"])
	})

	t.Run("GetChunks batch retrieval", func(t *testing.T) {
		keys := []string{generateUUID(), generateUUID(), generateUUID()}
		for i, key := range keys {
			metadata := map[string]interface{}{"type": "batch", "index": i}
			err := setup.Store.Add(setup.ctx, ChromemTestNamespace, key, generateTestEmbedding(ChromemTestDimension), metadata)
			require.NoError(t, err)
		}

		results, err := setup.Store.GetChunks(setup.ctx, ChromemTestNamespace, keys)
		require.NoError(t, err)
		assert.Len(t, results, 3)

		gotIDs := make(map[string]bool)
		for _, r := range results {
			gotIDs[r.ID] = true
		}
		for _, key := range keys {
			assert.True(t, gotIDs[key], "expected key %s in results", key)
		}
	})

	t.Run("GetChunks with empty ids returns empty slice", func(t *testing.T) {
		results, err := setup.Store.GetChunks(setup.ctx, ChromemTestNamespace, []string{})
		require.NoError(t, err)
		assert.Empty(t, results)
	})

	t.Run("GetChunks skips blank ids gracefully", func(t *testing.T) {
		key := generateUUID()
		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, key, generateTestEmbedding(ChromemTestDimension), map[string]interface{}{"type": "test"})
		require.NoError(t, err)

		// Mix valid key with blank; blank should be skipped
		results, err := setup.Store.GetChunks(setup.ctx, ChromemTestNamespace, []string{key, "", "   "})
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, key, results[0].ID)
	})

	t.Run("GetChunks with non-existent id skips silently", func(t *testing.T) {
		key := generateUUID()
		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, key, generateTestEmbedding(ChromemTestDimension), map[string]interface{}{"type": "test"})
		require.NoError(t, err)

		results, err := setup.Store.GetChunks(setup.ctx, ChromemTestNamespace, []string{key, generateUUID()})
		require.NoError(t, err)
		assert.Len(t, results, 1)
	})
}

func TestChromemStore_VectorSearch(t *testing.T) {
	setup := NewChromemTestSetup(t)
	defer setup.Cleanup(t)

	// Add documents with known embeddings
	targetEmbedding := generateTestEmbedding(ChromemTestDimension)
	documents := []struct {
		key       string
		embedding []float32
		metadata  map[string]interface{}
	}{
		{
			generateUUID(), targetEmbedding,
			map[string]interface{}{"type": "tech", "category": "Go"},
		},
		{
			generateUUID(), generateTestEmbedding(ChromemTestDimension),
			map[string]interface{}{"type": "tech", "category": "Python"},
		},
		{
			generateUUID(), generateTestEmbedding(ChromemTestDimension),
			map[string]interface{}{"type": "sports", "category": "football"},
		},
	}

	for _, doc := range documents {
		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, doc.key, doc.embedding, doc.metadata)
		require.NoError(t, err)
	}

	t.Run("GetNearest returns results above threshold", func(t *testing.T) {
		results, err := setup.Store.GetNearest(setup.ctx, ChromemTestNamespace, targetEmbedding, nil, []string{"type", "category"}, 0.0, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(results), 1)

		// All results must have a score
		for _, r := range results {
			require.NotNil(t, r.Score)
		}
	})

	t.Run("Top result for identical embedding has near-perfect score", func(t *testing.T) {
		results, err := setup.Store.GetNearest(setup.ctx, ChromemTestNamespace, targetEmbedding, nil, []string{"type", "category"}, 0.0, 10)
		require.NoError(t, err)
		require.NotEmpty(t, results)
		assert.InDelta(t, 1.0, *results[0].Score, 0.01)
	})

	t.Run("High threshold filters low-similarity results", func(t *testing.T) {
		results, err := setup.Store.GetNearest(setup.ctx, ChromemTestNamespace, targetEmbedding, nil, []string{"type"}, 0.99, 10)
		require.NoError(t, err)
		// Only the identical document should survive a 0.99 threshold
		for _, r := range results {
			assert.GreaterOrEqual(t, *r.Score, 0.99)
		}
	})

	t.Run("GetNearest with metadata filter", func(t *testing.T) {
		queries := []Query{
			{Field: "type", Operator: QueryOperatorEqual, Value: "tech"},
		}
		results, err := setup.Store.GetNearest(setup.ctx, ChromemTestNamespace, targetEmbedding, queries, []string{"type", "category"}, 0.0, 10)
		require.NoError(t, err)
		for _, r := range results {
			assert.Equal(t, "tech", r.Properties["type"])
		}
	})

	t.Run("GetNearest with selectFields limits returned properties", func(t *testing.T) {
		results, err := setup.Store.GetNearest(setup.ctx, ChromemTestNamespace, targetEmbedding, nil, []string{"type"}, 0.0, 10)
		require.NoError(t, err)
		require.NotEmpty(t, results)
		// category should not be present
		_, hasCategory := results[0].Properties["category"]
		assert.False(t, hasCategory)
	})

	t.Run("GetNearest on empty namespace returns empty slice", func(t *testing.T) {
		logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
		emptyStore, err := newChromemStore(context.Background(), &ChromemConfig{}, logger)
		require.NoError(t, err)
		_ = emptyStore.CreateNamespace(context.Background(), "empty-ns", ChromemTestDimension, nil)

		results, err := emptyStore.GetNearest(context.Background(), "empty-ns", targetEmbedding, nil, nil, 0.0, 10)
		require.NoError(t, err)
		assert.Empty(t, results)
	})

	t.Run("limit is respected", func(t *testing.T) {
		results, err := setup.Store.GetNearest(setup.ctx, ChromemTestNamespace, targetEmbedding, nil, nil, 0.0, 1)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(results), 1)
	})
}

func TestChromemStore_GetAll(t *testing.T) {
	setup := NewChromemTestSetup(t)
	defer setup.Cleanup(t)

	// Populate test data
	testData := []struct {
		key      string
		metadata map[string]interface{}
	}{
		{generateUUID(), map[string]interface{}{"type": "pdf", "author": "alice", "public": "true"}},
		{generateUUID(), map[string]interface{}{"type": "docx", "author": "bob", "public": "false"}},
		{generateUUID(), map[string]interface{}{"type": "pdf", "author": "alice", "public": "true"}},
		{generateUUID(), map[string]interface{}{"type": "txt", "author": "charlie", "public": "true"}},
	}

	for _, item := range testData {
		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, item.key, generateTestEmbedding(ChromemTestDimension), item.metadata)
		require.NoError(t, err)
	}

	t.Run("GetAll with no filter returns all documents", func(t *testing.T) {
		results, cursor, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, nil, nil, nil, 100)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(results), 4)
		assert.Nil(t, cursor)
	})

	t.Run("Filter by equal operator", func(t *testing.T) {
		queries := []Query{
			{Field: "type", Operator: QueryOperatorEqual, Value: "pdf"},
		}
		results, _, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, queries, []string{"type", "author"}, nil, 100)
		require.NoError(t, err)
		for _, r := range results {
			assert.Equal(t, "pdf", r.Properties["type"])
		}
	})

	t.Run("Filter by not-equal operator", func(t *testing.T) {
		queries := []Query{
			{Field: "type", Operator: QueryOperatorNotEqual, Value: "docx"},
		}
		results, _, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, queries, []string{"type"}, nil, 100)
		require.NoError(t, err)
		for _, r := range results {
			assert.NotEqual(t, "docx", r.Properties["type"])
		}
	})

	t.Run("Multiple equal filters (AND)", func(t *testing.T) {
		queries := []Query{
			{Field: "type", Operator: QueryOperatorEqual, Value: "pdf"},
			{Field: "author", Operator: QueryOperatorEqual, Value: "alice"},
		}
		results, _, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, queries, []string{"type", "author"}, nil, 100)
		require.NoError(t, err)
		for _, r := range results {
			assert.Equal(t, "pdf", r.Properties["type"])
			assert.Equal(t, "alice", r.Properties["author"])
		}
	})

	t.Run("selectFields limits returned properties", func(t *testing.T) {
		results, _, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, nil, []string{"type"}, nil, 100)
		require.NoError(t, err)
		require.NotEmpty(t, results)
		for _, r := range results {
			_, hasAuthor := r.Properties["author"]
			assert.False(t, hasAuthor)
			_, hasType := r.Properties["type"]
			assert.True(t, hasType)
		}
	})

	t.Run("Pagination: cursor-based", func(t *testing.T) {
		// GetAll uses QueryEmbedding with a zero vector (chromem has no ListDocuments).
		firstPage, cursor, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, nil, nil, nil, 2)
		require.NoError(t, err)
		assert.Len(t, firstPage, 2)

		if cursor != nil {
			nextPage, _, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, nil, nil, cursor, 2)
			require.NoError(t, err)
			assert.LessOrEqual(t, len(nextPage), 2)
			t.Logf("First page: %d results, next page: %d results", len(firstPage), len(nextPage))
		}
	})

	t.Run("Limit of zero defaults to 100", func(t *testing.T) {
		results, _, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, nil, nil, nil, 0)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(results), 4)
	})

	t.Run("GetAll on empty collection returns empty slice", func(t *testing.T) {
		logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
		emptyStore, err := newChromemStore(context.Background(), &ChromemConfig{}, logger)
		require.NoError(t, err)
		_ = emptyStore.CreateNamespace(context.Background(), "empty-getall", ChromemTestDimension, nil)

		results, cursor, err := emptyStore.GetAll(context.Background(), "empty-getall", nil, nil, nil, 10)
		require.NoError(t, err)
		assert.Empty(t, results)
		assert.Nil(t, cursor)
	})
}

func TestChromemStore_DeleteOperations(t *testing.T) {
	setup := NewChromemTestSetup(t)
	defer setup.Cleanup(t)

	t.Run("Delete single document", func(t *testing.T) {
		key := generateUUID()
		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, key, generateTestEmbedding(ChromemTestDimension), map[string]interface{}{"type": "ephemeral"})
		require.NoError(t, err)

		// Verify it exists
		_, err = setup.Store.GetChunk(setup.ctx, ChromemTestNamespace, key)
		require.NoError(t, err)

		// Delete it
		err = setup.Store.Delete(setup.ctx, ChromemTestNamespace, key)
		require.NoError(t, err)

		// Verify it is gone
		_, err = setup.Store.GetChunk(setup.ctx, ChromemTestNamespace, key)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("DeleteAll with equal filter removes matching documents", func(t *testing.T) {
		// Add documents to delete
		for i := 0; i < 3; i++ {
			err := setup.Store.Add(setup.ctx, ChromemTestNamespace, generateUUID(), generateTestEmbedding(ChromemTestDimension), map[string]interface{}{"type": "to-delete"})
			require.NoError(t, err)
		}
		// Add a document that should survive
		keepKey := generateUUID()
		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, keepKey, generateTestEmbedding(ChromemTestDimension), map[string]interface{}{"type": "keep-me"})
		require.NoError(t, err)

		queries := []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "to-delete"}}
		results, err := setup.Store.DeleteAll(setup.ctx, ChromemTestNamespace, queries)
		require.NoError(t, err)
		// chromem with hasOnlyExactMatch returns empty results (no per-doc tracking)
		_ = results

		// Verify kept document still exists
		kept, err := setup.Store.GetChunk(setup.ctx, ChromemTestNamespace, keepKey)
		require.NoError(t, err)
		assert.Equal(t, "keep-me", kept.Properties["type"])
	})

	t.Run("DeleteAll with no queries removes all documents", func(t *testing.T) {
		logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
		isolatedStore, err := newChromemStore(context.Background(), &ChromemConfig{}, logger)
		require.NoError(t, err)
		ns := "delete-all-ns"
		_ = isolatedStore.CreateNamespace(context.Background(), ns, ChromemTestDimension, nil)

		for i := 0; i < 3; i++ {
			err := isolatedStore.Add(context.Background(), ns, generateUUID(), generateTestEmbedding(ChromemTestDimension), map[string]interface{}{"type": "doc"})
			require.NoError(t, err)
		}

		results, err := isolatedStore.DeleteAll(context.Background(), ns, nil)
		require.NoError(t, err)
		assert.Len(t, results, 3)

		// Verify all deleted
		remaining, _, err := isolatedStore.GetAll(context.Background(), ns, nil, nil, nil, 100)
		require.NoError(t, err)
		assert.Empty(t, remaining)
	})

	t.Run("DeleteAll on empty collection returns empty results", func(t *testing.T) {
		logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
		emptyStore, err := newChromemStore(context.Background(), &ChromemConfig{}, logger)
		require.NoError(t, err)
		_ = emptyStore.CreateNamespace(context.Background(), "empty-delete-ns", ChromemTestDimension, nil)

		results, err := emptyStore.DeleteAll(context.Background(), "empty-delete-ns", nil)
		require.NoError(t, err)
		assert.Empty(t, results)
	})
}

func TestChromemStore_ErrorHandling(t *testing.T) {
	setup := NewChromemTestSetup(t)
	defer setup.Cleanup(t)

	t.Run("Add with empty ID returns error", func(t *testing.T) {
		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, "", generateTestEmbedding(ChromemTestDimension), map[string]interface{}{"type": "test"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "id is required")
	})

	t.Run("Add with whitespace-only ID returns error", func(t *testing.T) {
		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, "   ", generateTestEmbedding(ChromemTestDimension), map[string]interface{}{"type": "test"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "id is required")
	})

	t.Run("GetChunk with empty ID returns error", func(t *testing.T) {
		_, err := setup.Store.GetChunk(setup.ctx, ChromemTestNamespace, "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "id is required")
	})

	t.Run("GetChunk with non-existent ID returns not-found error", func(t *testing.T) {
		_, err := setup.Store.GetChunk(setup.ctx, ChromemTestNamespace, generateUUID())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("Delete with empty ID returns error", func(t *testing.T) {
		err := setup.Store.Delete(setup.ctx, ChromemTestNamespace, "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "id is required")
	})

	t.Run("Delete with whitespace-only ID returns error", func(t *testing.T) {
		err := setup.Store.Delete(setup.ctx, ChromemTestNamespace, "  ")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "id is required")
	})

	t.Run("Close always succeeds (in-process store)", func(t *testing.T) {
		err := setup.Store.Close(setup.ctx, ChromemTestNamespace)
		assert.NoError(t, err)
	})
}

func TestChromemStore_SemanticCacheWorkflow(t *testing.T) {
	setup := NewChromemTestSetup(t)
	defer setup.Cleanup(t)

	// Simulate a semantic cache workflow: store request embeddings with response metadata,
	// then find the most similar cached entry.
	cacheEntries := []struct {
		key       string
		embedding []float32
		metadata  map[string]interface{}
	}{
		{
			generateUUID(),
			generateTestEmbedding(ChromemTestDimension),
			map[string]interface{}{
				"request_hash":                       "abc123",
				"user":                               "u1",
				"lang":                               "en",
				"response":                           "answer1",
				"from_bifrost_semantic_cache_plugin": "true",
			},
		},
		{
			generateUUID(),
			generateTestEmbedding(ChromemTestDimension),
			map[string]interface{}{
				"request_hash":                       "def456",
				"user":                               "u1",
				"lang":                               "es",
				"response":                           "answer2",
				"from_bifrost_semantic_cache_plugin": "true",
			},
		},
		{
			generateUUID(),
			generateTestEmbedding(ChromemTestDimension),
			map[string]interface{}{
				"request_hash":                       "ghi789",
				"user":                               "u2",
				"lang":                               "en",
				"response":                           "answer3",
				"from_bifrost_semantic_cache_plugin": "true",
			},
		},
	}

	for _, entry := range cacheEntries {
		err := setup.Store.Add(setup.ctx, ChromemTestNamespace, entry.key, entry.embedding, entry.metadata)
		require.NoError(t, err)
	}

	selectFields := []string{"request_hash", "user", "lang", "response", "from_bifrost_semantic_cache_plugin"}

	t.Run("Exact hash lookup via GetAll", func(t *testing.T) {
		queries := []Query{{Field: "request_hash", Operator: QueryOperatorEqual, Value: "abc123"}}
		results, _, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, queries, selectFields, nil, 10)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, "abc123", results[0].Properties["request_hash"])
		assert.Equal(t, "answer1", results[0].Properties["response"])
	})

	t.Run("Semantic search with user filter finds relevant entry", func(t *testing.T) {
		queries := []Query{
			{Field: "user", Operator: QueryOperatorEqual, Value: "u1"},
			{Field: "lang", Operator: QueryOperatorEqual, Value: "en"},
		}
		similar := generateSimilarEmbedding(cacheEntries[0].embedding, 0.95)
		results, err := setup.Store.GetNearest(setup.ctx, ChromemTestNamespace, similar, queries, selectFields, 0.0, 10)
		require.NoError(t, err)
		for _, r := range results {
			assert.Equal(t, "u1", r.Properties["user"])
			assert.Equal(t, "en", r.Properties["lang"])
		}
	})

	t.Run("User filter excludes other users' entries", func(t *testing.T) {
		queries := []Query{{Field: "user", Operator: QueryOperatorEqual, Value: "u2"}}
		results, _, err := setup.Store.GetAll(setup.ctx, ChromemTestNamespace, queries, selectFields, nil, 10)
		require.NoError(t, err)
		for _, r := range results {
			assert.Equal(t, "u2", r.Properties["user"])
		}
	})
}

func TestChromemStore_PersistentMode(t *testing.T) {
	tmpDir := t.TempDir()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	persistDir := schemas.NewEnvVar(filepath.Join(tmpDir, "chromem-data"))
	config := &ChromemConfig{PersistDirectory: persistDir, Compress: false}

	// Write data to persistent store
	store, err := newChromemStore(ctx, config, logger)
	require.NoError(t, err)

	ns := "persist-ns"
	err = store.CreateNamespace(ctx, ns, ChromemTestDimension, map[string]VectorStoreProperties{
		"type": {DataType: VectorStorePropertyTypeString},
	})
	require.NoError(t, err)

	key := generateUUID()
	emb := generateTestEmbedding(ChromemTestDimension)
	err = store.Add(ctx, ns, key, emb, map[string]interface{}{"type": "persistent-doc"})
	require.NoError(t, err)

	// Verify the persist directory was created
	_, statErr := os.Stat(persistDir.GetValue())
	assert.NoError(t, statErr, "persist directory should exist after writing")

	// Re-open the persistent store and verify data is accessible
	store2, err := newChromemStore(ctx, config, logger)
	require.NoError(t, err)

	result, err := store2.GetChunk(ctx, ns, key)
	require.NoError(t, err)
	assert.Equal(t, key, result.ID)
	assert.Equal(t, "persistent-doc", result.Properties["type"])
}

// ============================================================================
// INTERFACE COMPLIANCE TESTS
// ============================================================================

func TestChromemStore_InterfaceCompliance(t *testing.T) {
	var _ VectorStore = (*ChromemStore)(nil)
}

func TestVectorStoreFactory_Chromem(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	config := &Config{
		Enabled: true,
		Type:    VectorStoreTypeChromem,
		Config:  ChromemConfig{},
	}

	store, err := NewVectorStore(context.Background(), config, logger)
	require.NoError(t, err)
	defer store.Close(context.Background(), ChromemTestNamespace)

	chromemStore, ok := store.(*ChromemStore)
	assert.True(t, ok)
	assert.NotNil(t, chromemStore)
}

func TestVectorStoreFactory_Chromem_Persistent(t *testing.T) {
	tmpDir := t.TempDir()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)

	persistDir := schemas.NewEnvVar(filepath.Join(tmpDir, "factory-chromem"))
	config := &Config{
		Enabled: true,
		Type:    VectorStoreTypeChromem,
		Config:  ChromemConfig{PersistDirectory: persistDir, Compress: false},
	}

	store, err := NewVectorStore(context.Background(), config, logger)
	require.NoError(t, err)
	defer store.Close(context.Background(), ChromemTestNamespace)

	chromemStore, ok := store.(*ChromemStore)
	assert.True(t, ok)
	assert.NotNil(t, chromemStore)
	assert.NotNil(t, chromemStore.config.PersistDirectory)
}
