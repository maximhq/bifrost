package azure

import (
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToAzureImageRequest converts a Bifrost Image Request to Azure format
func ToAzureImageRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *AzureImageRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	req := &AzureImageRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}

	mapImageParams(bifrostReq.Params, req)
	return req
}

// This function maps Image generation parameters from a Bifrost Request to Azure format
func mapImageParams(p *schemas.ImageGenerationParameters, req *AzureImageRequest) {
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

// ToBifrostImageResponse converts an Azure Image Response to Bifrost format
func ToBifrostImageResponse(azureResponse *AzureImageResponse, requestModel string, latency time.Duration) *schemas.BifrostImageGenerationResponse {
	if azureResponse == nil {
		return nil
	}

	data := make([]schemas.ImageData, len(azureResponse.Data))
	for i, img := range azureResponse.Data {
		data[i] = schemas.ImageData{
			URL:           img.URL,
			B64JSON:       img.B64JSON,
			RevisedPrompt: img.RevisedPrompt,
			Index:         i,
		}
	}

	var usage *schemas.ImageUsage
	if azureResponse.Usage != nil {
		usage = &schemas.ImageUsage{
			PromptTokens: azureResponse.Usage.InputTokens,
			TotalTokens:  azureResponse.Usage.TotalTokens,
		}
	}

	return &schemas.BifrostImageGenerationResponse{
		ID:      uuid.NewString(),
		Created: azureResponse.Created,
		Model:   requestModel,
		Data:    data,
		Usage:   usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Azure,
			Latency:  latency.Milliseconds(),
		},
	}
}
