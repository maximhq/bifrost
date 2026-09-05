package routing

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLLMClassifierConfig() *complexity.LLMConfig {
	return &complexity.LLMConfig{
		Provider: "openai",
		Model:    "gpt-5.6-terra",
	}
}

// chatResponseWithText builds a minimal non-stream chat response carrying one
// assistant text choice, the shape chatResponseText reads.
func chatResponseWithText(text string) *schemas.BifrostChatResponse {
	return &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: &text},
					},
				},
			},
		},
	}
}

// responsesResponseWithText builds a Responses answer that round-trips to the
// same assistant text through ToBifrostChatResponse, by using the codebase's
// own ChatMessage->Responses converter as the inverse.
func responsesResponseWithText(text string) *schemas.BifrostResponsesResponse {
	assistant := schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{ContentStr: &text},
	}
	return &schemas.BifrostResponsesResponse{
		Output: assistant.ToResponsesMessages(),
	}
}

func chatError(message string) *schemas.BifrostError {
	return &schemas.BifrostError{Error: &schemas.ErrorField{Message: message}}
}

// The provider's own instruction to switch endpoints, as OpenAI phrases it for
// a reasoning model that cannot serve this request shape on chat completions.
const responsesRequiredErrorMessage = "Function tools with reasoning_effort are not supported for gpt-5.6-terra in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'."

func TestClassifyComplexityViaLLMUsesChatWhenItSucceeds(t *testing.T) {
	plugin := &RoutingPlugin{}
	responsesCalled := false
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return chatResponseWithText(`{"tier":"SIMPLE"}`), nil
	})
	plugin.SetResponsesRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		responsesCalled = true
		return responsesResponseWithText(`{"tier":"COMPLEX"}`), nil
	})

	got, err := plugin.classifyComplexityTextViaLLM(t.Context(), testLLMClassifierConfig(), "system", "classify me")
	require.NoError(t, err)
	assert.Equal(t, `{"tier":"SIMPLE"}`, got)
	assert.False(t, responsesCalled, "responses fallback must not run when chat completions succeeds")
}

func TestClassifyComplexityViaLLMFallsBackToResponses(t *testing.T) {
	plugin := &RoutingPlugin{}
	var gotResponsesReq *schemas.BifrostResponsesRequest
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return nil, chatError(responsesRequiredErrorMessage)
	})
	plugin.SetResponsesRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		gotResponsesReq = req
		return responsesResponseWithText(`{"tier":"MEDIUM"}`), nil
	})

	got, err := plugin.classifyComplexityTextViaLLM(t.Context(), testLLMClassifierConfig(), "system", "classify me")
	require.NoError(t, err)
	assert.Equal(t, `{"tier":"MEDIUM"}`, got)

	require.NotNil(t, gotResponsesReq, "responses executor must be invoked on a /v1/responses rejection")
	assert.Equal(t, schemas.ModelProvider("openai"), gotResponsesReq.Provider)
	assert.Equal(t, "gpt-5.6-terra", gotResponsesReq.Model)
	require.NotNil(t, gotResponsesReq.Params)
	assert.Nil(t, gotResponsesReq.Params.Temperature, "temperature must be stripped for the responses retry")
	require.NotNil(t, gotResponsesReq.Params.MaxOutputTokens, "the completion-token budget must carry over")
	assert.Equal(t, llmClassifierMaxCompletionTokens, *gotResponsesReq.Params.MaxOutputTokens)
}

func TestClassifyComplexityViaLLMFallsBackToResponsesOnTemperatureRejection(t *testing.T) {
	// The exact failure a gpt-5.6 reasoning model returns for the classifier's
	// temperature=0 chat request: no /v1/responses hint, only a temperature
	// complaint. The Responses retry drops temperature, so it must still fire.
	const temperatureError = "Unsupported value: 'temperature' does not support 0 with this model. Only the default (1) value is supported."
	plugin := &RoutingPlugin{}
	var gotResponsesReq *schemas.BifrostResponsesRequest
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return nil, chatError(temperatureError)
	})
	plugin.SetResponsesRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		gotResponsesReq = req
		return responsesResponseWithText(`{"tier":"SIMPLE"}`), nil
	})

	got, err := plugin.classifyComplexityTextViaLLM(t.Context(), testLLMClassifierConfig(), "system", "where is washington")
	require.NoError(t, err)
	assert.Equal(t, `{"tier":"SIMPLE"}`, got)
	require.NotNil(t, gotResponsesReq)
	require.NotNil(t, gotResponsesReq.Params)
	assert.Nil(t, gotResponsesReq.Params.Temperature, "the temperature the chat model rejected must not be resent")
}

func TestClassifyComplexityViaLLMDoesNotRetryUnrelatedError(t *testing.T) {
	plugin := &RoutingPlugin{}
	responsesCalled := false
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return nil, chatError("invalid api key")
	})
	plugin.SetResponsesRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		responsesCalled = true
		return responsesResponseWithText(`{"tier":"SIMPLE"}`), nil
	})

	_, err := plugin.classifyComplexityTextViaLLM(t.Context(), testLLMClassifierConfig(), "system", "classify me")
	require.Error(t, err)
	assert.False(t, responsesCalled, "a non-endpoint error must not trigger the responses fallback")
}

func TestClassifyComplexityViaLLMSurfacesRejectionWhenNoResponsesExecutor(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return nil, chatError(responsesRequiredErrorMessage)
	})
	// No responses executor wired.

	_, err := plugin.classifyComplexityTextViaLLM(t.Context(), testLLMClassifierConfig(), "system", "classify me")
	require.Error(t, err)
	// The original, actionable provider message survives instead of a generic
	// "not configured" so an operator can see why.
	assert.Contains(t, err.Error(), "/v1/responses")
}

func TestLLMClassifierShouldRetryWithResponses(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{name: "nil error", message: "", want: false},
		{name: "function tools with reasoning_effort", message: responsesRequiredErrorMessage, want: true},
		{name: "chat and responses both named", message: "use /v1/responses instead of /v1/chat/completions", want: true},
		{name: "responses named without chat or function tool", message: "the /v1/responses endpoint is down", want: false},
		{name: "temperature zero unsupported", message: "Unsupported value: 'temperature' does not support 0 with this model. Only the default (1) value is supported.", want: true},
		{name: "temperature only default supported", message: "temperature only the default (1) value is supported", want: true},
		{name: "unrelated param unsupported", message: "top_p does not support this value", want: false},
		{name: "plain auth failure", message: "invalid api key", want: false},
		{name: "rate limit", message: "429 too many requests", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err *schemas.BifrostError
			if tt.message != "" {
				err = chatError(tt.message)
			}
			assert.Equal(t, tt.want, llmClassifierShouldRetryWithResponses(err))
		})
	}
}
