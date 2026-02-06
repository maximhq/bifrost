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

		if bifrostReq.Params.Size != nil && strings.TrimSpace(strings.ToLower(*bifrostReq.Params.Size)) != "auto" {
			size := strings.Split(strings.TrimSpace(strings.ToLower(*bifrostReq.Params.Size)), "x")
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
			req.ExtraParams = bifrostReq.Params.ExtraParams
			// Map guidance_scale
			if v, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["guidance_scale"]); ok {
				delete(req.ExtraParams, "guidance_scale")
				req.GuidanceScale = v
			}

			// Map loras in array format [{"url": "...", "scale": ...}]
			if lorasValue, exists := bifrostReq.Params.ExtraParams["loras"]; exists && lorasValue != nil {
				delete(req.ExtraParams, "loras")
				// Check if lorasValue is an array of maps
				if lorasArray, ok := lorasValue.([]interface{}); ok {
					for _, item := range lorasArray {
						if loraMap, ok := item.(map[string]interface{}); ok {
							if url, ok := schemas.SafeExtractString(loraMap["url"]); ok {
								if scale, ok := schemas.SafeExtractInt(loraMap["scale"]); ok {
									req.Loras = append(req.Loras, NebiusLora{URL: url, Scale: scale})
								}
							}
						}
					}
				}
			}
		}
	}
	return req, nil
}

// ToBifrostImageResponse converts a nebius image generation response to bifrost format.
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
