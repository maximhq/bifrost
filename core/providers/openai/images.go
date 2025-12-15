package openai

import (
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	ImageGenerationPartial   ImageGenerationEventType = "image_generation.partial_image"
	ImageGenerationCompleted ImageGenerationEventType = "image_generation.completed"

	// ImageGenerationChunkSize is the size of base64 chunks when splitting large image data
	ImageGenerationChunkSize = 128 * 1024
)

var StreamingEnabledImageModels = map[string]bool{
	"gpt-image-1": true,
	"dall-e-2":    false,
	"dall-e-3":    false,
}

// ToOpenAIImageGenerationRequest converts a Bifrost Image Request to OpenAI format
func ToOpenAIImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *OpenAIImageGenerationRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	req := &OpenAIImageGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}

	mapImageParams(bifrostReq.Params, req)
	return req
}

// This function maps Image generation parameters from a Bifrost Request to OpenAI format
func mapImageParams(p *schemas.ImageGenerationParameters, req *OpenAIImageGenerationRequest) {
	if p == nil {
		return
	}
	req.N = p.N
	req.Size = p.Size
	req.Quality = p.Quality
	req.Style = p.Style
	req.ResponseFormat = p.ResponseFormat
	req.User = p.User
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
		usage = &schemas.ImageUsage{
			PromptTokens: openaiResponse.Usage.InputTokens,
			TotalTokens:  openaiResponse.Usage.TotalTokens,
		}
	}

	return &schemas.BifrostImageGenerationResponse{
		ID:      uuid.NewString(),
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
