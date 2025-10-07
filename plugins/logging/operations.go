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
func (p *LoggerPlugin) insertInitialLogEntry(ctx context.Context, requestID string, parentRequestID string, timestamp time.Time, data *InitialLogData) error {
	entry := &logstore.Log{
		ID:        requestID,
		Timestamp: timestamp,
		Object:    data.Object,
		Provider:  data.Provider,
		Model:     data.Model,
		Status:    "processing",
		Stream:    false,
		CreatedAt: timestamp,
		// Set parsed fields for serialization
		InputHistoryParsed:       data.InputHistory,
		ParamsParsed:             data.Params,
		ToolsParsed:              data.Tools,
		SpeechInputParsed:        data.SpeechInput,
		TranscriptionInputParsed: data.TranscriptionInput,
	}

	if parentRequestID != "" {
		entry.ParentRequestID = &parentRequestID
	}

	return p.store.Create(ctx, entry)
}

// updateLogEntry updates an existing log entry using GORM
func (p *LoggerPlugin) updateLogEntry(ctx context.Context, requestID string, latency int64, cacheDebug *schemas.BifrostCacheDebug, data *UpdateLogData) error {
	updates := make(map[string]interface{})
	if latency != 0 {
		updates["latency"] = float64(latency)
	}
	updates["status"] = data.Status
	if data.Model != "" {
		updates["model"] = data.Model
	}
	if data.Object != "" {
		updates["object_type"] = data.Object // Note: using object_type for database column
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

	if data.ResponsesOutput != nil {
		tempEntry.ResponsesOutputParsed = data.ResponsesOutput
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Error("failed to serialize responses output: %v", err)
		} else {
			updates["responses_output"] = tempEntry.ResponsesOutput
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

	if data.RawResponse != nil {
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
func (p *LoggerPlugin) updateStreamingLogEntry(ctx context.Context, requestID string, cacheDebug *schemas.BifrostCacheDebug, streamResponse *streaming.ProcessedStreamResponse, isFinalChunk bool) error {
	p.logger.Debug("[logging] updating streaming log entry %s", requestID)
	updates := make(map[string]interface{})
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

	// Update object type if provided
	if streamResponse.Data.Object != "" {
		updates["object_type"] = streamResponse.Data.Object // Note: using object_type for database column
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
	// Handle transcription output from stream updates
	if streamResponse.Data.TranscriptionOutput != nil {
		tempEntry.TranscriptionOutputParsed = streamResponse.Data.TranscriptionOutput
		// Here we just log error but move one vs breaking the entire logging flow
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Warn("failed to serialize transcription output: %v", err)
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

// GetAvailableModels returns all unique models from logs
func (p *LoggerPlugin) GetAvailableModels(ctx context.Context) []string {
	modelSet := make(map[string]bool)
	// Query distinct models from logs
	result, err := p.store.FindAll(ctx, "model IS NOT NULL AND model != ''", "model")
	if err != nil {
		p.logger.Error("failed to get available models: %w", err)
		return []string{}
	}
	for _, model := range result {
		modelSet[model.Model] = true
	}
	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	return models
}
