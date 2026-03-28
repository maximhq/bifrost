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

// isStabilityAIModel returns true if the model is a Stability AI model (contains "stability.")
func isStabilityAIModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "stability.")
}

// ToStabilityAIImageGenerationRequest converts a Bifrost image generation request to the Stability AI
// flat request format used by Bedrock (stability.stable-image-* models).
func ToStabilityAIImageGenerationRequest(request *schemas.BifrostImageGenerationRequest) (*StabilityAIImageGenerationRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}
	if request.Input == nil {
		return nil, fmt.Errorf("request input is required")
	}

	req := &StabilityAIImageGenerationRequest{
		Prompt: request.Input.Prompt,
	}

	if request.Params != nil {
		if request.Params.AspectRatio != nil {
			req.AspectRatio = request.Params.AspectRatio
		}
		if request.Params.OutputFormat != nil {
			req.OutputFormat = request.Params.OutputFormat
		}
		if request.Params.Seed != nil {
			req.Seed = request.Params.Seed
		}
		if request.Params.NegativePrompt != nil {
			req.NegativePrompt = request.Params.NegativePrompt
		}
		if request.Params.ExtraParams != nil {
			// aspect_ratio may also arrive via ExtraParams if not in knownFields; skip if already set
			if req.AspectRatio == nil {
				if ar, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["aspect_ratio"]); ok {
					delete(request.Params.ExtraParams, "aspect_ratio")
					req.AspectRatio = ar
				}
			}
			req.ExtraParams = request.Params.ExtraParams
		}
	}

	return req, nil
}

// ToBedrockImageGenerationRequest converts a Bifrost image generation request to a Bedrock image generation request
func ToBedrockImageGenerationRequest(request *schemas.BifrostImageGenerationRequest) (*BedrockImageGenerationRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}

	if request.Input == nil {
		return nil, fmt.Errorf("request input is required")
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
				delete(request.Params.ExtraParams, "cfgScale")
				bedrockReq.ImageGenerationConfig.CfgScale = cfgScale
			}
			bedrockReq.ExtraParams = request.Params.ExtraParams
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
			delete(request.Params.ExtraParams, "images")
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
			delete(request.Params.ExtraParams, "prompt")
			bedrockReq.ImageVariationParams.Text = prompt
		}
		if negativeText, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["negativeText"]); ok {
			delete(request.Params.ExtraParams, "negativeText")
			bedrockReq.ImageVariationParams.NegativeText = negativeText
		}

		if similarityStrength, ok := schemas.SafeExtractFloat64Pointer(request.Params.ExtraParams["similarityStrength"]); ok {
			delete(request.Params.ExtraParams, "similarityStrength")
			// Validate similarityStrength range (0.2 to 1.0)
			if *similarityStrength < 0.2 || *similarityStrength > 1.0 {
				return nil, fmt.Errorf("similarityStrength must be between 0.2 and 1.0, got %f", *similarityStrength)
			}
			bedrockReq.ImageVariationParams.SimilarityStrength = similarityStrength
		}
		bedrockReq.ExtraParams = request.Params.ExtraParams
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

	bedrockReq.ExtraParams = request.Params.ExtraParams
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
			delete(request.Params.ExtraParams, "mask_prompt")
			params.MaskPrompt = maskPrompt
		}
		if returnMask, ok := schemas.SafeExtractBoolPointer(request.Params.ExtraParams["return_mask"]); ok {
			delete(request.Params.ExtraParams, "return_mask")
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
			delete(request.Params.ExtraParams, "mask_prompt")
			params.MaskPrompt = maskPrompt
		}
		if returnMask, ok := schemas.SafeExtractBoolPointer(request.Params.ExtraParams["return_mask"]); ok {
			delete(request.Params.ExtraParams, "return_mask")
			params.ReturnMask = returnMask
		}
		if outPaintingMode, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["outpainting_mode"]); ok {
			// Validate mode
			mode := strings.ToUpper(*outPaintingMode)
			if mode == "DEFAULT" || mode == "PRECISE" {
				delete(request.Params.ExtraParams, "outpainting_mode")
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
			delete(params.ExtraParams, "cfgScale")
			config.CfgScale = cfgScale
		}
	}

	return config
}

// getStabilityAITaskTypeFromParams maps the generic BifrostImageEditParameters.Type value
// to a Stability AI task type string. Returns "" if the value is not a recognized Stability AI task type.
func getStabilityAITaskTypeFromParams(t string) string {
	switch strings.ToLower(t) {
	case "inpainting", "inpaint":
		return "inpaint"
	case "outpainting", "outpaint":
		return "outpaint"
	case "background_removal", "remove_background":
		return "remove-bg"
	case "erase_object":
		return "erase-object"
	case "upscale_fast":
		return "upscale-fast"
	case "upscale_creative":
		return "upscale-creative"
	case "upscale_conservative":
		return "upscale-conservative"
	case "recolor":
		return "recolor"
	case "search_replace":
		return "search-replace"
	case "control_sketch":
		return "control-sketch"
	case "control_structure":
		return "control-structure"
	case "style_guide":
		return "style-guide"
	case "style_transfer":
		return "style-transfer"
	default:
		return ""
	}
}

// getStabilityAIEditTaskType infers the Stability AI edit task from the model name.
// Returns an error if the model name does not match any known pattern.
func getStabilityAIEditTaskType(model string) (string, error) {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "stable-creative-upscale"):
		return "upscale-creative", nil
	case strings.Contains(m, "stable-conservative-upscale"):
		return "upscale-conservative", nil
	case strings.Contains(m, "stable-fast-upscale"):
		return "upscale-fast", nil
	case strings.Contains(m, "stable-image-inpaint"):
		return "inpaint", nil
	case strings.Contains(m, "stable-outpaint"):
		return "outpaint", nil
	case strings.Contains(m, "stable-image-search-recolor"):
		return "recolor", nil
	case strings.Contains(m, "stable-image-search-replace"):
		return "search-replace", nil
	case strings.Contains(m, "stable-image-erase-object"):
		return "erase-object", nil
	case strings.Contains(m, "stable-image-remove-background"):
		return "remove-bg", nil
	case strings.Contains(m, "stable-image-control-sketch"):
		return "control-sketch", nil
	case strings.Contains(m, "stable-image-control-structure"):
		return "control-structure", nil
	case strings.Contains(m, "stable-image-style-guide"):
		return "style-guide", nil
	case strings.Contains(m, "stable-style-transfer"):
		return "style-transfer", nil
	default:
		return "", fmt.Errorf("cannot determine task type from stability ai model name %q", model)
	}
}

// ToStabilityAIImageEditRequest converts a Bifrost image edit request to the Stability AI flat request
// format used by Bedrock edit models. Only fields valid for the detected task type are populated.
// deployment is the resolved model identifier (after applying any deployment alias mapping); it is
// used for task-type inference so that alias-mapped models route correctly.
func ToStabilityAIImageEditRequest(request *schemas.BifrostImageEditRequest, deployment string) (*StabilityAIImageEditRequest, error) {
	if request == nil || request.Input == nil {
		return nil, fmt.Errorf("request or input is nil")
	}

	var taskType string
	if request.Params != nil && request.Params.Type != nil {
		taskType = getStabilityAITaskTypeFromParams(*request.Params.Type)
	}
	if taskType == "" {
		var err error
		taskType, err = getStabilityAIEditTaskType(deployment)
		if err != nil {
			return nil, err
		}
	}

	req := &StabilityAIImageEditRequest{}

	// Image sourcing
	if taskType == "style-transfer" {
		if len(request.Input.Images) != 2 {
			return nil, fmt.Errorf("style-transfer requires exactly two images: init_image and style_image")
		}
		if len(request.Input.Images[0].Image) == 0 || len(request.Input.Images[1].Image) == 0 {
			return nil, fmt.Errorf("style-transfer requires non-empty init_image and style_image")
		}
		initB64 := base64.StdEncoding.EncodeToString(request.Input.Images[0].Image)
		styleB64 := base64.StdEncoding.EncodeToString(request.Input.Images[1].Image)
		req.InitImage = &initB64
		req.StyleImage = &styleB64
	} else {
		if len(request.Input.Images) == 0 || len(request.Input.Images[0].Image) == 0 {
			return nil, fmt.Errorf("at least one image is required")
		}
		imageB64 := base64.StdEncoding.EncodeToString(request.Input.Images[0].Image)
		req.Image = &imageB64
	}

	// Common fields populated based on task allowlist
	prompt := request.Input.Prompt
	switch taskType {
	case "inpaint", "recolor", "search-replace", "control-sketch", "control-structure",
		"style-guide", "upscale-creative", "upscale-conservative", "outpaint", "style-transfer":
		req.Prompt = &prompt
	}

	// Negative prompt
	if request.Params != nil && request.Params.NegativePrompt != nil {
		switch taskType {
		case "inpaint", "outpaint", "recolor", "search-replace", "control-sketch",
			"control-structure", "style-guide", "upscale-creative", "upscale-conservative", "style-transfer":
			req.NegativePrompt = request.Params.NegativePrompt
		}
	}

	// Seed
	if request.Params != nil && request.Params.Seed != nil {
		switch taskType {
		case "inpaint", "outpaint", "recolor", "search-replace", "erase-object", "control-sketch",
			"control-structure", "style-guide", "upscale-creative", "upscale-conservative", "style-transfer":
			req.Seed = request.Params.Seed
		}
	}

	// Mask (from Params.Mask bytes)
	if request.Params != nil && len(request.Params.Mask) > 0 {
		switch taskType {
		case "inpaint", "erase-object":
			maskB64 := base64.StdEncoding.EncodeToString(request.Params.Mask)
			req.Mask = &maskB64
		}
	}

	// ExtraParams
	if request.Params != nil {
		// Typed OutputFormat takes priority over ExtraParams
		if request.Params.OutputFormat != nil {
			req.OutputFormat = request.Params.OutputFormat
		}

		if request.Params.ExtraParams != nil {
			ep := make(map[string]interface{}, len(request.Params.ExtraParams))
			for k, v := range request.Params.ExtraParams {
				ep[k] = v
			}

			// output_format — all tasks (fallback if not already set by typed field)
			if req.OutputFormat == nil {
				if v, ok := schemas.SafeExtractStringPointer(ep["output_format"]); ok {
					delete(ep, "output_format")
					req.OutputFormat = v
				}
			}

			// style_preset
			switch taskType {
			case "inpaint", "outpaint", "recolor", "search-replace", "control-sketch",
				"control-structure", "style-guide", "upscale-creative":
				if v, ok := schemas.SafeExtractStringPointer(ep["style_preset"]); ok {
					delete(ep, "style_preset")
					req.StylePreset = v
				}
			}

			// grow_mask
			switch taskType {
			case "inpaint", "recolor", "search-replace", "erase-object":
				if v, ok := schemas.SafeExtractIntPointer(ep["grow_mask"]); ok {
					delete(ep, "grow_mask")
					req.GrowMask = v
				}
			}

			// outpaint directional fields
			if taskType == "outpaint" {
				if v, ok := schemas.SafeExtractIntPointer(ep["left"]); ok {
					delete(ep, "left")
					req.Left = v
				}
				if v, ok := schemas.SafeExtractIntPointer(ep["right"]); ok {
					delete(ep, "right")
					req.Right = v
				}
				if v, ok := schemas.SafeExtractIntPointer(ep["up"]); ok {
					delete(ep, "up")
					req.Up = v
				}
				if v, ok := schemas.SafeExtractIntPointer(ep["down"]); ok {
					delete(ep, "down")
					req.Down = v
				}
			}

			// creativity
			switch taskType {
			case "upscale-creative", "upscale-conservative", "outpaint":
				if v, ok := schemas.SafeExtractFloat64Pointer(ep["creativity"]); ok {
					delete(ep, "creativity")
					req.Creativity = v
				}
			}

			// select_prompt (recolor)
			if taskType == "recolor" {
				if v, ok := schemas.SafeExtractStringPointer(ep["select_prompt"]); ok {
					delete(ep, "select_prompt")
					req.SelectPrompt = v
				}
			}

			// search_prompt (search-replace)
			if taskType == "search-replace" {
				if v, ok := schemas.SafeExtractStringPointer(ep["search_prompt"]); ok {
					delete(ep, "search_prompt")
					req.SearchPrompt = v
				}
			}

			// control_strength
			switch taskType {
			case "control-sketch", "control-structure":
				if v, ok := schemas.SafeExtractFloat64Pointer(ep["control_strength"]); ok {
					delete(ep, "control_strength")
					req.ControlStrength = v
				}
			}

			// style-guide fields
			if taskType == "style-guide" {
				if v, ok := schemas.SafeExtractStringPointer(ep["aspect_ratio"]); ok {
					delete(ep, "aspect_ratio")
					req.AspectRatio = v
				}
				if v, ok := schemas.SafeExtractFloat64Pointer(ep["fidelity"]); ok {
					delete(ep, "fidelity")
					req.Fidelity = v
				}
			}

			// style-transfer fields
			if taskType == "style-transfer" {
				if v, ok := schemas.SafeExtractFloat64Pointer(ep["style_strength"]); ok {
					delete(ep, "style_strength")
					req.StyleStrength = v
				}
				if v, ok := schemas.SafeExtractFloat64Pointer(ep["composition_fidelity"]); ok {
					delete(ep, "composition_fidelity")
					req.CompositionFidelity = v
				}
				if v, ok := schemas.SafeExtractFloat64Pointer(ep["change_strength"]); ok {
					delete(ep, "change_strength")
					req.ChangeStrength = v
				}
			}

			req.ExtraParams = ep
		}
	}

	// Validate required per-task fields
	if taskType == "recolor" && (req.SelectPrompt == nil || *req.SelectPrompt == "") {
		return nil, fmt.Errorf("select_prompt is required for stability ai recolor task")
	}
	if taskType == "search-replace" && (req.SearchPrompt == nil || *req.SearchPrompt == "") {
		return nil, fmt.Errorf("search_prompt is required for stability ai search-replace task")
	}

	return req, nil
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
