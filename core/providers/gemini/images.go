package gemini

import (
	"github.com/maximhq/bifrost/core/schemas"
)

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

			if textContent != "" {

				// Set usage information
				bifrostResp.Usage = &schemas.ImageUsage{
					InputTokens:  inputTokens,
					OutputTokens: outputTokens,
					TotalTokens:  totalTokens,
				}
			}
			bifrostResp.Data = imageData[0:]
			bifrostResp.Params = &imageMetadata[0]
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

	// Create parts for image gen request
	parts := []*Part{
		{
			Text: bifrostReq.Input.Prompt,
		},
	}

	geminiReq.Contents = []Content{
		{
			Parts: parts,
		},
	}

	return geminiReq
}
