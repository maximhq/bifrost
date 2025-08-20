package semanticcache

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// BaseAccount implements the schemas.Account interface for testing purposes.
// It provides mock implementations of the required methods to test the Maxim plugin
// with a basic OpenAI configuration.
type BaseAccount struct{}

// GetConfiguredProviders returns a list of supported providers for testing.
// Currently only supports OpenAI for simplicity in testing. You are free to add more providers as needed.
func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

const (
	TestCacheKey = "x-test-cache-key"
	TestPrefix   = "test_semantic_cache_plugin_"
	TestRedisClusterPrefix = "{test_semantic_cache_plugin_cluster}"
)

// GetKeysForProvider returns a mock API key configuration for testing.
// Uses the OPENAI_API_KEY environment variable for authentication.
func (baseAccount *BaseAccount) GetKeysForProvider(ctx *context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		openaiKey = "test-key" // Use a placeholder for testing Redis functionality
	}

	return []schemas.Key{
		{
			Value:  openaiKey,
			Models: []string{}, // Empty models array means it supports ALL models
			Weight: 1.0,
		},
	}, nil
}

// GetConfigForProvider returns default provider configuration for testing.
// Uses standard network and concurrency settings.
func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// clearTestKeysWithStore removes all keys matching the test prefix using the store interface.
// This is safer than FLUSHALL as it only affects test keys, not the entire Redis instance.
func clearTestKeysWithStore(t *testing.T, store vectorstore.VectorStore, prefix string) {
	ctx := context.Background()
	pattern := prefix + "*"

	var keys []string
	var cursor *string

	// Use store interface to find all keys matching the prefix
	for {
		batch, c, err := store.GetAll(ctx, pattern, cursor, 1000)
		if err != nil {
			t.Logf("Warning: Failed to scan keys with prefix %s: %v", prefix, err)
			return
		}
		keys = append(keys, batch...)
		cursor = c
		if cursor == nil {
			break
		}
	}

	// Delete keys in batches if any were found
	if len(keys) > 0 {
		if err := store.Delete(ctx, keys); err != nil {
			t.Logf("Warning: Failed to delete test keys: %v", err)
		} else {
			t.Logf("Cleaned up %d test keys with prefix %s", len(keys), prefix)
		}
	}
}

func TestSemanticCachePlugin(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Fatal("OPENAI_API_KEY is not set, skipping test")
		return
	}

	// Configure plugin with minimal Redis connection settings (only Addr is required)
	config := Config{
		CacheKey: TestCacheKey,
		Prefix:   TestPrefix, // Use test-specific prefix to isolate test data
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Type: "redis",
		Config: vectorstore.RedisConfig{
			Addr:     "localhost:6379",
			Password: os.Getenv("REDIS_PASSWORD"),
		},
	}, logger)
	if err != nil {
		t.Fatalf("Redis not available or failed to connect: %v", err)
		return
	}
	// Initialize the Redis plugin (it will create its own client)
	plugin, err := Init(context.Background(), config, logger, store)
	if err != nil {
		t.Fatalf("Redis not available or failed to connect: %v", err)
		return
	}

	// Get the internal store for test setup
	pluginImpl := plugin.(*Plugin)

	// Clear test keys using the store interface
	clearTestKeysWithStore(t, pluginImpl.store, TestPrefix)
	ctx := context.Background()

	account := BaseAccount{}

	ctx = context.WithValue(ctx, ContextKey(TestCacheKey), "test-value")

	// Initialize Bifrost with the plugin
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: &account,
		Plugins: []schemas.Plugin{plugin},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Cleanup()

	// Create a test request
	testRequest := &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{
				{
					Role: "user",
					Content: schemas.MessageContent{
						ContentStr: bifrost.Ptr("What is Bifrost? Answer in one short sentence."),
					},
				},
			},
		},
		Params: &schemas.ModelParameters{
			Temperature: bifrost.Ptr(0.7),
			MaxTokens:   bifrost.Ptr(50),
		},
	}

	t.Log("Making first request (should go to OpenAI and be cached)...")

	// Make first request (will go to OpenAI and be cached)
	start1 := time.Now()
	response1, bifrostErr1 := client.ChatCompletionRequest(ctx, testRequest)
	duration1 := time.Since(start1)

	if bifrostErr1 != nil {
		t.Fatalf("First request failed: %v", bifrostErr1)
	}

	if response1 == nil {
		t.Fatal("First response is nil")
	}

	if len(response1.Choices) == 0 {
		t.Fatal("First response has no choices")
	}

	if response1.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("First response content is nil")
	}

	t.Logf("First request completed in %v", duration1)
	t.Logf("Response: %s", *response1.Choices[0].Message.Content.ContentStr)

	// Wait a moment to ensure cache is written
	time.Sleep(100 * time.Millisecond)

	t.Log("Making second identical request (should be served from cache)...")

	// Make second identical request (should be cached)
	// Use the same context with cache key for the second request
	start2 := time.Now()
	response2, bifrostErr2 := client.ChatCompletionRequest(ctx, testRequest)
	duration2 := time.Since(start2)

	if bifrostErr2 != nil {
		t.Fatalf("Second request failed: %v", bifrostErr2)
	}

	if response2 == nil {
		t.Fatal("Second response is nil")
	}

	if len(response2.Choices) == 0 {
		t.Fatal("Second response has no choices")
	}

	if response2.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("Second response content is nil")
	}

	t.Logf("Second request completed in %v", duration2)
	t.Logf("Response: %s", *response2.Choices[0].Message.Content.ContentStr)

	// Check if second request was cached
	cached := false
	var cacheKeyValue interface{}

	if response2.ExtraFields.RawResponse == nil {
		t.Error("Second response ExtraFields.RawResponse is nil - expected cache metadata")
	} else {
		rawMap, ok := response2.ExtraFields.RawResponse.(map[string]interface{})
		if !ok {
			t.Error("Second response ExtraFields.RawResponse is not a map - expected cache metadata")
		} else {
			cachedFlag, exists := rawMap["bifrost_cached"]
			if !exists {
				t.Error("Second response missing 'bifrost_cached' flag - expected cache hit")
			} else {
				cachedBool, ok := cachedFlag.(bool)
				if !ok {
					t.Error("'bifrost_cached' flag is not a boolean")
				} else if !cachedBool {
					t.Error("'bifrost_cached' flag is false - expected cache hit")
				} else {
					cached = true
					t.Log("Second request was served from Redis cache!")

					cacheKeyValue, exists = rawMap["bifrost_cache_key"]
					if !exists {
						t.Error("Cache metadata missing 'bifrost_cache_key'")
					} else {
						t.Logf("Cache key: %v", cacheKeyValue)
					}
				}
			}
		}
	}

	// Performance comparison
	t.Logf("Performance Summary:")
	t.Logf("First request (OpenAI):  %v", duration1)
	t.Logf("Second request (Cache):  %v", duration2)

	if !cached {
		t.Fatal("Second request was not cached - cache functionality is not working")
	}

	if duration2 >= duration1 {
		t.Errorf("Cache request took longer than original request: cache=%v, original=%v", duration2, duration1)
	} else {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Cache speedup: %.2fx faster", speedup)

		// Assert that cache is at least 2x faster (reasonable expectation)
		if speedup < 2.0 {
			t.Errorf("Cache speedup is less than 2x: got %.2fx", speedup)
		}
	}

	// Verify responses are identical (content should be the same)
	content1 := *response1.Choices[0].Message.Content.ContentStr
	content2 := *response2.Choices[0].Message.Content.ContentStr

	if content1 != content2 {
		t.Errorf("Response content differs between cached and original:\nOriginal: %s\nCached:   %s", content1, content2)
	} else {
		t.Log("Both responses have identical content")
	}

	// Verify provider information is maintained in cached response
	// The cached response should have the provider set, while the original might not
	if response2.ExtraFields.Provider != testRequest.Provider {
		t.Errorf("Provider mismatch in cached response: expected %s, got %s",
			testRequest.Provider, response2.ExtraFields.Provider)
	}

	t.Log("Semantic caching test completed successfully!")
	t.Log("The Semantic Cache plugin successfully cached the response and served it faster on the second request.")
}

func TestSemanticCachePluginStreaming(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Fatal("OPENAI_API_KEY is not set, skipping test")
		return
	}
	// Configure plugin with minimal Redis connection settings
	config := Config{
		CacheKey: TestCacheKey,
		Prefix:   TestPrefix, // Use test-specific prefix to isolate test data
	}
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Type: "redis",
		Config: vectorstore.RedisConfig{
			Addr:     "localhost:6379",
			Password: os.Getenv("REDIS_PASSWORD"),
		},
	}, logger)
	if err != nil {
		t.Fatalf("Redis not available or failed to connect: %v", err)
		return
	}

	// Initialize the semantic cache plugin
	plugin, err := Init(context.Background(), config, logger, store)
	if err != nil {
		t.Fatalf("Redis not available or failed to connect: %v", err)
		return
	}

	// Get the internal store for test setup
	pluginImpl := plugin.(*Plugin)

	// Clear test keys before test (safer than FLUSHALL)
	clearTestKeysWithStore(t, pluginImpl.store, TestPrefix)
	ctx := context.Background()

	account := BaseAccount{}
	ctx = context.WithValue(ctx, ContextKey(TestCacheKey), "test-stream-value")

	// Initialize Bifrost with the plugin
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: &account,
		Plugins: []schemas.Plugin{plugin},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Cleanup()

	// Create a test streaming request
	testRequest := &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{
				{
					Role: "user",
					Content: schemas.MessageContent{
						ContentStr: bifrost.Ptr("Count from 1 to 3, each number on a new line."),
					},
				},
			},
		},
		Params: &schemas.ModelParameters{
			Temperature: bifrost.Ptr(0.0), // Use 0 temperature for more predictable responses
			MaxTokens:   bifrost.Ptr(20),
		},
	}

	t.Log("Making first streaming request (should go to OpenAI and be cached)...")

	// Make first streaming request
	start1 := time.Now()
	stream1, bifrostErr1 := client.ChatCompletionStreamRequest(ctx, testRequest)
	if bifrostErr1 != nil {
		t.Fatalf("First streaming request failed: %v", bifrostErr1)
	}

	var responses1 []schemas.BifrostResponse
	for streamMsg := range stream1 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in first stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostResponse != nil {
			responses1 = append(responses1, *streamMsg.BifrostResponse)
		}
	}
	duration1 := time.Since(start1)

	if len(responses1) == 0 {
		t.Fatal("First streaming request returned no responses")
	}

	t.Logf("First streaming request completed in %v with %d chunks", duration1, len(responses1))

	// Wait for cache to be written
	time.Sleep(200 * time.Millisecond)

	t.Log("Making second identical streaming request (should be served from cache)...")

	// Make second identical streaming request
	start2 := time.Now()
	stream2, bifrostErr2 := client.ChatCompletionStreamRequest(ctx, testRequest)
	if bifrostErr2 != nil {
		t.Fatalf("Second streaming request failed: %v", bifrostErr2)
	}

	var responses2 []schemas.BifrostResponse
	for streamMsg := range stream2 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in second stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostResponse != nil {
			responses2 = append(responses2, *streamMsg.BifrostResponse)
		}
	}
	duration2 := time.Since(start2)

	if len(responses2) == 0 {
		t.Fatal("Second streaming request returned no responses")
	}

	t.Logf("Second streaming request completed in %v with %d chunks", duration2, len(responses2))

	// Validate that both streams have the same number of chunks
	if len(responses1) != len(responses2) {
		t.Errorf("Stream chunk count mismatch: original=%d, cached=%d", len(responses1), len(responses2))
	}

	// Validate that the second stream was cached
	cached := false
	for _, response := range responses2 {
		if response.ExtraFields.RawResponse != nil {
			if rawMap, ok := response.ExtraFields.RawResponse.(map[string]interface{}); ok {
				if cachedFlag, exists := rawMap["bifrost_cached"]; exists {
					if cachedBool, ok := cachedFlag.(bool); ok && cachedBool {
						cached = true
						break
					}
				}
			}
		}
	}

	if !cached {
		t.Fatal("Second streaming request was not served from cache")
	}

	// Validate performance improvement
	if duration2 >= duration1 {
		t.Errorf("Cached stream took longer than original: cache=%v, original=%v", duration2, duration1)
	} else {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Streaming cache speedup: %.2fx faster", speedup)
	}

	// Validate chunk ordering is maintained
	for i := range responses2 {
		if responses2[i].ExtraFields.ChunkIndex != responses1[i].ExtraFields.ChunkIndex {
			t.Errorf("Chunk index mismatch at position %d: original=%d, cached=%d",
				i, responses1[i].ExtraFields.ChunkIndex, responses2[i].ExtraFields.ChunkIndex)
		}
	}

	t.Log("Semantic cache streaming test completed successfully!")
}

// TestSemanticCachePluginWithRedisCluster tests the semantic cache plugin with Redis Cluster backend
func TestSemanticCachePluginWithRedisCluster(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Fatalf("OPENAI_API_KEY is not set, skipping test")
		return
	}
	// Get Redis Cluster addresses from environment or use defaults
	// These ports match the docker-compose.yml configuration
	redisClusterAddrs := []string{"localhost:6371", "localhost:6372", "localhost:6373"}
	if envAddrs := os.Getenv("REDIS_CLUSTER_ADDRS"); envAddrs != "" {
		// If provided as a single comma-separated string, split it
		redisClusterAddrs = strings.Split(envAddrs, ",")
	}

	// Configure plugin with Redis Cluster
	config := Config{
		CacheKey: TestCacheKey,
		Prefix:   TestRedisClusterPrefix, // Use cluster-specific prefix
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Type: "redis_cluster",
		Config: vectorstore.RedisClusterConfig{
			Addrs:    redisClusterAddrs,
			Password: os.Getenv("REDIS_PASSWORD"),
			Username: os.Getenv("REDIS_USERNAME"),
		},
	}, logger)
	if err != nil {
		t.Fatalf("Redis Cluster not available or failed to connect: %v", err)
		return
	}

	// Initialize the Redis Cluster plugin
	plugin, err := Init(context.Background(), config, logger, store)
	if err != nil {
		t.Fatalf("Redis Cluster not available or failed to connect: %v", err)
		return
	}

	// Get the internal store for test setup
	pluginImpl := plugin.(*Plugin)

	// Clear test keys using the store interface
	clearTestKeysWithStore(t, pluginImpl.store, TestRedisClusterPrefix)
	ctx := context.Background()

	account := BaseAccount{}
	ctx = context.WithValue(ctx, ContextKey(TestCacheKey), "test-cluster-value")

	// Initialize Bifrost with the plugin
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: &account,
		Plugins: []schemas.Plugin{plugin},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost with Redis Cluster: %v", err)
	}
	defer client.Cleanup()

	// Create a test request
	testRequest := &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{
				{
					Role: "user",
					Content: schemas.MessageContent{
						ContentStr: bifrost.Ptr("What is Redis Cluster? Answer in one short sentence."),
					},
				},
			},
		},
		Params: &schemas.ModelParameters{
			Temperature: bifrost.Ptr(0.7),
			MaxTokens:   bifrost.Ptr(50),
		},
	}

	t.Log("Making first request with Redis Cluster (should go to OpenAI and be cached)...")

	// Make first request (will go to OpenAI and be cached in Redis Cluster)
	start1 := time.Now()
	response1, bifrostErr1 := client.ChatCompletionRequest(ctx, testRequest)
	duration1 := time.Since(start1)

	if bifrostErr1 != nil {
		t.Fatalf("First request failed with Redis Cluster: %v", bifrostErr1)
	}

	if response1 == nil || len(response1.Choices) == 0 || response1.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("First response from Redis Cluster is invalid")
	}

	t.Logf("First request with Redis Cluster completed in %v", duration1)
	t.Logf("Response: %s", *response1.Choices[0].Message.Content.ContentStr)

	// Wait a moment to ensure cache is written to cluster
	time.Sleep(100 * time.Millisecond)

	t.Log("Making second identical request with Redis Cluster (should be served from cache)...")

	// Make second identical request (should be cached in Redis Cluster)
	start2 := time.Now()
	response2, bifrostErr2 := client.ChatCompletionRequest(ctx, testRequest)
	duration2 := time.Since(start2)

	if bifrostErr2 != nil {
		t.Fatalf("Second request failed with Redis Cluster: %v", bifrostErr2)
	}

	if response2 == nil || len(response2.Choices) == 0 || response2.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("Second response from Redis Cluster is invalid")
	}

	t.Logf("Second request with Redis Cluster completed in %v", duration2)
	t.Logf("Response: %s", *response2.Choices[0].Message.Content.ContentStr)

	// Check if second request was cached
	cached := false
	if response2.ExtraFields.RawResponse != nil {
		if rawMap, ok := response2.ExtraFields.RawResponse.(map[string]interface{}); ok {
			if cachedFlag, exists := rawMap["bifrost_cached"]; exists {
				if cachedBool, ok := cachedFlag.(bool); ok && cachedBool {
					cached = true
					t.Log("Second request was served from Redis Cluster cache!")

					if cacheKey, exists := rawMap["bifrost_cache_key"]; exists {
						t.Logf("Cache key: %v", cacheKey)
					}
				}
			}
		}
	}

	if !cached {
		t.Fatal("Second request was not cached in Redis Cluster - cache functionality is not working")
	}

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
	} else {
		t.Log("Both responses have identical content with Redis Cluster")
	}

	t.Log("Semantic caching with Redis Cluster test completed successfully!")
	t.Log("The Redis Cluster backend successfully cached the response and served it faster on the second request.")
}
