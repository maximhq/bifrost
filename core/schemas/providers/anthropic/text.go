package anthropic

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToAnthropicTextCompletionRequest converts a Bifrost text completion request to Anthropic format
func ToAnthropicTextCompletionRequest(bifrostReq *schemas.BifrostRequest) *AnthropicTextRequest {
	anthropicReq := &AnthropicTextRequest{
		Model:             bifrostReq.Model,
		Prompt:            fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", *bifrostReq.Input.TextCompletionInput),
		MaxTokensToSample: AnthropicDefaultMaxTokens, // Default value
	}

	// Convert parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxTokens != nil {
			anthropicReq.MaxTokensToSample = *bifrostReq.Params.MaxTokens
		}
		anthropicReq.Temperature = bifrostReq.Params.Temperature
		anthropicReq.TopP = bifrostReq.Params.TopP
		anthropicReq.TopK = bifrostReq.Params.TopK
		anthropicReq.StopSequences = bifrostReq.Params.StopSequences
	}

	return anthropicReq
}

func (response *AnthropicTextResponse) ToBifrostResponse() *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ID: response.ID,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: schemas.BifrostMessage{
						Role: schemas.ModelChatMessageRoleAssistant,
						Content: schemas.MessageContent{
							ContentStr: &response.Completion,
						},
					},
				},
			},
		},
		Usage: &schemas.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
		Model: response.Model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Anthropic,
		},
	}
}
