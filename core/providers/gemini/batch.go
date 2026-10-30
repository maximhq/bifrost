package gemini

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToGeminiBatchGenerateContentRequest converts one Bifrost batch request body into the
// Gemini GenerateContentRequest shape used for batch input. OpenAI-style bodies (with
// "messages") have their messages converted to Gemini "contents"/"systemInstruction";
// bodies already in Gemini form are unmarshaled directly. Shared by the Gemini and Vertex
// batch paths so both emit identical request bodies.
func ToGeminiBatchGenerateContentRequest(body map[string]interface{}) (GeminiBatchGenerateContentRequest, error) {
	var geminiReq GeminiBatchGenerateContentRequest

	requestBytes, err := providerUtils.MarshalSorted(body)
	if err != nil {
		return geminiReq, fmt.Errorf("failed to marshal gemini request: %w", err)
	}
	if err := sonic.Unmarshal(requestBytes, &geminiReq); err != nil {
		return geminiReq, fmt.Errorf("failed to unmarshal gemini request: %w", err)
	}

	// OpenAI-style body: convert "messages" into Gemini "contents"/"systemInstruction".
	if rawMessages, ok := body["messages"]; ok {
		messagesBytes, err := providerUtils.MarshalSorted(rawMessages)
		if err != nil {
			return geminiReq, fmt.Errorf("failed to marshal messages: %w", err)
		}
		var chatMessages []schemas.ChatMessage
		if err := sonic.Unmarshal(messagesBytes, &chatMessages); err != nil {
			return geminiReq, fmt.Errorf("failed to unmarshal messages: %w", err)
		}
		contents, systemInstruction, err := convertBifrostMessagesToGemini(chatMessages)
		if err != nil {
			return geminiReq, fmt.Errorf("failed to convert messages: %w", err)
		}
		geminiReq.Contents = contents
		geminiReq.SystemInstruction = systemInstruction
	}

	return geminiReq, nil
}

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
		return schemas.BatchStatus(geminiState)
	}
}

// ToGeminiBatchStatus converts Bifrost batch status to Gemini batch job state.
func ToGeminiBatchStatus(status schemas.BatchStatus) string {
	switch status {
	case schemas.BatchStatusValidating, schemas.BatchStatusInProgress:
		return GeminiBatchStateRunning
	case schemas.BatchStatusFinalizing:
		return GeminiBatchStateRunning
	case schemas.BatchStatusCompleted, schemas.BatchStatusEnded:
		return GeminiBatchStateSucceeded
	case schemas.BatchStatusFailed:
		return GeminiBatchStateFailed
	case schemas.BatchStatusCancelling:
		return GeminiBatchStateCancelling
	case schemas.BatchStatusCancelled:
		return GeminiBatchStateCancelled
	case schemas.BatchStatusExpired:
		return GeminiBatchStateExpired
	default:
		return GeminiBatchStateUnspecified
	}
}

// ToGeminiBatchJobResponse converts Bifrost batch create response to Gemini batch job response format.
func ToGeminiBatchJobResponse(resp *schemas.BifrostBatchCreateResponse) *GeminiBatchJobResponse {
	if resp == nil {
		return nil
	}

	succeededCount := resp.RequestCounts.Succeeded
	if succeededCount == 0 {
		succeededCount = resp.RequestCounts.Completed
	}

	geminiResp := &GeminiBatchJobResponse{
		Name: resp.ID,
		Metadata: &GeminiBatchMetadata{
			Name:       resp.ID,
			Type:       "type.googleapis.com/google.ai.generativelanguage.v1beta.BatchPredictionJob",
			CreateTime: formatGeminiTimestamp(resp.CreatedAt),
			UpdateTime: formatGeminiTimestamp(resp.CreatedAt),
			State:      ToGeminiBatchStatus(resp.Status),
			BatchStats: &GeminiBatchStats{
				RequestCount:           resp.RequestCounts.Total,
				PendingRequestCount:    max(0, resp.RequestCounts.Total-succeededCount-resp.RequestCounts.Failed),
				SuccessfulRequestCount: succeededCount,
			},
		},
	}

	if resp.OperationName != nil && *resp.OperationName != "" {
		geminiResp.Metadata.Name = *resp.OperationName
		geminiResp.Name = *resp.OperationName
	}

	if resp.InputFileID != "" {
		geminiResp.Metadata.InputConfig = &GeminiBatchMetadataInputConfig{
			FileName: resp.InputFileID,
		}
	}

	if resp.OutputFileID != nil && *resp.OutputFileID != "" {
		geminiResp.Dest = &GeminiBatchDest{
			FileName: *resp.OutputFileID,
		}
		geminiResp.Metadata.Output = &GeminiBatchMetadataOutputConfig{
			ResponsesFile: *resp.OutputFileID,
		}
	}

	if resp.Status == schemas.BatchStatusCompleted ||
		resp.Status == schemas.BatchStatusEnded ||
		resp.Status == schemas.BatchStatusFailed ||
		resp.Status == schemas.BatchStatusExpired ||
		resp.Status == schemas.BatchStatusCancelled {
		geminiResp.Done = true
	}

	return geminiResp
}

// ToGeminiBatchRetrieveResponse converts a Bifrost batch retrieve response to Gemini batch job response format.
func ToGeminiBatchRetrieveResponse(resp *schemas.BifrostBatchRetrieveResponse) *GeminiBatchJobResponse {
	if resp == nil {
		return nil
	}

	succeededCount := resp.RequestCounts.Succeeded
	if succeededCount == 0 {
		succeededCount = resp.RequestCounts.Completed
	}

	pendingCount := resp.RequestCounts.Pending
	if pendingCount == 0 && resp.RequestCounts.Total > 0 {
		processedCount := resp.RequestCounts.Completed
		if processedCount == 0 {
			processedCount = succeededCount
		}
		pendingCount = resp.RequestCounts.Total - processedCount - resp.RequestCounts.Failed
		if pendingCount < 0 {
			pendingCount = 0
		}
	}

	geminiResp := &GeminiBatchJobResponse{
		Name: resp.ID,
		Metadata: &GeminiBatchMetadata{
			Name:       resp.ID,
			Type:       "type.googleapis.com/google.ai.generativelanguage.v1beta.BatchPredictionJob",
			CreateTime: formatGeminiTimestamp(resp.CreatedAt),
			UpdateTime: formatGeminiTimestamp(resp.CreatedAt),
			State:      ToGeminiBatchStatus(resp.Status),
			BatchStats: &GeminiBatchStats{
				RequestCount:           resp.RequestCounts.Total,
				PendingRequestCount:    pendingCount,
				SuccessfulRequestCount: succeededCount,
			},
		},
	}

	if resp.OperationName != nil && *resp.OperationName != "" {
		geminiResp.Metadata.Name = *resp.OperationName
		geminiResp.Name = *resp.OperationName
	}

	if resp.Done != nil {
		geminiResp.Done = *resp.Done
	} else {
		geminiResp.Done = resp.Status == schemas.BatchStatusCompleted ||
			resp.Status == schemas.BatchStatusEnded ||
			resp.Status == schemas.BatchStatusFailed ||
			resp.Status == schemas.BatchStatusExpired ||
			resp.Status == schemas.BatchStatusCancelled
	}

	if resp.InputFileID != "" {
		geminiResp.Metadata.InputConfig = &GeminiBatchMetadataInputConfig{
			FileName: resp.InputFileID,
		}
	}

	if resp.OutputFileID != nil && *resp.OutputFileID != "" {
		geminiResp.Dest = &GeminiBatchDest{
			FileName: *resp.OutputFileID,
		}
		geminiResp.Metadata.Output = &GeminiBatchMetadataOutputConfig{
			ResponsesFile: *resp.OutputFileID,
		}
	}

	// Set end time from the most relevant terminal timestamp
	var endTime int64
	if resp.CompletedAt != nil {
		endTime = *resp.CompletedAt
	} else if resp.FailedAt != nil {
		endTime = *resp.FailedAt
	} else if resp.ExpiredAt != nil {
		endTime = *resp.ExpiredAt
	} else if resp.CancelledAt != nil {
		endTime = *resp.CancelledAt
	}
	if endTime > 0 {
		geminiResp.Metadata.EndTime = formatGeminiTimestamp(endTime)
	}

	return geminiResp
}

// ToGeminiBatchListResponse converts a Bifrost batch list response to Gemini format.
func ToGeminiBatchListResponse(resp *schemas.BifrostBatchListResponse) *GeminiBatchListResponse {
	if resp == nil {
		return nil
	}

	operations := make([]GeminiBatchJobResponse, 0, len(resp.Data))
	for i := range resp.Data {
		if geminiResp := ToGeminiBatchRetrieveResponse(&resp.Data[i]); geminiResp != nil {
			operations = append(operations, *geminiResp)
		}
	}

	geminiListResp := &GeminiBatchListResponse{
		Operations: operations,
	}

	if resp.NextCursor != nil {
		geminiListResp.NextPageToken = *resp.NextCursor
	}

	return geminiListResp
}

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

// downloadBatchResultsFile downloads and parses a batch results file from Gemini.
// Returns the parsed result items from the JSONL file and any parse errors encountered.
func (provider *GeminiProvider) downloadBatchResultsFile(ctx context.Context, key schemas.Key, fileName string) ([]schemas.BatchResultItem, []schemas.BatchError, *schemas.BifrostError) {
	// Create request to download the file
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build download URL - use the download endpoint with alt=media
	// The base URL is like https://generativelanguage.googleapis.com/v1beta
	// We need to change it to https://generativelanguage.googleapis.com/download/v1beta
	baseURL := strings.Replace(provider.networkConfig.BaseURL, "/v1beta", "/download/v1beta", 1)

	filePath, bifrostErr := geminiResourcePath(fileName, "files", "file_id")
	if bifrostErr != nil {
		return nil, nil, bifrostErr
	}

	url := fmt.Sprintf("%s/%s:download?alt=media", baseURL, filePath)

	provider.logger.Debug("gemini batch results file download url: " + url)
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	setGeminiAuthHeader(req, key.Value.GetValue())

	// Make request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, nil, providerUtils.SetErrorLatency(parseGeminiError(resp), latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	results, parseErrors := parseGeminiBatchResultsJSONL(body, provider.logger.Warn)
	return results, parseErrors, nil
}

func parseGeminiBatchResultsJSONL(body []byte, warnf func(string, ...any)) ([]schemas.BatchResultItem, []schemas.BatchError) {
	results := make([]schemas.BatchResultItem, 0)
	lineIndex := 0
	parseResult := providerUtils.ParseJSONL(body, func(line []byte) error {
		sourceIndex := lineIndex
		lineIndex++

		var resultLine GeminiBatchFileResultLine
		if err := sonic.Unmarshal(line, &resultLine); err != nil {
			if warnf != nil {
				warnf("gemini batch results file parse error: %s", strconv.QuoteToASCII(err.Error()))
			}
			return err
		}

		resultItem, err := geminiFileResultToBatchResultItem(resultLine, fmt.Sprintf("request-%d", sourceIndex))
		if err != nil {
			if warnf != nil {
				warnf("gemini batch results file response parse error: %s", strconv.QuoteToASCII(err.Error()))
			}
			return err
		}
		results = append(results, resultItem)
		return nil
	})

	return results, parseResult.Errors
}

func geminiFileResultToBatchResultItem(resultLine GeminiBatchFileResultLine, fallbackID string) (schemas.BatchResultItem, error) {
	customID := resultLine.CustomID
	if customID == "" {
		customID = resultLine.Key
	}
	if customID == "" {
		customID = fallbackID
	}
	item := schemas.BatchResultItem{CustomID: customID}
	if resultLine.Error != nil {
		item.Error = &schemas.BatchResultError{
			Code:    fmt.Sprintf("%d", resultLine.Error.Code),
			Message: resultLine.Error.Message,
		}
		return item, nil
	}
	if len(resultLine.Response) == 0 || strings.TrimSpace(string(resultLine.Response)) == "null" {
		return schemas.BatchResultItem{}, fmt.Errorf("gemini batch result line has neither response nor error")
	}

	var envelope GeminiFileResponseLine
	if err := sonic.Unmarshal(resultLine.Response, &envelope); err != nil {
		return item, err
	}
	if envelope.StatusCode != 0 || envelope.Body != nil {
		item.Response = &schemas.BatchResultResponse{
			StatusCode: envelope.StatusCode,
			Body:       normalizeBatchBody(envelope.Body),
		}
		return item, nil
	}

	var embedding GeminiEmbeddingResponse
	if err := sonic.Unmarshal(resultLine.Response, &embedding); err == nil && len(geminiResponseEmbeddings(&embedding)) > 0 {
		item.Response = &schemas.BatchResultResponse{
			StatusCode: http.StatusOK,
			Body:       geminiEmbeddingToBatchResultBody(&embedding),
		}
		return item, nil
	}

	var native GenerateContentResponse
	if err := sonic.Unmarshal(resultLine.Response, &native); err != nil {
		return item, err
	}
	item.Response = &schemas.BatchResultResponse{
		StatusCode: http.StatusOK,
		Body:       geminiGenerateContentToBatchResultBody(&native),
	}
	return item, nil
}

// geminiBatchOutput extracts the batch output (a responses file name or inline responses)
// from a Gemini batch job response. The generativelanguage REST API reports the output
// under the Operation's top-level `response` field, mirrored in `metadata.output`; the
// `dest` field is a client-SDK-only abstraction and is never present on the wire. The
// top-level response is preferred, with metadata.output as a fallback.
func geminiBatchOutput(resp *GeminiBatchJobResponse) (fileName string, inlined []GeminiInlinedResponse) {
	if resp == nil {
		return "", nil
	}
	if resp.Response != nil {
		fileName = resp.Response.ResponsesFile
		if resp.Response.InlinedResponses != nil {
			inlined = resp.Response.InlinedResponses.InlinedResponses
		}
	}
	if fileName == "" && len(inlined) == 0 && resp.Metadata != nil && resp.Metadata.Output != nil {
		fileName = resp.Metadata.Output.ResponsesFile
		if resp.Metadata.Output.InlinedResponses != nil {
			inlined = resp.Metadata.Output.InlinedResponses.InlinedResponses
		}
	}
	return fileName, inlined
}

// geminiGenerateContentToBatchResultBody flattens a Gemini GenerateContentResponse into
// the compact result body shape shared by the inline and file-based batch result paths.
func geminiGenerateContentToBatchResultBody(resp *GenerateContentResponse) map[string]interface{} {
	body := make(map[string]interface{})
	if resp == nil {
		return body
	}
	if len(resp.Candidates) > 0 {
		candidate := resp.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			var textParts []string
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					textParts = append(textParts, part.Text)
				}
			}
			if len(textParts) > 0 {
				body["text"] = strings.Join(textParts, "")
			}
		}
		body["finish_reason"] = string(candidate.FinishReason)
	}
	if resp.UsageMetadata != nil {
		completionTokens := resp.UsageMetadata.CandidatesTokenCount + resp.UsageMetadata.ThoughtsTokenCount
		usage := map[string]interface{}{
			"prompt_tokens":     resp.UsageMetadata.PromptTokenCount,
			"completion_tokens": completionTokens,
			"total_tokens":      resp.UsageMetadata.TotalTokenCount,
		}
		if resp.UsageMetadata.ThoughtsTokenCount > 0 {
			usage["completion_tokens_details"] = map[string]interface{}{
				"reasoning_tokens": resp.UsageMetadata.ThoughtsTokenCount,
			}
		}
		// Surface cached input tokens in OpenAI's shape so batch accounting can
		// apply the cache-read discount. Like OpenAI's prompt_tokens, Gemini's
		// PromptTokenCount already includes these, so this is a breakdown of
		// prompt_tokens rather than an addition to it.
		if resp.UsageMetadata.CachedContentTokenCount > 0 {
			usage["prompt_tokens_details"] = map[string]interface{}{
				"cached_tokens": resp.UsageMetadata.CachedContentTokenCount,
			}
		}
		body["usage"] = usage
	}
	return body
}

// geminiInlineResponseToBatchResultItem converts a single Gemini inline batch response
// into a Bifrost BatchResultItem. customIDFallback is used when the response carries no
// metadata key.
func geminiInlineResponseToBatchResultItem(inlineResp GeminiInlinedResponse, customIDFallback string) schemas.BatchResultItem {
	customID := customIDFallback
	if inlineResp.Metadata != nil && inlineResp.Metadata.Key != "" {
		customID = inlineResp.Metadata.Key
	}

	resultItem := schemas.BatchResultItem{CustomID: customID}
	if inlineResp.Error != nil {
		resultItem.Error = &schemas.BatchResultError{
			Code:    fmt.Sprintf("%d", inlineResp.Error.Code),
			Message: inlineResp.Error.Message,
		}
	} else if inlineResp.Response != nil && inlineResp.Response.Embedding != nil {
		resultItem.Response = &schemas.BatchResultResponse{
			StatusCode: 200,
			Body: geminiEmbeddingToBatchResultBody(&GeminiEmbeddingResponse{
				Embedding:     inlineResp.Response.Embedding,
				UsageMetadata: inlineResp.Response.UsageMetadata,
			}),
		}
	} else if inlineResp.Response != nil {
		resultItem.Response = &schemas.BatchResultResponse{
			StatusCode: 200,
			Body:       geminiGenerateContentToBatchResultBody(inlineResp.Response),
		}
	}
	return resultItem
}

// geminiEmbeddingToBatchResultBody shapes an embeddings batch response like an OpenAI
// /v1/embeddings body so batch consumers read vectors the same way as a direct call.
func geminiEmbeddingToBatchResultBody(resp *GeminiEmbeddingResponse) map[string]interface{} {
	embeddings := geminiResponseEmbeddings(resp)
	data := make([]map[string]interface{}, len(embeddings))
	for i, embedding := range embeddings {
		data[i] = map[string]interface{}{
			"object":    "embedding",
			"index":     i,
			"embedding": embedding.Values,
		}
	}
	body := map[string]interface{}{
		"object": "list",
		"data":   data,
	}
	if resp.UsageMetadata != nil {
		// Embedding usage may carry only the prompt count, and input is all it bills.
		promptTokens := int(resp.UsageMetadata.PromptTokenCount)
		totalTokens := int(resp.UsageMetadata.TotalTokenCount)
		if totalTokens == 0 {
			totalTokens = promptTokens
		}
		body["usage"] = map[string]interface{}{
			"prompt_tokens": promptTokens,
			"total_tokens":  totalTokens,
		}
	}
	return body
}

// ToGeminiBatchEmbeddingRequest converts one Bifrost batch request body into a Gemini
// embedding request. OpenAI-style bodies ("input", "dimensions") are converted; bodies
// already in Gemini form ("content") are unmarshaled directly. A batch line embeds
// exactly one text, matching how results are keyed per line.
func ToGeminiBatchEmbeddingRequest(body map[string]interface{}) (GeminiEmbeddingRequest, error) {
	var req GeminiEmbeddingRequest
	if _, native := body["content"]; native {
		raw, err := providerUtils.MarshalSorted(body)
		if err != nil {
			return req, fmt.Errorf("failed to marshal gemini embedding request: %w", err)
		}
		if err := sonic.Unmarshal(raw, &req); err != nil {
			return req, fmt.Errorf("failed to unmarshal gemini embedding request: %w", err)
		}
		return req, nil
	}

	// Round-trip through JSON so Go-typed bodies ([]string, int) read the same as decoded ones.
	raw, err := providerUtils.MarshalSorted(body)
	if err != nil {
		return req, fmt.Errorf("failed to marshal embedding batch request: %w", err)
	}
	var openAI struct {
		Input      interface{} `json:"input"`
		Dimensions *int        `json:"dimensions"`
	}
	if err := sonic.Unmarshal(raw, &openAI); err != nil {
		return req, fmt.Errorf("failed to unmarshal embedding batch request: %w", err)
	}

	var text string
	switch input := openAI.Input.(type) {
	case string:
		text = input
	case []interface{}:
		if len(input) != 1 {
			return req, fmt.Errorf("embedding batch requests take exactly one input per line, got %d", len(input))
		}
		first, ok := input[0].(string)
		if !ok {
			return req, fmt.Errorf("embedding batch input must be a string")
		}
		text = first
	default:
		return req, fmt.Errorf("embedding batch request is missing a string input")
	}
	req.Content = &Content{Parts: []*Part{{Text: text}}}
	if openAI.Dimensions != nil && *openAI.Dimensions > 0 {
		req.OutputDimensionality = openAI.Dimensions
	}
	return req, nil
}

// normalizeBatchBody converts camelCase usage keys (promptTokens, completionTokens,
// totalTokens) to snake_case as expected by the batch accounting pricing code.
func normalizeBatchBody(body map[string]interface{}) map[string]interface{} {
	if body == nil {
		return nil
	}
	out := make(map[string]interface{}, len(body))
	for k, v := range body {
		out[k] = v
	}
	if raw, ok := body["usage"]; ok {
		if usage, ok := raw.(map[string]interface{}); ok {
			normalized := make(map[string]interface{}, len(usage))
			for k, v := range usage {
				switch k {
				case "promptTokens":
					normalized["prompt_tokens"] = v
				case "completionTokens":
					normalized["completion_tokens"] = v
				case "totalTokens":
					normalized["total_tokens"] = v
				default:
					normalized[k] = v
				}
			}
			out["usage"] = normalized
		}
	}
	return out
}

// extractGeminiUsageMetadata extracts usage metadata (as ints) from Gemini response
func extractGeminiUsageMetadata(geminiResponse *GenerateContentResponse) (int, int, int) {
	var inputTokens, outputTokens, totalTokens int
	if geminiResponse.UsageMetadata != nil {
		usageMetadata := geminiResponse.UsageMetadata
		inputTokens = int(usageMetadata.PromptTokenCount)
		outputTokens = int(usageMetadata.CandidatesTokenCount)
		totalTokens = int(usageMetadata.TotalTokenCount)
	}
	return inputTokens, outputTokens, totalTokens
}

// isGeminiEmbeddingBatch reports whether a batch create request targets embeddings, by
// its OpenAI endpoint or by an embedding model name. Gemini serves these through a
// different batch method than generate-content jobs.
func isGeminiEmbeddingBatch(request *schemas.BifrostBatchCreateRequest, model string) bool {
	return request.Endpoint == schemas.BatchEndpointEmbeddings || strings.Contains(strings.ToLower(model), "embedding")
}

// buildGeminiEmbeddingBatchBody builds the asyncBatchEmbedContent body from either an
// uploaded input file or inline requests.
func buildGeminiEmbeddingBatchBody(request *schemas.BifrostBatchCreateRequest, model string) ([]byte, *schemas.BifrostError) {
	hasFileInput := request.InputFileID != ""
	if !hasFileInput && len(request.Requests) == 0 {
		return nil, providerUtils.NewBifrostOperationError("either input_file_id or requests must be provided", nil)
	}
	if hasFileInput && len(request.Requests) > 0 {
		return nil, providerUtils.NewBifrostOperationError("cannot specify both input_file_id and requests", nil)
	}

	displayName := fmt.Sprintf("bifrost-batch-%d", time.Now().UnixNano())
	if request.DisplayName != nil && *request.DisplayName != "" {
		displayName = *request.DisplayName
	}
	batchReq := &GeminiBatchEmbedCreateRequest{Batch: GeminiBatchEmbedConfig{DisplayName: displayName}}

	if hasFileInput {
		fileID := request.InputFileID
		if !strings.HasPrefix(fileID, "files/") {
			fileID = "files/" + fileID
		}
		batchReq.Batch.InputConfig.FileName = fileID
	} else {
		items := make([]GeminiBatchEmbedRequestItem, len(request.Requests))
		for i, bifrostItem := range request.Requests {
			embedReq, err := ToGeminiBatchEmbeddingRequest(bifrostItem.Body)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("failed to convert batch request to gemini embedding format", err)
			}
			// Every request in an embeddings batch uses the batch's model.
			modelName := "models/" + model
			if embedReq.Model == "" {
				embedReq.Model = modelName
			} else if embedReq.Model != modelName {
				return nil, providerUtils.NewBifrostBadRequestError("embedding request model does not match the batch model")
			}
			items[i] = GeminiBatchEmbedRequestItem{Request: embedReq}
			if bifrostItem.CustomID != "" {
				items[i].Metadata = &GeminiBatchMetadata{Key: bifrostItem.CustomID}
			}
		}
		batchReq.Batch.InputConfig.Requests = &GeminiBatchEmbedRequestsWrapper{Requests: items}
	}

	jsonData, err := providerUtils.MarshalSorted(batchReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	return jsonData, nil
}

// geminiBatchResultsEndpoint tells accounting which endpoint a finished job served.
// Results carry no endpoint of their own, so embeddings are recognized by their
// OpenAI-style body; anything else is priced as chat, as before.
func geminiBatchResultsEndpoint(results []schemas.BatchResultItem) schemas.BatchEndpoint {
	for _, item := range results {
		if item.Response == nil {
			continue
		}
		if _, ok := item.Response.Body["data"].([]map[string]interface{}); ok && item.Response.Body["object"] == "list" {
			return schemas.BatchEndpointEmbeddings
		}
	}
	return schemas.BatchEndpointChatCompletions
}
