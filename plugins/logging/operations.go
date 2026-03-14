// Package logging provides database operations for the trace/span-based logging plugin
package logging

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// SearchTraces searches traces with filters and pagination
func (p *LoggerPlugin) SearchTraces(ctx context.Context, filters logstore.SearchFilters, pagination logstore.PaginationOptions) (*logstore.TraceSearchResult, error) {
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
	return p.store.SearchTraces(ctx, filters, pagination)
}

// GetTrace retrieves a single trace (root span + children) by ID.
func (p *LoggerPlugin) GetTrace(ctx context.Context, id string) (*logstore.SpanLog, []*logstore.SpanLog, error) {
	return p.store.FindTraceWithSpans(ctx, id)
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

// GetAvailableModels returns all unique models from logs.
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

// DeleteTraces deletes traces (root spans and their children) by IDs
func (p *LoggerPlugin) DeleteTraces(ctx context.Context, ids []string) error {
	return p.store.DeleteTraces(ctx, ids)
}

// RecalculateCosts recomputes cost for span log entries that are missing cost values
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

	searchResult, err := p.store.SearchTraces(ctx, filters, pagination)
	if err != nil {
		return nil, fmt.Errorf("failed to search traces for cost recalculation: %w", err)
	}

	result := &RecalculateCostResult{
		TotalMatched: searchResult.Stats.TotalRequests,
	}

	costUpdates := make(map[string]float64, len(searchResult.Traces))

	for _, spanEntry := range searchResult.Traces {
		cost, calcErr := p.calculateCostForSpan(&spanEntry)
		if calcErr != nil {
			result.Skipped++
			p.logger.Debug("skipping cost recalculation for span %s: %v", spanEntry.ID, calcErr)
			continue
		}
		costUpdates[spanEntry.ID] = cost
	}

	if len(costUpdates) > 0 {
		if err := p.store.BulkUpdateCost(ctx, costUpdates); err != nil {
			return nil, fmt.Errorf("failed to bulk update costs: %w", err)
		}
		result.Updated = len(costUpdates)
	}

	// Re-count how many traces still match the missing-cost filter after updates
	remainingResult, err := p.store.SearchTraces(ctx, filters, logstore.PaginationOptions{
		Limit:  1,
		Offset: 0,
		SortBy: "timestamp",
		Order:  "asc",
	})
	if err != nil {
		p.logger.Warn("failed to recompute remaining missing-cost traces: %v", err)
	} else {
		result.Remaining = remainingResult.Stats.TotalRequests
	}

	return result, nil
}

func (p *LoggerPlugin) calculateCostForSpan(spanEntry *logstore.SpanLog) (float64, error) {
	if spanEntry == nil {
		return 0, fmt.Errorf("span entry cannot be nil")
	}

	if (spanEntry.TokenUsageParsed == nil && spanEntry.TokenUsage != "") ||
		(spanEntry.CacheDebugParsed == nil && spanEntry.CacheDebug != "") {
		if err := spanEntry.DeserializeFields(); err != nil {
			return 0, fmt.Errorf("failed to deserialize fields for span %s: %w", spanEntry.ID, err)
		}
	}

	usage := spanEntry.TokenUsageParsed
	cacheDebug := spanEntry.CacheDebugParsed

	// If no cache hit and no usage, we can't calculate cost
	if usage == nil && (cacheDebug == nil || !cacheDebug.CacheHit) {
		return 0, fmt.Errorf("token usage not available for span %s", spanEntry.ID)
	}

	requestType := schemas.RequestType(spanEntry.Object)
	if requestType == "" && (cacheDebug == nil || !cacheDebug.CacheHit) {
		p.logger.Warn("skipping cost calculation for span %s: object type is empty (timestamp: %s)", spanEntry.ID, spanEntry.Timestamp)
		return 0, fmt.Errorf("object type is empty for span %s", spanEntry.ID)
	}

	extraFields := schemas.BifrostResponseExtraFields{
		RequestType:    requestType,
		Provider:       schemas.ModelProvider(spanEntry.Provider),
		ModelRequested: spanEntry.Model,
		CacheDebug:     cacheDebug,
	}

	resp := buildResponseForRequestType(requestType, usage, extraFields)

	// Patch modality-specific output fields
	if resp.TranscriptionResponse != nil &&
		spanEntry.TranscriptionOutputParsed != nil &&
		spanEntry.TranscriptionOutputParsed.Usage != nil {
		resp.TranscriptionResponse.Usage = spanEntry.TranscriptionOutputParsed.Usage
	}

	if resp.ImageGenerationResponse != nil && spanEntry.ImageGenerationOutputParsed != nil {
		parsed := spanEntry.ImageGenerationOutputParsed
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

	if resp.SpeechResponse != nil &&
		spanEntry.SpeechOutputParsed != nil &&
		spanEntry.SpeechOutputParsed.Usage != nil {
		resp.SpeechResponse.Usage = spanEntry.SpeechOutputParsed.Usage
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
