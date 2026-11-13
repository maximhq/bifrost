package xai

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// xAI text-to-speech defaults: POST /v1/tts requires a language ("auto" detects it
// from the text) and falls back to the "eve" voice.
const (
	xaiSpeechDefaultLanguage = "auto"
	xaiSpeechDefaultVoice    = "eve"
)

// XAISpeechRequest is the JSON body of xAI's POST /v1/tts. The endpoint answers with
// raw audio bytes in the requested codec.
type XAISpeechRequest struct {
	Text         string                 `json:"text"`
	Language     string                 `json:"language"`
	VoiceID      string                 `json:"voice_id,omitempty"`
	Speed        *float64               `json:"speed,omitempty"`
	OutputFormat *XAISpeechOutputFormat `json:"output_format,omitempty"`

	ExtraParams map[string]interface{} `json:"-"`
}

// XAISpeechOutputFormat selects the audio codec (mp3, wav, pcm, opus, mulaw, alaw)
// and optional sample rate of the synthesized audio.
type XAISpeechOutputFormat struct {
	Codec      string `json:"codec"`
	SampleRate *int   `json:"sample_rate,omitempty"`
}

// GetExtraParams satisfies providerUtils.RequestBodyWithExtraParams.
func (r *XAISpeechRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// ToXAISpeechRequest maps a Bifrost speech request onto xAI's /v1/tts body.
// response_format becomes output_format.codec; an ExtraParams "sample_rate" sets
// output_format.sample_rate. xAI's TTS endpoint takes no model field.
func ToXAISpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) *XAISpeechRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	xaiReq := &XAISpeechRequest{
		Text:     bifrostReq.Input.Input,
		Language: xaiSpeechDefaultLanguage,
		VoiceID:  xaiSpeechDefaultVoice,
	}

	params := bifrostReq.Params
	if params == nil {
		return xaiReq
	}

	if params.LanguageCode != nil && *params.LanguageCode != "" {
		xaiReq.Language = *params.LanguageCode
	}
	if params.VoiceConfig != nil && params.VoiceConfig.Voice != nil && *params.VoiceConfig.Voice != "" {
		xaiReq.VoiceID = *params.VoiceConfig.Voice
	}
	xaiReq.Speed = params.Speed
	if params.ResponseFormat != "" {
		xaiReq.OutputFormat = &XAISpeechOutputFormat{Codec: params.ResponseFormat}
	}

	if params.ExtraParams != nil {
		// Copy so consuming keys below doesn't mutate the caller's request map.
		xaiReq.ExtraParams = make(map[string]interface{}, len(params.ExtraParams))
		for k, v := range params.ExtraParams {
			xaiReq.ExtraParams[k] = v
		}
		if v, ok := schemas.SafeExtractIntPointer(xaiReq.ExtraParams["sample_rate"]); ok {
			delete(xaiReq.ExtraParams, "sample_rate")
			if xaiReq.OutputFormat == nil {
				xaiReq.OutputFormat = &XAISpeechOutputFormat{Codec: "mp3"}
			}
			xaiReq.OutputFormat.SampleRate = v
		}
	}

	return xaiReq
}
