package vectorstore

import (
	"context"
	"maps"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/qdrant/go-client/qdrant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
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
	host := schemas.NewSecretVar(getEnvWithDefault("QDRANT_HOST", QdrantTestDefaultHost))
	port := schemas.NewSecretVar(getEnvWithDefault("QDRANT_PORT", QdrantTestDefaultPort))
	apiKey := schemas.NewSecretVar(os.Getenv("QDRANT_API_KEY"))
	useTLS := schemas.NewSecretVar(os.Getenv("QDRANT_USE_TLS"))

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
				Host: *schemas.NewSecretVar("localhost"),
				Port: *schemas.NewSecretVar("6334"),
			},
			expectError: false,
		},
		{
			name: "missing host",
			config: QdrantConfig{
				Port: *schemas.NewSecretVar("6334"),
			},
			expectError: true,
			errorMsg:    "qdrant host is required",
		},
		{
			name: "missing port uses default",
			config: QdrantConfig{
				Host: *schemas.NewSecretVar("localhost"),
			},
			expectError: false, // Port defaults to 6334 via CoerceInt fallback
		},
		{
			name: "with api key",
			config: QdrantConfig{
				Host:   *schemas.NewSecretVar("cluster.qdrant.io"),
				Port:   *schemas.NewSecretVar("6334"),
				APIKey: *schemas.NewSecretVar("test-key"),
			},
			expectError: false,
		},
		{
			name: "with tls",
			config: QdrantConfig{
				Host:   *schemas.NewSecretVar("localhost"),
				Port:   *schemas.NewSecretVar("6334"),
				UseTLS: *schemas.NewSecretVar("true"),
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

	host := schemas.NewSecretVar(getEnvWithDefault("QDRANT_HOST", QdrantTestDefaultHost))
	port := schemas.NewSecretVar(getEnvWithDefault("QDRANT_PORT", QdrantTestDefaultPort))
	apiKey := schemas.NewSecretVar(os.Getenv("QDRANT_API_KEY"))

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
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

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

// TestQdrantStore_CreateNamespaceRejectsDimensionChange pins the guard every store shares and the
// sentinel callers use to tell a configuration mistake apart from a store failure.
func TestQdrantStore_CreateNamespaceRejectsDimensionChange(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewQdrantTestSetup(t)
	defer setup.Cleanup(t)

	collection := "TestDimGuard"
	props := map[string]VectorStoreProperties{"type": {DataType: VectorStorePropertyTypeString}}
	_ = setup.Store.DeleteNamespace(setup.ctx, collection)
	defer func() { _ = setup.Store.DeleteNamespace(setup.ctx, collection) }()

	require.NoError(t, setup.Store.CreateNamespace(setup.ctx, collection, 512, props))
	require.NoError(t, setup.Store.CreateNamespace(setup.ctx, collection, 512, props))

	err := setup.Store.CreateNamespace(setup.ctx, collection, 1024, props)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDimensionMismatch)
	assert.Contains(t, err.Error(), "already exists with dimension 512 but config requires 1024")
}

// TestQdrantStore_GetNearestScoresCosineSimilarity pins GetNearest's threshold and score to cosine
// similarity, the scale every store uses.
func TestQdrantStore_GetNearestScoresCosineSimilarity(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewQdrantTestSetup(t)
	defer setup.Cleanup(t)

	collection := "TestCosineScore"
	props := map[string]VectorStoreProperties{"label": {DataType: VectorStorePropertyTypeString}}
	_ = setup.Store.DeleteNamespace(setup.ctx, collection)
	defer func() { _ = setup.Store.DeleteNamespace(setup.ctx, collection) }()
	require.NoError(t, setup.Store.CreateNamespace(setup.ctx, collection, 2, props))

	require.NoError(t, setup.Store.Add(setup.ctx, collection, generateUUID(), unitVectorAtCosine(2, 0.9), map[string]interface{}{"label": "close"}))
	require.NoError(t, setup.Store.Add(setup.ctx, collection, generateUUID(), unitVectorAtCosine(2, 0.7), map[string]interface{}{"label": "far"}))

	results, err := setup.Store.GetNearest(setup.ctx, collection, unitVectorAtCosine(2, 1), nil, []string{"label"}, 0.8, 10)
	require.NoError(t, err)
	require.Len(t, results, 1, "only the vector at cosine 0.9 clears a 0.8 threshold")
	assert.Equal(t, "close", results[0].Properties["label"])
	require.NotNil(t, results[0].Score)
	assert.InDelta(t, 0.9, *results[0].Score, 1e-3)
}

// TestQdrantStore_DeleteMissingIDSucceeds pins deleting an id that was never written as a no-op,
// the contract every store shares so clearing an unknown cache entry is not a server error.
func TestQdrantStore_DeleteMissingIDSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	setup := NewQdrantTestSetup(t)
	defer setup.Cleanup(t)

	props := map[string]VectorStoreProperties{"type": {DataType: VectorStorePropertyTypeString}}
	require.NoError(t, setup.Store.CreateNamespace(setup.ctx, QdrantTestCollection, QdrantTestDimension, props))
	assert.NoError(t, setup.Store.Delete(setup.ctx, QdrantTestCollection, generateUUID()))
}

type qdrantNamespaceCalls struct {
	mu          sync.Mutex
	collections []*qdrant.CreateCollection
	indexes     []*qdrant.CreateFieldIndexCollection
}

// Exercise the concrete client without a server dependency or a production
// client abstraction. The interceptor records provisioning RPCs, including
// failed index attempts, and rejects unexpected operations such as deletion.
func newQdrantNamespaceRPCFixture(t *testing.T, exists bool, dimension int, failMethod string) (*QdrantStore, *qdrantNamespaceCalls) {
	t.Helper()
	calls := &qdrantNamespaceCalls{}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(_ context.Context, req interface{}, info *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (interface{}, error) {
		calls.mu.Lock()
		defer calls.mu.Unlock()
		switch request := req.(type) {
		case *qdrant.CreateCollection:
			calls.collections = append(calls.collections, request)
		case *qdrant.CreateFieldIndexCollection:
			calls.indexes = append(calls.indexes, request)
		}
		if info.FullMethod == failMethod {
			return nil, status.Error(codes.Unavailable, "provisioning unavailable")
		}
		switch info.FullMethod {
		case qdrant.Collections_CollectionExists_FullMethodName:
			return &qdrant.CollectionExistsResponse{Result: &qdrant.CollectionExists{Exists: exists}}, nil
		case qdrant.Collections_Get_FullMethodName:
			return &qdrant.GetCollectionInfoResponse{Result: &qdrant.CollectionInfo{Config: &qdrant.CollectionConfig{Params: &qdrant.CollectionParams{
				VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{Size: uint64(dimension), Distance: qdrant.Distance_Cosine}),
			}}}}, nil
		case qdrant.Collections_Create_FullMethodName:
			return &qdrant.CollectionOperationResponse{Result: true}, nil
		case qdrant.Points_CreateFieldIndex_FullMethodName:
			return &qdrant.PointsOperationResponse{Result: &qdrant.UpdateResult{Status: qdrant.UpdateStatus_Completed}}, nil
		default:
			t.Errorf("unexpected Qdrant RPC: %s", info.FullMethod)
			return nil, status.Error(codes.Unimplemented, "unexpected provisioning operation")
		}
	}))
	qdrant.RegisterCollectionsServer(server, &qdrant.UnimplementedCollectionsServer{})
	qdrant.RegisterPointsServer(server, &qdrant.UnimplementedPointsServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	client, err := qdrant.NewClient(&qdrant.Config{
		Host: "localhost", Port: 6334, PoolSize: 1, SkipCompatibilityCheck: true,
		GrpcOptions: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		})},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, client.Close()) })
	return &QdrantStore{client: client, logger: bifrost.NewDefaultLogger(schemas.LogLevelError)}, calls
}

func TestQdrantStore_CreateNamespacePayloadIndexPolicy(t *testing.T) {
	filterable := map[string]VectorStoreProperties{
		"label": {DataType: VectorStorePropertyTypeString, Filterable: true},
		"count": {DataType: VectorStorePropertyTypeInteger, Filterable: true},
		"flag":  {DataType: VectorStorePropertyTypeBoolean, Filterable: true},
		"tags":  {DataType: VectorStorePropertyTypeStringArray, Filterable: true},
	}
	expected := map[string]qdrant.FieldType{
		"label": qdrant.FieldType_FieldTypeKeyword,
		"count": qdrant.FieldType_FieldTypeInteger,
		"flag":  qdrant.FieldType_FieldTypeBool,
		"tags":  qdrant.FieldType_FieldTypeKeyword,
	}
	mixed := maps.Clone(filterable)
	mixed["response"] = VectorStoreProperties{DataType: VectorStorePropertyTypeString}
	mixed["stream_chunks"] = VectorStoreProperties{DataType: VectorStorePropertyTypeStringArray}
	mixed["expires_at"] = VectorStoreProperties{DataType: VectorStorePropertyTypeInteger}
	mixed["payload_flag"] = VectorStoreProperties{DataType: VectorStorePropertyTypeBoolean}
	legacy := maps.Clone(filterable)
	for field, prop := range legacy {
		prop.Filterable = false
		legacy[field] = prop
	}
	tests := []struct {
		name       string
		properties map[string]VectorStoreProperties
		indexes    map[string]qdrant.FieldType
	}{
		{"mixed", mixed, expected},
		{"all filterable", filterable, expected},
		{"legacy unannotated", legacy, expected},
		{"empty", map[string]VectorStoreProperties{}, map[string]qdrant.FieldType{}},
		{"nil", nil, map[string]qdrant.FieldType{}},
	}
	for _, tt := range tests {
		for _, exists := range []bool{false, true} {
			name := "new"
			if exists {
				name = "existing"
			}
			t.Run(tt.name+"/"+name, func(t *testing.T) {
				store, calls := newQdrantNamespaceRPCFixture(t, exists, 3, "")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, store.CreateNamespace(ctx, "payload-policy", 3, tt.properties))
				calls.mu.Lock()
				defer calls.mu.Unlock()
				if exists {
					assert.Empty(t, calls.collections)
				} else {
					require.Len(t, calls.collections, 1)
					assert.Equal(t, "payload-policy", calls.collections[0].CollectionName)
					assert.Equal(t, uint64(3), calls.collections[0].VectorsConfig.GetParams().Size)
					assert.Equal(t, qdrant.Distance_Cosine, calls.collections[0].VectorsConfig.GetParams().Distance)
				}
				actual := make(map[string]qdrant.FieldType)
				for _, index := range calls.indexes {
					assert.Equal(t, "payload-policy", index.CollectionName)
					actual[index.FieldName] = index.GetFieldType()
				}
				assert.Len(t, calls.indexes, len(tt.indexes))
				assert.Equal(t, tt.indexes, actual)
			})
		}
	}
}

func TestQdrantStore_CreateNamespaceAddsFilterableIndexesOnRepeat(t *testing.T) {
	store, calls := newQdrantNamespaceRPCFixture(t, true, 3, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	properties := map[string]VectorStoreProperties{
		"cache_key": {DataType: VectorStorePropertyTypeString, Filterable: true},
		"response":  {DataType: VectorStorePropertyTypeString},
	}
	require.NoError(t, store.CreateNamespace(ctx, "payload-policy", 3, properties))
	properties["provider"] = VectorStoreProperties{DataType: VectorStorePropertyTypeString, Filterable: true}
	require.NoError(t, store.CreateNamespace(ctx, "payload-policy", 3, properties))
	calls.mu.Lock()
	defer calls.mu.Unlock()
	require.Len(t, calls.indexes, 3)
	fields := make([]string, 0, len(calls.indexes))
	for _, index := range calls.indexes {
		fields = append(fields, index.FieldName)
	}
	assert.ElementsMatch(t, []string{"cache_key", "cache_key", "provider"}, fields)
}

func TestQdrantStore_CreateNamespaceProvisioningErrors(t *testing.T) {
	tests := []struct {
		name       string
		exists     bool
		dimension  int
		failMethod string
		errorMsg   string
		indexes    int
	}{
		{"existence failure", false, 3, qdrant.Collections_CollectionExists_FullMethodName, "failed to check collection existence", 0},
		{"creation failure", false, 3, qdrant.Collections_Create_FullMethodName, "failed to create collection", 0},
		{"dimension mismatch", true, 4, "", "already exists with dimension 4 but config requires 3", 0},
		{"inspection failure remains best effort", true, 3, qdrant.Collections_Get_FullMethodName, "", 1},
		{"index failure remains best effort", true, 3, qdrant.Points_CreateFieldIndex_FullMethodName, "", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, calls := newQdrantNamespaceRPCFixture(t, tt.exists, tt.dimension, tt.failMethod)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := store.CreateNamespace(ctx, "payload-policy", 3, map[string]VectorStoreProperties{
				"cache_key": {DataType: VectorStorePropertyTypeString, Filterable: true},
				"response":  {DataType: VectorStorePropertyTypeString},
			})
			if tt.errorMsg != "" {
				require.ErrorContains(t, err, tt.errorMsg)
				if tt.name == "dimension mismatch" {
					assert.ErrorIs(t, err, ErrDimensionMismatch)
				}
			} else {
				require.NoError(t, err)
			}
			calls.mu.Lock()
			defer calls.mu.Unlock()
			assert.Len(t, calls.indexes, tt.indexes)
		})
	}
}

// Legacy callers such as Warp and semantic routing do not annotate their
// filter fields. Keep their indexes, including on strict-mode collections.
func TestQdrantStore_LegacyPayloadFiltersInStrictMode(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}
	setup := NewQdrantTestSetup(t)
	t.Cleanup(func() { setup.Cleanup(t) })
	namespace := "payload-policy-legacy-" + generateUUID()
	t.Cleanup(func() { assert.NoError(t, setup.Store.DeleteNamespace(context.Background(), namespace)) })
	require.NoError(t, setup.Store.CreateNamespace(setup.ctx, namespace, 3, map[string]VectorStoreProperties{
		"kind":     {DataType: VectorStorePropertyTypeString},
		"warp_log": {DataType: VectorStorePropertyTypeBoolean},
	}))
	id := generateUUID()
	require.NoError(t, setup.Store.Add(setup.ctx, namespace, id, []float32{1, 0, 0}, map[string]interface{}{"kind": "example", "warp_log": true}))
	require.NoError(t, setup.Store.client.UpdateCollection(setup.ctx, &qdrant.UpdateCollection{CollectionName: namespace, StrictModeConfig: &qdrant.StrictModeConfig{
		Enabled: qdrant.PtrOf(true), UnindexedFilteringRetrieve: qdrant.PtrOf(false), UnindexedFilteringUpdate: qdrant.PtrOf(false),
	}}))
	for _, query := range []Query{{Field: "kind", Operator: QueryOperatorEqual, Value: "example"}, {Field: "warp_log", Operator: QueryOperatorEqual, Value: true}} {
		results, err := setup.Store.GetNearest(setup.ctx, namespace, []float32{1, 0, 0}, []Query{query}, nil, 0.8, 1)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, id, results[0].ID)
	}
}
