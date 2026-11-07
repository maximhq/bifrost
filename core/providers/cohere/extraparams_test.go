package cohere

import (
	"reflect"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newThinkingExtraParams() map[string]interface{} {
	return map[string]interface{}{
		"safety_mode": "CONTEXTUAL",
		"thinking":    map[string]interface{}{"type": "enabled", "token_budget": 1024},
		"custom":      "kept",
	}
}

func TestToCohereChatCompletionRequest_DoesNotMutateCallerExtraParams(t *testing.T) {
	req := &schemas.BifrostChatRequest{
		Model: "command-a-03-2025",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Params: &schemas.ChatParameters{ExtraParams: newThinkingExtraParams()},
	}

	out, err := ToCohereChatCompletionRequest(req)

	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(newThinkingExtraParams(), req.Params.ExtraParams), "caller's ExtraParams was modified: %v", req.Params.ExtraParams)
	require.NotNil(t, out.SafetyMode)
	assert.Equal(t, "CONTEXTUAL", *out.SafetyMode)
	require.NotNil(t, out.Thinking)
	assert.Equal(t, CohereThinkingType("enabled"), out.Thinking.Type)
	require.NotNil(t, out.Thinking.TokenBudget)
	assert.Equal(t, 1024, *out.Thinking.TokenBudget)
	assert.Equal(t, "kept", out.ExtraParams["custom"])
	assert.NotContains(t, out.ExtraParams, "safety_mode")
}

func TestToCohereResponsesRequest_DoesNotMutateCallerExtraParams(t *testing.T) {
	newExtra := func() map[string]interface{} {
		return map[string]interface{}{
			"top_k":    5,
			"thinking": map[string]interface{}{"type": "enabled", "token_budget": 2048},
			"custom":   "kept",
		}
	}
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Cohere,
		Model:    "command-a-03-2025",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
		Params: &schemas.ResponsesParameters{ExtraParams: newExtra()},
	}

	out, err := ToCohereResponsesRequest(req)

	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(newExtra(), req.Params.ExtraParams), "caller's ExtraParams was modified: %v", req.Params.ExtraParams)
	require.NotNil(t, out.K)
	assert.Equal(t, 5, *out.K)
	require.NotNil(t, out.Thinking)
	assert.Equal(t, CohereThinkingType("enabled"), out.Thinking.Type)
	assert.Equal(t, "kept", out.ExtraParams["custom"])
}

func TestToCohereEmbeddingRequest_DoesNotMutateCallerExtraParams(t *testing.T) {
	newExtra := func() map[string]interface{} {
		return map[string]interface{}{"input_type": "classification", "truncate": "END", "custom": "kept"}
	}
	text := "hello"
	req := &schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
		},
		Params: &schemas.EmbeddingParameters{ExtraParams: newExtra()},
	}

	out, err := ToCohereEmbeddingRequest(req)

	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(newExtra(), req.Params.ExtraParams), "caller's ExtraParams was modified: %v", req.Params.ExtraParams)
	assert.Equal(t, "classification", out.InputType)
	assert.Equal(t, "kept", out.ExtraParams["custom"])
}
