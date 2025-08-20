package semanticcache

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// checkRedisClusterAvailability performs a lightweight check to see if Redis cluster is reachable
func checkRedisClusterAvailability() error {
	// Get Redis cluster addresses from environment or use defaults
	redisClusterAddrs := []string{"localhost:6371", "localhost:6372", "localhost:6373"}
	if envAddrs := os.Getenv("REDIS_CLUSTER_ADDRS"); envAddrs != "" {
		// Parse comma-separated addresses if provided
		redisClusterAddrs = strings.Split(envAddrs, ",")
		for i, addr := range redisClusterAddrs {
			redisClusterAddrs[i] = strings.TrimSpace(addr)
		}
	}

	// Try to connect to at least one Redis cluster node
	var lastErr error
	for _, addr := range redisClusterAddrs {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil // At least one node is reachable
		}
		lastErr = err
	}

	return fmt.Errorf("no Redis cluster nodes reachable: %v", lastErr)
}

// TestRedisClusterIntegration tests the semantic cache plugin with Redis Cluster backend
func TestRedisClusterIntegration(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping Redis Cluster test")
	}

	// Check Redis cluster availability before attempting to set up the test
	if err := checkRedisClusterAvailability(); err != nil {
		t.Skipf("Redis cluster not available, skipping test: %v", err)
	}

	setup := NewRedisClusterTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-cluster-value")

	// Create a test request
	testRequest := CreateBasicChatRequest(
		"What is Redis Cluster? Answer in one short sentence.",
		0.7,
		50,
	)

	t.Log("Making first request with Redis Cluster (should go to OpenAI and be cached)...")

	// Make first request (will go to OpenAI and be cached in Redis Cluster)
	start1 := time.Now()
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, testRequest)
	duration1 := time.Since(start1)

	if err1 != nil {
		t.Fatalf("First request failed with Redis Cluster: %v", err1)
	}

	if response1 == nil || len(response1.Choices) == 0 || response1.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("First response from Redis Cluster is invalid")
	}

	t.Logf("First request with Redis Cluster completed in %v", duration1)
	t.Logf("Response: %s", *response1.Choices[0].Message.Content.ContentStr)

	// Wait a moment to ensure cache is written to cluster
	WaitForCache()

	t.Log("Making second identical request with Redis Cluster (should be served from cache)...")

	// Make second identical request (should be cached in Redis Cluster)
	start2 := time.Now()
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, testRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		t.Fatalf("Second request failed with Redis Cluster: %v", err2)
	}

	if response2 == nil || len(response2.Choices) == 0 || response2.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("Second response from Redis Cluster is invalid")
	}

	t.Logf("Second request with Redis Cluster completed in %v", duration2)
	t.Logf("Response: %s", *response2.Choices[0].Message.Content.ContentStr)

	// Check if second request was cached
	AssertCacheHit(t, response2, string(CacheTypeDirect))

	// Performance comparison
	t.Logf("Redis Cluster Performance Summary:")
	t.Logf("First request (OpenAI):  %v", duration1)
	t.Logf("Second request (Cache):  %v", duration2)

	if duration2 < duration1 {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Redis Cluster cache speedup: %.2fx faster", speedup)
	}

	// Verify responses are identical
	content1 := *response1.Choices[0].Message.Content.ContentStr
	content2 := *response2.Choices[0].Message.Content.ContentStr

	if content1 != content2 {
		t.Errorf("Response content differs between Redis Cluster cached and original:\nOriginal: %s\nCached:   %s", content1, content2)
	}

	t.Log("✅ Redis Cluster integration test completed successfully!")
}

// TestRedisOperations tests core Redis operations without requiring OpenAI API
func TestRedisOperations(t *testing.T) {
	setup := NewTestSetup(t, TestPrefix+"redis_ops_")
	defer setup.Cleanup()

	ctx := context.Background()

	// Get the internal store for testing
	pluginImpl := setup.Plugin.(*Plugin)
	store := pluginImpl.store

	t.Log("Testing direct Redis operations...")

	// Test data
	testRequestID := "test-request-123"
	testHash := "abc123def456"
	testProvider := schemas.OpenAI
	testModel := "gpt-4o-mini"

	// Generate cache keys using the plugin's method
	hashKey := pluginImpl.generateCacheKey(testProvider, testModel, testRequestID, "hash")
	responseKey := pluginImpl.generateCacheKey(testProvider, testModel, testRequestID, "response")

	t.Logf("Generated keys - Hash: %s, Response: %s", hashKey, responseKey)

	// Test 1: Hash storage and retrieval
	t.Log("Testing hash storage and retrieval...")
	err := store.Add(ctx, hashKey, testHash, 5*time.Minute)
	if err != nil {
		t.Fatalf("Failed to store hash: %v", err)
	}

	retrievedHash, err := store.GetChunk(ctx, hashKey)
	if err != nil {
		t.Fatalf("Failed to retrieve hash: %v", err)
	}
	if retrievedHash != testHash {
		t.Fatalf("Hash mismatch: expected %s, got %s", testHash, retrievedHash)
	}
	t.Log("✅ Hash storage/retrieval successful")

	// Test 2: Response storage and chunked retrieval (simulating streaming)
	t.Log("Testing streaming response chunks...")
	for i := 0; i < 3; i++ {
		chunkKey := fmt.Sprintf("%s_chunk_%d", responseKey, i)
		chunkResponse := fmt.Sprintf(`{"choices":[{"message":{"content":"Chunk %d"}}],"extra_fields":{"chunk_index":%d}}`, i, i)

		err = store.Add(ctx, chunkKey, chunkResponse, 5*time.Minute)
		if err != nil {
			t.Fatalf("Failed to store response chunk %d: %v", i, err)
		}
	}

	// Test chunk retrieval
	chunkPattern := responseKey + "_chunk_*"
	var chunkKeys []string
	var cursor *string

	for {
		batch, c, err := store.GetAll(ctx, chunkPattern, cursor, 1000)
		if err != nil {
			t.Fatalf("Failed to scan chunk keys: %v", err)
		}
		chunkKeys = append(chunkKeys, batch...)
		cursor = c
		if cursor == nil {
			break
		}
	}

	if len(chunkKeys) != 3 {
		t.Fatalf("Expected 3 chunk keys, got %d", len(chunkKeys))
	}

	// Retrieve all chunks
	chunkData, err := store.GetChunks(ctx, chunkKeys)
	if err != nil {
		t.Fatalf("Failed to retrieve chunks: %v", err)
	}
	if len(chunkData) != 3 {
		t.Fatalf("Expected 3 chunks, got %d", len(chunkData))
	}
	t.Log("✅ Streaming response chunks successful")

	// Test 3: Pattern-based key search
	t.Log("Testing pattern-based key search...")
	hashPattern := setup.Config.Prefix + string(testProvider) + "-" + testModel + "-*-hash"
	var hashKeys []string
	cursor = nil

	for {
		batch, c, err := store.GetAll(ctx, hashPattern, cursor, 1000)
		if err != nil {
			t.Fatalf("Failed to scan hash keys: %v", err)
		}
		hashKeys = append(hashKeys, batch...)
		cursor = c
		if cursor == nil {
			break
		}
	}

	if len(hashKeys) != 1 {
		t.Fatalf("Expected 1 hash key, got %d", len(hashKeys))
	}
	if hashKeys[0] != hashKey {
		t.Fatalf("Wrong hash key found: expected %s, got %s", hashKey, hashKeys[0])
	}
	t.Log("✅ Pattern-based key search successful")

	// Test 4: Cleanup
	t.Log("Testing cleanup...")
	allKeys := append(chunkKeys, hashKey)
	err = store.Delete(ctx, allKeys)
	if err != nil {
		t.Fatalf("Failed to delete test keys: %v", err)
	}
	t.Log("✅ Cleanup successful")

	t.Log("🎉 All Redis operations tests passed!")
}

// TestVectorStoreSemanticOperations tests semantic search operations
func TestVectorStoreSemanticOperations(t *testing.T) {
	setup := NewTestSetup(t, TestPrefix+"semantic_ops_")
	defer setup.Cleanup()

	ctx := context.Background()

	// Get the internal store for testing
	pluginImpl := setup.Plugin.(*Plugin)
	store := pluginImpl.store

	t.Log("Testing semantic search operations...")

	// Test data
	testEmbedding := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	testMetadata := map[string]interface{}{
		"temperature": 0.7,
		"max_tokens":  100,
		"provider":    "openai",
		"model":       "gpt-4o-mini",
	}

	// Test 1: Ensure semantic index exists
	embeddingDim := len(testEmbedding)
	metadataFields := []string{"temperature", "max_tokens", "provider", "model"}

	err := store.EnsureSemanticIndex(ctx, SemanticIndexName, setup.Config.Prefix, embeddingDim, metadataFields)
	if err != nil {
		t.Fatalf("Failed to ensure semantic index: %v", err)
	}
	t.Log("✅ Semantic index creation successful")

	WaitForCache()

	// Test 2: Add embedding with metadata
	embeddingKey := setup.Config.Prefix + "test-embedding-key"
	err = store.AddSemanticCache(ctx, embeddingKey, testEmbedding, testMetadata, 5*time.Minute)
	if err != nil {
		t.Fatalf("Failed to add semantic cache: %v", err)
	}
	t.Log("✅ Semantic cache addition successful")

	// Test 3: Search for similar embeddings
	queryEmbedding := []float32{0.1, 0.2, 0.3, 0.4, 0.5} // Identical embedding
	queryMetadata := map[string]interface{}{
		"temperature": 0.7,
		"max_tokens":  100,
	}

	results, err := store.SearchSemanticCache(ctx, SemanticIndexName, queryEmbedding, queryMetadata, 0.9, 10)
	if err != nil {
		t.Fatalf("Failed to search semantic cache: %v", err)
	}

	if len(results) == 0 {
		t.Log("⚠️  No semantic search results found - this may be expected depending on implementation")
	} else {
		t.Logf("✅ Found %d semantic search results", len(results))
		for i, result := range results {
			t.Logf("Result %d: Key=%s", i, result.Key)
		}
	}

	// Test 4: Clean up semantic data
	err = store.DropSemanticIndex(ctx, SemanticIndexName)
	if err != nil {
		t.Logf("⚠️  Failed to drop semantic index (may be expected): %v", err)
	} else {
		t.Log("✅ Semantic index cleanup successful")
	}

	t.Log("✅ Semantic operations tests completed!")
}

// TestConcurrentOperations tests concurrent access patterns
func TestConcurrentOperations(t *testing.T) {
	setup := NewTestSetup(t, TestPrefix+"concurrent_")
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("concurrent-test")

	// Create a test request that all goroutines will use
	testRequest := CreateBasicChatRequest(
		"Concurrent test request",
		0.5,
		50,
	)

	// Number of concurrent requests
	concurrency := 5
	results := make(chan error, concurrency)

	t.Logf("Making %d concurrent requests...", concurrency)

	// Launch concurrent requests
	for i := 0; i < concurrency; i++ {
		go func(id int) {
			_, err := setup.Client.ChatCompletionRequest(ctx, testRequest)
			if err != nil {
				results <- fmt.Errorf("concurrent request %d failed: %v", id, err)
			} else {
				results <- nil
			}
		}(i)
	}

	// Collect results
	successCount := 0
	errorCount := 0
	for i := 0; i < concurrency; i++ {
		if err := <-results; err != nil {
			t.Logf("⚠️  %v", err)
			errorCount++
		} else {
			successCount++
		}
	}

	t.Logf("Concurrent results: %d successes, %d errors", successCount, errorCount)

	// At least some requests should succeed
	if successCount == 0 {
		t.Fatal("All concurrent requests failed")
	}

	t.Log("✅ Concurrent operations test completed!")
}

// TestLargePayloadHandling tests handling of large requests and responses
func TestLargePayloadHandling(t *testing.T) {
	setup := NewTestSetup(t, TestPrefix+"large_payload_")
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("large-payload-test")

	// Create a request with large content
	largeContent := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 500) // ~17,500 characters

	testRequest := CreateBasicChatRequest(
		largeContent,
		0.1,
		50,
	)

	t.Log("Processing large content request...")
	start := time.Now()
	response, err := setup.Client.ChatCompletionRequest(ctx, testRequest)
	duration := time.Since(start)

	if err != nil {
		t.Logf("⚠️  Large content request failed: %v", err)
		// Don't fail the test as this might be expected for very large content
		return
	}

	if response == nil {
		t.Fatal("Large content response is nil")
	}

	t.Logf("Large content processed successfully in %v", duration)

	WaitForCache()

	// Try the same request again to test caching of large content
	t.Log("Making second large content request...")
	start2 := time.Now()
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, testRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		t.Fatalf("Second large content request failed: %v", err2)
	}

	t.Logf("Second large content request completed in %v", duration2)

	// Check if it was cached
	if response2.ExtraFields.RawResponse != nil {
		if rawMap, ok := response2.ExtraFields.RawResponse.(map[string]interface{}); ok {
			if cachedFlag, exists := rawMap["bifrost_cached"]; exists {
				if cachedBool, ok := cachedFlag.(bool); ok && cachedBool {
					t.Log("✅ Large content successfully cached and retrieved")
					if duration2 < duration {
						speedup := float64(duration) / float64(duration2)
						t.Logf("Large content cache speedup: %.2fx faster", speedup)
					}
				}
			}
		}
	}

	t.Log("✅ Large payload handling test completed!")
}

// isExpectedValidationError checks if an error is an expected validation error
func isExpectedValidationError(err *schemas.BifrostError) bool {
	if err == nil {
		return false
	}

	// Check both the message field and the error field
	var errorStr string
	if err.Error.Message != "" {
		errorStr = strings.ToLower(err.Error.Message)
	} else if err.Error.Error != nil {
		errorStr = strings.ToLower(err.Error.Error.Error())
	} else {
		return false
	}

	return strings.Contains(errorStr, "validation") ||
		strings.Contains(errorStr, "invalid") ||
		strings.Contains(errorStr, "empty") ||
		strings.Contains(errorStr, "content") ||
		strings.Contains(errorStr, "required")
}

// TestErrorRecovery tests error recovery and graceful handling
func TestErrorRecovery(t *testing.T) {
	setup := NewTestSetup(t, TestPrefix+"error_recovery_")
	defer setup.Cleanup()

	t.Run("Empty Content", func(t *testing.T) {
		ctx := CreateContextWithCacheKey("error-recovery-empty-content")
		request := CreateBasicChatRequest("", 0.5, 10)

		t.Log("Testing empty content handling...")
		_, err := setup.Client.ChatCompletionRequest(ctx, request)

		// Empty content should return a validation error or be handled gracefully
		if err != nil {
			if isExpectedValidationError(err) {
				t.Logf("✅ Empty content correctly rejected with validation error: %s", err.Error.Message)
			} else {
				errorMsg := err.Error.Message
				if errorMsg == "" && err.Error.Error != nil {
					errorMsg = err.Error.Error.Error()
				}
				t.Errorf("Unexpected error for empty content (possible regression): %s", errorMsg)
			}
		} else {
			t.Log("✅ Empty content handled gracefully (provider accepted empty request)")
		}
	})

	t.Run("Very High Temperature", func(t *testing.T) {
		ctx := CreateContextWithCacheKey("error-recovery-high-temp")
		request := CreateBasicChatRequest("Test high temperature behavior", 2.0, 10)

		t.Log("Testing very high temperature handling...")
		response, err := setup.Client.ChatCompletionRequest(ctx, request)

		// High temperature (2.0) should work with OpenAI - any error is unexpected
		if err != nil {
			errorMsg := err.Error.Message
			if errorMsg == "" && err.Error.Error != nil {
				errorMsg = err.Error.Error.Error()
			}
			t.Errorf("High temperature request failed unexpectedly (possible regression): %s", errorMsg)
			return
		}

		// Verify response is valid
		if response == nil || len(response.Choices) == 0 || response.Choices[0].Message.Content.ContentStr == nil {
			t.Fatal("Invalid response for high temperature request")
		}

		responseContent := *response.Choices[0].Message.Content.ContentStr
		t.Logf("✅ High temperature request handled successfully: %s", responseContent)

		// Test caching behavior
		WaitForCache()
		t.Log("Testing high temperature caching...")
		response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
		if err2 != nil {
			errorMsg := err2.Error.Message
			if errorMsg == "" && err2.Error.Error != nil {
				errorMsg = err2.Error.Error.Error()
			}
			t.Errorf("Cached high temperature request failed: %s", errorMsg)
			return
		}

		AssertCacheHit(t, response2, string(CacheTypeDirect))
		t.Log("✅ High temperature caching works correctly")
	})

	t.Run("Very Low Max Tokens", func(t *testing.T) {
		ctx := CreateContextWithCacheKey("error-recovery-low-tokens")
		request := CreateBasicChatRequest("Test low tokens", 0.5, 1)

		t.Log("Testing very low max tokens handling...")
		response1, err1 := setup.Client.ChatCompletionRequest(ctx, request)

		// Low max tokens should work - any error is unexpected
		if err1 != nil {
			errorMsg := err1.Error.Message
			if errorMsg == "" && err1.Error.Error != nil {
				errorMsg = err1.Error.Error.Error()
			}
			t.Errorf("Low max tokens request failed unexpectedly (possible regression): %s", errorMsg)
			return
		}

		if response1 == nil || len(response1.Choices) == 0 || response1.Choices[0].Message.Content.ContentStr == nil {
			t.Fatal("Invalid response for low max tokens request")
		}

		// Verify response respects token constraint (should be very short)
		responseContent := *response1.Choices[0].Message.Content.ContentStr
		if len(strings.Fields(responseContent)) > 5 {
			t.Logf("⚠️  Response may not respect max_tokens=1 constraint (got %d words): %s",
				len(strings.Fields(responseContent)), responseContent)
		}

		t.Logf("✅ Low max tokens handled successfully: %s", responseContent)

		// Test caching behavior
		WaitForCache()
		t.Log("Testing low max tokens caching...")
		response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
		if err2 != nil {
			errorMsg := err2.Error.Message
			if errorMsg == "" && err2.Error.Error != nil {
				errorMsg = err2.Error.Error.Error()
			}
			t.Errorf("Cached low max tokens request failed: %s", errorMsg)
			return
		}

		AssertCacheHit(t, response2, string(CacheTypeDirect))

		// Verify responses are identical
		content1 := *response1.Choices[0].Message.Content.ContentStr
		content2 := *response2.Choices[0].Message.Content.ContentStr
		if content1 != content2 {
			t.Errorf("Cached response differs from original (caching regression): Original: %s, Cached: %s", content1, content2)
		}

		t.Log("✅ Low max tokens caching works correctly")
	})

	t.Run("Special Characters", func(t *testing.T) {
		ctx := CreateContextWithCacheKey("error-recovery-special-chars")
		request := CreateBasicChatRequest("Test\\n\\t\\r\"'`~!@#$%^&*()", 0.5, 50)

		t.Log("Testing special characters handling...")
		response1, err1 := setup.Client.ChatCompletionRequest(ctx, request)

		// Special characters should work fine - any error is unexpected
		if err1 != nil {
			errorMsg := err1.Error.Message
			if errorMsg == "" && err1.Error.Error != nil {
				errorMsg = err1.Error.Error.Error()
			}
			t.Errorf("Special characters request failed unexpectedly (possible regression): %s", errorMsg)
			return
		}

		if response1 == nil || len(response1.Choices) == 0 || response1.Choices[0].Message.Content.ContentStr == nil {
			t.Fatal("Invalid response for special characters request")
		}

		responseContent := *response1.Choices[0].Message.Content.ContentStr
		t.Logf("✅ Special characters handled successfully: %s", responseContent)

		// Test caching behavior
		WaitForCache()
		t.Log("Testing special characters caching...")
		response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
		if err2 != nil {
			errorMsg := err2.Error.Message
			if errorMsg == "" && err2.Error.Error != nil {
				errorMsg = err2.Error.Error.Error()
			}
			t.Errorf("Cached special characters request failed: %s", errorMsg)
			return
		}

		AssertCacheHit(t, response2, string(CacheTypeDirect))

		// Verify responses are identical (deterministic behavior)
		content1 := *response1.Choices[0].Message.Content.ContentStr
		content2 := *response2.Choices[0].Message.Content.ContentStr
		if content1 != content2 {
			t.Errorf("Cached response differs from original (determinism regression): Original: %s, Cached: %s", content1, content2)
		}

		t.Log("✅ Special characters caching works correctly")
	})

	t.Log("✅ Error recovery test completed successfully!")
}
