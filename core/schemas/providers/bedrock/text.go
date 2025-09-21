package bedrock

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const AnthropicDefaultMaxTokens = 4096

// ToBedrockTextCompletionRequest converts a Bifrost text completion request to Bedrock format
func ToBedrockTextCompletionRequest(bifrostReq *schemas.BifrostRequest) *BedrockTextCompletionRequest {
	if bifrostReq == nil || bifrostReq.Input.TextCompletionInput == nil {
		return nil
	}

	bedrockReq := &BedrockTextCompletionRequest{
		Prompt: *bifrostReq.Input.TextCompletionInput,
	}

	// Convert parameters if present
	if bifrostReq.Params != nil {
		// Handle max tokens with model-specific logic
		if bifrostReq.Params.MaxTokens != nil {
			if strings.Contains(bifrostReq.Model, "anthropic.") {
				bedrockReq.MaxTokensToSample = bifrostReq.Params.MaxTokens
			} else {
				bedrockReq.MaxTokens = bifrostReq.Params.MaxTokens
			}
		}

		// Standard sampling parameters
		bedrockReq.Temperature = bifrostReq.Params.Temperature
		bedrockReq.TopP = bifrostReq.Params.TopP
		bedrockReq.TopK = bifrostReq.Params.TopK

		// Handle stop sequences with dual support
		if bifrostReq.Params.StopSequences != nil {
			if strings.Contains(bifrostReq.Model, "anthropic.") {
				bedrockReq.StopSequences = bifrostReq.Params.StopSequences
			} else {
				bedrockReq.Stop = bifrostReq.Params.StopSequences
			}
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
					StopString: &response.Stop,
				},
				FinishReason: &response.StopReason,
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
				Message: schemas.BifrostMessage{
					Role: schemas.ModelChatMessageRoleAssistant,
					Content: schemas.MessageContent{
						ContentStr: &output.Text,
					},
				},
			},
			FinishReason: &output.StopReason,
		})
	}

	return &schemas.BifrostResponse{
		Choices: choices,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Bedrock,
		},
	}
}
