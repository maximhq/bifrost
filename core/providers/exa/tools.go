package exa

import (
	"net/http"
	"strconv"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Search runs a web search through Exa's /search.
//
// Every neutral filter has an Exa equivalent, so only the result bound is checked here. Exa
// prices the call itself and reports it in costDollars, which is attached as the call's cost.
func (provider *ExaProvider) Search(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSearchRequest) (*schemas.BifrostSearchResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Exa, nil, schemas.SearchRequest); err != nil {
		return nil, err
	}
	if request.MaxResults != nil && *request.MaxResults > exaMaxResults {
		return nil, providerUtils.NewBifrostBadRequestError("max_results must be between 1 and 100")
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			body := &exaSearchBody{
				Query:              request.Query,
				NumResults:         request.MaxResults,
				IncludeDomains:     request.IncludeDomains,
				ExcludeDomains:     request.ExcludeDomains,
				StartPublishedDate: request.PublishedAfter,
				EndPublishedDate:   request.PublishedBefore,
				ExtraParams:        request.ExtraParams,
			}
			if request.IncludeContent != nil && *request.IncludeContent {
				body.Contents = &exaContentsOptions{Text: true}
			}
			return body, nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	responseBody, latency, providerResponseHeaders, bifrostErr := provider.postRequest(ctx, key, "/search", jsonBody)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	var wire exaSearchWireResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(
		responseBody,
		&wire,
		jsonBody,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	results := make([]schemas.BifrostSearchResult, 0, len(wire.Results))
	for _, result := range wire.Results {
		results = append(results, result.toBifrost())
	}
	response := &schemas.BifrostSearchResponse{
		Results: results,
		Usage:   costUsage(wire.CostDollars),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:             schemas.SearchRequest,
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerResponseHeaders,
		},
	}
	attachRawFields(&response.ExtraFields, rawRequest, rawResponse)
	return response, nil
}

// Fetch retrieves one page's text through Exa's /contents.
//
// /contents answers 200 even when the page could not be read and reports the failure in
// statuses, so a failed page is turned into an error here with the status Exa gives for it.
// The page is read by Exa's crawler, not by Bifrost, so the gateway's private-network
// safeguards do not apply.
func (provider *ExaProvider) Fetch(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFetchRequest) (*schemas.BifrostFetchResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Exa, nil, schemas.FetchRequest); err != nil {
		return nil, err
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return &exaContentsBody{URLs: []string{request.URL}, Text: true, ExtraParams: request.ExtraParams}, nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	responseBody, latency, providerResponseHeaders, bifrostErr := provider.postRequest(ctx, key, "/contents", jsonBody)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	var wire exaContentsWireResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(
		responseBody,
		&wire,
		jsonBody,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	for _, status := range wire.Statuses {
		if status.Status != "error" {
			continue
		}
		// Exa answers 200 and reports the page's own failure in statuses, so the page's status
		// is not this call's status: a page that answered 401 or 429 upstream says nothing
		// about Exa credentials or rate limits. It stays in the message, only a missing page
		// maps to 404, and everything else is a bad gateway.
		statusCode, tag, pageStatus := fasthttp.StatusBadGateway, "unknown", ""
		if status.Error != nil {
			tag = status.Error.Tag
			if code := status.Error.HTTPStatusCode; code != nil {
				pageStatus = " (page status " + strconv.Itoa(*code) + ")"
				if *code == fasthttp.StatusNotFound || *code == fasthttp.StatusGone {
					statusCode = fasthttp.StatusNotFound
				}
			}
		}
		return nil, providerUtils.SetErrorLatency(
			providerUtils.NewProviderAPIError("exa could not fetch "+status.ID+": "+tag+pageStatus, nil, statusCode, schemas.Ptr(tag), nil),
			latency,
		)
	}
	if len(wire.Results) == 0 {
		return nil, providerUtils.SetErrorLatency(
			providerUtils.NewProviderAPIError("exa returned no page for "+request.URL, nil, fasthttp.StatusBadGateway, nil, nil),
			latency,
		)
	}

	response := &schemas.BifrostFetchResponse{
		Title:   wire.Results[0].Title,
		Content: wire.Results[0].Text,
		Usage:   costUsage(wire.CostDollars),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:             schemas.FetchRequest,
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerResponseHeaders,
		},
	}
	attachRawFields(&response.ExtraFields, rawRequest, rawResponse)
	return response, nil
}

// postRequest performs one POST against Exa's API, authenticating with the key in x-api-key.
//
// Non-200 responses are parsed with the shared OpenAI-compatible error parser, which keeps the
// upstream status code: 402 means the account is out of credit and 429 a rate limit, and
// flattening either into a generic provider error would hide the fix.
func (provider *ExaProvider) postRequest(ctx *schemas.BifrostContext, key schemas.Key, path string, body []byte) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, path))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("x-api-key", value)
	}
	req.SetBody(body)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, latency, nil, bifrostErr
	}

	// Headers are extracted before the status check so an error response still forwards them.
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, latency, providerResponseHeaders, providerUtils.SetErrorLatency(openai.ParseOpenAIError(resp), latency)
	}

	// The response goes back to fasthttp's pool when this returns, so the body is decoded into
	// one this call owns.
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, latency, providerResponseHeaders, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}
	return body, latency, providerResponseHeaders, nil
}
