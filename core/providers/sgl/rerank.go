package sgl

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// sglRerankRequest is the wire body for sglang's /v1/rerank endpoint
// (V1RerankReqInput upstream).
//
// IMPORTANT: sglang's V1RerankReqInput rejects unknown fields, including
// `model`, so we intentionally omit it here. Adding a model field will cause
// sglang to return a 400 with "extra fields not permitted".
type sglRerankRequest struct {
	Query           string                 `json:"query"`
	Documents       []string               `json:"documents"`
	TopN            *int                   `json:"top_n,omitempty"`
	ReturnDocuments *bool                  `json:"return_documents,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

// GetExtraParams returns passthrough parameters for providerUtils.CheckContextAndGetRequestBody.
func (r *sglRerankRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// ToSGLRerankRequest converts a Bifrost rerank request into sglang's wire format.
//
// The outgoing body intentionally omits `model`; sglang's /v1/rerank
// (V1RerankReqInput) does not accept it.
func ToSGLRerankRequest(bifrostReq *schemas.BifrostRerankRequest) *sglRerankRequest {
	if bifrostReq == nil {
		return nil
	}

	sglReq := &sglRerankRequest{
		Query:     bifrostReq.Query,
		Documents: make([]string, len(bifrostReq.Documents)),
	}
	for i, doc := range bifrostReq.Documents {
		sglReq.Documents[i] = doc.Text
	}

	if bifrostReq.Params != nil {
		sglReq.TopN = bifrostReq.Params.TopN
		sglReq.ReturnDocuments = bifrostReq.Params.ReturnDocuments
		sglReq.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return sglReq
}

// ToBifrostRerankResponse converts sglang's bare-array rerank response payload
// to Bifrost format.
//
// sglang returns a bare JSON array: [{score, document, index}, ...] — NOT
// wrapped in {"results": [...]} like vLLM/Cohere. The score field is "score"
// (not "relevance_score").
func ToBifrostRerankResponse(items []interface{}, documents []schemas.RerankDocument, returnDocuments bool) (*schemas.BifrostRerankResponse, error) {
	if items == nil {
		return nil, fmt.Errorf("sgl rerank response is nil")
	}

	response := &schemas.BifrostRerankResponse{}
	seenIndices := make(map[int]struct{}, len(items))
	response.Results = make([]schemas.RerankResult, 0, len(items))

	for _, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid sgl rerank response: result item must be an object")
		}

		index, ok := schemas.SafeExtractInt(itemMap["index"])
		if !ok {
			return nil, fmt.Errorf("invalid sgl rerank response: result index is required")
		}
		if index < 0 || index >= len(documents) {
			return nil, fmt.Errorf("invalid sgl rerank response: result index %d out of range", index)
		}
		if _, exists := seenIndices[index]; exists {
			return nil, fmt.Errorf("invalid sgl rerank response: duplicate index %d", index)
		}
		seenIndices[index] = struct{}{}

		score, ok := schemas.SafeExtractFloat64(itemMap["score"])
		if !ok {
			return nil, fmt.Errorf("invalid sgl rerank response: score is required")
		}

		result := schemas.RerankResult{
			Index:          index,
			RelevanceScore: score,
		}

		if returnDocuments {
			doc := documents[index]
			result.Document = &doc
		}

		response.Results = append(response.Results, result)
	}

	sort.SliceStable(response.Results, func(i, j int) bool {
		if response.Results[i].RelevanceScore == response.Results[j].RelevanceScore {
			return response.Results[i].Index < response.Results[j].Index
		}
		return response.Results[i].RelevanceScore > response.Results[j].RelevanceScore
	})

	return response, nil
}

// callSGLRerankEndpoint POSTs to sglang's /v1/rerank and decodes the bare-array
// response. sglang only serves /v1/rerank, so unlike vLLM there is no
// /rerank fallback path.
func (provider *SGLProvider) callSGLRerankEndpoint(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	endpointPath string,
	jsonData []byte,
) ([]interface{}, []byte, time.Duration, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, nil, 0, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(baseURL + endpointPath)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.SGL) {
		req.SetBody(jsonData)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, nil, latency, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, rawErrBody, latency, ParseSGLError(resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, rawErrBody, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	var items []interface{}
	if err := sonic.Unmarshal(body, &items); err != nil {
		return nil, body, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}

	return items, body, latency, nil
}

// Rerank performs a rerank request to sglang's /v1/rerank endpoint.
func (provider *SGLProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	// Opt into ExtraParams pass-through so caller-supplied request.Params.ExtraParams
	// are merged into the outgoing JSON body. Mirrors vLLM's Rerank wiring.
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToSGLRerankRequest(request), nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	resolvedPath := providerUtils.GetPathFromContext(ctx, "")
	if resolvedPath == "" {
		resolvedPath = "/v1/rerank"
	} else if !strings.HasPrefix(resolvedPath, "/") {
		resolvedPath = "/" + resolvedPath
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	items, responseBody, latency, bifrostErr := provider.callSGLRerankEndpoint(ctx, key, resolvedPath, jsonData)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	returnDocuments := request.Params != nil && request.Params.ReturnDocuments != nil && *request.Params.ReturnDocuments
	bifrostResponse, err := ToBifrostRerankResponse(items, request.Documents, returnDocuments)
	if err != nil {
		return nil, providerUtils.EnrichError(
			ctx,
			providerUtils.NewBifrostOperationError("error converting rerank response", err),
			jsonData,
			responseBody,
			sendBackRawRequest,
			sendBackRawResponse,
		)
	}

	// Keep requested model as the canonical model in Bifrost response,
	// since sglang's bare-array response does not include one.
	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	if sendBackRawRequest {
		var rawReq interface{}
		if err := sonic.Unmarshal(jsonData, &rawReq); err == nil {
			bifrostResponse.ExtraFields.RawRequest = rawReq
		}
	}
	if sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = items
	}

	return bifrostResponse, nil
}
