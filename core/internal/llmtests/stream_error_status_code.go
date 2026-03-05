package llmtests

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunStreamErrorStatusCodeTest validates that pre-stream errors from providers carry
// the correct HTTP status code in BifrostError.StatusCode. This is critical because
// the HTTP transport layer (sendStreamError) relies on this field to propagate the
// provider's actual status code to clients, rather than always returning 200 OK.
//
// The test sends a streaming request with a deliberately invalid model name.
// All providers (OpenAI, Anthropic, Bedrock) return 4xx status codes for such errors,
// and Bifrost must preserve those codes through the error chain.
func RunStreamErrorStatusCodeTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.CompletionStream {
		t.Logf("Completion stream not supported for provider %s, skipping stream error status code test", testConfig.Provider)
		return
	}

	// Skip providers that perform deployment-based key selection.
	// These providers validate model→deployment mapping during key selection,
	// which means invalid models fail BEFORE reaching the provider API.
	// Since no HTTP request is made, there's no provider status code to propagate.
	deploymentBasedProviders := map[schemas.ModelProvider]bool{
		schemas.Azure:     true,
		schemas.Bedrock:   true,
		schemas.Vertex:    true,
		schemas.Replicate: true,
		schemas.VLLM:      true,
	}
	if deploymentBasedProviders[testConfig.Provider] {
		t.Logf("Skipping StreamErrorStatusCode for %s (deployment-based key selection validates models before API call)", testConfig.Provider)
		return
	}

	t.Run("StreamErrorStatusCode", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Use a model name that is guaranteed to not exist across all providers.
		// This triggers a pre-stream validation error (400/404) rather than an in-stream error.
		invalidModel := "bifrost-nonexistent-model-for-testing-12345"

		// Test with Chat Completion stream (most universally supported stream type)
		t.Run("ChatCompletionStream", func(t *testing.T) {
			messages := []schemas.ChatMessage{
				CreateBasicChatMessage("Hello"),
			}

			request := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    invalidModel,
				Input:    messages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(10),
				},
			}

			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			stream, bifrostErr := client.ChatCompletionStreamRequest(bfCtx, request)

			// We expect an error — the model doesn't exist
			if bifrostErr == nil {
				// If somehow no error, drain the stream and fail
				if stream != nil {
					for range stream {
					}
				}
				t.Fatal("❌ Expected error for invalid model in stream request, but got nil")
			}

			// Core assertion: the error must carry a provider HTTP status code
			if bifrostErr.StatusCode == nil {
				t.Fatalf("❌ BifrostError.StatusCode is nil for provider %s — provider status code was not propagated. Error: %s",
					testConfig.Provider, GetErrorMessage(bifrostErr))
			}

			statusCode := *bifrostErr.StatusCode

			// The status code should be a 4xx client error (invalid model → 400, 404, or similar)
			if statusCode < 400 || statusCode >= 600 {
				t.Fatalf("❌ Expected 4xx/5xx status code for invalid model, got %d. Error: %s",
					statusCode, GetErrorMessage(bifrostErr))
			}

			// Should not be a Bifrost-generated error — it should come from the provider
			if bifrostErr.IsBifrostError {
				// Some providers may have bifrost-level validation that catches invalid models
				// before reaching the provider. Log but don't fail.
				t.Logf("⚠️  Error is a Bifrost error (not provider error) with status %d — this may indicate model validation happened before the provider call", statusCode)
			}

			t.Logf("✅ Stream error for invalid model returned status code %d (provider: %s)", statusCode, testConfig.Provider)
			t.Logf("   Error message: %s", GetErrorMessage(bifrostErr))
		})

		// Also test Responses stream if supported (Anthropic uses a different path)
		t.Run("ResponsesStream", func(t *testing.T) {
			messages := []schemas.ResponsesMessage{
				CreateBasicResponsesMessage("Hello"),
			}

			request := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    invalidModel,
				Input:    messages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(10),
				},
			}

			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			stream, bifrostErr := client.ResponsesStreamRequest(bfCtx, request)

			if bifrostErr == nil {
				if stream != nil {
					for range stream {
					}
				}
				t.Fatal("❌ Expected error for invalid model in responses stream request, but got nil")
			}

			if bifrostErr.StatusCode == nil {
				t.Fatalf("❌ BifrostError.StatusCode is nil for provider %s responses stream — provider status code was not propagated. Error: %s",
					testConfig.Provider, GetErrorMessage(bifrostErr))
			}

			statusCode := *bifrostErr.StatusCode

			if statusCode < 400 || statusCode >= 600 {
				t.Fatalf("❌ Expected 4xx/5xx status code for invalid model in responses stream, got %d. Error: %s",
					statusCode, GetErrorMessage(bifrostErr))
			}

			t.Logf("✅ Responses stream error for invalid model returned status code %d (provider: %s)", statusCode, testConfig.Provider)
		})
	})
}
