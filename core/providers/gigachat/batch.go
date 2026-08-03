package gigachat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	openaiProvider "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

type openAICompatibleBatchInputRow struct {
	CustomID string          `json:"custom_id"`
	Method   string          `json:"method,omitempty"`
	URL      string          `json:"url,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
}

const gigaChatBatchCompletionWindow24h = "24h"

func (provider *GigaChatProvider) batchCreateWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest, forceRefresh bool) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)

	method, completionWindow, body, bifrostErr := provider.buildGigaChatBatchCreatePayload(ctx, key, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	values := url.Values{}
	values.Set("method", string(method))
	path := withGigaChatQuery("/batches", values)
	responseBody, providerResponseHeaders, _, latency, bifrostErr := provider.executeGigaChatFileRequest(ctx, key, schemas.BatchCreateRequest, http.MethodPost, path, "application/octet-stream", "application/json", body, body, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var raw json.RawMessage
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &raw, body, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, enrichGigaChatError(ctx, bifrostErr, body, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	batch, err := decodeGigaChatBatchResponse(responseBody)
	if err != nil {
		return nil, newGigaChatProviderResponseError("failed to decode GigaChat batch create response", err)
	}

	response := toBifrostGigaChatBatchCreateResponse(provider.GetProviderKey(), batch, request, completionWindow, latency)
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

func (provider *GigaChatProvider) batchListWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, _ *schemas.BifrostBatchListRequest, forceRefresh bool) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)

	responseBody, providerResponseHeaders, _, latency, bifrostErr := provider.executeGigaChatFileRequest(ctx, key, schemas.BatchListRequest, http.MethodGet, "/batches", "", "application/json", nil, nil, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	gigaChatResponse := &GigaChatBatches{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, gigaChatResponse, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, enrichGigaChatError(ctx, bifrostErr, nil, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	response := toBifrostGigaChatBatchListResponse(provider.GetProviderKey(), *gigaChatResponse, latency)
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

func (provider *GigaChatProvider) batchRetrieveWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchRetrieveRequest, forceRefresh bool) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)

	values := url.Values{}
	values.Set("batch_id", strings.TrimSpace(request.BatchID))
	path := withGigaChatQuery("/batches", values)
	responseBody, providerResponseHeaders, _, latency, bifrostErr := provider.executeGigaChatFileRequest(ctx, key, schemas.BatchRetrieveRequest, http.MethodGet, path, "", "application/json", nil, nil, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var raw json.RawMessage
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &raw, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, enrichGigaChatError(ctx, bifrostErr, nil, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	batch, err := decodeGigaChatBatchResponse(responseBody)
	if err != nil {
		return nil, newGigaChatProviderResponseError("failed to decode GigaChat batch retrieve response", err)
	}

	response := toBifrostGigaChatBatchRetrieveResponse(provider.GetProviderKey(), batch, "", latency)
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

func (provider *GigaChatProvider) buildGigaChatBatchCreatePayload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (GigaChatBatchMethod, string, []byte, *schemas.BifrostError) {
	method, completionWindow, bifrostErr := validateGigaChatBatchCreateRequest(request)
	if bifrostErr != nil {
		return "", "", nil, bifrostErr
	}

	var (
		body []byte
		err  error
	)
	switch {
	case strings.TrimSpace(request.InputFileID) != "":
		content, contentErr := provider.readGigaChatBatchInputFile(ctx, key, request)
		if contentErr != nil {
			return "", "", nil, contentErr
		}
		body, err = convertGigaChatBatchInputJSONL(request.Endpoint, content)
	case len(request.Requests) > 0:
		body, err = convertGigaChatBatchRequestItemsToJSONL(request.Endpoint, request.Requests)
	default:
		return "", "", nil, providerUtils.NewBifrostOperationError("either input_file_id or requests array is required for GigaChat batch API", nil)
	}
	if err != nil {
		return "", "", nil, providerUtils.NewBifrostOperationError("failed to convert GigaChat batch input rows", err)
	}
	return method, completionWindow, body, nil
}

func (provider *GigaChatProvider) readGigaChatBatchInputFile(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) ([]byte, *schemas.BifrostError) {
	fileRequest := &schemas.BifrostFileContentRequest{
		Provider: provider.GetProviderKey(),
		Model:    request.Model,
		FileID:   strings.TrimSpace(request.InputFileID),
	}
	response, bifrostErr := provider.fileContentWithRefresh(ctx, key, fileRequest, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		response, bifrostErr = provider.fileContentWithRefresh(ctx, key, fileRequest, true)
	}
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return response.Content, nil
}

func validateGigaChatBatchCreateRequest(request *schemas.BifrostBatchCreateRequest) (GigaChatBatchMethod, string, *schemas.BifrostError) {
	if request == nil {
		return "", "", providerUtils.NewBifrostOperationError("batch create request is nil", nil)
	}
	if strings.TrimSpace(string(request.Endpoint)) == "" {
		return "", "", providerUtils.NewBifrostOperationError("endpoint is required for GigaChat batch API", nil)
	}
	method, err := toGigaChatBatchMethod(request.Endpoint)
	if err != nil {
		return "", "", providerUtils.NewBifrostOperationError(err.Error(), err)
	}
	if strings.TrimSpace(request.InputFileID) != "" && len(request.Requests) > 0 {
		return "", "", providerUtils.NewBifrostOperationError("input_file_id and requests array cannot both be set for GigaChat batch API", nil)
	}
	completionWindow := strings.TrimSpace(request.CompletionWindow)
	if completionWindow == "" {
		completionWindow = gigaChatBatchCompletionWindow24h
	}
	if completionWindow != gigaChatBatchCompletionWindow24h {
		return "", "", providerUtils.NewBifrostOperationError("GigaChat batch API supports completion_window=24h only", nil)
	}
	if request.InputBlob != nil {
		return "", "", providerUtils.NewBifrostOperationError("GigaChat batch API does not support input_blob", nil)
	}
	if request.OutputFolder != nil {
		return "", "", providerUtils.NewBifrostOperationError("GigaChat batch API does not support output_folder", nil)
	}
	if request.OutputExpiresAfter != nil {
		return "", "", providerUtils.NewBifrostOperationError("GigaChat batch API does not support output_expires_after", nil)
	}
	if len(request.Metadata) > 0 {
		return "", "", providerUtils.NewBifrostOperationError("GigaChat batch API does not support metadata", nil)
	}
	if len(request.ExtraParams) > 0 {
		return "", "", providerUtils.NewBifrostOperationError("GigaChat batch API does not support extra batch create parameters", nil)
	}
	return method, completionWindow, nil
}

func validateGigaChatBatchListRequest(request *schemas.BifrostBatchListRequest) *schemas.BifrostError {
	if request.Limit > 0 {
		return providerUtils.NewBifrostOperationError("GigaChat batch list does not support limit pagination", nil)
	}
	if request.BeforeID != nil && strings.TrimSpace(*request.BeforeID) != "" {
		return providerUtils.NewBifrostOperationError("GigaChat batch list does not support before_id pagination", nil)
	}
	if request.AfterID != nil && strings.TrimSpace(*request.AfterID) != "" {
		return providerUtils.NewBifrostOperationError("GigaChat batch list does not support after_id pagination", nil)
	}
	if request.PageToken != nil && strings.TrimSpace(*request.PageToken) != "" {
		return providerUtils.NewBifrostOperationError("GigaChat batch list does not support page_token pagination", nil)
	}
	if request.PageSize > 0 {
		return providerUtils.NewBifrostOperationError("GigaChat batch list does not support page_size pagination", nil)
	}
	if request.NextCursor != nil && strings.TrimSpace(*request.NextCursor) != "" {
		return providerUtils.NewBifrostOperationError("GigaChat batch list does not support next_cursor pagination", nil)
	}
	if len(request.ExtraParams) > 0 {
		return providerUtils.NewBifrostOperationError("GigaChat batch list does not support extra parameters", nil)
	}
	return nil
}

func toGigaChatBatchMethod(endpoint schemas.BatchEndpoint) (GigaChatBatchMethod, error) {
	switch normalizeGigaChatBatchEndpoint(string(endpoint)) {
	case "/v1/chat/completions", "/chat/completions":
		return GigaChatBatchMethodChatCompletions, nil
	case "/v1/responses", "/responses":
		return GigaChatBatchMethodResponses, nil
	case "/v1/embeddings", "/embeddings":
		return GigaChatBatchMethodEmbedder, nil
	default:
		return "", fmt.Errorf("GigaChat batches do not support endpoint %q", endpoint)
	}
}

func normalizeGigaChatBatchEndpoint(endpoint string) string {
	normalized := strings.ToLower(strings.TrimSpace(endpoint))
	if normalized == "" {
		return ""
	}
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	return strings.TrimRight(normalized, "/")
}

func toBifrostGigaChatBatchStatus(status GigaChatBatchStatus) schemas.BatchStatus {
	switch status {
	case GigaChatBatchStatusCreated:
		return schemas.BatchStatusValidating
	case GigaChatBatchStatusInProgress:
		return schemas.BatchStatusInProgress
	case GigaChatBatchStatusCompleted:
		return schemas.BatchStatusCompleted
	default:
		return schemas.BatchStatus(status)
	}
}

func toBifrostGigaChatBatchProviderExtraFields(batch GigaChatBatch) map[string]interface{} {
	fields := make(map[string]interface{})
	if batch.Method != "" {
		fields["gigachat_batch_method"] = string(batch.Method)
	}
	switch batch.Status {
	case "", GigaChatBatchStatusCreated, GigaChatBatchStatusInProgress, GigaChatBatchStatusCompleted:
	default:
		fields["gigachat_batch_status"] = string(batch.Status)
	}
	if batch.ResultFileID != nil && strings.TrimSpace(*batch.ResultFileID) != "" {
		fields["gigachat_result_file_id"] = strings.TrimSpace(*batch.ResultFileID)
	}
	if batch.OutputFileID != nil && strings.TrimSpace(*batch.OutputFileID) != "" {
		fields["gigachat_output_file_id"] = strings.TrimSpace(*batch.OutputFileID)
	}
	if batch.InputFileID != nil && strings.TrimSpace(*batch.InputFileID) != "" {
		fields["gigachat_input_file_id"] = strings.TrimSpace(*batch.InputFileID)
	}
	if batch.ErrorFileID != nil && strings.TrimSpace(*batch.ErrorFileID) != "" {
		fields["gigachat_error_file_id"] = strings.TrimSpace(*batch.ErrorFileID)
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func decodeGigaChatBatchResponse(responseBody []byte) (GigaChatBatch, error) {
	var batch GigaChatBatch
	if err := json.Unmarshal(responseBody, &batch); err == nil && strings.TrimSpace(batch.ID) != "" {
		return batch, nil
	}

	var batches GigaChatBatches
	if err := json.Unmarshal(responseBody, &batches); err != nil {
		return GigaChatBatch{}, err
	}
	switch len(batches.Data) {
	case 0:
		return GigaChatBatch{}, fmt.Errorf("GigaChat batch response does not contain batch data")
	case 1:
		if strings.TrimSpace(batches.Data[0].ID) == "" {
			return GigaChatBatch{}, fmt.Errorf("GigaChat batch response is missing id")
		}
		return batches.Data[0], nil
	default:
		return GigaChatBatch{}, fmt.Errorf("GigaChat batch response contains %d batches, want 1", len(batches.Data))
	}
}

func toBifrostGigaChatBatchCreateResponse(providerName schemas.ModelProvider, batch GigaChatBatch, request *schemas.BifrostBatchCreateRequest, completionWindow string, latency time.Duration) *schemas.BifrostBatchCreateResponse {
	inputFileID := ""
	endpoint := ""
	if request != nil {
		inputFileID = strings.TrimSpace(request.InputFileID)
		endpoint = string(request.Endpoint)
	}
	if batch.InputFileID != nil && strings.TrimSpace(*batch.InputFileID) != "" {
		inputFileID = strings.TrimSpace(*batch.InputFileID)
	}
	if strings.TrimSpace(batch.CompletionWindow) != "" {
		completionWindow = batch.CompletionWindow
	}
	if strings.TrimSpace(endpoint) == "" {
		endpoint = toBifrostGigaChatBatchEndpoint(batch.Method)
	}

	response := &schemas.BifrostBatchCreateResponse{
		ID:                  batch.ID,
		Object:              toBifrostGigaChatBatchObject(batch.Object),
		Endpoint:            endpoint,
		InputFileID:         inputFileID,
		CompletionWindow:    completionWindow,
		Status:              toBifrostGigaChatBatchStatus(batch.Status),
		RequestCounts:       toBifrostGigaChatBatchRequestCounts(batch.RequestCounts),
		CreatedAt:           batch.CreatedAt,
		OutputFileID:        toBifrostGigaChatBatchOutputFileID(batch),
		ErrorFileID:         cleanGigaChatBatchFileID(batch.ErrorFileID),
		ProviderExtraFields: toBifrostGigaChatBatchProviderExtraFields(batch),
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: providerName,
			Latency:  latency.Milliseconds(),
		},
	}
	return response
}

func toBifrostGigaChatBatchListResponse(providerName schemas.ModelProvider, batches GigaChatBatches, latency time.Duration) *schemas.BifrostBatchListResponse {
	data := make([]schemas.BifrostBatchRetrieveResponse, 0, len(batches.Data))
	for _, batch := range batches.Data {
		data = append(data, *toBifrostGigaChatBatchRetrieveResponse(providerName, batch, "", latency))
	}

	response := &schemas.BifrostBatchListResponse{
		Object: "list",
		Data:   data,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: providerName,
			Latency:  latency.Milliseconds(),
		},
	}
	if len(data) > 0 {
		firstID := data[0].ID
		lastID := data[len(data)-1].ID
		response.FirstID = &firstID
		response.LastID = &lastID
	}
	return response
}

func toBifrostGigaChatBatchRetrieveResponse(providerName schemas.ModelProvider, batch GigaChatBatch, fallbackEndpoint string, latency time.Duration) *schemas.BifrostBatchRetrieveResponse {
	inputFileID := ""
	if batch.InputFileID != nil {
		inputFileID = strings.TrimSpace(*batch.InputFileID)
	}
	endpoint := fallbackEndpoint
	if strings.TrimSpace(endpoint) == "" {
		endpoint = toBifrostGigaChatBatchEndpoint(batch.Method)
	}

	return &schemas.BifrostBatchRetrieveResponse{
		ID:                  batch.ID,
		Object:              toBifrostGigaChatBatchObject(batch.Object),
		Endpoint:            endpoint,
		InputFileID:         inputFileID,
		CompletionWindow:    batch.CompletionWindow,
		Status:              toBifrostGigaChatBatchStatus(batch.Status),
		RequestCounts:       toBifrostGigaChatBatchRequestCounts(batch.RequestCounts),
		CreatedAt:           batch.CreatedAt,
		CompletedAt:         batch.CompletedAt,
		OutputFileID:        toBifrostGigaChatBatchOutputFileID(batch),
		ErrorFileID:         cleanGigaChatBatchFileID(batch.ErrorFileID),
		ProviderExtraFields: toBifrostGigaChatBatchProviderExtraFields(batch),
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: providerName,
			Latency:  latency.Milliseconds(),
		},
	}
}

func toBifrostGigaChatBatchRequestCounts(counts *GigaChatBatchRequestCounts) schemas.BatchRequestCounts {
	if counts == nil {
		return schemas.BatchRequestCounts{}
	}
	return schemas.BatchRequestCounts{
		Total:     counts.Total,
		Completed: counts.Completed,
		Failed:    counts.Failed,
	}
}

func toBifrostGigaChatBatchEndpoint(method GigaChatBatchMethod) string {
	switch method {
	case GigaChatBatchMethodChatCompletions:
		return string(schemas.BatchEndpointChatCompletions)
	case GigaChatBatchMethodResponses:
		return string(schemas.BatchEndpointResponses)
	case GigaChatBatchMethodEmbedder:
		return string(schemas.BatchEndpointEmbeddings)
	default:
		return ""
	}
}

func toBifrostGigaChatBatchObject(object string) string {
	if strings.TrimSpace(object) == "" {
		return "batch"
	}
	return object
}

func cleanGigaChatBatchFileID(fileID *string) *string {
	if fileID == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*fileID)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func toBifrostGigaChatBatchOutputFileID(batch GigaChatBatch) *string {
	if outputFileID := cleanGigaChatBatchFileID(batch.OutputFileID); outputFileID != nil {
		return outputFileID
	}
	return cleanGigaChatBatchFileID(batch.ResultFileID)
}

func (provider *GigaChatProvider) readGigaChatBatchOutputFile(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest, fileID string) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	fileRequest := &schemas.BifrostFileContentRequest{
		Provider: provider.GetProviderKey(),
		Model:    request.Model,
		FileID:   strings.TrimSpace(fileID),
	}
	var lastErr *schemas.BifrostError
	for _, key := range keys {
		response, bifrostErr := provider.fileContentWithRefresh(ctx, key, fileRequest, false)
		if isGigaChatUnauthorizedError(bifrostErr) {
			response, bifrostErr = provider.fileContentWithRefresh(ctx, key, fileRequest, true)
		}
		if bifrostErr == nil {
			return response, nil
		}
		lastErr = bifrostErr
	}
	return nil, lastErr
}

func parseGigaChatBatchResultsJSONL(content []byte, logger schemas.Logger) ([]schemas.BatchResultItem, []schemas.BatchError) {
	results := make([]schemas.BatchResultItem, 0)
	parseResult := providerUtils.ParseJSONL(content, func(line []byte) error {
		var resultRow GigaChatBatchResultRow
		if err := json.Unmarshal(line, &resultRow); err != nil {
			if logger != nil {
				logger.Warn("failed to parse GigaChat batch result line: %v", err)
			}
			return err
		}

		result := schemas.BatchResultItem{
			CustomID: strings.TrimSpace(resultRow.CustomID),
			Response: resultRow.Response,
			Result:   resultRow.Result,
			Error:    resultRow.Error,
		}
		if result.CustomID == "" {
			result.CustomID = strings.TrimSpace(resultRow.ID)
		}
		results = append(results, result)
		return nil
	})
	return results, parseResult.Errors
}

func cloneGigaChatBatchProviderExtraFields(fields map[string]interface{}) map[string]interface{} {
	if len(fields) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}

func withGigaChatQuery(path string, values url.Values) string {
	encoded := values.Encode()
	if encoded == "" {
		return path
	}
	if strings.Contains(path, "?") {
		return path + "&" + encoded
	}
	return path + "?" + encoded
}

func convertGigaChatBatchRequestItemsToJSONL(endpoint schemas.BatchEndpoint, requests []schemas.BatchRequestItem) ([]byte, error) {
	var buf bytes.Buffer
	for index, request := range requests {
		row, err := toGigaChatBatchInputRowFromRequestItem(endpoint, request)
		if err != nil {
			return nil, fmt.Errorf("requests[%d]: %w", index, err)
		}
		if err := writeGigaChatBatchInputRow(&buf, row); err != nil {
			return nil, fmt.Errorf("requests[%d]: %w", index, err)
		}
	}
	return buf.Bytes(), nil
}

func convertGigaChatBatchInputJSONL(endpoint schemas.BatchEndpoint, input []byte) ([]byte, error) {
	var buf bytes.Buffer
	parseResult := providerUtils.ParseJSONL(input, func(line []byte) error {
		row, err := toGigaChatBatchInputRowFromJSONLine(endpoint, line)
		if err != nil {
			return err
		}
		return writeGigaChatBatchInputRow(&buf, row)
	})
	if len(parseResult.Errors) > 0 {
		return nil, formatGigaChatBatchJSONLErrors(parseResult.Errors)
	}
	return buf.Bytes(), nil
}

func toGigaChatBatchInputRowFromRequestItem(defaultEndpoint schemas.BatchEndpoint, request schemas.BatchRequestItem) (GigaChatBatchInputRow, error) {
	if len(request.Params) > 0 {
		return GigaChatBatchInputRow{}, fmt.Errorf("params are not supported by GigaChat batch row conversion")
	}
	if request.Body == nil {
		return GigaChatBatchInputRow{}, fmt.Errorf("body is required")
	}
	body, err := schemas.MarshalSorted(request.Body)
	if err != nil {
		return GigaChatBatchInputRow{}, fmt.Errorf("marshal body: %w", err)
	}
	row := openAICompatibleBatchInputRow{
		CustomID: request.CustomID,
		Method:   request.Method,
		URL:      request.URL,
		Body:     body,
	}
	return toGigaChatBatchInputRow(defaultEndpoint, row)
}

func toGigaChatBatchInputRowFromJSONLine(defaultEndpoint schemas.BatchEndpoint, line []byte) (GigaChatBatchInputRow, error) {
	var row openAICompatibleBatchInputRow
	if err := json.Unmarshal(line, &row); err != nil {
		return GigaChatBatchInputRow{}, fmt.Errorf("decode batch row: %w", err)
	}
	return toGigaChatBatchInputRow(defaultEndpoint, row)
}

func toGigaChatBatchInputRow(defaultEndpoint schemas.BatchEndpoint, row openAICompatibleBatchInputRow) (GigaChatBatchInputRow, error) {
	customID := strings.TrimSpace(row.CustomID)
	if customID == "" {
		return GigaChatBatchInputRow{}, fmt.Errorf("custom_id is required")
	}
	if method := strings.TrimSpace(row.Method); method != "" && !strings.EqualFold(method, http.MethodPost) {
		return GigaChatBatchInputRow{}, fmt.Errorf("method %q is not supported by GigaChat batches", row.Method)
	}
	if len(bytes.TrimSpace(row.Body)) == 0 {
		return GigaChatBatchInputRow{}, fmt.Errorf("body is required")
	}

	endpoint := defaultEndpoint
	if strings.TrimSpace(row.URL) != "" {
		endpoint = schemas.BatchEndpoint(row.URL)
	}
	request, err := toGigaChatBatchRequestBody(endpoint, row.Body)
	if err != nil {
		return GigaChatBatchInputRow{}, err
	}
	return GigaChatBatchInputRow{
		ID:      customID,
		Request: request,
	}, nil
}

func toGigaChatBatchRequestBody(endpoint schemas.BatchEndpoint, body json.RawMessage) (json.RawMessage, error) {
	switch normalizeGigaChatBatchEndpoint(string(endpoint)) {
	case "/v1/chat/completions", "/chat/completions":
		var request openaiProvider.OpenAIChatRequest
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, fmt.Errorf("decode chat completion body: %w", err)
		}
		bifrostReq := request.ToBifrostChatRequest(gigaChatBatchConversionContext())
		if request.MaxTokens != nil {
			if bifrostReq.Params == nil {
				bifrostReq.Params = &schemas.ChatParameters{}
			}
			if bifrostReq.Params.MaxCompletionTokens == nil {
				bifrostReq.Params.MaxCompletionTokens = request.MaxTokens
			}
		}
		bifrostReq.Provider = schemas.GigaChat
		gigaChatReq, err := ToGigaChatChatRequest(gigaChatBatchConversionContext(), bifrostReq)
		if err != nil {
			return nil, err
		}
		return marshalGigaChatBatchRequest(gigaChatReq)
	case "/v1/responses", "/responses":
		var request openaiProvider.OpenAIResponsesRequest
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, fmt.Errorf("decode responses body: %w", err)
		}
		bifrostReq := request.ToBifrostResponsesRequest(gigaChatBatchConversionContext())
		bifrostReq.Provider = schemas.GigaChat
		gigaChatReq, err := ToGigaChatResponsesRequest(bifrostReq)
		if err != nil {
			return nil, err
		}
		return marshalGigaChatBatchRequest(gigaChatReq)
	case "/v1/embeddings", "/embeddings":
		var request openaiProvider.OpenAIEmbeddingRequest
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, fmt.Errorf("decode embeddings body: %w", err)
		}
		bifrostReq := request.ToBifrostEmbeddingRequest(gigaChatBatchConversionContext())
		bifrostReq.Provider = schemas.GigaChat
		gigaChatReq, err := ToGigaChatEmbeddingRequest(bifrostReq)
		if err != nil {
			return nil, err
		}
		return marshalGigaChatBatchRequest(gigaChatReq)
	default:
		return nil, fmt.Errorf("GigaChat batches do not support endpoint %q", endpoint)
	}
}

func marshalGigaChatBatchRequest(request providerUtils.RequestBodyWithExtraParams) (json.RawMessage, error) {
	body, err := schemas.MarshalSorted(request)
	if err != nil {
		return nil, fmt.Errorf("marshal GigaChat batch request: %w", err)
	}
	if extraParams := request.GetExtraParams(); len(extraParams) > 0 {
		body, err = providerUtils.MergeExtraParamsIntoJSON(body, extraParams)
		if err != nil {
			return nil, fmt.Errorf("merge GigaChat batch request extra params: %w", err)
		}
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, body); err != nil {
		return nil, fmt.Errorf("compact GigaChat batch request: %w", err)
	}
	return json.RawMessage(compacted.Bytes()), nil
}

func writeGigaChatBatchInputRow(buf *bytes.Buffer, row GigaChatBatchInputRow) error {
	line, err := schemas.MarshalSorted(row)
	if err != nil {
		return err
	}
	buf.Write(line)
	buf.WriteByte('\n')
	return nil
}

func formatGigaChatBatchJSONLErrors(errors []schemas.BatchError) error {
	if len(errors) == 0 {
		return nil
	}
	messages := make([]string, 0, len(errors))
	for _, parseErr := range errors {
		if parseErr.Line != nil {
			messages = append(messages, fmt.Sprintf("line %d: %s", *parseErr.Line, parseErr.Message))
		} else {
			messages = append(messages, parseErr.Message)
		}
	}
	return fmt.Errorf("failed to convert GigaChat batch JSONL: %s", strings.Join(messages, "; "))
}

func gigaChatBatchConversionContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(nil, schemas.NoDeadline)
}
