package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunResponsesImageGenerationToolTest exercises the hosted image_generation
// tool through Responses, rather than the standalone /images endpoint. It is
// opt-in because it consumes a real image-generation request. Set
// BIFROST_RESPONSES_IMAGE_GENERATION_MODEL to override the default Luna model.
func RunResponsesImageGenerationToolTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	model := os.Getenv("BIFROST_RESPONSES_IMAGE_GENERATION_MODEL")
	if model == "" {
		if os.Getenv("BIFROST_RUN_RESPONSES_IMAGE_GENERATION_TESTS") != "true" {
			return
		}
		model = "gpt-5.6-luna"
	}

	t.Run("ResponsesImageGenerationTool", func(t *testing.T) {
		request := func(_ bool) *schemas.BifrostResponsesRequest {
			return &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Generate an image of a cute orange cat sitting in a coffee shop.")},
				}},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{{
						Type: schemas.ResponsesToolTypeImageGeneration,
						ResponsesToolImageGeneration: &schemas.ResponsesToolImageGeneration{
							Action: schemas.Ptr(schemas.ResponsesImageGenerationActionGenerate),
						},
					}},
				},
			}
		}

		t.Run("non_streaming", func(t *testing.T) {
			response, err := client.ResponsesRequest(schemas.NewBifrostContext(ctx, schemas.NoDeadline), request(false))
			if err != nil {
				if strings.Contains(GetErrorMessage(err), "failed to peek at type field") {
					t.Fatalf("image_generation response schema regression: %s", GetErrorMessage(err))
				}
				t.Fatalf("Responses image_generation request failed for %s/%s: %s", testConfig.Provider, model, GetErrorMessage(err))
			}
			if response == nil {
				t.Fatal("Responses image_generation returned a nil response")
			}
			if !hasImageGenerationCall(response.Output) {
				t.Fatalf("Responses image_generation response contained no image_generation_call")
			}
		})

		t.Run("streaming", func(t *testing.T) {
			streamCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
			defer cancel()
			stream, err := client.ResponsesStreamRequest(schemas.NewBifrostContext(streamCtx, schemas.NoDeadline), request(true))
			if err != nil {
				t.Fatalf("Responses image_generation stream setup failed for %s/%s: %s", testConfig.Provider, model, GetErrorMessage(err))
			}

			var sawImageCall, sawCompleted bool
			for {
				select {
				case chunk, ok := <-stream:
					if !ok {
						if !sawImageCall {
							t.Fatal("Responses image_generation stream contained no image_generation_call")
						}
						if !sawCompleted {
							t.Fatal("Responses image_generation stream ended without response.completed")
						}
						return
					}
					if chunk == nil {
						continue
					}
					if chunk.BifrostError != nil {
						message := GetErrorMessage(chunk.BifrostError)
						if strings.Contains(message, "failed to peek at type field") {
							t.Fatalf("image_generation stream schema regression: %s", message)
						}
						t.Fatalf("Responses image_generation stream failed: %s", message)
					}
					if chunk.BifrostResponsesStreamResponse == nil {
						continue
					}
					response := chunk.BifrostResponsesStreamResponse
					if response.Item != nil && response.Item.Type != nil && *response.Item.Type == schemas.ResponsesMessageTypeImageGenerationCall {
						sawImageCall = true
					}
					if response.Type == schemas.ResponsesStreamResponseTypeCompleted {
						sawCompleted = true
					}
				case <-streamCtx.Done():
					t.Fatalf("timed out waiting for Responses image_generation stream: %v", streamCtx.Err())
				}
			}
		})
	})
}

func hasImageGenerationCall(output []schemas.ResponsesMessage) bool {
	for _, item := range output {
		if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeImageGenerationCall {
			return true
		}
	}
	return false
}
