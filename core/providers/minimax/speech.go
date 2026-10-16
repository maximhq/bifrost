package minimax

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type speechResponse struct {
	Data struct {
		Audio  string `json:"audio"`
		Status int    `json:"status"`
	} `json:"data"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

func (provider *MinimaxProvider) speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest, stream bool) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if request == nil || request.Input == nil || request.Input.Input == "" {
		return nil, providerUtils.NewBifrostOperationError("speech input text is required", nil)
	}
	body := map[string]interface{}{"model": request.Model, "text": request.Input.Input, "stream": stream}
	if request.Params != nil {
		p := request.Params
		if p.ResponseFormat != "" {
			body["audio_setting"] = map[string]interface{}{"format": p.ResponseFormat}
		}
		if p.VoiceConfig != nil {
			body["voice_setting"] = p.VoiceConfig
		}
		if p.LanguageCode != nil {
			body["language_boost"] = *p.LanguageCode
		}
		for k, v := range p.ExtraParams {
			body[k] = v
		}
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to encode MiniMax speech request", err)
	}
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/v1/t2a_v2"))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	req.SetBody(jsonBody)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("MiniMax speech request failed", nil), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	var decoded speechResponse
	if err := json.Unmarshal(resp.Body(), &decoded); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to parse MiniMax speech response", err), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	audio, err := base64.StdEncoding.DecodeString(decoded.Data.Audio)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to decode MiniMax speech audio", err), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	result := &schemas.BifrostSpeechResponse{Audio: audio, ExtraFields: schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()}}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&result.ExtraFields, jsonBody)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		result.ExtraFields.RawResponse = decoded
	}
	return result, nil
}
