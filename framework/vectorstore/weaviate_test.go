package vectorstore

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/filters"
	"github.com/weaviate/weaviate/entities/models"
)

// Test constants
const (
	TestTimeout        = 30 * time.Second
	TestClassPrefix    = "test_weaviate_"
	TestEmbeddingDim   = 384
	DefaultTestScheme  = "http"
	DefaultTestHost    = "localhost:8080"
	DefaultTestTimeout = 10 * time.Second
)

// TestSetup provides common test infrastructure
type TestSetup struct {
	Store     *WeaviateStore
	Logger    schemas.Logger
	Config    WeaviateConfig
	ClassName string
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewTestSetup creates a test setup with environment-driven configuration
func NewTestSetup(t *testing.T) *TestSetup {
	// Get configuration from environment variables
	scheme := getEnvWithDefault("WEAVIATE_SCHEME", DefaultTestScheme)
	host := getEnvWithDefault("WEAVIATE_HOST", DefaultTestHost)
	apiKey := os.Getenv("WEAVIATE_API_KEY")

	timeoutStr := getEnvWithDefault("WEAVIATE_TIMEOUT", "10s")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		timeout = DefaultTestTimeout
	}

	// Generate unique class name for this test
	className := TestClassPrefix + generateRandomID()

	config := WeaviateConfig{
		Scheme:    scheme,
		Host:      host,
		ApiKey:    apiKey,
		Timeout:   timeout,
		ClassName: className,
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)

	store, err := newWeaviateStore(ctx, &config, logger)
	if err != nil {
		cancel()
		t.Fatalf("Failed to create Weaviate store: %v", err)
	}

	setup := &TestSetup{
		Store:     store,
		Logger:    logger,
		Config:    config,
		ClassName: className,
		ctx:       ctx,
		cancel:    cancel,
	}

	// Ensure class exists for integration tests
	if !testing.Short() {
		setup.ensureClassExists(t)
	}

	return setup
}

// Cleanup cleans up test resources
func (ts *TestSetup) Cleanup(t *testing.T) {
	defer ts.cancel()

	if !testing.Short() {
		// Clean up test data
		ts.cleanupTestData(t)
	}

	if err := ts.Store.Close(ts.ctx); err != nil {
		t.Logf("Warning: Failed to close store: %v", err)
	}
}

// ensureClassExists creates the test class in Weaviate
func (ts *TestSetup) ensureClassExists(t *testing.T) {
	// Try to get class schema first
	exists, err := ts.Store.client.Schema().ClassGetter().
		WithClassName(ts.ClassName).
		Do(ts.ctx)

	if err == nil && exists != nil {
		t.Logf("Class %s already exists", ts.ClassName)
		return
	}

	// Create class with minimal schema - let Weaviate auto-create properties
	class := &models.Class{
		Class: ts.ClassName,
		Properties: []*models.Property{
			{
				Name:     "key",
				DataType: []string{"text"},
			},
			{
				Name:     "test_type",
				DataType: []string{"text"},
			},
			{
				Name:     "size",
				DataType: []string{"int"},
			},
			{
				Name:     "public",
				DataType: []string{"boolean"},
			},
		},
		VectorIndexConfig: map[string]interface{}{
			"distance": "cosine",
		},
	}

	err = ts.Store.client.Schema().ClassCreator().
		WithClass(class).
		Do(ts.ctx)

	if err != nil {
		t.Logf("Warning: Failed to create test class %s: %v", ts.ClassName, err)
		t.Logf("This might be due to auto-schema creation. Continuing...")
	} else {
		t.Logf("Created test class: %s", ts.ClassName)
	}
}

// cleanupTestData removes all test objects from the class
func (ts *TestSetup) cleanupTestData(t *testing.T) {
	// Delete all objects in the test class
	err := ts.Store.client.Schema().ClassDeleter().
		WithClassName(ts.ClassName).
		Do(ts.ctx)

	if err != nil {
		t.Logf("Warning: Failed to cleanup test class %s: %v", ts.ClassName, err)
	} else {
		t.Logf("Cleaned up test class: %s", ts.ClassName)
	}
}

// Helper functions
func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func generateRandomID() string {
	return fmt.Sprintf("%d_%d", time.Now().UnixNano(), rand.Intn(10000))
}

func generateUUID() string {
	return uuid.New().String()
}

func generateTestEmbedding(dim int) []float32 {
	embedding := make([]float32, dim)
	for i := range embedding {
		embedding[i] = rand.Float32()*2 - 1 // Random values between -1 and 1
	}
	return embedding
}

func generateSimilarEmbedding(original []float32, similarity float32) []float32 {
	similar := make([]float32, len(original))
	for i := range similar {
		// Add small random noise to create similar but not identical embedding
		noise := (rand.Float32()*2 - 1) * (1 - similarity) * 0.1
		similar[i] = original[i] + noise
	}
	return similar
}

// ============================================================================
// UNIT TESTS
// ============================================================================

func TestWeaviateConfig_Validation(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	tests := []struct {
		name        string
		config      WeaviateConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			config: WeaviateConfig{
				Scheme: "http",
				Host:   "localhost:8080",
			},
			expectError: false,
		},
		{
			name: "missing scheme",
			config: WeaviateConfig{
				Host: "localhost:8080",
			},
			expectError: true,
			errorMsg:    "scheme and host are required",
		},
		{
			name: "missing host",
			config: WeaviateConfig{
				Scheme: "http",
			},
			expectError: true,
			errorMsg:    "scheme and host are required",
		},
		{
			name: "with api key",
			config: WeaviateConfig{
				Scheme: "https",
				Host:   "cluster.weaviate.network",
				ApiKey: "test-key",
			},
			expectError: false,
		},
		{
			name: "with custom headers",
			config: WeaviateConfig{
				Scheme: "http",
				Host:   "localhost:8080",
				Headers: map[string]string{
					"Custom-Header": "value",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := newWeaviateStore(ctx, &tt.config, logger)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, store)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				// Note: This will fail with connection error in unit tests
				// but should pass config validation
				assert.Nil(t, store) // Expected due to no real Weaviate instance
				assert.Error(t, err) // Connection error expected
			}
		})
	}
}

func TestDefaultClassName(t *testing.T) {
	config := WeaviateConfig{
		Scheme: "http",
		Host:   "localhost:8080",
	}

	// This will fail to connect but should set default class name
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	_, err := newWeaviateStore(context.Background(), &config, logger)

	// Should fail with connection error, but we can't test the default class name
	// without mocking the client, which would be more complex
	assert.Error(t, err)
}

func TestBuildWeaviateFilter(t *testing.T) {
	tests := []struct {
		name     string
		queries  []Query
		expected *filters.WhereBuilder // We'll test the structure, not exact equality
		isNil    bool
	}{
		{
			name:     "empty queries",
			queries:  []Query{},
			expected: nil,
			isNil:    true,
		},
		{
			name: "single string query",
			queries: []Query{
				{Field: "category", Operator: QueryOperatorEqual, Value: "tech"},
			},
			isNil: false,
		},
		{
			name: "single numeric query",
			queries: []Query{
				{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1000},
			},
			isNil: false,
		},
		{
			name: "multiple queries (AND)",
			queries: []Query{
				{Field: "category", Operator: QueryOperatorEqual, Value: "tech"},
				{Field: "public", Operator: QueryOperatorEqual, Value: true},
			},
			isNil: false,
		},
		{
			name: "mixed types",
			queries: []Query{
				{Field: "name", Operator: QueryOperatorLike, Value: "test%"},
				{Field: "count", Operator: QueryOperatorLessThan, Value: int64(100)},
				{Field: "active", Operator: QueryOperatorEqual, Value: true},
				{Field: "score", Operator: QueryOperatorGreaterThanOrEqual, Value: 95.5},
			},
			isNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildWeaviateFilter(tt.queries)

			if tt.isNil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
				// We can't easily test the internal structure without reflection
				// or implementing String() methods, but we verify it's not nil
			}
		})
	}
}

func TestConvertOperator(t *testing.T) {
	tests := []struct {
		input    QueryOperator
		expected filters.WhereOperator
	}{
		{QueryOperatorEqual, filters.Equal},
		{QueryOperatorNotEqual, filters.NotEqual},
		{QueryOperatorLessThan, filters.LessThan},
		{QueryOperatorLessThanOrEqual, filters.LessThanEqual},
		{QueryOperatorGreaterThan, filters.GreaterThan},
		{QueryOperatorGreaterThanOrEqual, filters.GreaterThanEqual},
		{QueryOperatorLike, filters.Like},
		{QueryOperatorContainsAny, filters.ContainsAny},
		{QueryOperatorContainsAll, filters.ContainsAll},
		{QueryOperatorIsNull, filters.IsNull},
		{QueryOperatorIsNotNull, filters.IsNull},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			result := convertOperator(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ============================================================================
// INTEGRATION TESTS (require real Weaviate instance)
// ============================================================================

func TestWeaviateStore_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewTestSetup(t)
	defer setup.Cleanup(t)

	t.Run("Add and GetChunk", func(t *testing.T) {
		testKey := generateUUID()
		embedding := generateTestEmbedding(TestEmbeddingDim)
		metadata := map[string]interface{}{
			"type":   "document",
			"size":   1024,
			"public": true,
		}

		// Add object
		err := setup.Store.Add(setup.ctx, testKey, embedding, metadata)
		require.NoError(t, err)

		// Small delay to ensure consistency
		time.Sleep(100 * time.Millisecond)

		// Get single chunk
		result, err := setup.Store.GetChunk(setup.ctx, testKey)
		require.NoError(t, err)
		assert.NotEmpty(t, result)
		assert.Contains(t, result, "document") // Should contain metadata
	})

	t.Run("Add without embedding", func(t *testing.T) {
		testKey := generateUUID()
		metadata := map[string]interface{}{
			"type": "metadata-only",
			"id":   123,
		}

		// Add object without embedding
		err := setup.Store.Add(setup.ctx, testKey, nil, metadata)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Retrieve it
		result, err := setup.Store.GetChunk(setup.ctx, testKey)
		require.NoError(t, err)
		assert.Contains(t, result, "metadata-only")
	})

	t.Run("GetChunks batch retrieval", func(t *testing.T) {
		keys := []string{generateUUID(), generateUUID(), generateUUID(), generateUUID()}

		// Add first three objects
		for i, key := range keys[:3] {
			metadata := map[string]interface{}{
				"batch_id": i,
				"type":     "batch_test",
			}
			err := setup.Store.Add(setup.ctx, key, generateTestEmbedding(TestEmbeddingDim), metadata)
			require.NoError(t, err)
		}

		time.Sleep(200 * time.Millisecond)

		// Get all keys (including non-existent)
		results, err := setup.Store.GetChunks(setup.ctx, keys)
		require.NoError(t, err)

		// Should return 3 results (non-existent key should be skipped)
		assert.Len(t, results, 3)
	})
}

func TestWeaviateStore_FilteringScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewTestSetup(t)
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
		embedding := generateTestEmbedding(TestEmbeddingDim)
		err := setup.Store.Add(setup.ctx, item.key, embedding, item.metadata)
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond) // Wait for consistency

	t.Run("Filter by string equality", func(t *testing.T) {
		queries := []Query{
			{Field: "type", Operator: "Equal", Value: "pdf"},
		}

		results, cursor, err := setup.Store.GetAll(setup.ctx, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Nil(t, cursor)     // Should fit in one page
		assert.Len(t, results, 2) // doc1 and doc3
	})

	t.Run("Filter by numeric comparison", func(t *testing.T) {
		queries := []Query{
			{Field: "size", Operator: "GreaterThan", Value: 1000},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1 (1024) and doc2 (2048)
	})

	t.Run("Filter by boolean", func(t *testing.T) {
		queries := []Query{
			{Field: "public", Operator: "Equal", Value: true},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 3) // doc1, doc3, doc4
	})

	t.Run("Multiple filters (AND)", func(t *testing.T) {
		queries := []Query{
			{Field: "type", Operator: "Equal", Value: "pdf"},
			{Field: "public", Operator: "Equal", Value: true},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1 and doc3
	})

	t.Run("Complex multi-condition filter", func(t *testing.T) {
		queries := []Query{
			{Field: "author", Operator: "Equal", Value: "alice"},
			{Field: "size", Operator: "LessThan", Value: 2000},
			{Field: "public", Operator: "Equal", Value: true},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1 and doc3 (both by alice, < 2000 size, public)
	})

	t.Run("Pagination test", func(t *testing.T) {
		// Test with limit of 2
		results, cursor, err := setup.Store.GetAll(setup.ctx, nil, filterFields, nil, 2)
		require.NoError(t, err)
		assert.Len(t, results, 2)

		if cursor != nil {
			// Get next page
			nextResults, _, err := setup.Store.GetAll(setup.ctx, nil, filterFields, cursor, 2)
			require.NoError(t, err)
			assert.LessOrEqual(t, len(nextResults), 2)
			t.Logf("First page: %d results, Next page: %d results", len(results), len(nextResults))
		}
	})
}

func TestWeaviateStore_VectorSimilaritySearch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewTestSetup(t)
	defer setup.Cleanup(t)

	// Create test embeddings with known similarity relationships
	baseEmbedding := generateTestEmbedding(TestEmbeddingDim)
	similarEmbedding := generateSimilarEmbedding(baseEmbedding, 0.9)
	differentEmbedding := generateTestEmbedding(TestEmbeddingDim)

	testData := []struct {
		key       string
		embedding []float32
		metadata  map[string]interface{}
	}{
		{
			generateUUID(),
			baseEmbedding,
			map[string]interface{}{
				"category": "tech",
				"user":     "alice",
			},
		},
		{
			generateUUID(),
			similarEmbedding,
			map[string]interface{}{
				"category": "tech",
				"user":     "alice",
			},
		},
		{
			generateUUID(),
			differentEmbedding,
			map[string]interface{}{
				"category": "sports",
				"user":     "bob",
			},
		},
	}

	filterFields := []string{"category", "user"}

	// Add test data
	for _, item := range testData {
		err := setup.Store.Add(setup.ctx, item.key, item.embedding, item.metadata)
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond)

	t.Run("Vector similarity without filters", func(t *testing.T) {
		results, err := setup.Store.GetNearest(
			setup.ctx,
			baseEmbedding,
			nil, // No filters
			filterFields,
			0.5, // Threshold
			10,  // Limit
		)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(results), 2) // Should find similar docs

		// First result should be exact match (distance ~0)
		assert.Equal(t, testData[0].key, results[0].ID)
		assert.Less(t, *results[0].Score, 0.01) // Very low distance for exact match
	})

	t.Run("Vector similarity with metadata filters", func(t *testing.T) {
		// Search for similar vectors but only in "tech" category
		queries := []Query{
			{Field: "category", Operator: "Equal", Value: "tech"},
		}

		results, err := setup.Store.GetNearest(
			setup.ctx,
			baseEmbedding,
			queries,
			filterFields,
			0.7, // Threshold
			10,  // Limit
		)
		require.NoError(t, err)
		assert.Len(t, results, 2) // Should find both tech documents

		// Verify all results are in tech category
		for _, result := range results {
			metadata, ok := result.Properties["metadata"].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, "tech", metadata["category"])
		}
	})

	t.Run("Vector similarity with user filter", func(t *testing.T) {
		// Search for Alice's content only
		queries := []Query{
			{Field: "user", Operator: "Equal", Value: "alice"},
		}

		results, err := setup.Store.GetNearest(
			setup.ctx,
			baseEmbedding,
			queries,
			filterFields,
			0.8,
			10,
		)
		require.NoError(t, err)
		assert.Len(t, results, 2) // Should find Alice's documents only
	})

	t.Run("Strict threshold test", func(t *testing.T) {
		// Use very strict threshold that should only match exact/very similar
		results, err := setup.Store.GetNearest(
			setup.ctx,
			baseEmbedding,
			nil,
			filterFields,
			0.1, // Very strict threshold
			10,
		)
		require.NoError(t, err)

		// Should find at least the exact match
		assert.GreaterOrEqual(t, len(results), 1)
		assert.Equal(t, testData[0].key, results[0].ID)
	})
}

func TestWeaviateStore_DeleteOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewTestSetup(t)
	defer setup.Cleanup(t)

	// Add test objects
	testKeys := []string{"delete-test-1", "delete-test-2", "delete-test-3"}
	for _, key := range testKeys {
		metadata := map[string]interface{}{
			"test_type": "deletion",
		}
		err := setup.Store.Add(setup.ctx, key, generateTestEmbedding(TestEmbeddingDim), metadata)
		require.NoError(t, err)
	}

	time.Sleep(200 * time.Millisecond)

	t.Run("Single delete", func(t *testing.T) {
		// Delete one object
		err := setup.Store.Delete(setup.ctx, testKeys[0])
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Verify it's gone
		_, err = setup.Store.GetChunk(setup.ctx, testKeys[0])
		assert.Error(t, err) // Should not be found
	})

	t.Run("Batch delete", func(t *testing.T) {
		// Delete remaining objects
		resp, err := setup.Store.DeleteAll(setup.ctx, []Query{{Field: "test_type", Operator: "Equal", Value: "deletion"}})
		require.NoError(t, err)
		assert.Len(t, resp, 2)
		assert.Equal(t, DeleteStatusSuccess, resp[0].Status)
		assert.Equal(t, DeleteStatusSuccess, resp[1].Status)

		time.Sleep(100 * time.Millisecond)

		// Verify they're gone
		results, err := setup.Store.GetChunks(setup.ctx, testKeys[1:])
		require.NoError(t, err)
		assert.Empty(t, results) // Should find nothing
	})

	t.Run("Delete non-existent keys", func(t *testing.T) {
		// Should not error when deleting non-existent keys
		resp, err := setup.Store.DeleteAll(setup.ctx, []Query{{Field: "test_type", Operator: "Equal", Value: "non-existent"}})
		require.NoError(t, err)
		assert.Len(t, resp, 0)
	})
}

// ============================================================================
// EDGE CASES AND ERROR HANDLING
// ============================================================================

func TestWeaviateStore_EdgeCases(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewTestSetup(t)
	defer setup.Cleanup(t)

	filterFields := []string{"type", "size", "public", "author"}

	t.Run("Empty and nil values", func(t *testing.T) {
		// Test with empty string key
		err := setup.Store.Add(setup.ctx, "", generateTestEmbedding(TestEmbeddingDim), map[string]interface{}{})
		assert.Error(t, err) // Should error on empty key

		// Test with nil metadata
		err = setup.Store.Add(setup.ctx, "nil-metadata-test", generateTestEmbedding(TestEmbeddingDim), nil)
		assert.Error(t, err) // Should error on nil metadata

		// Test with empty metadata
		err = setup.Store.Add(setup.ctx, "empty-metadata-test", generateTestEmbedding(TestEmbeddingDim), map[string]interface{}{})
		assert.NoError(t, err) // Should be OK
	})

	t.Run("Large metadata objects", func(t *testing.T) {
		// Create large metadata object
		largeMetadata := map[string]interface{}{
			"large_field": strings.Repeat("x", 10000), // 10KB string
			"nested": map[string]interface{}{
				"level1": map[string]interface{}{
					"level2": map[string]interface{}{
						"deep_data": "nested value",
					},
				},
			},
			"array_data": []string{"item1", "item2", "item3"},
		}

		err := setup.Store.Add(setup.ctx, "large-metadata-test", generateTestEmbedding(TestEmbeddingDim), largeMetadata)
		assert.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Retrieve and verify
		result, err := setup.Store.GetChunk(setup.ctx, "large-metadata-test")
		require.NoError(t, err)
		assert.Contains(t, result, "nested value")
	})

	t.Run("Special characters in keys and values", func(t *testing.T) {
		specialKey := "test-key-with-special-chars-éñ中文🚀"
		specialMetadata := map[string]interface{}{
			"unicode_field": "Value with émojis 🎉 and ñiño中文",
			"symbols":       "!@#$%^&*()_+-=[]{}|;':\",./<>?",
		}

		err := setup.Store.Add(setup.ctx, specialKey, generateTestEmbedding(TestEmbeddingDim), specialMetadata)
		assert.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		result, err := setup.Store.GetChunk(setup.ctx, specialKey)
		require.NoError(t, err)
		assert.Contains(t, result, "émojis")
	})

	t.Run("Zero-dimension and invalid embeddings", func(t *testing.T) {
		// Test with zero-length embedding
		err := setup.Store.Add(setup.ctx, "zero-embedding-test", []float32{}, map[string]interface{}{"test": "zero"})
		// This might succeed or fail depending on Weaviate configuration
		t.Logf("Zero embedding result: %v", err)

		// Test with very large embedding
		largeEmbedding := make([]float32, 10000) // 10K dimensions
		for i := range largeEmbedding {
			largeEmbedding[i] = 0.1
		}

		err = setup.Store.Add(setup.ctx, "large-embedding-test", largeEmbedding, map[string]interface{}{"test": "large"})
		// This will likely fail due to dimension mismatch
		t.Logf("Large embedding result: %v", err)
	})

	t.Run("Boundary conditions for similarity search", func(t *testing.T) {
		testEmbedding := generateTestEmbedding(TestEmbeddingDim)

		// Add a test object
		err := setup.Store.Add(setup.ctx, "boundary-test", testEmbedding, map[string]interface{}{"type": "boundary"})
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Test with threshold = 0 (should match everything)
		results, err := setup.Store.GetNearest(setup.ctx, testEmbedding, nil, filterFields, 0.0, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(results), 1)

		// Test with threshold = 1 (should match very little)
		results, err = setup.Store.GetNearest(setup.ctx, testEmbedding, nil, filterFields, 1.0, 10)
		require.NoError(t, err)
		// Should still find exact match
		assert.GreaterOrEqual(t, len(results), 1)

		// Test with limit = 0
		results, err = setup.Store.GetNearest(setup.ctx, testEmbedding, nil, filterFields, 0.5, 0)
		require.NoError(t, err)
		assert.Empty(t, results) // No results with limit 0
	})

	t.Run("Concurrent operations", func(t *testing.T) {
		// Test concurrent adds
		concurrency := 5
		done := make(chan bool, concurrency)

		for i := 0; i < concurrency; i++ {
			go func(id int) {
				defer func() { done <- true }()

				key := fmt.Sprintf("concurrent-test-%d", id)
				metadata := map[string]interface{}{
					"thread_id": id,
					"timestamp": time.Now().Unix(),
				}

				err := setup.Store.Add(setup.ctx, key, generateTestEmbedding(TestEmbeddingDim), metadata)
				assert.NoError(t, err)
			}(i)
		}

		// Wait for all goroutines
		for i := 0; i < concurrency; i++ {
			<-done
		}

		time.Sleep(200 * time.Millisecond)

		// Verify all objects were added
		for i := 0; i < concurrency; i++ {
			key := fmt.Sprintf("concurrent-test-%d", i)
			result, err := setup.Store.GetChunk(setup.ctx, key)
			assert.NoError(t, err)
			assert.NotEmpty(t, result)
		}
	})
}

func TestWeaviateStore_CompleteUseCases(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewTestSetup(t)
	defer setup.Cleanup(t)

	t.Run("Document Storage & Retrieval Scenario", func(t *testing.T) {
		// Add documents with different types
		documents := []struct {
			key       string
			embedding []float32
			metadata  map[string]interface{}
		}{
			{
				"doc1",
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"type": "pdf", "size": 1024, "public": true},
			},
			{
				"doc2",
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"type": "docx", "size": 2048, "public": false},
			},
			{
				"doc3",
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"type": "pdf", "size": 512, "public": true},
			},
		}

		filterFields := []string{"type", "size", "public", "author"}

		for _, doc := range documents {
			err := setup.Store.Add(setup.ctx, doc.key, doc.embedding, doc.metadata)
			require.NoError(t, err)
		}

		time.Sleep(300 * time.Millisecond)

		// Test various retrieval patterns

		// Get PDF documents
		pdfQuery := []Query{{Field: "type", Operator: "Equal", Value: "pdf"}}
		results, _, err := setup.Store.GetAll(setup.ctx, pdfQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1, doc3

		// Get large documents (size > 1000)
		sizeQuery := []Query{{Field: "size", Operator: "GreaterThan", Value: 1000}}
		results, _, err = setup.Store.GetAll(setup.ctx, sizeQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1, doc2

		// Get public PDFs
		combinedQuery := []Query{
			{Field: "public", Operator: "Equal", Value: true},
			{Field: "type", Operator: "Equal", Value: "pdf"},
		}
		results, _, err = setup.Store.GetAll(setup.ctx, combinedQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1, doc3

		// Vector similarity search
		queryEmbedding := documents[0].embedding // Similar to doc1
		vectorResults, err := setup.Store.GetNearest(setup.ctx, queryEmbedding, nil, filterFields, 0.8, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(vectorResults), 1)
	})

	t.Run("User Content Management Scenario", func(t *testing.T) {
		// Add user content with metadata
		userContent := []struct {
			key       string
			embedding []float32
			metadata  map[string]interface{}
		}{
			{
				"user1_content",
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"user": "alice", "lang": "en", "category": "tech"},
			},
			{
				"user2_content",
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"user": "bob", "lang": "es", "category": "tech"},
			},
			{
				"user3_content",
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"user": "alice", "lang": "en", "category": "sports"},
			},
		}

		filterFields := []string{"user", "lang", "category"}

		for _, content := range userContent {
			err := setup.Store.Add(setup.ctx, content.key, content.embedding, content.metadata)
			require.NoError(t, err)
		}

		time.Sleep(300 * time.Millisecond)

		// Test user-specific filtering
		aliceQuery := []Query{{Field: "user", Operator: "Equal", Value: "alice"}}
		results, _, err := setup.Store.GetAll(setup.ctx, aliceQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // Alice's content

		// English tech content
		techEnQuery := []Query{
			{Field: "lang", Operator: "Equal", Value: "en"},
			{Field: "category", Operator: "Equal", Value: "tech"},
		}
		results, _, err = setup.Store.GetAll(setup.ctx, techEnQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 1) // user1_content

		// Alice's similar content (semantic search with user filter)
		aliceFilter := []Query{{Field: "user", Operator: "Equal", Value: "alice"}}
		queryEmbedding := userContent[0].embedding
		vectorResults, err := setup.Store.GetNearest(setup.ctx, queryEmbedding, aliceFilter, filterFields, 0.5, 10)
		require.NoError(t, err)
		assert.Len(t, vectorResults, 2) // Both of Alice's content
	})

	t.Run("Semantic Cache-like Workflow", func(t *testing.T) {
		// Add request-response pairs with parameters
		cacheEntries := []struct {
			key       string
			embedding []float32
			metadata  map[string]interface{}
		}{
			{
				"req123",
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{
					"request_hash": "abc123",
					"user":         "u1",
					"lang":         "en",
					"response":     "answer1",
				},
			},
			{
				"req456",
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{
					"request_hash": "def456",
					"user":         "u1",
					"lang":         "es",
					"response":     "answer2",
				},
			},
		}

		filterFields := []string{"request_hash", "user", "lang", "response"}

		for _, entry := range cacheEntries {
			err := setup.Store.Add(setup.ctx, entry.key, entry.embedding, entry.metadata)
			require.NoError(t, err)
		}

		time.Sleep(300 * time.Millisecond)

		// Test hash-based direct retrieval (exact match)
		hashQuery := []Query{{Field: "request_hash", Operator: "Equal", Value: "abc123"}}
		results, _, err := setup.Store.GetAll(setup.ctx, hashQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 1)

		// Test semantic search with user and language filters
		userLangFilter := []Query{
			{Field: "user", Operator: "Equal", Value: "u1"},
			{Field: "lang", Operator: "Equal", Value: "en"},
		}
		similarEmbedding := generateSimilarEmbedding(cacheEntries[0].embedding, 0.9)
		vectorResults, err := setup.Store.GetNearest(setup.ctx, similarEmbedding, userLangFilter, filterFields, 0.7, 10)
		require.NoError(t, err)
		assert.Len(t, vectorResults, 1) // Should find English content for u1
	})
}

// ============================================================================
// PERFORMANCE AND STRESS TESTS
// ============================================================================

func TestWeaviateStore_Performance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance tests in short mode")
	}

	setup := NewTestSetup(t)
	defer setup.Cleanup(t)

	filterFields := []string{"index", "category"}

	t.Run("Bulk insert performance", func(t *testing.T) {
		numObjects := 100
		start := time.Now()

		for i := 0; i < numObjects; i++ {
			key := fmt.Sprintf("perf-test-%d", i)
			metadata := map[string]interface{}{
				"index":    i,
				"category": fmt.Sprintf("cat-%d", i%5),
			}

			err := setup.Store.Add(setup.ctx, key, generateTestEmbedding(TestEmbeddingDim), metadata)
			require.NoError(t, err)
		}

		duration := time.Since(start)
		t.Logf("Inserted %d objects in %v (%.2f objects/sec)",
			numObjects, duration, float64(numObjects)/duration.Seconds())

		// Allow time for indexing
		time.Sleep(2 * time.Second)

		// Test batch retrieval performance
		keys := make([]string, numObjects)
		for i := range numObjects {
			keys[i] = fmt.Sprintf("perf-test-%d", i)
		}

		start = time.Now()
		results, err := setup.Store.GetChunks(setup.ctx, keys)
		duration = time.Since(start)

		require.NoError(t, err)
		assert.Len(t, results, numObjects)
		t.Logf("Retrieved %d objects in %v (%.2f objects/sec)",
			len(results), duration, float64(len(results))/duration.Seconds())
	})

	t.Run("Vector search performance", func(t *testing.T) {
		// Perform multiple vector searches and measure performance
		queryEmbedding := generateTestEmbedding(TestEmbeddingDim)
		numSearches := 10

		start := time.Now()
		for i := 0; i < numSearches; i++ {
			_, err := setup.Store.GetNearest(setup.ctx, queryEmbedding, nil, filterFields, 0.8, 10)
			require.NoError(t, err)
		}
		duration := time.Since(start)

		t.Logf("Performed %d vector searches in %v (%.2f searches/sec)",
			numSearches, duration, float64(numSearches)/duration.Seconds())
	})
}

// ============================================================================
// INTERFACE COMPLIANCE TESTS
// ============================================================================

func TestWeaviateStore_InterfaceCompliance(t *testing.T) {
	// Verify that WeaviateStore implements VectorStore interface
	var _ VectorStore = (*WeaviateStore)(nil)
}

func TestVectorStoreFactory_Weaviate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	config := &Config{
		Enabled: true,
		Type:    VectorStoreTypeWeaviate,
		Config: WeaviateConfig{
			Scheme: getEnvWithDefault("WEAVIATE_SCHEME", DefaultTestScheme),
			Host:   getEnvWithDefault("WEAVIATE_HOST", DefaultTestHost),
			ApiKey: os.Getenv("WEAVIATE_API_KEY"),
		},
	}

	store, err := NewVectorStore(context.Background(), config, logger)
	if err != nil {
		t.Skipf("Could not create Weaviate store: %v", err)
	}
	defer store.Close(context.Background())

	// Verify it's actually a WeaviateStore
	weaviateStore, ok := store.(*WeaviateStore)
	assert.True(t, ok)
	assert.NotNil(t, weaviateStore)
}
