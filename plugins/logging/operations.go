// Package logging provides database operations for the GORM-based logging plugin
package logging

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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
	}
	if parentRequestID != "" {
		entry.ParentRequestID = &parentRequestID
	}
	return p.store.CreateIfNotExists(ctx, entry)
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
	// Handle JSON fields by setting them on a temporary entry and serializing
	tempEntry := &logstore.Log{}
	if data.ChatOutput != nil {
		tempEntry.OutputMessageParsed = data.ChatOutput
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Error("failed to serialize output message: %v", err)
		} else {
			updates["output_message"] = tempEntry.OutputMessage
			updates["content_summary"] = tempEntry.ContentSummary // Update content summary
		}
	}

	if p.disableContentLogging == nil || !*p.disableContentLogging {
		if data.ResponsesOutput != nil {
			tempEntry.ResponsesOutputParsed = data.ResponsesOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize responses output: %v", err)
			} else {
				updates["responses_output"] = tempEntry.ResponsesOutput
			}
		}

		if data.ListModelsOutput != nil {
			tempEntry.ListModelsOutputParsed = data.ListModelsOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize list models output: %v", err)
			} else {
				updates["list_models_output"] = tempEntry.ListModelsOutput
			}
		}

		if data.EmbeddingOutput != nil {
			tempEntry.EmbeddingOutputParsed = data.EmbeddingOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize embedding output: %v", err)
			} else {
				updates["embedding_output"] = tempEntry.EmbeddingOutput
			}
		}

		if data.RerankOutput != nil {
			tempEntry.RerankOutputParsed = data.RerankOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize rerank output: %v", err)
			} else {
				updates["rerank_output"] = tempEntry.RerankOutput
				updates["content_summary"] = tempEntry.ContentSummary
			}
		}

		if data.SpeechOutput != nil {
			tempEntry.SpeechOutputParsed = data.SpeechOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize speech output: %v", err)
			} else {
				updates["speech_output"] = tempEntry.SpeechOutput
			}
		}

		if data.TranscriptionOutput != nil {
			tempEntry.TranscriptionOutputParsed = data.TranscriptionOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize transcription output: %v", err)
			} else {
				updates["transcription_output"] = tempEntry.TranscriptionOutput
			}
		}

		if data.ImageGenerationOutput != nil {
			tempEntry.ImageGenerationOutputParsed = data.ImageGenerationOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize image generation output: %v", err)
			} else {
				updates["image_generation_output"] = tempEntry.ImageGenerationOutput
			}
		}

		if data.VideoGenerationOutput != nil {
			tempEntry.VideoGenerationOutputParsed = data.VideoGenerationOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize video generation output: %v", err)
			} else {
				updates["video_generation_output"] = tempEntry.VideoGenerationOutput
			}
		}

		if data.VideoRetrieveOutput != nil {
			tempEntry.VideoRetrieveOutputParsed = data.VideoRetrieveOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize video retrieve output: %v", err)
			} else {
				updates["video_retrieve_output"] = tempEntry.VideoRetrieveOutput
			}
		}

		if data.VideoDownloadOutput != nil {
			tempEntry.VideoDownloadOutputParsed = data.VideoDownloadOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize video download output: %v", err)
			} else {
				updates["video_download_output"] = tempEntry.VideoDownloadOutput
			}
		}

		if data.VideoListOutput != nil {
			tempEntry.VideoListOutputParsed = data.VideoListOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize video list output: %v", err)
			} else {
				updates["video_list_output"] = tempEntry.VideoListOutput
			}
		}

		if data.VideoDeleteOutput != nil {
			tempEntry.VideoDeleteOutputParsed = data.VideoDeleteOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize video delete output: %v", err)
			} else {
				updates["video_delete_output"] = tempEntry.VideoDeleteOutput
			}
		}

		// Handle raw request marshaling and logging
		if data.RawRequest != nil {
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
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Error("failed to serialize token usage: %v", err)
		} else {
			updates["token_usage"] = tempEntry.TokenUsage
			updates["prompt_tokens"] = data.TokenUsage.PromptTokens
			updates["completion_tokens"] = data.TokenUsage.CompletionTokens
			updates["total_tokens"] = data.TokenUsage.TotalTokens
		}
	}

	// Handle cost from pricing plugin
	if data.Cost != nil {
		updates["cost"] = *data.Cost
	}

	// Handle cache debug
	if cacheDebug != nil {
		tempEntry.CacheDebugParsed = cacheDebug
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Error("failed to serialize cache debug: %v", err)
		} else {
			updates["cache_debug"] = tempEntry.CacheDebug
		}
	}

	if data.ErrorDetails != nil {
		tempEntry.ErrorDetailsParsed = data.ErrorDetails
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Error("failed to serialize error details: %v", err)
		} else {
			updates["error_details"] = tempEntry.ErrorDetails
		}
	}

	if p.disableContentLogging == nil || !*p.disableContentLogging && data.RawResponse != nil {
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
		return p.store.Update(ctx, requestID, map[string]interface{}{
			"status":        "error",
			"latency":       float64(streamResponse.Data.Latency),
			"error_details": tempEntry.ErrorDetails,
		})
	}

	// Always mark as streaming and update timestamp
	updates["stream"] = true

	// Calculate latency when stream finishes
	tempEntry := &logstore.Log{}

	updates["latency"] = float64(streamResponse.Data.Latency)

	// Update model if provided
	if streamResponse.Data.Model != "" {
		updates["model"] = streamResponse.Data.Model
	}

	// Update token usage if provided
	if streamResponse.Data.TokenUsage != nil {
		tempEntry.TokenUsageParsed = streamResponse.Data.TokenUsage
		if err := tempEntry.SerializeFields(); err == nil {
			updates["token_usage"] = tempEntry.TokenUsage
			updates["prompt_tokens"] = streamResponse.Data.TokenUsage.PromptTokens
			updates["completion_tokens"] = streamResponse.Data.TokenUsage.CompletionTokens
			updates["total_tokens"] = streamResponse.Data.TokenUsage.TotalTokens
		}
	}

	// Handle cost from pricing plugin
	if streamResponse.Data.Cost != nil {
		updates["cost"] = *streamResponse.Data.Cost
	}
	// Handle finish reason - if present, mark as complete
	if isFinalChunk {
		updates["status"] = "success"
	}

	if p.disableContentLogging == nil || !*p.disableContentLogging {
		// Handle transcription output from stream updates
		if streamResponse.Data.TranscriptionOutput != nil {
			tempEntry.TranscriptionOutputParsed = streamResponse.Data.TranscriptionOutput
			// Here we just log error but move one vs breaking the entire logging flow
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize transcription output: %v", err)
			} else {
				updates["transcription_output"] = tempEntry.TranscriptionOutput
			}
		}
		// Handle speech output from stream updates
		if streamResponse.Data.AudioOutput != nil {
			tempEntry.SpeechOutputParsed = streamResponse.Data.AudioOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize speech output: %v", err)
			} else {
				updates["speech_output"] = tempEntry.SpeechOutput
			}
		}
		// Handle image generation output from stream updates
		if streamResponse.Data.ImageGenerationOutput != nil {
			tempEntry.ImageGenerationOutputParsed = streamResponse.Data.ImageGenerationOutput
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize image generation output: %v", err)
			} else {
				updates["image_generation_output"] = tempEntry.ImageGenerationOutput
			}
		}
		// Handle cache debug
		if cacheDebug != nil {
			tempEntry.CacheDebugParsed = cacheDebug
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize cache debug: %v", err)
			} else {
				updates["cache_debug"] = tempEntry.CacheDebug
			}
		}
		// Create content summary
		if streamResponse.Data.OutputMessage != nil {
			tempEntry.OutputMessageParsed = streamResponse.Data.OutputMessage
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize output message: %v", err)
			} else {
				updates["output_message"] = tempEntry.OutputMessage
				updates["content_summary"] = tempEntry.ContentSummary
			}
		}
		// Handle responses output from stream updates
		if streamResponse.Data.OutputMessages != nil {
			tempEntry.ResponsesOutputParsed = streamResponse.Data.OutputMessages
			if err := tempEntry.SerializeFields(); err != nil {
				p.logger.Error("failed to serialize responses output: %v", err)
			} else {
				updates["responses_output"] = tempEntry.ResponsesOutput
			}
		}
		// Handle raw request from stream updates
		if streamResponse.RawRequest != nil && *streamResponse.RawRequest != nil {
			rawRequestBytes, err := sonic.Marshal(*streamResponse.RawRequest)
			if err != nil {
				p.logger.Error("failed to marshal raw request: %v", err)
			} else {
				updates["raw_request"] = string(rawRequestBytes)
			}
		}
		// Handle raw response from stream updates
		if streamResponse.Data.RawResponse != nil {
			updates["raw_response"] = *streamResponse.Data.RawResponse
		}
	}
	// Only perform update if there's something to update
	if len(updates) > 0 {
		return p.store.Update(ctx, requestID, updates)
	}
	return nil
}

// getLogEntry retrieves a log entry by ID using GORM
func (p *LoggerPlugin) getLogEntry(ctx context.Context, requestID string) (*logstore.Log, error) {
	entry, err := p.store.FindFirst(ctx, map[string]interface{}{"id": requestID})
	if err != nil {
		return nil, err
	}
	return entry, nil
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

// GetAvailableModels returns all unique models from logs
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

// GetAvailableRoutingEngines returns all unique routing engine types used in logs
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
	scopes := pricingScopesForLog(logEntry)

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

	return p.pricingManager.CalculateCostWithScopes(resp, scopes), nil
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
		// Convert BifrostLLMUsage back to ResponsesResponseUsage
		var respUsage *schemas.ResponsesResponseUsage
		if usage != nil {
			respUsage = &schemas.ResponsesResponseUsage{
				InputTokens:  usage.PromptTokens,
				OutputTokens: usage.CompletionTokens,
				TotalTokens:  usage.TotalTokens,
				Cost:         usage.Cost,
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
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
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

func pricingScopesForLog(logEntry *logstore.Log) modelcatalog.PricingLookupScopes {
	if logEntry == nil {
		return modelcatalog.PricingLookupScopes{}
	}

	virtualKeyID := ""
	if logEntry.VirtualKeyID != nil {
		virtualKeyID = *logEntry.VirtualKeyID
	}

	return modelcatalog.PricingLookupScopes{
		ProviderID:    logEntry.Provider,
		ProviderKeyID: logEntry.SelectedKeyID,
		VirtualKeyID:  virtualKeyID,
	}
}
