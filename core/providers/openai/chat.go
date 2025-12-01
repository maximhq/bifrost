package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostChatRequest converts an OpenAI chat request to Bifrost format
func (request *OpenAIChatRequest) ToBifrostChatRequest() *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

	return &schemas.BifrostChatRequest{
		Provider:  provider,
		Model:     model,
		Input:     ConvertOpenAIMessagesToBifrostMessages(request.Messages),
		Params:    &request.ChatParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

// ToOpenAIChatRequest converts a Bifrost chat completion request to OpenAI format
func ToOpenAIChatRequest(bifrostReq *schemas.BifrostChatRequest) *OpenAIChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	openaiReq := &OpenAIChatRequest{
		Model:    bifrostReq.Model,
		Messages: ConvertBifrostMessagesToOpenAIMessages(bifrostReq.Input),
	}

	if bifrostReq.Params != nil {
		openaiReq.ChatParameters = *bifrostReq.Params
	}

	switch bifrostReq.Provider {
	case schemas.OpenAI:
		return openaiReq
	case schemas.Gemini:
		openaiReq.filterOpenAISpecificParameters()
		// Removing extra parameters that are not supported by Gemini
		openaiReq.ServiceTier = nil
		return openaiReq
	case schemas.Mistral:
		openaiReq.filterOpenAISpecificParameters()
		openaiReq.applyMistralCompatibility()
		return openaiReq
	case schemas.Vertex:
		openaiReq.filterOpenAISpecificParameters()

		// Apply Mistral-specific transformations for Vertex Mistral models
		if schemas.IsMistralModel(bifrostReq.Model) {
			openaiReq.applyMistralCompatibility()
		}
		return openaiReq
	default:
		openaiReq.filterOpenAISpecificParameters()
		return openaiReq
	}
}

// Filter OpenAI Specific Parameters
func (request *OpenAIChatRequest) filterOpenAISpecificParameters() {
	if request.ChatParameters.Reasoning != nil && request.ChatParameters.Reasoning.Effort != nil && *request.ChatParameters.Reasoning.Effort == "minimal" {
		request.ChatParameters.Reasoning.Effort = schemas.Ptr("low")
	}
	if request.ChatParameters.PromptCacheKey != nil {
		request.ChatParameters.PromptCacheKey = nil
	}
	if request.ChatParameters.Verbosity != nil {
		request.ChatParameters.Verbosity = nil
	}
	if request.ChatParameters.Store != nil {
		request.ChatParameters.Store = nil
	}
}

// applyMistralCompatibility applies Mistral-specific transformations to the request
func (request *OpenAIChatRequest) applyMistralCompatibility() {
	// Mistral uses max_tokens instead of max_completion_tokens
	if request.MaxCompletionTokens != nil {
		request.MaxTokens = request.MaxCompletionTokens
		request.MaxCompletionTokens = nil
	}

	// Mistral does not support ToolChoiceStruct, only simple tool choice strings are supported
	if request.ToolChoice != nil && request.ToolChoice.ChatToolChoiceStruct != nil {
		request.ToolChoice.ChatToolChoiceStr = schemas.Ptr("any")
		request.ToolChoice.ChatToolChoiceStruct = nil
	}
}
