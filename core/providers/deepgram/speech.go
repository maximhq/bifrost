package deepgram

import (
	"github.com/maximhq/bifrost/core/schemas"
)

func ToDeepgramSpeechRequest(
	bifrostReq *schemas.BifrostSpeechRequest,
) *DeepgramSpeechRequest {

	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	req := &DeepgramSpeechRequest{
        Text:  bifrostReq.Input.Input,
        Model: bifrostReq.Model,
    }

    if req.Model == "" {
        req.Model = "aura-2-thalia-en"
    }

	if bifrostReq.Model != "" {
		req.Model = bifrostReq.Model
	}

	if bifrostReq.Params == nil {
		return req
	}

	if bifrostReq.Params.Speed != nil {
		req.Speed = *bifrostReq.Params.Speed
	}

	if bifrostReq.Params.ResponseFormat != "" {
		req.Encoding = ConvertBifrostSpeechFormatToDeepgram(bifrostReq.Params.ResponseFormat)
	}

	if bifrostReq.Params.ExtraParams != nil {

		req.ExtraParams = bifrostReq.Params.ExtraParams

		if v, ok := schemas.SafeExtractStringPointer(
			req.ExtraParams["encoding"],
		); ok {
			req.Encoding = *v
			delete(req.ExtraParams, "encoding")
		}

		if v, ok := schemas.SafeExtractStringPointer(
			req.ExtraParams["container"],
		); ok {
			req.Container = *v
			delete(req.ExtraParams, "container")
		}

		if v, ok := schemas.SafeExtractIntPointer(
			req.ExtraParams["sample_rate"],
		); ok {
			req.SampleRate = *v
			delete(req.ExtraParams, "sample_rate")
		}
	}

	return req
}