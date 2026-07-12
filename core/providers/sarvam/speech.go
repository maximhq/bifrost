package sarvam

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToSarvamSpeechRequest maps a Bifrost speech request onto Sarvam's text-to-speech request.
func ToSarvamSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) *SarvamSpeechRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	sarvamReq := &SarvamSpeechRequest{
		Text:  bifrostReq.Input.Input,
		Model: bifrostReq.Model,
	}

	if bifrostReq.Params == nil {
		return sarvamReq
	}

	// Copy so consuming keys below doesn't mutate the caller's request map.
	if bifrostReq.Params.ExtraParams != nil {
		sarvamReq.ExtraParams = make(map[string]interface{}, len(bifrostReq.Params.ExtraParams))
		for k, v := range bifrostReq.Params.ExtraParams {
			sarvamReq.ExtraParams[k] = v
		}
	}

	if bifrostReq.Params.LanguageCode != nil {
		sarvamReq.TargetLanguageCode = *bifrostReq.Params.LanguageCode
	}
	if bifrostReq.Params.VoiceConfig != nil && bifrostReq.Params.VoiceConfig.Voice != nil {
		sarvamReq.Speaker = bifrostReq.Params.VoiceConfig.Voice
	}
	if bifrostReq.Params.Speed != nil {
		sarvamReq.Pace = bifrostReq.Params.Speed
	}

	if ep := bifrostReq.Params.ExtraParams; ep != nil {
		if v, ok := schemas.SafeExtractStringPointer(ep["target_language_code"]); ok {
			delete(sarvamReq.ExtraParams, "target_language_code")
			sarvamReq.TargetLanguageCode = *v
		}
		if v, ok := schemas.SafeExtractStringPointer(ep["speaker"]); ok {
			delete(sarvamReq.ExtraParams, "speaker")
			sarvamReq.Speaker = v
		}
		if v, ok := schemas.SafeExtractFloat64Pointer(ep["pace"]); ok {
			delete(sarvamReq.ExtraParams, "pace")
			sarvamReq.Pace = v
		}
		if v, ok := schemas.SafeExtractFloat64Pointer(ep["pitch"]); ok {
			delete(sarvamReq.ExtraParams, "pitch")
			sarvamReq.Pitch = v
		}
		if v, ok := schemas.SafeExtractFloat64Pointer(ep["loudness"]); ok {
			delete(sarvamReq.ExtraParams, "loudness")
			sarvamReq.Loudness = v
		}
		if v, ok := schemas.SafeExtractFloat64Pointer(ep["temperature"]); ok {
			delete(sarvamReq.ExtraParams, "temperature")
			sarvamReq.Temperature = v
		}
		if v, ok := schemas.SafeExtractIntPointer(ep["speech_sample_rate"]); ok {
			delete(sarvamReq.ExtraParams, "speech_sample_rate")
			sarvamReq.SpeechSampleRate = v
		}
		if v, ok := schemas.SafeExtractStringPointer(ep["output_audio_codec"]); ok {
			delete(sarvamReq.ExtraParams, "output_audio_codec")
			sarvamReq.OutputAudioCodec = v
		}
		if v, ok := schemas.SafeExtractBoolPointer(ep["enable_preprocessing"]); ok {
			delete(sarvamReq.ExtraParams, "enable_preprocessing")
			sarvamReq.EnablePreprocessing = v
		}
		if v, ok := schemas.SafeExtractStringPointer(ep["dict_id"]); ok {
			delete(sarvamReq.ExtraParams, "dict_id")
			sarvamReq.DictID = v
		}
		if v, ok := schemas.SafeExtractBoolPointer(ep["enable_cached_responses"]); ok {
			delete(sarvamReq.ExtraParams, "enable_cached_responses")
			sarvamReq.EnableCachedResponses = v
		}
	}

	return sarvamReq
}
