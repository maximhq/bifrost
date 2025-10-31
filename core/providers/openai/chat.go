package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToBifrostChatRequest converts an OpenAI chat request to Bifrost format
func (request *OpenAIChatRequest) ToBifrostChatRequest() *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input:    request.Messages,
		Params:   &request.ChatParameters,
	}

	return bifrostReq
}

// ToOpenAIChatRequest converts a Bifrost chat completion request to OpenAI format
func ToOpenAIChatRequest(bifrostReq *schemas.BifrostChatRequest) *OpenAIChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	openaiReq := &OpenAIChatRequest{
		Model:    bifrostReq.Model,
		Messages: bifrostReq.Input,
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

		// Remove max_completion_tokens and replace with max_tokens
		if openaiReq.MaxCompletionTokens != nil {
			openaiReq.MaxTokens = openaiReq.MaxCompletionTokens
			openaiReq.MaxCompletionTokens = nil
		}

		// Mistral does not support ToolChoiceStruct, only simple tool choice strings are supported.
		if openaiReq.ToolChoice != nil && openaiReq.ToolChoice.ChatToolChoiceStruct != nil {
			openaiReq.ToolChoice.ChatToolChoiceStr = schemas.Ptr("required")
			openaiReq.ToolChoice.ChatToolChoiceStruct = nil
		}
		return openaiReq
	default:
		openaiReq.filterOpenAISpecificParameters()
		return openaiReq
	}
}

// Filter OpenAI Specific Parameters
func (request *OpenAIChatRequest) filterOpenAISpecificParameters() {
	if request.ChatParameters.ReasoningEffort != nil && *request.ChatParameters.ReasoningEffort == "minimal" {
		request.ChatParameters.ReasoningEffort = schemas.Ptr("low")
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
