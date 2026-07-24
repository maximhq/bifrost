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
	if key.Value.GetValue() != "" {
		req.Header.Set("x-goog-api-key", key.Value.GetValue())
	}

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
		usage := map[string]interface{}{
			"prompt_tokens":     resp.UsageMetadata.PromptTokenCount,
			"completion_tokens": resp.UsageMetadata.CandidatesTokenCount,
			"total_tokens":      resp.UsageMetadata.TotalTokenCount,
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
	} else if inlineResp.Response != nil {
		resultItem.Response = &schemas.BatchResultResponse{
			StatusCode: 200,
			Body:       geminiGenerateContentToBatchResultBody(inlineResp.Response),
		}
	}
	return resultItem
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
