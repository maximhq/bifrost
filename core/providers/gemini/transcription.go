package gemini

import (
	"fmt"
	"maps"
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const geminiInteractionTranscriptionModel = "gemini-3.5-transcribe"

type geminiInteractionContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

type geminiInteractionTranscriptionRequest struct {
	Model            string                             `json:"model"`
	Input            []geminiInteractionContent         `json:"input"`
	GenerationConfig *geminiInteractionGenerationConfig `json:"generation_config,omitempty"`
	Stream           bool                               `json:"stream,omitempty"`
}

type geminiInteractionGenerationConfig struct {
	TranscriptionConfig map[string]interface{} `json:"transcription_config,omitempty"`
}

type geminiInteractionResponse struct {
	OutputText string                   `json:"output_text,omitempty"`
	Steps      []geminiInteractionStep  `json:"steps,omitempty"`
	Usage      *geminiInteractionsUsage `json:"usage,omitempty"`
}

type geminiInteractionStep struct {
	Type    string                     `json:"type,omitempty"`
	Content []geminiInteractionContent `json:"content,omitempty"`
}

type geminiInteractionStreamEvent struct {
	EventType   string                              `json:"event_type,omitempty"`
	Delta       *geminiInteractionContent           `json:"delta,omitempty"`
	Usage       *geminiInteractionsUsage            `json:"usage,omitempty"`
	Interaction *geminiInteractionStreamInteraction `json:"interaction,omitempty"`
	Metadata    *geminiInteractionStreamMetadata    `json:"metadata,omitempty"`
	Error       *geminiInteractionStreamError       `json:"error,omitempty"`
}

type geminiInteractionStreamInteraction struct {
	Steps []geminiInteractionStep  `json:"steps,omitempty"`
	Usage *geminiInteractionsUsage `json:"usage,omitempty"`
}

type geminiInteractionStreamMetadata struct {
	TotalUsage *geminiInteractionsUsage `json:"total_usage,omitempty"`
}

type geminiInteractionStreamError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func isGeminiInteractionTranscriptionModel(model string) bool {
	return strings.TrimSpace(model) == geminiInteractionTranscriptionModel
}

// ToGeminiInteractionTranscriptionRequest converts a Bifrost transcription request to the Gemini Interactions API format.
func ToGeminiInteractionTranscriptionRequest(bifrostReq *schemas.BifrostTranscriptionRequest, stream bool) *geminiInteractionTranscriptionRequest {
	if bifrostReq == nil {
		return nil
	}

	request := &geminiInteractionTranscriptionRequest{
		Model:  bifrostReq.Model,
		Input:  make([]geminiInteractionContent, 0, 2),
		Stream: stream,
	}

	if bifrostReq.Params != nil && bifrostReq.Params.Prompt != nil && strings.TrimSpace(*bifrostReq.Params.Prompt) != "" {
		request.Input = append(request.Input, geminiInteractionContent{
			Type: "text",
			Text: *bifrostReq.Params.Prompt,
		})
	}

	if bifrostReq.Input != nil && len(bifrostReq.Input.File) > 0 {
		request.Input = append(request.Input, geminiInteractionContent{
			Type:     "audio",
			Data:     encodeBytesToBase64String(bifrostReq.Input.File),
			MIMEType: utils.DetectAudioMimeType(bifrostReq.Input.File),
		})
	}

	transcriptionConfig := make(map[string]interface{})
	if bifrostReq.Params != nil {
		if bifrostReq.Params.Language != nil && strings.TrimSpace(*bifrostReq.Params.Language) != "" {
			transcriptionConfig["language_codes"] = []string{*bifrostReq.Params.Language}
		}
		if len(bifrostReq.Params.TimestampGranularities) > 0 {
			transcriptionConfig["mode"] = map[string]interface{}{
				"type":                    "verbatim",
				"timestamp_granularities": bifrostReq.Params.TimestampGranularities,
			}
		}
		for _, key := range []string{"language_codes", "mode", "diarization_mode", "custom_vocabulary"} {
			if value, ok := bifrostReq.Params.ExtraParams[key]; ok {
				transcriptionConfig[key] = value
			}
		}
	}
	if len(transcriptionConfig) > 0 {
		request.GenerationConfig = &geminiInteractionGenerationConfig{
			TranscriptionConfig: transcriptionConfig,
		}
	}

	return request
}

// ToBifrostTranscriptionResponse converts a Gemini Interactions API response to a Bifrost transcription response.
func (response *geminiInteractionResponse) ToBifrostTranscriptionResponse() *schemas.BifrostTranscriptionResponse {
	if response == nil {
		return &schemas.BifrostTranscriptionResponse{}
	}

	textContent := strings.TrimSpace(response.OutputText)
	if textContent == "" {
		textContent = strings.TrimSpace(geminiInteractionTextFromSteps(response.Steps))
	}

	bifrostResp := &schemas.BifrostTranscriptionResponse{Text: textContent}
	if textContent != "" {
		bifrostResp.Task = schemas.Ptr("transcribe")
	}
	bifrostResp.Usage = geminiInteractionUsageToTranscriptionUsage(response.Usage)
	return bifrostResp
}

func geminiInteractionTextFromSteps(steps []geminiInteractionStep) string {
	var textContent strings.Builder
	for _, step := range steps {
		for _, content := range step.Content {
			if content.Type == "text" || content.Type == "" {
				textContent.WriteString(content.Text)
			}
		}
	}
	return textContent.String()
}

func geminiInteractionUsageToTranscriptionUsage(usage *geminiInteractionsUsage) *schemas.TranscriptionUsage {
	if usage == nil {
		return nil
	}
	return &schemas.TranscriptionUsage{
		Type:         "tokens",
		InputTokens:  schemas.Ptr(usage.InputTokens),
		OutputTokens: schemas.Ptr(usage.OutputTokens),
		TotalTokens:  schemas.Ptr(usage.TotalTokens),
	}
}

func geminiInteractionStreamUsage(event *geminiInteractionStreamEvent) *geminiInteractionsUsage {
	if event == nil {
		return nil
	}
	if event.Interaction != nil && event.Interaction.Usage != nil {
		return event.Interaction.Usage
	}
	if event.Metadata != nil && event.Metadata.TotalUsage != nil {
		return event.Metadata.TotalUsage
	}
	return event.Usage
}

// ToBifrostTranscriptionRequest converts a GeminiGenerationRequest to a BifrostTranscriptionRequest
func (request *GeminiGenerationRequest) ToBifrostTranscriptionRequest(ctx *schemas.BifrostContext) (*schemas.BifrostTranscriptionRequest, error) {
	provider, model := schemas.ParseModelString(request.Model, "")

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
		geminiReq.ExtraParams = maps.Clone(bifrostReq.Params.ExtraParams)
		// Handle extra parameters
		if bifrostReq.Params.ExtraParams != nil {
			// Safety settings
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
				delete(geminiReq.ExtraParams, "safety_settings")
				if settings, ok := SafeExtractSafetySettings(safetySettings); ok {
					geminiReq.SafetySettings = settings
				}
			}

			// Cached content
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				delete(geminiReq.ExtraParams, "cached_content")
				geminiReq.CachedContent = cachedContent
			}

			// Labels
			if labels, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "labels"); ok {
				if labelMap, ok := schemas.SafeExtractStringMap(labels); ok {
					delete(geminiReq.ExtraParams, "labels")
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
			}
		}
	}

	// Usage is billed even when the transcript is empty.
	bifrostResp.Usage = convertGeminiUsageMetadataToTranscriptionUsage(response.UsageMetadata)

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

	// Set usage metadata from transcription usage with modality details
	genaiResp.UsageMetadata = convertBifrostTranscriptionUsageToGeminiUsageMetadata(bifrostResp.Usage)

	genaiResp.Candidates = []*Candidate{candidate}
	return genaiResp
}
