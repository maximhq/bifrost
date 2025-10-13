package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAITextCompletionRequest converts a Bifrost text completion request to OpenAI format
func ToOpenAITextCompletionRequest(bifrostReq *schemas.BifrostTextCompletionRequest) *OpenAITextCompletionRequest {
	if bifrostReq == nil {
		return nil
	}

	params := bifrostReq.Params

	openaiReq := &OpenAITextCompletionRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input,
	}

	if params != nil {
		openaiReq.TextCompletionParameters = *params
	}

	return openaiReq
}

func (request *OpenAITextCompletionRequest) ToBifrostRequest() *schemas.BifrostTextCompletionRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

	return &schemas.BifrostTextCompletionRequest{
		Provider: provider,
		Model:    model,
		Input:    request.Prompt,
		Params:   &request.TextCompletionParameters,
	}
}
