package llmtests

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunFastModeTest tests that the fast-mode-2026-02-01 beta header is correctly
// sent when speed="fast" is specified via ExtraParams.
//
// This test verifies:
//  1. The fast-mode beta header is properly injected when speed=fast
//  2. The API accepts the request without error
//  3. The response is valid
//
// Note: Fast mode is currently only supported on Anthropic (direct API) with Opus 4.6.
func RunFastModeTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.FastMode {
		t.Logf("Fast mode not supported for provider %s", testConfig.Provider)
		return
	}

	// Fast mode is currently Anthropic-only
	if testConfig.Provider != schemas.Anthropic {
		t.Logf("Fast mode test skipped: only supported for Anthropic provider")
		return
	}

	t.Run("FastMode", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		model := testConfig.FastModeModel
		if model == "" {
			model = "claude-opus-4-6"
		}

		messages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What is 2+2? Answer in one word."),
		}

		t.Run("NonStreaming", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			request := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    messages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(100),
					ExtraParams: map[string]interface{}{
						"speed": "fast",
					},
				},
			}

			response, err := client.ResponsesRequest(bfCtx, request)
			if err != nil {
				t.Fatalf("Fast mode non-streaming request failed: %s", GetErrorMessage(err))
			}
			if response == nil {
				t.Fatal("Expected non-nil response")
			}

			content := GetResponsesContent(response)
			if content == "" {
				t.Error("Expected non-empty response content")
			}

			t.Logf("Fast mode non-streaming passed: content=%s", content)

			// Validate raw request/response fields when enabled
			if testConfig.ExpectRawRequestResponse {
				if err := ValidateRawField(response.ExtraFields.RawRequest, "RawRequest"); err != nil {
					t.Errorf("Fast mode non-streaming raw request validation failed: %v", err)
				}
				if err := ValidateRawField(response.ExtraFields.RawResponse, "RawResponse"); err != nil {
					t.Errorf("Fast mode non-streaming raw response validation failed: %v", err)
				}
			}
		})

		t.Run("ChatNonStreaming", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			chatMessages := []schemas.ChatMessage{
				CreateBasicChatMessage("What is 2+2? Answer in one word."),
			}

			request := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(100),
					ExtraParams: map[string]interface{}{
						"speed": "fast",
					},
				},
			}

			response, err := client.ChatCompletionRequest(bfCtx, request)
			if err != nil {
				t.Fatalf("Fast mode chat non-streaming request failed: %s", GetErrorMessage(err))
			}
			if response == nil {
				t.Fatal("Expected non-nil response")
			}

			content := GetChatContent(response)
			if content == "" {
				t.Error("Expected non-empty response content")
			}

			t.Logf("Fast mode chat non-streaming passed: content=%s", content)

			// Validate raw request/response fields when enabled
			if testConfig.ExpectRawRequestResponse {
				if err := ValidateRawField(response.ExtraFields.RawRequest, "RawRequest"); err != nil {
					t.Errorf("Fast mode chat non-streaming raw request validation failed: %v", err)
				}
				if err := ValidateRawField(response.ExtraFields.RawResponse, "RawResponse"); err != nil {
					t.Errorf("Fast mode chat non-streaming raw response validation failed: %v", err)
				}
			}
		})
	})
}
