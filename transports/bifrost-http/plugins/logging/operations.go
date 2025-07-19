// Package logging provides database operations for the GORM-based logging plugin
package logging

import (
	"fmt"
	"time"

	"database/sql"

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
		InputHistoryParsed: data.InputHistory,
		ParamsParsed:       data.Params,
		ToolsParsed:        data.Tools,
	}

	return p.db.Create(entry).Error
}

// updateLogEntry updates an existing log entry using GORM
func (p *LoggerPlugin) updateLogEntry(requestID string, timestamp time.Time, data *UpdateLogData) error {
	updates := make(map[string]interface{})

	// Calculate latency if we have the original timestamp
	var originalEntry LogEntry
	if err := p.db.Select("created_at").Where("id = ?", requestID).First(&originalEntry).Error; err == nil {
		latency := float64(timestamp.Sub(originalEntry.CreatedAt).Nanoseconds()) / 1e6 // Convert to milliseconds
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
func (p *LoggerPlugin) processStreamUpdate(requestID string, timestamp time.Time, data *StreamUpdateData) error {
	updates := make(map[string]interface{})

	updates["stream"] = true
	updates["timestamp"] = timestamp // Update timestamp for streaming deltas

	if data.Model != "" {
		updates["model"] = data.Model
	}

	if data.Object != "" {
		updates["object_type"] = data.Object // Note: using object_type for database column
	}

	// Handle JSON fields
	tempEntry := &LogEntry{}
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
			updates["status"] = "error"
		}
	} else if data.FinishReason != nil {
		updates["status"] = "success"
	}

	// Calculate latency when stream finishes (either success or error)
	if data.FinishReason != nil || data.ErrorDetails != nil {
		var originalEntry LogEntry
		if err := p.db.Select("created_at").Where("id = ?", requestID).First(&originalEntry).Error; err == nil {
			latency := float64(timestamp.Sub(originalEntry.CreatedAt).Nanoseconds()) / 1e6 // Convert to milliseconds
			updates["latency"] = latency
		}
	}

	// Handle streaming delta content if present - integrate into single update
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

	return p.db.Model(&LogEntry{}).Where("id = ?", requestID).Updates(updates).Error
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
		// Note: errors in unmarshaling are ignored as per original logic
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

// appendDeltaToEntry efficiently appends streaming delta content to existing database entry
// Deprecated: Use prepareDeltaUpdates for better transaction handling
func (p *LoggerPlugin) appendDeltaToEntry(requestID string, delta *schemas.BifrostStreamDelta) error {
	updates, err := p.prepareDeltaUpdates(requestID, delta)
	if err != nil {
		return err
	}

	return p.db.Model(&LogEntry{}).Where("id = ?", requestID).Updates(updates).Error
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

	query := p.db.Model(&LogEntry{})

	// Apply filters
	if len(filters.Providers) > 0 {
		query = query.Where("provider IN ?", filters.Providers)
	}

	if len(filters.Models) > 0 {
		query = query.Where("model IN ?", filters.Models)
	}

	if len(filters.Status) > 0 {
		query = query.Where("status IN ?", filters.Status)
	}

	if len(filters.Objects) > 0 {
		query = query.Where("object_type IN ?", filters.Objects) // Note: using object_type column
	}

	if filters.StartTime != nil {
		query = query.Where("timestamp >= ?", *filters.StartTime)
	}

	if filters.EndTime != nil {
		query = query.Where("timestamp <= ?", *filters.EndTime)
	}

	if filters.MinLatency != nil {
		query = query.Where("latency >= ?", *filters.MinLatency)
	}

	if filters.MaxLatency != nil {
		query = query.Where("latency <= ?", *filters.MaxLatency)
	}

	if filters.MinTokens != nil {
		query = query.Where("total_tokens >= ?", *filters.MinTokens)
	}

	if filters.MaxTokens != nil {
		query = query.Where("total_tokens <= ?", *filters.MaxTokens)
	}

	if filters.ContentSearch != "" {
		// Enhanced content search with FTS support check
		if p.checkFTSSupport() {
			// Use FTS for better search performance
			query = query.Where("id IN (SELECT rowid FROM logs_fts WHERE content_summary MATCH ?)", filters.ContentSearch)
		} else {
			// Fallback to LIKE search
			query = query.Where("content_summary LIKE ?", "%"+filters.ContentSearch+"%")
		}
	}

	// Clone query for statistics calculation (before pagination)
	statsQuery := query

	// Count total for pagination info
	var totalCount int64
	if err := query.Count(&totalCount).Error; err != nil {
		return nil, err
	}

	// Apply sorting
	orderClause := "timestamp DESC" // Default sorting
	if pagination.SortBy != "" {
		direction := "DESC"
		if pagination.Order == "asc" {
			direction = "ASC"
		}

		switch pagination.SortBy {
		case "timestamp":
			orderClause = "timestamp " + direction
		case "latency":
			orderClause = "latency " + direction
		case "tokens":
			orderClause = "total_tokens " + direction
		default:
			orderClause = "timestamp " + direction // fallback
		}
	}

	query = query.Order(orderClause)

	// Apply pagination
	if pagination.Limit > 0 {
		query = query.Limit(pagination.Limit)
	}
	if pagination.Offset > 0 {
		query = query.Offset(pagination.Offset)
	}

	// Execute main query
	var logs []LogEntry
	if err := query.Find(&logs).Error; err != nil {
		return nil, err
	}

	// Calculate statistics based on the same filters (filtered statistics)
	result := &SearchResult{
		Logs:       logs,
		Pagination: pagination,
	}

	result.Stats.TotalRequests = totalCount

	// Calculate filtered statistics (only from results matching the current filters)
	var globalAverageLatency float64
	var globalTotalTokens int64
	var globalCompletedRequests int64

	if totalCount > 0 {
		// Calculate success rate from filtered results
		var successCount int64
		statsQuery.Where("status = ?", "success").Count(&successCount)

		// Calculate completed requests (success + error, excluding processing)
		var completedCount int64
		statsQuery.Where("status IN ?", []string{"success", "error"}).Count(&completedCount)

		globalCompletedRequests = completedCount

		if completedCount > 0 {
			result.Stats.SuccessRate = float64(successCount) / float64(completedCount) * 100
		}

		// Calculate average latency from filtered results with latency
		var avgLatency sql.NullFloat64
		statsQuery.Select("AVG(latency)").Where("latency IS NOT NULL").Scan(&avgLatency)
		if avgLatency.Valid {
			globalAverageLatency = avgLatency.Float64
		}
		result.Stats.AverageLatency = globalAverageLatency

		// Calculate total tokens from filtered results
		var totalTokens sql.NullInt64
		statsQuery.Select("SUM(total_tokens)").Scan(&totalTokens)
		if totalTokens.Valid {
			globalTotalTokens = totalTokens.Int64
		}
		result.Stats.TotalTokens = globalTotalTokens
	}

	// Update stats to use completed requests count instead of total (excludes processing entries)
	result.Stats.TotalRequests = globalCompletedRequests

	return result, nil
}

// checkFTSSupport checks if FTS (Full Text Search) is available
// Note: For GORM with different databases, FTS support varies
func (p *LoggerPlugin) checkFTSSupport() bool {
	// For SQLite, check if FTS table exists
	var count int64
	err := p.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='logs_fts'").Scan(&count).Error
	if err != nil {
		// If we can't query sqlite_master, we're probably not using SQLite or FTS is not available
		return false
	}
	return count > 0
}

// createFTSTable creates a FTS table for enhanced search performance (SQLite specific)
func (p *LoggerPlugin) createFTSTable() error {
	// Create FTS virtual table for content search
	createFTSSQL := `
		CREATE VIRTUAL TABLE IF NOT EXISTS logs_fts USING fts5(
			content_summary,
			content='logs',
			content_rowid='id'
		)
	`

	if err := p.db.Exec(createFTSSQL).Error; err != nil {
		return fmt.Errorf("failed to create FTS table: %w", err)
	}

	// Create triggers to maintain FTS table
	triggerSQL := []string{
		`CREATE TRIGGER IF NOT EXISTS logs_fts_insert AFTER INSERT ON logs BEGIN
			INSERT INTO logs_fts(rowid, content_summary) VALUES (new.rowid, new.content_summary);
		END`,
		`CREATE TRIGGER IF NOT EXISTS logs_fts_delete AFTER DELETE ON logs BEGIN
			INSERT INTO logs_fts(logs_fts, rowid, content_summary) VALUES('delete', old.rowid, old.content_summary);
		END`,
		`CREATE TRIGGER IF NOT EXISTS logs_fts_update AFTER UPDATE ON logs BEGIN
			INSERT INTO logs_fts(logs_fts, rowid, content_summary) VALUES('delete', old.rowid, old.content_summary);
			INSERT INTO logs_fts(rowid, content_summary) VALUES (new.rowid, new.content_summary);
		END`,
	}

	for _, sql := range triggerSQL {
		if err := p.db.Exec(sql).Error; err != nil {
			return fmt.Errorf("failed to create FTS trigger: %w", err)
		}
	}

	return nil
}
