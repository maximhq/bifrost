package vectorstore

import (
	"context"
	"testing"
	"time"

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
	TestClassName      = "TestWeaviate"
	TestEmbeddingDim   = 384
	DefaultTestScheme  = "http"
	DefaultTestHost    = "localhost:9000"
	DefaultTestTimeout = 10 * time.Second
)

// TestSetup provides common test infrastructure
type TestSetup struct {
	Store  *WeaviateStore
	Logger schemas.Logger
	Config WeaviateConfig
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTestSetup creates a test setup with environment-driven configuration
func NewTestSetup(t *testing.T) *TestSetup {
	// Get configuration from environment variables
	scheme := getEnvWithDefault("WEAVIATE_SCHEME", DefaultTestScheme)
	host := schemas.NewEnvVar(getEnvWithDefault("WEAVIATE_HOST", DefaultTestHost))

	timeoutStr := getEnvWithDefault("WEAVIATE_TIMEOUT", "10s")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		timeout = DefaultTestTimeout
	}

	config := WeaviateConfig{
		Scheme:  scheme,
		Host:    host,
		APIKey:  schemas.NewEnvVar("env.WEAVIATE_API_KEY"),
		Timeout: timeout,
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)

	store, err := newWeaviateStore(ctx, &config, logger)
	if err != nil {
		cancel()
		t.Fatalf("Failed to create Weaviate store: %v", err)
	}

	setup := &TestSetup{
		Store:  store,
		Logger: logger,
		Config: config,
		ctx:    ctx,
		cancel: cancel,
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

	if err := ts.Store.Close(ts.ctx, TestClassName); err != nil {
		t.Logf("Warning: Failed to close store: %v", err)
	}
}

// ensureClassExists creates the test class in Weaviate
func (ts *TestSetup) ensureClassExists(t *testing.T) {
	// Try to get class schema first
	exists, err := ts.Store.client.Schema().ClassGetter().
		WithClassName(TestClassName).
		Do(ts.ctx)

	if err == nil && exists != nil {
		t.Logf("Class %s already exists", TestClassName)
		return
	}

	// Create class with minimal schema - let Weaviate auto-create properties
	class := &models.Class{
		Class: TestClassName,
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
		t.Logf("Warning: Failed to create test class %s: %v", TestClassName, err)
		t.Logf("This might be due to auto-schema creation. Continuing...")
	} else {
		t.Logf("Created test class: %s", TestClassName)
	}
}

// cleanupTestData removes all test objects from the class
func (ts *TestSetup) cleanupTestData(t *testing.T) {
	// Delete all objects in the test class
	allTestKeys, _, err := ts.Store.GetAll(ts.ctx, TestClassName, []Query{}, []string{}, nil, 1000)
	if err != nil {
		t.Logf("Warning: Failed to get all test keys: %v", err)
		return
	}

	for _, key := range allTestKeys {
		err := ts.Store.Delete(ts.ctx, TestClassName, key.ID)
		if err != nil {
			t.Logf("Warning: Failed to delete test key %s: %v", key.ID, err)
		}
	}

	t.Logf("Cleaned up test class: %s", TestClassName)
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
				Host:   schemas.NewEnvVar("localhost:8080"),
			},
			expectError: false,
		},
		{
			name: "missing scheme",
			config: WeaviateConfig{
				Host: schemas.NewEnvVar("localhost:8080"),
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
				Host:   schemas.NewEnvVar("cluster.weaviate.network"),
				APIKey: schemas.NewEnvVar("test-key"),
			},
			expectError: false,
		},
		{
			name: "with custom headers",
			config: WeaviateConfig{
				Scheme: "http",
				Host:   schemas.NewEnvVar("localhost:8080"),
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
		Host:   schemas.NewEnvVar("localhost:8080"),
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
		err := setup.Store.Add(setup.ctx, TestClassName, testKey, embedding, metadata)
		require.NoError(t, err)

		// Small delay to ensure consistency
		time.Sleep(100 * time.Millisecond)

		// Get single chunk
		result, err := setup.Store.GetChunk(setup.ctx, TestClassName, testKey)
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
		err := setup.Store.Add(setup.ctx, TestClassName, testKey, nil, metadata)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Retrieve it
		result, err := setup.Store.GetChunk(setup.ctx, TestClassName, testKey)
		require.NoError(t, err)
		assert.Equal(t, "metadata-only", result.Properties["type"])
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
		err := setup.Store.Add(setup.ctx, TestClassName, item.key, embedding, item.metadata)
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond) // Wait for consistency

	t.Run("Filter by numeric comparison", func(t *testing.T) {
		queries := []Query{
			{Field: "size", Operator: "GreaterThan", Value: 1000},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, TestClassName, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1 (1024) and doc2 (2048)
	})

	t.Run("Filter by boolean", func(t *testing.T) {
		queries := []Query{
			{Field: "public", Operator: "Equal", Value: true},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, TestClassName, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 3) // doc1, doc3, doc4
	})

	t.Run("Multiple filters (AND)", func(t *testing.T) {
		queries := []Query{
			{Field: "type", Operator: "Equal", Value: "pdf"},
			{Field: "public", Operator: "Equal", Value: true},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, TestClassName, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1 and doc3
	})

	t.Run("Complex multi-condition filter", func(t *testing.T) {
		queries := []Query{
			{Field: "author", Operator: "Equal", Value: "alice"},
			{Field: "size", Operator: "LessThan", Value: 2000},
			{Field: "public", Operator: "Equal", Value: true},
		}

		results, _, err := setup.Store.GetAll(setup.ctx, TestClassName, queries, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1 and doc3 (both by alice, < 2000 size, public)
	})

	t.Run("Pagination test", func(t *testing.T) {
		// Test with limit of 2
		results, cursor, err := setup.Store.GetAll(setup.ctx, TestClassName, nil, filterFields, nil, 2)
		require.NoError(t, err)
		assert.Len(t, results, 2)

		if cursor != nil {
			// Get next page
			nextResults, _, err := setup.Store.GetAll(setup.ctx, TestClassName, nil, filterFields, cursor, 2)
			require.NoError(t, err)
			assert.LessOrEqual(t, len(nextResults), 2)
			t.Logf("First page: %d results, Next page: %d results", len(results), len(nextResults))
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
				generateUUID(),
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"type": "pdf", "size": 1024, "public": true},
			},
			{
				generateUUID(),
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"type": "docx", "size": 2048, "public": false},
			},
			{
				generateUUID(),
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"type": "pdf", "size": 512, "public": true},
			},
		}

		filterFields := []string{"type", "size", "public", "author"}

		for _, doc := range documents {
			err := setup.Store.Add(setup.ctx, TestClassName, doc.key, doc.embedding, doc.metadata)
			require.NoError(t, err)
		}

		time.Sleep(300 * time.Millisecond)

		// Test various retrieval patterns

		// Get PDF documents
		pdfQuery := []Query{{Field: "type", Operator: "Equal", Value: "pdf"}}
		results, _, err := setup.Store.GetAll(setup.ctx, TestClassName, pdfQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1, doc3

		// Get large documents (size > 1000)
		sizeQuery := []Query{{Field: "size", Operator: "GreaterThan", Value: 1000}}
		results, _, err = setup.Store.GetAll(setup.ctx, TestClassName, sizeQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1, doc2

		// Get public PDFs
		combinedQuery := []Query{
			{Field: "public", Operator: "Equal", Value: true},
			{Field: "type", Operator: "Equal", Value: "pdf"},
		}
		results, _, err = setup.Store.GetAll(setup.ctx, TestClassName, combinedQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // doc1, doc3

		// Vector similarity search
		queryEmbedding := documents[0].embedding // Similar to doc1
		vectorResults, err := setup.Store.GetNearest(setup.ctx, TestClassName, queryEmbedding, nil, filterFields, 0.8, 10)
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
				generateUUID(),
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"user": "alice", "lang": "en", "category": "tech"},
			},
			{
				generateUUID(),
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"user": "bob", "lang": "es", "category": "tech"},
			},
			{
				generateUUID(),
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{"user": "alice", "lang": "en", "category": "sports"},
			},
		}

		filterFields := []string{"user", "lang", "category"}

		for _, content := range userContent {
			err := setup.Store.Add(setup.ctx, TestClassName, content.key, content.embedding, content.metadata)
			require.NoError(t, err)
		}

		time.Sleep(300 * time.Millisecond)

		// Test user-specific filtering
		aliceQuery := []Query{{Field: "user", Operator: "Equal", Value: "alice"}}
		results, _, err := setup.Store.GetAll(setup.ctx, TestClassName, aliceQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 2) // Alice's content

		// English tech content
		techEnQuery := []Query{
			{Field: "lang", Operator: "Equal", Value: "en"},
			{Field: "category", Operator: "Equal", Value: "tech"},
		}
		results, _, err = setup.Store.GetAll(setup.ctx, TestClassName, techEnQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 1) // user1_content

		// Alice's similar content (semantic search with user filter)
		aliceFilter := []Query{{Field: "user", Operator: "Equal", Value: "alice"}}
		queryEmbedding := userContent[0].embedding
		vectorResults, err := setup.Store.GetNearest(setup.ctx, TestClassName, queryEmbedding, aliceFilter, filterFields, 0.1, 10)
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
				generateUUID(),
				generateTestEmbedding(TestEmbeddingDim),
				map[string]interface{}{
					"request_hash": "abc123",
					"user":         "u1",
					"lang":         "en",
					"response":     "answer1",
				},
			},
			{
				generateUUID(),
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
			err := setup.Store.Add(setup.ctx, TestClassName, entry.key, entry.embedding, entry.metadata)
			require.NoError(t, err)
		}

		time.Sleep(300 * time.Millisecond)

		// Test hash-based direct retrieval (exact match)
		hashQuery := []Query{{Field: "request_hash", Operator: "Equal", Value: "abc123"}}
		results, _, err := setup.Store.GetAll(setup.ctx, TestClassName, hashQuery, filterFields, nil, 10)
		require.NoError(t, err)
		assert.Len(t, results, 1)

		// Test semantic search with user and language filters
		userLangFilter := []Query{
			{Field: "user", Operator: "Equal", Value: "u1"},
			{Field: "lang", Operator: "Equal", Value: "en"},
		}
		similarEmbedding := generateSimilarEmbedding(cacheEntries[0].embedding, 0.9)
		vectorResults, err := setup.Store.GetNearest(setup.ctx, TestClassName, similarEmbedding, userLangFilter, filterFields, 0.7, 10)
		require.NoError(t, err)
		assert.Len(t, vectorResults, 1) // Should find English content for u1
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
			Host:   schemas.NewEnvVar(getEnvWithDefault("WEAVIATE_HOST", DefaultTestHost)),
			APIKey: schemas.NewEnvVar("env.WEAVIATE_API_KEY"),
		},
	}

	store, err := NewVectorStore(context.Background(), config, logger)
	if err != nil {
		t.Skipf("Could not create Weaviate store: %v", err)
	}
	defer store.Close(context.Background(), TestClassName)

	// Verify it's actually a WeaviateStore
	weaviateStore, ok := store.(*WeaviateStore)
	assert.True(t, ok)
	assert.NotNil(t, weaviateStore)
}

func TestWeaviateStore_NamespaceDimensionHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewTestSetup(t)
	defer setup.Cleanup(t)

	testClassName := "TestDimensionHandling"

	t.Run("Recreate class with different dimension should not crash", func(t *testing.T) {
		properties := map[string]VectorStoreProperties{
			"type": {DataType: VectorStorePropertyTypeString},
			"test": {DataType: VectorStorePropertyTypeString},
		}

		// Step 1: Create class with dimension 512
		err := setup.Store.CreateNamespace(setup.ctx, testClassName, 512, properties)
		require.NoError(t, err)

		// Add a document with 512-dimensional embedding
		testKey512 := generateUUID()
		embedding512 := generateTestEmbedding(512)
		metadata := map[string]interface{}{
			"type": "test_doc",
			"test": "dimension_512",
		}

		err = setup.Store.Add(setup.ctx, testClassName, testKey512, embedding512, metadata)
		require.NoError(t, err)

		// Verify it was added
		result, err := setup.Store.GetChunk(setup.ctx, testClassName, testKey512)
		require.NoError(t, err)
		assert.Equal(t, "dimension_512", result.Properties["test"])

		// Step 2: Delete the class/namespace
		err = setup.Store.DeleteNamespace(setup.ctx, testClassName)
		require.NoError(t, err)

		// Step 3: Create class with same name but different dimension - should not crash
		err = setup.Store.CreateNamespace(setup.ctx, testClassName, 1024, properties)
		require.NoError(t, err)

		// Add a document with 1024-dimensional embedding
		testKey1024 := generateUUID()
		embedding1024 := generateTestEmbedding(1024)
		metadata1024 := map[string]interface{}{
			"type": "test_doc",
			"test": "dimension_1024",
		}

		err = setup.Store.Add(setup.ctx, testClassName, testKey1024, embedding1024, metadata1024)
		require.NoError(t, err)

		// Verify new document exists
		result, err = setup.Store.GetChunk(setup.ctx, testClassName, testKey1024)
		require.NoError(t, err)
		assert.Equal(t, "dimension_1024", result.Properties["test"])

		// Verify vector search works with new dimension
		vectorResults, err := setup.Store.GetNearest(setup.ctx, testClassName, embedding1024, nil, []string{"type", "test"}, 0.8, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(vectorResults), 1)
		assert.NotNil(t, vectorResults[0].Score)

		// Cleanup
		err = setup.Store.DeleteNamespace(setup.ctx, testClassName)
		if err != nil {
			t.Logf("Warning: Failed to cleanup class: %v", err)
		}
	})
}
