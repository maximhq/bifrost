package gemini

import "github.com/maximhq/bifrost/core/schemas"

func ToGeminiTranscriptionRequest(bifrostReq *schemas.BifrostTranscriptionRequest) *GeminiGenerationRequest {
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

	// Determine the prompt text
	var prompt string
	if bifrostReq.Params != nil && bifrostReq.Params.Prompt != nil {
		prompt = *bifrostReq.Params.Prompt
	} else {
		prompt = "Generate a transcript of the speech."
	}

	// Create parts for the transcription request
	parts := []*CustomPart{
		{
			Text: prompt,
		},
	}

	// Add audio file if present
	if len(bifrostReq.Input.File) > 0 {
		parts = append(parts, &CustomPart{
			InlineData: &CustomBlob{
				MIMEType: detectAudioMimeType(bifrostReq.Input.File),
				Data:     bifrostReq.Input.File,
			},
		})
	}

	geminiReq.Contents = []CustomContent{
		{
			Parts: parts,
		},
	}

	return geminiReq
}
