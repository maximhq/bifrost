package gemini

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToBifrostBatchStatus converts Gemini batch job state to Bifrost status.
func ToBifrostBatchStatus(geminiState string) schemas.BatchStatus {
	switch geminiState {
	case GeminiBatchStatePending, GeminiBatchStateRunning:
		return schemas.BatchStatusInProgress
	case GeminiBatchStateSucceeded:
		return schemas.BatchStatusCompleted
	case GeminiBatchStateFailed:
		return schemas.BatchStatusFailed
	case GeminiBatchStateCancelling:
		return schemas.BatchStatusCancelling
	case GeminiBatchStateCancelled:
		return schemas.BatchStatusCancelled
	case GeminiBatchStateExpired:
		return schemas.BatchStatusExpired
	default:
		return schemas.BatchStatusInProgress
	}
}

// ==================== HELPER FUNCTIONS ====================

// parseGeminiTimestamp converts Gemini RFC3339 timestamp to Unix timestamp.
func parseGeminiTimestamp(timestamp string) int64 {
	if timestamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// extractBatchIDFromName extracts the batch ID from the full resource name.
// e.g., "batches/abc123" -> "batches/abc123"
func extractBatchIDFromName(name string) string {
	return name
}

// buildBatchRequestItems converts Bifrost batch requests to Gemini format.
func buildBatchRequestItems(requests []schemas.BatchRequestItem) []GeminiBatchRequestItem {
	items := make([]GeminiBatchRequestItem, 0, len(requests))

	for _, req := range requests {
		contents := []Content{}

		// Try Body first, then fall back to Params (Anthropic SDK uses Params)
		requestData := req.Body
		if requestData == nil {
			requestData = req.Params
		}

		// Extract messages from the request data
		if requestData != nil {
			if msgs, ok := requestData["messages"].([]interface{}); ok {
				for _, msg := range msgs {
					if msgMap, ok := msg.(map[string]interface{}); ok {
						role := "user"
						if r, ok := msgMap["role"].(string); ok {
							if r == "assistant" {
								role = "model"
							} else if r == "system" {
								// System messages are handled separately in Gemini
								continue
							} else {
								role = r
							}
						}

						parts := []*Part{}
						if c, ok := msgMap["content"].(string); ok {
							parts = append(parts, &Part{Text: c})
						}

						contents = append(contents, Content{
							Role:  role,
							Parts: parts,
						})
					}
				}
			}
		}

		item := GeminiBatchRequestItem{
			Request: GeminiBatchGenerateContentRequest{
				Contents: contents,
			},
		}

		// Add metadata with custom_id as key
		if req.CustomID != "" {
			item.Metadata = &GeminiBatchMetadata{
				Key: req.CustomID,
			}
		}

		items = append(items, item)
	}

	return items
}

// ==================== BATCH OPERATIONS ====================

// BatchCreate creates a new batch job for Gemini.
// Uses the asynchronous batchGenerateContent endpoint as per official documentation.
// Supports both inline requests and file-based input (via InputFileID).
func (provider *GeminiProvider) BatchCreate(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Validate that either InputFileID or Requests is provided, but not both
	hasFileInput := request.InputFileID != ""
	hasInlineRequests := len(request.Requests) > 0

	if !hasFileInput && !hasInlineRequests {
		return nil, providerUtils.NewBifrostOperationError("either input_file_id or requests must be provided", nil, providerName)
	}

	if hasFileInput && hasInlineRequests {
		return nil, providerUtils.NewBifrostOperationError("cannot specify both input_file_id and requests", nil, providerName)
	}

	// Build the batch request with proper nested structure
	batchReq := &GeminiBatchCreateRequest{
		Batch: GeminiBatchConfig{
			DisplayName: fmt.Sprintf("bifrost-batch-%d", time.Now().UnixNano()),
		},
	}

	if hasFileInput {
		// File-based input: use file_name in input_config
		fileID := request.InputFileID
		// Ensure file ID has the "files/" prefix
		if !strings.HasPrefix(fileID, "files/") {
			fileID = "files/" + fileID
		}
		batchReq.Batch.InputConfig = GeminiBatchInputConfig{
			FileName: fileID,
		}
	} else {
		// Inline requests: use requests in input_config
		batchReq.Batch.InputConfig = GeminiBatchInputConfig{
			Requests: &GeminiBatchRequestsWrapper{
				Requests: buildBatchRequestItems(request.Requests),
			},
		}
	}

	jsonData, err := sonic.Marshal(batchReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL - use batchGenerateContent endpoint
	_, model := schemas.ParseModelString(request.Model, schemas.Gemini)
	if model == "" {
		model = "gemini-2.5-flash"
	}
	url := fmt.Sprintf("%s/models/%s:batchGenerateContent", provider.networkConfig.BaseURL, model)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")
	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(providerName, resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Parse the batch job response
	var geminiResp GeminiBatchJobResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		provider.logger.Error("gemini batch create unmarshal error: " + err.Error())
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Calculate request counts based on response
	totalRequests := geminiResp.Metadata.BatchStats.RequestCount
	completedCount := geminiResp.Metadata.BatchStats.RequestCount - geminiResp.Metadata.BatchStats.PendingRequestCount
	failedCount := 0

	// If results are already available (fast completion), count them
	if geminiResp.Dest != nil && len(geminiResp.Dest.InlinedResponses) > 0 {
		for _, inlineResp := range geminiResp.Dest.InlinedResponses {
			if inlineResp.Error != nil {
				failedCount++
			} else if inlineResp.Response != nil {
				completedCount++
			}
		}
	}

	// Determine status
	status := ToBifrostBatchStatus(geminiResp.Metadata.State)

	// If state is empty but we have results, it's completed
	if geminiResp.Metadata.State == "" && geminiResp.Dest != nil && len(geminiResp.Dest.InlinedResponses) > 0 {
		status = schemas.BatchStatusCompleted
		completedCount = len(geminiResp.Dest.InlinedResponses) - failedCount
	}

	// Build response
	result := &schemas.BifrostBatchCreateResponse{
		ID:            geminiResp.Metadata.Name,
		Object:        "batch",
		Endpoint:      string(request.Endpoint),
		Status:        status,
		CreatedAt:     parseGeminiTimestamp(geminiResp.Metadata.CreateTime),
		OperationName: &geminiResp.Metadata.Name,
		RequestCounts: schemas.BatchRequestCounts{
			Total:     totalRequests,
			Completed: completedCount,
			Failed:    failedCount,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCreateRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	// Include InputFileID if file-based input was used
	if hasFileInput {
		result.InputFileID = request.InputFileID
	}

	// Include output file ID if results are in a file
	if geminiResp.Dest != nil && geminiResp.Dest.FileName != "" {
		result.OutputFileID = &geminiResp.Dest.FileName
	}

	return result, nil
}

// BatchList lists batch jobs for Gemini.
// Note: The consumer API may have limited list functionality.
func (provider *GeminiProvider) BatchList(ctx context.Context, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Select a key for the request
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("at least one API key is required", nil, providerName)
	}
	key := keys[0]

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL for listing batches
	url := fmt.Sprintf("%s/batches", provider.networkConfig.BaseURL)

	// Add pagination parameters
	if request.PageSize > 0 {
		url += fmt.Sprintf("?pageSize=%d", request.PageSize)
	} else if request.Limit > 0 {
		url += fmt.Sprintf("?pageSize=%d", request.Limit)
	}
	if request.PageToken != nil && *request.PageToken != "" {
		if strings.Contains(url, "?") {
			url += "&pageToken=" + *request.PageToken
		} else {
			url += "?pageToken=" + *request.PageToken
		}
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response - if listing is not supported, return empty list
	if resp.StatusCode() != fasthttp.StatusOK {
		// If 404 or method not allowed, batch listing may not be available
		if resp.StatusCode() == fasthttp.StatusNotFound || resp.StatusCode() == fasthttp.StatusMethodNotAllowed {
			provider.logger.Debug("gemini batch list not available, returning empty list")
			return &schemas.BifrostBatchListResponse{
				Object:  "list",
				Data:    []schemas.BifrostBatchRetrieveResponse{},
				HasMore: false,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType: schemas.BatchListRequest,
					Provider:    providerName,
					Latency:     latency.Milliseconds(),
				},
			}, nil
		}
		return nil, parseGeminiError(providerName, resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiBatchListResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Convert to Bifrost format
	data := make([]schemas.BifrostBatchRetrieveResponse, 0, len(geminiResp.Operations))
	for _, batch := range geminiResp.Operations {
		data = append(data, schemas.BifrostBatchRetrieveResponse{
			ID:            extractBatchIDFromName(batch.Name),
			Object:        "batch",
			Status:        ToBifrostBatchStatus(batch.Metadata.State),
			CreatedAt:     parseGeminiTimestamp(batch.Metadata.CreateTime),
			OperationName: &batch.Name,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchListRequest,
				Provider:    providerName,
			},
		})
	}

	hasMore := geminiResp.NextPageToken != ""
	var nextCursor *string
	if hasMore {
		nextCursor = &geminiResp.NextPageToken
	}

	return &schemas.BifrostBatchListResponse{
		Object:     "list",
		Data:       data,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// BatchRetrieve retrieves a specific batch job for Gemini.
func (provider *GeminiProvider) BatchRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL - batch ID might be full resource name or just the ID
	batchID := request.BatchID
	var url string
	if strings.HasPrefix(batchID, "batches/") {
		url = fmt.Sprintf("%s/%s", provider.networkConfig.BaseURL, batchID)
	} else {
		url = fmt.Sprintf("%s/batches/%s", provider.networkConfig.BaseURL, batchID)
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(providerName, resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiBatchJobResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Calculate request counts from results if available
	var completedCount, failedCount int
	if geminiResp.Dest != nil {
		for _, inlineResp := range geminiResp.Dest.InlinedResponses {
			if inlineResp.Error != nil {
				failedCount++
			} else if inlineResp.Response != nil {
				completedCount++
			}
		}
	}

	// Determine if job is done
	isDone := geminiResp.Metadata.State == GeminiBatchStateSucceeded ||
		geminiResp.Metadata.State == GeminiBatchStateFailed ||
		geminiResp.Metadata.State == GeminiBatchStateCancelled ||
		geminiResp.Metadata.State == GeminiBatchStateExpired

	return &schemas.BifrostBatchRetrieveResponse{
		ID:            geminiResp.Metadata.Name,
		Object:        "batch",
		Status:        ToBifrostBatchStatus(geminiResp.Metadata.State),
		CreatedAt:     parseGeminiTimestamp(geminiResp.Metadata.CreateTime),
		OperationName: &geminiResp.Metadata.Name,
		Done:          &isDone,
		RequestCounts: schemas.BatchRequestCounts{
			Completed: completedCount,
			Failed:    failedCount,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// BatchCancel cancels a batch job for Gemini.
// Note: Cancellation support depends on the API version and batch state.
func (provider *GeminiProvider) BatchCancel(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL for cancel operation
	batchID := request.BatchID
	var url string
	if strings.HasPrefix(batchID, "batches/") {
		url = fmt.Sprintf("%s/%s:cancel", provider.networkConfig.BaseURL, batchID)
	} else {
		url = fmt.Sprintf("%s/batches/%s:cancel", provider.networkConfig.BaseURL, batchID)
	}

	provider.logger.Debug("gemini batch cancel url: " + url)
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle response
	if resp.StatusCode() != fasthttp.StatusOK {
		// If cancel is not supported, return appropriate status
		if resp.StatusCode() == fasthttp.StatusNotFound || resp.StatusCode() == fasthttp.StatusMethodNotAllowed {
			return &schemas.BifrostBatchCancelResponse{
				ID:     request.BatchID,
				Object: "batch",
				Status: schemas.BatchStatusCompleted, // Assume completed if cancel not available
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType: schemas.BatchCancelRequest,
					Provider:    providerName,
					Latency:     latency.Milliseconds(),
				},
			}, nil
		}
		return nil, parseGeminiError(providerName, resp)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(providerName, resp)
	}

	now := time.Now().Unix()
	return &schemas.BifrostBatchCancelResponse{
		ID:           request.BatchID,
		Object:       "batch",
		Status:       schemas.BatchStatusCancelling,
		CancellingAt: &now,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCancelRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// downloadBatchResultsFile downloads and parses a batch results file from Gemini.
// Returns the parsed result items from the JSONL file.
func (provider *GeminiProvider) downloadBatchResultsFile(ctx context.Context, key schemas.Key, fileName string) ([]schemas.BatchResultItem, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Create request to download the file
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build download URL - use the download endpoint with alt=media
	// The base URL is like https://generativelanguage.googleapis.com/v1beta
	// We need to change it to https://generativelanguage.googleapis.com/download/v1beta
	baseURL := strings.Replace(provider.networkConfig.BaseURL, "/v1beta", "/download/v1beta", 1)

	// Ensure fileName has proper format
	fileID := fileName
	if !strings.HasPrefix(fileID, "files/") {
		fileID = "files/" + fileID
	}

	url := fmt.Sprintf("%s/%s:download?alt=media", baseURL, fileID)

	provider.logger.Debug("gemini batch results file download url: " + url)
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}

	// Make request
	_, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(providerName, resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Parse JSONL content - each line is a separate JSON object
	results := make([]schemas.BatchResultItem, 0)
	lines := strings.Split(string(body), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var resultLine GeminiBatchFileResultLine
		if err := sonic.Unmarshal([]byte(line), &resultLine); err != nil {
			provider.logger.Error("gemini batch results file parse error: " + err.Error())
			continue
		}

		customID := resultLine.Key
		if customID == "" {
			customID = fmt.Sprintf("request-%d", len(results))
		}

		resultItem := schemas.BatchResultItem{
			CustomID: customID,
		}

		if resultLine.Error != nil {
			resultItem.Error = &schemas.BatchResultError{
				Code:    fmt.Sprintf("%d", resultLine.Error.Code),
				Message: resultLine.Error.Message,
			}
		} else if resultLine.Response != nil {
			// Convert the response to a map for the Body field
			respBody := make(map[string]interface{})
			if len(resultLine.Response.Candidates) > 0 {
				candidate := resultLine.Response.Candidates[0]
				if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
					var textParts []string
					for _, part := range candidate.Content.Parts {
						if part.Text != "" {
							textParts = append(textParts, part.Text)
						}
					}
					if len(textParts) > 0 {
						respBody["text"] = strings.Join(textParts, "")
					}
				}
				respBody["finish_reason"] = string(candidate.FinishReason)
			}
			if resultLine.Response.UsageMetadata != nil {
				respBody["usage"] = map[string]interface{}{
					"prompt_tokens":     resultLine.Response.UsageMetadata.PromptTokenCount,
					"completion_tokens": resultLine.Response.UsageMetadata.CandidatesTokenCount,
					"total_tokens":      resultLine.Response.UsageMetadata.TotalTokenCount,
				}
			}

			resultItem.Response = &schemas.BatchResultResponse{
				StatusCode: 200,
				Body:       respBody,
			}
		}

		results = append(results, resultItem)
	}

	return results, nil
}

// BatchResults retrieves batch results for Gemini.
// Results are extracted from dest.inlinedResponses for inline batches,
// or downloaded from dest.fileName for file-based batches.
func (provider *GeminiProvider) BatchResults(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	// First, retrieve the batch to get its results
	retrieveReq := &schemas.BifrostBatchRetrieveRequest{
		BatchID: request.BatchID,
	}

	// We need to get the full batch response with results, so make the API call directly
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL
	batchID := request.BatchID
	var url string
	if strings.HasPrefix(batchID, "batches/") {
		url = fmt.Sprintf("%s/%s", provider.networkConfig.BaseURL, batchID)
	} else {
		url = fmt.Sprintf("%s/batches/%s", provider.networkConfig.BaseURL, batchID)
	}

	provider.logger.Debug("gemini batch results url: " + url)
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(providerName, resp)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiBatchJobResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Check if batch is still processing
	if geminiResp.Metadata.State == GeminiBatchStatePending || geminiResp.Metadata.State == GeminiBatchStateRunning {
		return nil, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("batch %s is still processing (state: %s), results not yet available", retrieveReq.BatchID, geminiResp.Metadata.State),
			nil,
			providerName,
		)
	}

	// Extract results - check for file-based results first, then inline responses
	var results []schemas.BatchResultItem

	if geminiResp.Dest != nil && geminiResp.Dest.FileName != "" {
		// File-based results: download and parse the results file
		provider.logger.Debug("gemini batch results in file: " + geminiResp.Dest.FileName)
		fileResults, bifrostErr := provider.downloadBatchResultsFile(ctx, key, geminiResp.Dest.FileName)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		results = fileResults
	} else if geminiResp.Dest != nil && len(geminiResp.Dest.InlinedResponses) > 0 {
		// Inline results: extract from inlinedResponses
		results = make([]schemas.BatchResultItem, 0, len(geminiResp.Dest.InlinedResponses))
		for i, inlineResp := range geminiResp.Dest.InlinedResponses {
			customID := fmt.Sprintf("request-%d", i)
			if inlineResp.Metadata != nil && inlineResp.Metadata.Key != "" {
				customID = inlineResp.Metadata.Key
			}

			resultItem := schemas.BatchResultItem{
				CustomID: customID,
			}

			if inlineResp.Error != nil {
				resultItem.Error = &schemas.BatchResultError{
					Code:    fmt.Sprintf("%d", inlineResp.Error.Code),
					Message: inlineResp.Error.Message,
				}
			} else if inlineResp.Response != nil {
				// Convert the response to a map for the Body field
				respBody := make(map[string]interface{})
				if len(inlineResp.Response.Candidates) > 0 {
					candidate := inlineResp.Response.Candidates[0]
					if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
						var textParts []string
						for _, part := range candidate.Content.Parts {
							if part.Text != "" {
								textParts = append(textParts, part.Text)
							}
						}
						if len(textParts) > 0 {
							respBody["text"] = strings.Join(textParts, "")
						}
					}
					respBody["finish_reason"] = string(candidate.FinishReason)
				}
				if inlineResp.Response.UsageMetadata != nil {
					respBody["usage"] = map[string]interface{}{
						"prompt_tokens":     inlineResp.Response.UsageMetadata.PromptTokenCount,
						"completion_tokens": inlineResp.Response.UsageMetadata.CandidatesTokenCount,
						"total_tokens":      inlineResp.Response.UsageMetadata.TotalTokenCount,
					}
				}

				resultItem.Response = &schemas.BatchResultResponse{
					StatusCode: 200,
					Body:       respBody,
				}
			}

			results = append(results, resultItem)
		}
	}

	// If no results found but job is complete, return info message
	if len(results) == 0 && (geminiResp.Metadata.State == GeminiBatchStateSucceeded || geminiResp.Metadata.State == GeminiBatchStateFailed) {
		results = []schemas.BatchResultItem{{
			CustomID: "info",
			Response: &schemas.BatchResultResponse{
				StatusCode: 200,
				Body: map[string]interface{}{
					"message": fmt.Sprintf("Batch completed with state: %s. No results available.", geminiResp.Metadata.State),
				},
			},
		}}
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
