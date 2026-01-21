package openai

import (
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAIImageGenerationRequest converts a Bifrost Image Request to OpenAI format
func ToOpenAIImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *OpenAIImageGenerationRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Prompt == "" {
		return nil
	}

	req := &OpenAIImageGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}

	if bifrostReq.Params != nil {
		req.ImageGenerationParameters = *bifrostReq.Params
	}

	switch bifrostReq.Provider {
	case schemas.XAI:
		filterXAISpecificParameters(req)
	case schemas.OpenAI, schemas.Azure:
		filterOpenAISpecificParameters(req)
	}
	return req
}

func filterXAISpecificParameters(req *OpenAIImageGenerationRequest) {
	req.ImageGenerationParameters.Quality = nil
	req.ImageGenerationParameters.Style = nil
	req.ImageGenerationParameters.Size = nil
	req.ImageGenerationParameters.OutputCompression = nil
}

func filterOpenAISpecificParameters(req *OpenAIImageGenerationRequest) {
	req.ImageGenerationParameters.Seed = nil
	req.NumInferenceSteps = nil
	req.NegativePrompt = nil
}

// ToBifrostImageGenerationRequest converts an OpenAI image generation request to Bifrost format
func (request *OpenAIImageGenerationRequest) ToBifrostImageGenerationRequest(ctx *schemas.BifrostContext) *schemas.BifrostImageGenerationRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.OpenAI))

	// Only set Params if the embedded struct is non-empty to avoid always emitting empty params
	var params *schemas.ImageGenerationParameters
	if request.N != nil || request.Background != nil || request.Moderation != nil ||
		request.PartialImages != nil || request.Size != nil || request.Quality != nil ||
		request.OutputCompression != nil || request.OutputFormat != nil || request.Style != nil ||
		request.ResponseFormat != nil || request.Seed != nil || request.NegativePrompt != nil ||
		request.NumInferenceSteps != nil || request.User != nil ||
		len(request.ExtraParams) > 0 {
		params = &request.ImageGenerationParameters
	}

	return &schemas.BifrostImageGenerationRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.ImageGenerationInput{
			Prompt: request.Prompt,
		},
		Params:    params,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}
