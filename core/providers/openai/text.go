package openai

import (
	"github.com/maximhq/bifrost/core/providers/utils"
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
		// Drop user field if it exceeds OpenAI's 64 character limit
		openaiReq.TextCompletionParameters.User = SanitizeUserField(openaiReq.TextCompletionParameters.User)
	}
	if bifrostReq.Params != nil {
		openaiReq.ExtraParams = bifrostReq.Params.ExtraParams
	}
	return openaiReq
}

// ToBifrostTextCompletionRequest converts an OpenAI text completion request to Bifrost format
func (req *OpenAITextCompletionRequest) ToBifrostTextCompletionRequest(ctx *schemas.BifrostContext) *schemas.BifrostTextCompletionRequest {
	if req == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(req.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.OpenAI))

	return &schemas.BifrostTextCompletionRequest{
		Provider:  provider,
		Model:     model,
		Input:     req.Prompt,
		Params:    &req.TextCompletionParameters,
		Fallbacks: schemas.ParseFallbacks(req.Fallbacks),
	}
}
