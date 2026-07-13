package cartesia

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToCartesiaSpeechRequest converts a Bifrost speech request into Cartesia's request body.
// When forStreaming is true the output_format is constrained to the "raw" container,
// which is the only container the /tts/sse endpoint supports.
func ToCartesiaSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest, forStreaming bool) *CartesiaSpeechRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	req := &CartesiaSpeechRequest{
		ModelID:    bifrostReq.Model,
		Transcript: bifrostReq.Input.Input,
	}

	responseFormat := ""
	if bifrostReq.Params != nil {
		req.ExtraParams = bifrostReq.Params.ExtraParams
		responseFormat = bifrostReq.Params.ResponseFormat

		if bifrostReq.Params.VoiceConfig != nil && bifrostReq.Params.VoiceConfig.Voice != nil {
			req.Voice = CartesiaVoice{Mode: "id", ID: *bifrostReq.Params.VoiceConfig.Voice}
		}

		if bifrostReq.Params.LanguageCode != nil {
			req.Language = bifrostReq.Params.LanguageCode
		}

		if bifrostReq.Params.WithTimestamps != nil {
			req.AddTimestamps = bifrostReq.Params.WithTimestamps
		}

		if bifrostReq.Params.Speed != nil {
			req.GenerationConfig = &CartesiaGenerationConfig{Speed: bifrostReq.Params.Speed}
		}
	}

	req.OutputFormat = resolveCartesiaOutputFormat(responseFormat, forStreaming, req.ExtraParams)

	return req
}
