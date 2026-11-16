package soniox

import (
	"maps"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToSonioxSpeechRequest maps a Bifrost speech request onto Soniox's POST /tts body.
// Soniox requires language and audio_format; language comes from language_code and an
// absent value is left for Soniox to reject.
func ToSonioxSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) *SonioxSpeechRequest {
	if bifrostReq == nil {
		return nil
	}

	req := &SonioxSpeechRequest{
		Model:       bifrostReq.Model,
		AudioFormat: convertBifrostSpeechFormatToSoniox(""),
	}
	if bifrostReq.Input != nil {
		req.Text = bifrostReq.Input.Input
	}
	if bifrostReq.Params == nil {
		return req
	}

	params := bifrostReq.Params
	req.AudioFormat = convertBifrostSpeechFormatToSoniox(params.ResponseFormat)
	req.Speed = params.Speed
	if params.LanguageCode != nil {
		req.Language = *params.LanguageCode
	}
	if params.VoiceConfig != nil && params.VoiceConfig.Voice != nil {
		req.Voice = *params.VoiceConfig.Voice
	}

	if params.ExtraParams != nil {
		// Work on a copy so a retry or fallback sees the caller's original params.
		req.ExtraParams = maps.Clone(params.ExtraParams)
		if sampleRate, ok := schemas.SafeExtractIntPointer(req.ExtraParams["sample_rate"]); ok {
			delete(req.ExtraParams, "sample_rate")
			req.SampleRate = sampleRate
		}
		if bitrate, ok := schemas.SafeExtractIntPointer(req.ExtraParams["bitrate"]); ok {
			delete(req.ExtraParams, "bitrate")
			req.Bitrate = bitrate
		}
		if reduceSilence, ok := schemas.SafeExtractBoolPointer(req.ExtraParams["reduce_silence"]); ok {
			delete(req.ExtraParams, "reduce_silence")
			req.ReduceSilence = reduceSilence
		}
		if clientReferenceID, ok := schemas.SafeExtractStringPointer(req.ExtraParams["client_reference_id"]); ok {
			delete(req.ExtraParams, "client_reference_id")
			req.ClientReferenceID = clientReferenceID
		}
	}

	return req
}
