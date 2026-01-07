package gemini

import (
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostImageGenerationRequest converts a Gemini generation request to a Bifrost image generation request
func (request *GeminiGenerationRequest) ToBifrostImageGenerationRequest() *schemas.BifrostImageGenerationRequest {
	if request == nil {
		return nil
	}

	bifrostReq := &schemas.BifrostImageGenerationRequest{
		Provider: schemas.Gemini,
		Model:    request.Model,
		Input:    &schemas.ImageGenerationInput{},
		Params:   &schemas.ImageGenerationParameters{},
	}

	// Convert string fallbacks to Fallback structs
	if len(request.Fallbacks) > 0 {
		bifrostReq.Fallbacks = make([]schemas.Fallback, len(request.Fallbacks))
		for i, fallback := range request.Fallbacks {
			bifrostReq.Fallbacks[i] = schemas.Fallback{
				Provider: schemas.Gemini,
				Model:    fallback,
			}
		}
	}

	// First, try to extract prompt from Imagen format (instances)
	if len(request.Instances) > 0 && request.Instances[0].Prompt != "" {
		bifrostReq.Input.Prompt = request.Instances[0].Prompt

		// Extract Imagen parameters
		if request.Parameters != nil {
			if request.Parameters.NumberOfImages != nil {
				bifrostReq.Params.N = request.Parameters.NumberOfImages
			} else if request.Parameters.SampleCount != nil {
				bifrostReq.Params.N = request.Parameters.SampleCount
			}
			// Convert Imagen size format to standard format
			if request.Parameters.ImageSize != nil || request.Parameters.AspectRatio != nil {
				size := convertImagenFormatToSize(request.Parameters.ImageSize, request.Parameters.AspectRatio)
				if size != "" {
					bifrostReq.Params.Size = &size
				}
			}
		}
		return bifrostReq
	}

	// Fall back to standard Gemini format (contents)
	if len(request.Contents) > 0 {
		for _, content := range request.Contents {
			for _, part := range content.Parts {
				if part.Text != "" {
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

// convertImagenFormatToSize converts Imagen imageSize and aspectRatio to standard WxH format
func convertImagenFormatToSize(imageSize *string, aspectRatio *string) string {
	// Default size based on imageSize parameter
	baseSize := 1024
	if imageSize != nil {
		switch *imageSize {
		case "2k":
			baseSize = 2048
		case "1k":
			baseSize = 1024
		}
	}

	// Apply aspect ratio
	if aspectRatio != nil {
		switch *aspectRatio {
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

func (response *GenerateContentResponse) ToBifrostImageGenerationResponse() *schemas.BifrostImageGenerationResponse {
	bifrostResp := &schemas.BifrostImageGenerationResponse{
		ID:    response.ResponseID,
		Model: response.ModelVersion,
	}

	// Extract usage metadata
	inputTokens, outputTokens, totalTokens, _, _ := response.extractUsageMetadata()

	// Process candidates to extract text content
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			var textContent string
			var imageData []schemas.ImageData
			var imageMetadata []schemas.ImageGenerationResponseParameters

			// Extract text content from all parts
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					textContent += part.Text
				}

				if part.InlineData != nil {
					imageData = append(imageData, schemas.ImageData{
						B64JSON: string(part.InlineData.Data),
					})
					imageMetadata = append(imageMetadata, schemas.ImageGenerationResponseParameters{
						OutputFormat: part.InlineData.MIMEType,
					})

				}
			}

			// Set usage information
			bifrostResp.Usage = &schemas.ImageUsage{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				TotalTokens:  totalTokens,
			}
			if len(imageData) > 0 {
				bifrostResp.Data = imageData
				if len(imageMetadata) > 0 {
					bifrostResp.Params = &imageMetadata[0]
				}
			}
		}
	}

	return bifrostResp
}

func ToGeminiImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Convert parameters to generation config
	if bifrostReq.Params != nil {

		// Handle extra parameters
		if bifrostReq.Params.ExtraParams != nil {
			// Safety settings
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
				if settings, ok := safetySettings.([]SafetySetting); ok {
					geminiReq.SafetySettings = settings
				}
			}

			// Cached content
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				geminiReq.CachedContent = cachedContent
			}

			// Labels
			if labels, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "labels"); ok {
				if labelMap, ok := labels.(map[string]string); ok {
					geminiReq.Labels = labelMap
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

	return geminiReq
}

// ToImagenImageGenerationRequest converts a Bifrost Image Request to Imagen format
func ToImagenImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *GeminiImagenRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	// Create instances array with prompt
	prompt := bifrostReq.Input.Prompt
	instances := []struct {
		Prompt *string `json:"prompt"`
	}{
		{
			Prompt: &prompt,
		},
	}

	req := &GeminiImagenRequest{
		Instances:  &instances,
		Parameters: GeminiImagenParameters{},
	}

	if bifrostReq.Params != nil {
		if bifrostReq.Params.N != nil {
			req.Parameters.NumberOfImages = bifrostReq.Params.N
		}

		// Handle size conversion
		if bifrostReq.Params.Size != nil {
			imageSize, aspectRatio := convertSizeToImagenFormat(*bifrostReq.Params.Size)
			if imageSize != "" {
				req.Parameters.ImageSize = &imageSize
			}
			if aspectRatio != "" {
				req.Parameters.AspectRatio = &aspectRatio
			}
		}

		// Handle extra parameters for Imagen-specific fields
		if bifrostReq.Params.ExtraParams != nil {
			if imageSize, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["imageSize"]); ok {
				req.Parameters.ImageSize = &imageSize
			}

			if aspectRatio, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["aspectRatio"]); ok {
				req.Parameters.AspectRatio = &aspectRatio
			}

			if personGeneration, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["personGeneration"]); ok {
				req.Parameters.PersonGeneration = &personGeneration
			}
		}
	}

	return req
}

// convertSizeToImagenFormat converts standard size format (e.g., "1024x1024") to Imagen format
// Returns (imageSize, aspectRatio) where imageSize is "1k" or "2k" and aspectRatio is "1:1", "3:4", etc.
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

	var imageSize string
	if width <= 1024 && height <= 1024 {
		imageSize = "1k"
	} else if width <= 2048 && height <= 2048 {
		imageSize = "2k"
	} else {
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
			// Store the first image's MIME type in params
			bifrostResp.Params = &schemas.ImageGenerationResponseParameters{
				OutputFormat: prediction.MimeType,
			}
		}
	}

	return bifrostResp
}

// ToGeminiImageGenerationResponse converts a BifrostImageGenerationResponse back to Gemini format
func ToGeminiImageGenerationResponse(bifrostResp *schemas.BifrostImageGenerationResponse) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	geminiResp := &GenerateContentResponse{
		ResponseID:   bifrostResp.ID,
		ModelVersion: bifrostResp.Model,
	}

	// Convert image data to candidate parts
	if len(bifrostResp.Data) > 0 {
		parts := make([]*Part, 0, len(bifrostResp.Data))
		for _, imageData := range bifrostResp.Data {
			// Determine MIME type
			mimeType := "image/png" // default
			if bifrostResp.Params != nil && bifrostResp.Params.OutputFormat != "" {
				mimeType = bifrostResp.Params.OutputFormat
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

	return geminiResp
}
