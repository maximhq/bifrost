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
	provider, model := schemas.ParseModelString(request.Model, "")

	bifrostReq := &schemas.BifrostSpeechRequest{
		Provider: provider,
		Model:    model,
	}

	// Extract text input from contents
	var textInput string
	for _, content := range request.Contents {
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
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
			if languageCode := request.GenerationConfig.SpeechConfig.LanguageCode; languageCode != "" {
				bifrostReq.Params.LanguageCode = &languageCode
			}
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
	// Gemini TTS returns raw PCM; only wav (wrapped here) or empty (raw PCM) can be produced.
	if bifrostReq.Params != nil && bifrostReq.Params.ResponseFormat != "" && bifrostReq.Params.ResponseFormat != "wav" {
		return nil, utils.InvalidRequestErrorf("gemini speech does not support response_format %q; use wav, or leave it empty for raw 24 kHz PCM", bifrostReq.Params.ResponseFormat)
	}
	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}
	// Convert parameters to generation config
	geminiReq.GenerationConfig.ResponseModalities = []Modality{ModalityAudio}
	// Convert speech input to Gemini format
	if bifrostReq.Input != nil && bifrostReq.Input.Input != "" {
		text := bifrostReq.Input.Input
		// Gemini TTS has no instructions field; a style direction ahead of the text steers the voice.
		if bifrostReq.Params != nil {
			if instructions := strings.TrimSpace(bifrostReq.Params.Instructions); instructions != "" {
				text = instructions + ": " + text
			}
		}
		geminiReq.Contents = []Content{
			{
				Parts: []*Part{
					{
						Text: text,
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
			geminiReq.ExtraParams = bifrostReq.Params.ExtraParams
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
				if part == nil {
					continue
				}
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

			// Set usage information
			if response.UsageMetadata != nil {
				bifrostResp.Usage = convertGeminiUsageMetadataToSpeechUsage(response.UsageMetadata)
			}
		}
	}
	return bifrostResp, nil
}

// geminiPCMMimeType labels the headerless signed 16-bit 24 kHz PCM that Gemini TTS answers with.
const geminiPCMMimeType = "audio/L16;codec=pcm;rate=24000"

// geminiSpeechMimeType labels speech audio for a GenAI client; Gemini TTS answers in raw PCM or WAV, so audio without a WAV header is PCM whatever its first bytes look like.
func geminiSpeechMimeType(audio []byte, routing schemas.RoutingInfo) string {
	mimeType := utils.DetectAudioMimeType(audio)
	model := routing.Model
	if routing.ResolvedKeyAlias != nil && routing.ResolvedKeyAlias.ModelID != "" {
		model = routing.ResolvedKeyAlias.ModelID // a key alias hides the model behind its own name
	}
	isGeminiTTS := (routing.Provider == schemas.Gemini || routing.Provider == schemas.Vertex) &&
		strings.HasPrefix(strings.TrimPrefix(model, "models/"), "gemini")
	if isGeminiTTS && mimeType != "audio/wav" {
		return geminiPCMMimeType
	}
	return mimeType
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
						MIMEType: geminiSpeechMimeType(bifrostResp.Audio, bifrostResp.ExtraFields.RoutingInfo),
					},
				},
			},
			Role: string(RoleModel),
		},
	}

	// Set usage metadata if present
	if bifrostResp.Usage != nil {
		genaiResp.UsageMetadata = convertBifrostSpeechUsageToGeminiUsageMetadata(bifrostResp.Usage)
	}

	genaiResp.Candidates = []*Candidate{candidate}
	return genaiResp
}

// ToGeminiSpeechStreamResponse converts a Bifrost speech stream chunk to the
// GenerateContentResponse shape used by Gemini's streamGenerateContent SSE API.
func ToGeminiSpeechStreamResponse(bifrostResp *schemas.BifrostSpeechStreamResponse) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	genaiResp := &GenerateContentResponse{}
	if len(bifrostResp.Audio) > 0 {
		mimeType := utils.DetectAudioMimeType(bifrostResp.Audio)
		provider := bifrostResp.ExtraFields.RoutingInfo.Provider
		if provider == schemas.Gemini || provider == schemas.Vertex {
			// Gemini TTS stream chunks are headerless signed 16-bit PCM. Generic
			// header detection falls back to audio/mp3 for headerless data, which
			// makes native GenAI clients decode valid PCM as the wrong format.
			mimeType = geminiPCMMimeType
		}
		genaiResp.Candidates = []*Candidate{{
			Content: &Content{
				Parts: []*Part{{
					InlineData: &Blob{
						Data:     encodeBytesToBase64String(bifrostResp.Audio),
						MIMEType: mimeType,
					},
				}},
				Role: string(RoleModel),
			},
		}}
	}

	if bifrostResp.Type == schemas.SpeechStreamResponseTypeDone {
		if len(genaiResp.Candidates) == 0 {
			genaiResp.Candidates = []*Candidate{{FinishReason: FinishReasonStop}}
		} else {
			genaiResp.Candidates[0].FinishReason = FinishReasonStop
		}
	}
	if bifrostResp.Usage != nil {
		genaiResp.UsageMetadata = convertBifrostSpeechUsageToGeminiUsageMetadata(bifrostResp.Usage)
	}

	return genaiResp
}
