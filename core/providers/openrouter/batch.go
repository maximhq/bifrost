package openrouter

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const openRouterBatchesPath = "/v1/batches"

// ToOpenRouterBatchRequest converts inline items without uploading an input file.
func ToOpenRouterBatchRequest(request *schemas.BifrostBatchCreateRequest) (*OpenRouterBatchRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("batch create request is nil")
	}
	if request.Model == nil || strings.TrimSpace(*request.Model) == "" {
		return nil, fmt.Errorf("model is required for OpenRouter batches")
	}
	switch request.Endpoint {
	case schemas.BatchEndpointChatCompletions, schemas.BatchEndpointResponses, schemas.BatchEndpointMessages, schemas.BatchEndpointEmbeddings:
	default:
		return nil, fmt.Errorf("unsupported OpenRouter batch endpoint %q", request.Endpoint)
	}
	if request.InputFileID != "" || request.InputBlob != nil || request.OutputFolder != nil {
		return nil, fmt.Errorf("OpenRouter batches require inline requests, not input files or blobs")
	}
	if len(request.Requests) == 0 {
		return nil, fmt.Errorf("requests must not be empty")
	}
	if len(request.Metadata) != 0 || request.DisplayName != nil || request.OutputExpiresAfter != nil {
		return nil, fmt.Errorf("OpenRouter batches do not support metadata, display_name, or output_expires_after")
	}
	window := request.CompletionWindow
	if window == "" {
		window = "24h"
	}
	if window != "24h" {
		return nil, fmt.Errorf("OpenRouter batch completion_window must be 24h")
	}
	native := &OpenRouterBatchRequest{
		Endpoint: request.Endpoint, Model: *request.Model, CompletionWindow: window,
		Requests: make([]OpenRouterBatchRequestItem, len(request.Requests)),
	}
	seen := make(map[string]struct{}, len(request.Requests))
	for i, item := range request.Requests {
		if strings.TrimSpace(item.CustomID) == "" {
			return nil, fmt.Errorf("requests[%d].custom_id must not be empty", i)
		}
		if _, exists := seen[item.CustomID]; exists {
			return nil, fmt.Errorf("duplicate custom_id %q", item.CustomID)
		}
		seen[item.CustomID] = struct{}{}
		if item.Method != "" && item.Method != http.MethodPost {
			return nil, fmt.Errorf("request %q must use POST", item.CustomID)
		}
		if item.URL != "" && item.URL != string(request.Endpoint) {
			return nil, fmt.Errorf("request %q must use the batch endpoint", item.CustomID)
		}
		body := item.Body
		if body == nil {
			body = item.Params
		} else if item.Params != nil {
			return nil, fmt.Errorf("request %q must not specify both body and params", item.CustomID)
		}
		if len(body) == 0 {
			return nil, fmt.Errorf("request %q requires a body", item.CustomID)
		}
		if model, exists := body["model"]; exists && model != native.Model {
			return nil, fmt.Errorf("request %q model must match the batch model", item.CustomID)
		}
		native.Requests[i] = OpenRouterBatchRequestItem{CustomID: item.CustomID, Body: body}
	}
	for name, value := range request.ExtraParams {
		switch name {
		case "provider":
			options, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s must be a provider options object", name)
			}
			if request.Endpoint == schemas.BatchEndpointEmbeddings {
				return nil, fmt.Errorf("OpenRouter embeddings batches do not support provider options")
			}
			for option, setting := range options {
				switch option {
				case "only":
					slugs, ok := schemas.SafeExtractStringSlice(setting)
					if !ok || len(slugs) == 0 {
						return nil, fmt.Errorf("provider.only requires a nonempty array of provider slugs")
					}
					for _, slug := range slugs {
						if strings.TrimSpace(slug) == "" {
							return nil, fmt.Errorf("provider.only requires nonempty strings")
						}
					}
				case "allow_fallbacks":
					if _, ok := setting.(bool); !ok {
						return nil, fmt.Errorf("provider.allow_fallbacks must be a boolean")
					}
				default:
					return nil, fmt.Errorf("unsupported OpenRouter batch provider option %q", option)
				}
			}
			native.Provider = options
		case "callback_url":
			callback, ok := value.(string)
			parsed, err := url.Parse(callback)
			if !ok || err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				return nil, fmt.Errorf("callback_url must be an HTTPS URL")
			}
			native.CallbackURL = callback
		default:
			return nil, fmt.Errorf("unsupported OpenRouter batch parameter %q", name)
		}
	}
	return native, nil
}

// requestBatch sends a native batch operation through the provider's unary client.
func (provider *OpenRouterProvider) requestBatch(ctx *schemas.BifrostContext, key schemas.Key, method, path string, body []byte, out any, successStatus int) (schemas.BifrostResponseExtraFields, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(provider.networkConfig.BaseURL + path)
	req.Header.SetMethod(method)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	if body != nil {
		req.SetBody(body)
	}
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	rawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	rawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return schemas.BifrostResponseExtraFields{}, providerUtils.EnrichError(ctx, bifrostErr, body, nil, rawRequest, rawResponse, latency)
	}
	if resp.StatusCode() != successStatus {
		errorBody := resp.Body()
		if rawResponse {
			// Invalid JSON is retained as-is by EnrichError; detach it from the pool.
			errorBody = append([]byte(nil), errorBody...)
		}
		return schemas.BifrostResponseExtraFields{}, providerUtils.EnrichError(ctx, parseOpenRouterError(resp), body, errorBody, rawRequest, rawResponse, latency)
	}
	decoded, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return schemas.BifrostResponseExtraFields{}, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), body, nil, rawRequest, rawResponse, latency)
	}
	rawReq, rawResp, bifrostErr := providerUtils.HandleProviderResponse(decoded, &out, body, rawRequest, rawResponse)
	if bifrostErr != nil {
		return schemas.BifrostResponseExtraFields{}, providerUtils.EnrichError(ctx, bifrostErr, body, decoded, rawRequest, rawResponse, latency)
	}
	return schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds(), RawRequest: rawReq, RawResponse: rawResp, ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp)}, nil
}

// batchByKeys tries registered credentials, honoring an explicit pin. Lists return
// one workspace page rather than merging duplicates visible to every workspace key.
func (provider *OpenRouterProvider) batchByKeys(ctx *schemas.BifrostContext, keys []schemas.Key, method, path string, out any) (schemas.BifrostResponseExtraFields, *schemas.BifrostError) {
	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if id, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); id != "" {
			if id != key.ID {
				continue
			}
		} else if name, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyName).(string); name != "" && name != key.Name {
			continue
		}
		extra, err := provider.requestBatch(ctx, key, method, path, nil, out, http.StatusOK)
		if err == nil {
			ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, key.ID)
			ctx.SetValue(schemas.BifrostContextKeySelectedKeyName, key.Name)
			return extra, nil
		}
		// Try another credential only for credential/resource visibility failures.
		// Do not fan out an outage or retry a partially decoded response into out.
		if err.StatusCode == nil || (*err.StatusCode != http.StatusUnauthorized && *err.StatusCode != http.StatusForbidden && *err.StatusCode != http.StatusNotFound) {
			return schemas.BifrostResponseExtraFields{}, err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = providerUtils.NewBifrostOperationError("no eligible batch credential found", nil)
	}
	return schemas.BifrostResponseExtraFields{}, lastErr
}

// BatchCreate submits a native inline batch and accepts asynchronous validation.
func (provider *OpenRouterProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	native, err := ToOpenRouterBatchRequest(request)
	if err != nil {
		return nil, providerUtils.NewBifrostBadRequestError(err.Error())
	}
	body, err := providerUtils.MarshalSorted(native)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	var result schemas.BifrostBatchCreateResponse
	extra, bifrostErr := provider.requestBatch(ctx, key, http.MethodPost, openRouterBatchesPath, body, &result, http.StatusAccepted)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if result.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("OpenRouter returned an empty batch id", nil)
	}
	result.ExtraFields = extra
	return &result, nil
}

// BatchList lists one upstream workspace page using an eligible credential.
func (provider *OpenRouterProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostBadRequestError("batch list request is nil")
	}
	if request.Limit < 0 || request.Limit > 100 {
		return nil, providerUtils.NewBifrostBadRequestError("limit must be between 1 and 100")
	}
	if request.BeforeID != nil || request.AfterID != nil || request.PageToken != nil || request.NextCursor != nil || request.PageSize != 0 {
		return nil, providerUtils.NewBifrostBadRequestError("OpenRouter batches use after pagination")
	}
	query := url.Values{}
	if request.Limit > 0 {
		query.Set("limit", strconv.Itoa(request.Limit))
	}
	if request.After != nil {
		if *request.After == "" {
			return nil, providerUtils.NewBifrostBadRequestError("after must not be empty")
		}
		query.Set("after", *request.After)
	}
	for name, value := range request.ExtraParams {
		switch name {
		case "status":
			statuses, ok := schemas.SafeExtractStringSlice(value)
			if status, isString := value.(string); isString {
				statuses, ok = []string{status}, true
			}
			if !ok {
				return nil, providerUtils.NewBifrostBadRequestError("status must contain strings")
			}
			for _, status := range statuses {
				switch schemas.BatchStatus(status) {
				case schemas.BatchStatusValidating, schemas.BatchStatusInProgress, schemas.BatchStatusCompleted, schemas.BatchStatusFailed, schemas.BatchStatusExpired, schemas.BatchStatusCancelled:
					query.Add("status", status)
				default:
					return nil, providerUtils.NewBifrostBadRequestError("unsupported batch status filter")
				}
			}
		case "created_after", "created_before":
			value, ok := value.(string)
			if !ok {
				return nil, providerUtils.NewBifrostBadRequestError(name + " must be a timestamp string")
			}
			query.Set(name, value)
		default:
			return nil, providerUtils.NewBifrostBadRequestError("unsupported batch list parameter " + name)
		}
	}
	path := openRouterBatchesPath
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var result schemas.BifrostBatchListResponse
	extra, err := provider.batchByKeys(ctx, keys, http.MethodGet, path, &result)
	if err != nil {
		return nil, err
	}
	result.ExtraFields = extra
	return &result, nil
}

// BatchRetrieve retrieves lifecycle state, aggregate usage and inline results.
func (provider *OpenRouterProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostBadRequestError("batch retrieve request is nil")
	}
	id, err := providerUtils.EscapeResourceID(request.BatchID, "batch_id")
	if err != nil {
		return nil, err
	}
	var result schemas.BifrostBatchRetrieveResponse
	extra, err := provider.batchByKeys(ctx, keys, http.MethodGet, openRouterBatchesPath+"/"+id, &result)
	if err != nil {
		return nil, err
	}
	if result.ID != request.BatchID {
		return nil, providerUtils.NewBifrostOperationError("OpenRouter returned a mismatched batch id", nil)
	}
	result.ExtraFields = extra
	return &result, nil
}

// BatchResults reads terminal results from the upstream batch resource.
func (provider *OpenRouterProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostBadRequestError("batch results request is nil")
	}
	batch, err := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{BatchID: request.BatchID})
	if err != nil {
		return nil, err
	}
	return ToBifrostBatchResultsResponse(batch)
}

// BatchDelete deletes a terminal batch; it does not cancel running work.
func (provider *OpenRouterProvider) BatchDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostBadRequestError("batch delete request is nil")
	}
	id, err := providerUtils.EscapeResourceID(request.BatchID, "batch_id")
	if err != nil {
		return nil, err
	}
	var result schemas.BifrostBatchDeleteResponse
	extra, err := provider.batchByKeys(ctx, keys, http.MethodDelete, openRouterBatchesPath+"/"+id, &result)
	if err != nil {
		return nil, err
	}
	if result.ID != request.BatchID || result.Deletion["openrouter"] != "deleted" {
		return nil, providerUtils.NewBifrostOperationError("invalid OpenRouter batch deletion response", nil)
	}
	result.Status = schemas.BatchStatusDeleted
	result.ExtraFields = extra
	return &result, nil
}

// ToBifrostBatchResultsResponse exposes terminal inline results without another GET.
func ToBifrostBatchResultsResponse(batch *schemas.BifrostBatchRetrieveResponse) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	switch batch.Status {
	case schemas.BatchStatusCompleted:
		if batch.Results == nil {
			return nil, &schemas.BifrostError{StatusCode: schemas.Ptr(http.StatusConflict), Error: &schemas.ErrorField{Message: "batch results are not ready yet"}}
		}
	case schemas.BatchStatusFailed, schemas.BatchStatusExpired, schemas.BatchStatusCancelled:
	default:
		return nil, &schemas.BifrostError{StatusCode: schemas.Ptr(http.StatusConflict), Error: &schemas.ErrorField{Message: "batch is still processing"}}
	}
	usage := batch.Usage
	// Rejected batches have no executed requests to charge.
	if usage == nil && batch.Status == schemas.BatchStatusFailed && batch.RequestCounts.Completed == 0 &&
		batch.RequestCounts.Failed == 0 && len(batch.Results) == 0 {
		usage = &schemas.BatchUsage{BifrostLLMUsage: schemas.BifrostLLMUsage{Cost: &schemas.BifrostCost{}}}
	}
	return &schemas.BifrostBatchResultsResponse{
		BatchID: batch.ID, Model: batch.Model, Status: batch.Status,
		Endpoint: schemas.BatchEndpoint(batch.Endpoint), Usage: usage, RequestCounts: &batch.RequestCounts,
		Error: batch.Error, Results: batch.Results, ExtraFields: batch.ExtraFields,
	}, nil
}
