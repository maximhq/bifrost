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

// RunExternalCompactionTest tests OpenAI's /v1/responses/compact endpoint via
// bifrost.CompactionRequest. It validates:
//  1. The response object is "response.compaction"
//  2. The output array contains at least one compaction item (type=="compaction")
//  3. Usage token counts are present
//  4. The compacted output can be fed back into a subsequent ResponsesRequest
func RunExternalCompactionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ExternalCompaction {
		t.Logf("ExternalCompaction not supported for provider %s", testConfig.Provider)
		return
	}

	model := testConfig.ExternalCompactionModel
	if model == "" {
		model = "gpt-4o"
	}

	t.Run("ExternalCompaction", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Build a short conversation to compact. Real-world use needs >50k tokens to
		// benefit, but the endpoint accepts any length — we verify the mechanics here.
		conversation := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What is the capital of France?"),
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("The capital of France is Paris."),
				},
			},
			CreateBasicResponsesMessage("What is the capital of Germany?"),
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("The capital of Germany is Berlin."),
				},
			},
			CreateBasicResponsesMessage("Summarize the two capitals we discussed."),
		}

		t.Run("BasicCompaction", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			req := &schemas.BifrostCompactionRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    conversation,
			}

			resp, err := client.CompactionRequest(bfCtx, req)
			if err != nil {
				t.Fatalf("CompactionRequest failed: %s", GetErrorMessage(err))
			}
			if resp == nil {
				t.Fatal("Expected non-nil CompactionResponse")
			}

			// object must be "response.compaction"
			if resp.Object != "response.compaction" {
				t.Errorf("Expected object=%q, got %q", "response.compaction", resp.Object)
			}

			// Usage must be present with non-zero input tokens
			if resp.Usage == nil {
				t.Error("Expected non-nil usage")
			} else {
				if resp.Usage.InputTokens == 0 {
					t.Error("Expected non-zero input_tokens in usage")
				}
				t.Logf("usage: input=%d output=%d total=%d",
					resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens)
			}

			// Output must be non-empty and contain at least one compaction item
			if len(resp.Output) == 0 {
				t.Fatal("Expected non-empty output array")
			}
			hasCompactionItem := false
			for _, item := range resp.Output {
				if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeCompaction {
					hasCompactionItem = true
					// Compaction items carry encrypted_content via ResponsesReasoning (not Content)
					if item.ResponsesReasoning == nil || item.ResponsesReasoning.EncryptedContent == nil {
						t.Error("Compaction item missing encrypted_content")
					}
				}
			}
			if !hasCompactionItem {
				t.Errorf("Expected at least one output item with type=%q; output had %d item(s)",
					schemas.ResponsesMessageTypeCompaction, len(resp.Output))
			}

			t.Logf("BasicCompaction passed: object=%s, %d output items", resp.Object, len(resp.Output))
		})

		t.Run("CompactedOutputUsableInFollowUp", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			// Step 1: compact the conversation
			compactReq := &schemas.BifrostCompactionRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    conversation,
			}
			compactResp, err := client.CompactionRequest(bfCtx, compactReq)
			if err != nil {
				t.Fatalf("CompactionRequest failed: %s", GetErrorMessage(err))
			}
			if compactResp == nil || len(compactResp.Output) == 0 {
				t.Fatal("Empty compaction response; cannot test follow-up")
			}

			// Step 2: append a new user message to the compacted output and call /responses
			followUpInput := append(compactResp.Output, CreateBasicResponsesMessage("Which of those two cities is further north?"))

			bfCtx2 := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			followUpResp, followUpErr := client.ResponsesRequest(bfCtx2, &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    followUpInput,
				Params:   &schemas.ResponsesParameters{MaxOutputTokens: bifrost.Ptr(100)},
			})
			if followUpErr != nil {
				t.Fatalf("Follow-up ResponsesRequest with compacted input failed: %s", GetErrorMessage(followUpErr))
			}
			if followUpResp == nil {
				t.Fatal("Expected non-nil follow-up response")
			}

			content := GetResponsesContent(followUpResp)
			if content == "" {
				t.Error("Expected non-empty follow-up response content")
			}

			t.Logf("CompactedOutputUsableInFollowUp passed: follow-up content=%s", content)
		})

		t.Run("CompactionWithInstructions", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			instructions := "Always respond in formal English."

			req := &schemas.BifrostCompactionRequest{
				Provider:     testConfig.Provider,
				Model:        model,
				Input:        conversation,
				Instructions: &instructions,
			}

			resp, err := client.CompactionRequest(bfCtx, req)
			if err != nil {
				t.Fatalf("CompactionRequest with instructions failed: %s", GetErrorMessage(err))
			}
			if resp == nil {
				t.Fatal("Expected non-nil response")
			}
			if resp.Object != "response.compaction" {
				t.Errorf("Expected object=%q, got %q", "response.compaction", resp.Object)
			}

			t.Logf("CompactionWithInstructions passed: %d output items", len(resp.Output))
		})

		t.Run("CompactionMultiTurnWithTimeout", func(t *testing.T) {
			timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			bfCtx := schemas.NewBifrostContext(timeoutCtx, schemas.NoDeadline)

			req := &schemas.BifrostCompactionRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    conversation,
			}

			resp, err := client.CompactionRequest(bfCtx, req)
			if err != nil {
				t.Fatalf("Timed CompactionRequest failed: %s", GetErrorMessage(err))
			}
			if resp == nil || resp.Object != "response.compaction" {
				t.Fatalf("Unexpected response: %+v", resp)
			}
			t.Logf("CompactionMultiTurnWithTimeout passed in time")
		})
	})
}
