package gemini

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostImageGenerationRequest converts a Gemini generation request to a Bifrost image generation request
func (request *GeminiGenerationRequest) ToBifrostImageGenerationRequest(ctx *schemas.BifrostContext) *schemas.BifrostImageGenerationRequest {
	if request == nil {
		return nil
	}

	// Parse provider from model string (e.g., "openai/gpt-image-1" -> provider="openai", model="gpt-image-1")
	// This allows cross-provider routing through the GenAI endpoint
	provider, model := schemas.ParseModelString(request.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.Gemini))

	bifrostReq := &schemas.BifrostImageGenerationRequest{
		Provider: provider,
		Model:    model,
		Input:    &schemas.ImageGenerationInput{},
		Params:   &schemas.ImageGenerationParameters{},
	}

	fallbacks := schemas.ParseFallbacks(request.Fallbacks)
	bifrostReq.Fallbacks = fallbacks

	// First, try to extract prompt from Imagen format (instances)
	if len(request.Instances) > 0 && request.Instances[0].Prompt != "" {
		bifrostReq.Input.Prompt = request.Instances[0].Prompt

		// Extract Imagen parameters
		if request.Parameters != nil {
			if request.Parameters.SampleCount != nil {
				bifrostReq.Params.N = request.Parameters.SampleCount
			}
			// Convert Imagen size format to standard format
			if request.Parameters.SampleImageSize != nil || request.Parameters.AspectRatio != nil {
				size := convertImagenFormatToSize(request.Parameters.SampleImageSize, request.Parameters.AspectRatio)
				if size != "" && strings.ToLower(size) != "auto" {
					bifrostReq.Params.Size = &size
				}
			}

			// Map additional parameters to ExtraParams if not in Bifrost schema
			if bifrostReq.Params.ExtraParams == nil {
				bifrostReq.Params.ExtraParams = make(map[string]interface{})
			}

			if request.Parameters.PersonGeneration != nil {
				bifrostReq.Params.ExtraParams["personGeneration"] = *request.Parameters.PersonGeneration
			}
			if request.Parameters.Seed != nil {
				bifrostReq.Params.Seed = request.Parameters.Seed
			}
			if request.Parameters.NegativePrompt != nil {
				bifrostReq.Params.NegativePrompt = request.Parameters.NegativePrompt
			}
			if request.Parameters.Language != nil {
				bifrostReq.Params.ExtraParams["language"] = *request.Parameters.Language
			}
			if request.Parameters.EnhancePrompt != nil {
				bifrostReq.Params.ExtraParams["enhancePrompt"] = *request.Parameters.EnhancePrompt
			}
			if request.Parameters.AddWatermark != nil {
				bifrostReq.Params.ExtraParams["addWatermark"] = *request.Parameters.AddWatermark
			}
			if len(request.Parameters.SafetySettings) > 0 {
				bifrostReq.Params.ExtraParams["safetySettings"] = request.Parameters.SafetySettings
			}
		}
		return bifrostReq
	}

	// Fall back to standard Gemini format (contents)
	if len(request.Contents) > 0 {
		for _, content := range request.Contents {
			for _, part := range content.Parts {
				if part != nil && part.Text != "" {
					bifrostReq.Input.Prompt = part.Text
					break
				}
			}
			if bifrostReq.Input.Prompt != "" {
				break
			}
		}
	}

	return bifrostReq
}

// convertImagenFormatToSize converts Imagen sampleImageSize and aspectRatio to standard WxH format
// Supports aspect ratios: "1:1", "3:4", "4:3", "9:16", "16:9" (supported for Imagen models)
func convertImagenFormatToSize(sampleImageSize *string, aspectRatio *string) string {
	// Default size based on imageSize parameter
	baseSize := 1024
	if sampleImageSize != nil {
		normalizedSize := strings.ToLower(strings.TrimSpace(*sampleImageSize))
		switch normalizedSize {
		case "1k":
			baseSize = 1024
		case "2k":
			baseSize = 2048
		}
	}

	// Apply aspect ratio
	if aspectRatio != nil {
		switch strings.TrimSpace(*aspectRatio) {
		case "1:1":
			return strconv.Itoa(baseSize) + "x" + strconv.Itoa(baseSize)
		case "3:4":
			return strconv.Itoa(baseSize*3/4) + "x" + strconv.Itoa(baseSize)
		case "4:3":
			return strconv.Itoa(baseSize) + "x" + strconv.Itoa(baseSize*3/4)
		case "9:16":
			return strconv.Itoa(baseSize*9/16) + "x" + strconv.Itoa(baseSize)
		case "16:9":
			return strconv.Itoa(baseSize) + "x" + strconv.Itoa(baseSize*9/16)
		}
	}

	// Default to square
	return strconv.Itoa(baseSize) + "x" + strconv.Itoa(baseSize)
}

func (response *GenerateContentResponse) ToBifrostImageGenerationResponse() (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	bifrostResp := &schemas.BifrostImageGenerationResponse{
		ID:    response.ResponseID,
		Model: response.ModelVersion,
		Data:  []schemas.ImageData{},
	}

	// Extract usage metadata
	inputTokens, outputTokens, totalTokens, _, _ := response.extractUsageMetadata()

	// Process candidates to extract image data
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			var imageData []schemas.ImageData
			var imageMetadata []schemas.ImageGenerationResponseParameters

			// Extract image data from all parts
			for idx, part := range candidate.Content.Parts {
				// Check that part is not nil before accessing its fields
				if part != nil && part.InlineData != nil {
					imageData = append(imageData, schemas.ImageData{
						B64JSON: part.InlineData.Data,
						Index:   idx,
					})
					// Convert MIME type to file extension for OutputFormat
					outputFormat := convertMimeTypeToExtension(part.InlineData.MIMEType)
					imageMetadata = append(imageMetadata, schemas.ImageGenerationResponseParameters{
						OutputFormat: outputFormat,
					})
				}
			}

			// Set usage information
			bifrostResp.Usage = &schemas.ImageUsage{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				TotalTokens:  totalTokens,
			}
			// Only assign imageData when it has elements
			if len(imageData) > 0 {
				bifrostResp.Data = imageData
				// Only set ImageGenerationResponseParameters when metadata exists
				if len(imageMetadata) > 0 {
					bifrostResp.ImageGenerationResponseParameters = &imageMetadata[0]
				}
			}
		} else {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: candidate.FinishMessage,
					Code:    schemas.Ptr(string(candidate.FinishReason)),
				},
			}
		}
	} else {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "No candidates found in response",
			},
		}
	}

	return bifrostResp, nil
}

func ToGeminiImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Set response modalities to indicate this is an image generation request
	geminiReq.GenerationConfig.ResponseModalities = []Modality{ModalityImage}

	// Convert parameters to generation config
	if bifrostReq.Params != nil {

		// Handle extra parameters
		if bifrostReq.Params.ExtraParams != nil {
			// Safety settings - support both camelCase (canonical) and snake_case (legacy) keys
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safetySettings"); ok {
				if settings, ok := SafeExtractSafetySettings(safetySettings); ok {
					geminiReq.SafetySettings = settings
				}
			} else if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
				if settings, ok := SafeExtractSafetySettings(safetySettings); ok {
					geminiReq.SafetySettings = settings
				}
			}

			// Cached content - support both camelCase (canonical) and snake_case (legacy) keys
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cachedContent"]); ok {
				geminiReq.CachedContent = cachedContent
			} else if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				geminiReq.CachedContent = cachedContent
			}

			// Labels
			if labels, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "labels"); ok {
				switch m := labels.(type) {
				case map[string]string:
					geminiReq.Labels = m
				case map[string]interface{}:
					out := make(map[string]string, len(m))
					for k, v := range m {
						if s, ok := schemas.SafeExtractString(v); ok {
							out[k] = s
						}
					}
					if len(out) > 0 {
						geminiReq.Labels = out
					}
				}
			}
		}
	}

	if bifrostReq.Input == nil {
		return nil
	}

	// Create parts for image gen request
	parts := []*Part{
		{
			Text: bifrostReq.Input.Prompt,
		},
	}

	geminiReq.Contents = []Content{
		{
			Role:  RoleUser,
			Parts: parts,
		},
	}

	// Note: Gemini image generation always returns a single image, so we do not propagate
	// bifrostReq.Params.N to GenerationConfig.CandidateCount. The N parameter is silently dropped.

	return geminiReq
}

// ToImagenImageGenerationRequest converts a Bifrost Image Request to Imagen format
func ToImagenImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *GeminiImagenRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	// Create instances array with prompt
	prompt := bifrostReq.Input.Prompt
	instances := []ImagenInstance{
		{
			Prompt: prompt,
		},
	}

	req := &GeminiImagenRequest{
		Instances:  instances,
		Parameters: GeminiImagenParameters{},
	}

	if bifrostReq.Params != nil {
		if bifrostReq.Params.N != nil {
			req.Parameters.SampleCount = bifrostReq.Params.N
		}

		// Handle size conversion
		if bifrostReq.Params.Size != nil && strings.ToLower(*bifrostReq.Params.Size) != "auto" {
			imageSize, aspectRatio := convertSizeToImagenFormat(*bifrostReq.Params.Size)
			if imageSize != "" {
				req.Parameters.SampleImageSize = &imageSize
			}
			if aspectRatio != "" {
				req.Parameters.AspectRatio = &aspectRatio
			}
		}

		// Handle output format conversion to mimeType
		outputFormat := ""
		if bifrostReq.Params.OutputFormat != nil {
			outputFormat = *bifrostReq.Params.OutputFormat
		}

		if outputFormat != "" {
			mimeType := convertOutputFormatToMimeType(outputFormat)
			if mimeType != "" {
				if req.Parameters.OutputOptions == nil {
					req.Parameters.OutputOptions = &ImagenOutputOptions{}
				}
				req.Parameters.OutputOptions.MimeType = &mimeType
			}
		}

		if bifrostReq.Params.Seed != nil {
			req.Parameters.Seed = bifrostReq.Params.Seed
		}
		if bifrostReq.Params.NegativePrompt != nil {
			req.Parameters.NegativePrompt = bifrostReq.Params.NegativePrompt
		}

		// Handle extra parameters for Imagen-specific fields
		if bifrostReq.Params.ExtraParams != nil {
			if addWatermark, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["addWatermark"]); ok {
				req.Parameters.AddWatermark = addWatermark
			}
			if sampleImageSize, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["sampleImageSize"]); ok {
				req.Parameters.SampleImageSize = &sampleImageSize
			}

			if aspectRatio, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["aspectRatio"]); ok {
				req.Parameters.AspectRatio = &aspectRatio
			}

			if personGeneration, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["personGeneration"]); ok {
				req.Parameters.PersonGeneration = &personGeneration
			}

			// Map language from ExtraParams
			if language, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["language"]); ok {
				req.Parameters.Language = &language
			}

			// Map enhancePrompt from ExtraParams
			if enhancePrompt, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["enhancePrompt"]); ok {
				req.Parameters.EnhancePrompt = enhancePrompt
			}

			// Map safetySettings from ExtraParams
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safetySettings"); ok {
				if settings, ok := SafeExtractSafetySettings(safetySettings); ok {
					req.Parameters.SafetySettings = settings
				}
			}
		}
	}

	return req
}

// convertMimeTypeToExtension converts MIME type to file extension
// Maps "image/png" -> "png", "image/jpeg" -> "jpeg", "image/webp" -> "webp"
// For unknown MIME types, extracts the subtype after '/' as fallback
func convertMimeTypeToExtension(mimeType string) string {
	if mimeType == "" {
		return ""
	}
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if semi := strings.Index(mimeType, ";"); semi != -1 {
		mimeType = strings.TrimSpace(mimeType[:semi])
	}
	switch mimeType {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		// Extract subtype after '/' if present, otherwise return empty string
		if idx := strings.Index(mimeType, "/"); idx != -1 && idx+1 < len(mimeType) {
			return mimeType[idx+1:]
		}
		return ""
	}
}

// convertOutputFormatToMimeType converts Bifrost output_format to Imagen mimeType
// Maps "png" -> "image/png", "jpg"/"jpeg" -> "image/jpeg", "webp" -> "image/webp"
// Returns empty string for unsupported formats
func convertOutputFormatToMimeType(outputFormat string) string {
	format := strings.ToLower(strings.TrimSpace(outputFormat))
	switch format {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

// convertSizeToImagenFormat converts standard size format (e.g., "1024x1024") to Imagen format
// Returns (imageSize, aspectRatio) where imageSize is "1k", "2k" and aspectRatio is one of:
// "1:1", "3:4", "4:3", "9:16", or "16:9"
func convertSizeToImagenFormat(size string) (string, string) {
	// Parse size string (format: "WIDTHxHEIGHT")
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return "", ""
	}

	width, err1 := strconv.Atoi(parts[0])
	height, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return "", ""
	}

	// Validate width and height are positive integers
	if width <= 0 || height <= 0 {
		return "", ""
	}

	var imageSize string
	if width <= 1024 && height <= 1024 {
		imageSize = "1k"
	} else if width <= 2048 && height <= 2048 {
		imageSize = "2k"
	}

	// Calculate aspect ratio
	var aspectRatio string
	ratio := float64(width) / float64(height)

	// Common aspect ratios with tolerance
	if ratio >= 0.99 && ratio <= 1.01 {
		aspectRatio = "1:1"
	} else if ratio >= 0.74 && ratio <= 0.76 {
		aspectRatio = "3:4"
	} else if ratio >= 1.32 && ratio <= 1.34 {
		aspectRatio = "4:3"
	} else if ratio >= 0.56 && ratio <= 0.57 {
		aspectRatio = "9:16"
	} else if ratio >= 1.77 && ratio <= 1.78 {
		aspectRatio = "16:9"
	}

	return imageSize, aspectRatio
}

// ToBifrostImageGenerationResponse converts an Imagen response to Bifrost format
func (response *GeminiImagenResponse) ToBifrostImageGenerationResponse() *schemas.BifrostImageGenerationResponse {
	if response == nil {
		return nil
	}

	bifrostResp := &schemas.BifrostImageGenerationResponse{
		Data: make([]schemas.ImageData, len(response.Predictions)),
	}

	// Convert each prediction to ImageData
	for i, prediction := range response.Predictions {
		bifrostResp.Data[i] = schemas.ImageData{
			B64JSON: prediction.BytesBase64Encoded,
			Index:   i,
		}

		// Set output format from MIME type if available
		if prediction.MimeType != "" && i == 0 {
			// Convert MIME type to file extension for OutputFormat
			outputFormat := convertMimeTypeToExtension(prediction.MimeType)
			bifrostResp.ImageGenerationResponseParameters = &schemas.ImageGenerationResponseParameters{
				OutputFormat: outputFormat,
			}
		}
	}

	return bifrostResp
}

// ToGeminiImageGenerationResponse converts a BifrostImageGenerationResponse back to Gemini format
func ToGeminiImageGenerationResponse(ctx context.Context, bifrostResp *schemas.BifrostImageGenerationResponse) (*GenerateContentResponse, error) {
	if bifrostResp == nil {
		return nil, nil
	}

	geminiResp := &GenerateContentResponse{
		ResponseID:   bifrostResp.ID,
		ModelVersion: bifrostResp.Model,
	}

	// Convert image data to candidate parts
	if len(bifrostResp.Data) > 0 {
		parts := make([]*Part, 0, len(bifrostResp.Data))
		for i := range bifrostResp.Data {
			imageData := &bifrostResp.Data[i]
			// Determine MIME type - convert file extension back to MIME type
			mimeType := "image/png" // default
			if bifrostResp.ImageGenerationResponseParameters != nil && bifrostResp.ImageGenerationResponseParameters.OutputFormat != "" {
				mimeType = convertOutputFormatToMimeType(bifrostResp.ImageGenerationResponseParameters.OutputFormat)
				if mimeType == "" {
					// Fallback: if conversion fails, assume PNG
					mimeType = "image/png"
				}
			}
			if imageData.B64JSON == "" && imageData.URL != "" {
				// Fetch image from URL with context-aware timeout and size limit
				downloadedImage, err := downloadImageFromURL(ctx, imageData.URL)
				if err != nil {
					return nil, fmt.Errorf("failed to download image from URL: %w", err)
				}
				imageData.B64JSON = downloadedImage
			}
			part := &Part{
				InlineData: &Blob{
					Data:     imageData.B64JSON,
					MIMEType: mimeType,
				},
			}
			parts = append(parts, part)
		}

		geminiResp.Candidates = []*Candidate{
			{
				Content: &Content{
					Role:  RoleModel,
					Parts: parts,
				},
				FinishReason: FinishReasonStop,
			},
		}
	}

	// Convert usage metadata
	if bifrostResp.Usage != nil {
		geminiResp.UsageMetadata = &GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(bifrostResp.Usage.InputTokens),
			CandidatesTokenCount: int32(bifrostResp.Usage.OutputTokens),
			TotalTokenCount:      int32(bifrostResp.Usage.TotalTokens),
		}
	}

	return geminiResp, nil
}
