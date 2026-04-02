package governance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBuildComplexityInput_ResponsesInputTextBlocks(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ResponsesRequest)

	body := map[string]any{
		"instructions": "Be concise",
		"input": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "input_text",
						"text": "Explain distributed tracing in simple terms",
					},
				},
			},
		},
	}

	input, ok := buildComplexityInput(ctx, body)
	require.True(t, ok)
	assert.Equal(t, "Explain distributed tracing in simple terms", input.LastUserText)
	assert.Equal(t, "Be concise", input.SystemText)
}

func TestBuildComplexityInput_AnthropicMessagesFallbackFromResponsesRequest(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ResponsesRequest)

	body := map[string]any{
		"system": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": "You are a careful assistant",
			},
		},
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": "Compare mutexes and channels in Go",
					},
				},
			},
		},
	}

	input, ok := buildComplexityInput(ctx, body)
	require.True(t, ok)
	assert.Equal(t, "Compare mutexes and channels in Go", input.LastUserText)
	assert.Equal(t, "You are a careful assistant", input.SystemText)
}

func TestBuildComplexityInput_ResponsesIgnoresAssistantOutputHistory(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ResponsesRequest)

	body := map[string]any{
		"instructions": "Review carefully",
		"input": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "input_text",
						"text": "I changed the retry policy and circuit breaker thresholds.",
					},
				},
			},
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type": "output_text",
						"text": "The patch now retries idempotent requests and opens the breaker sooner.",
					},
				},
			},
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "input_text",
						"text": "Can you explain the changes?",
					},
				},
			},
		},
	}

	input, ok := buildComplexityInput(ctx, body)
	require.True(t, ok)
	assert.Equal(t, "Can you explain the changes?", input.LastUserText)
	assert.Equal(t, []string{"I changed the retry policy and circuit breaker thresholds."}, input.PriorUserTexts)
	assert.Equal(t, "Review carefully", input.SystemText)
}

func TestBuildComplexityInput_SkipsUnsupportedRequestTypesEvenWhenTextIsPresent(t *testing.T) {
	tests := []struct {
		name        string
		requestType schemas.RequestType
		body        map[string]any
	}{
		{
			name:        "embeddings_input_text",
			requestType: schemas.EmbeddingRequest,
			body: map[string]any{
				"input": "Create embeddings for this support ticket summary",
			},
		},
		{
			name:        "image_generation_prompt",
			requestType: schemas.ImageGenerationRequest,
			body: map[string]any{
				"prompt": "Create a watercolor poster of a launch campaign",
			},
		},
		{
			name:        "image_edit_prompt",
			requestType: schemas.ImageEditRequest,
			body: map[string]any{
				"prompt": "Remove the background and brighten the subject",
			},
		},
		{
			name:        "speech_input",
			requestType: schemas.SpeechRequest,
			body: map[string]any{
				"input": "Read this release note in a calm voice",
			},
		},
		{
			name:        "count_tokens_input",
			requestType: schemas.CountTokensRequest,
			body: map[string]any{
				"input": []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{
								"type": "input_text",
								"text": "How many tokens is this prompt?",
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, tt.requestType)

			input, ok := buildComplexityInput(ctx, tt.body)
			require.False(t, ok)
			assert.Equal(t, "", input.LastUserText)
		})
	}
}

func TestBuildComplexityInput_SkipsMixedModalityUserContent(t *testing.T) {
	tests := []struct {
		name        string
		requestType schemas.RequestType
		body        map[string]any
	}{
		{
			name:        "chat_text_plus_image",
			requestType: schemas.ChatCompletionRequest,
			body: map[string]any{
				"messages": []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{"type": "text", "text": "What changed in this screenshot?"},
							map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": "https://example.com/image.png"}},
						},
					},
				},
			},
		},
		{
			name:        "responses_text_plus_file",
			requestType: schemas.ResponsesRequest,
			body: map[string]any{
				"input": []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{"type": "input_text", "text": "Summarize this document"},
							map[string]interface{}{"type": "input_file", "file_id": "file_123"},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, tt.requestType)

			input, ok := buildComplexityInput(ctx, tt.body)
			require.False(t, ok)
			assert.Equal(t, "", input.LastUserText)
		})
	}
}
