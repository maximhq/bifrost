package openai

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	ImageGenerationPartial   ImageGenerationEventType = "image_generation.partial_image"
	ImageGenerationCompleted ImageGenerationEventType = "image_generation.completed"

	// ImageGenerationChunkSize is the size of base64 chunks when splitting large image data
	ImageGenerationChunkSize = 128 * 1024
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

// ToBifrostImageResponse converts an OpenAI Image Response to Bifrost format
func ToBifrostImageResponse(openaiResponse *OpenAIImageGenerationResponse, requestModel string, latency time.Duration) *schemas.BifrostImageGenerationResponse {
	if openaiResponse == nil {
		return nil
	}

	data := make([]schemas.ImageData, len(openaiResponse.Data))
	for i, img := range openaiResponse.Data {
		data[i] = schemas.ImageData{
			URL:           img.URL,
			B64JSON:       img.B64JSON,
			RevisedPrompt: img.RevisedPrompt,
			Index:         i,
		}
	}

	var usage *schemas.ImageUsage
	if openaiResponse.Usage != nil {
		usage = openaiResponse.Usage
	}

	return &schemas.BifrostImageGenerationResponse{
		Created: openaiResponse.Created,
		Model:   requestModel,
		Data:    data,
		Usage:   usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.OpenAI,
			Latency:  latency.Milliseconds(),
		},
	}
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
