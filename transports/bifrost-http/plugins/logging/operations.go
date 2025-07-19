// Package logging provides database operations for the GORM-based logging plugin
package logging

import (
	"context"
	"fmt"
	"time"

	"database/sql"

	"gorm.io/gorm"

	"github.com/maximhq/bifrost/core/schemas"
)

// insertInitialLogEntry creates a new log entry in the database using GORM
func (p *LoggerPlugin) insertInitialLogEntry(requestID string, timestamp time.Time, data *InitialLogData) error {
	entry := &LogEntry{
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

	return p.db.Create(entry).Error
}

// updateLogEntry updates an existing log entry using GORM
func (p *LoggerPlugin) updateLogEntry(requestID string, timestamp time.Time, data *UpdateLogData, ctx context.Context) error {
	updates := make(map[string]interface{})

	// Try to get original timestamp from context first for latency calculation
	latency, err := p.calculateLatency(requestID, timestamp, ctx)
	if err != nil {
		return err
	}
	updates["latency"] = latency

	updates["status"] = data.Status

	if data.Model != "" {
		updates["model"] = data.Model
	}

	if data.Object != "" {
		updates["object_type"] = data.Object // Note: using object_type for database column
	}

	// Handle JSON fields by setting them on a temporary entry and serializing
	tempEntry := &LogEntry{}
	if data.OutputMessage != nil {
		tempEntry.OutputMessageParsed = data.OutputMessage
		if err := tempEntry.serializeFields(); err == nil {
			updates["output_message"] = tempEntry.OutputMessage
			updates["content_summary"] = tempEntry.ContentSummary // Update content summary
		}
	}

	if data.ToolCalls != nil {
		tempEntry.ToolCallsParsed = data.ToolCalls
		if err := tempEntry.serializeFields(); err == nil {
			updates["tool_calls"] = tempEntry.ToolCalls
		}
	}

	if data.SpeechOutput != nil {
		tempEntry.SpeechOutputParsed = data.SpeechOutput
		if err := tempEntry.serializeFields(); err == nil {
			updates["speech_output"] = tempEntry.SpeechOutput
		}
	}

	if data.TranscriptionOutput != nil {
		tempEntry.TranscriptionOutputParsed = data.TranscriptionOutput
		if err := tempEntry.serializeFields(); err == nil {
			updates["transcription_output"] = tempEntry.TranscriptionOutput
		}
	}

	if data.TokenUsage != nil {
		tempEntry.TokenUsageParsed = data.TokenUsage
		if err := tempEntry.serializeFields(); err == nil {
			updates["token_usage"] = tempEntry.TokenUsage
			updates["prompt_tokens"] = data.TokenUsage.PromptTokens
			updates["completion_tokens"] = data.TokenUsage.CompletionTokens
			updates["total_tokens"] = data.TokenUsage.TotalTokens
		}
	}

	if data.ErrorDetails != nil {
		tempEntry.ErrorDetailsParsed = data.ErrorDetails
		if err := tempEntry.serializeFields(); err == nil {
			updates["error_details"] = tempEntry.ErrorDetails
		}
	}

	return p.db.Model(&LogEntry{}).Where("id = ?", requestID).Updates(updates).Error
}

// processStreamUpdate handles streaming updates using GORM
func (p *LoggerPlugin) processStreamUpdate(requestID string, timestamp time.Time, data *StreamUpdateData, isFinalChunk bool, ctx context.Context) error {
	updates := make(map[string]interface{})

	// Handle error case first
	if data.ErrorDetails != nil {
		latency, err := p.calculateLatency(requestID, timestamp, ctx)
		if err != nil {
			// If we can't get created_at, just update status and error
			tempEntry := &LogEntry{}
			tempEntry.ErrorDetailsParsed = data.ErrorDetails
			if err := tempEntry.serializeFields(); err == nil {
				return p.db.Model(&LogEntry{}).Where("id = ?", requestID).Updates(map[string]interface{}{
					"status":        "error",
					"error_details": tempEntry.ErrorDetails,
				}).Error
			}
			return err
		}

		tempEntry := &LogEntry{}
		tempEntry.ErrorDetailsParsed = data.ErrorDetails
		if err := tempEntry.serializeFields(); err != nil {
			return fmt.Errorf("failed to serialize error details: %w", err)
		}
		return p.db.Model(&LogEntry{}).Where("id = ?", requestID).Updates(map[string]interface{}{
			"status":        "error",
			"error_details": tempEntry.ErrorDetails,
			"latency":       latency,
			"timestamp":     timestamp,
		}).Error
	}

	// Always mark as streaming and update timestamp
	updates["stream"] = true
	updates["timestamp"] = timestamp

	// Calculate latency when stream finishes
	var needsLatency bool
	var latency float64

	if isFinalChunk {
		// Stream is finishing, calculate latency
		var err error
		latency, err = p.calculateLatency(requestID, timestamp, ctx)
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
		tempEntry := &LogEntry{}
		tempEntry.TokenUsageParsed = data.TokenUsage
		if err := tempEntry.serializeFields(); err == nil {
			updates["token_usage"] = tempEntry.TokenUsage
			updates["prompt_tokens"] = data.TokenUsage.PromptTokens
			updates["completion_tokens"] = data.TokenUsage.CompletionTokens
			updates["total_tokens"] = data.TokenUsage.TotalTokens
		}
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
		tempEntry := &LogEntry{}
		tempEntry.TranscriptionOutputParsed = data.TranscriptionOutput
		if err := tempEntry.serializeFields(); err != nil {
			return fmt.Errorf("failed to serialize transcription output: %w", err)
		}
		updates["transcription_output"] = tempEntry.TranscriptionOutput
	}

	// Only perform update if there's something to update
	if len(updates) > 0 {
		return p.db.Model(&LogEntry{}).Where("id = ?", requestID).Updates(updates).Error
	}

	return nil
}

// calculateLatency computes latency in milliseconds from creation time
func (p *LoggerPlugin) calculateLatency(requestID string, currentTime time.Time, ctx context.Context) (float64, error) {
	// Try to get original timestamp from context first
	if ctxTimestamp, ok := ctx.Value(CreatedTimestampKey).(time.Time); ok {
		return float64(currentTime.Sub(ctxTimestamp).Nanoseconds()) / 1e6, nil
	}

	// Fallback to database query if not found in context
	var originalEntry LogEntry
	if err := p.db.Select("created_at").Where("id = ?", requestID).First(&originalEntry).Error; err != nil {
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
	var currentEntry LogEntry
	if err := p.db.Where("id = ?", requestID).First(&currentEntry).Error; err != nil {
		return nil, fmt.Errorf("failed to get existing entry: %w", err)
	}

	// Parse existing message or create new one
	var outputMessage *schemas.BifrostMessage
	if currentEntry.OutputMessage != "" {
		outputMessage = &schemas.BifrostMessage{}
		// Attempt to deserialize; use parsed message only if successful
		if err := currentEntry.deserializeFields(); err == nil && currentEntry.OutputMessageParsed != nil {
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
	tempEntry := &LogEntry{
		OutputMessageParsed: outputMessage,
	}
	if outputMessage.AssistantMessage != nil && outputMessage.AssistantMessage.ToolCalls != nil {
		tempEntry.ToolCallsParsed = outputMessage.AssistantMessage.ToolCalls
	}

	if err := tempEntry.serializeFields(); err != nil {
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
func (p *LoggerPlugin) getLogEntry(requestID string) (*LogEntry, error) {
	var entry LogEntry
	err := p.db.Where("id = ?", requestID).First(&entry).Error
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// SearchLogs searches logs with filters and pagination using GORM
func (p *LoggerPlugin) SearchLogs(filters SearchFilters, pagination PaginationOptions) (*SearchResult, error) {
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
	baseQuery := p.db.Model(&LogEntry{})

	// Apply filters efficiently
	if len(filters.Providers) > 0 {
		baseQuery = baseQuery.Where("provider IN ?", filters.Providers)
	}
	if len(filters.Models) > 0 {
		baseQuery = baseQuery.Where("model IN ?", filters.Models)
	}
	if len(filters.Status) > 0 {
		baseQuery = baseQuery.Where("status IN ?", filters.Status)
	}
	if len(filters.Objects) > 0 {
		baseQuery = baseQuery.Where("object_type IN ?", filters.Objects)
	}
	if filters.StartTime != nil {
		baseQuery = baseQuery.Where("timestamp >= ?", *filters.StartTime)
	}
	if filters.EndTime != nil {
		baseQuery = baseQuery.Where("timestamp <= ?", *filters.EndTime)
	}
	if filters.MinLatency != nil {
		baseQuery = baseQuery.Where("latency >= ?", *filters.MinLatency)
	}
	if filters.MaxLatency != nil {
		baseQuery = baseQuery.Where("latency <= ?", *filters.MaxLatency)
	}
	if filters.MinTokens != nil {
		baseQuery = baseQuery.Where("total_tokens >= ?", *filters.MinTokens)
	}
	if filters.MaxTokens != nil {
		baseQuery = baseQuery.Where("total_tokens <= ?", *filters.MaxTokens)
	}
	if filters.ContentSearch != "" {
		baseQuery = baseQuery.Where("content_summary LIKE ?", "%"+filters.ContentSearch+"%")
	}

	// Get total count
	var totalCount int64
	if err := baseQuery.Count(&totalCount).Error; err != nil {
		return nil, err
	}

	// Initialize stats
	stats := SearchStats{}

	// Calculate statistics efficiently if we have data
	if totalCount > 0 {
		// Total requests should include all requests (processing, success, error)
		stats.TotalRequests = totalCount

		// Get completed requests count (success + error, excluding processing) for success rate calculation
		var completedCount int64
		completedQuery := baseQuery.Session(&gorm.Session{})
		if err := completedQuery.Where("status IN ?", []string{"success", "error"}).Count(&completedCount).Error; err != nil {
			return nil, err
		}

		if completedCount > 0 {
			// Calculate success rate based on completed requests only
			var successCount int64
			successQuery := baseQuery.Session(&gorm.Session{})
			if err := successQuery.Where("status = ?", "success").Count(&successCount).Error; err != nil {
				return nil, err
			}
			stats.SuccessRate = float64(successCount) / float64(completedCount) * 100

			// Calculate average latency and total tokens in a single query for better performance
			var result struct {
				AvgLatency  sql.NullFloat64 `json:"avg_latency"`
				TotalTokens sql.NullInt64   `json:"total_tokens"`
			}

			statsQuery := baseQuery.Session(&gorm.Session{})
			if err := statsQuery.Select("AVG(latency) as avg_latency, SUM(total_tokens) as total_tokens").Scan(&result).Error; err != nil {
				return nil, err
			}

			if result.AvgLatency.Valid {
				stats.AverageLatency = result.AvgLatency.Float64
			}
			if result.TotalTokens.Valid {
				stats.TotalTokens = result.TotalTokens.Int64
			}
		}
	}

	// Build order clause
	direction := "DESC"
	if pagination.Order == "asc" {
		direction = "ASC"
	}

	var orderClause string
	switch pagination.SortBy {
	case "timestamp":
		orderClause = "timestamp " + direction
	case "latency":
		orderClause = "latency " + direction
	case "tokens":
		orderClause = "total_tokens " + direction
	default:
		orderClause = "timestamp " + direction
	}

	// Execute main query with sorting and pagination
	var logs []LogEntry
	mainQuery := baseQuery.Order(orderClause)

	if pagination.Limit > 0 {
		mainQuery = mainQuery.Limit(pagination.Limit)
	}
	if pagination.Offset > 0 {
		mainQuery = mainQuery.Offset(pagination.Offset)
	}

	if err := mainQuery.Find(&logs).Error; err != nil {
		return nil, err
	}

	return &SearchResult{
		Logs:       logs,
		Pagination: pagination,
		Stats:      stats,
	}, nil
}


