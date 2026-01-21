package gemini

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostTranscriptionRequest converts a GeminiGenerationRequest to a BifrostTranscriptionRequest
func (request *GeminiGenerationRequest) ToBifrostTranscriptionRequest(ctx *schemas.BifrostContext) (*schemas.BifrostTranscriptionRequest, error) {
	provider, model := schemas.ParseModelString(request.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.Gemini))

	bifrostReq := &schemas.BifrostTranscriptionRequest{
		Provider: provider,
		Model:    model,
	}

	// Extract audio data and prompt from contents
	var promptText string
	var audioData []byte
	var audioMimeType string

	for _, content := range request.Contents {
		for _, part := range content.Parts {
			// Extract text prompt
			if part.Text != "" {
				if promptText != "" {
					promptText += " "
				}
				promptText += part.Text
			}

			// Extract audio data from inline data
			if part.InlineData != nil && strings.HasPrefix(strings.ToLower(part.InlineData.MIMEType), "audio/") {
				decodedData, err := decodeBase64StringToBytes(part.InlineData.Data)
				if err != nil {
					return nil, fmt.Errorf("failed to decode base64 audio data: %v", err)
				}
				audioData = append(audioData, decodedData...)
				if audioMimeType == "" {
					audioMimeType = part.InlineData.MIMEType
				}
			}

			// Extract audio data from file data (would need to be fetched separately in real scenario)
			// For now, we just note the file URI in extra params
			if part.FileData != nil && strings.HasPrefix(strings.ToLower(part.FileData.MIMEType), "audio/") {
				if bifrostReq.Params == nil {
					bifrostReq.Params = &schemas.TranscriptionParameters{}
				}
				if bifrostReq.Params.ExtraParams == nil {
					bifrostReq.Params.ExtraParams = make(map[string]interface{})
				}
				bifrostReq.Params.ExtraParams["file_uri"] = part.FileData.FileURI
				if audioMimeType == "" {
					audioMimeType = part.FileData.MIMEType
				}
			}
		}
	}

	// Set the audio input
	bifrostReq.Input = &schemas.TranscriptionInput{
		File: audioData,
	}

	// Set parameters
	if bifrostReq.Params == nil {
		bifrostReq.Params = &schemas.TranscriptionParameters{}
	}

	// Set prompt if provided
	if promptText != "" {
		bifrostReq.Params.Prompt = &promptText
	}

	// Handle safety settings from request
	if len(request.SafetySettings) > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}
		bifrostReq.Params.ExtraParams["safety_settings"] = request.SafetySettings
	}

	// Handle cached content
	if request.CachedContent != "" {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}
		bifrostReq.Params.ExtraParams["cached_content"] = request.CachedContent
	}

	// Handle labels
	if len(request.Labels) > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}
		bifrostReq.Params.ExtraParams["labels"] = request.Labels
	}

	return bifrostReq, nil
}

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
				if settings, ok := SafeExtractSafetySettings(safetySettings); ok {
					geminiReq.SafetySettings = settings
				}
			}

			// Cached content
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				geminiReq.CachedContent = cachedContent
			}

			// Labels
			if labels, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "labels"); ok {
				if labelMap, ok := schemas.SafeExtractStringMap(labels); ok {
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
	parts := []*Part{
		{
			Text: prompt,
		},
	}

	// Add audio file if present
	if len(bifrostReq.Input.File) > 0 {
		parts = append(parts, &Part{
			InlineData: &Blob{
				MIMEType: utils.DetectAudioMimeType(bifrostReq.Input.File),
				Data:     encodeBytesToBase64String(bifrostReq.Input.File),
			},
		})
	}

	geminiReq.Contents = []Content{
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
	inputTokens, outputTokens, totalTokens, _, _ := response.extractUsageMetadata()

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
