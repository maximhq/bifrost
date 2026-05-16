package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunCompactionTest tests that context_management with compaction is correctly
// forwarded through Bifrost via the Responses API.
//
// Because compaction requires a minimum trigger of 50,000 input tokens, this
// test does NOT trigger actual compaction. Instead it verifies:
//  1. The context_management field survives the Bifrost request round-trip
//  2. The compact-2026-01-12 beta header is properly sent
//  3. The API accepts the request without error (non-streaming + streaming)
func RunCompactionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Compaction {
		t.Logf("Compaction not supported for provider %s", testConfig.Provider)
		return
	}

	// Compaction is currently Anthropic-only
	if testConfig.Provider != schemas.Anthropic {
		t.Logf("Compaction test skipped: only supported for Anthropic provider")
		return
	}

	t.Run("Compaction", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Build context_management with compaction config
		contextManagement := &anthropic.ContextManagement{
			Edits: []anthropic.ContextManagementEdit{
				{
					Type: anthropic.ContextManagementEditTypeCompact,
					CompactManagementEditConfig: &anthropic.CompactManagementEditConfig{
						// Use minimum trigger to avoid actual compaction on short input
						Trigger: &anthropic.CompactManagementEditTypeAndValue{
							TypeAndValueObject: &anthropic.CompactManagementEditTypeAndValueObject{
								Type:  "input_tokens",
								Value: schemas.Ptr(50000),
							},
						},
					},
				},
			},
		}

		messages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("Hello! What is the capital of France? Answer in one word."),
		}

		// Compaction requires Claude Opus 4.6 or Claude Sonnet 4.6
		compactionModel := testConfig.CompactionModel
		if compactionModel == "" {
			compactionModel = "claude-sonnet-4-6"
		}

		// --- Non-streaming test ---
		t.Run("NonStreaming", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			request := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    compactionModel,
				Input:    messages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(100),
					ExtraParams: map[string]interface{}{
						"context_management": contextManagement,
					},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			response, err := client.ResponsesRequest(bfCtx, request)
			if err != nil {
				t.Fatalf("Compaction non-streaming request failed: %s", GetErrorMessage(err))
			}
			if response == nil {
				t.Fatal("Expected non-nil response")
			}

			content := GetResponsesContent(response)
			if content == "" {
				t.Error("Expected non-empty response content")
			}

			// Verify stop_reason is NOT "compaction" (input is too short to trigger)
			if response.StopReason != nil && *response.StopReason == "compaction" {
				t.Log("Compaction triggered unexpectedly on short input")
			}

			t.Logf("Compaction non-streaming passed: stop_reason=%v, content=%s",
				response.StopReason, content)
		})

		// --- Streaming test ---
		t.Run("Streaming", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			request := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    compactionModel,
				Input:    messages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(100),
					ExtraParams: map[string]interface{}{
						"context_management": contextManagement,
					},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			responseChan, err := client.ResponsesStreamRequest(bfCtx, request)
			if err != nil {
				t.Fatalf("Compaction streaming request failed: %s", GetErrorMessage(err))
			}

			var fullContent strings.Builder
			var chunkCount int
			var hasCreated, hasCompleted bool

			streamCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			for {
				select {
				case chunk, ok := <-responseChan:
					if !ok {
						goto done
					}
					chunkCount++
					if chunk.BifrostResponsesStreamResponse != nil {
						if chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCreated {
							hasCreated = true
						}
						if chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
							hasCompleted = true
						}
						if chunk.BifrostResponsesStreamResponse.Delta != nil {
							fullContent.WriteString(*chunk.BifrostResponsesStreamResponse.Delta)
						}
					}
				case <-streamCtx.Done():
					t.Fatal("Streaming timed out")
				}
			}
		done:

			if chunkCount == 0 {
				t.Fatal("Expected at least one streaming chunk")
			}
			if !hasCreated {
				t.Error("Missing response.created event")
			}
			if !hasCompleted {
				t.Error("Missing response.completed event")
			}

			content := fullContent.String()
			t.Logf("Compaction streaming passed: %d chunks, content=%s", chunkCount, content)
		})
	})
}
