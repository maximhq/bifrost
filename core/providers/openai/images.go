package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	ImageGenerationPartial   ImageGenerationEventType = "image_generation.partial_image"
	ImageGenerationCompleted ImageGenerationEventType = "image_generation.completed"
)

// ToOpenAIImageGenerationRequest converts a Bifrost Image Request to OpenAI format
func ToOpenAIImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *OpenAIImageGenerationRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	req := &OpenAIImageGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}

	if bifrostReq.Params != nil {
		req.ImageGenerationParameters = *bifrostReq.Params
	}
	return req
}

// ToBifrostImageGenerationRequest converts an OpenAI image generation request to Bifrost format
func (request *OpenAIImageGenerationRequest) ToBifrostImageGenerationRequest() *schemas.BifrostImageGenerationRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

	return &schemas.BifrostImageGenerationRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.ImageGenerationInput{
			Prompt: request.Prompt,
		},
		Params:    &request.ImageGenerationParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}
