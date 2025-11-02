package gemini

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

func ToGeminiSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Convert parameters to generation config
	geminiReq.GenerationConfig.ResponseModalities = []Modality{ModalityAudio}

	// Convert speech input to Gemini format
	if bifrostReq.Input.Input != "" {
		geminiReq.Contents = []Content{
			{
				Parts: []*Part{
					{
						Text: bifrostReq.Input.Input,
					},
				},
			},
		}

		// Add speech config to generation config if voice config is provided
		if bifrostReq.Params != nil && bifrostReq.Params.VoiceConfig != nil && bifrostReq.Params.VoiceConfig.Voice != nil {
			addSpeechConfigToGenerationConfig(&geminiReq.GenerationConfig, bifrostReq.Params.VoiceConfig)
		}
	}

	return geminiReq
}

// ToBifrostSpeechResponse converts a GenerateContentResponse to a BifrostSpeechResponse
func (response *GenerateContentResponse) ToBifrostSpeechResponse() *schemas.BifrostSpeechResponse {
	bifrostResp := &schemas.BifrostSpeechResponse{}

	// Process candidates to extract audio content
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			var audioData []byte

			// Extract audio data from all parts
			for _, part := range candidate.Content.Parts {
				if part.InlineData != nil && part.InlineData.Data != nil {
					// Check if this is audio data
					if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
						audioData = append(audioData, part.InlineData.Data...)
					}
				}
			}

			if len(audioData) > 0 {
				bifrostResp.Audio = audioData
			}
		}
	}

	return bifrostResp
}

// ToGeminiSpeechResponse converts a BifrostSpeechResponse to Gemini's GenerateContentResponse
func ToGeminiSpeechResponse(bifrostResp *schemas.BifrostSpeechResponse) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	genaiResp := &GenerateContentResponse{}

	candidate := &Candidate{
		Content: &Content{
			Parts: []*Part{
				{
					InlineData: &Blob{
						Data:     bifrostResp.Audio,
						MIMEType: detectAudioMimeType(bifrostResp.Audio),
					},
				},
			},
			Role: string(RoleModel),
		},
	}

	genaiResp.Candidates = []*Candidate{candidate}
	return genaiResp
}
