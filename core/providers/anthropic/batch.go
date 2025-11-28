package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Anthropic Batch API Types

// AnthropicBatchRequestItem represents a single request in a batch.
type AnthropicBatchRequestItem struct {
	CustomID string         `json:"custom_id"`
	Params   map[string]any `json:"params"`
}

// AnthropicBatchCreateRequest represents the request body for creating a batch.
type AnthropicBatchCreateRequest struct {
	Requests []AnthropicBatchRequestItem `json:"requests"`
}

// AnthropicBatchCancelRequest represents the request body for canceling a batch.
type AnthropicBatchCancelRequest struct {
	BatchID  string                `json:"batch_id"`
}

// AnthropicBatchRetrieveRequest represents the request body for retrieving a batch.
type AnthropicBatchRetrieveRequest struct {
	BatchID  string                `json:"batch_id"`	
}

// AnthropicBatchListRequest represents the request body for listing batches.
type AnthropicBatchListRequest struct {
	PageToken *string               `json:"page_token"`
	PageSize  int                   `json:"page_size"`
}

// AnthropicBatchResultsRequest represents the request body for retrieving batch results.
type AnthropicBatchResultsRequest struct {
	BatchID  string                `json:"batch_id"`
}

// AnthropicBatchResponse represents an Anthropic batch response.
type AnthropicBatchResponse struct {
	ID                string                       `json:"id"`
	Type              string                       `json:"type"`
	ProcessingStatus  string                       `json:"processing_status"`
	RequestCounts     *AnthropicBatchRequestCounts `json:"request_counts,omitempty"`
	EndedAt           *string                      `json:"ended_at,omitempty"`
	CreatedAt         string                       `json:"created_at"`
	ExpiresAt         string                       `json:"expires_at"`
	ArchivedAt        *string                      `json:"archived_at,omitempty"`
	CancelInitiatedAt *string                      `json:"cancel_initiated_at,omitempty"`
	ResultsURL        *string                      `json:"results_url,omitempty"`
}

// AnthropicBatchRequestCounts represents the request counts for a batch.
type AnthropicBatchRequestCounts struct {
	Processing int `json:"processing"`
	Succeeded  int `json:"succeeded"`
	Errored    int `json:"errored"`
	Canceled   int `json:"canceled"`
	Expired    int `json:"expired"`
}

// AnthropicBatchListResponse represents the response from listing batches.
type AnthropicBatchListResponse struct {
	Data    []AnthropicBatchResponse `json:"data"`
	HasMore bool                     `json:"has_more"`
	FirstID *string                  `json:"first_id,omitempty"`
	LastID  *string                  `json:"last_id,omitempty"`
}

// AnthropicBatchResultItem represents a single result from a batch.
type AnthropicBatchResultItem struct {
	CustomID string                   `json:"custom_id"`
	Result   AnthropicBatchResultData `json:"result"`
}

// AnthropicBatchResultData represents the result data.
type AnthropicBatchResultData struct {
	Type    string                 `json:"type"` // "succeeded", "errored", "expired", "canceled"
	Message map[string]interface{} `json:"message,omitempty"`
	Error   *AnthropicBatchError   `json:"error,omitempty"`
}

// AnthropicBatchError represents an error in batch results.
type AnthropicBatchError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ToBifrostBatchStatus converts Anthropic processing_status to Bifrost status.
func ToBifrostBatchStatus(status string) schemas.BatchStatus {
	switch status {
	case "in_progress":
		return schemas.BatchStatusInProgress
	case "canceling":
		return schemas.BatchStatusCancelling
	case "ended":
		return schemas.BatchStatusEnded
	default:
		return schemas.BatchStatus(status)
	}
}

// parseAnthropicTimestamp converts Anthropic ISO timestamp to Unix timestamp.
func parseAnthropicTimestamp(timestamp string) int64 {
	if timestamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// ToBifrostBatchCreateResponse converts Anthropic batch response to Bifrost batch create response.
func (r *AnthropicBatchResponse) ToBifrostBatchCreateResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawResponse bool, rawResponse interface{}) *schemas.BifrostBatchCreateResponse {
	resp := &schemas.BifrostBatchCreateResponse{
		ID:               r.ID,
		Object:           r.Type,
		Status:           ToBifrostBatchStatus(r.ProcessingStatus),
		ProcessingStatus: &r.ProcessingStatus,
		ResultsURL:       r.ResultsURL,
		CreatedAt:        parseAnthropicTimestamp(r.CreatedAt),
		ExpiresAt:        parseAnthropicTimestamp(r.ExpiresAt),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCreateRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if r.RequestCounts != nil {
		resp.RequestCounts = schemas.BatchRequestCounts{
			Total:     r.RequestCounts.Processing + r.RequestCounts.Succeeded + r.RequestCounts.Errored + r.RequestCounts.Canceled + r.RequestCounts.Expired,
			Completed: r.RequestCounts.Succeeded,
			Failed:    r.RequestCounts.Errored,
			Succeeded: r.RequestCounts.Succeeded,
			Expired:   r.RequestCounts.Expired,
			Canceled:  r.RequestCounts.Canceled,
			Pending:   r.RequestCounts.Processing,
		}
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}

// ToBifrostBatchRetrieveResponse converts Anthropic batch response to Bifrost batch retrieve response.
func (r *AnthropicBatchResponse) ToBifrostBatchRetrieveResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawResponse bool, rawResponse interface{}) *schemas.BifrostBatchRetrieveResponse {
	resp := &schemas.BifrostBatchRetrieveResponse{
		ID:               r.ID,
		Object:           r.Type,
		Status:           ToBifrostBatchStatus(r.ProcessingStatus),
		ProcessingStatus: &r.ProcessingStatus,
		ResultsURL:       r.ResultsURL,
		CreatedAt:        parseAnthropicTimestamp(r.CreatedAt),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	expiresAt := parseAnthropicTimestamp(r.ExpiresAt)
	if expiresAt > 0 {
		resp.ExpiresAt = &expiresAt
	}

	if r.EndedAt != nil {
		endedAt := parseAnthropicTimestamp(*r.EndedAt)
		resp.CompletedAt = &endedAt
	}

	if r.ArchivedAt != nil {
		archivedAt := parseAnthropicTimestamp(*r.ArchivedAt)
		resp.ArchivedAt = &archivedAt
	}

	if r.CancelInitiatedAt != nil {
		cancellingAt := parseAnthropicTimestamp(*r.CancelInitiatedAt)
		resp.CancellingAt = &cancellingAt
	}

	if r.RequestCounts != nil {
		resp.RequestCounts = schemas.BatchRequestCounts{
			Total:     r.RequestCounts.Processing + r.RequestCounts.Succeeded + r.RequestCounts.Errored + r.RequestCounts.Canceled + r.RequestCounts.Expired,
			Completed: r.RequestCounts.Succeeded,
			Failed:    r.RequestCounts.Errored,
			Succeeded: r.RequestCounts.Succeeded,
			Expired:   r.RequestCounts.Expired,
			Canceled:  r.RequestCounts.Canceled,
			Pending:   r.RequestCounts.Processing,
		}
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}

// BatchCreate creates a new batch job.
func (provider *AnthropicProvider) BatchCreate(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Anthropic does not support file-based batching
	if request.InputFileID != "" {
		// Here we should convert the input file to inline requests		
		return nil, providerUtils.NewBifrostOperationError("Anthropic batch API does not support input_file_id, use inline requests instead", nil, providerName)
	}

	if len(request.Requests) == 0 {
		return nil, providerUtils.NewBifrostOperationError("requests array is required for Anthropic batch API", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/messages/batches", schemas.BatchCreateRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Build request body
	anthropicReq := &AnthropicBatchCreateRequest{
		Requests: make([]AnthropicBatchRequestItem, len(request.Requests)),
	}

	for i, r := range request.Requests {
		anthropicReq.Requests[i] = AnthropicBatchRequestItem{
			CustomID: r.CustomID,
			Params:   r.Params,
		}
		// Use Body if Params is empty
		if anthropicReq.Requests[i].Params == nil && r.Body != nil {
			anthropicReq.Requests[i].Params = r.Body
		}
	}

	jsonData, err := sonic.Marshal(anthropicReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}
	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.BatchCreateRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var anthropicResp AnthropicBatchResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return anthropicResp.ToBifrostBatchCreateResponse(providerName, latency, sendBackRawResponse, rawResponse), nil
}

// BatchList lists batch jobs.
func (provider *AnthropicProvider) BatchList(ctx context.Context, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	url := provider.buildRequestURL(ctx, "/v1/messages/batches", schemas.BatchListRequest)
	hasParams := false
	if request.Limit > 0 {
		url += fmt.Sprintf("?limit=%d", request.Limit)
		hasParams = true
	}
	if request.BeforeID != nil {
		if hasParams {
			url += "&"
		} else {
			url += "?"
			hasParams = true
		}
		url += fmt.Sprintf("before_id=%s", *request.BeforeID)
	}
	if request.AfterID != nil {
		if hasParams {
			url += "&"
		} else {
			url += "?"
		}
		url += fmt.Sprintf("after_id=%s", *request.AfterID)
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Use first key if available
	if len(keys) > 0 && keys[0].Value != "" {
		req.Header.Set("x-api-key", keys[0].Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.BatchListRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var anthropicResp AnthropicBatchListResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostBatchListResponse{
		Object:  "list",
		FirstID: anthropicResp.FirstID,
		LastID:  anthropicResp.LastID,
		HasMore: anthropicResp.HasMore,
		Data:    make([]schemas.BifrostBatchRetrieveResponse, len(anthropicResp.Data)),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	for i, batch := range anthropicResp.Data {
		bifrostResp.Data[i] = *batch.ToBifrostBatchRetrieveResponse(providerName, 0, false, nil)
	}

	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// BatchRetrieve retrieves a specific batch job.
func (provider *AnthropicProvider) BatchRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/messages/batches/" + request.BatchID)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.BatchRetrieveRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var anthropicResp AnthropicBatchResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result := anthropicResp.ToBifrostBatchRetrieveResponse(providerName, latency, sendBackRawResponse, rawResponse)
	result.ExtraFields.RequestType = schemas.BatchRetrieveRequest
	return result, nil
}

// BatchCancel cancels a batch job.
func (provider *AnthropicProvider) BatchCancel(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/messages/batches/" + request.BatchID + "/cancel")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.BatchCancelRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var anthropicResp AnthropicBatchResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result := &schemas.BifrostBatchCancelResponse{
		ID:     anthropicResp.ID,
		Object: anthropicResp.Type,
		Status: ToBifrostBatchStatus(anthropicResp.ProcessingStatus),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCancelRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if anthropicResp.CancelInitiatedAt != nil {
		cancellingAt := parseAnthropicTimestamp(*anthropicResp.CancelInitiatedAt)
		result.CancellingAt = &cancellingAt
	}

	if anthropicResp.RequestCounts != nil {
		result.RequestCounts = schemas.BatchRequestCounts{
			Total:     anthropicResp.RequestCounts.Processing + anthropicResp.RequestCounts.Succeeded + anthropicResp.RequestCounts.Errored + anthropicResp.RequestCounts.Canceled + anthropicResp.RequestCounts.Expired,
			Completed: anthropicResp.RequestCounts.Succeeded,
			Failed:    anthropicResp.RequestCounts.Errored,
		}
	}

	if sendBackRawResponse {
		result.ExtraFields.RawResponse = rawResponse
	}

	return result, nil
}

// BatchResults retrieves batch results.
func (provider *AnthropicProvider) BatchResults(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/messages/batches/" + request.BatchID + "/results")
	req.Header.SetMethod(http.MethodGet)

	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseAnthropicError(resp, schemas.BatchResultsRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Parse JSONL content - each line is a separate result
	var results []schemas.BatchResultItem
	lines := splitJSONL(body)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var anthropicResult AnthropicBatchResultItem
		if err := sonic.Unmarshal(line, &anthropicResult); err != nil {
			provider.logger.Warn(fmt.Sprintf("failed to parse batch result line: %v", err))
			continue
		}

		// Convert to Bifrost format
		resultItem := schemas.BatchResultItem{
			CustomID: anthropicResult.CustomID,
			Result: &schemas.BatchResultData{
				Type:    anthropicResult.Result.Type,
				Message: anthropicResult.Result.Message,
			},
		}

		if anthropicResult.Result.Error != nil {
			resultItem.Error = &schemas.BatchResultError{
				Code:    anthropicResult.Result.Error.Type,
				Message: anthropicResult.Result.Error.Message,
			}
		}

		results = append(results, resultItem)
	}

	return &schemas.BifrostBatchResultsResponse{
		BatchID: request.BatchID,
		Results: results,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchResultsRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// splitJSONL splits JSONL content into individual lines.
func splitJSONL(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// ParseAnthropicError parses Anthropic error responses for batch operations.
func ParseAnthropicError(resp *fasthttp.Response, requestType schemas.RequestType, providerName schemas.ModelProvider, model string) *schemas.BifrostError {
	var errorResp AnthropicError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.Error.Type != "" {
		bifrostErr.Error.Type = &errorResp.Error.Type
	}
	if errorResp.Error.Message != "" {
		bifrostErr.Error.Message = errorResp.Error.Message
	}
	bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
		RequestType:    requestType,
		Provider:       providerName,
		ModelRequested: model,
	}
	return bifrostErr
}

// ToAnthropicBatchCreateResponse converts a Bifrost batch create response to Anthropic format.
func ToAnthropicBatchCreateResponse(resp *schemas.BifrostBatchCreateResponse) *AnthropicBatchResponse {
	result := &AnthropicBatchResponse{
		ID:               resp.ID,
		Type:             "message_batch",
		ProcessingStatus: toAnthropicProcessingStatus(resp.Status),
		CreatedAt:        formatAnthropicTimestamp(resp.CreatedAt),
		ExpiresAt:        formatAnthropicTimestamp(resp.ExpiresAt),
		ResultsURL:       resp.ResultsURL,
	}
	if resp.RequestCounts.Total > 0 {
		result.RequestCounts = &AnthropicBatchRequestCounts{
			Processing: resp.RequestCounts.Pending,
			Succeeded:  resp.RequestCounts.Succeeded,
			Errored:    resp.RequestCounts.Failed,
			Canceled:   resp.RequestCounts.Canceled,
			Expired:    resp.RequestCounts.Expired,
		}
	}
	return result
}

// ToAnthropicBatchListResponse converts a Bifrost batch list response to Anthropic format.
func ToAnthropicBatchListResponse(resp *schemas.BifrostBatchListResponse) *AnthropicBatchListResponse {
	result := &AnthropicBatchListResponse{
		Data:    make([]AnthropicBatchResponse, len(resp.Data)),
		HasMore: resp.HasMore,
		FirstID: resp.FirstID,
		LastID:  resp.LastID,
	}

	for i, batch := range resp.Data {
		result.Data[i] = *ToAnthropicBatchRetrieveResponse(&batch)
	}

	return result
}

// ToAnthropicBatchRetrieveResponse converts a Bifrost batch retrieve response to Anthropic format.
func ToAnthropicBatchRetrieveResponse(resp *schemas.BifrostBatchRetrieveResponse) *AnthropicBatchResponse {
	result := &AnthropicBatchResponse{
		ID:               resp.ID,
		Type:             "message_batch",
		ProcessingStatus: toAnthropicProcessingStatus(resp.Status),
		CreatedAt:        formatAnthropicTimestamp(resp.CreatedAt),
		ResultsURL:       resp.ResultsURL,
	}

	if resp.ExpiresAt != nil {
		result.ExpiresAt = formatAnthropicTimestamp(*resp.ExpiresAt)
	}

	if resp.CompletedAt != nil {
		endedAt := formatAnthropicTimestamp(*resp.CompletedAt)
		result.EndedAt = &endedAt
	}

	if resp.ArchivedAt != nil {
		archivedAt := formatAnthropicTimestamp(*resp.ArchivedAt)
		result.ArchivedAt = &archivedAt
	}

	if resp.CancellingAt != nil {
		cancelInitiatedAt := formatAnthropicTimestamp(*resp.CancellingAt)
		result.CancelInitiatedAt = &cancelInitiatedAt
	}

	if resp.RequestCounts.Total > 0 {
		result.RequestCounts = &AnthropicBatchRequestCounts{
			Processing: resp.RequestCounts.Pending,
			Succeeded:  resp.RequestCounts.Succeeded,
			Errored:    resp.RequestCounts.Failed,
			Canceled:   resp.RequestCounts.Canceled,
			Expired:    resp.RequestCounts.Expired,
		}
	}

	return result
}

// ToAnthropicBatchCancelResponse converts a Bifrost batch cancel response to Anthropic format.
func ToAnthropicBatchCancelResponse(resp *schemas.BifrostBatchCancelResponse) *AnthropicBatchResponse {
	result := &AnthropicBatchResponse{
		ID:               resp.ID,
		Type:             "message_batch",
		ProcessingStatus: toAnthropicProcessingStatus(resp.Status),
	}

	if resp.CancellingAt != nil {
		cancelInitiatedAt := formatAnthropicTimestamp(*resp.CancellingAt)
		result.CancelInitiatedAt = &cancelInitiatedAt
	}

	if resp.RequestCounts.Total > 0 {
		result.RequestCounts = &AnthropicBatchRequestCounts{
			Processing: resp.RequestCounts.Pending,
			Succeeded:  resp.RequestCounts.Completed,
			Errored:    resp.RequestCounts.Failed,
		}
	}

	return result
}

// toAnthropicProcessingStatus converts Bifrost batch status to Anthropic processing_status.
func toAnthropicProcessingStatus(status schemas.BatchStatus) string {
	switch status {
	case schemas.BatchStatusInProgress:
		fallthrough
	case schemas.BatchStatusValidating:
		return "in_progress"
	case schemas.BatchStatusCancelling:
		return "canceling"
	case schemas.BatchStatusEnded, schemas.BatchStatusCompleted, schemas.BatchStatusCancelled:
		return "ended"
	default:
		return string(status)
	}
}

// formatAnthropicTimestamp converts Unix timestamp to Anthropic ISO timestamp format.
func formatAnthropicTimestamp(unixTime int64) string {
	if unixTime == 0 {
		return ""
	}
	return time.Unix(unixTime, 0).UTC().Format(time.RFC3339)
}
