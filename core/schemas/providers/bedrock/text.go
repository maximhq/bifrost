package bedrock

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/anthropic"
)

// ToBedrockTextCompletionRequest converts a Bifrost text completion request to Bedrock format
func ToBedrockTextCompletionRequest(bifrostReq *schemas.BifrostRequest) *BedrockTextCompletionRequest {
	if bifrostReq == nil || bifrostReq.Input.TextCompletionInput == nil {
		return nil
	}

	anthropicReq := anthropic.ToAnthropicTextCompletionRequest(bifrostReq)

	bedrockReq := &BedrockTextCompletionRequest{
		Prompt:      anthropicReq.Prompt,
		Temperature: anthropicReq.Temperature,
		TopP:        anthropicReq.TopP,
		TopK:        anthropicReq.TopK,
	}

	if strings.Contains(bifrostReq.Model, "anthropic.") || strings.Contains(bifrostReq.Model, "claude") {
		bedrockReq.MaxTokensToSample = &anthropicReq.MaxTokensToSample
		bedrockReq.StopSequences = anthropicReq.StopSequences
	} else {
		bedrockReq.MaxTokens = &anthropicReq.MaxTokensToSample
		bedrockReq.Stop = anthropicReq.StopSequences
	}

	return bedrockReq
}

// ToBifrostResponse converts a Bedrock Anthropic text response to Bifrost format
func (response *BedrockAnthropicTextResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	return &schemas.BifrostResponse{
		ChatCompletionsExtendedResponse: &schemas.ChatCompletionsExtendedResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
						Message: schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							Content: schemas.ChatMessageContent{
								ContentStr: &response.Completion,
							},
						},
						StopString: &response.Stop,
					},
					FinishReason: &response.StopReason,
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Bedrock,
		},
	}
}

// ToBifrostResponse converts a Bedrock Mistral text response to Bifrost format
func (response *BedrockMistralTextResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	var choices []schemas.BifrostResponseChoice
	for i, output := range response.Outputs {
		choices = append(choices, schemas.BifrostResponseChoice{
			Index: i,
			BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
				Message: schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: schemas.ChatMessageContent{
						ContentStr: &output.Text,
					},
				},
			},
			FinishReason: &output.StopReason,
		})
	}

	return &schemas.BifrostResponse{
		ChatCompletionsExtendedResponse: &schemas.ChatCompletionsExtendedResponse{
			Choices: choices,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Bedrock,
		},
	}
}
