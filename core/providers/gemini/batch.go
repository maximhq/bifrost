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

		// Extract messages from the request data - handle multiple possible types
		// Go type assertions don't work across slice types, so we handle each case
		if requestData != nil {
			var messages []map[string]interface{}

			// Try []interface{} first (generic JSON unmarshaling)
			if msgsInterface, ok := requestData["messages"].([]interface{}); ok {
				for _, m := range msgsInterface {
					if msgMap, ok := m.(map[string]interface{}); ok {
						messages = append(messages, msgMap)
					}
				}
			} else if msgsTyped, ok := requestData["messages"].([]map[string]interface{}); ok {
				// Try []map[string]interface{} (typed maps)
				messages = msgsTyped
			} else if msgsString, ok := requestData["messages"].([]map[string]string); ok {
				// Try []map[string]string (test case format)
				for _, m := range msgsString {
					msgMap := make(map[string]interface{})
					for k, v := range m {
						msgMap[k] = v
					}
					messages = append(messages, msgMap)
				}
			}

			// Process extracted messages
			for _, msgMap := range messages {
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
