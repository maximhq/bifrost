package vectorstore

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	PineconeTestTimeout          = 30 * time.Second
	PineconeTestNamespace        = "bifrost-test-namespace"
	PineconeTestDimension        = 1536             // Matches text-embedding-3-small dimension
	PineconeTestDefaultAPIKey    = "pclocal"        // Pinecone Local doesn't validate API keys
	PineconeTestDefaultIndexHost = "localhost:5081" // Pinecone Local default port
)

type PineconeTestSetup struct {
	Store  *PineconeStore
	Logger schemas.Logger
	Config PineconeConfig
	ctx    context.Context
	cancel context.CancelFunc
}

func NewPineconeTestSetup(t *testing.T) *PineconeTestSetup {
	apiKey := schemas.NewEnvVar(getEnvWithDefault("PINECONE_API_KEY", PineconeTestDefaultAPIKey))
	indexHost := schemas.NewEnvVar(getEnvWithDefault("PINECONE_INDEX_HOST", PineconeTestDefaultIndexHost))

	config := PineconeConfig{
		APIKey:    *apiKey,
		IndexHost: *indexHost,
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx, cancel := context.WithTimeout(context.Background(), PineconeTestTimeout)

	store, err := newPineconeStore(ctx, &config, logger)
	if err != nil {
		cancel()
		t.Fatalf("Failed to create Pinecone store: %v", err)
	}

	setup := &PineconeTestSetup{
		Store:  store,
		Logger: logger,
		Config: config,
		ctx:    ctx,
		cancel: cancel,
	}

	return setup
}

func (ts *PineconeTestSetup) Cleanup(t *testing.T) {
	defer ts.cancel()

	if !testing.Short() {
		ts.cleanupTestData(t)
	}

	if err := ts.Store.Close(ts.ctx, PineconeTestNamespace); err != nil {
		t.Logf("Warning: Failed to close store: %v", err)
	}
}

func (ts *PineconeTestSetup) cleanupTestData(t *testing.T) {
	// Delete all vectors in the test namespace
	err := ts.Store.DeleteNamespace(ts.ctx, PineconeTestNamespace)
	if err != nil {
		t.Logf("Warning: Failed to cleanup test namespace: %v", err)
	}
	t.Logf("Cleaned up test namespace: %s", PineconeTestNamespace)
}

// ============================================================================
// UNIT TESTS
// ============================================================================

func TestPineconeConfig_Validation(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	tests := []struct {
		name        string
		config      PineconeConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "missing api key",
			config: PineconeConfig{
				IndexHost: *schemas.NewEnvVar("https://my-index.svc.environment.pinecone.io"),
			},
			expectError: true,
			errorMsg:    "pinecone api_key is required",
		},
		{
			name: "missing index host",
			config: PineconeConfig{
				APIKey: *schemas.NewEnvVar("test-api-key"),
			},
			expectError: true,
			errorMsg:    "pinecone index_host is required",
		},
		{
			name: "empty api key",
			config: PineconeConfig{
				APIKey:    *schemas.NewEnvVar(""),
				IndexHost: *schemas.NewEnvVar("https://my-index.svc.environment.pinecone.io"),
			},
			expectError: true,
			errorMsg:    "pinecone api_key is required",
		},
		{
			name: "empty index host",
			config: PineconeConfig{
				APIKey:    *schemas.NewEnvVar("test-api-key"),
				IndexHost: *schemas.NewEnvVar(""),
			},
			expectError: true,
			errorMsg:    "pinecone index_host is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := newPineconeStore(ctx, &tt.config, logger)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, store)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				// Note: This will fail with connection error in unit tests
				// but should pass config validation
				if err != nil {
					assert.Contains(t, err.Error(), "failed to connect")
				}
			}
		})
	}
}

func TestBuildPineconeFilter(t *testing.T) {
	tests := []struct {
		name     string
		queries  []Query
		expected bool
	}{
		{
			name:     "empty queries",
			queries:  []Query{},
			expected: false,
		},
		{
			name: "single string query",
			queries: []Query{
				{Field: "category", Operator: QueryOperatorEqual, Value: "tech"},
			},
			expected: true,
		},
		{
			name: "single numeric query",
			queries: []Query{
				{Field: "size", Operator: QueryOperatorGreaterThan, Value: 1000},
			},
			expected: true,
		},
		{
			name: "multiple queries",
			queries: []Query{
				{Field: "category", Operator: QueryOperatorEqual, Value: "tech"},
				{Field: "public", Operator: QueryOperatorEqual, Value: true},
			},
			expected: true,
		},
		{
			name: "not equal query",
			queries: []Query{
				{Field: "status", Operator: QueryOperatorNotEqual, Value: "deleted"},
			},
			expected: true,
		},
		{
			name: "range queries",
			queries: []Query{
				{Field: "count", Operator: QueryOperatorGreaterThanOrEqual, Value: 10},
				{Field: "score", Operator: QueryOperatorLessThanOrEqual, Value: 100},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := buildPineconeFilter(tt.queries)
			assert.NoError(t, err)

			if tt.expected {
				assert.NotNil(t, result)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

func TestBuildPineconeCondition(t *testing.T) {
	tests := []struct {
		name     string
		query    Query
		expected map[string]interface{}
	}{
		{
			name:     "equal operator",
			query:    Query{Field: "category", Operator: QueryOperatorEqual, Value: "tech"},
			expected: map[string]interface{}{"$eq": "tech"},
		},
		{
			name:     "not equal operator",
			query:    Query{Field: "status", Operator: QueryOperatorNotEqual, Value: "deleted"},
			expected: map[string]interface{}{"$ne": "deleted"},
		},
		{
			name:     "greater than operator",
			query:    Query{Field: "count", Operator: QueryOperatorGreaterThan, Value: 10},
			expected: map[string]interface{}{"$gt": 10},
		},
		{
			name:     "greater than or equal operator",
			query:    Query{Field: "count", Operator: QueryOperatorGreaterThanOrEqual, Value: 10},
			expected: map[string]interface{}{"$gte": 10},
		},
		{
			name:     "less than operator",
			query:    Query{Field: "score", Operator: QueryOperatorLessThan, Value: 100},
			expected: map[string]interface{}{"$lt": 100},
		},
		{
			name:     "less than or equal operator",
			query:    Query{Field: "score", Operator: QueryOperatorLessThanOrEqual, Value: 100},
			expected: map[string]interface{}{"$lte": 100},
		},
		{
			name:     "contains any operator",
			query:    Query{Field: "tags", Operator: QueryOperatorContainsAny, Value: []string{"a", "b"}},
			expected: map[string]interface{}{"$in": []string{"a", "b"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildPineconeCondition(tt.query)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMatchesQueries(t *testing.T) {
	tests := []struct {
		name     string
		props    map[string]interface{}
		queries  []Query
		expected bool
	}{
		{
			name:     "empty queries matches all",
			props:    map[string]interface{}{"type": "document"},
			queries:  []Query{},
			expected: true,
		},
		{
			name:     "equal match",
			props:    map[string]interface{}{"type": "document"},
			queries:  []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "document"}},
			expected: true,
		},
		{
			name:     "equal no match",
			props:    map[string]interface{}{"type": "document"},
			queries:  []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "image"}},
			expected: false,
		},
		{
			name:     "not equal match",
			props:    map[string]interface{}{"type": "document"},
			queries:  []Query{{Field: "type", Operator: QueryOperatorNotEqual, Value: "image"}},
			expected: true,
		},
		{
			name:     "is null match",
			props:    map[string]interface{}{"type": "document"},
			queries:  []Query{{Field: "author", Operator: QueryOperatorIsNull, Value: nil}},
			expected: true,
		},
		{
			name:     "is not null match",
			props:    map[string]interface{}{"type": "document", "author": "alice"},
			queries:  []Query{{Field: "author", Operator: QueryOperatorIsNotNull, Value: nil}},
			expected: true,
		},
		{
			name:  "multiple queries all match",
			props: map[string]interface{}{"type": "document", "public": true},
			queries: []Query{
				{Field: "type", Operator: QueryOperatorEqual, Value: "document"},
				{Field: "public", Operator: QueryOperatorEqual, Value: true},
			},
			expected: true,
		},
		{
			name:  "multiple queries one fails",
			props: map[string]interface{}{"type": "document", "public": false},
			queries: []Query{
				{Field: "type", Operator: QueryOperatorEqual, Value: "document"},
				{Field: "public", Operator: QueryOperatorEqual, Value: true},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchesQueries(tt.props, tt.queries)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFilterPropertiesPinecone(t *testing.T) {
	props := map[string]interface{}{
		"type":   "document",
		"author": "alice",
		"size":   1024,
		"public": true,
	}

	tests := []struct {
		name         string
		selectFields []string
		expected     map[string]interface{}
	}{
		{
			name:         "empty select returns all",
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
			name:         "select non-existent field",
			selectFields: []string{"missing"},
			expected:     map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterPropertiesPinecone(props, tt.selectFields)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ============================================================================
// INTEGRATION TESTS (require real Pinecone instance)
// ============================================================================

func TestPineconeStore_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewPineconeTestSetup(t)
	defer setup.Cleanup(t)

	// Test Ping
	err := setup.Store.Ping(setup.ctx)
	require.NoError(t, err)

	// Test Add and GetChunk
	key := generateUUID()
	embedding := generateTestEmbedding(PineconeTestDimension)
	metadata := map[string]interface{}{
		"type":   "document",
		"author": "test",
	}

	err = setup.Store.Add(setup.ctx, PineconeTestNamespace, key, embedding, metadata)
	require.NoError(t, err)

	// Wait for eventual consistency
	time.Sleep(2 * time.Second)

	result, err := setup.Store.GetChunk(setup.ctx, PineconeTestNamespace, key)
	require.NoError(t, err)
	assert.Equal(t, key, result.ID)
	assert.Equal(t, "document", result.Properties["type"])
	assert.Equal(t, "test", result.Properties["author"])

	// Test GetChunks
	key2 := generateUUID()
	err = setup.Store.Add(setup.ctx, PineconeTestNamespace, key2, generateTestEmbedding(PineconeTestDimension), map[string]interface{}{"type": "image"})
	require.NoError(t, err)

	time.Sleep(2 * time.Second)

	results, err := setup.Store.GetChunks(setup.ctx, PineconeTestNamespace, []string{key, key2})
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestPineconeStore_VectorSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewPineconeTestSetup(t)
	defer setup.Cleanup(t)

	// Add test vectors
	emb := generateTestEmbedding(PineconeTestDimension)
	err := setup.Store.Add(setup.ctx, PineconeTestNamespace, generateUUID(), emb, map[string]interface{}{"type": "tech"})
	require.NoError(t, err)

	err = setup.Store.Add(setup.ctx, PineconeTestNamespace, generateUUID(), generateTestEmbedding(PineconeTestDimension), map[string]interface{}{"type": "sports"})
	require.NoError(t, err)

	// Wait for eventual consistency
	time.Sleep(3 * time.Second)

	// Test vector similarity search
	results, err := setup.Store.GetNearest(setup.ctx, PineconeTestNamespace, emb, nil, []string{"type"}, 0.1, 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)

	if len(results) > 0 {
		require.NotNil(t, results[0].Score)
	}

	// Test with filter
	queries := []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "tech"}}
	results, err = setup.Store.GetNearest(setup.ctx, PineconeTestNamespace, emb, queries, []string{"type"}, 0.1, 10)
	require.NoError(t, err)
	for _, result := range results {
		assert.Equal(t, "tech", result.Properties["type"])
	}
}

func TestPineconeStore_Delete(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewPineconeTestSetup(t)
	defer setup.Cleanup(t)

	// Add a vector
	key := generateUUID()
	err := setup.Store.Add(setup.ctx, PineconeTestNamespace, key, generateTestEmbedding(PineconeTestDimension), map[string]interface{}{"type": "to-delete"})
	require.NoError(t, err)

	time.Sleep(2 * time.Second)

	// Verify it exists
	_, err = setup.Store.GetChunk(setup.ctx, PineconeTestNamespace, key)
	require.NoError(t, err)

	// Delete it
	err = setup.Store.Delete(setup.ctx, PineconeTestNamespace, key)
	require.NoError(t, err)

	time.Sleep(2 * time.Second)

	// Verify it's gone
	_, err = setup.Store.GetChunk(setup.ctx, PineconeTestNamespace, key)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPineconeStore_ErrorHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewPineconeTestSetup(t)
	defer setup.Cleanup(t)

	// Test GetChunk with non-existent ID
	_, err := setup.Store.GetChunk(setup.ctx, PineconeTestNamespace, generateUUID())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Test Add with empty ID
	err = setup.Store.Add(setup.ctx, PineconeTestNamespace, "", generateTestEmbedding(PineconeTestDimension), map[string]interface{}{"type": "test"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")

	// Test Delete with empty ID
	err = setup.Store.Delete(setup.ctx, PineconeTestNamespace, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")
}

func TestPineconeStore_SemanticCacheWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewPineconeTestSetup(t)
	defer setup.Cleanup(t)

	// Simulate a semantic cache workflow
	cacheEntries := []struct {
		key       string
		embedding []float32
		metadata  map[string]interface{}
	}{
		{
			generateUUID(),
			generateTestEmbedding(PineconeTestDimension),
			map[string]interface{}{
				"request_hash": "abc123",
				"user":         "u1",
				"lang":         "en",
				"response":     "answer1",
			},
		},
		{
			generateUUID(),
			generateTestEmbedding(PineconeTestDimension),
			map[string]interface{}{
				"request_hash": "def456",
				"user":         "u1",
				"lang":         "es",
				"response":     "answer2",
			},
		},
	}

	// Add cache entries
	for _, entry := range cacheEntries {
		err := setup.Store.Add(setup.ctx, PineconeTestNamespace, entry.key, entry.embedding, entry.metadata)
		require.NoError(t, err)
	}

	time.Sleep(3 * time.Second)

	// Test semantic search with user filter
	userFilter := []Query{{Field: "user", Operator: QueryOperatorEqual, Value: "u1"}}
	results, err := setup.Store.GetNearest(setup.ctx, PineconeTestNamespace, cacheEntries[0].embedding, userFilter, []string{"request_hash", "user", "lang", "response"}, 0.1, 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)

	// Verify user filter worked
	for _, result := range results {
		assert.Equal(t, "u1", result.Properties["user"])
	}
}

// ============================================================================
// INTERFACE COMPLIANCE TESTS
// ============================================================================

func TestPineconeStore_InterfaceCompliance(t *testing.T) {
	var _ VectorStore = (*PineconeStore)(nil)
}

func TestVectorStoreFactory_Pinecone(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	apiKey := schemas.NewEnvVar(getEnvWithDefault("PINECONE_API_KEY", PineconeTestDefaultAPIKey))
	indexHost := schemas.NewEnvVar(getEnvWithDefault("PINECONE_INDEX_HOST", PineconeTestDefaultIndexHost))

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	config := &Config{
		Enabled: true,
		Type:    VectorStoreTypePinecone,
		Config: PineconeConfig{
			APIKey:    *apiKey,
			IndexHost: *indexHost,
		},
	}

	store, err := NewVectorStore(context.Background(), config, logger)
	if err != nil {
		t.Skipf("Could not create Pinecone store: %v", err)
	}
	defer store.Close(context.Background(), PineconeTestNamespace)

	pineconeStore, ok := store.(*PineconeStore)
	assert.True(t, ok)
	assert.NotNil(t, pineconeStore)
}
