package sgl

import (
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// sglDecisionPath is where SGLang serves decision models in TypeSafe's System One shape.
const sglDecisionPath = "/v1/systemone"

func ToSGLDecisionRequest(request *schemas.BifrostDecisionRequest) (*SGLDecisionRequest, error) {
	if request.Input.HasAssetParts() {
		return nil, providerUtils.InvalidRequestErrorf("decision input carries an image sent by reference, which this provider cannot read; route the request to a provider that accepts it")
	}
	if request.Input.IsEmpty() {
		return nil, providerUtils.InvalidRequestErrorf("decision input is required")
	}
	input, images, err := providerUtils.SplitDecisionImages(request.Input)
	if err != nil {
		return nil, err
	}
	textOnly := *request
	textOnly.Input = input
	native, err := typesafe.ToTypesafeDecisionRequest(&textOnly)
	if err != nil {
		return nil, err
	}
	return &SGLDecisionRequest{TypesafeDecisionRequest: native, Images: images}, nil
}

// parseSGLDecisionError builds the error from an SGLang error reply: a 422 validation
// {"detail":[{loc,msg}]}, a 400 {"code","message"}, or a 401 {"error":"Unauthorized"}.
func parseSGLDecisionError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := openai.ParseOpenAIError(resp)
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return bifrostErr
	}
	message := ""
	if value := gjson.GetBytes(body, "message"); value.Type == gjson.String {
		message = value.String()
	}
	if message == "" {
		if value := gjson.GetBytes(body, "error"); value.Type == gjson.String {
			message = value.String()
		}
	}
	if message == "" {
		message = describeDetail(gjson.GetBytes(body, "detail"))
	}
	if message != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = message
	}
	return bifrostErr
}

// describeDetail flattens a validation detail: a string as is, an array of {loc, msg}
// entries as "path: msg; path: msg" with the leading "body" segment dropped.
func describeDetail(detail gjson.Result) string {
	switch detail.Type {
	case gjson.String:
		return detail.String()
	case gjson.JSON:
		if !detail.IsArray() {
			return ""
		}
		parts := make([]string, 0, 2)
		detail.ForEach(func(_, entry gjson.Result) bool {
			msg := entry.Get("msg").String()
			if msg == "" {
				return true
			}
			var loc []string
			entry.Get("loc").ForEach(func(_, segment gjson.Result) bool {
				if segment.Type == gjson.String && segment.String() != "body" {
					loc = append(loc, segment.String())
				}
				return true
			})
			if len(loc) > 0 {
				parts = append(parts, strings.Join(loc, ".")+": "+msg)
			} else {
				parts = append(parts, msg)
			}
			return true
		})
		return strings.Join(parts, "; ")
	}
	return ""
}

// Decision answers a decision request on SGLang's /v1/systemone. Every model is sent;
// one SGLang does not serve, or cannot read decisions from, comes back as its own error.
func (provider *SGLProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToSGLDecisionRequest(request)
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
	req.SetRequestURI(baseURL + providerUtils.GetPathFromContext(ctx, sglDecisionPath))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for name, value := range openai.BearerAuthHeader(key) {
		req.Header.Set(name, value)
	}
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.SGL) {
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
		return nil, providerUtils.EnrichError(ctx, parseSGLDecisionError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
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