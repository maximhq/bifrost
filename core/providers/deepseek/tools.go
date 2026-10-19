package deepseek

import (
	"net/http"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// DeepSeek bills Search and Fetch at a flat $0.75 per 1,000 successful requests, counted
// per successful request rather than per result row, and nothing is billed on failure.
//
// The provider reports the cost on the response instead of relying on a catalog pricing
// row, because these calls carry no model and the pricing store is keyed by model. A
// fixed fee per call is exactly what a provider-reported cost is for, and it keeps the
// feature working on a deployment whose catalog knows nothing about these operations.
const (
	deepseekSearchCostPerRequest = 0.00075
	deepseekFetchCostPerRequest  = 0.00075
)

// deepseekSearchBody is the wire shape of a DeepSeek Search request.
//
// ExtraParams is never serialized as its own field; CheckContextAndGetRequestBody merges
// it into the JSON when passthrough extra params are enabled, matching every other
// provider. Unknown fields are safe to send upstream, which DeepSeek documents it ignores.
type deepseekSearchBody struct {
	Query        string                 `json:"query"`
	Count        *int                   `json:"count,omitempty"`
	OriQuestion  *string                `json:"ori_question,omitempty"`
	Localization *string                `json:"localization,omitempty"`
	NeedContent  *bool                  `json:"need_content,omitempty"`
	ExtraParams  map[string]interface{} `json:"-"`
}

// GetExtraParams returns the caller-supplied passthrough parameters.
func (b *deepseekSearchBody) GetExtraParams() map[string]interface{} { return b.ExtraParams }

// deepseekFetchBody is the wire shape of a DeepSeek Fetch request.
type deepseekFetchBody struct {
	URL         string                 `json:"url"`
	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams returns the caller-supplied passthrough parameters.
func (b *deepseekFetchBody) GetExtraParams() map[string]interface{} { return b.ExtraParams }

// deepseekSearchWireResponse is the upstream Search envelope.
type deepseekSearchWireResponse struct {
	Results []schemas.BifrostSearchResult `json:"results"`
}

// deepseekFetchWireResponse is the upstream Fetch envelope.
type deepseekFetchWireResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// Search runs a web search through DeepSeek's Search API.
//
// The request carries no model: Search is a web call, not an inference call, so nothing
// is resolved or priced through the model catalog. The cost of the call is reported on the
// response for the cost engine, and the raw query and result count are the only request
// facts worth logging, because `need_content` can return whole pages.
func (provider *DeepSeekProvider) Search(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSearchRequest) (*schemas.BifrostSearchResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.DeepSeek, nil, schemas.SearchRequest); err != nil {
		return nil, err
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return &deepseekSearchBody{
				Query:        request.Query,
				Count:        request.Count,
				OriQuestion:  request.OriQuestion,
				Localization: request.Localization,
				NeedContent:  request.NeedContent,
				ExtraParams:  request.ExtraParams,
			}, nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	responseBody, latency, providerResponseHeaders, bifrostErr := provider.postToolsRequest(ctx, key, "/v1/tools/search", jsonBody)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// A Search that finds nothing returns an empty results array, which is a successful
	// answer and still billed. Only the envelope failing to decode is an error here.
	wire := deepseekSearchWireResponse{}
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

	results := wire.Results
	if results == nil {
		results = []schemas.BifrostSearchResult{}
	}

	response := &schemas.BifrostSearchResponse{
		Results: results,
		Usage:   toolsUsage(deepseekSearchCostPerRequest),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:             schemas.SearchRequest,
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerResponseHeaders,
		},
	}
	attachToolsRawFields(&response.ExtraFields, rawRequest, rawResponse)
	return response, nil
}

// Fetch retrieves one page as Markdown through DeepSeek's Fetch API.
//
// The URL is read by DeepSeek's servers, not by Bifrost, so the gateway's private-network
// safeguards do not apply; the only thing a caller controls is what DeepSeek is asked to
// read. A rejected page surfaces as the upstream 403 rather than being flattened.
func (provider *DeepSeekProvider) Fetch(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFetchRequest) (*schemas.BifrostFetchResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.DeepSeek, nil, schemas.FetchRequest); err != nil {
		return nil, err
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return &deepseekFetchBody{
				URL:         request.URL,
				ExtraParams: request.ExtraParams,
			}, nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	responseBody, latency, providerResponseHeaders, bifrostErr := provider.postToolsRequest(ctx, key, "/v1/tools/fetch", jsonBody)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Fetch decodes loosely: an empty title is normal, and the content is Markdown that
	// must survive byte-for-byte, so HandleProviderResponse is the single decode here.
	var wire deepseekFetchWireResponse
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

	response := &schemas.BifrostFetchResponse{
		Title:   wire.Title,
		Content: wire.Content,
		Usage:   toolsUsage(deepseekFetchCostPerRequest),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:             schemas.FetchRequest,
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerResponseHeaders,
		},
	}
	attachToolsRawFields(&response.ExtraFields, rawRequest, rawResponse)
	return response, nil
}

// postToolsRequest performs one POST against DeepSeek's tools API.
//
// Non-200 responses are parsed with the shared OpenAI-compatible error parser, which keeps
// the upstream status code intact: 402 means "top up" and 403 means "fetch rejected by
// policy", and flattening either into a generic provider error would hide the fix.
func (provider *DeepSeekProvider) postToolsRequest(ctx *schemas.BifrostContext, key schemas.Key, path string, body []byte) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, path))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
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

	return resp.Body(), latency, providerResponseHeaders, nil
}

// toolsUsage reports the flat per-request cost of one tools call.
func toolsUsage(costPerRequest float64) *schemas.BifrostLLMUsage {
	return &schemas.BifrostLLMUsage{
		Cost: &schemas.BifrostCost{TotalCost: costPerRequest},
	}
}

// attachToolsRawFields copies optional raw request and response bodies onto the extra
// fields, leaving them unset when the caller did not ask for them.
func attachToolsRawFields(extraFields *schemas.BifrostResponseExtraFields, rawRequest, rawResponse interface{}) {
	if extraFields == nil {
		return
	}
	if rawRequest != nil {
		extraFields.RawRequest = rawRequest
	}
	if rawResponse != nil {
		extraFields.RawResponse = rawResponse
	}
}
