package vectorstore

import (
	"context"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test constants
const (
	RedisTestTimeout        = 30 * time.Second
	TestNamespace           = "TestRedis"
	DefaultTestAddr         = "localhost:6379"
	DefaultRedisTestTimeout = 10 * time.Second
	RedisTestDimension      = 1536
)

// TestSetup provides common test infrastructure
type RedisTestSetup struct {
	Store  *RedisStore
	Logger schemas.Logger
	Config RedisConfig
	ctx    context.Context
	cancel context.CancelFunc
}

// NewRedisTestSetup creates a test setup with environment-driven configuration
func NewRedisTestSetup(t *testing.T) *RedisTestSetup {
	// Get configuration from environment variables

	addr := schemas.NewEnvVar(getEnvWithDefault("REDIS_ADDR", DefaultTestAddr))
	username := schemas.NewEnvVar(os.Getenv("REDIS_USERNAME"))
	password := schemas.NewEnvVar(os.Getenv("REDIS_PASSWORD"))
	db := schemas.NewEnvVar(getEnvWithDefault("REDIS_DB", "0"))

	timeoutStr := getEnvWithDefault("REDIS_TIMEOUT", "10s")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		timeout = DefaultRedisTestTimeout
	}

	config := RedisConfig{
		Addr:           addr,
		Username:       username,
		Password:       password,
		DB:             db,
		ContextTimeout: timeout,
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx, cancel := context.WithTimeout(context.Background(), RedisTestTimeout)

	store, err := newRedisStore(ctx, config, logger)
	if err != nil {
		cancel()
		t.Fatalf("Failed to create Redis store: %v", err)
	}

	setup := &RedisTestSetup{
		Store:  store,
		Logger: logger,
		Config: config,
		ctx:    ctx,
		cancel: cancel,
	}

	// Ensure namespace exists for integration tests
	if !testing.Short() {
		setup.ensureNamespaceExists(t)
	}

	return setup
}

// Cleanup cleans up test resources
func (ts *RedisTestSetup) Cleanup(t *testing.T) {
	defer ts.cancel()

	if !testing.Short() {
		// Clean up test data
		ts.cleanupTestData(t)
	}

	if err := ts.Store.Close(ts.ctx, TestNamespace); err != nil {
		t.Logf("Warning: Failed to close store: %v", err)
	}
}

// ensureNamespaceExists creates the test namespace in Redis
func (ts *RedisTestSetup) ensureNamespaceExists(t *testing.T) {
	// Create namespace with test properties
	properties := map[string]VectorStoreProperties{
		"key": {
			DataType: VectorStorePropertyTypeString,
		},
		"type": {
			DataType: VectorStorePropertyTypeString,
		},
		"test_type": {
			DataType: VectorStorePropertyTypeString,
		},
		"size": {
			DataType: VectorStorePropertyTypeInteger,
		},
		"public": {
			DataType: VectorStorePropertyTypeBoolean,
		},
		"author": {
			DataType: VectorStorePropertyTypeString,
		},
		"request_hash": {
			DataType: VectorStorePropertyTypeString,
		},
		"user": {
			DataType: VectorStorePropertyTypeString,
		},
		"lang": {
			DataType: VectorStorePropertyTypeString,
		},
		"category": {
			DataType: VectorStorePropertyTypeString,
		},
		"content": {
			DataType: VectorStorePropertyTypeString,
		},
		"response": {
			DataType: VectorStorePropertyTypeString,
		},
		"from_bifrost_semantic_cache_plugin": {
			DataType: VectorStorePropertyTypeBoolean,
		},
	}

	err := ts.Store.CreateNamespace(ts.ctx, TestNamespace, RedisTestDimension, properties)
	if err != nil {
		t.Fatalf("Failed to create namespace %q: %v", TestNamespace, err)
	}
	t.Logf("Created test namespace: %s", TestNamespace)
}

// cleanupTestData removes all test objects from the namespace
func (ts *RedisTestSetup) cleanupTestData(t *testing.T) {
	// Delete all objects in the test namespace
	allTestKeys, _, err := ts.Store.GetAll(ts.ctx, TestNamespace, []Query{}, []string{}, nil, 1000)
	if err != nil {
		t.Logf("Warning: Failed to get all test keys: %v", err)
		return
	}

	for _, key := range allTestKeys {
		err := ts.Store.Delete(ts.ctx, TestNamespace, key.ID)
		if err != nil {
			t.Logf("Warning: Failed to delete test key %s: %v", key.ID, err)
		}
	}

	t.Logf("Cleaned up test namespace: %s", TestNamespace)
}

// ============================================================================
// UNIT TESTS
// ============================================================================

func TestRedisConfig_Validation(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	tests := []struct {
		name        string
		config      RedisConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			config: RedisConfig{
				Addr: schemas.NewEnvVar("localhost:6379"),
			},
			expectError: false,
		},
		{
			name: "missing addr",
			config: RedisConfig{
				Username: schemas.NewEnvVar("user"),
			},
			expectError: true,
			errorMsg:    "redis addr is required",
		},
		{
			name: "with credentials",
			config: RedisConfig{
				Addr:     schemas.NewEnvVar("localhost:6379"),
				Username: schemas.NewEnvVar("default"),
				Password: schemas.NewEnvVar(""),
			},
			expectError: false,
		},
		{
			name: "with custom db",
			config: RedisConfig{
				Addr: schemas.NewEnvVar("localhost:6379"),
				DB:   schemas.NewEnvVar("1"),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := newRedisStore(ctx, tt.config, logger)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, store)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				// For valid configs, store creation should succeed
				// (connection will fail later when actually using Redis)
				assert.NoError(t, err)
				assert.NotNil(t, store)
			}
		})
	}
}

func TestRedisStore_ParseSearchResults_RESP3Map(t *testing.T) {
	store := &RedisStore{}
	resp := map[interface{}]interface{}{
		"results": []interface{}{
			map[interface{}]interface{}{
				"id": "TestRedis:doc-1",
				"extra_attributes": map[interface{}]interface{}{
					"score":        "0.123",
					"request_hash": "abc123",
					"cache_key":    "session-1",
				},
			},
		},
	}

	results, err := store.parseSearchResults(resp, []string{"request_hash"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "doc-1", results[0].ID)
	assert.Equal(t, "abc123", results[0].Properties["request_hash"])
	assert.Equal(t, "0.123", results[0].Properties["score"])
	assert.NotContains(t, results[0].Properties, "cache_key")
	require.NotNil(t, results[0].Score)
	assert.InDelta(t, 0.123, *results[0].Score, 0.000001)
}

func TestRedisStore_ParseSearchResults_RESP2Array(t *testing.T) {
	store := &RedisStore{}
	resp := []interface{}{
		int64(1),
		"TestRedis:doc-2",
		[]interface{}{
			"score", []byte("0.25"),
			"request_hash", "def456",
			"cache_key", "session-2",
		},
	}

	results, err := store.parseSearchResults(resp, nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "doc-2", results[0].ID)
	assert.Equal(t, "def456", results[0].Properties["request_hash"])
	assert.Equal(t, "session-2", results[0].Properties["cache_key"])
	assert.Equal(t, []byte("0.25"), results[0].Properties["score"])
	require.NotNil(t, results[0].Score)
	assert.InDelta(t, 0.25, *results[0].Score, 0.000001)
}

func TestRedisStore_ParseSearchResults_RESP3StringKeyMap(t *testing.T) {
	store := &RedisStore{}
	resp := map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{
				"id": "TestRedis:doc-3",
				"extra_attributes": map[string]interface{}{
					"score":        "0.456",
					"request_hash": "ghi789",
					"cache_key":    "session-3",
				},
			},
		},
	}

	results, err := store.parseSearchResults(resp, []string{"request_hash"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "doc-3", results[0].ID)
	assert.Equal(t, "ghi789", results[0].Properties["request_hash"])
	assert.Equal(t, "0.456", results[0].Properties["score"])
	assert.NotContains(t, results[0].Properties, "cache_key")
	require.NotNil(t, results[0].Score)
	assert.InDelta(t, 0.456, *results[0].Score, 0.000001)
}

func TestRedisStore_ParseSearchResults_EmptyRESP2(t *testing.T) {
	store := &RedisStore{}
	// RESP2 array with total count 0 and no documents
	resp := []interface{}{
		int64(0),
	}

	results, err := store.parseSearchResults(resp, nil)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestRedisStore_ParseSearchResults_ByteScore(t *testing.T) {
	store := &RedisStore{}
	// Simulates Valkey RESP2 returning score as []byte
	resp := []interface{}{
		int64(1),
		"TestRedis:doc-4",
		[]interface{}{
			"score", []byte("0.75"),
			"request_hash", "jkl012",
		},
	}

	results, err := store.parseSearchResults(resp, nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "doc-4", results[0].ID)
	require.NotNil(t, results[0].Score)
	assert.InDelta(t, 0.75, *results[0].Score, 0.000001)
}

func TestMatchesQueriesForScan(t *testing.T) {
	tests := []struct {
		name       string
		properties map[string]interface{}
		queries    []Query
		expected   bool
	}{
		// GreaterThan
		{
			name:       "GreaterThan true",
			properties: map[string]interface{}{"size": "1024"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1000}},
			expected:   true,
		},
		{
			name:       "GreaterThan false",
			properties: map[string]interface{}{"size": "500"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1000}},
			expected:   false,
		},
		{
			name:       "GreaterThan equal value is false",
			properties: map[string]interface{}{"size": "1000"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1000}},
			expected:   false,
		},
		// LessThan
		{
			name:       "LessThan true",
			properties: map[string]interface{}{"size": "500"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorLessThan, Value: 1000}},
			expected:   true,
		},
		{
			name:       "LessThan false",
			properties: map[string]interface{}{"size": "1024"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorLessThan, Value: 1000}},
			expected:   false,
		},
		{
			name:       "LessThan equal value is false",
			properties: map[string]interface{}{"size": "1000"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorLessThan, Value: 1000}},
			expected:   false,
		},
		// GreaterThanOrEqual
		{
			name:       "GreaterThanOrEqual boundary true",
			properties: map[string]interface{}{"size": "1000"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorGreaterThanOrEqual, Value: 1000}},
			expected:   true,
		},
		{
			name:       "GreaterThanOrEqual above true",
			properties: map[string]interface{}{"size": "1001"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorGreaterThanOrEqual, Value: 1000}},
			expected:   true,
		},
		{
			name:       "GreaterThanOrEqual below false",
			properties: map[string]interface{}{"size": "999"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorGreaterThanOrEqual, Value: 1000}},
			expected:   false,
		},
		// LessThanOrEqual
		{
			name:       "LessThanOrEqual boundary true",
			properties: map[string]interface{}{"size": "1000"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorLessThanOrEqual, Value: 1000}},
			expected:   true,
		},
		{
			name:       "LessThanOrEqual below true",
			properties: map[string]interface{}{"size": "999"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorLessThanOrEqual, Value: 1000}},
			expected:   true,
		},
		{
			name:       "LessThanOrEqual above false",
			properties: map[string]interface{}{"size": "1001"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorLessThanOrEqual, Value: 1000}},
			expected:   false,
		},
		// Non-numeric value
		{
			name:       "Non-numeric string returns false for GreaterThan",
			properties: map[string]interface{}{"size": "not-a-number"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1000}},
			expected:   false,
		},
		{
			name:       "Non-numeric string returns false for LessThan",
			properties: map[string]interface{}{"size": "abc"},
			queries:    []Query{{Field: "size", Operator: QueryOperatorLessThan, Value: 1000}},
			expected:   false,
		},
		// Missing field
		{
			name:       "Missing field returns false for GreaterThan",
			properties: map[string]interface{}{},
			queries:    []Query{{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1000}},
			expected:   false,
		},
		{
			name:       "Missing field returns false for LessThanOrEqual",
			properties: map[string]interface{}{},
			queries:    []Query{{Field: "size", Operator: QueryOperatorLessThanOrEqual, Value: 1000}},
			expected:   false,
		},
		// Float values
		{
			name:       "Float GreaterThan",
			properties: map[string]interface{}{"score": "0.95"},
			queries:    []Query{{Field: "score", Operator: QueryOperatorGreaterThan, Value: 0.5}},
			expected:   true,
		},
		{
			name:       "Float LessThan",
			properties: map[string]interface{}{"score": "0.3"},
			queries:    []Query{{Field: "score", Operator: QueryOperatorLessThan, Value: 0.5}},
			expected:   true,
		},
		// Multiple queries combined
		{
			name:       "Multiple numeric queries all match",
			properties: map[string]interface{}{"size": "500", "count": "10"},
			queries: []Query{
				{Field: "size", Operator: QueryOperatorGreaterThan, Value: 100},
				{Field: "count", Operator: QueryOperatorLessThanOrEqual, Value: 10},
			},
			expected: true,
		},
		{
			name:       "Multiple numeric queries one fails",
			properties: map[string]interface{}{"size": "500", "count": "20"},
			queries: []Query{
				{Field: "size", Operator: QueryOperatorGreaterThan, Value: 100},
				{Field: "count", Operator: QueryOperatorLessThanOrEqual, Value: 10},
			},
			expected: false,
		},
		// Empty queries
		{
			name:       "No queries matches everything",
			properties: map[string]interface{}{"size": "500"},
			queries:    []Query{},
			expected:   true,
		},
		// ContainsAny / ContainsAll
		{
			name:       "ContainsAny true with JSON array property",
			properties: map[string]interface{}{"tags": "[\"red\",\"blue\"]"},
			queries:    []Query{{Field: "tags", Operator: QueryOperatorContainsAny, Value: []interface{}{"green", "blue"}}},
			expected:   true,
		},
		{
			name:       "ContainsAny false with JSON array property",
			properties: map[string]interface{}{"tags": "[\"red\",\"blue\"]"},
			queries:    []Query{{Field: "tags", Operator: QueryOperatorContainsAny, Value: []interface{}{"green", "yellow"}}},
			expected:   false,
		},
		{
			name:       "ContainsAll true with JSON array property",
			properties: map[string]interface{}{"tags": "[\"red\",\"blue\",\"green\"]"},
			queries:    []Query{{Field: "tags", Operator: QueryOperatorContainsAll, Value: []interface{}{"red", "green"}}},
			expected:   true,
		},
		{
			name:       "ContainsAll false with JSON array property",
			properties: map[string]interface{}{"tags": "[\"red\",\"blue\"]"},
			queries:    []Query{{Field: "tags", Operator: QueryOperatorContainsAll, Value: []interface{}{"red", "green"}}},
			expected:   false,
		},
		{
			name:       "ContainsAny true with scalar string property",
			properties: map[string]interface{}{"tags": "red"},
			queries:    []Query{{Field: "tags", Operator: QueryOperatorContainsAny, Value: []interface{}{"red", "green"}}},
			expected:   true,
		},
		{
			name:       "ContainsAll false with scalar string property",
			properties: map[string]interface{}{"tags": "red"},
			queries:    []Query{{Field: "tags", Operator: QueryOperatorContainsAll, Value: []interface{}{"red", "green"}}},
			expected:   false,
		},
		{
			name:       "ContainsAny malformed query value returns false",
			properties: map[string]interface{}{"tags": "[\"red\",\"blue\"]"},
			queries:    []Query{{Field: "tags", Operator: QueryOperatorContainsAny, Value: "red"}},
			expected:   false,
		},
		{
			name:       "ContainsAll missing field returns false",
			properties: map[string]interface{}{},
			queries:    []Query{{Field: "tags", Operator: QueryOperatorContainsAll, Value: []interface{}{"red"}}},
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchesQueriesForScan(tt.properties, tt.queries)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ============================================================================
// INTEGRATION TESTS (require real Redis instance with RediSearch)
// ============================================================================

func TestRedisStore_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewRedisTestSetup(t)
	defer setup.Cleanup(t)

	t.Run("Add and GetChunk", func(t *testing.T) {
		testKey := generateUUID()
		embedding := generateTestEmbedding(RedisTestDimension)
		metadata := map[string]interface{}{
			"type":   "document",
			"size":   1024,
			"public": true,
		}

		// Add object
		err := setup.Store.Add(setup.ctx, TestNamespace, testKey, embedding, metadata)
		require.NoError(t, err)

		// Small delay to ensure consistency
		time.Sleep(100 * time.Millisecond)

		// Get single chunk
		result, err := setup.Store.GetChunk(setup.ctx, TestNamespace, testKey)
		require.NoError(t, err)
		assert.NotEmpty(t, result)
		assert.Equal(t, "document", result.Properties["type"]) // Should contain metadata
	})

	t.Run("Add without embedding", func(t *testing.T) {
		testKey := generateUUID()
		metadata := map[string]interface{}{
			"type": "metadata-only",
		}

		// Add object without embedding
		err := setup.Store.Add(setup.ctx, TestNamespace, testKey, nil, metadata)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Retrieve it
		result, err := setup.Store.GetChunk(setup.ctx, TestNamespace, testKey)
		require.NoError(t, err)
		assert.Equal(t, "metadata-only", result.Properties["type"])
	})

	t.Run("GetChunks batch retrieval", func(t *testing.T) {
		// Add multiple objects
		keys := []string{generateUUID(), generateUUID(), generateUUID()}
		embeddings := [][]float32{
			generateTestEmbedding(RedisTestDimension),
			generateTestEmbedding(RedisTestDimension),
			nil,
		}
		metadata := []map[string]interface{}{
			{"type": "doc1", "size": 100},
			{"type": "doc2", "size": 200},
			{"type": "doc3", "size": 300},
		}

		for i, key := range keys {
			emb := embeddings[i]
			err := setup.Store.Add(setup.ctx, TestNamespace, key, emb, metadata[i])
			require.NoError(t, err)
		}

		time.Sleep(100 * time.Millisecond)

		// Get all chunks
		results, err := setup.Store.GetChunks(setup.ctx, TestNamespace, keys)
		require.NoError(t, err)
		assert.Len(t, results, 3)

		// Verify each result
		for i, result := range results {
			assert.Equal(t, keys[i], result.ID)
			assert.Equal(t, metadata[i]["type"], result.Properties["type"])
		}
	})
}

func TestRedisStore_FilteringScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewRedisTestSetup(t)
	defer setup.Cleanup(t)

	// Setup test data for filtering scenarios
	testData := []struct {
		key      string
		metadata map[string]interface{}
	}{
		{
			generateUUID(),
			map[string]interface{}{
				"type":   "pdf",
				"size":   1024,
				"public": true,
				"author": "alice",
			},
		},
		{
			generateUUID(),
			map[string]interface{}{
				"type":   "docx",
				"size":   2048,
				"public": false,
				"author": "bob",
			},
		},
		{
			generateUUID(),
			map[string]interface{}{
				"type":   "pdf",
				"size":   512,
				"public": true,
				"author": "alice",
			},
		},
		{
			generateUUID(),
			map[string]interface{}{
				"type":   "txt",
				"size":   256,
				"public": true,
				"author": "charlie",
			},
		},
	}

	filterFields := []string{"type", "size", "public", "author"}

	// Add all test data
	for _, item := range testData {
		embedding := generateTestEmbedding(RedisTestDimension)
		err := setup.Store.Add(setup.ctx, TestNamespace, item.key, embedding, item.metadata)
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond) // Wait for consistency

	t.Run("Filter by numeric comparison", func(t *testing.T) {
		queries := []Query{
			{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1000},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, TestNamespace, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1 (1024) and doc2 (2048)
	})

	t.Run("Filter by boolean", func(t *testing.T) {
		queries := []Query{
			{Field: "public", Operator: QueryOperatorEqual, Value: true},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, TestNamespace, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 3) // doc1, doc3, doc4
	})

	t.Run("Multiple filters (AND)", func(t *testing.T) {
		queries := []Query{
			{Field: "type", Operator: QueryOperatorEqual, Value: "pdf"},
			{Field: "public", Operator: QueryOperatorEqual, Value: true},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, TestNamespace, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1 and doc3
	})

	t.Run("Complex multi-condition filter", func(t *testing.T) {
		queries := []Query{
			{Field: "author", Operator: QueryOperatorEqual, Value: "alice"},
			{Field: "size", Operator: QueryOperatorLessThan, Value: 2000},
			{Field: "public", Operator: QueryOperatorEqual, Value: true},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, TestNamespace, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1 and doc3 (both by alice, < 2000 size, public)
	})

	t.Run("Pagination test", func(t *testing.T) {
		// Test with limit of 2
		results, cursor, err := setup.Store.GetAll(setup.ctx, TestNamespace, nil, filterFields, nil, 2)
		require.NoError(t, err)
		assert.Len(t, results, 2)

		if cursor != nil {
			// Get next page
			nextResults, _, err := setup.Store.GetAll(setup.ctx, TestNamespace, nil, filterFields, cursor, 2)
			require.NoError(t, err)
			assert.LessOrEqual(t, len(nextResults), 2)
			t.Logf("First page: %d results, Next page: %d results", len(results), len(nextResults))
		}
	})
}

func TestRedisStore_VectorSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewRedisTestSetup(t)
	defer setup.Cleanup(t)

	// Add test documents with embeddings
	testDocs := []struct {
		key       string
		embedding []float32
		metadata  map[string]interface{}
	}{
		{
			generateUUID(),
			generateTestEmbedding(RedisTestDimension),
			map[string]interface{}{
				"type":     "tech",
				"category": "programming",
				"content":  "Go programming language",
			},
		},
		{
			generateUUID(),
			generateTestEmbedding(RedisTestDimension),
			map[string]interface{}{
				"type":     "tech",
				"category": "programming",
				"content":  "Python programming language",
			},
		},
		{
			generateUUID(),
			generateTestEmbedding(RedisTestDimension),
			map[string]interface{}{
				"type":     "sports",
				"category": "football",
				"content":  "Football match results",
			},
		},
	}

	for _, doc := range testDocs {
		err := setup.Store.Add(setup.ctx, TestNamespace, doc.key, doc.embedding, doc.metadata)
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond)

	t.Run("Vector similarity search", func(t *testing.T) {
		// Search for similar content to the first document
		queryEmbedding := testDocs[0].embedding
		results, err := setup.Store.GetNearest(setup.ctx, TestNamespace, queryEmbedding, nil, []string{"type", "category", "content"}, 0.1, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(results), 1)

		// Check that results have scores and are not nil
		require.NotEmpty(t, results)
		require.NotNil(t, results[0].Score)
		assert.InDelta(t, 1.0, *results[0].Score, 1e-4)
	})

	t.Run("Vector search with metadata filters", func(t *testing.T) {
		// Search for tech content only
		queries := []Query{
			{Field: "type", Operator: QueryOperatorEqual, Value: "tech"},
		}

		queryEmbedding := testDocs[0].embedding
		results, err := setup.Store.GetNearest(setup.ctx, TestNamespace, queryEmbedding, queries, []string{"type", "category", "content"}, 0.1, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(results), 1)

		// All results should be tech type
		for _, result := range results {
			assert.Equal(t, "tech", result.Properties["type"])
		}
	})

	t.Run("Vector search with threshold", func(t *testing.T) {
		// Use a very high threshold to get only very similar results
		queryEmbedding := testDocs[0].embedding
		results, err := setup.Store.GetNearest(setup.ctx, TestNamespace, queryEmbedding, nil, []string{"type", "category", "content"}, 0.99, 10)
		require.NoError(t, err)
		// Should return fewer results due to high threshold
		t.Logf("High threshold search returned %d results", len(results))
	})
}

func TestRedisStore_CompleteUseCases(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewRedisTestSetup(t)
	defer setup.Cleanup(t)

	t.Run("Document Storage & Retrieval Scenario", func(t *testing.T) {
		// Add documents with different types
		documents := []struct {
			key       string
			embedding []float32
			metadata  map[string]interface{}
		}{
			{
				generateUUID(),
				generateTestEmbedding(RedisTestDimension),
				map[string]interface{}{"type": "pdf", "size": 1024, "public": true},
			},
			{
				generateUUID(),
				generateTestEmbedding(RedisTestDimension),
				map[string]interface{}{"type": "docx", "size": 2048, "public": false},
			},
			{
				generateUUID(),
				generateTestEmbedding(RedisTestDimension),
				map[string]interface{}{"type": "pdf", "size": 512, "public": true},
			},
		}

		filterFields := []string{"type", "size", "public"}

		for _, doc := range documents {
			err := setup.Store.Add(setup.ctx, TestNamespace, doc.key, doc.embedding, doc.metadata)
			require.NoError(t, err)
		}

		time.Sleep(300 * time.Millisecond)

		// Test various retrieval patterns

		// Get PDF documents
		pdfQuery := []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "pdf"}}
		results, _, err := setup.Store.GetAll(setup.ctx, TestNamespace, pdfQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1, doc3

		// Get large documents (size > 1000)
		sizeQuery := []Query{{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1000}}
		results, _, err = setup.Store.GetAll(setup.ctx, TestNamespace, sizeQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1, doc2

		// Get public PDFs
		combinedQuery := []Query{
			{Field: "public", Operator: QueryOperatorEqual, Value: true},
			{Field: "type", Operator: QueryOperatorEqual, Value: "pdf"},
		}
		results, _, err = setup.Store.GetAll(setup.ctx, TestNamespace, combinedQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1, doc3

		// Vector similarity search
		queryEmbedding := documents[0].embedding // Similar to doc1
		vectorResults, err := setup.Store.GetNearest(setup.ctx, TestNamespace, queryEmbedding, nil, filterFields, 0.8, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(vectorResults), 1)
	})

	t.Run("Semantic Cache-like Workflow", func(t *testing.T) {
		// Add request-response pairs with parameters
		cacheEntries := []struct {
			key       string
			embedding []float32
			metadata  map[string]interface{}
		}{
			{
				generateUUID(),
				generateTestEmbedding(RedisTestDimension),
				map[string]interface{}{
					"request_hash":                       "abc123",
					"user":                               "u1",
					"lang":                               "en",
					"response":                           "answer1",
					"from_bifrost_semantic_cache_plugin": true,
				},
			},
			{
				generateUUID(),
				generateTestEmbedding(RedisTestDimension),
				map[string]interface{}{
					"request_hash":                       "def456",
					"user":                               "u1",
					"lang":                               "es",
					"response":                           "answer2",
					"from_bifrost_semantic_cache_plugin": true,
				},
			},
		}

		filterFields := []string{"request_hash", "user", "lang", "response", "from_bifrost_semantic_cache_plugin"}

		for _, entry := range cacheEntries {
			err := setup.Store.Add(setup.ctx, TestNamespace, entry.key, entry.embedding, entry.metadata)
			require.NoError(t, err)
		}

		time.Sleep(300 * time.Millisecond)

		// Test hash-based direct retrieval (exact match)
		hashQuery := []Query{{Field: "request_hash", Operator: QueryOperatorEqual, Value: "abc123"}}
		results, _, err := setup.Store.GetAll(setup.ctx, TestNamespace, hashQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 1)

		// Test semantic search with user and language filters
		userLangFilter := []Query{
			{Field: "user", Operator: QueryOperatorEqual, Value: "u1"},
			{Field: "lang", Operator: QueryOperatorEqual, Value: "en"},
		}
		similarEmbedding := generateSimilarEmbedding(cacheEntries[0].embedding, 0.9)
		vectorResults, err := setup.Store.GetNearest(setup.ctx, TestNamespace, similarEmbedding, userLangFilter, filterFields, 0.7, 10)
		require.NoError(t, err)
		assert.Len(t, vectorResults, 1) // Should find English content for u1
	})
}

func TestRedisStore_DeleteOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewRedisTestSetup(t)
	defer setup.Cleanup(t)

	t.Run("Delete single item", func(t *testing.T) {
		// Add an item
		key := generateUUID()
		embedding := generateTestEmbedding(RedisTestDimension)
		metadata := map[string]interface{}{"type": "test", "value": "delete_me"}

		err := setup.Store.Add(setup.ctx, TestNamespace, key, embedding, metadata)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Verify it exists
		result, err := setup.Store.GetChunk(setup.ctx, TestNamespace, key)
		require.NoError(t, err)
		assert.Equal(t, "test", result.Properties["type"])

		// Delete it
		err = setup.Store.Delete(setup.ctx, TestNamespace, key)
		require.NoError(t, err)

		// Verify it's gone
		_, err = setup.Store.GetChunk(setup.ctx, TestNamespace, key)
		assert.Error(t, err)
	})

	t.Run("DeleteAll with filters", func(t *testing.T) {
		// Add multiple items with different types
		testItems := []struct {
			key       string
			embedding []float32
			metadata  map[string]interface{}
		}{
			{
				generateUUID(),
				generateTestEmbedding(RedisTestDimension),
				map[string]interface{}{"type": "delete_me", "category": "test"},
			},
			{
				generateUUID(),
				generateTestEmbedding(RedisTestDimension),
				map[string]interface{}{"type": "delete_me", "category": "test"},
			},
			{
				generateUUID(),
				generateTestEmbedding(RedisTestDimension),
				map[string]interface{}{"type": "keep_me", "category": "test"},
			},
		}

		for _, item := range testItems {
			err := setup.Store.Add(setup.ctx, TestNamespace, item.key, item.embedding, item.metadata)
			require.NoError(t, err)
		}

		time.Sleep(300 * time.Millisecond)

		// Delete all items with type "delete_me"
		queries := []Query{
			{Field: "type", Operator: QueryOperatorEqual, Value: "delete_me"},
		}

		deleteResults, err := setup.Store.DeleteAll(setup.ctx, TestNamespace, queries)
		require.NoError(t, err)
		assert.Len(t, deleteResults, 2) // Should delete 2 items

		// Verify only "keep_me" items remain
		allResults, _, err := setup.Store.GetAll(setup.ctx, TestNamespace, nil, []string{"type"}, nil, 10)
		require.NoError(t, err)
		assert.Len(t, allResults, 1) // Only the "keep_me" item should remain
		assert.Equal(t, "keep_me", allResults[0].Properties["type"])
	})
}

// ============================================================================
// INTERFACE COMPLIANCE TESTS
// ============================================================================

func TestRedisStore_InterfaceCompliance(t *testing.T) {
	// Verify that RedisStore implements VectorStore interface
	var _ VectorStore = (*RedisStore)(nil)
}

func TestVectorStoreFactory_Redis(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	config := &Config{
		Enabled: true,
		Type:    VectorStoreTypeRedis,
		Config: RedisConfig{
			Addr:     schemas.NewEnvVar(getEnvWithDefault("REDIS_ADDR", DefaultTestAddr)),
			Username: schemas.NewEnvVar("env.REDIS_USERNAME"),
			Password: schemas.NewEnvVar("env.REDIS_PASSWORD"),
		},
	}

	store, err := NewVectorStore(context.Background(), config, logger)
	if err != nil {
		t.Skipf("Could not create Redis store: %v", err)
	}
	defer store.Close(context.Background(), TestNamespace)

	// Verify it's actually a RedisStore
	redisStore, ok := store.(*RedisStore)
	assert.True(t, ok)
	assert.NotNil(t, redisStore)
}

// ============================================================================
// ERROR HANDLING TESTS
// ============================================================================

func TestRedisStore_ErrorHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewRedisTestSetup(t)
	defer setup.Cleanup(t)

	t.Run("GetChunk with non-existent key", func(t *testing.T) {
		_, err := setup.Store.GetChunk(setup.ctx, TestNamespace, "non-existent-key")
		assert.Error(t, err)
	})

	t.Run("Delete non-existent key", func(t *testing.T) {
		err := setup.Store.Delete(setup.ctx, TestNamespace, "non-existent-key")
		assert.Error(t, err)
	})

	t.Run("Add with empty ID", func(t *testing.T) {
		embedding := generateTestEmbedding(RedisTestDimension)
		metadata := map[string]interface{}{"type": "test"}

		err := setup.Store.Add(setup.ctx, TestNamespace, "", embedding, metadata)
		assert.Error(t, err)
	})

	t.Run("GetNearest with empty namespace", func(t *testing.T) {
		embedding := generateTestEmbedding(RedisTestDimension)
		_, err := setup.Store.GetNearest(setup.ctx, "", embedding, nil, []string{}, 0.8, 10)
		assert.Error(t, err)
	})
}

func TestRedisStore_NamespaceDimensionHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewRedisTestSetup(t)
	defer setup.Cleanup(t)

	testNamespace := "TestDimensionHandling"

	t.Run("Recreate namespace with different dimension should not crash", func(t *testing.T) {
		properties := map[string]VectorStoreProperties{
			"type": {DataType: VectorStorePropertyTypeString},
			"test": {DataType: VectorStorePropertyTypeString},
		}

		// Step 1: Create namespace with dimension 512
		err := setup.Store.CreateNamespace(setup.ctx, testNamespace, 512, properties)
		require.NoError(t, err)

		// Add a document with 512-dimensional embedding
		embedding512 := generateTestEmbedding(512)
		metadata := map[string]interface{}{
			"type": "test_doc",
			"test": "dimension_512",
		}

		err = setup.Store.Add(setup.ctx, testNamespace, "test-key-512", embedding512, metadata)
		require.NoError(t, err)

		// Verify it was added
		result, err := setup.Store.GetChunk(setup.ctx, testNamespace, "test-key-512")
		require.NoError(t, err)
		assert.Equal(t, "dimension_512", result.Properties["test"])

		// Step 2: Delete the namespace
		err = setup.Store.DeleteNamespace(setup.ctx, testNamespace)
		require.NoError(t, err)

		// Step 3: Create namespace with same name but different dimension - should not crash
		err = setup.Store.CreateNamespace(setup.ctx, testNamespace, 1024, properties)
		require.NoError(t, err)

		// Add a document with 1024-dimensional embedding
		embedding1024 := generateTestEmbedding(1024)
		metadata1024 := map[string]interface{}{
			"type": "test_doc",
			"test": "dimension_1024",
		}

		err = setup.Store.Add(setup.ctx, testNamespace, "test-key-1024", embedding1024, metadata1024)
		require.NoError(t, err)

		// Verify new document exists
		result, err = setup.Store.GetChunk(setup.ctx, testNamespace, "test-key-1024")
		require.NoError(t, err)
		assert.Equal(t, "dimension_1024", result.Properties["test"])

		// Verify vector search works with new dimension
		vectorResults, err := setup.Store.GetNearest(setup.ctx, testNamespace, embedding1024, nil, []string{"type", "test"}, 0.8, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(vectorResults), 1)
		assert.NotNil(t, vectorResults[0].Score)

		// Cleanup
		err = setup.Store.DeleteNamespace(setup.ctx, testNamespace)
		if err != nil {
			t.Logf("Warning: Failed to cleanup namespace: %v", err)
		}
	})
}
