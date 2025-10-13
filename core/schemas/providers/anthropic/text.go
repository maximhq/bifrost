package anthropic

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToAnthropicTextCompletionRequest converts a Bifrost text completion request to Anthropic format
func ToAnthropicTextCompletionRequest(bifrostReq *schemas.BifrostTextCompletionRequest) *AnthropicTextRequest {
	if bifrostReq == nil {
		return nil
	}

	prompt := ""
	if bifrostReq.Input.PromptStr != nil {
		prompt = *bifrostReq.Input.PromptStr
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
			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				anthropicReq.TopK = topK
			}
		}
	}

	return anthropicReq
}

// ToBifrostRequest converts an Anthropic text request back to Bifrost format
func (request *AnthropicTextRequest) ToBifrostRequest() *schemas.BifrostTextCompletionRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostTextCompletionRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.TextCompletionInput{
			PromptStr: &request.Prompt,
		},
		Params: &schemas.TextCompletionParameters{
			MaxTokens:   &request.MaxTokensToSample,
			Temperature: request.Temperature,
			TopP:        request.TopP,
			Stop:        request.StopSequences,
		},
	}

	// Add extra params if present
	if request.TopK != nil {
		bifrostReq.Params.ExtraParams = map[string]interface{}{
			"top_k": *request.TopK,
		}
	}

	return bifrostReq
}

func (response *AnthropicTextResponse) ToBifrostResponse() *schemas.BifrostTextCompletionResponse {
	if response == nil {
		return nil
	}
	return &schemas.BifrostTextCompletionResponse{
		ID: response.ID,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{
					Text: &response.Completion,
				},
			},
		},
		Usage: &schemas.BifrostLLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
		Model: response.Model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.TextCompletionRequest,
			Provider:    schemas.Anthropic,
		},
	}
}

// ToAnthropicTextCompletionResponse converts a BifrostResponse back to Anthropic text completion format
func ToAnthropicTextCompletionResponse(bifrostResp *schemas.BifrostTextCompletionResponse) *AnthropicTextResponse {
	if bifrostResp == nil {
		return nil
	}

	anthropicResp := &AnthropicTextResponse{
		ID:    bifrostResp.ID,
		Type:  "completion",
		Model: bifrostResp.Model,
	}

	// Convert choices to completion text
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0] // Anthropic text API typically returns one choice

		if choice.TextCompletionResponseChoice != nil && choice.TextCompletionResponseChoice.Text != nil {
			anthropicResp.Completion = *choice.TextCompletionResponseChoice.Text
		}
	}

	// Convert usage information
	if bifrostResp.Usage != nil {
		anthropicResp.Usage.InputTokens = bifrostResp.Usage.PromptTokens
		anthropicResp.Usage.OutputTokens = bifrostResp.Usage.CompletionTokens
	}

	return anthropicResp
}
