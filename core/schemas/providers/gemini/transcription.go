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

// ToBifrostTranscriptionResponse converts a GenerateContentResponse to a BifrostTranscriptionResponse
func (response *GenerateContentResponse) ToBifrostTranscriptionResponse() *schemas.BifrostTranscriptionResponse {
	bifrostResp := &schemas.BifrostTranscriptionResponse{}

	// Extract usage metadata
	inputTokens, outputTokens, totalTokens := response.extractUsageMetadata()

	// Process candidates to extract text content
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			var textContent string

			// Extract text content from all parts
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					textContent += part.Text
				}
			}

			if textContent != "" {
				bifrostResp.Text = textContent
				bifrostResp.Task = schemas.Ptr("transcribe")

				// Set usage information
				bifrostResp.Usage = &schemas.TranscriptionUsage{
					Type:         "tokens",
					InputTokens:  &inputTokens,
					OutputTokens: &outputTokens,
					TotalTokens:  &totalTokens,
				}
			}
		}
	}

	return bifrostResp
}

// ToGeminiTranscriptionResponse converts a BifrostTranscriptionResponse to Gemini's GenerateContentResponse
func ToGeminiTranscriptionResponse(bifrostResp *schemas.BifrostTranscriptionResponse) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	genaiResp := &GenerateContentResponse{}

	candidate := &Candidate{
		Content: &Content{
			Parts: []*Part{
				{
					Text: bifrostResp.Text,
				},
			},
			Role: string(RoleModel),
		},
	}

	// Set usage metadata from transcription usage
	if bifrostResp.Usage != nil {
		var promptTokens, candidatesTokens, totalTokens int32
		if bifrostResp.Usage.InputTokens != nil {
			promptTokens = int32(*bifrostResp.Usage.InputTokens)
		}
		if bifrostResp.Usage.OutputTokens != nil {
			candidatesTokens = int32(*bifrostResp.Usage.OutputTokens)
		}
		if bifrostResp.Usage.TotalTokens != nil {
			totalTokens = int32(*bifrostResp.Usage.TotalTokens)
		}

		genaiResp.UsageMetadata = &GenerateContentResponseUsageMetadata{
			PromptTokenCount:     promptTokens,
			CandidatesTokenCount: candidatesTokens,
			TotalTokenCount:      totalTokens,
		}
	}

	genaiResp.Candidates = []*Candidate{candidate}
	return genaiResp
}
