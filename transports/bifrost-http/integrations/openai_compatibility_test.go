package integrations

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func Ptr[T any](v T) *T {
	return &v
}

func TestStripExtraFieldsForOpenAI_ChatResponse(t *testing.T) {
	resp := &schemas.BifrostChatResponse{
		ID:      "test-id",
		Created: 1234567890,
		Model:   "anthropic/claude-3-5-sonnet",
		Object:  "chat.completion",
		Usage:   &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		ExtraFields: &schemas.BifrostResponseExtraFields{
			Provider:       schemas.Anthropic,
			ModelRequested: "claude-3-5-sonnet",
			Latency:        150,
		},
		SearchResults: []schemas.SearchResult{{Title: "Test", URL: "http://test.com"}},
		Videos:        []schemas.VideoResult{{URL: "http://video.com"}},
		Citations:     []string{"citation1"},
	}

	stripped := StripExtraFieldsForOpenAI(resp)

	assert.Equal(t, "test-id", stripped.ID)
	assert.Equal(t, "anthropic/claude-3-5-sonnet", stripped.Model)
	assert.NotNil(t, stripped.Usage)
	assert.Equal(t, 10, stripped.Usage.PromptTokens)
	assert.Equal(t, 20, stripped.Usage.CompletionTokens)
	assert.Equal(t, 30, stripped.Usage.TotalTokens)

	assert.Nil(t, stripped.ExtraFields)
	assert.Nil(t, stripped.SearchResults)
	assert.Nil(t, stripped.Videos)
	assert.Nil(t, stripped.Citations)

	assert.NotSame(t, resp, stripped, "should return a new struct")
}

func TestStripExtraFieldsForOpenAI_ChatResponse_Nil(t *testing.T) {
	result := StripExtraFieldsForOpenAI(nil)
	assert.Nil(t, result)
}

func TestStripExtraFieldsForOpenAI_ChatResponse_NilExtraFields(t *testing.T) {
	resp := &schemas.BifrostChatResponse{
		ID:          "test-id",
		Model:       "anthropic/claude-3-5-sonnet",
		Usage:       &schemas.BifrostLLMUsage{PromptTokens: 10},
		ExtraFields: nil,
	}

	stripped := StripExtraFieldsForOpenAI(resp)

	assert.Equal(t, "test-id", stripped.ID)
	assert.Nil(t, stripped.ExtraFields)
	assert.Nil(t, stripped.SearchResults)
	assert.Nil(t, stripped.Videos)
	assert.Nil(t, stripped.Citations)
}

func TestStripExtraFieldsForOpenAIText(t *testing.T) {
	resp := &schemas.BifrostTextCompletionResponse{
		ID:    "test-id",
		Model: "openai/gpt-4o-mini",
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		ExtraFields: &schemas.BifrostResponseExtraFields{
			Provider:   schemas.OpenAI,
			Latency:    100,
			RawRequest: map[string]string{"key": "value"},
		},
	}

	stripped := stripExtraFieldsForOpenAIText(resp)

	assert.Equal(t, "test-id", stripped.ID)
	assert.NotNil(t, stripped.Usage)
	assert.Nil(t, stripped.ExtraFields)
}

func TestStripExtraFieldsForOpenAIText_Nil(t *testing.T) {
	result := stripExtraFieldsForOpenAIText(nil)
	assert.Nil(t, result)
}

func TestStripExtraFieldsForOpenAIResponses(t *testing.T) {
	resp := &schemas.BifrostResponsesResponse{
		ID:    Ptr("test-id"),
		Model: "anthropic/claude-3-5-sonnet",
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
		ExtraFields: &schemas.BifrostResponseExtraFields{
			Provider:       schemas.Anthropic,
			ModelRequested: "claude-3-5-sonnet",
			Latency:        200,
		},
		SearchResults: []schemas.SearchResult{{Title: "Search Result", URL: "http://search.com"}},
		Videos:        []schemas.VideoResult{{URL: "http://video.com"}},
		Citations:     []string{"cite1", "cite2"},
	}

	stripped := stripExtraFieldsForOpenAIResponses(resp)

	assert.Equal(t, "test-id", *stripped.ID)
	assert.NotNil(t, stripped.Usage)
	assert.Equal(t, 10, stripped.Usage.InputTokens)
	assert.Equal(t, 20, stripped.Usage.OutputTokens)
	assert.Equal(t, 30, stripped.Usage.TotalTokens)
	assert.Nil(t, stripped.ExtraFields)
	assert.Nil(t, stripped.SearchResults)
	assert.Nil(t, stripped.Videos)
	assert.Nil(t, stripped.Citations)
}

func TestStripExtraFieldsForOpenAIResponses_Nil(t *testing.T) {
	result := stripExtraFieldsForOpenAIResponses(nil)
	assert.Nil(t, result)
}

func TestStripExtraFieldsForOpenAIResponsesStream(t *testing.T) {
	resp := &schemas.BifrostResponsesStreamResponse{
		Type:           "response.created",
		SequenceNumber: 0,
		Response: &schemas.BifrostResponsesResponse{
			ID:    Ptr("test-id"),
			Model: "anthropic/claude-3-5-sonnet",
			Usage: &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
		},
		ExtraFields: &schemas.BifrostResponseExtraFields{
			Provider:       schemas.Anthropic,
			ModelRequested: "claude-3-5-sonnet",
			Latency:        200,
		},
		SearchResults: []schemas.SearchResult{{Title: "Search Result", URL: "http://search.com"}},
		Videos:        []schemas.VideoResult{{URL: "http://video.com"}},
		Citations:     []string{"cite1"},
	}

	stripped := stripExtraFieldsForOpenAIResponsesStream(resp)

	assert.Equal(t, "response.created", string(stripped.Type))
	assert.Equal(t, 0, stripped.SequenceNumber)
	assert.NotNil(t, stripped.Response)
	assert.Equal(t, "test-id", *stripped.Response.ID)
	assert.NotNil(t, stripped.Response.Usage)
	assert.Equal(t, 10, stripped.Response.Usage.InputTokens)
	assert.Equal(t, 20, stripped.Response.Usage.OutputTokens)
	assert.Equal(t, 30, stripped.Response.Usage.TotalTokens)
	assert.Nil(t, stripped.ExtraFields)
	assert.Nil(t, stripped.SearchResults)
	assert.Nil(t, stripped.Videos)
	assert.Nil(t, stripped.Citations)
}

func TestStripExtraFieldsForOpenAIResponsesStream_Nil(t *testing.T) {
	result := stripExtraFieldsForOpenAIResponsesStream(nil)
	assert.Nil(t, result)
}

func TestStripExtraFieldsForOpenAIError(t *testing.T) {
	err := &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: "test error",
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:       schemas.OpenAI,
			ModelRequested: "gpt-4o",
			RequestType:    schemas.ChatCompletionRequest,
		},
	}

	stripped := stripExtraFieldsForOpenAIError(err)

	assert.True(t, stripped.IsBifrostError)
	assert.NotNil(t, stripped.Error)
	assert.Equal(t, "test error", stripped.Error.Message)
	assert.Equal(t, schemas.BifrostErrorExtraFields{}, stripped.ExtraFields)
}

func TestStripExtraFieldsForOpenAIError_Nil(t *testing.T) {
	result := stripExtraFieldsForOpenAIError(nil)
	assert.Nil(t, result)
}

func TestOpenAIResponseConverter_OpenAIProvider(t *testing.T) {
	resp := &schemas.BifrostChatResponse{
		ID:      "test-id",
		Created: 1234567890,
		Model:   "gpt-4o",
		Object:  "chat.completion",
		Usage:   &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		ExtraFields: &schemas.BifrostResponseExtraFields{
			Provider:       schemas.OpenAI,
			ModelRequested: "gpt-4o",
			Latency:        100,
			RawResponse:    map[string]interface{}{"id": "raw-id"},
		},
	}

	assert.NotNil(t, resp.ExtraFields)
	assert.Equal(t, schemas.OpenAI, resp.ExtraFields.Provider)
	assert.Equal(t, map[string]interface{}{"id": "raw-id"}, resp.ExtraFields.RawResponse)

	result := StripExtraFieldsForOpenAI(resp)
	assert.Equal(t, "test-id", result.ID)
	assert.Equal(t, "gpt-4o", result.Model)
	assert.NotNil(t, result.Usage)
	assert.Nil(t, result.ExtraFields)
	assert.Nil(t, result.SearchResults)
	assert.Nil(t, result.Videos)
	assert.Nil(t, result.Citations)
}

func TestOpenAIResponseConverter_NonOpenAIProvider_StripsFields(t *testing.T) {
	resp := &schemas.BifrostChatResponse{
		ID:      "test-id",
		Created: 1234567890,
		Model:   "anthropic/claude-3-5-sonnet",
		Object:  "chat.completion",
		Usage:   &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		ExtraFields: &schemas.BifrostResponseExtraFields{
			Provider:       schemas.Anthropic,
			ModelRequested: "claude-3-5-sonnet",
			Latency:        150,
		},
		SearchResults: []schemas.SearchResult{{Title: "Test", URL: "http://test.com"}},
		Videos:        []schemas.VideoResult{{URL: "http://video.com"}},
		Citations:     []string{"citation1"},
	}

	result := StripExtraFieldsForOpenAI(resp)

	assert.Equal(t, "test-id", result.ID)
	assert.Equal(t, "anthropic/claude-3-5-sonnet", result.Model)
	assert.NotNil(t, result.Usage)
	assert.Equal(t, 10, result.Usage.PromptTokens)
	assert.Equal(t, 20, result.Usage.CompletionTokens)
	assert.Equal(t, 30, result.Usage.TotalTokens)
	assert.Nil(t, result.ExtraFields)
	assert.Nil(t, result.SearchResults)
	assert.Nil(t, result.Videos)
	assert.Nil(t, result.Citations)
}

func TestOpenAIResponseConverter_NilExtraFields_StripsOtherFields(t *testing.T) {
	resp := &schemas.BifrostChatResponse{
		ID:            "test-id",
		Created:       1234567890,
		Model:         "anthropic/claude-3-5-sonnet",
		Object:        "chat.completion",
		Usage:         &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		ExtraFields:   nil,
		SearchResults: []schemas.SearchResult{{Title: "Test", URL: "http://test.com"}},
		Videos:        []schemas.VideoResult{{URL: "http://video.com"}},
		Citations:     []string{"citation1"},
	}

	result := StripExtraFieldsForOpenAI(resp)

	assert.Equal(t, "test-id", result.ID)
	assert.Nil(t, result.ExtraFields)
	assert.Nil(t, result.SearchResults)
	assert.Nil(t, result.Videos)
	assert.Nil(t, result.Citations)
}

func TestPerplexityFieldsAreStripped(t *testing.T) {
	resp := &schemas.BifrostChatResponse{
		ID:      "perplexity-test",
		Created: 1234567890,
		Model:   "perplexity/sonar-reasoning",
		Object:  "chat.completion",
		Usage:   &schemas.BifrostLLMUsage{PromptTokens: 50, CompletionTokens: 100, TotalTokens: 150},
		ExtraFields: &schemas.BifrostResponseExtraFields{
			Provider:       schemas.Perplexity,
			ModelRequested: "sonar-reasoning",
			Latency:        300,
		},
		SearchResults: []schemas.SearchResult{
			{Title: "Result 1", URL: "http://example.com/1"},
			{Title: "Result 2", URL: "http://example.com/2"},
		},
		Videos:    []schemas.VideoResult{{URL: "http://youtube.com/video1"}},
		Citations: []string{"cite1", "cite2", "cite3"},
	}

	stripped := StripExtraFieldsForOpenAI(resp)

	assert.Nil(t, stripped.SearchResults)
	assert.Nil(t, stripped.Videos)
	assert.Nil(t, stripped.Citations)
	assert.NotNil(t, stripped.Usage)
	assert.Equal(t, 50, stripped.Usage.PromptTokens)
	assert.Equal(t, 100, stripped.Usage.CompletionTokens)
	assert.Equal(t, 150, stripped.Usage.TotalTokens)
}

func TestOpenAIResponseFormat_UsagePreserved(t *testing.T) {
	resp := &schemas.BifrostChatResponse{
		ID:                "usage-test",
		Created:           1234567890,
		Model:             "anthropic/claude-3-opus-20241120",
		Object:            "chat.completion",
		SystemFingerprint: "fp_abc123",
		ServiceTier:       Ptr[string]("standard"),
		Usage: &schemas.BifrostLLMUsage{
			PromptTokens:     100,
			CompletionTokens: 250,
			TotalTokens:      350,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{
				TextTokens:   80,
				CachedTokens: 20,
			},
			CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
				ReasoningTokens: 100,
			},
			Cost: &schemas.BifrostCost{
				InputTokensCost:  0.001,
				OutputTokensCost: 0.0025,
				TotalCost:        0.0035,
			},
		},
		ExtraFields: &schemas.BifrostResponseExtraFields{
			Provider:       schemas.Anthropic,
			ModelRequested: "claude-3-opus-20241120",
			Latency:        500,
		},
	}

	stripped := StripExtraFieldsForOpenAI(resp)

	assert.NotNil(t, stripped.Usage)
	assert.Equal(t, 100, stripped.Usage.PromptTokens)
	assert.Equal(t, 250, stripped.Usage.CompletionTokens)
	assert.Equal(t, 350, stripped.Usage.TotalTokens)
	assert.NotNil(t, stripped.Usage.PromptTokensDetails)
	assert.Equal(t, 80, stripped.Usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, 20, stripped.Usage.PromptTokensDetails.CachedTokens)
	assert.NotNil(t, stripped.Usage.CompletionTokensDetails)
	assert.Equal(t, 100, stripped.Usage.CompletionTokensDetails.ReasoningTokens)
	assert.NotNil(t, stripped.Usage.Cost)
	assert.Equal(t, 0.0035, stripped.Usage.Cost.TotalCost)
	assert.Nil(t, stripped.ExtraFields)
}

func TestOpenAIResponseConverter_AllProviderTypes(t *testing.T) {
	providers := []schemas.ModelProvider{
		schemas.Anthropic,
		schemas.Bedrock,
		schemas.Cohere,
		schemas.Gemini,
		schemas.Groq,
		schemas.Mistral,
		schemas.Ollama,
		schemas.OpenRouter,
		schemas.Perplexity,
		schemas.XAI,
	}

	for _, provider := range providers {
		t.Run(string(provider), func(t *testing.T) {
			resp := &schemas.BifrostChatResponse{
				ID:      "test-" + string(provider),
				Created: 1234567890,
				Model:   string(provider) + "/model-name",
				Object:  "chat.completion",
				Usage:   &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
				ExtraFields: &schemas.BifrostResponseExtraFields{
					Provider:       provider,
					ModelRequested: "model-name",
					Latency:        100,
				},
				SearchResults: []schemas.SearchResult{{Title: "Test", URL: "http://test.com"}},
				Videos:        []schemas.VideoResult{{URL: "http://video.com"}},
				Citations:     []string{"citation1"},
			}

			stripped := StripExtraFieldsForOpenAI(resp)

			assert.Nil(t, stripped.ExtraFields, "ExtraFields should be nil for provider: "+string(provider))
			assert.Nil(t, stripped.SearchResults, "SearchResults should be nil for provider: "+string(provider))
			assert.Nil(t, stripped.Videos, "Videos should be nil for provider: "+string(provider))
			assert.Nil(t, stripped.Citations, "Citations should be nil for provider: "+string(provider))
			assert.NotNil(t, stripped.Usage, "Usage should be preserved for provider: "+string(provider))
		})
	}
}
