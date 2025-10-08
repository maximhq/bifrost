package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAITextCompletionRequest converts a Bifrost text completion request to OpenAI format
func ToOpenAITextCompletionRequest(bifrostReq *schemas.BifrostTextCompletionRequest) *OpenAITextCompletionRequest {
	if bifrostReq == nil {
		return nil
	}

	openaiReq := AcquireTextRequest()
	openaiReq.Model = bifrostReq.Model
	openaiReq.Prompt = bifrostReq.Input // schemas.TextCompletionInput - not pooled per user instruction

	if bifrostReq.Params != nil {
		openaiReq.TextCompletionParameters = *bifrostReq.Params
	}

	return openaiReq
}

func (r *OpenAITextCompletionRequest) ToBifrostRequest() *schemas.BifrostTextCompletionRequest {
	if r == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	return &schemas.BifrostTextCompletionRequest{
		Provider: provider,
		Model:    model,
		Input:    r.Prompt,
		Params:   &r.TextCompletionParameters,
	}
}
