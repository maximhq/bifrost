package openai

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

// OpenAI Batch API Types

// OpenAIBatchRequest represents the request body for creating a batch.
type OpenAIBatchRequest struct {
	InputFileID      string            `json:"input_file_id"`
	Endpoint         string            `json:"endpoint"`
	CompletionWindow string            `json:"completion_window"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

// OpenAIBatchResponse represents an OpenAI batch response.
type OpenAIBatchResponse struct {
	ID               string                    `json:"id"`
	Object           string                    `json:"object"`
	Endpoint         string                    `json:"endpoint"`
	Errors           *schemas.BatchErrors      `json:"errors,omitempty"`
	InputFileID      string                    `json:"input_file_id"`
	CompletionWindow string                    `json:"completion_window"`
	Status           string                    `json:"status"`
	OutputFileID     *string                   `json:"output_file_id,omitempty"`
	ErrorFileID      *string                   `json:"error_file_id,omitempty"`
	CreatedAt        int64                     `json:"created_at"`
	InProgressAt     *int64                    `json:"in_progress_at,omitempty"`
	ExpiresAt        *int64                    `json:"expires_at,omitempty"`
	FinalizingAt     *int64                    `json:"finalizing_at,omitempty"`
	CompletedAt      *int64                    `json:"completed_at,omitempty"`
	FailedAt         *int64                    `json:"failed_at,omitempty"`
	ExpiredAt        *int64                    `json:"expired_at,omitempty"`
	CancellingAt     *int64                    `json:"cancelling_at,omitempty"`
	CancelledAt      *int64                    `json:"cancelled_at,omitempty"`
	RequestCounts    *OpenAIBatchRequestCounts `json:"request_counts,omitempty"`
	Metadata         map[string]string         `json:"metadata,omitempty"`
}

// OpenAIBatchRequestCounts represents the request counts for a batch.
type OpenAIBatchRequestCounts struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// OpenAIBatchListResponse represents the response from listing batches.
type OpenAIBatchListResponse struct {
	Object  string                `json:"object"`
	Data    []OpenAIBatchResponse `json:"data"`
	FirstID *string               `json:"first_id,omitempty"`
	LastID  *string               `json:"last_id,omitempty"`
	HasMore bool                  `json:"has_more"`
}

// ToBifrostBatchStatus converts OpenAI status to Bifrost status.
func ToBifrostBatchStatus(status string) schemas.BatchStatus {
	switch status {
	case "validating":
		return schemas.BatchStatusValidating
	case "failed":
		return schemas.BatchStatusFailed
	case "in_progress":
		return schemas.BatchStatusInProgress
	case "finalizing":
		return schemas.BatchStatusFinalizing
	case "completed":
		return schemas.BatchStatusCompleted
	case "expired":
		return schemas.BatchStatusExpired
	case "cancelling":
		return schemas.BatchStatusCancelling
	case "cancelled":
		return schemas.BatchStatusCancelled
	default:
		return schemas.BatchStatus(status)
	}
}

// ToBifrostBatchCreateResponse converts OpenAI batch response to Bifrost batch response.
func (r *OpenAIBatchResponse) ToBifrostBatchCreateResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawResponse bool, rawResponse interface{}) *schemas.BifrostBatchCreateResponse {
	resp := &schemas.BifrostBatchCreateResponse{
		ID:               r.ID,
		Object:           r.Object,
		Endpoint:         r.Endpoint,
		InputFileID:      r.InputFileID,
		CompletionWindow: r.CompletionWindow,
		Status:           ToBifrostBatchStatus(r.Status),
		Metadata:         r.Metadata,
		CreatedAt:        r.CreatedAt,
		OutputFileID:     r.OutputFileID,
		ErrorFileID:      r.ErrorFileID,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCreateRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if r.ExpiresAt != nil {
		resp.ExpiresAt = *r.ExpiresAt
	}

	if r.RequestCounts != nil {
		resp.RequestCounts = schemas.BatchRequestCounts{
			Total:     r.RequestCounts.Total,
			Completed: r.RequestCounts.Completed,
			Failed:    r.RequestCounts.Failed,
		}
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}

// ToBifrostBatchRetrieveResponse converts OpenAI batch response to Bifrost batch retrieve response.
func (r *OpenAIBatchResponse) ToBifrostBatchRetrieveResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawResponse bool, rawResponse interface{}) *schemas.BifrostBatchRetrieveResponse {
	resp := &schemas.BifrostBatchRetrieveResponse{
		ID:               r.ID,
		Object:           r.Object,
		Endpoint:         r.Endpoint,
		InputFileID:      r.InputFileID,
		CompletionWindow: r.CompletionWindow,
		Status:           ToBifrostBatchStatus(r.Status),
		Metadata:         r.Metadata,
		CreatedAt:        r.CreatedAt,
		InProgressAt:     r.InProgressAt,
		FinalizingAt:     r.FinalizingAt,
		CompletedAt:      r.CompletedAt,
		FailedAt:         r.FailedAt,
		ExpiredAt:        r.ExpiredAt,
		CancellingAt:     r.CancellingAt,
		CancelledAt:      r.CancelledAt,
		OutputFileID:     r.OutputFileID,
		ErrorFileID:      r.ErrorFileID,
		Errors:           r.Errors,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if r.ExpiresAt != nil {
		resp.ExpiresAt = r.ExpiresAt
	}

	if r.RequestCounts != nil {
		resp.RequestCounts = schemas.BatchRequestCounts{
			Total:     r.RequestCounts.Total,
			Completed: r.RequestCounts.Completed,
			Failed:    r.RequestCounts.Failed,
		}
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}

// BatchCreate creates a new batch job.
func (provider *OpenAIProvider) BatchCreate(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	inputFileID := request.InputFileID

	// If no file_id provided but inline requests are available, upload them first
	if inputFileID == "" && len(request.Requests) > 0 {	
		// Convert inline requests to JSONL format
		jsonlData, err := ConvertRequestsToJSONL(request.Requests)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to convert requests to JSONL", err, providerName)
		}

		// Upload the file with purpose "batch"
		uploadResp, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
			Provider: schemas.OpenAI,
			File:     jsonlData,
			Filename: "batch_requests.jsonl",
			Purpose:  "batch",
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		inputFileID = uploadResp.ID
	}

	// Validate that we have a file ID (either provided or uploaded)
	if inputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("either input_file_id or requests array is required for OpenAI batch API", nil, providerName)
	}

	// Validate that we have an endpoint
	if request.Endpoint == "" {
		return nil, providerUtils.NewBifrostOperationError("endpoint is required for OpenAI batch API (either specify endpoint or include url in inline requests)", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/batches", schemas.BatchCreateRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Build request body
	openAIReq := &OpenAIBatchRequest{
		InputFileID:      inputFileID,
		Endpoint:         string(request.Endpoint),
		CompletionWindow: request.CompletionWindow,
		Metadata:         request.Metadata,
	}

	// Set default completion window if not provided
	if openAIReq.CompletionWindow == "" {
		openAIReq.CompletionWindow = "24h"
	}

	jsonData, err := sonic.Marshal(openAIReq)
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
		return nil, ParseOpenAIError(resp, schemas.BatchCreateRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp OpenAIBatchResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostBatchCreateResponse(providerName, latency, sendBackRawResponse, rawResponse), nil
}

// BatchList lists batch jobs.
func (provider *OpenAIProvider) BatchList(ctx context.Context, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	url := provider.buildRequestURL(ctx, "/v1/batches", schemas.BatchListRequest)
	if request.Limit > 0 || request.After != nil {
		url += "?"
		if request.Limit > 0 {
			url += fmt.Sprintf("limit=%d", request.Limit)
		}
		if request.After != nil {
			if request.Limit > 0 {
				url += "&"
			}
			url += fmt.Sprintf("after=%s", *request.After)
		}
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Use first key if available
	if len(keys) > 0 && keys[0].Value != "" {
		req.Header.Set("Authorization", "Bearer "+keys[0].Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.BatchListRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp OpenAIBatchListResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostBatchListResponse{
		Object:  openAIResp.Object,
		FirstID: openAIResp.FirstID,
		LastID:  openAIResp.LastID,
		HasMore: openAIResp.HasMore,
		Data:    make([]schemas.BifrostBatchRetrieveResponse, len(openAIResp.Data)),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	for i, batch := range openAIResp.Data {
		bifrostResp.Data[i] = *batch.ToBifrostBatchRetrieveResponse(providerName, 0, false, nil)
	}

	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// BatchRetrieve retrieves a specific batch job.
func (provider *OpenAIProvider) BatchRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
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
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/batches/" + request.BatchID)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.BatchRetrieveRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp OpenAIBatchResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result := openAIResp.ToBifrostBatchRetrieveResponse(providerName, latency, sendBackRawResponse, rawResponse)
	result.ExtraFields.RequestType = schemas.BatchRetrieveRequest
	return result, nil
}

// BatchCancel cancels a batch job.
func (provider *OpenAIProvider) BatchCancel(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
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
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/batches/" + request.BatchID + "/cancel")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.BatchCancelRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp OpenAIBatchResponse
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result := &schemas.BifrostBatchCancelResponse{
		ID:           openAIResp.ID,
		Object:       openAIResp.Object,
		Status:       ToBifrostBatchStatus(openAIResp.Status),
		CancellingAt: openAIResp.CancellingAt,
		CancelledAt:  openAIResp.CancelledAt,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCancelRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if openAIResp.RequestCounts != nil {
		result.RequestCounts = schemas.BatchRequestCounts{
			Total:     openAIResp.RequestCounts.Total,
			Completed: openAIResp.RequestCounts.Completed,
			Failed:    openAIResp.RequestCounts.Failed,
		}
	}

	if sendBackRawResponse {
		result.ExtraFields.RawResponse = rawResponse
	}

	return result, nil
}

// BatchResults retrieves batch results.
// Note: For OpenAI, batch results are obtained by downloading the output_file_id.
// This method returns the file content parsed as batch results.
func (provider *OpenAIProvider) BatchResults(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// First, retrieve the batch to get the output_file_id
	batchResp, bifrostErr := provider.BatchRetrieve(ctx, key, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  request.BatchID,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if batchResp.OutputFileID == nil || *batchResp.OutputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch results not available: output_file_id is empty (batch may not be completed)", nil, providerName)
	}

	// Download the output file
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + *batchResp.OutputFileID + "/content")
	req.Header.SetMethod(http.MethodGet)

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.BatchResultsRequest, providerName, "")
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

		var resultItem schemas.BatchResultItem
		if err := sonic.Unmarshal(line, &resultItem); err != nil {
			provider.logger.Warn(fmt.Sprintf("failed to parse batch result line: %v", err))
			continue
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

// BatchDelete is not supported by OpenAI provider.
func (provider *OpenAIProvider) BatchDelete(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

