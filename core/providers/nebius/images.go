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

		// Handle nebius inconsistency
		if req.ResponseExtension != nil && *req.ResponseExtension == "jpeg" {
			*req.ResponseExtension = "jpg"
		}
		if bifrostReq.Params.ExtraParams != nil {
			// Map num_inference_steps
			if v, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_inference_steps"]); ok {
				req.NumInferenceSteps = v
			}

			// Map seed
			if v, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["seed"]); ok {
				req.Seed = v
			}

			// Map guidance_scale
			if v, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["guidance_scale"]); ok {
				req.GuidanceScale = v
			}

			// Map negative_prompt
			if v, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["negative_prompt"]); ok {
				req.NegativePrompt = &v
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
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Nebius,
		},
	}
}
