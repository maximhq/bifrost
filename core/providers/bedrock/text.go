package bedrock

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
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

// ToBifrostTextCompletionRequest converts a Bedrock text completion request to Bifrost format
func (request *BedrockTextCompletionRequest) ToBifrostTextCompletionRequest(ctx *schemas.BifrostContext) *schemas.BifrostTextCompletionRequest {
	if request == nil {
		return nil
	}

	prompt := request.Prompt
	// Fallback for Claude 3 Messages API
	if prompt == "" && len(request.Messages) > 0 {
		var parts []string
		for _, msg := range request.Messages {
			for _, content := range msg.Content {
				if content.Text != nil {
					parts = append(parts, *content.Text)
				}
			}
		}
		prompt = strings.Join(parts, "\n\n")
	}

	provider, model := schemas.ParseModelString(request.ModelID, utils.CheckAndSetDefaultProvider(ctx, schemas.Bedrock))

	bifrostReq := &schemas.BifrostTextCompletionRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.TextCompletionInput{
			PromptStr: &prompt,
		},
		Params: &schemas.TextCompletionParameters{
			Temperature: request.Temperature,
			TopP:        request.TopP,
		},
	}

	if request.MaxTokens != nil {
		bifrostReq.Params.MaxTokens = request.MaxTokens
	} else if request.MaxTokensToSample != nil {
		bifrostReq.Params.MaxTokens = request.MaxTokensToSample
	}

	if len(request.Stop) > 0 {
		bifrostReq.Params.Stop = request.Stop
	} else if len(request.StopSequences) > 0 {
		bifrostReq.Params.Stop = request.StopSequences
	}

	return bifrostReq
}

// ToBifrostTextCompletionResponse converts a Bedrock Anthropic text response to Bifrost format
func (response *BedrockAnthropicTextResponse) ToBifrostTextCompletionResponse() *schemas.BifrostTextCompletionResponse {
	if response == nil {
		return nil
	}

	return &schemas.BifrostTextCompletionResponse{
		Object: "text_completion",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{
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

// ToBifrostTextCompletionResponse converts a Bedrock Mistral text response to Bifrost format
func (response *BedrockMistralTextResponse) ToBifrostTextCompletionResponse() *schemas.BifrostTextCompletionResponse {
	if response == nil {
		return nil
	}

	var choices []schemas.BifrostResponseChoice
	for i, output := range response.Outputs {
		choices = append(choices, schemas.BifrostResponseChoice{
			Index: i,
			TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{
				Text: &output.Text,
			},
			FinishReason: &output.StopReason,
		})
	}

	return &schemas.BifrostTextCompletionResponse{
		Object:  "text_completion",
		Choices: choices,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.TextCompletionRequest,
			Provider:    schemas.Bedrock,
		},
	}
}

// ToBedrockTextCompletionResponse converts a BifrostTextCompletionResponse back to Bedrock text completion format
// Returns either *BedrockAnthropicTextResponse or *BedrockMistralTextResponse based on the model
func ToBedrockTextCompletionResponse(bifrostResp *schemas.BifrostTextCompletionResponse) interface{} {
	if bifrostResp == nil {
		return nil
	}

	// Determine response format based on model
	// Use ModelRequested from ExtraFields if available, otherwise use Model
	model := bifrostResp.Model
	if bifrostResp.ExtraFields.ModelRequested != "" {
		model = bifrostResp.ExtraFields.ModelRequested
	}

	if strings.Contains(model, "anthropic.") || strings.Contains(model, "claude") {
		// Convert to Anthropic format
		bedrockResp := &BedrockAnthropicTextResponse{}

		// Convert choices to completion text
		if len(bifrostResp.Choices) > 0 {
			choice := bifrostResp.Choices[0] // Anthropic text API typically returns one choice
			if choice.TextCompletionResponseChoice != nil && choice.TextCompletionResponseChoice.Text != nil {
				bedrockResp.Completion = *choice.TextCompletionResponseChoice.Text
			}
			if choice.FinishReason != nil {
				bedrockResp.StopReason = *choice.FinishReason
			}
		}

		return bedrockResp
	} else if strings.Contains(model, "mistral.") {
		// Convert to Mistral format
		bedrockResp := &BedrockMistralTextResponse{}

		// Convert choices to outputs
		for _, choice := range bifrostResp.Choices {
			var output struct {
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			}

			if choice.TextCompletionResponseChoice != nil && choice.TextCompletionResponseChoice.Text != nil {
				output.Text = *choice.TextCompletionResponseChoice.Text
			}
			if choice.FinishReason != nil {
				output.StopReason = *choice.FinishReason
			}

			bedrockResp.Outputs = append(bedrockResp.Outputs, output)
		}

		return bedrockResp
	}

	// Default to Anthropic format if model type cannot be determined
	bedrockResp := &BedrockAnthropicTextResponse{}
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0]
		if choice.TextCompletionResponseChoice != nil && choice.TextCompletionResponseChoice.Text != nil {
			bedrockResp.Completion = *choice.TextCompletionResponseChoice.Text
		}
		if choice.FinishReason != nil {
			bedrockResp.StopReason = *choice.FinishReason
		}
	}

	return bedrockResp
}
