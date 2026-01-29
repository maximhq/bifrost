package bedrock

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// mapQualityToBedrock maps quality values to Bedrock format:
// - "low" and "medium" -> "standard"
// - "high" -> "premium"
// - "standard" and "premium" (case-insensitive) -> pass through as lowercase ("standard"/"premium")
func mapQualityToBedrock(quality *string) *string {
	if quality == nil {
		return nil
	}

	qualityLower := strings.ToLower(strings.TrimSpace(*quality))

	switch qualityLower {
	case "low", "medium":
		return schemas.Ptr("standard")
	case "high":
		return schemas.Ptr("premium")
	case "standard":
		return schemas.Ptr("standard")
	case "premium":
		return schemas.Ptr("premium")
	default:
		return quality
	}
}

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
			bedrockReq.ImageGenerationConfig.Quality = mapQualityToBedrock(request.Params.Quality)
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

// ToBedrockImageVariationRequest converts a Bifrost image variation request to a Bedrock image variation request
func ToBedrockImageVariationRequest(request *schemas.BifrostImageVariationRequest) (*BedrockImageVariationRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}

	if request.Input == nil || request.Input.Image.Image == nil || len(request.Input.Image.Image) == 0 {
		return nil, fmt.Errorf("request.Input.Image is required")
	}

	bedrockReq := &BedrockImageVariationRequest{
		TaskType: schemas.Ptr(TaskTypeImageVariation),
		ImageVariationParams: &BedrockImageVariationParams{
			Images: []string{},
		},
		ImageGenerationConfig: &ImageGenerationConfig{},
	}

	// Convert all images to base64 strings
	// Primary image from Input.Image
	imageBase64 := base64.StdEncoding.EncodeToString(request.Input.Image.Image)
	bedrockReq.ImageVariationParams.Images = append(bedrockReq.ImageVariationParams.Images, imageBase64)

	// Additional images from ExtraParams (stored as [][]byte)
	if request.Params != nil && request.Params.ExtraParams != nil {
		if additionalImages, ok := request.Params.ExtraParams["images"]; ok {
			// Handle array of byte arrays (stored by HTTP handler)
			if imagesArray, ok := additionalImages.([][]byte); ok {
				for _, imgBytes := range imagesArray {
					if len(imgBytes) > 0 {
						additionalBase64 := base64.StdEncoding.EncodeToString(imgBytes)
						bedrockReq.ImageVariationParams.Images = append(bedrockReq.ImageVariationParams.Images, additionalBase64)
					}
				}
			}
		}

		// Extract optional fields from ExtraParams
		if prompt, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["prompt"]); ok {
			bedrockReq.ImageVariationParams.Text = prompt
		}
		if negativeText, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["negativeText"]); ok {
			bedrockReq.ImageVariationParams.NegativeText = negativeText
		}

		if similarityStrength, ok := schemas.SafeExtractFloat64Pointer(request.Params.ExtraParams["similarityStrength"]); ok {
			// Validate similarityStrength range (0.2 to 1.0)
			if *similarityStrength < 0.2 || *similarityStrength > 1.0 {
				return nil, fmt.Errorf("similarityStrength must be between 0.2 and 1.0, got %f", *similarityStrength)
			}
			bedrockReq.ImageVariationParams.SimilarityStrength = similarityStrength
		}
	}

	// Map standard params to ImageGenerationConfig
	if request.Params != nil {
		if request.Params.N != nil {
			bedrockReq.ImageGenerationConfig.NumberOfImages = request.Params.N
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

		// Extract quality and cfgScale from ExtraParams
		if request.Params.ExtraParams != nil {
			if quality, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["quality"]); ok {
				bedrockReq.ImageGenerationConfig.Quality = mapQualityToBedrock(quality)
			}

			if cfgScale, ok := schemas.SafeExtractFloat64Pointer(request.Params.ExtraParams["cfgScale"]); ok {
				bedrockReq.ImageGenerationConfig.CfgScale = cfgScale
			}
		}
	}

	return bedrockReq, nil
}

// ToBedrockImageEditRequest converts a Bifrost image edit request to a Bedrock image edit request
func ToBedrockImageEditRequest(request *schemas.BifrostImageEditRequest) (*BedrockImageEditRequest, error) {
	// Validate request
	if request == nil || request.Input == nil {
		return nil, fmt.Errorf("request or input is nil")
	}

	if len(request.Input.Images) == 0 || len(request.Input.Images[0].Image) == 0 {
		return nil, fmt.Errorf("at least one image is required")
	}

	// Validate and extract type (required)
	if request.Params == nil || request.Params.Type == nil {
		return nil, fmt.Errorf("type field is required (must be inpainting, outpainting, or background_removal)")
	}

	editType := strings.ToLower(*request.Params.Type)

	// Convert first image to base64
	imageBase64 := base64.StdEncoding.EncodeToString(request.Input.Images[0].Image)

	bedrockReq := &BedrockImageEditRequest{}

	switch editType {
	case "inpainting":
		bedrockReq.TaskType = schemas.Ptr(TaskTypeInpainting)
		bedrockReq.InPaintingParams = buildInPaintingParams(imageBase64, request)
		bedrockReq.ImageGenerationConfig = buildImageGenerationConfig(request.Params)

	case "outpainting":
		bedrockReq.TaskType = schemas.Ptr(TaskTypeOutpainting)
		bedrockReq.OutPaintingParams = buildOutPaintingParams(imageBase64, request)
		bedrockReq.ImageGenerationConfig = buildImageGenerationConfig(request.Params)

	case "background_removal":
		bedrockReq.TaskType = schemas.Ptr(TaskTypeBackgroundRemoval)
		bedrockReq.BackgroundRemovalParams = &BedrockBackgroundRemovalParams{
			Image: imageBase64,
		}

	default:
		return nil, fmt.Errorf("unsupported type for Bedrock: %s (must be inpainting, outpainting, or background_removal)", editType)
	}

	return bedrockReq, nil
}

// Helper functions
func buildInPaintingParams(imageBase64 string, request *schemas.BifrostImageEditRequest) *BedrockInPaintingParams {
	params := &BedrockInPaintingParams{
		Image: imageBase64,
		Text:  request.Input.Prompt,
	}

	if request.Params.NegativePrompt != nil {
		params.NegativeText = request.Params.NegativePrompt
	}

	if request.Params.ExtraParams != nil {
		if maskPrompt, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["mask_prompt"]); ok {
			params.MaskPrompt = maskPrompt
		}
		if returnMask, ok := schemas.SafeExtractBoolPointer(request.Params.ExtraParams["return_mask"]); ok {
			params.ReturnMask = returnMask
		}
	}

	// Convert mask to base64 if present
	if len(request.Params.Mask) > 0 {
		maskBase64 := base64.StdEncoding.EncodeToString(request.Params.Mask)
		params.MaskImage = &maskBase64
	}

	return params
}

func buildOutPaintingParams(imageBase64 string, request *schemas.BifrostImageEditRequest) *BedrockOutPaintingParams {
	params := &BedrockOutPaintingParams{
		Text:  request.Input.Prompt,
		Image: imageBase64,
	}

	if request.Params.NegativePrompt != nil {
		params.NegativeText = request.Params.NegativePrompt
	}

	if request.Params.ExtraParams != nil {
		if maskPrompt, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["mask_prompt"]); ok {
			params.MaskPrompt = maskPrompt
		}
		if returnMask, ok := schemas.SafeExtractBoolPointer(request.Params.ExtraParams["return_mask"]); ok {
			params.ReturnMask = returnMask
		}
		if outPaintingMode, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["outpainting_mode"]); ok {
			// Validate mode
			mode := strings.ToUpper(*outPaintingMode)
			if mode == "DEFAULT" || mode == "PRECISE" {
				params.OutPaintingMode = &mode
			}
		}
	}

	// Convert mask to base64 if present
	if len(request.Params.Mask) > 0 {
		maskBase64 := base64.StdEncoding.EncodeToString(request.Params.Mask)
		params.MaskImage = &maskBase64
	}

	return params
}

func buildImageGenerationConfig(params *schemas.ImageEditParameters) *ImageGenerationConfig {
	config := &ImageGenerationConfig{}

	if params.N != nil {
		config.NumberOfImages = params.N
	}

	// Parse size (reuse logic from image generation)
	if params.Size != nil && strings.TrimSpace(strings.ToLower(*params.Size)) != "auto" {
		size := strings.Split(strings.TrimSpace(strings.ToLower(*params.Size)), "x")
		if len(size) == 2 {
			width, err := strconv.Atoi(size[0])
			if err == nil {
				height, err := strconv.Atoi(size[1])
				if err == nil {
					config.Width = schemas.Ptr(width)
					config.Height = schemas.Ptr(height)
				}
			}
		}
	}

	if params.Quality != nil {
		config.Quality = mapQualityToBedrock(params.Quality)
	}

	if params.Seed != nil {
		config.Seed = params.Seed
	}

	if params.ExtraParams != nil {
		if cfgScale, ok := schemas.SafeExtractFloat64Pointer(params.ExtraParams["cfgScale"]); ok {
			config.CfgScale = cfgScale
		}
	}

	return config
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
