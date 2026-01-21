package openai

import (
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostSpeechRequest converts an OpenAI speech request to Bifrost format
func (request *OpenAISpeechRequest) ToBifrostSpeechRequest(ctx *schemas.BifrostContext) *schemas.BifrostSpeechRequest {
	provider, model := schemas.ParseModelString(request.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.OpenAI))

	return &schemas.BifrostSpeechRequest{
		Provider:  provider,
		Model:     model,
		Input:     &schemas.SpeechInput{Input: request.Input},
		Params:    &request.SpeechParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

// ToOpenAISpeechRequest converts a Bifrost speech request to OpenAI format
func ToOpenAISpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) *OpenAISpeechRequest {
	if bifrostReq == nil || bifrostReq.Input.Input == "" {
		return nil
	}

	speechInput := bifrostReq.Input
	params := bifrostReq.Params

	openaiReq := &OpenAISpeechRequest{
		Model: bifrostReq.Model,
		Input: speechInput.Input,
	}

	if params != nil {
		openaiReq.SpeechParameters = *params
	}

	return openaiReq
}
