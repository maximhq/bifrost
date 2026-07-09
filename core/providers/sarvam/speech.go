package sarvam

import (
	"encoding/base64"
	"net/http"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToSarvamSpeechRequest maps a Bifrost speech request onto Sarvam's text-to-speech request.
// Bifrost's generic voice/speed fields map to Sarvam's speaker/pace; Indic-specific fields
// (target_language_code, pitch, loudness, dict_id, ...) are read from ExtraParams.
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

	sarvamReq.ExtraParams = bifrostReq.Params.ExtraParams

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

// Speech performs a text-to-speech request to Sarvam's API. Sarvam returns JSON with a
// base64-encoded audios array rather than raw binary, so the audio is decoded here.
func (provider *SarvamProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if request == nil || request.Input == nil || request.Input.Input == "" {
		return nil, providerUtils.NewBifrostOperationError("speech input text is required", nil)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/text-to-speech")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("api-subscription-key", key.Value.GetValue())
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToSarvamSpeechRequest(request), nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	req.SetBody(jsonData)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseSarvamError(resp), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	var sarvamResp SarvamSpeechResponse
	if err := sonic.Unmarshal(body, &sarvamResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to parse Sarvam text-to-speech response", err)
	}
	if len(sarvamResp.Audios) == 0 || sarvamResp.Audios[0] == "" {
		return nil, providerUtils.NewBifrostOperationError("Sarvam text-to-speech response contained no audio", nil)
	}

	audioBytes, err := base64.StdEncoding.DecodeString(sarvamResp.Audios[0])
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to decode Sarvam base64 audio", err)
	}

	bifrostResponse := &schemas.BifrostSpeechResponse{
		Audio: audioBytes,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
		},
	}

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(body, &rawResponse); err != nil {
			rawResponse = string(body)
		}
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}
