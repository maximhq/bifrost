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

const (
	QdrantTestTimeout     = 30 * time.Second
	QdrantTestCollection  = "bifrost-test-collection"
	QdrantTestDefaultHost = "localhost"
	QdrantTestDefaultPort = "6334"
	QdrantTestDimension   = 384
)

type QdrantTestSetup struct {
	Store  *QdrantStore
	Logger schemas.Logger
	Config QdrantConfig
	ctx    context.Context
	cancel context.CancelFunc
}

func NewQdrantTestSetup(t *testing.T) *QdrantTestSetup {
	host := schemas.NewEnvVar(getEnvWithDefault("QDRANT_HOST", QdrantTestDefaultHost))
	port := schemas.NewEnvVar(getEnvWithDefault("QDRANT_PORT", QdrantTestDefaultPort))
	apiKey := schemas.NewEnvVar(os.Getenv("QDRANT_API_KEY"))
	useTLS := schemas.NewEnvVar(os.Getenv("QDRANT_USE_TLS"))

	config := QdrantConfig{
		Host:   *host,
		Port:   *port,
		APIKey: *apiKey,
		UseTLS: *useTLS,
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx, cancel := context.WithTimeout(context.Background(), QdrantTestTimeout)

	store, err := newQdrantStore(ctx, &config, logger)
	if err != nil {
		cancel()
		t.Fatalf("Failed to create Qdrant store: %v", err)
	}

	setup := &QdrantTestSetup{
		Store:  store,
		Logger: logger,
		Config: config,
		ctx:    ctx,
		cancel: cancel,
	}

	setup.ensureCollectionExists(t)

	return setup
}

func (ts *QdrantTestSetup) Cleanup(t *testing.T) {
	defer ts.cancel()

	if !testing.Short() {
		ts.cleanupTestData(t)
	}

	if err := ts.Store.Close(ts.ctx, QdrantTestCollection); err != nil {
		t.Logf("Warning: Failed to close store: %v", err)
	}
}

func (ts *QdrantTestSetup) ensureCollectionExists(t *testing.T) {
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
	}

	err := ts.Store.CreateNamespace(ts.ctx, QdrantTestCollection, QdrantTestDimension, properties)
	if err != nil {
		t.Fatalf("Failed to create collection %q: %v", QdrantTestCollection, err)
	}
	t.Logf("Created test collection: %s", QdrantTestCollection)
}

func (ts *QdrantTestSetup) cleanupTestData(t *testing.T) {
	allTestKeys, _, err := ts.Store.GetAll(ts.ctx, QdrantTestCollection, []Query{}, []string{}, nil, 1000)
	if err != nil {
		t.Logf("Warning: Failed to get all test keys: %v", err)
		return
	}

	for _, key := range allTestKeys {
		err := ts.Store.Delete(ts.ctx, QdrantTestCollection, key.ID)
		if err != nil {
			t.Logf("Warning: Failed to delete test key %s: %v", key.ID, err)
		}
	}

	t.Logf("Cleaned up test collection: %s", QdrantTestCollection)
}

func TestQdrantConfig_Validation(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	ctx := context.Background()

	tests := []struct {
		name        string
		config      QdrantConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			config: QdrantConfig{
				Host: *schemas.NewEnvVar("localhost"),
				Port: *schemas.NewEnvVar("6334"),
			},
			expectError: false,
		},
		{
			name: "missing host",
			config: QdrantConfig{
				Port: *schemas.NewEnvVar("6334"),
			},
			expectError: true,
			errorMsg:    "qdrant host is required",
		},
		{
			name: "missing port",
			config: QdrantConfig{
				Host: *schemas.NewEnvVar("localhost"),
			},
			expectError: true,
			errorMsg:    "qdrant port is required",
		},
		{
			name: "with api key",
			config: QdrantConfig{
				Host:   *schemas.NewEnvVar("cluster.qdrant.io"),
				Port:   *schemas.NewEnvVar("6334"),
				APIKey: *schemas.NewEnvVar("test-key"),
			},
			expectError: false,
		},
		{
			name: "with tls",
			config: QdrantConfig{
				Host:   *schemas.NewEnvVar("localhost"),
				Port:   *schemas.NewEnvVar("6334"),
				UseTLS: *schemas.NewEnvVar("true"),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := newQdrantStore(ctx, &tt.config, logger)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, store)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				if err != nil {
					assert.Contains(t, err.Error(), "failed to connect")
				}
			}
		})
	}
}

func TestParsePointID(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		expectError bool
	}{
		{
			name:        "valid UUID",
			id:          "550e8400-e29b-41d4-a716-446655440000",
			expectError: false,
		},
		{
			name:        "invalid UUID",
			id:          "not-a-uuid",
			expectError: true,
		},
		{
			name:        "empty string",
			id:          "",
			expectError: true,
		},
		{
			name:        "numeric string",
			id:          "12345",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pointID, err := parsePointID(tt.id)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, pointID)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, pointID)
			}
		})
	}
}

func TestBuildQdrantFilter(t *testing.T) {
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
			name: "multiple queries (AND)",
			queries: []Query{
				{Field: "category", Operator: QueryOperatorEqual, Value: "tech"},
				{Field: "public", Operator: QueryOperatorEqual, Value: true},
			},
			expected: true,
		},
		{
			name: "null checks",
			queries: []Query{
				{Field: "author", Operator: QueryOperatorIsNull, Value: nil},
			},
			expected: true,
		},
		{
			name: "not null checks",
			queries: []Query{
				{Field: "author", Operator: QueryOperatorIsNotNull, Value: nil},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildQdrantFilter(tt.queries)

			if tt.expected {
				assert.NotNil(t, result)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

func TestQdrantStore_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewQdrantTestSetup(t)
	defer setup.Cleanup(t)

	err := setup.Store.Ping(setup.ctx)
	require.NoError(t, err)

	key := generateUUID()
	err = setup.Store.Add(setup.ctx, QdrantTestCollection, key, generateTestEmbedding(QdrantTestDimension), map[string]interface{}{"type": "document"})
	require.NoError(t, err)

	result, err := setup.Store.GetChunk(setup.ctx, QdrantTestCollection, key)
	require.NoError(t, err)
	assert.Equal(t, "document", result.Properties["type"])

	keys := []string{generateUUID(), generateUUID()}
	for i, k := range keys {
		err = setup.Store.Add(setup.ctx, QdrantTestCollection, k, generateTestEmbedding(QdrantTestDimension), map[string]interface{}{"type": i})
		require.NoError(t, err)
	}

	results, err := setup.Store.GetChunks(setup.ctx, QdrantTestCollection, keys)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestQdrantStore_Filtering(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewQdrantTestSetup(t)
	defer setup.Cleanup(t)

	for i := 0; i < 3; i++ {
		metadata := map[string]interface{}{"type": "pdf", "public": true}
		if i == 1 {
			metadata["type"] = "docx"
			metadata["public"] = false
		}
		err := setup.Store.Add(setup.ctx, QdrantTestCollection, generateUUID(), generateTestEmbedding(QdrantTestDimension), metadata)
		require.NoError(t, err)
	}

	queries := []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "pdf"}}
	results, _, err := setup.Store.GetAll(setup.ctx, QdrantTestCollection, queries, []string{"type"}, nil, 10)
	require.NoError(t, err)
	assert.Len(t, results, 2)

	multiQuery := []Query{
		{Field: "type", Operator: QueryOperatorEqual, Value: "pdf"},
		{Field: "public", Operator: QueryOperatorEqual, Value: true},
	}
	results, _, err = setup.Store.GetAll(setup.ctx, QdrantTestCollection, multiQuery, []string{"type"}, nil, 10)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestQdrantStore_VectorSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewQdrantTestSetup(t)
	defer setup.Cleanup(t)

	emb := generateTestEmbedding(QdrantTestDimension)
	err := setup.Store.Add(setup.ctx, QdrantTestCollection, generateUUID(), emb, map[string]interface{}{"type": "tech"})
	require.NoError(t, err)

	err = setup.Store.Add(setup.ctx, QdrantTestCollection, generateUUID(), generateTestEmbedding(QdrantTestDimension), map[string]interface{}{"type": "sports"})
	require.NoError(t, err)

	results, err := setup.Store.GetNearest(setup.ctx, QdrantTestCollection, emb, nil, []string{"type"}, 0.1, 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)
	require.NotNil(t, results[0].Score)

	queries := []Query{{Field: "type", Operator: QueryOperatorEqual, Value: "tech"}}
	results, err = setup.Store.GetNearest(setup.ctx, QdrantTestCollection, emb, queries, []string{"type"}, 0.1, 10)
	require.NoError(t, err)
	for _, result := range results {
		assert.Equal(t, "tech", result.Properties["type"])
	}
}

func TestQdrantStore_InterfaceCompliance(t *testing.T) {
	var _ VectorStore = (*QdrantStore)(nil)
}

func TestVectorStoreFactory_Qdrant(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)

	host := schemas.NewEnvVar(getEnvWithDefault("QDRANT_HOST", QdrantTestDefaultHost))
	port := schemas.NewEnvVar(getEnvWithDefault("QDRANT_PORT", QdrantTestDefaultPort))
	apiKey := schemas.NewEnvVar(os.Getenv("QDRANT_API_KEY"))

	config := &Config{
		Enabled: true,
		Type:    VectorStoreTypeQdrant,
		Config: QdrantConfig{
			Host:   *host,
			Port:   *port,
			APIKey: *apiKey,
		},
	}

	store, err := NewVectorStore(context.Background(), config, logger)
	if err != nil {
		t.Skipf("Could not create Qdrant store: %v", err)
	}
	defer store.Close(context.Background(), QdrantTestCollection)

	qdrantStore, ok := store.(*QdrantStore)
	assert.True(t, ok)
	assert.NotNil(t, qdrantStore)
}

func TestQdrantStore_DimensionHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewQdrantTestSetup(t)
	defer setup.Cleanup(t)

	testCollection := "TestDim"
	props := map[string]VectorStoreProperties{"type": {DataType: VectorStorePropertyTypeString}}

	err := setup.Store.CreateNamespace(setup.ctx, testCollection, 512, props)
	require.NoError(t, err)

	err = setup.Store.Add(setup.ctx, testCollection, generateUUID(), generateTestEmbedding(512), map[string]interface{}{"type": "test"})
	require.NoError(t, err)

	err = setup.Store.DeleteNamespace(setup.ctx, testCollection)
	require.NoError(t, err)

	err = setup.Store.CreateNamespace(setup.ctx, testCollection, QdrantTestDimension, props)
	require.NoError(t, err)

	emb := generateTestEmbedding(QdrantTestDimension)
	err = setup.Store.Add(setup.ctx, testCollection, generateUUID(), emb, map[string]interface{}{"type": "test"})
	require.NoError(t, err)

	results, err := setup.Store.GetNearest(setup.ctx, testCollection, emb, nil, []string{"type"}, 0.8, 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)

	setup.Store.DeleteNamespace(setup.ctx, testCollection)
}

func TestQdrantStore_ErrorHandling(t *testing.T) {
	setup := NewQdrantTestSetup(t)
	defer setup.Cleanup(t)

	_, err := setup.Store.GetChunk(setup.ctx, QdrantTestCollection, generateUUID())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	err = setup.Store.Add(setup.ctx, QdrantTestCollection, "", generateTestEmbedding(QdrantTestDimension), map[string]interface{}{"type": "test"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")

	err = setup.Store.Add(setup.ctx, QdrantTestCollection, "not-a-uuid", generateTestEmbedding(QdrantTestDimension), map[string]interface{}{"type": "test"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid id format")

	err = setup.Store.Delete(setup.ctx, QdrantTestCollection, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")

	err = setup.Store.Delete(setup.ctx, QdrantTestCollection, "not-a-uuid")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid id format")
}
