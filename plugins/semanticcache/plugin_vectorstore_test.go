package semanticcache

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// TestVectorStoreUnifiedOperations tests the unified VectorStore operations
func TestVectorStoreUnifiedOperations(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := context.Background()

	// Get the internal store for testing
	pluginImpl := setup.Plugin.(*Plugin)
	store := pluginImpl.store

	t.Log("Testing unified VectorStore operations...")

	// Test data for unified VectorEntry
	testEmbedding := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	unifiedMetadata := map[string]interface{}{
		"provider":     "openai",
		"model":        "gpt-4o-mini",
		"request_hash": "test-hash-123",
		"cache_key":    "test-cache-key-123",
		"response":     `{"choices":[{"message":{"role":"assistant","content":"Hello!"}}]}`,
		"params": map[string]interface{}{
			"temperature": 0.7,
			"max_tokens":  100,
		},
	}

	// Test 1: Add unified entry
	t.Log("Testing unified entry storage...")
	entryID := uuid.New().String()

	err := store.Add(ctx, entryID, testEmbedding, unifiedMetadata)
	if err != nil {
		t.Fatalf("Failed to add unified entry: %v", err)
	}
	t.Log("✅ Unified entry stored successfully")

	// Test 2: Query with filters (simulating direct hash search)
	t.Log("Testing query with filters...")
	filters := []vectorstore.Query{
		{Field: "provider", Operator: vectorstore.QueryOperatorEqual, Value: "openai"},
		{Field: "model", Operator: vectorstore.QueryOperatorEqual, Value: "gpt-4o-mini"},
		{Field: "request_hash", Operator: vectorstore.QueryOperatorEqual, Value: "test-hash-123"},
	}

	results, _, err := store.GetAll(ctx, filters, SelectFields, nil, 10)
	if err != nil {
		t.Fatalf("GetAll with filters failed: %v", err)
	} else {
		t.Logf("✅ GetAll with filters returned %d results", len(results))
		if len(results) > 0 {
			t.Logf("Found entry with ID: %s", results[0].ID)
			if results[0].Properties != nil {
				if hash, exists := results[0].Properties["request_hash"]; exists {
					t.Logf("Entry has request_hash: %v", hash)
				}
			}
		}
	}

	// Test 3: Semantic search with embedding
	t.Log("Testing semantic search with embedding...")
	queryEmbedding := []float32{0.1, 0.2, 0.3, 0.4, 0.5} // Same embedding
	semanticFilters := []vectorstore.Query{
		{Field: "provider", Operator: vectorstore.QueryOperatorEqual, Value: "openai"},
		{Field: "model", Operator: vectorstore.QueryOperatorEqual, Value: "gpt-4o-mini"},
	}

	nearestResults, err := store.GetNearest(ctx, queryEmbedding, semanticFilters, SelectFields, 0.8, 5)
	if err != nil {
		t.Fatalf("GetNearest failed: %v", err)
	} else {
		t.Logf("✅ GetNearest returned %d results", len(nearestResults))
		for i, result := range nearestResults {
			t.Logf("Result %d: ID=%s, Score=%f", i, result.ID, *result.Score)
		}
	}

	// Test 4: Delete entry
	t.Log("Testing entry deletion...")
	err = store.Delete(ctx, entryID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	} else {
		t.Log("✅ Entry deleted successfully")
	}

	t.Log("🎉 VectorStore unified operations test completed!")
}

// TestVectorStoreBasicConnectivity tests basic connectivity to the vector store
func TestVectorStoreBasicConnectivity(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := context.Background()
	pluginImpl := setup.Plugin.(*Plugin)
	store := pluginImpl.store

	t.Log("Testing basic vector store connectivity...")

	// Test basic Add operation (this should work)
	testID := uuid.New().String()
	testEmbedding := []float32{0.5, 0.5, 0.5, 0.5, 0.5} // Use 5 dimensions consistently
	testMetadata := map[string]interface{}{
		"test": "connectivity",
	}

	err := store.Add(ctx, testID, testEmbedding, testMetadata)
	if err != nil {
		t.Fatalf("Basic Add operation failed - vector store connectivity issue: %v", err)
	}

	t.Log("✅ Basic connectivity test passed - vector store is accessible")

	// Clean up
	_ = store.Delete(ctx, testID)
	t.Log("🎉 Basic connectivity test completed!")
}
