package logging

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// insertInitialLogEntry stores an initial log entry in SQLite using the new async data structure
func (p *LoggerPlugin) insertInitialLogEntry(requestID string, timestamp time.Time, data *InitialLogData) error {
	// Serialize complex fields to JSON
	inputHistoryJSON, _ := json.Marshal(data.InputHistory)
	toolsJSON, _ := json.Marshal(data.Tools)
	paramsJSON, _ := json.Marshal(data.Params)
	speechInputJSON, _ := json.Marshal(data.SpeechInput)
	transcriptionInputJSON, _ := json.Marshal(data.TranscriptionInput)

	// Create content summary for searching
	contentSummary := p.createContentSummaryFromInitialData(data)

	// Insert into main table
	query := `
	INSERT INTO logs (
		id, provider, model, object_type, status,
		input_history, tools, params, speech_input, transcription_input,
		content_summary, stream, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := p.db.Exec(query,
		requestID, data.Provider, data.Model,
		data.Object, "processing",
		string(inputHistoryJSON), string(toolsJSON), string(paramsJSON),
		string(speechInputJSON), string(transcriptionInputJSON),
		contentSummary, false, timestamp.UnixNano())

	if err != nil {
		return fmt.Errorf("failed to insert initial log entry: %w", err)
	}

	return nil
}

// updateLogEntry updates an existing log entry with new data using the new async data structure
func (p *LoggerPlugin) updateLogEntry(requestID string, timestamp time.Time, data *UpdateLogData) error {
	// First, get the created_at timestamp to calculate latency
	var createdAtUnix int64
	err := p.db.QueryRow("SELECT created_at FROM logs WHERE id = ?", requestID).Scan(&createdAtUnix)
	if err != nil {
		return fmt.Errorf("failed to get created_at for latency calculation: %w", err)
	}

	createdAt := time.Unix(createdAtUnix/1e9, createdAtUnix%1e9) // Convert from nanoseconds
	latency := float64(timestamp.Sub(createdAt).Milliseconds())

	// Build dynamic UPDATE query
	var setParts []string
	var args []interface{}

	// Update request timestamp
	setParts = append(setParts, "timestamp = ?")
	args = append(args, timestamp.UnixNano())

	// Always update latency
	setParts = append(setParts, "latency = ?")
	args = append(args, latency)

	// Update status
	if data.Status != "" {
		setParts = append(setParts, "status = ?")
		args = append(args, data.Status)
	}

	// Update model if provided
	if data.Model != "" {
		setParts = append(setParts, "model = ?")
		args = append(args, data.Model)
	}

	// Update object type if provided
	if data.Object != "" {
		setParts = append(setParts, "object_type = ?")
		args = append(args, data.Object)
	}

	// Update token usage
	if data.TokenUsage != nil {
		setParts = append(setParts, "prompt_tokens = ?, completion_tokens = ?, total_tokens = ?")
		args = append(args, data.TokenUsage.PromptTokens, data.TokenUsage.CompletionTokens, data.TokenUsage.TotalTokens)
	}

	// Update output message
	if data.OutputMessage != nil {
		outputMessageJSON, _ := json.Marshal(data.OutputMessage)
		setParts = append(setParts, "output_message = ?")
		args = append(args, string(outputMessageJSON))
	}

	// Update tool calls
	if data.ToolCalls != nil {
		toolCallsJSON, _ := json.Marshal(data.ToolCalls)
		setParts = append(setParts, "tool_calls = ?")
		args = append(args, string(toolCallsJSON))
	}

	// Update error details
	if data.ErrorDetails != nil {
		errorDetailsJSON, _ := json.Marshal(data.ErrorDetails)
		setParts = append(setParts, "error_details = ?")
		args = append(args, string(errorDetailsJSON))
	}

	// Update speech output (for non-streaming)
	if data.SpeechOutput != nil {
		speechOutputJSON, _ := json.Marshal(data.SpeechOutput)
		setParts = append(setParts, "speech_output = ?")
		args = append(args, string(speechOutputJSON))
	}

	// Update transcription output (for non-streaming)
	if data.TranscriptionOutput != nil {
		transcriptionOutputJSON, _ := json.Marshal(data.TranscriptionOutput)
		setParts = append(setParts, "transcription_output = ?")
		args = append(args, string(transcriptionOutputJSON))
	}

	// Add the WHERE clause parameter
	args = append(args, requestID)

	query := fmt.Sprintf("UPDATE logs SET %s WHERE id = ?", strings.Join(setParts, ", "))

	// Update content summary if we have new content
	if data.OutputMessage != nil {
		// Get current log entry to rebuild content summary
		if currentEntry, err := p.getLogEntry(requestID); err == nil {
			newContentSummary := p.createContentSummary(currentEntry)
			query = strings.Replace(query, "WHERE id = ?", ", content_summary = ? WHERE id = ?", 1)
			// Insert content_summary before the requestID in args
			args = append(args[:len(args)-1], newContentSummary, args[len(args)-1])
		}
	}

	_, err = p.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("failed to update log entry: %w", err)
	}

	return nil
}

// processStreamUpdate handles streaming delta updates efficiently with minimal database operations
func (p *LoggerPlugin) processStreamUpdate(requestID string, timestamp time.Time, data *StreamUpdateData, isFinalChunk bool) error {
	// Handle error case first
	if data.ErrorDetails != nil {
		// For errors, we should also calculate latency
		// Get created_at timestamp to calculate latency
		var createdAtUnix int64
		err := p.db.QueryRow("SELECT created_at FROM logs WHERE id = ?", requestID).Scan(&createdAtUnix)
		if err != nil {
			// If we can't get created_at, just update status and error
			query := "UPDATE logs SET status = ?, error_details = ? WHERE id = ?"
			errorDetailsJSON, _ := json.Marshal(data.ErrorDetails)
			_, execErr := p.db.Exec(query, "error", string(errorDetailsJSON), requestID)
			return execErr
		}

		createdAt := time.Unix(createdAtUnix/1e9, createdAtUnix%1e9)
		latency := float64(timestamp.Sub(createdAt).Milliseconds())

		query := "UPDATE logs SET status = ?, error_details = ?, latency = ?, timestamp = ? WHERE id = ?"
		errorDetailsJSON, _ := json.Marshal(data.ErrorDetails)
		_, err = p.db.Exec(query, "error", string(errorDetailsJSON), latency, timestamp.UnixNano(), requestID)
		return err
	}

	// For streaming updates, we need to calculate latency when the stream finishes
	var needsLatency bool
	var latency float64

	if isFinalChunk {
		// Stream is finishing, calculate latency
		var createdAtUnix int64
		err := p.db.QueryRow("SELECT created_at FROM logs WHERE id = ?", requestID).Scan(&createdAtUnix)
		if err != nil {
			return fmt.Errorf("failed to get created_at for latency calculation: %w", err)
		}

		createdAt := time.Unix(createdAtUnix/1e9, createdAtUnix%1e9)
		latency = float64(timestamp.Sub(createdAt).Milliseconds())
		needsLatency = true
	}

	// Build dynamic UPDATE query for streaming data
	var setParts []string
	var args []interface{}

	// Always mark as streaming and update timestamp
	setParts = append(setParts, "stream = ?, timestamp = ?")
	args = append(args, true, timestamp.UnixNano())

	// Add latency if this is the final chunk
	if needsLatency {
		setParts = append(setParts, "latency = ?")
		args = append(args, latency)
	}

	// Update model if provided
	if data.Model != "" {
		setParts = append(setParts, "model = ?")
		args = append(args, data.Model)
	}

	// Update object type if provided
	if data.Object != "" {
		setParts = append(setParts, "object_type = ?")
		args = append(args, data.Object)
	}

	// Update token usage if provided
	if data.TokenUsage != nil {
		setParts = append(setParts, "prompt_tokens = ?, completion_tokens = ?, total_tokens = ?")
		args = append(args, data.TokenUsage.PromptTokens, data.TokenUsage.CompletionTokens, data.TokenUsage.TotalTokens)
	}

	// Handle finish reason - if present, mark as complete
	if isFinalChunk {
		setParts = append(setParts, "status = ?")
		args = append(args, "success")
	}

	// Process delta content and tool calls if present
	if data.Delta != nil {
		if err := p.appendDeltaToEntry(requestID, data.Delta, &setParts, &args); err != nil {
			return fmt.Errorf("failed to append delta: %w", err)
		}
	}

	// Handle transcription output from stream updates
	if data.TranscriptionOutput != nil {
		transcriptionOutputJSON, err := json.Marshal(data.TranscriptionOutput)
		if err != nil {
			return fmt.Errorf("failed to marshal transcription output: %w", err)
		}

		setParts = append(setParts, "transcription_output = ?")
		args = append(args, string(transcriptionOutputJSON))
	}

	// Only perform update if there's something to update
	if len(setParts) > 0 {
		// Add WHERE clause parameter
		args = append(args, requestID)

		query := fmt.Sprintf("UPDATE logs SET %s WHERE id = ?", strings.Join(setParts, ", "))
		_, err := p.db.Exec(query, args...)
		if err != nil {
			return fmt.Errorf("failed to update streaming log entry: %w", err)
		}
	}

	return nil
}

// appendDeltaToEntry efficiently appends streaming delta content to existing database entry
func (p *LoggerPlugin) appendDeltaToEntry(requestID string, delta *schemas.BifrostStreamDelta, setParts *[]string, args *[]interface{}) error {
	// Only fetch existing content if we have content or tool calls to append
	if (delta.Content == nil || *delta.Content == "") && len(delta.ToolCalls) == 0 && delta.Refusal == nil {
		return nil
	}

	// Get only the necessary fields from existing entry
	var outputMessageJSON sql.NullString
	err := p.db.QueryRow("SELECT output_message FROM logs WHERE id = ?", requestID).Scan(&outputMessageJSON)
	if err != nil {
		return fmt.Errorf("failed to get existing output message: %w", err)
	}

	// Parse existing message or create new one
	var outputMessage *schemas.BifrostMessage
	if outputMessageJSON.Valid && outputMessageJSON.String != "null" && outputMessageJSON.String != "" {
		outputMessage = &schemas.BifrostMessage{}
		if err := json.Unmarshal([]byte(outputMessageJSON.String), outputMessage); err != nil {
			// If unmarshaling fails, create new message
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

	// Update the database fields
	updatedMessageJSON, _ := json.Marshal(outputMessage)
	*setParts = append(*setParts, "output_message = ?")
	*args = append(*args, string(updatedMessageJSON))

	// Also update tool_calls field for backward compatibility
	if outputMessage.AssistantMessage != nil && outputMessage.AssistantMessage.ToolCalls != nil {
		toolCallsJSON, _ := json.Marshal(outputMessage.AssistantMessage.ToolCalls)
		*setParts = append(*setParts, "tool_calls = ?")
		*args = append(*args, string(toolCallsJSON))
	}

	return nil
}

// appendContentToMessage efficiently appends content to a message
func (p *LoggerPlugin) appendContentToMessage(message *schemas.BifrostMessage, newContent string) {
	if message.Content.ContentStr != nil {
		// Append to existing string content
		*message.Content.ContentStr += newContent
	} else if message.Content.ContentBlocks != nil {
		// Find the last text block and append, or create new one
		blocks := *message.Content.ContentBlocks
		if len(blocks) > 0 && blocks[len(blocks)-1].Type == schemas.ContentBlockTypeText && blocks[len(blocks)-1].Text != nil {
			// Append to last text block
			*blocks[len(blocks)-1].Text += newContent
		} else {
			// Create new text block
			blocks = append(blocks, schemas.ContentBlock{
				Type: schemas.ContentBlockTypeText,
				Text: &newContent,
			})
			message.Content.ContentBlocks = &blocks
		}
	} else {
		// Initialize with string content
		message.Content.ContentStr = &newContent
	}
}

// accumulateToolCallsInMessage efficiently accumulates tool calls in a message
func (p *LoggerPlugin) accumulateToolCallsInMessage(message *schemas.BifrostMessage, deltaToolCalls []schemas.ToolCall) {
	if message.AssistantMessage == nil {
		message.AssistantMessage = &schemas.AssistantMessage{}
	}

	if message.AssistantMessage.ToolCalls == nil {
		message.AssistantMessage.ToolCalls = &[]schemas.ToolCall{}
	}

	existingToolCalls := *message.AssistantMessage.ToolCalls

	for _, deltaToolCall := range deltaToolCalls {
		// Find existing tool call with same ID or create new one
		found := false
		for i := range existingToolCalls {
			if existingToolCalls[i].ID != nil && deltaToolCall.ID != nil &&
				*existingToolCalls[i].ID == *deltaToolCall.ID {
				// Append arguments to existing tool call
				existingToolCalls[i].Function.Arguments += deltaToolCall.Function.Arguments
				found = true
				break
			}
		}

		if !found {
			// Add new tool call
			existingToolCalls = append(existingToolCalls, deltaToolCall)
		}
	}

	message.AssistantMessage.ToolCalls = &existingToolCalls
}

// createContentSummaryFromInitialData creates a searchable content summary from initial log data
func (p *LoggerPlugin) createContentSummaryFromInitialData(data *InitialLogData) string {
	var parts []string

	// Add input history content
	for _, msg := range data.InputHistory {
		if msg.Content.ContentStr != nil {
			parts = append(parts, *msg.Content.ContentStr)
		}
	}

	// Add speech input content
	if data.SpeechInput != nil && data.SpeechInput.Input != "" {
		parts = append(parts, data.SpeechInput.Input)
		if data.SpeechInput.Instructions != "" {
			parts = append(parts, data.SpeechInput.Instructions)
		}
	}

	// Add transcription input prompt if available
	if data.TranscriptionInput != nil && data.TranscriptionInput.Prompt != nil && *data.TranscriptionInput.Prompt != "" {
		parts = append(parts, *data.TranscriptionInput.Prompt)
	}

	return strings.Join(parts, " ")
}

// getLogEntry retrieves a complete log entry by ID
func (p *LoggerPlugin) getLogEntry(requestID string) (*LogEntry, error) {
	query := `
	SELECT id, timestamp, provider, model, object_type, status, latency,
		   prompt_tokens, completion_tokens, total_tokens,
		   input_history, output_message, tools, tool_calls,
		   params, error_details, speech_input, transcription_input,
		   speech_output, transcription_output, stream, created_at
	FROM logs WHERE id = ?`

	var entry LogEntry
	var timestampUnix, createdAtUnix int64
	var inputHistoryJSON, outputMessageJSON, toolsJSON, toolCallsJSON sql.NullString
	var paramsJSON, errorDetailsJSON sql.NullString
	var speechInputJSON, transcriptionInputJSON sql.NullString
	var speechOutputJSON, transcriptionOutputJSON sql.NullString
	var promptTokens, completionTokens, totalTokensRow sql.NullInt64
	var latency sql.NullFloat64
	var stream sql.NullBool

	err := p.db.QueryRow(query, requestID).Scan(
		&entry.ID, &timestampUnix, &entry.Provider, &entry.Model,
		&entry.Object, &entry.Status, &latency,
		&promptTokens, &completionTokens, &totalTokensRow,
		&inputHistoryJSON, &outputMessageJSON, &toolsJSON, &toolCallsJSON,
		&paramsJSON, &errorDetailsJSON, &speechInputJSON, &transcriptionInputJSON,
		&speechOutputJSON, &transcriptionOutputJSON, &stream,
		&createdAtUnix,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get log entry: %w", err)
	}

	// Convert timestamps
	entry.Timestamp = time.Unix(timestampUnix/1e9, timestampUnix%1e9) // Convert from nanoseconds
	entry.CreatedAt = time.Unix(createdAtUnix/1e9, createdAtUnix%1e9) // Convert from nanoseconds

	// Handle latency
	if latency.Valid {
		entry.Latency = &latency.Float64
	}

	// Handle stream flag
	if stream.Valid {
		entry.Stream = stream.Bool
	}

	// Handle token usage
	if promptTokens.Valid || completionTokens.Valid || totalTokensRow.Valid {
		entry.TokenUsage = &schemas.LLMUsage{}
		if promptTokens.Valid {
			entry.TokenUsage.PromptTokens = int(promptTokens.Int64)
		}
		if completionTokens.Valid {
			entry.TokenUsage.CompletionTokens = int(completionTokens.Int64)
		}
		if totalTokensRow.Valid {
			entry.TokenUsage.TotalTokens = int(totalTokensRow.Int64)
		}
	}

	// Deserialize JSON fields with NULL checks
	if inputHistoryJSON.Valid {
		json.Unmarshal([]byte(inputHistoryJSON.String), &entry.InputHistory)
	}
	if outputMessageJSON.Valid {
		json.Unmarshal([]byte(outputMessageJSON.String), &entry.OutputMessage)
	}
	if toolsJSON.Valid {
		json.Unmarshal([]byte(toolsJSON.String), &entry.Tools)
	}
	if toolCallsJSON.Valid {
		json.Unmarshal([]byte(toolCallsJSON.String), &entry.ToolCalls)
	}

	if paramsJSON.Valid {
		json.Unmarshal([]byte(paramsJSON.String), &entry.Params)
	}
	if errorDetailsJSON.Valid {
		json.Unmarshal([]byte(errorDetailsJSON.String), &entry.ErrorDetails)
	}
	if speechInputJSON.Valid {
		json.Unmarshal([]byte(speechInputJSON.String), &entry.SpeechInput)
	}
	if transcriptionInputJSON.Valid {
		json.Unmarshal([]byte(transcriptionInputJSON.String), &entry.TranscriptionInput)
	}
	if speechOutputJSON.Valid {
		json.Unmarshal([]byte(speechOutputJSON.String), &entry.SpeechOutput)
	}
	if transcriptionOutputJSON.Valid {
		json.Unmarshal([]byte(transcriptionOutputJSON.String), &entry.TranscriptionOutput)
	}

	return &entry, nil
}

// createContentSummary creates a searchable content summary from the log entry
func (p *LoggerPlugin) createContentSummary(entry *LogEntry) string {
	var parts []string

	// Add input history content
	for _, msg := range entry.InputHistory {
		if msg.Content.ContentStr != nil {
			parts = append(parts, *msg.Content.ContentStr)
		}
	}

	// Add output message content
	if entry.OutputMessage != nil && entry.OutputMessage.Content.ContentStr != nil {
		parts = append(parts, *entry.OutputMessage.Content.ContentStr)
	}

	// Add tool calls content
	if entry.ToolCalls != nil {
		for _, toolCall := range *entry.ToolCalls {
			if toolCall.Function.Arguments != "" {
				parts = append(parts, toolCall.Function.Arguments)
			}
		}
	}

	// Add speech input content
	if entry.SpeechInput != nil && entry.SpeechInput.Input != "" {
		parts = append(parts, entry.SpeechInput.Input)
		if entry.SpeechInput.Instructions != "" {
			parts = append(parts, entry.SpeechInput.Instructions)
		}
	}

	// Add transcription input prompt if available
	if entry.TranscriptionInput != nil && entry.TranscriptionInput.Prompt != nil && *entry.TranscriptionInput.Prompt != "" {
		parts = append(parts, *entry.TranscriptionInput.Prompt)
	}

	// Add transcription output text
	if entry.TranscriptionOutput != nil && entry.TranscriptionOutput.Text != "" {
		parts = append(parts, entry.TranscriptionOutput.Text)
	}

	// Add error details
	if entry.ErrorDetails != nil {
		parts = append(parts, entry.ErrorDetails.Error.Message)
	}

	return strings.Join(parts, " ")
}

// SearchLogs searches for log entries based on filters and pagination
func (p *LoggerPlugin) SearchLogs(filters *SearchFilters, pagination *PaginationOptions) (*SearchResult, error) {
	if pagination == nil {
		pagination = &PaginationOptions{
			Limit:  50,
			Offset: 0,
			SortBy: "timestamp",
			Order:  "desc",
		}
	}

	// Build the SQL query
	query, countQuery, args := p.buildSearchQuery(filters, pagination)

	// Get total count and global statistics (exclude LIMIT and OFFSET args)
	filterArgs := args[:len(args)-2]

	var totalCount int64
	err := p.db.QueryRow(countQuery, filterArgs...).Scan(&totalCount)
	if err != nil {
		return nil, fmt.Errorf("failed to get total count: %w", err)
	}

	// Calculate global statistics only from completed requests (exclude processing status)
	var globalAverageLatency float64
	var globalTotalTokens int64
	var globalSuccessfulRequests int64
	var globalCompletedRequests int64

	if totalCount > 0 {
		// Build statistics query with same filters but no pagination, excluding processing entries
		statsQuery := strings.Replace(countQuery, "COUNT(*)",
			"AVG(latency) as avg_latency, SUM(total_tokens) as total_tokens, COUNT(CASE WHEN status = 'success' THEN 1 END) as successful_requests, COUNT(CASE WHEN status IN ('success', 'error') THEN 1 END) as completed_requests", 1)

		var avgLatency sql.NullFloat64
		var totalTokens sql.NullInt64
		var successfulRequests sql.NullInt64
		var completedRequests sql.NullInt64

		err = p.db.QueryRow(statsQuery, filterArgs...).Scan(&avgLatency, &totalTokens, &successfulRequests, &completedRequests)
		if err != nil {
			return nil, fmt.Errorf("failed to get global statistics: %w", err)
		}

		if avgLatency.Valid {
			globalAverageLatency = avgLatency.Float64
		}
		if totalTokens.Valid {
			globalTotalTokens = totalTokens.Int64
		}
		if successfulRequests.Valid {
			globalSuccessfulRequests = successfulRequests.Int64
		}
		if completedRequests.Valid {
			globalCompletedRequests = completedRequests.Int64
		}
	}

	// Execute main query
	rows, err := p.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute search query: %w", err)
	}
	defer rows.Close()

	var logs []LogEntry

	for rows.Next() {
		var entry LogEntry
		var timestampUnix sql.NullInt64
		var inputHistoryJSON, outputMessageJSON, toolsJSON, toolCallsJSON sql.NullString
		var paramsJSON, errorDetailsJSON sql.NullString
		var speechInputJSON, transcriptionInputJSON sql.NullString
		var speechOutputJSON, transcriptionOutputJSON sql.NullString
		var promptTokens, completionTokens, totalTokensRow sql.NullInt64
		var latency sql.NullFloat64

		err := rows.Scan(
			&entry.ID, &timestampUnix, &entry.Provider, &entry.Model,
			&entry.Object, &entry.Status, &latency,
			&promptTokens, &completionTokens, &totalTokensRow,
			&inputHistoryJSON, &outputMessageJSON, &toolsJSON, &toolCallsJSON,
			&paramsJSON, &errorDetailsJSON, &speechInputJSON, &transcriptionInputJSON,
			&speechOutputJSON, &transcriptionOutputJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// Convert timestamp (handle NULL values)
		if timestampUnix.Valid {
			entry.Timestamp = time.Unix(timestampUnix.Int64/1e9, timestampUnix.Int64%1e9) // Convert from nanoseconds
		} else {
			entry.Timestamp = time.Time{} // Set to zero time if NULL
		}

		// Handle latency
		if latency.Valid {
			entry.Latency = &latency.Float64
		}

		// Handle token usage
		if promptTokens.Valid || completionTokens.Valid || totalTokensRow.Valid {
			entry.TokenUsage = &schemas.LLMUsage{}
			if promptTokens.Valid {
				entry.TokenUsage.PromptTokens = int(promptTokens.Int64)
			}
			if completionTokens.Valid {
				entry.TokenUsage.CompletionTokens = int(completionTokens.Int64)
			}
			if totalTokensRow.Valid {
				entry.TokenUsage.TotalTokens = int(totalTokensRow.Int64)
			}
		}

		// Deserialize JSON fields with NULL checks
		if inputHistoryJSON.Valid {
			json.Unmarshal([]byte(inputHistoryJSON.String), &entry.InputHistory)
		}
		if outputMessageJSON.Valid {
			json.Unmarshal([]byte(outputMessageJSON.String), &entry.OutputMessage)
		}
		if toolsJSON.Valid {
			json.Unmarshal([]byte(toolsJSON.String), &entry.Tools)
		}
		if toolCallsJSON.Valid {
			json.Unmarshal([]byte(toolCallsJSON.String), &entry.ToolCalls)
		}

		if paramsJSON.Valid {
			json.Unmarshal([]byte(paramsJSON.String), &entry.Params)
		}
		if errorDetailsJSON.Valid {
			json.Unmarshal([]byte(errorDetailsJSON.String), &entry.ErrorDetails)
		}
		if speechInputJSON.Valid {
			json.Unmarshal([]byte(speechInputJSON.String), &entry.SpeechInput)
		}
		if transcriptionInputJSON.Valid {
			json.Unmarshal([]byte(transcriptionInputJSON.String), &entry.TranscriptionInput)
		}
		if speechOutputJSON.Valid {
			json.Unmarshal([]byte(speechOutputJSON.String), &entry.SpeechOutput)
		}
		if transcriptionOutputJSON.Valid {
			json.Unmarshal([]byte(transcriptionOutputJSON.String), &entry.TranscriptionOutput)
		}

		logs = append(logs, entry)
	}

	// Calculate global success rate based on completed requests only
	var successRate float64
	if globalCompletedRequests > 0 {
		successRate = float64(globalSuccessfulRequests) / float64(globalCompletedRequests) * 100
	}

	return &SearchResult{
		Logs:       logs,
		Pagination: *pagination,
		Stats: struct {
			TotalRequests  int64   `json:"total_requests"`
			SuccessRate    float64 `json:"success_rate"`
			AverageLatency float64 `json:"average_latency"`
			TotalTokens    int64   `json:"total_tokens"`
		}{
			TotalRequests:  globalCompletedRequests, // Use completed requests count
			SuccessRate:    successRate,
			AverageLatency: globalAverageLatency,
			TotalTokens:    globalTotalTokens,
		},
	}, nil
}

// buildSearchQuery constructs the SQL query based on filters and pagination
func (p *LoggerPlugin) buildSearchQuery(filters *SearchFilters, pagination *PaginationOptions) (string, string, []interface{}) {
	var whereClauses []string
	var args []interface{}

	baseQuery := `
	SELECT id, timestamp, provider, model, object_type, status, latency,
		   prompt_tokens, completion_tokens, total_tokens,
		   input_history, output_message, tools, tool_calls,
		   params, error_details, speech_input, transcription_input,
		   speech_output, transcription_output
	FROM logs`

	countQuery := "SELECT COUNT(*) FROM logs"

	// Build WHERE clauses
	if filters != nil {
		// Provider filter
		if len(filters.Providers) > 0 {
			placeholders := make([]string, len(filters.Providers))
			for i, provider := range filters.Providers {
				placeholders[i] = "?"
				args = append(args, provider)
			}
			whereClauses = append(whereClauses, fmt.Sprintf("provider IN (%s)", strings.Join(placeholders, ",")))
		}

		// Model filter
		if len(filters.Models) > 0 {
			placeholders := make([]string, len(filters.Models))
			for i, model := range filters.Models {
				placeholders[i] = "?"
				args = append(args, model)
			}
			whereClauses = append(whereClauses, fmt.Sprintf("model IN (%s)", strings.Join(placeholders, ",")))
		}

		// Status filter
		if len(filters.Status) > 0 {
			placeholders := make([]string, len(filters.Status))
			for i, status := range filters.Status {
				placeholders[i] = "?"
				args = append(args, status)
			}
			whereClauses = append(whereClauses, fmt.Sprintf("status IN (%s)", strings.Join(placeholders, ",")))
		}

		// Object type filter
		if len(filters.Objects) > 0 {
			placeholders := make([]string, len(filters.Objects))
			for i, object := range filters.Objects {
				placeholders[i] = "?"
				args = append(args, object)
			}
			whereClauses = append(whereClauses, fmt.Sprintf("object_type IN (%s)", strings.Join(placeholders, ",")))
		}

		// Time range filters
		if filters.StartTime != nil {
			whereClauses = append(whereClauses, "timestamp >= ?")
			args = append(args, filters.StartTime.UnixNano())
		}
		if filters.EndTime != nil {
			whereClauses = append(whereClauses, "timestamp <= ?")
			args = append(args, filters.EndTime.UnixNano())
		}

		// Latency range filters
		if filters.MinLatency != nil {
			whereClauses = append(whereClauses, "latency >= ?")
			args = append(args, *filters.MinLatency)
		}
		if filters.MaxLatency != nil {
			whereClauses = append(whereClauses, "latency <= ?")
			args = append(args, *filters.MaxLatency)
		}

		// Token range filters
		if filters.MinTokens != nil {
			whereClauses = append(whereClauses, "total_tokens >= ?")
			args = append(args, *filters.MinTokens)
		}
		if filters.MaxTokens != nil {
			whereClauses = append(whereClauses, "total_tokens <= ?")
			args = append(args, *filters.MaxTokens)
		}

		// Content search
		if filters.ContentSearch != "" {
			if p.checkFTSTableExists() {
				// Use FTS if available and table exists
				whereClauses = append(whereClauses, "id IN (SELECT id FROM logs_fts WHERE content_summary MATCH ?)")
				args = append(args, filters.ContentSearch)
			} else {
				// Fallback to LIKE search
				whereClauses = append(whereClauses, "content_summary LIKE ?")
				args = append(args, "%"+filters.ContentSearch+"%")
			}
		}
	}

	// Add WHERE clause to queries
	if len(whereClauses) > 0 {
		whereClause := " WHERE " + strings.Join(whereClauses, " AND ")
		baseQuery += whereClause
		countQuery += whereClause
	}

	// Add ORDER BY
	orderBy := " ORDER BY "
	switch pagination.SortBy {
	case "latency":
		orderBy += "latency"
	case "tokens":
		orderBy += "total_tokens"
	default:
		orderBy += "timestamp"
	}

	if pagination.Order == "asc" {
		orderBy += " ASC"
	} else {
		orderBy += " DESC"
	}

	baseQuery += orderBy

	// Add LIMIT and OFFSET
	baseQuery += " LIMIT ? OFFSET ?"
	args = append(args, pagination.Limit, pagination.Offset)

	return baseQuery, countQuery, args
}

// checkFTSTableExists verifies if the FTS table exists and is accessible
func (p *LoggerPlugin) checkFTSTableExists() bool {
	var count int
	err := p.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='logs_fts'").Scan(&count)
	return err == nil && count > 0
}

// determineObjectType determines the object type from the input
func (p *LoggerPlugin) determineObjectType(input schemas.RequestInput) string {
	if input.ChatCompletionInput != nil {
		return "chat.completion"
	} else if input.TextCompletionInput != nil {
		return "text.completion"
	} else if input.EmbeddingInput != nil {
		return "embedding"
	} else if input.SpeechInput != nil {
		return "audio.speech"
	} else if input.TranscriptionInput != nil {
		return "audio.transcription"
	}
	return "unknown"
}

// extractInputHistory extracts input history from the request
func (p *LoggerPlugin) extractInputHistory(input schemas.RequestInput) []schemas.BifrostMessage {
	var inputHistory []schemas.BifrostMessage

	if input.ChatCompletionInput != nil {
		// ChatCompletionInput is *[]BifrostMessage, so we dereference it
		inputHistory = *input.ChatCompletionInput
	} else if input.TextCompletionInput != nil {
		// TextCompletionInput is *string, so we dereference it
		if *input.TextCompletionInput != "" {
			inputHistory = []schemas.BifrostMessage{
				{
					Role: schemas.ModelChatMessageRoleUser,
					Content: schemas.MessageContent{
						ContentStr: input.TextCompletionInput,
					},
				},
			}
		}
	} else if input.EmbeddingInput != nil {
		// EmbeddingInput has Texts field
		for _, text := range input.EmbeddingInput.Texts {
			inputHistory = append(inputHistory, schemas.BifrostMessage{
				Role: schemas.ModelChatMessageRoleUser,
				Content: schemas.MessageContent{
					ContentStr: &text,
				},
			})
		}
	}

	return inputHistory
}

// LogManager defines the main interface that combines all logging functionality
type LogManager interface {
	// Search searches for log entries based on filters and pagination
	Search(filters *SearchFilters, pagination *PaginationOptions) (*SearchResult, error)

	// Get the number of dropped requests
	GetDroppedRequests() int64
}

type PluginLogManager struct {
	plugin *LoggerPlugin
}

func (p *PluginLogManager) Search(filters *SearchFilters, pagination *PaginationOptions) (*SearchResult, error) {
	return p.plugin.SearchLogs(filters, pagination)
}

func (p *PluginLogManager) GetDroppedRequests() int64 {
	return p.plugin.droppedRequests.Load()
}

func (p *LoggerPlugin) GetPluginLogManager() *PluginLogManager {
	return &PluginLogManager{
		plugin: p,
	}
}
