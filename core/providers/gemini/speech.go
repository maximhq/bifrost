package gemini

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostSpeechRequest converts a GeminiGenerationRequest to a BifrostSpeechRequest
func (request *GeminiGenerationRequest) ToBifrostSpeechRequest(ctx *schemas.BifrostContext) *schemas.BifrostSpeechRequest {
	provider, model := schemas.ParseModelString(request.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.Gemini))

	bifrostReq := &schemas.BifrostSpeechRequest{
		Provider: provider,
		Model:    model,
	}

	// Extract text input from contents
	var textInput string
	for _, content := range request.Contents {
		for _, part := range content.Parts {
			if part.Text != "" {
				textInput += part.Text
			}
		}
	}

	bifrostReq.Input = &schemas.SpeechInput{
		Input: textInput,
	}

	// Convert generation config to parameters
	if request.GenerationConfig.SpeechConfig != nil || len(request.GenerationConfig.ResponseModalities) > 0 {
		bifrostReq.Params = &schemas.SpeechParameters{}

		// Extract voice config from speech config
		if request.GenerationConfig.SpeechConfig != nil {
			// Handle single-speaker voice config
			if request.GenerationConfig.SpeechConfig.VoiceConfig != nil {
				bifrostReq.Params.VoiceConfig = &schemas.SpeechVoiceInput{}

				if request.GenerationConfig.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig != nil {
					voiceName := request.GenerationConfig.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName
					bifrostReq.Params.VoiceConfig.Voice = &voiceName
				}
			} else if request.GenerationConfig.SpeechConfig.MultiSpeakerVoiceConfig != nil {
				// Handle multi-speaker voice config
				// Convert to Bifrost's MultiVoiceConfig format
				if len(request.GenerationConfig.SpeechConfig.MultiSpeakerVoiceConfig.SpeakerVoiceConfigs) > 0 {
					bifrostReq.Params.VoiceConfig = &schemas.SpeechVoiceInput{}
					multiVoiceConfig := make([]schemas.VoiceConfig, 0, len(request.GenerationConfig.SpeechConfig.MultiSpeakerVoiceConfig.SpeakerVoiceConfigs))

					for _, speakerConfig := range request.GenerationConfig.SpeechConfig.MultiSpeakerVoiceConfig.SpeakerVoiceConfigs {
						if speakerConfig.VoiceConfig != nil && speakerConfig.VoiceConfig.PrebuiltVoiceConfig != nil {
							multiVoiceConfig = append(multiVoiceConfig, schemas.VoiceConfig{
								Speaker: speakerConfig.Speaker,
								Voice:   speakerConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName,
							})
						}
					}

					bifrostReq.Params.VoiceConfig.MultiVoiceConfig = multiVoiceConfig
				}
			}
		}

		// Store response modalities in extra params if needed
		if len(request.GenerationConfig.ResponseModalities) > 0 {
			if bifrostReq.Params.ExtraParams == nil {
				bifrostReq.Params.ExtraParams = make(map[string]interface{})
			}
			modalities := make([]string, len(request.GenerationConfig.ResponseModalities))
			for i, mod := range request.GenerationConfig.ResponseModalities {
				modalities[i] = string(mod)
			}
			bifrostReq.Params.ExtraParams["response_modalities"] = modalities
		}
	}

	return bifrostReq
}

// ToGeminiSpeechRequest converts a BifrostSpeechRequest to a GeminiGenerationRequest
func ToGeminiSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) (*GeminiGenerationRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrostReq is nil")
	}
	// Here we confirm if the response_format is wav or empty string
	// If its anything else, we will return an error
	if bifrostReq.Params != nil && bifrostReq.Params.ResponseFormat != "" && bifrostReq.Params.ResponseFormat != "wav" {
		return nil, fmt.Errorf("gemini does not support response_format: %s. Only wav or empty string is supported which defaults to wav", bifrostReq.Params.ResponseFormat)
	}
	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}
	// Convert parameters to generation config
	geminiReq.GenerationConfig.ResponseModalities = []Modality{ModalityAudio}
	// Convert speech input to Gemini format
	if bifrostReq.Input != nil && bifrostReq.Input.Input != "" {
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
		if bifrostReq.Params != nil && bifrostReq.Params.VoiceConfig != nil {
			// Handle both single voice and multi-voice configurations
			if bifrostReq.Params.VoiceConfig.Voice != nil || len(bifrostReq.Params.VoiceConfig.MultiVoiceConfig) > 0 {
				addSpeechConfigToGenerationConfig(&geminiReq.GenerationConfig, bifrostReq.Params.VoiceConfig)
			}
		}
	}
	return geminiReq, nil
}

// ToBifrostSpeechResponse converts a GenerateContentResponse to a BifrostSpeechResponse
func (response *GenerateContentResponse) ToBifrostSpeechResponse(ctx context.Context) (*schemas.BifrostSpeechResponse, error) {
	bifrostResp := &schemas.BifrostSpeechResponse{}

	// Process candidates to extract audio content
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			var audioData []byte
			// Extract audio data from all parts
			for _, part := range candidate.Content.Parts {
				if part.InlineData != nil && len(part.InlineData.Data) > 0 {
					// Check if this is audio data
					if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
						decodedData, err := decodeBase64StringToBytes(part.InlineData.Data)
						if err != nil {
							return nil, fmt.Errorf("failed to decode base64 audio data: %v", err)
						}
						audioData = append(audioData, decodedData...)
					}
				}
			}
			if len(audioData) > 0 {
				responseFormat := ctx.Value(BifrostContextKeyResponseFormat).(string)
				// Gemini returns PCM audio (s16le, 24000 Hz, mono)
				// Convert to WAV for standard playable output format
				if responseFormat == "wav" {
					wavData, err := utils.ConvertPCMToWAV(audioData, utils.DefaultGeminiPCMConfig())
					if err != nil {
						return nil, fmt.Errorf("failed to convert PCM to WAV: %v", err)
					}
					bifrostResp.Audio = wavData
				} else {
					bifrostResp.Audio = audioData
				}
			}
		}
	}
	return bifrostResp, nil
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
						Data:     encodeBytesToBase64String(bifrostResp.Audio),
						MIMEType: utils.DetectAudioMimeType(bifrostResp.Audio),
					},
				},
			},
			Role: string(RoleModel),
		},
	}

	genaiResp.Candidates = []*Candidate{candidate}
	return genaiResp
}
