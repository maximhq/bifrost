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
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.BatchResultsRequest,
		})
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
