package mistral

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// MistralSpeechRequest is the JSON body of Mistral's POST /v1/audio/speech
// (Voxtral TTS, e.g. voxtral-mini-tts-latest).
type MistralSpeechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	VoiceID        *string `json:"voice_id,omitempty"`
	ResponseFormat *string `json:"response_format,omitempty"`

	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams satisfies providerUtils.RequestBodyWithExtraParams.
func (r *MistralSpeechRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// MistralSpeechResponse is the JSON response of Mistral's POST /v1/audio/speech:
// the audio is base64-encoded in audio_data.
type MistralSpeechResponse struct {
	AudioData string `json:"audio_data"`
}

// ToMistralSpeechRequest maps a Bifrost speech request onto Mistral's speech body.
// The voice is a preset slug (e.g. gb_jane_neutral) or a saved voice ID;
// response_format is mp3 (default), wav, pcm, flac or opus. Other Mistral fields
// (ref_audio, expressiveness, phoneme_hints, ...) pass through ExtraParams.
func ToMistralSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) *MistralSpeechRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	mistralReq := &MistralSpeechRequest{
		Model: bifrostReq.Model,
		Input: bifrostReq.Input.Input,
	}

	params := bifrostReq.Params
	if params == nil {
		return mistralReq
	}
	if params.VoiceConfig != nil && params.VoiceConfig.Voice != nil && *params.VoiceConfig.Voice != "" {
		mistralReq.VoiceID = params.VoiceConfig.Voice
	}
	if params.ResponseFormat != "" {
		mistralReq.ResponseFormat = &params.ResponseFormat
	}
	mistralReq.ExtraParams = params.ExtraParams

	return mistralReq
}
