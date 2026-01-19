package bedrock

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBedrockImageGenerationRequest converts a Bifrost image generation request to a Bedrock image generation request
func ToBedrockImageGenerationRequest(request *schemas.BifrostImageGenerationRequest) (*BedrockImageGenerationRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}

	if request.Input == nil {
		return nil, fmt.Errorf("request.Input is required")
	}

	bedrockReq := &BedrockImageGenerationRequest{
		TaskType: schemas.Ptr(TaskTypeTextImage),
		TextToImageParams: &BedrockTextToImageParams{
			Text: request.Input.Prompt,
		},
		ImageGenerationConfig: &ImageGenerationConfig{},
	}

	if request.Params != nil {
		if request.Params.N != nil {
			bedrockReq.ImageGenerationConfig.NumberOfImages = request.Params.N
		}
		if request.Params.NegativePrompt != nil {
			bedrockReq.TextToImageParams.NegativeText = request.Params.NegativePrompt
		}
		if request.Params.Seed != nil {
			bedrockReq.ImageGenerationConfig.Seed = request.Params.Seed
		}
		if request.Params.Quality != nil {
			bedrockReq.ImageGenerationConfig.Quality = request.Params.Quality
		}
		if request.Params.Style != nil {
			bedrockReq.TextToImageParams.Style = request.Params.Style
		}
		if request.Params.Size != nil && strings.TrimSpace(strings.ToLower(*request.Params.Size)) != "auto" {

			size := strings.Split(strings.TrimSpace(strings.ToLower(*request.Params.Size)), "x")
			if len(size) != 2 {
				return nil, fmt.Errorf("invalid size format: expected 'WIDTHxHEIGHT', got %q", *request.Params.Size)
			}

			width, err := strconv.Atoi(size[0])
			if err != nil {
				return nil, fmt.Errorf("invalid width in size %q: %w", *request.Params.Size, err)
			}

			height, err := strconv.Atoi(size[1])
			if err != nil {
				return nil, fmt.Errorf("invalid height in size %q: %w", *request.Params.Size, err)
			}

			bedrockReq.ImageGenerationConfig.Width = schemas.Ptr(width)
			bedrockReq.ImageGenerationConfig.Height = schemas.Ptr(height)
		}
		if request.Params.ExtraParams != nil {
			if cfgScale, ok := schemas.SafeExtractFloat64Pointer(request.Params.ExtraParams["cfgScale"]); ok {
				bedrockReq.ImageGenerationConfig.CfgScale = cfgScale
			}
		}
	}

	return bedrockReq, nil

}

// ToBifrostImageGenerationResponse converts a Bedrock image generation response to a Bifrost image generation response
func ToBifrostImageGenerationResponse(response *BedrockImageGenerationResponse) *schemas.BifrostImageGenerationResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostImageGenerationResponse{}

	for index, image := range response.Images {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.ImageData{
			B64JSON: image,
			Index:   index,
		})
	}

	return bifrostResponse
}

// DetermineImageGenModelType determines the image generation model type from the model name
func DetermineImageGenModelType(model string) string {
	if strings.Contains(model, "nova-canvas-v1:0") {
		return "nova-canvas-v1:0"
	} else if strings.Contains(model, "titan-image-generator-v2:0") {
		return "titan-image-generator-v2:0"
	} else if strings.Contains(model, "titan-image-generator-v1") {
		return "titan-image-generator-v1"
	}
	return model
}
