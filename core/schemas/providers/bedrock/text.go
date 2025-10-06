package bedrock

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/anthropic"
)

// ToBedrockTextCompletionRequest converts a Bifrost text completion request to Bedrock format
func ToBedrockTextCompletionRequest(bifrostReq *schemas.BifrostTextCompletionRequest) *BedrockTextCompletionRequest {
	if bifrostReq == nil || (bifrostReq.Input.PromptStr == nil && len(bifrostReq.Input.PromptArray) == 0) {
		return nil
	}

	// Extract the raw prompt from bifrostReq
	prompt := ""
	if bifrostReq.Input != nil {
		if bifrostReq.Input.PromptStr != nil {
			prompt = *bifrostReq.Input.PromptStr
		} else if len(bifrostReq.Input.PromptArray) > 0 && bifrostReq.Input.PromptArray != nil {
			prompt = strings.Join(bifrostReq.Input.PromptArray, "\n\n")
		}
	}

	bedrockReq := &BedrockTextCompletionRequest{
		Prompt: prompt,
	}

	// Apply parameters
	if bifrostReq.Params != nil {
		bedrockReq.Temperature = bifrostReq.Params.Temperature
		bedrockReq.TopP = bifrostReq.Params.TopP

		if bifrostReq.Params.ExtraParams != nil {
			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				bedrockReq.TopK = topK
			}
		}
	}

	// Apply model-specific formatting and field naming
	if strings.Contains(bifrostReq.Model, "anthropic.") || strings.Contains(bifrostReq.Model, "claude") {
		// For Claude models, wrap the prompt in Anthropic format and use Anthropic field names
		anthropicReq := anthropic.ToAnthropicTextCompletionRequest(bifrostReq)
		bedrockReq.Prompt = anthropicReq.Prompt
		bedrockReq.MaxTokensToSample = &anthropicReq.MaxTokensToSample
		bedrockReq.StopSequences = anthropicReq.StopSequences
	} else {
		// For other models, use standard field names with raw prompt
		if bifrostReq.Params != nil {
			bedrockReq.MaxTokens = bifrostReq.Params.MaxTokens
			bedrockReq.Stop = bifrostReq.Params.Stop
		}
	}

	return bedrockReq
}

// ToBifrostResponse converts a Bedrock Anthropic text response to Bifrost format
func (response *BedrockAnthropicTextResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	return &schemas.BifrostResponse{
		Choices: []schemas.BifrostChatResponseChoice{
			{
				Index: 0,
				BifrostTextCompletionResponseChoice: &schemas.BifrostTextCompletionResponseChoice{
					Text: &response.Completion,
				},
				FinishReason: &response.StopReason,
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.TextCompletionRequest,
			Provider:    schemas.Bedrock,
		},
	}
}

// ToBifrostResponse converts a Bedrock Mistral text response to Bifrost format
func (response *BedrockMistralTextResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	var choices []schemas.BifrostChatResponseChoice
	for i, output := range response.Outputs {
		choices = append(choices, schemas.BifrostChatResponseChoice{
			Index: i,
			BifrostTextCompletionResponseChoice: &schemas.BifrostTextCompletionResponseChoice{
				Text: &output.Text,
			},
			FinishReason: &output.StopReason,
		})
	}

	return &schemas.BifrostResponse{
		Choices: choices,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.TextCompletionRequest,
			Provider:    schemas.Bedrock,
		},
	}
}
