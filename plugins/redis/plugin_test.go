package redis

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
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

// GetKeysForProvider returns a mock API key configuration for testing.
// Uses the OPENAI_API_KEY environment variable for authentication.
func (baseAccount *BaseAccount) GetKeysForProvider(providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{
		{
			Value:  os.Getenv("OPENAI_API_KEY"),
			Models: []string{"gpt-4o-mini", "gpt-4-turbo"},
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

func TestRedisPlugin(t *testing.T) {
	// Configure plugin with minimal Redis connection settings (only Addr is required)
	config := RedisPluginConfig{
		Addr: "localhost:6379",
		// Optional: add password if your Redis instance requires it
		Password: os.Getenv("REDIS_PASSWORD"),
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)

	// Initialize the Redis plugin (it will create its own client)
	plugin, err := NewRedisPlugin(config, logger)
	if err != nil {
		t.Skipf("Redis not available or failed to connect: %v", err)
		return
	}

	// Get the internal client for test setup (we need to type assert to access it)
	pluginImpl := plugin.(*Plugin)
	redisClient := pluginImpl.client

	defer func() {
		// Cleanup will be handled by plugin.Cleanup()
		if cleanupErr := plugin.Cleanup(); cleanupErr != nil {
			t.Logf("Warning: Plugin cleanup failed: %v", cleanupErr)
		}
	}()

	// Clear cache before test
	ctx := context.Background()
	err = redisClient.FlushAll(ctx).Err()
	if err != nil {
		t.Fatalf("Failed to clear cache: %v", err)
	}

	account := BaseAccount{}

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

	log.Println("🚀 Making first request (should go to OpenAI and be cached)...")

	// Make first request (will go to OpenAI and be cached)
	start1 := time.Now()
	response1, bifrostErr1 := client.ChatCompletionRequest(context.Background(), testRequest)
	duration1 := time.Since(start1)

	if bifrostErr1 != nil {
		log.Printf("Error in first Bifrost request: %v", bifrostErr1)
		t.Fatalf("First request failed: %v", bifrostErr1)
	}

	log.Printf("✅ First request completed in %v", duration1)
	if len(response1.Choices) > 0 && response1.Choices[0].Message.Content.ContentStr != nil {
		log.Printf("Response: %s", *response1.Choices[0].Message.Content.ContentStr)
	}

	// Wait a moment to ensure cache is written
	time.Sleep(100 * time.Millisecond)

	log.Println("📤 Making second identical request (should be served from cache)...")

	// Make second identical request (should be cached)
	start2 := time.Now()
	response2, bifrostErr2 := client.ChatCompletionRequest(context.Background(), testRequest)
	duration2 := time.Since(start2)

	if bifrostErr2 != nil {
		log.Printf("Error in second Bifrost request: %v", bifrostErr2)
		t.Fatalf("Second request failed: %v", bifrostErr2)
	}

	log.Printf("✅ Second request completed in %v", duration2)
	if len(response2.Choices) > 0 && response2.Choices[0].Message.Content.ContentStr != nil {
		log.Printf("Response: %s", *response2.Choices[0].Message.Content.ContentStr)
	}

	// Check if second request was cached
	cached := false
	if response2.ExtraFields.RawResponse != nil {
		if rawMap, ok := response2.ExtraFields.RawResponse.(map[string]interface{}); ok {
			if cachedFlag, exists := rawMap["bifrost_cached"]; exists {
				if cachedBool, ok := cachedFlag.(bool); ok && cachedBool {
					cached = true
					log.Println("🎯 Second request was served from Redis cache!")
					if cacheKey, exists := rawMap["bifrost_cache_key"]; exists {
						log.Printf("Cache key: %v", cacheKey)
					}
				}
			}
		}
	}

	// Performance comparison
	log.Printf("\n📊 Performance Summary:")
	log.Printf("First request (OpenAI):  %v", duration1)
	log.Printf("Second request (Cache):  %v", duration2)

	if cached && duration2 < duration1 {
		speedup := float64(duration1) / float64(duration2)
		log.Printf("⚡ Cache speedup: %.2fx faster", speedup)
	} else if !cached {
		log.Println("⚠️  Second request was not cached (this might be expected if Redis is not available)")
	}

	// Verify responses are identical (content should be the same)
	if len(response1.Choices) > 0 && len(response2.Choices) > 0 {
		if response1.Choices[0].Message.Content.ContentStr != nil && response2.Choices[0].Message.Content.ContentStr != nil {
			content1 := *response1.Choices[0].Message.Content.ContentStr
			content2 := *response2.Choices[0].Message.Content.ContentStr
			if content1 == content2 {
				log.Println("✅ Both responses have identical content")
			} else {
				log.Printf("⚠️  Response content differs:\nFirst:  %s\nSecond: %s", content1, content2)
			}
		}
	}

	log.Println("\n🎉 Redis caching demo completed!")
	if cached {
		log.Println("💡 The Redis plugin successfully cached the response and served it faster on the second request.")
	} else {
		log.Println("💡 Redis caching was not demonstrated (Redis may not be available or configured).")
	}
}
