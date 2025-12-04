package testutil

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageGenerationStreamTest executes the end-to-end streaming image generation test
func RunImageGenerationStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.ImageGenerationModel == "" {
		t.Logf("Image generation streaming not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageGenerationStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		request := &schemas.BifrostImageGenerationRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ImageGenerationModel,
			Input: &schemas.ImageGenerationInput{
				Prompt: "A futuristic cityscape at sunset with flying cars",
			},
			Params: &schemas.ImageGenerationParameters{
				Size:           bifrost.Ptr("1024x1024"),
				Quality:        bifrost.Ptr("hd"),
				ResponseFormat: bifrost.Ptr("b64_json"),
				N:              bifrost.Ptr(1),
			},
			Fallbacks: testConfig.ImageGenerationFallbacks,
		}

		streamChan, bifrostErr := client.ImageGenerationStreamRequest(ctx, request)
		if bifrostErr != nil {
			t.Fatalf("❌ Image generation stream failed: %v", GetErrorMessage(bifrostErr))
		}

		if streamChan == nil {
			t.Fatal("❌ Image generation stream returned nil channel")
		}

		var chunks []*schemas.BifrostStream
		var mu sync.Mutex
		var wg sync.WaitGroup
		done := make(chan bool)

		// Collect chunks
		wg.Add(1)
		go func() {
			defer wg.Done()
			for stream := range streamChan {
				mu.Lock()
				chunks = append(chunks, stream)
				mu.Unlock()

				// Check for errors
				if stream.BifrostError != nil {
					t.Errorf("❌ Stream error: %v", GetErrorMessage(stream.BifrostError))
					done <- true
					return
				}

				// Check for image stream response
				if stream.BifrostImageGenerationStreamResponse != nil {
					imgResp := stream.BifrostImageGenerationStreamResponse
					if imgResp.Type == "image_generation.completed" {
						done <- true
						return
					}
				}
			}
			done <- true
		}()

		// Wait for completion or timeout
		select {
		case <-done:
			wg.Wait()
		case <-time.After(60 * time.Second):
			t.Fatal("❌ Image generation stream timed out after 60 seconds")
		}

		mu.Lock()
		defer mu.Unlock()

		if len(chunks) == 0 {
			t.Fatal("❌ No stream chunks received")
		}

		// Validate chunks
		hasPartialImage := false
		hasCompleted := false
		var completedChunk *schemas.BifrostImageGenerationStreamResponse

		for _, chunk := range chunks {
			if chunk.BifrostImageGenerationStreamResponse != nil {
				imgResp := chunk.BifrostImageGenerationStreamResponse
				if strings.Contains(imgResp.Type, "partial_image") {
					hasPartialImage = true
					if imgResp.PartialB64 == "" {
						t.Error("❌ Partial image chunk missing PartialB64")
					}
				}
				if imgResp.Type == "image_generation.completed" {
					hasCompleted = true
					completedChunk = imgResp
				}
			}
		}

		if !hasPartialImage {
			t.Logf("⚠️  No partial image chunks received (may be provider-specific)")
		}

		if !hasCompleted {
			t.Error("❌ No completion chunk received")
		}

		if completedChunk != nil {
			if completedChunk.Usage == nil {
				t.Logf("⚠️  Completion chunk missing usage (may be provider-specific)")
			}
		}

		t.Logf("✅ Image generation stream successful: Received %d chunks, HasPartial=%v, HasCompleted=%v",
			len(chunks), hasPartialImage, hasCompleted)
	})
}
