package subq

import (
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	subqDecisionPath = "/v1/systemone"
	maxDecisionImages        = 4
)

// ToSubqDecisionRequest converts a Bifrost decision request into Subq's
// System One body. Images must be data: URIs (or raw base64 with an explicit
// media type); http(s) URLs and asset references are rejected.
func ToSubqDecisionRequest(request *schemas.BifrostDecisionRequest) (*SubqDecisionRequest, error) {
	if request.Input.HasAssetParts() {
		return nil, providerUtils.InvalidRequestErrorf("decision input carries an image sent by reference, which this provider cannot read; route the request to a provider that accepts it")
	}
	if request.Input.IsEmpty() {
		return nil, providerUtils.InvalidRequestErrorf("decision input is required")
	}
	input, imageURLs, err := providerUtils.SplitDecisionImages(request.Input)
	if err != nil {
		return nil, err
	}
	if len(imageURLs) > maxDecisionImages {
		return nil, providerUtils.InvalidRequestErrorf("subq decisions accept at most %d images", maxDecisionImages)
	}
	images := make([]SubqImage, 0, len(imageURLs))
	for i, url := range imageURLs {
		img, convErr := parseDecisionImage(url, i)
		if convErr != nil {
			return nil, convErr
		}
		images = append(images, img)
	}
	textOnly := *request
	textOnly.Input = input
	native, err := typesafe.ToTypesafeDecisionRequest(&textOnly)
	if err != nil {
		return nil, err
	}
	return &SubqDecisionRequest{TypesafeDecisionRequest: native, Images: images}, nil
}

// parseDecisionImage converts a Bifrost decision image URL into Subq's
// {mediaType, base64} shape. Only data: URIs are accepted.
func parseDecisionImage(url string, index int) (SubqImage, error) {
	trimmed := strings.TrimSpace(url)
	if trimmed == "" {
		return SubqImage{}, providerUtils.InvalidRequestErrorf("decision input carries an input_image part without an image_url")
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return SubqImage{}, providerUtils.InvalidRequestErrorf("subq decisions reject image urls (images/%d); send a data uri", index)
	}
	if !strings.HasPrefix(lower, "data:") {
		return SubqImage{}, providerUtils.InvalidRequestErrorf("subq decisions require a data uri for images/%d", index)
	}
	// data:[<mediatype>][;base64],<data>
	comma := strings.Index(trimmed, ",")
	if comma < 0 {
		return SubqImage{}, providerUtils.InvalidRequestErrorf("subq decisions require a data uri for images/%d", index)
	}
	meta := trimmed[len("data:"):comma]
	payload := trimmed[comma+1:]
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return SubqImage{}, providerUtils.InvalidRequestErrorf("subq decisions require base64 data uris for images/%d", index)
	}
	mediaType := strings.Split(meta, ";")[0]
	mediaType = strings.TrimSpace(mediaType)
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return SubqImage{}, providerUtils.InvalidRequestErrorf("subq decisions accept image/png, image/jpeg, or image/webp for images/%d", index)
	}
	if payload == "" {
		return SubqImage{}, providerUtils.InvalidRequestErrorf("subq decisions require base64 data for images/%d", index)
	}
	return SubqImage{MediaType: mediaType, Base64: payload}, nil
}

// Decision answers a decision request on Subq's /v1/systemone.
func (provider *SubqProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*SubqDecisionRequest, error) {
			return ToSubqDecisionRequest(request)
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
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, subqDecisionPath))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for name, value := range openai.BearerAuthHeader(key) {
		req.Header.Set(name, value)
	}
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.Subq) {
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
		return nil, providerUtils.EnrichError(ctx, parseSubqDecisionError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
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
