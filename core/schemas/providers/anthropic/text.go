package anthropic

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToAnthropicTextCompletionRequest converts a Bifrost text completion request to Anthropic format
func ToAnthropicTextCompletionRequest(bifrostReq *schemas.BifrostTextCompletionRequest) *AnthropicTextRequest {
	prompt := ""
	if bifrostReq.Input.Prompt != nil {
		prompt = *bifrostReq.Input.Prompt
	} else if len(bifrostReq.Input.PromptArray) > 0 {
		prompt = strings.Join(bifrostReq.Input.PromptArray, "\n\n")
	}

	anthropicReq := &AnthropicTextRequest{
		Model:             bifrostReq.Model,
		Prompt:            fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", prompt),
		MaxTokensToSample: AnthropicDefaultMaxTokens, // Default value
	}

	// Convert parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxTokens != nil {
			anthropicReq.MaxTokensToSample = *bifrostReq.Params.MaxTokens
		}
		anthropicReq.Temperature = bifrostReq.Params.Temperature
		anthropicReq.TopP = bifrostReq.Params.TopP
		anthropicReq.StopSequences = bifrostReq.Params.Stop

		if bifrostReq.Params.ExtraParams != nil {
			if topK, ok := bifrostReq.Params.ExtraParams["top_k"].(int); ok {
				anthropicReq.TopK = &topK
			}
		}
	}

	return anthropicReq
}

func (response *AnthropicTextResponse) ToBifrostResponse() *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ID: response.ID,
		Choices: []schemas.BifrostChatResponseChoice{
			{
				Index: 0,
				BifrostTextCompletionResponseChoice: &schemas.BifrostTextCompletionResponseChoice{
					Text: &response.Completion,
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
