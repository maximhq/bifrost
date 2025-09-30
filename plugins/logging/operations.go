// Package logging provides database operations for the GORM-based logging plugin
package logging

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// insertInitialLogEntry creates a new log entry in the database using GORM
func (p *LoggerPlugin) insertInitialLogEntry(requestID string, timestamp time.Time, data *InitialLogData) error {
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

	return p.store.Create(entry)
}

// updateLogEntry updates an existing log entry using GORM
func (p *LoggerPlugin) updateLogEntry(ctx context.Context, requestID string, timestamp time.Time, cacheDebug *schemas.BifrostCacheDebug, data *UpdateLogData) error {
	updates := make(map[string]interface{})
	if !timestamp.IsZero() {
		// Try to get original timestamp from context first for latency calculation
		latency, err := p.calculateLatency(ctx, requestID, timestamp)
		if err != nil {
			return err
		}
		updates["latency"] = latency
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
	if data.OutputMessage != nil {
		tempEntry.OutputMessageParsed = data.OutputMessage
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Error("failed to serialize output message: %v", err)
		} else {
			updates["output_message"] = tempEntry.OutputMessage
			updates["content_summary"] = tempEntry.ContentSummary // Update content summary
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

	if data.ToolCalls != nil {
		tempEntry.ToolCallsParsed = data.ToolCalls
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Error("failed to serialize tool calls: %v", err)
		} else {
			updates["tool_calls"] = tempEntry.ToolCalls
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
	return p.store.Update(requestID, updates)
}

// processStreamUpdate handles streaming updates using GORM
func (p *LoggerPlugin) processStreamUpdate(ctx context.Context, requestID string, timestamp time.Time, cacheDebug *schemas.BifrostCacheDebug, data *StreamUpdateData, isFinalChunk bool) error {
	updates := make(map[string]interface{})

	// Handle error case first
	if data.ErrorDetails != nil {
		latency, err := p.calculateLatency(ctx, requestID, timestamp)
		if err != nil {
			// If we can't get created_at, just update status and error
			tempEntry := &logstore.Log{}
			tempEntry.ErrorDetailsParsed = data.ErrorDetails
			if err := tempEntry.SerializeFields(); err == nil {
				return p.store.Update(requestID, map[string]interface{}{
					"status":        "error",
					"error_details": tempEntry.ErrorDetails,
					"timestamp":     timestamp,
				})
			}
			return err
		}

		tempEntry := &logstore.Log{}
		tempEntry.ErrorDetailsParsed = data.ErrorDetails
		if err := tempEntry.SerializeFields(); err != nil {
			return fmt.Errorf("failed to serialize error details: %w", err)
		}
		return p.store.Update(requestID, map[string]interface{}{
			"status":        "error",
			"latency":       latency,
			"timestamp":     timestamp,
			"error_details": tempEntry.ErrorDetails,
		})
	}

	// Always mark as streaming and update timestamp
	updates["stream"] = true
	updates["timestamp"] = timestamp

	// Calculate latency when stream finishes
	var needsLatency bool
	var latency float64
	tempEntry := &logstore.Log{}

	if isFinalChunk {
		// Stream is finishing, calculate latency
		var err error
		latency, err = p.calculateLatency(ctx, requestID, timestamp)
		if err != nil {
			return fmt.Errorf("failed to get created_at for latency calculation: %w", err)
		}
		needsLatency = true
	}

	// Add latency if this is the final chunk
	if needsLatency {
		updates["latency"] = latency
	}

	// Update model if provided
	if data.Model != "" {
		updates["model"] = data.Model
	}

	// Update object type if provided
	if data.Object != "" {
		updates["object_type"] = data.Object // Note: using object_type for database column
	}

	// Update token usage if provided
	if data.TokenUsage != nil {
		tempEntry.TokenUsageParsed = data.TokenUsage
		if err := tempEntry.SerializeFields(); err == nil {
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

	// Handle finish reason - if present, mark as complete
	if isFinalChunk {
		updates["status"] = "success"
	}

	// Process delta content and tool calls if present
	if data.Delta != nil {
		deltaUpdates, err := p.prepareDeltaUpdates(requestID, data.Delta)
		if err != nil {
			return fmt.Errorf("failed to prepare delta updates: %w", err)
		}
		// Merge delta updates into main updates
		for key, value := range deltaUpdates {
			updates[key] = value
		}
	}

	// Handle transcription output from stream updates
	if data.TranscriptionOutput != nil {
		tempEntry.TranscriptionOutputParsed = data.TranscriptionOutput
		// Here we just log error but move one vs breaking the entire logging flow
		if err := tempEntry.SerializeFields(); err != nil {
			p.logger.Warn("failed to serialize transcription output: %v", err)
		} else {
			updates["transcription_output"] = tempEntry.TranscriptionOutput
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

	// Only perform update if there's something to update
	if len(updates) > 0 {
		return p.store.Update(requestID, updates)
	}

	return nil
}

// calculateLatency computes latency in milliseconds from creation time
func (p *LoggerPlugin) calculateLatency(ctx context.Context, requestID string, currentTime time.Time) (float64, error) {
	// Try to get original timestamp from context first
	if ctxTimestamp, ok := ctx.Value(CreatedTimestampKey).(time.Time); ok {
		return float64(currentTime.Sub(ctxTimestamp).Nanoseconds()) / 1e6, nil
	}
	var originalEntry *logstore.Log
	err := retryOnNotFound(ctx, func() error {
		var opErr error
		originalEntry, opErr = p.store.FindFirst(map[string]interface{}{"id": requestID}, "created_at")
		return opErr
	})
	if err != nil {
		return 0, err
	}
	return float64(currentTime.Sub(originalEntry.CreatedAt).Nanoseconds()) / 1e6, nil
}

// prepareDeltaUpdates prepares updates for streaming delta content without executing them
func (p *LoggerPlugin) prepareDeltaUpdates(requestID string, delta *schemas.BifrostStreamDelta) (map[string]interface{}, error) {
	// Only fetch existing content if we have content or tool calls to append
	if (delta.Content == nil || *delta.Content == "") && len(delta.ToolCalls) == 0 && delta.Refusal == nil {
		return map[string]interface{}{}, nil
	}

	// Get current entry
	var currentEntry *logstore.Log
	currentEntry, err := p.store.FindFirst(map[string]interface{}{"id": requestID}, "output_message")
	if err != nil {
		return nil, fmt.Errorf("failed to get existing entry: %w", err)
	}

	// Parse existing message or create new one
	var outputMessage *schemas.BifrostMessage
	if currentEntry.OutputMessage != "" {
		outputMessage = &schemas.BifrostMessage{}
		// Attempt to deserialize; use parsed message only if successful
		if err := currentEntry.DeserializeFields(); err == nil && currentEntry.OutputMessageParsed != nil {
			outputMessage = currentEntry.OutputMessageParsed
		} else {
			// Create new message if parsing fails
			outputMessage = &schemas.BifrostMessage{
				Role:    schemas.ModelChatMessageRoleAssistant,
				Content: schemas.MessageContent{},
			}
		}
	} else {
		// Create new message
		outputMessage = &schemas.BifrostMessage{
			Role:    schemas.ModelChatMessageRoleAssistant,
			Content: schemas.MessageContent{},
		}
	}

	// Handle role (usually in first chunk)
	if delta.Role != nil {
		outputMessage.Role = schemas.ModelChatMessageRole(*delta.Role)
	}

	// Append content
	if delta.Content != nil && *delta.Content != "" {
		p.appendContentToMessage(outputMessage, *delta.Content)
	}

	// Handle refusal
	if delta.Refusal != nil && *delta.Refusal != "" {
		if outputMessage.AssistantMessage == nil {
			outputMessage.AssistantMessage = &schemas.AssistantMessage{}
		}
		if outputMessage.AssistantMessage.Refusal == nil {
			outputMessage.AssistantMessage.Refusal = delta.Refusal
		} else {
			*outputMessage.AssistantMessage.Refusal += *delta.Refusal
		}
	}

	// Accumulate tool calls
	if len(delta.ToolCalls) > 0 {
		p.accumulateToolCallsInMessage(outputMessage, delta.ToolCalls)
	}

	// Update the database with new content
	tempEntry := &logstore.Log{
		OutputMessageParsed: outputMessage,
	}
	if outputMessage.AssistantMessage != nil && outputMessage.AssistantMessage.ToolCalls != nil {
		tempEntry.ToolCallsParsed = outputMessage.AssistantMessage.ToolCalls
	}

	if err := tempEntry.SerializeFields(); err != nil {
		return nil, fmt.Errorf("failed to serialize fields: %w", err)
	}

	updates := map[string]interface{}{
		"output_message":  tempEntry.OutputMessage,
		"content_summary": tempEntry.ContentSummary,
	}

	// Also update tool_calls field for backward compatibility
	if tempEntry.ToolCalls != "" {
		updates["tool_calls"] = tempEntry.ToolCalls
	}

	return updates, nil
}

// getLogEntry retrieves a log entry by ID using GORM
func (p *LoggerPlugin) getLogEntry(requestID string) (*logstore.Log, error) {
	entry, err := p.store.FindFirst(map[string]interface{}{"id": requestID})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// SearchLogs searches logs with filters and pagination using GORM
func (p *LoggerPlugin) SearchLogs(filters logstore.SearchFilters, pagination logstore.PaginationOptions) (*logstore.SearchResult, error) {
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
	return p.store.SearchLogs(filters, pagination)
}

// GetAvailableModels returns all unique models from logs
func (p *LoggerPlugin) GetAvailableModels() []string {
	modelSet := make(map[string]bool)
	// Query distinct models from logs
	result, err := p.store.FindAll("model IS NOT NULL AND model != ''", "model")
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
