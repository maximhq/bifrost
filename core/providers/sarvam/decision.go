package sarvam

import (
	"net/http"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// sarvamDecisionPath is where Sarvam serves decision models in TypeSafe's
// System One shape.
const sarvamDecisionPath = "/v2/systemone"

// ToSarvamDecisionRequest converts a normalized decision request into Sarvam's
// System One body: the input becomes the state, and each question is keyed by
// its name. Sarvam serves the same state + named-questions shape as TypeSafe,
// but reads text only and has no box questions, so an image sent by reference
// and a box question are refused locally rather than sent to fail upstream.
func ToSarvamDecisionRequest(request *schemas.BifrostDecisionRequest) (*typesafe.TypesafeDecisionRequest, error) {
	if request.Input.HasAssetParts() {
		return nil, providerUtils.InvalidRequestErrorf("decision input carries an image sent by reference, which Sarvam does not support")
	}
	if request.Input.IsEmpty() {
		return nil, providerUtils.InvalidRequestErrorf("decision input is required")
	}
	for _, question := range request.Questions {
		if question.Type == schemas.DecisionTypeBox {
			return nil, providerUtils.InvalidRequestErrorf("Sarvam does not support box questions")
		}
	}
	return typesafe.ToTypesafeDecisionRequest(request)
}

// Decision answers a decision request on Sarvam's /v2/systemone. Sarvam bills
// input tokens only; the response's output_tokens is always 0.
func (provider *SarvamProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToSarvamDecisionRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, sarvamDecisionPath))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for name, value := range openai.BearerAuthHeader(key) {
		req.Header.Set(name, value)
	}
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.Sarvam) {
		req.SetBody(jsonData)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, openai.ParseOpenAIError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var decisionResp typesafe.TypesafeDecisionResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &decisionResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp, bifrostErr := typesafe.ToBifrostDecisionResponse(&decisionResp, request)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResp, nil
}
