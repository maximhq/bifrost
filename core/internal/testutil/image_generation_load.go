package testutil

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageGenerationLoadTest tests concurrent image generation requests
func RunImageGenerationLoadTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageGenerationStream {
		t.Logf("Image generation stream load test skipped: not supported for provider %s", testConfig.Provider)
		return
	}

	if testConfig.ImageGenerationModel == "" {
		t.Logf("Image generation load test skipped: not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageGenerationLoad", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		const numConcurrentRequests = 10
		const numRequestsPerGoroutine = 2
		totalRequests := numConcurrentRequests * numRequestsPerGoroutine

		var successCount int64
		var errorCount int64
		var totalDuration time.Duration
		var mu sync.Mutex

		start := time.Now()
		var wg sync.WaitGroup

		// Launch concurrent requests
		for i := 0; i < numConcurrentRequests; i++ {
			wg.Add(1)
			go func(goroutineID int) {
				defer wg.Done()

				for j := 0; j < numRequestsPerGoroutine; j++ {
					request := &schemas.BifrostImageGenerationRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.ImageGenerationModel,
						Input: &schemas.ImageGenerationInput{
							Prompt: "A test image for load testing - " + time.Now().Format("20060102150405"),
						},
						Params: &schemas.ImageGenerationParameters{
							Size:           bifrost.Ptr("1024x1024"), // Smaller size for faster generation
							ResponseFormat: bifrost.Ptr("b64_json"),
							N:              bifrost.Ptr(1),
						},
					}

					reqStart := time.Now()
					response, bifrostErr := client.ImageGenerationRequest(ctx, request)
					reqDuration := time.Since(reqStart)

					mu.Lock()
					totalDuration += reqDuration
					mu.Unlock()

					if bifrostErr != nil {
						atomic.AddInt64(&errorCount, 1)
						t.Logf("⚠️  Request %d-%d failed: %v", goroutineID, j, GetErrorMessage(bifrostErr))
					} else if response != nil && len(response.Data) > 0 {
						atomic.AddInt64(&successCount, 1)
					} else {
						atomic.AddInt64(&errorCount, 1)
					}
				}
			}(i)
		}

		wg.Wait()
		totalTime := time.Since(start)

		// Calculate statistics
		avgDuration := totalDuration / time.Duration(totalRequests)
		successRate := float64(successCount) / float64(totalRequests) * 100

		t.Logf("✅ Load test completed:")
		t.Logf("   Total requests: %d", totalRequests)
		t.Logf("   Successful: %d (%.2f%%)", successCount, successRate)
		t.Logf("   Failed: %d", errorCount)
		t.Logf("   Total time: %v", totalTime)
		t.Logf("   Average request duration: %v", avgDuration)
		t.Logf("   Requests per second: %.2f", float64(totalRequests)/totalTime.Seconds())

		// Validate results
		if successRate < 80.0 {
			t.Errorf("❌ Success rate too low: %.2f%% (expected >= 80%%)", successRate)
		} else {
			t.Logf("✅ Success rate acceptable: %.2f%%", successRate)
		}
	})
}

// RunImageGenerationStreamLoadTest tests stream memory usage under load
func RunImageGenerationStreamLoadTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.ImageGenerationModel == "" {
		t.Logf("Image generation stream load test skipped: not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageGenerationStreamLoad", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		const numConcurrentStreams = 5
		const streamsPerGoroutine = 1

		var successCount int64
		var errorCount int64
		var totalChunks int64
		var mu sync.Mutex

		start := time.Now()
		var wg sync.WaitGroup

		// Launch concurrent streams
		for i := 0; i < numConcurrentStreams; i++ {
			wg.Add(1)
			go func(streamID int) {
				defer wg.Done()

				for j := 0; j < streamsPerGoroutine; j++ {
					request := &schemas.BifrostImageGenerationRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.ImageGenerationModel,
						Input: &schemas.ImageGenerationInput{
							Prompt: "A streaming test image for load testing",
						},
						Params: &schemas.ImageGenerationParameters{
							Size:           bifrost.Ptr("1024x1024"),
							ResponseFormat: bifrost.Ptr("b64_json"),
							N:              bifrost.Ptr(1),
						},
					}

					// Create derived context for this stream
					streamCtx, cancel := context.WithCancel(ctx)

					streamChan, bifrostErr := client.ImageGenerationStreamRequest(streamCtx, request)
					if bifrostErr != nil {
						cancel()
						atomic.AddInt64(&errorCount, 1)
						t.Logf("⚠️  Stream %d-%d failed to start: %v", streamID, j, GetErrorMessage(bifrostErr))
						continue
					}

					if streamChan == nil {
						cancel()
						atomic.AddInt64(&errorCount, 1)
						t.Logf("⚠️  Stream %d-%d returned nil channel", streamID, j)
						continue
					}

					// Collect chunks
					chunkCount := int64(0)
					completed := false

					// Process stream until completion or error
					for stream := range streamChan {
						if stream.BifrostError != nil {
							t.Logf("⚠️  Stream %d-%d error: %v", streamID, j, GetErrorMessage(stream.BifrostError))
							cancel()
							continue
						}

						if stream.BifrostImageGenerationStreamResponse != nil {
							chunkCount++
							if stream.BifrostImageGenerationStreamResponse.Type == string(openai.ImageGenerationCompleted) {
								completed = true
								cancel()
								continue
							}
						}
					}

					cancel()

					mu.Lock()
					totalChunks += chunkCount
					mu.Unlock()

					if completed {
						atomic.AddInt64(&successCount, 1)
					} else {
						atomic.AddInt64(&errorCount, 1)
					}
				}
			}(i)
		}

		wg.Wait()
		totalTime := time.Since(start)

		avgChunksPerStream := float64(totalChunks) / float64(numConcurrentStreams*streamsPerGoroutine)
		successRate := float64(successCount) / float64(numConcurrentStreams*streamsPerGoroutine) * 100

		t.Logf("✅ Stream load test completed:")
		t.Logf("   Total streams: %d", numConcurrentStreams*streamsPerGoroutine)
		t.Logf("   Successful: %d (%.2f%%)", successCount, successRate)
		t.Logf("   Failed: %d", errorCount)
		t.Logf("   Total chunks: %d", totalChunks)
		t.Logf("   Average chunks per stream: %.2f", avgChunksPerStream)
		t.Logf("   Total time: %v", totalTime)

		if successRate < 80.0 {
			t.Errorf("❌ Stream success rate too low: %.2f%% (expected >= 80%%)", successRate)
		} else {
			t.Logf("✅ Stream success rate acceptable: %.2f%%", successRate)
		}
	})
}

// RunImageGenerationCacheLoadTest tests cache performance at scale
func RunImageGenerationCacheLoadTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.ImageGenerationModel == "" {
		t.Logf("Image generation cache load test skipped: not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageGenerationCacheLoad", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Generate a unique prompt that will be cached
		cachePrompt := "Cache load test image - " + time.Now().Format("20060102150405")

		// First request to populate cache
		request := &schemas.BifrostImageGenerationRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ImageGenerationModel,
			Input: &schemas.ImageGenerationInput{
				Prompt: cachePrompt,
			},
			Params: &schemas.ImageGenerationParameters{
				Size:           bifrost.Ptr("1024x1024"),
				ResponseFormat: bifrost.Ptr("b64_json"),
			},
		}

		_, err := client.ImageGenerationRequest(ctx, request)
		if err != nil {
			t.Fatalf("❌ Failed to populate cache: %v", GetErrorMessage(err))
		}

		// Wait a bit for cache to be written
		time.Sleep(1 * time.Second)

		// Now test concurrent cache hits
		const numConcurrentRequests = 20
		var cacheHitCount int64
		var cacheMissCount int64
		var totalDuration time.Duration
		var mu sync.Mutex

		start := time.Now()
		var wg sync.WaitGroup

		for i := 0; i < numConcurrentRequests; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Create new request for each goroutine to avoid data race
				localRequest := &schemas.BifrostImageGenerationRequest{
					Provider: testConfig.Provider,
					Model:    testConfig.ImageGenerationModel,
					Input: &schemas.ImageGenerationInput{
						Prompt: cachePrompt,
					},
					Params: &schemas.ImageGenerationParameters{
						Size:           bifrost.Ptr("1024x1024"),
						ResponseFormat: bifrost.Ptr("b64_json"),
					},
				}
				reqStart := time.Now()
				response, bifrostErr := client.ImageGenerationRequest(ctx, localRequest)
				reqDuration := time.Since(reqStart)

				mu.Lock()
				totalDuration += reqDuration
				mu.Unlock()

				if bifrostErr != nil {
					t.Logf("⚠️  Cache load test request failed: %v", GetErrorMessage(bifrostErr))
					return
				}

				if response != nil && response.ExtraFields.CacheDebug != nil {
					if response.ExtraFields.CacheDebug.CacheHit {
						atomic.AddInt64(&cacheHitCount, 1)
					} else {
						atomic.AddInt64(&cacheMissCount, 1)
					}
				}
			}()
		}

		wg.Wait()
		totalTime := time.Since(start)

		avgDuration := totalDuration / time.Duration(numConcurrentRequests)
		cacheHitRate := float64(cacheHitCount) / float64(numConcurrentRequests) * 100

		t.Logf("✅ Cache load test completed:")
		t.Logf("   Total requests: %d", numConcurrentRequests)
		t.Logf("   Cache hits: %d (%.2f%%)", cacheHitCount, cacheHitRate)
		t.Logf("   Cache misses: %d", cacheMissCount)
		t.Logf("   Total time: %v", totalTime)
		t.Logf("   Average request duration: %v", avgDuration)
		t.Logf("   Requests per second: %.2f", float64(numConcurrentRequests)/totalTime.Seconds())

		if cacheHitRate > 50.0 {
			t.Logf("✅ Cache hit rate acceptable: %.2f%%", cacheHitRate)
		} else {
			t.Logf("⚠️  Cache hit rate lower than expected: %.2f%% (cache may not be enabled)", cacheHitRate)
		}
	})
}
