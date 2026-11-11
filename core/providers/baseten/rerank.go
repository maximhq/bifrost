package baseten

import (
	"net/http"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// basetenRerankPath is BEI's rerank route under a deployment's /sync base.
const basetenRerankPath = "/rerank"

// BasetenRerankRequest is the body BEI rerank deployments read on /sync/rerank.
// Fields such as raw_scores, truncate and truncation_direction go through extra
// params with passthrough on.
type BasetenRerankRequest struct {
	Query       string         `json:"query"`
	Texts       []string       `json:"texts"`
	ExtraParams map[string]any `json:"-"`
}

// GetExtraParams returns the params merged into the body when passthrough is on.
func (r *BasetenRerankRequest) GetExtraParams() map[string]any {
	return r.ExtraParams
}

// basetenRerankResult is one entry of BEI's rerank reply: the document's position
// in texts and its score.
type basetenRerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// ToBasetenRerankRequest converts a rerank request to BEI's body: the query and
// each document's text, in order.
func ToBasetenRerankRequest(request *schemas.BifrostRerankRequest) (*BasetenRerankRequest, error) {
	if strings.TrimSpace(request.Query) == "" {
		return nil, providerUtils.InvalidRequestErrorf("baseten rerank needs a query")
	}
	if len(request.Documents) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("baseten rerank needs at least one document")
	}
	texts := make([]string, len(request.Documents))
	for i, document := range request.Documents {
		texts[i] = document.Text
	}
	body := &BasetenRerankRequest{Query: request.Query, Texts: texts}
	if request.Params != nil {
		body.ExtraParams = request.Params.ExtraParams
	}
	return body, nil
}

// ToBifrostRerankResponse maps BEI's results to Bifrost's, highest score first.
// BEI has no top_n or return_documents, so top_n trims the sorted list and
// return_documents attaches each request document by index.
func ToBifrostRerankResponse(results []basetenRerankResult, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	limit := len(results)
	returnDocuments := false
	if request.Params != nil {
		if request.Params.TopN != nil && *request.Params.TopN >= 0 && *request.Params.TopN < limit {
			limit = *request.Params.TopN
		}
		returnDocuments = request.Params.ReturnDocuments != nil && *request.Params.ReturnDocuments
	}
	out := &schemas.BifrostRerankResponse{Model: request.Model, Results: make([]schemas.RerankResult, 0, limit)}
	for _, result := range results[:limit] {
		if result.Index < 0 || result.Index >= len(request.Documents) {
			return nil, providerUtils.NewBifrostOperationError("baseten rerank returned an index outside the documents sent", nil)
		}
		entry := schemas.RerankResult{Index: result.Index, RelevanceScore: result.Score, ID: request.Documents[result.Index].ID}
		if returnDocuments {
			document := request.Documents[result.Index]
			entry.Document = &document
		}
		out.Results = append(out.Results, entry)
	}
	return out, nil
}

// rerankURL derives BEI's rerank URL from an alias deployment_url. The documented
// deployment_url is the OpenAI-compatible /sync/v1 base, and rerank lives beside
// /v1 under /sync, so a trailing /v1 is dropped first.
func rerankURL(ctx *schemas.BifrostContext, deploymentBase string) string {
	return strings.TrimSuffix(deploymentBase, "/v1") + providerUtils.GetPathFromContext(ctx, basetenRerankPath)
}

// Rerank scores documents against a query on a BEI rerank deployment's
// /sync/rerank. Model APIs has no rerank route, so the alias needs a
// deployment_url.
func (provider *BasetenProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.dedicatedURLOrError(ctx, schemas.RerankRequest)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (*BasetenRerankRequest, error) {
		return ToBasetenRerankRequest(request)
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(rerankURL(ctx, baseURL))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for k, v := range openai.BearerAuthHeader(key) {
		req.Header.Set(k, v)
	}
	req.SetBody(jsonData)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseBasetenDeploymentError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}
	var results []basetenRerankResult
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &results, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}
	bifrostResp, bifrostErr := ToBifrostRerankResponse(results, request)
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
