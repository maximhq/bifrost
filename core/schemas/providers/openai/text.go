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
		Model:           bifrostReq.Model,
		Prompt:          *bifrostReq.Input.TextCompletionInput,
		ModelParameters: bifrostReq.Params, // Directly embed the parameters
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

// ToOpenAITextCompletionResponse converts an OpenAI text completion response to Bifrost format
func (response *OpenAITextCompletionResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	// Convert choices
	choices := make([]schemas.BifrostResponseChoice, 0, len(response.Choices))
	for i, choice := range response.Choices {
		// Create a copy of the text to avoid pointer issues
		textCopy := choice.Text

		bifrostChoice := schemas.BifrostResponseChoice{
			Index: i,
			BifrostTextCompletionResponseChoice: &schemas.BifrostTextCompletionResponseChoice{
				Text: &textCopy,
			},
			FinishReason: choice.FinishReason,
		}

		// Add log probabilities if available
		if choice.Logprobs != nil {
			bifrostChoice.LogProbs = &schemas.LogProbs{
				TextCompletionLogProb: choice.Logprobs,
			}
		}

		choices = append(choices, bifrostChoice)
	}

	// Create the Bifrost response
	bifrostResponse := &schemas.BifrostResponse{
		ID:     response.ID,
		Object: "list", // Standard Bifrost object type for completions
		ChatCompletionsExtendedResponse: &schemas.ChatCompletionsExtendedResponse{
			Choices: choices,
		},
		Model:   response.Model,
		Created: response.Created,
		// Set provider outside of this function
	}

	// Set system fingerprint
	if response.SystemFingerprint != nil {
		bifrostResponse.SystemFingerprint = response.SystemFingerprint
	}

	// Set usage information
	if response.Usage != nil {
		// Create a copy to avoid pointer issues
		usageCopy := *response.Usage
		bifrostResponse.Usage = &usageCopy
	}

	return bifrostResponse
}
