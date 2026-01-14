package nebius

import (
	"fmt"
	"strconv"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToNebiusImageGenerationRequest converts a bifrost image generation request to nebius format.
func (provider *NebiusProvider) ToNebiusImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) (*NebiusImageGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, fmt.Errorf("bifrost request is nil or input is nil")
	}

	req := &NebiusImageGenerationRequest{
		Model:  &bifrostReq.Model,
		Prompt: &bifrostReq.Input.Prompt,
	}

	if bifrostReq.Params != nil {

		if bifrostReq.Params.ResponseFormat != nil {
			req.ResponseFormat = bifrostReq.Params.ResponseFormat
		}

		if bifrostReq.Params.Size != nil {
			size := strings.Split(*bifrostReq.Params.Size, "x")
			if len(size) != 2 {
				return nil, fmt.Errorf("invalid size format: expected 'WIDTHxHEIGHT', got %q", *bifrostReq.Params.Size)
			}

			width, err := strconv.Atoi(size[0])
			if err != nil {
				return nil, fmt.Errorf("invalid width in size %q: %w", *bifrostReq.Params.Size, err)
			}

			height, err := strconv.Atoi(size[1])
			if err != nil {
				return nil, fmt.Errorf("invalid height in size %q: %w", *bifrostReq.Params.Size, err)
			}

			req.Width = &width
			req.Height = &height
		}
		if bifrostReq.Params.OutputFormat != nil {
			req.ResponseExtension = bifrostReq.Params.OutputFormat
		}
		if req.ResponseExtension != nil && strings.ToLower(*req.ResponseExtension) == "jpeg" {
			req.ResponseExtension = schemas.Ptr("jpg")
		}
		if bifrostReq.Params.Seed != nil {
			req.Seed = bifrostReq.Params.Seed
		}
		if bifrostReq.Params.NegativePrompt != nil {
			req.NegativePrompt = bifrostReq.Params.NegativePrompt
		}
		if bifrostReq.Params.NumInferenceSteps != nil {
			req.NumInferenceSteps = bifrostReq.Params.NumInferenceSteps
		}
		// Handle extra params
		if bifrostReq.Params.ExtraParams != nil {
			// Map guidance_scale
			if v, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["guidance_scale"]); ok {
				req.GuidanceScale = v
			}
		}
	}
	return req, nil
}

// ToBifrostImageGenerationResponse converts a nebius image generation response to bifrost format.
func ToBifrostImageResponse(nebiusResponse *NebiusImageGenerationResponse) *schemas.BifrostImageGenerationResponse {
	if nebiusResponse == nil {
		return nil
	}

	data := make([]schemas.ImageData, len(nebiusResponse.Data))
	for i, img := range nebiusResponse.Data {
		data[i] = schemas.ImageData{
			URL:           img.URL,
			B64JSON:       img.B64JSON,
			RevisedPrompt: img.RevisedPrompt,
			Index:         i,
		}
	}
	return &schemas.BifrostImageGenerationResponse{
		ID:   nebiusResponse.Id,
		Data: data,
	}
}
