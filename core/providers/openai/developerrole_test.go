package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// stubDeveloperRoleDatasheet marks glm-5 as rejecting the developer role and
// qwen3-dev as accepting it; every other model has no row.
func stubDeveloperRoleDatasheet(t *testing.T) {
	rows := map[string]bool{"glm-5": false, "qwen3-dev": true}
	schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		supported, ok := rows[model]
		if !ok {
			return nil
		}
		return &schemas.ModelCapabilities{SupportsDeveloperRole: schemas.Ptr(supported)}
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
}

func developerChatRequest(provider schemas.ModelProvider, model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleDeveloper, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Be brief.")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
	}
}

func developerResponsesRequest(provider schemas.ModelProvider, model string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeAdditionalTools),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleDeveloper),
			},
			{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleDeveloper),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Be brief.")},
			},
			{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
			},
		},
	}
}

func TestDeveloperRoleUnsupported(t *testing.T) {
	stubDeveloperRoleDatasheet(t)
	tests := []struct {
		provider schemas.ModelProvider
		model    string
		want     bool
	}{
		{schemas.Fireworks, "glm-5", true},                                   // datasheet false
		{schemas.Fireworks, "qwen3-dev", false},                              // datasheet true beats the Qwen fallback
		{schemas.Fireworks, "accounts/fireworks/models/qwen3p8-max", true},   // no row: Qwen on Fireworks
		{schemas.Fireworks, "accounts/fireworks/models/gpt-oss-120b", false}, // no row: other Fireworks models
		{schemas.OpenAI, "qwen3p8-max", false},                               // no row: Qwen elsewhere
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, developerRoleUnsupported(nil, tt.provider, tt.model), "%s/%s", tt.provider, tt.model)
	}
}

func TestToOpenAIChatRequest_DeveloperRoleAsSystem(t *testing.T) {
	stubDeveloperRoleDatasheet(t)
	req := developerChatRequest(schemas.Fireworks, "glm-5")
	result := ToOpenAIChatRequest(nil, req)
	require.NotNil(t, result)
	require.Equal(t, schemas.ChatMessageRoleSystem, result.Messages[0].Role)
	require.Equal(t, schemas.ChatMessageRoleUser, result.Messages[1].Role)
	require.Equal(t, schemas.ChatMessageRoleDeveloper, req.Input[0].Role)

	result = ToOpenAIChatRequest(nil, developerChatRequest(schemas.Fireworks, "qwen3-dev"))
	require.Equal(t, schemas.ChatMessageRoleDeveloper, result.Messages[0].Role)
}

func TestToOpenAIResponsesRequest_DeveloperRoleAsSystem(t *testing.T) {
	stubDeveloperRoleDatasheet(t)
	req := developerResponsesRequest(schemas.Fireworks, "glm-5")
	result := ToOpenAIResponsesRequest(nil, req)
	require.NotNil(t, result)
	input := result.Input.OpenAIResponsesRequestInputArray
	require.Len(t, input, 3)
	require.Equal(t, schemas.ResponsesMessageTypeAdditionalTools, *input[0].Type)
	require.Equal(t, schemas.ResponsesInputMessageRoleSystem, *input[1].Role)
	require.Equal(t, schemas.ResponsesInputMessageRoleUser, *input[2].Role)
	require.Equal(t, schemas.ResponsesInputMessageRoleDeveloper, *req.Input[1].Role)

	result = ToOpenAIResponsesRequest(nil, developerResponsesRequest(schemas.Fireworks, "qwen3-dev"))
	require.Equal(t, schemas.ResponsesInputMessageRoleDeveloper, *result.Input.OpenAIResponsesRequestInputArray[1].Role)
}
