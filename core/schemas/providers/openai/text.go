package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAITextCompletionRequest converts a Bifrost text completion request to OpenAI format
func ToOpenAITextCompletionRequest(bifrostReq *schemas.BifrostRequest) *OpenAITextCompletionRequest {
	if bifrostReq == nil || bifrostReq.Input.TextCompletionInput == nil {
		return nil
	}

	openaiReq := &OpenAITextCompletionRequest{
		Model:  bifrostReq.Model,
		Prompt: *bifrostReq.Input.TextCompletionInput,
	}

	// Handle OpenAI-specific parameters from ExtraParams
	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		// Echo prompt
		if echo, ok := bifrostReq.Params.ExtraParams["echo"].(bool); ok {
			openaiReq.Echo = &echo
		}

		// Best of
		if bestOf, ok := bifrostReq.Params.ExtraParams["best_of"].(int); ok {
			openaiReq.BestOf = &bestOf
		}

		// Suffix
		if suffix, ok := bifrostReq.Params.ExtraParams["suffix"].(string); ok {
			openaiReq.Suffix = &suffix
		}
	}

	return openaiReq
}
