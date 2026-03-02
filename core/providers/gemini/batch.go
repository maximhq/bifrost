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
		return schemas.BatchStatus(geminiState)
	}
}

// ToGeminiBatchStatus converts Bifrost batch status to Gemini batch job state.
func ToGeminiBatchStatus(status schemas.BatchStatus) string {
	switch status {
	case schemas.BatchStatusValidating, schemas.BatchStatusInProgress:
		return GeminiBatchStateRunning
	case schemas.BatchStatusCompleted:
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
				PendingRequestCount:    resp.RequestCounts.Total - resp.RequestCounts.Completed - resp.RequestCounts.Failed,
				SuccessfulRequestCount: resp.RequestCounts.Completed,
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

	if resp.Status == schemas.BatchStatusCompleted || resp.Status == schemas.BatchStatusFailed || resp.Status == schemas.BatchStatusExpired {
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
		pendingCount = resp.RequestCounts.Total - resp.RequestCounts.Completed - resp.RequestCounts.Failed
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

// extractBatchIDFromName extracts the batch ID from the full resource name.
// e.g., "batches/abc123" -> "batches/abc123"
func extractBatchIDFromName(name string) string {
	return name
}

// downloadBatchResultsFile downloads and parses a batch results file from Gemini.
// Returns the parsed result items from the JSONL file and any parse errors encountered.
func (provider *GeminiProvider) downloadBatchResultsFile(ctx context.Context, key schemas.Key, fileName string) ([]schemas.BatchResultItem, []schemas.BatchError, *schemas.BifrostError) {
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
	if key.Value.GetValue() != "" {
		req.Header.Set("x-goog-api-key", key.Value.GetValue())
	}

	// Make request
	_, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.BatchResultsRequest,
		})
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Parse JSONL content - each line is a separate JSON object
	// Use streaming parser to avoid string conversion and collect parse errors
	results := make([]schemas.BatchResultItem, 0)

	parseResult := providerUtils.ParseJSONL(body, func(line []byte) error {
		var resultLine GeminiBatchFileResultLine
		if err := sonic.Unmarshal(line, &resultLine); err != nil {
			provider.logger.Warn("gemini batch results file parse error: " + err.Error())
			return err
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
		return nil
	})

	return results, parseResult.Errors, nil
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
