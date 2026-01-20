// Package logging provides utility functions and interfaces for the GORM-based logging plugin
package logging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/streaming"
)

// KeyPair represents an ID-Name pair for keys
type KeyPair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LogManager defines the main interface that combines all logging functionality
type LogManager interface {
	// Search searches for log entries based on filters and pagination
	Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error)

	// GetStats calculates statistics for logs matching the given filters
	GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error)

	// GetHistogram returns time-bucketed request counts for the given filters
	GetHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error)

	// GetTokenHistogram returns time-bucketed token usage for the given filters
	GetTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error)

	// GetCostHistogram returns time-bucketed cost data with model breakdown for the given filters
	GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error)

	// GetModelHistogram returns time-bucketed model usage with success/error breakdown for the given filters
	GetModelHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ModelHistogramResult, error)

	// Get the number of dropped requests
	GetDroppedRequests(ctx context.Context) int64

	// GetAvailableModels returns all unique models from logs
	GetAvailableModels(ctx context.Context) []string

	// GetAvailableSelectedKeys returns all unique selected key ID-Name pairs from logs
	GetAvailableSelectedKeys(ctx context.Context) []KeyPair

	// GetAvailableVirtualKeys returns all unique virtual key ID-Name pairs from logs
	GetAvailableVirtualKeys(ctx context.Context) []KeyPair

	// DeleteLog deletes a log entry by its ID
	DeleteLog(ctx context.Context, id string) error

	// DeleteLogs deletes multiple log entries by their IDs
	DeleteLogs(ctx context.Context, ids []string) error

	// RecalculateCosts recomputes missing costs for logs matching the filters
	RecalculateCosts(ctx context.Context, filters *logstore.SearchFilters, limit int) (*RecalculateCostResult, error)
}

// PluginLogManager implements LogManager interface wrapping the plugin
type PluginLogManager struct {
	plugin *LoggerPlugin
}

func (p *PluginLogManager) Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	if filters == nil || pagination == nil {
		return nil, fmt.Errorf("filters and pagination cannot be nil")
	}
	return p.plugin.SearchLogs(ctx, *filters, *pagination)
}

func (p *PluginLogManager) GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetStats(ctx, *filters)
}

func (p *PluginLogManager) GetHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetTokenHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetCostHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetModelHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ModelHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetModelHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetDroppedRequests(ctx context.Context) int64 {
	return p.plugin.droppedRequests.Load()
}

// GetAvailableModels returns all unique models from logs
func (p *PluginLogManager) GetAvailableModels(ctx context.Context) []string {
	return p.plugin.GetAvailableModels(ctx)
}

// GetAvailableSelectedKeys returns all unique selected key ID-Name pairs from logs
func (p *PluginLogManager) GetAvailableSelectedKeys(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableSelectedKeys(ctx)
}

// GetAvailableVirtualKeys returns all unique virtual key ID-Name pairs from logs
func (p *PluginLogManager) GetAvailableVirtualKeys(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableVirtualKeys(ctx)
}

// DeleteLog deletes a log from the log store
func (p *PluginLogManager) DeleteLog(ctx context.Context, id string) error {
	if p.plugin == nil || p.plugin.store == nil {
		return fmt.Errorf("log store not initialized")
	}
	return p.plugin.store.DeleteLog(ctx, id)
}

// DeleteLogs deletes multiple logs from the log store
func (p *PluginLogManager) DeleteLogs(ctx context.Context, ids []string) error {
	if p.plugin == nil || p.plugin.store == nil {
		return fmt.Errorf("log store not initialized")
	}
	return p.plugin.store.DeleteLogs(ctx, ids)
}

func (p *PluginLogManager) RecalculateCosts(ctx context.Context, filters *logstore.SearchFilters, limit int) (*RecalculateCostResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.RecalculateCosts(ctx, *filters, limit)
}

// GetPluginLogManager returns a LogManager interface for this plugin
func (p *LoggerPlugin) GetPluginLogManager() *PluginLogManager {
	return &PluginLogManager{
		plugin: p,
	}
}

// retryOnNotFound retries a function up to 3 times with 1-second delays if it returns logstore.ErrNotFound
func retryOnNotFound(ctx context.Context, operation func() error) error {
	const maxRetries = 3
	const retryDelay = time.Second

	var lastErr error
	for attempt := range maxRetries {
		err := operation()
		if err == nil {
			return nil
		}

		// Check if the error is logstore.ErrNotFound
		if !errors.Is(err, logstore.ErrNotFound) {
			return err
		}

		lastErr = err

		// Don't wait after the last attempt
		if attempt < maxRetries-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
				// Continue to next retry
			}
		}
	}

	return lastErr
}

// extractInputHistory extracts input history from request input
func (p *LoggerPlugin) extractInputHistory(request *schemas.BifrostRequest) ([]schemas.ChatMessage, []schemas.ResponsesMessage) {
	if request.ChatRequest != nil {
		return request.ChatRequest.Input, []schemas.ResponsesMessage{}
	}
	if request.ResponsesRequest != nil && len(request.ResponsesRequest.Input) > 0 {
		return []schemas.ChatMessage{}, request.ResponsesRequest.Input
	}
	if request.TextCompletionRequest != nil {
		var text string
		if request.TextCompletionRequest.Input.PromptStr != nil {
			text = *request.TextCompletionRequest.Input.PromptStr
		} else {
			var stringBuilder strings.Builder
			for _, prompt := range request.TextCompletionRequest.Input.PromptArray {
				stringBuilder.WriteString(prompt)
			}
			text = stringBuilder.String()
		}
		return []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &text,
				},
			},
		}, []schemas.ResponsesMessage{}
	}
	if request.EmbeddingRequest != nil {
		texts := request.EmbeddingRequest.Input.Texts

		if len(texts) == 0 && request.EmbeddingRequest.Input.Text != nil {
			texts = []string{*request.EmbeddingRequest.Input.Text}
		}

		contentBlocks := make([]schemas.ChatContentBlock, len(texts))
		for i, text := range texts {
			// Create a per-iteration copy to avoid reusing the same memory address
			t := text
			contentBlocks[i] = schemas.ChatContentBlock{
				Type: schemas.ChatContentBlockTypeText,
				Text: &t,
			}
		}
		return []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: contentBlocks,
				},
			},
		}, []schemas.ResponsesMessage{}
	}
	return []schemas.ChatMessage{}, []schemas.ResponsesMessage{}
}

// getStringFromContext safely extracts a string value from context
func getStringFromContext(ctx context.Context, key any) string {
	if value := ctx.Value(key); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// getIntFromContext safely extracts an int value from context
func getIntFromContext(ctx context.Context, key any) int {
	if value := ctx.Value(key); value != nil {
		if intVal, ok := value.(int); ok {
			return intVal
		}
	}
	return 0
}

// convertToProcessedStreamResponse converts a StreamAccumulatorResult to ProcessedStreamResponse
// for use with the logging plugin's streaming log update functionality.
func convertToProcessedStreamResponse(result *schemas.StreamAccumulatorResult, requestType schemas.RequestType) *streaming.ProcessedStreamResponse {
	if result == nil {
		return nil
	}

	// Determine stream type from request type
	var streamType streaming.StreamType
	switch requestType {
	case schemas.TextCompletionStreamRequest:
		streamType = streaming.StreamTypeText
	case schemas.ChatCompletionStreamRequest:
		streamType = streaming.StreamTypeChat
	case schemas.ResponsesStreamRequest:
		streamType = streaming.StreamTypeResponses
	case schemas.SpeechStreamRequest:
		streamType = streaming.StreamTypeAudio
	case schemas.TranscriptionStreamRequest:
		streamType = streaming.StreamTypeTranscription
	case schemas.ImageGenerationStreamRequest:
		streamType = streaming.StreamTypeImage
	default:
		streamType = streaming.StreamTypeChat
	}

	// Build accumulated data
	data := &streaming.AccumulatedData{
		RequestID:             result.RequestID,
		Model:                 result.Model,
		Status:                result.Status,
		Stream:                true,
		Latency:               result.Latency,
		TimeToFirstToken:      result.TimeToFirstToken,
		OutputMessage:         result.OutputMessage,
		OutputMessages:        result.OutputMessages,
		ErrorDetails:          result.ErrorDetails,
		TokenUsage:            result.TokenUsage,
		Cost:                  result.Cost,
		AudioOutput:           result.AudioOutput,
		TranscriptionOutput:   result.TranscriptionOutput,
		ImageGenerationOutput: result.ImageGenerationOutput,
		FinishReason:          result.FinishReason,
		RawResponse:           result.RawResponse,
	}

	// Handle tool calls if present
	if result.OutputMessage != nil && result.OutputMessage.ChatAssistantMessage != nil {
		data.ToolCalls = result.OutputMessage.ChatAssistantMessage.ToolCalls
	}

	resp := &streaming.ProcessedStreamResponse{
		RequestID:  result.RequestID,
		StreamType: streamType,
		Provider:   result.Provider,
		Model:      result.Model,
		Data:       data,
	}

	if result.RawRequest != nil {
		rawReq := result.RawRequest
		resp.RawRequest = &rawReq
	}

	return resp
}
