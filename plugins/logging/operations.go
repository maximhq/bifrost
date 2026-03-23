// Package logging provides database operations for the GORM-based logging plugin
package logging

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/streaming"
)

// insertInitialLogEntry creates a new log entry in the database using GORM
func (p *LoggerPlugin) insertInitialLogEntry(
	ctx context.Context,
	requestID string,
	parentRequestID string,
	timestamp time.Time,
	fallbackIndex int,
	routingEnginesUsed []string, // list of routing engines used
	data *InitialLogData,
) error {
	entry := &logstore.Log{
		ID:            requestID,
		Timestamp:     timestamp,
		Object:        data.Object,
		Provider:      data.Provider,
		Model:         data.Model,
		FallbackIndex: fallbackIndex,
		Status:        "processing",
		Stream:        false,
		CreatedAt:     timestamp,
		// Set parsed fields for serialization
		InputHistoryParsed:          data.InputHistory,
		ResponsesInputHistoryParsed: data.ResponsesInputHistory,
		ParamsParsed:                data.Params,
		ToolsParsed:                 data.Tools,
		SpeechInputParsed:           data.SpeechInput,
		TranscriptionInputParsed:    data.TranscriptionInput,
		ImageGenerationInputParsed:  data.ImageGenerationInput,
		RoutingEnginesUsed:          routingEnginesUsed,
		MetadataParsed:              data.Metadata,
		VideoGenerationInputParsed:  data.VideoGenerationInput,
		PassthroughRequestBody:      data.PassthroughRequestBody,
	}
	if parentRequestID != "" {
		entry.ParentRequestID = &parentRequestID
	}
	return p.store.CreateIfNotExists(ctx, entry)
}

// applySerializedLogUpdates copies serialized fields from a temporary log entry
// into the GORM update map, respecting content-logging gates.
func applySerializedLogUpdates(
	updates map[string]interface{},
	entry *logstore.Log,
	data *UpdateLogData,
	cacheDebug *schemas.BifrostCacheDebug,
	contentLoggingEnabled bool,
) {
	if data.ChatOutput != nil && contentLoggingEnabled {
		updates["output_message"] = entry.OutputMessage
		updates["content_summary"] = entry.ContentSummary
	}

	if contentLoggingEnabled {
		if data.ResponsesOutput != nil {
			updates["responses_output"] = entry.ResponsesOutput
		}
		if data.ListModelsOutput != nil {
			updates["list_models_output"] = entry.ListModelsOutput
		}
		if data.EmbeddingOutput != nil {
			updates["embedding_output"] = entry.EmbeddingOutput
		}
		if data.RerankOutput != nil {
			updates["rerank_output"] = entry.RerankOutput
			updates["content_summary"] = entry.ContentSummary
		}
		if data.SpeechOutput != nil {
			updates["speech_output"] = entry.SpeechOutput
		}
		if data.TranscriptionOutput != nil {
			updates["transcription_output"] = entry.TranscriptionOutput
		}
		if data.ImageGenerationOutput != nil {
			updates["image_generation_output"] = entry.ImageGenerationOutput
		}
		if data.VideoGenerationOutput != nil {
			updates["video_generation_output"] = entry.VideoGenerationOutput
		}
		if data.VideoRetrieveOutput != nil {
			updates["video_retrieve_output"] = entry.VideoRetrieveOutput
		}
		if data.VideoDownloadOutput != nil {
			updates["video_download_output"] = entry.VideoDownloadOutput
		}
		if data.VideoListOutput != nil {
			updates["video_list_output"] = entry.VideoListOutput
		}
		if data.VideoDeleteOutput != nil {
			updates["video_delete_output"] = entry.VideoDeleteOutput
		}
	}

	if data.TokenUsage != nil {
		updates["token_usage"] = entry.TokenUsage
		updates["prompt_tokens"] = data.TokenUsage.PromptTokens
		updates["completion_tokens"] = data.TokenUsage.CompletionTokens
		updates["total_tokens"] = data.TokenUsage.TotalTokens
		updates["cached_read_tokens"] = entry.CachedReadTokens
	}

	if cacheDebug != nil {
		updates["cache_debug"] = entry.CacheDebug
	}
	if data.ErrorDetails != nil {
		updates["error_details"] = entry.ErrorDetails
	}
}

// applySerializedStreamingLogUpdates copies serialized streaming fields from a
// temporary log entry into the GORM update map, respecting content-logging
// gates.
func applySerializedStreamingLogUpdates(
	updates map[string]interface{},
	entry *logstore.Log,
	streamResponse *streaming.ProcessedStreamResponse,
	cacheDebug *schemas.BifrostCacheDebug,
	contentLoggingEnabled bool,
) {
	if streamResponse.Data.TokenUsage != nil {
		updates["token_usage"] = entry.TokenUsage
		updates["prompt_tokens"] = streamResponse.Data.TokenUsage.PromptTokens
		updates["completion_tokens"] = streamResponse.Data.TokenUsage.CompletionTokens
		updates["total_tokens"] = streamResponse.Data.TokenUsage.TotalTokens
		updates["cached_read_tokens"] = entry.CachedReadTokens
	}

	if !contentLoggingEnabled {
		return
	}

	if streamResponse.Data.TranscriptionOutput != nil {
		updates["transcription_output"] = entry.TranscriptionOutput
	}
	if streamResponse.Data.AudioOutput != nil {
		updates["speech_output"] = entry.SpeechOutput
	}
	if streamResponse.Data.ImageGenerationOutput != nil {
		updates["image_generation_output"] = entry.ImageGenerationOutput
	}
	if cacheDebug != nil {
		updates["cache_debug"] = entry.CacheDebug
	}
	if streamResponse.Data.OutputMessage != nil {
		updates["output_message"] = entry.OutputMessage
		updates["content_summary"] = entry.ContentSummary
	}
	if streamResponse.Data.OutputMessages != nil {
		updates["responses_output"] = entry.ResponsesOutput
	}
}

// updateLogEntry updates an existing log entry using GORM
func (p *LoggerPlugin) updateLogEntry(
	ctx context.Context,
	requestID string,
	selectedKeyID string,
	selectedKeyName string,
	latency int64,
	virtualKeyID string,
	virtualKeyName string,
	routingRuleID string,
	routingRuleName string,
	numberOfRetries int,
	cacheDebug *schemas.BifrostCacheDebug,
	routingEngineLogs string,
	data *UpdateLogData,
) error {
	updates := make(map[string]interface{})
	updates["selected_key_id"] = selectedKeyID
	updates["selected_key_name"] = selectedKeyName
	if latency != 0 {
		updates["latency"] = float64(latency)
	}
	updates["status"] = data.Status
	if virtualKeyID != "" {
		updates["virtual_key_id"] = virtualKeyID
	}
	if virtualKeyName != "" {
		updates["virtual_key_name"] = virtualKeyName
	}
	if routingRuleID != "" {
		updates["routing_rule_id"] = routingRuleID
	}
	if routingRuleName != "" {
		updates["routing_rule_name"] = routingRuleName
	}
	if numberOfRetries != 0 {
		updates["number_of_retries"] = numberOfRetries
	}
	if routingEngineLogs != "" {
		updates["routing_engine_logs"] = routingEngineLogs
	}
	contentLoggingEnabled := p.disableContentLogging == nil || !*p.disableContentLogging
	tempEntry := &logstore.Log{}
	needsSerialization := false

	if contentLoggingEnabled {
		if data.ChatOutput != nil {
			tempEntry.OutputMessageParsed = data.ChatOutput
			needsSerialization = true
		}
		if data.ResponsesOutput != nil {
			tempEntry.ResponsesOutputParsed = data.ResponsesOutput
			needsSerialization = true
		}
		if data.ListModelsOutput != nil {
			tempEntry.ListModelsOutputParsed = data.ListModelsOutput
			needsSerialization = true
		}
		if data.EmbeddingOutput != nil {
			tempEntry.EmbeddingOutputParsed = data.EmbeddingOutput
			needsSerialization = true
		}
		if data.RerankOutput != nil {
			tempEntry.RerankOutputParsed = data.RerankOutput
			needsSerialization = true
		}
		if data.SpeechOutput != nil {
			tempEntry.SpeechOutputParsed = data.SpeechOutput
			needsSerialization = true
		}
		if data.TranscriptionOutput != nil {
			tempEntry.TranscriptionOutputParsed = data.TranscriptionOutput
			needsSerialization = true
		}
		if data.ImageGenerationOutput != nil {
			tempEntry.ImageGenerationOutputParsed = data.ImageGenerationOutput
			needsSerialization = true
		}
		if data.VideoGenerationOutput != nil {
			tempEntry.VideoGenerationOutputParsed = data.VideoGenerationOutput
			needsSerialization = true
		}
		if data.VideoRetrieveOutput != nil {
			tempEntry.VideoRetrieveOutputParsed = data.VideoRetrieveOutput
			needsSerialization = true
		}
		if data.VideoDownloadOutput != nil {
			tempEntry.VideoDownloadOutputParsed = data.VideoDownloadOutput
			needsSerialization = true
		}
		if data.VideoListOutput != nil {
			tempEntry.VideoListOutputParsed = data.VideoListOutput
			needsSerialization = true
		}
		if data.VideoDeleteOutput != nil {
			tempEntry.VideoDeleteOutputParsed = data.VideoDeleteOutput
			needsSerialization = true
		}

		// Handle raw request marshaling and logging
		if data.IsLargePayloadRequest {
			// Large payload preview is already a string — skip sonic.Marshal to avoid
			// double-encoding a pre-truncated preview string.
			if str, ok := data.RawRequest.(string); ok {
				updates["raw_request"] = str
			}
		} else if data.RawRequest != nil {
			rawRequestBytes, err := sonic.Marshal(data.RawRequest)
			if err != nil {
				p.logger.Error("failed to marshal raw request: %v", err)
			} else {
				updates["raw_request"] = string(rawRequestBytes)
			}
		}
	}

	if data.TokenUsage != nil {
		tempEntry.TokenUsageParsed = data.TokenUsage
		needsSerialization = true
	}

	// Handle cost from pricing plugin
	if data.Cost != nil {
		updates["cost"] = *data.Cost
	}

	// Handle cache debug
	if cacheDebug != nil {
		tempEntry.CacheDebugParsed = cacheDebug
		needsSerialization = true
	}

	if data.ErrorDetails != nil {
		tempEntry.ErrorDetailsParsed = data.ErrorDetails
		needsSerialization = true
	}

	if needsSerialization {
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Error("failed to serialize log update fields: %v", err)
		} else {
			applySerializedLogUpdates(updates, tempEntry, data, cacheDebug, contentLoggingEnabled)
		}
	}

	// Flag is set outside the content logging guard so the dashboard can always
	// tag large payload requests regardless of content logging settings.
	if data.IsLargePayloadRequest {
		updates["is_large_payload_request"] = true
	}

	if data.IsLargePayloadResponse {
		updates["is_large_payload_response"] = true
		// Large payload preview is already a string — skip sonic.Marshal.
		if p.disableContentLogging == nil || !*p.disableContentLogging {
			if str, ok := data.RawResponse.(string); ok {
				updates["raw_response"] = str
			}
		}
	} else if (p.disableContentLogging == nil || !*p.disableContentLogging) && data.RawResponse != nil {
		rawResponseBytes, err := sonic.Marshal(data.RawResponse)
		if err != nil {
			p.logger.Error("failed to marshal raw response: %v", err)
		} else {
			updates["raw_response"] = string(rawResponseBytes)
		}
	}
	return p.store.Update(ctx, requestID, updates)
}

// updateStreamingLogEntry handles streaming updates using GORM
func (p *LoggerPlugin) updateStreamingLogEntry(
	ctx context.Context,
	requestID string,
	selectedKeyID string,
	selectedKeyName string,
	virtualKeyID string,
	virtualKeyName string,
	routingRuleID string,
	routingRuleName string,
	numberOfRetries int,
	cacheDebug *schemas.BifrostCacheDebug,
	routingEngineLogs string,
	streamResponse *streaming.ProcessedStreamResponse,
	isFinalChunk bool,
	isLargePayloadRequest bool,
	isLargePayloadResponse bool,
) error {
	p.logger.Debug("[logging] updating streaming log entry %s", requestID)
	updates := make(map[string]interface{})
	updates["selected_key_id"] = selectedKeyID
	updates["selected_key_name"] = selectedKeyName
	if virtualKeyID != "" {
		updates["virtual_key_id"] = virtualKeyID
	}
	if virtualKeyName != "" {
		updates["virtual_key_name"] = virtualKeyName
	}
	if routingRuleID != "" {
		updates["routing_rule_id"] = routingRuleID
	}
	if routingRuleName != "" {
		updates["routing_rule_name"] = routingRuleName
	}
	if numberOfRetries != 0 {
		updates["number_of_retries"] = numberOfRetries
	}
	if routingEngineLogs != "" {
		updates["routing_engine_logs"] = routingEngineLogs
	}
	// Handle error case first
	if streamResponse.Data.ErrorDetails != nil {
		tempEntry := &logstore.Log{}
		tempEntry.ErrorDetailsParsed = streamResponse.Data.ErrorDetails
		if err := tempEntry.SerializeFields(); err != nil {
			return fmt.Errorf("failed to serialize error details: %w", err)
		}
		errorUpdates := map[string]interface{}{
			"status":        "error",
			"latency":       float64(streamResponse.Data.Latency),
			"error_details": tempEntry.ErrorDetails,
		}
		if isLargePayloadRequest {
			errorUpdates["is_large_payload_request"] = true
		}
		if isLargePayloadResponse {
			errorUpdates["is_large_payload_response"] = true
		}
		return p.store.Update(ctx, requestID, errorUpdates)
	}

	// Always mark as streaming and update timestamp
	updates["stream"] = true

	tempEntry := &logstore.Log{}
	updates["latency"] = float64(streamResponse.Data.Latency)

	// Update model if provided
	if streamResponse.Data.Model != "" {
		updates["model"] = streamResponse.Data.Model
	}

	needsSerialization := false

	// Update token usage if provided
	if streamResponse.Data.TokenUsage != nil {
		tempEntry.TokenUsageParsed = streamResponse.Data.TokenUsage
		needsSerialization = true
	}

	// Handle cost from pricing plugin
	if streamResponse.Data.Cost != nil {
		updates["cost"] = *streamResponse.Data.Cost
	}
	// Handle finish reason - if present, mark as complete
	if isFinalChunk {
		updates["status"] = "success"
	}

	contentLoggingEnabled := p.disableContentLogging == nil || !*p.disableContentLogging
	if contentLoggingEnabled {
		// Handle transcription output from stream updates
		if streamResponse.Data.TranscriptionOutput != nil {
			tempEntry.TranscriptionOutputParsed = streamResponse.Data.TranscriptionOutput
			needsSerialization = true
		}
		// Handle speech output from stream updates
		if streamResponse.Data.AudioOutput != nil {
			tempEntry.SpeechOutputParsed = streamResponse.Data.AudioOutput
			needsSerialization = true
		}
		// Handle image generation output from stream updates
		if streamResponse.Data.ImageGenerationOutput != nil {
			tempEntry.ImageGenerationOutputParsed = streamResponse.Data.ImageGenerationOutput
			needsSerialization = true
		}
		// Handle cache debug
		if cacheDebug != nil {
			tempEntry.CacheDebugParsed = cacheDebug
			needsSerialization = true
		}
		// Create content summary
		if streamResponse.Data.OutputMessage != nil {
			tempEntry.OutputMessageParsed = streamResponse.Data.OutputMessage
			needsSerialization = true
		}
		// Handle responses output from stream updates
		if streamResponse.Data.OutputMessages != nil {
			tempEntry.ResponsesOutputParsed = streamResponse.Data.OutputMessages
			needsSerialization = true
		}
		// Handle raw request from stream updates
		if streamResponse.RawRequest != nil && *streamResponse.RawRequest != nil {
			if isLargePayloadRequest {
				// Large payload preview is already a string — skip sonic.Marshal to avoid
				// double-encoding a pre-truncated preview string.
				if str, ok := (*streamResponse.RawRequest).(string); ok {
					updates["raw_request"] = str
				}
			} else {
				rawRequestBytes, err := sonic.Marshal(*streamResponse.RawRequest)
				if err != nil {
					p.logger.Error("failed to marshal raw request: %v", err)
				} else {
					updates["raw_request"] = string(rawRequestBytes)
				}
			}
		}
		// Handle raw response from stream updates
		if streamResponse.Data.RawResponse != nil {
			updates["raw_response"] = *streamResponse.Data.RawResponse
		}
	}

	if needsSerialization {
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Error("failed to serialize streaming log update fields: %v", err)
		} else {
			applySerializedStreamingLogUpdates(updates, tempEntry, streamResponse, cacheDebug, contentLoggingEnabled)
		}
	}
	// Persist large payload flags for dashboard tagging
	if isLargePayloadRequest {
		updates["is_large_payload_request"] = true
	}
	if isLargePayloadResponse {
		updates["is_large_payload_response"] = true
	}
	// Only perform update if there's something to update
	if len(updates) > 0 {
		return p.store.Update(ctx, requestID, updates)
	}
	return nil
}

// makePostWriteCallback creates a callback function for use after the batch writer commits.
// It receives the already-inserted entry directly (no DB re-read needed).
func (p *LoggerPlugin) makePostWriteCallback(enrichFn func(*logstore.Log)) func(entry *logstore.Log) {
	return func(entry *logstore.Log) {
		p.mu.Lock()
		callback := p.logCallback
		p.mu.Unlock()
		if callback == nil {
			return
		}
		if entry == nil {
			return
		}
		if enrichFn != nil {
			enrichFn(entry)
		}
		callback(p.ctx, entry)
	}
}

// applyStreamingOutputToEntry applies accumulated streaming data to a log entry.
func (p *LoggerPlugin) applyStreamingOutputToEntry(entry *logstore.Log, streamResponse *streaming.ProcessedStreamResponse) {
	if streamResponse.Data == nil {
		return
	}

	// Handle error case first
	if streamResponse.Data.ErrorDetails != nil {
		entry.Status = "error"
		// Serialize error details immediately to avoid use-after-free with pooled errors
		if data, err := sonic.Marshal(streamResponse.Data.ErrorDetails); err == nil {
			entry.ErrorDetails = string(data)
		}
		latF := float64(streamResponse.Data.Latency)
		entry.Latency = &latF
		return
	}

	entry.Status = "success"
	latF := float64(streamResponse.Data.Latency)
	entry.Latency = &latF

	// Update model if provided
	if streamResponse.Data.Model != "" {
		entry.Model = streamResponse.Data.Model
	}

	// Token usage
	if streamResponse.Data.TokenUsage != nil {
		entry.TokenUsageParsed = streamResponse.Data.TokenUsage
		entry.PromptTokens = streamResponse.Data.TokenUsage.PromptTokens
		entry.CompletionTokens = streamResponse.Data.TokenUsage.CompletionTokens
		entry.TotalTokens = streamResponse.Data.TokenUsage.TotalTokens
	}

	// Cost
	if streamResponse.Data.Cost != nil {
		entry.Cost = streamResponse.Data.Cost
	}

	if p.disableContentLogging == nil || !*p.disableContentLogging {
		// Transcription output
		if streamResponse.Data.TranscriptionOutput != nil {
			entry.TranscriptionOutputParsed = streamResponse.Data.TranscriptionOutput
		}
		// Speech output
		if streamResponse.Data.AudioOutput != nil {
			entry.SpeechOutputParsed = streamResponse.Data.AudioOutput
		}
		// Image generation output
		if streamResponse.Data.ImageGenerationOutput != nil {
			entry.ImageGenerationOutputParsed = streamResponse.Data.ImageGenerationOutput
		}
		// Cache debug
		if streamResponse.Data.CacheDebug != nil {
			entry.CacheDebugParsed = streamResponse.Data.CacheDebug
		}
		// Output message
		if streamResponse.Data.OutputMessage != nil {
			entry.OutputMessageParsed = streamResponse.Data.OutputMessage
		}
		// Responses output
		if streamResponse.Data.OutputMessages != nil {
			entry.ResponsesOutputParsed = streamResponse.Data.OutputMessages
		}
		// Raw request
		if streamResponse.RawRequest != nil && *streamResponse.RawRequest != nil {
			rawRequestBytes, err := sonic.Marshal(*streamResponse.RawRequest)
			if err == nil {
				entry.RawRequest = string(rawRequestBytes)
			}
		}
		// Raw response
		if streamResponse.Data.RawResponse != nil {
			entry.RawResponse = *streamResponse.Data.RawResponse
		}
	}
}

// isPassthroughErrorResponse returns true when the result is a passthrough
// response with a provider-reported HTTP error status (4xx or 5xx).
func isPassthroughErrorResponse(result *schemas.BifrostResponse) bool {
	return result != nil &&
		result.PassthroughResponse != nil &&
		result.PassthroughResponse.StatusCode >= 400
}

// applyNonStreamingOutputToEntry applies non-streaming response data to a log entry.
func (p *LoggerPlugin) applyNonStreamingOutputToEntry(entry *logstore.Log, result *schemas.BifrostResponse) {
	if result == nil {
		return
	}
	// Token usage
	var usage *schemas.BifrostLLMUsage
	switch {
	case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
		usage = result.TextCompletionResponse.Usage
	case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
		usage = result.ChatResponse.Usage
	case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
		usage = result.ResponsesResponse.Usage.ToBifrostLLMUsage()
	case result.EmbeddingResponse != nil && result.EmbeddingResponse.Usage != nil:
		usage = result.EmbeddingResponse.Usage
	case result.TranscriptionResponse != nil && result.TranscriptionResponse.Usage != nil:
		usage = &schemas.BifrostLLMUsage{}
		if result.TranscriptionResponse.Usage.InputTokens != nil {
			usage.PromptTokens = *result.TranscriptionResponse.Usage.InputTokens
		}
		if result.TranscriptionResponse.Usage.OutputTokens != nil {
			usage.CompletionTokens = *result.TranscriptionResponse.Usage.OutputTokens
		}
		if result.TranscriptionResponse.Usage.TotalTokens != nil {
			usage.TotalTokens = *result.TranscriptionResponse.Usage.TotalTokens
		} else {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
	case result.ImageGenerationResponse != nil && result.ImageGenerationResponse.Usage != nil:
		usage = &schemas.BifrostLLMUsage{}
		usage.PromptTokens = result.ImageGenerationResponse.Usage.InputTokens
		usage.CompletionTokens = result.ImageGenerationResponse.Usage.OutputTokens
		if result.ImageGenerationResponse.Usage.TotalTokens > 0 {
			usage.TotalTokens = result.ImageGenerationResponse.Usage.TotalTokens
		} else {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
	}
	if usage != nil {
		entry.TokenUsageParsed = usage
		entry.PromptTokens = usage.PromptTokens
		entry.CompletionTokens = usage.CompletionTokens
		entry.TotalTokens = usage.TotalTokens
	}

	// Extract raw request/response and output content
	extraFields := result.GetExtraFields()
	if p.disableContentLogging == nil || !*p.disableContentLogging {
		if extraFields.RawRequest != nil {
			rawRequestBytes, err := sonic.Marshal(extraFields.RawRequest)
			if err == nil {
				entry.RawRequest = string(rawRequestBytes)
			}
		}
		if extraFields.RawResponse != nil {
			rawRespBytes, err := sonic.Marshal(extraFields.RawResponse)
			if err == nil {
				entry.RawResponse = string(rawRespBytes)
			}
		}
		if result.ListModelsResponse != nil && result.ListModelsResponse.Data != nil {
			entry.ListModelsOutputParsed = result.ListModelsResponse.Data
		}
		if result.TextCompletionResponse != nil {
			if len(result.TextCompletionResponse.Choices) > 0 {
				choice := result.TextCompletionResponse.Choices[0]
				if choice.TextCompletionResponseChoice != nil {
					entry.OutputMessageParsed = &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: choice.TextCompletionResponseChoice.Text,
						},
					}
				}
			}
		}
		if result.ChatResponse != nil {
			if len(result.ChatResponse.Choices) > 0 {
				choice := result.ChatResponse.Choices[0]
				if choice.ChatNonStreamResponseChoice != nil {
					entry.OutputMessageParsed = choice.ChatNonStreamResponseChoice.Message
				}
			}
		}
		if result.ResponsesResponse != nil {
			entry.ResponsesOutputParsed = result.ResponsesResponse.Output
		}
		if result.EmbeddingResponse != nil && len(result.EmbeddingResponse.Data) > 0 {
			entry.EmbeddingOutputParsed = result.EmbeddingResponse.Data
		}
		if result.SpeechResponse != nil {
			entry.SpeechOutputParsed = result.SpeechResponse
		}
		if result.TranscriptionResponse != nil {
			entry.TranscriptionOutputParsed = result.TranscriptionResponse
		}
		if result.ImageGenerationResponse != nil {
			entry.ImageGenerationOutputParsed = result.ImageGenerationResponse
		}
		if result.PassthroughResponse != nil && len(result.PassthroughResponse.Body) > 0 {
			entry.PassthroughResponseBody = string(result.PassthroughResponse.Body)
		}
	}

	if result.PassthroughResponse != nil {
		if params, ok := entry.ParamsParsed.(*schemas.PassthroughLogParams); ok {
			params.StatusCode = result.PassthroughResponse.StatusCode
		}
	}
}

// SearchLogs searches logs with filters and pagination using GORM
func (p *LoggerPlugin) SearchLogs(ctx context.Context, filters logstore.SearchFilters, pagination logstore.PaginationOptions) (*logstore.SearchResult, error) {
	// Set default pagination if not provided
	if pagination.Limit == 0 {
		pagination.Limit = 50
	}
	if pagination.SortBy == "" {
		pagination.SortBy = "timestamp"
	}
	if pagination.Order == "" {
		pagination.Order = "desc"
	}
	// Build base query with all filters applied
	return p.store.SearchLogs(ctx, filters, pagination)
}

// GetLog retrieves a single log entry by ID including all fields (raw_request, raw_response).
func (p *LoggerPlugin) GetLog(ctx context.Context, id string) (*logstore.Log, error) {
	return p.store.FindByID(ctx, id)
}

// GetStats calculates statistics for logs matching the given filters
func (p *LoggerPlugin) GetStats(ctx context.Context, filters logstore.SearchFilters) (*logstore.SearchStats, error) {
	return p.store.GetStats(ctx, filters)
}

// GetHistogram returns time-bucketed request counts for the given filters
func (p *LoggerPlugin) GetHistogram(ctx context.Context, filters logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error) {
	return p.store.GetHistogram(ctx, filters, bucketSizeSeconds)
}

// GetTokenHistogram returns time-bucketed token usage for the given filters
func (p *LoggerPlugin) GetTokenHistogram(ctx context.Context, filters logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error) {
	return p.store.GetTokenHistogram(ctx, filters, bucketSizeSeconds)
}

// GetCostHistogram returns time-bucketed cost data with model breakdown for the given filters
func (p *LoggerPlugin) GetCostHistogram(ctx context.Context, filters logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error) {
	return p.store.GetCostHistogram(ctx, filters, bucketSizeSeconds)
}

// GetModelHistogram returns time-bucketed model usage with success/error breakdown for the given filters
func (p *LoggerPlugin) GetModelHistogram(ctx context.Context, filters logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ModelHistogramResult, error) {
	return p.store.GetModelHistogram(ctx, filters, bucketSizeSeconds)
}

// GetLatencyHistogram returns time-bucketed latency percentiles for the given filters
func (p *LoggerPlugin) GetLatencyHistogram(ctx context.Context, filters logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.LatencyHistogramResult, error) {
	return p.store.GetLatencyHistogram(ctx, filters, bucketSizeSeconds)
}

// GetProviderCostHistogram returns time-bucketed cost data with provider breakdown for the given filters
func (p *LoggerPlugin) GetProviderCostHistogram(ctx context.Context, filters logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderCostHistogramResult, error) {
	return p.store.GetProviderCostHistogram(ctx, filters, bucketSizeSeconds)
}

// GetProviderTokenHistogram returns time-bucketed token usage with provider breakdown for the given filters
func (p *LoggerPlugin) GetProviderTokenHistogram(ctx context.Context, filters logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderTokenHistogramResult, error) {
	return p.store.GetProviderTokenHistogram(ctx, filters, bucketSizeSeconds)
}

// GetProviderLatencyHistogram returns time-bucketed latency percentiles with provider breakdown for the given filters
func (p *LoggerPlugin) GetProviderLatencyHistogram(ctx context.Context, filters logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderLatencyHistogramResult, error) {
	return p.store.GetProviderLatencyHistogram(ctx, filters, bucketSizeSeconds)
}

func (p *LoggerPlugin) GetModelRankings(ctx context.Context, filters logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	return p.store.GetModelRankings(ctx, filters)
}

// GetAvailableModels returns all unique models from logs.
// Uses DISTINCT to avoid loading all rows (28K+) when only unique values are needed.
func (p *LoggerPlugin) GetAvailableModels(ctx context.Context) []string {
	models, err := p.store.GetDistinctModels(ctx)
	if err != nil {
		p.logger.Error("failed to get available models: %v", err)
		return []string{}
	}
	return models
}

func (p *LoggerPlugin) GetAvailableSelectedKeys(ctx context.Context) []KeyPair {
	results, err := p.store.GetDistinctKeyPairs(ctx, "selected_key_id", "selected_key_name")
	if err != nil {
		p.logger.Error("failed to get available selected keys: %v", err)
		return []KeyPair{}
	}
	return keyPairResultsToKeyPairs(results)
}

func (p *LoggerPlugin) GetAvailableVirtualKeys(ctx context.Context) []KeyPair {
	results, err := p.store.GetDistinctKeyPairs(ctx, "virtual_key_id", "virtual_key_name")
	if err != nil {
		p.logger.Error("failed to get available virtual keys: %v", err)
		return []KeyPair{}
	}
	return keyPairResultsToKeyPairs(results)
}

func (p *LoggerPlugin) GetAvailableRoutingRules(ctx context.Context) []KeyPair {
	results, err := p.store.GetDistinctKeyPairs(ctx, "routing_rule_id", "routing_rule_name")
	if err != nil {
		p.logger.Error("failed to get available routing rules: %v", err)
		return []KeyPair{}
	}
	return keyPairResultsToKeyPairs(results)
}

// GetAvailableRoutingEngines returns all unique routing engine types used in logs.
// Uses DISTINCT to avoid loading all rows when only unique values are needed.
func (p *LoggerPlugin) GetAvailableRoutingEngines(ctx context.Context) []string {
	engines, err := p.store.GetDistinctRoutingEngines(ctx)
	if err != nil {
		p.logger.Error("failed to get available routing engines: %v", err)
		return []string{}
	}
	return engines
}

// keyPairResultsToKeyPairs converts logstore.KeyPairResult slice to KeyPair slice
func keyPairResultsToKeyPairs(results []logstore.KeyPairResult) []KeyPair {
	pairs := make([]KeyPair, len(results))
	for i, r := range results {
		pairs[i] = KeyPair{ID: r.ID, Name: r.Name}
	}
	return pairs
}

// GetAvailableMCPVirtualKeys returns all unique virtual key ID-Name pairs from MCP tool logs
func (p *LoggerPlugin) GetAvailableMCPVirtualKeys(ctx context.Context) []KeyPair {
	result, err := p.store.GetAvailableMCPVirtualKeys(ctx)
	if err != nil {
		p.logger.Error("failed to get available virtual keys from MCP logs: %w", err)
		return []KeyPair{}
	}
	return p.extractUniqueMCPKeyPairs(result, func(log *logstore.MCPToolLog) KeyPair {
		if log.VirtualKeyID != nil && log.VirtualKeyName != nil {
			return KeyPair{
				ID:   *log.VirtualKeyID,
				Name: *log.VirtualKeyName,
			}
		}
		return KeyPair{}
	})
}

// extractUniqueMCPKeyPairs extracts unique non-empty key pairs from MCP logs using the provided extractor function
func (p *LoggerPlugin) extractUniqueMCPKeyPairs(logs []logstore.MCPToolLog, extractor func(*logstore.MCPToolLog) KeyPair) []KeyPair {
	uniqueSet := make(map[string]KeyPair)
	for i := range logs {
		pair := extractor(&logs[i])
		if pair.ID != "" && pair.Name != "" {
			uniqueSet[pair.ID] = pair
		}
	}

	result := make([]KeyPair, 0, len(uniqueSet))
	for _, pair := range uniqueSet {
		result = append(result, pair)
	}
	return result
}

// RecalculateCosts recomputes cost for log entries that are missing cost values
func (p *LoggerPlugin) RecalculateCosts(ctx context.Context, filters logstore.SearchFilters, limit int) (*RecalculateCostResult, error) {
	if p.pricingManager == nil {
		return nil, fmt.Errorf("pricing manager is not configured")
	}

	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	// Always scope to logs that don't have cost populated
	filters.MissingCostOnly = true
	pagination := logstore.PaginationOptions{
		Limit: limit,
		// Always look at the oldest requests first
		SortBy: "timestamp",
		Order:  "asc",
	}

	searchResult, err := p.store.SearchLogs(ctx, filters, pagination)
	if err != nil {
		return nil, fmt.Errorf("failed to search logs for cost recalculation: %w", err)
	}

	result := &RecalculateCostResult{
		TotalMatched: searchResult.Stats.TotalRequests,
	}

	costUpdates := make(map[string]float64, len(searchResult.Logs))

	for _, logEntry := range searchResult.Logs {
		cost, calcErr := p.calculateCostForLog(&logEntry)
		if calcErr != nil {
			result.Skipped++
			p.logger.Debug("skipping cost recalculation for log %s: %v", logEntry.ID, calcErr)
			continue
		}
		costUpdates[logEntry.ID] = cost
	}

	if len(costUpdates) > 0 {
		if err := p.store.BulkUpdateCost(ctx, costUpdates); err != nil {
			return nil, fmt.Errorf("failed to bulk update costs: %w", err)
		}
		result.Updated = len(costUpdates)
	}

	// Re-count how many logs still match the missing-cost filter after updates
	remainingResult, err := p.store.SearchLogs(ctx, filters, logstore.PaginationOptions{
		Limit:  1, // we only need stats.TotalRequests for the count
		Offset: 0,
		SortBy: "timestamp",
		Order:  "asc",
	})
	if err != nil {
		p.logger.Warn("failed to recompute remaining missing-cost logs: %v", err)
	} else {
		result.Remaining = remainingResult.Stats.TotalRequests
	}

	return result, nil
}

func (p *LoggerPlugin) calculateCostForLog(logEntry *logstore.Log) (float64, error) {
	if logEntry == nil {
		return 0, fmt.Errorf("log entry cannot be nil")
	}

	if (logEntry.TokenUsageParsed == nil && logEntry.TokenUsage != "") ||
		(logEntry.CacheDebugParsed == nil && logEntry.CacheDebug != "") {
		if err := logEntry.DeserializeFields(); err != nil {
			return 0, fmt.Errorf("failed to deserialize fields for log %s: %w", logEntry.ID, err)
		}
	}

	usage := logEntry.TokenUsageParsed
	cacheDebug := logEntry.CacheDebugParsed

	// If no cache hit and no usage, we can't calculate cost
	if usage == nil && (cacheDebug == nil || !cacheDebug.CacheHit) {
		return 0, fmt.Errorf("token usage not available for log %s", logEntry.ID)
	}

	requestType := schemas.RequestType(logEntry.Object)
	if requestType == "" && (cacheDebug == nil || !cacheDebug.CacheHit) {
		p.logger.Warn("skipping cost calculation for log %s: object type is empty (timestamp: %s)", logEntry.ID, logEntry.Timestamp)
		return 0, fmt.Errorf("object type is empty for log %s", logEntry.ID)
	}

	// Build a minimal BifrostResponse matching the request type so that
	// extractCostInput routes usage into the correct field for each compute function.
	extraFields := schemas.BifrostResponseExtraFields{
		RequestType:    requestType,
		Provider:       schemas.ModelProvider(logEntry.Provider),
		ModelRequested: logEntry.Model,
		CacheDebug:     cacheDebug,
	}

	resp := buildResponseForRequestType(requestType, usage, extraFields)

	// Patch modality-specific output fields that are not captured in BifrostLLMUsage
	// but are required for accurate cost calculation.

	// Transcription: restore Seconds (duration billing) and InputTokenDetails
	// (audio/text token breakdown) from the stored response object.
	if resp.TranscriptionResponse != nil &&
		logEntry.TranscriptionOutputParsed != nil &&
		logEntry.TranscriptionOutputParsed.Usage != nil {
		resp.TranscriptionResponse.Usage = logEntry.TranscriptionOutputParsed.Usage
	}

	// ImageGeneration: restore full ImageUsage (OutputTokensDetails/NImages for
	// per-image pricing), Data count, and Size from the stored response object.
	if resp.ImageGenerationResponse != nil && logEntry.ImageGenerationOutputParsed != nil {
		parsed := logEntry.ImageGenerationOutputParsed
		if parsed.Usage != nil {
			resp.ImageGenerationResponse.Usage = parsed.Usage
		}
		if resp.ImageGenerationResponse.ImageGenerationResponseParameters == nil &&
			parsed.ImageGenerationResponseParameters != nil {
			resp.ImageGenerationResponse.ImageGenerationResponseParameters = parsed.ImageGenerationResponseParameters
		}
		if len(resp.ImageGenerationResponse.Data) == 0 {
			resp.ImageGenerationResponse.Data = parsed.Data
		}
	}

	// VideoGeneration: patch in Seconds from the stored output so that
	// extractCostInput can compute the per-second cost.
	if resp.VideoGenerationResponse != nil && logEntry.VideoGenerationOutputParsed != nil {
		resp.VideoGenerationResponse.Seconds = logEntry.VideoGenerationOutputParsed.Seconds
	}

	// Speech: restore provider-specific usage (e.g. character-count billing) from
	// the stored response instead of relying solely on aggregate token counts.
	if resp.SpeechResponse != nil &&
		logEntry.SpeechOutputParsed != nil &&
		logEntry.SpeechOutputParsed.Usage != nil {
		resp.SpeechResponse.Usage = logEntry.SpeechOutputParsed.Usage
	}

	return p.pricingManager.CalculateCost(resp), nil
}

// buildResponseForRequestType wraps BifrostLLMUsage into the correct response
// field so that CalculateCost's extractCostInput routes it properly.
func buildResponseForRequestType(requestType schemas.RequestType, usage *schemas.BifrostLLMUsage, extra schemas.BifrostResponseExtraFields) *schemas.BifrostResponse {
	switch requestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		return &schemas.BifrostResponse{
			TextCompletionResponse: &schemas.BifrostTextCompletionResponse{
				Usage:       usage,
				ExtraFields: extra,
			},
		}
	case schemas.EmbeddingRequest:
		return &schemas.BifrostResponse{
			EmbeddingResponse: &schemas.BifrostEmbeddingResponse{
				Usage:       usage,
				ExtraFields: extra,
			},
		}
	case schemas.RerankRequest:
		return &schemas.BifrostResponse{
			RerankResponse: &schemas.BifrostRerankResponse{
				Usage:       usage,
				ExtraFields: extra,
			},
		}
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		// Convert BifrostLLMUsage back to ResponsesResponseUsage, preserving token
		// detail breakdowns so CalculateCost can apply cache and search-query pricing.
		var respUsage *schemas.ResponsesResponseUsage
		if usage != nil {
			respUsage = &schemas.ResponsesResponseUsage{
				InputTokens:  usage.PromptTokens,
				OutputTokens: usage.CompletionTokens,
				TotalTokens:  usage.TotalTokens,
				Cost:         usage.Cost,
			}
			if usage.PromptTokensDetails != nil {
				respUsage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{
					TextTokens:        usage.PromptTokensDetails.TextTokens,
					AudioTokens:       usage.PromptTokensDetails.AudioTokens,
					ImageTokens:       usage.PromptTokensDetails.ImageTokens,
					CachedReadTokens:  usage.PromptTokensDetails.CachedReadTokens,
					CachedWriteTokens: usage.PromptTokensDetails.CachedWriteTokens,
				}
			}
		if usage.CompletionTokensDetails != nil {
			respUsage.OutputTokensDetails = &schemas.ResponsesResponseOutputTokens{
				TextTokens:               usage.CompletionTokensDetails.TextTokens,
				AcceptedPredictionTokens: usage.CompletionTokensDetails.AcceptedPredictionTokens,
				AudioTokens:              usage.CompletionTokensDetails.AudioTokens,
				ImageTokens:              usage.CompletionTokensDetails.ImageTokens,
				ReasoningTokens:          usage.CompletionTokensDetails.ReasoningTokens,
				RejectedPredictionTokens: usage.CompletionTokensDetails.RejectedPredictionTokens,
				CitationTokens:           usage.CompletionTokensDetails.CitationTokens,
				NumSearchQueries:         usage.CompletionTokensDetails.NumSearchQueries,
			}
		}
		}
		return &schemas.BifrostResponse{
			ResponsesResponse: &schemas.BifrostResponsesResponse{
				Usage:       respUsage,
				ExtraFields: extra,
			},
		}
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		var speechUsage *schemas.SpeechUsage
		if usage != nil {
			speechUsage = &schemas.SpeechUsage{
				InputTokens:  usage.PromptTokens,
				OutputTokens: usage.CompletionTokens,
				TotalTokens:  usage.TotalTokens,
			}
		}
		return &schemas.BifrostResponse{
			SpeechResponse: &schemas.BifrostSpeechResponse{
				Usage:       speechUsage,
				ExtraFields: extra,
			},
		}
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		var txUsage *schemas.TranscriptionUsage
		if usage != nil {
			txUsage = &schemas.TranscriptionUsage{
				InputTokens:  &usage.PromptTokens,
				OutputTokens: &usage.CompletionTokens,
				TotalTokens:  &usage.TotalTokens,
			}
		}
		return &schemas.BifrostResponse{
			TranscriptionResponse: &schemas.BifrostTranscriptionResponse{
				Usage:       txUsage,
				ExtraFields: extra,
			},
		}
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest,
		schemas.ImageEditRequest, schemas.ImageEditStreamRequest, schemas.ImageVariationRequest:
		// Log entries only store BifrostLLMUsage; convert to ImageUsage for proper routing
		var imgUsage *schemas.ImageUsage
		if usage != nil {
			imgUsage = &schemas.ImageUsage{
				InputTokens:  usage.PromptTokens,
				OutputTokens: usage.CompletionTokens,
				TotalTokens:  usage.TotalTokens,
			}
		}
		return &schemas.BifrostResponse{
			ImageGenerationResponse: &schemas.BifrostImageGenerationResponse{
				Usage:       imgUsage,
				ExtraFields: extra,
			},
		}
	case schemas.VideoGenerationRequest, schemas.VideoRemixRequest:
		// Seconds is not stored in BifrostLLMUsage; the caller must patch it in from
		// the stored VideoGenerationOutputParsed after this function returns.
		return &schemas.BifrostResponse{
			VideoGenerationResponse: &schemas.BifrostVideoGenerationResponse{
				ExtraFields: extra,
			},
		}
	default:
		// Default to chat response for unknown or chat request types
		return &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				Usage:       usage,
				ExtraFields: extra,
			},
		}
	}
}
