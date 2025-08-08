// Package logging provides streaming-related functionality for the GORM-based logging plugin
package logging

import (
	"context"
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// appendContentToMessage efficiently appends content to a message
func (p *LoggerPlugin) appendContentToMessage(message *schemas.BifrostMessage, newContent string) {
	if message == nil {
		return
	}
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
	if message == nil {
		return
	}
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

// Stream accumulator helper methods

// createStreamAccumulator creates a new stream accumulator for a request
func (p *LoggerPlugin) createStreamAccumulator(requestID string) *StreamAccumulator {
	accumulator := &StreamAccumulator{
		RequestID:  requestID,
		Chunks:     make([]*StreamChunk, 0),
		IsComplete: false,
		Object:     "",
	}

	p.streamAccumulators.Store(requestID, accumulator)
	return accumulator
}

// getOrCreateStreamAccumulator gets or creates a stream accumulator for a request
func (p *LoggerPlugin) getOrCreateStreamAccumulator(requestID string) *StreamAccumulator {
	if accumulator, exists := p.streamAccumulators.Load(requestID); exists {
		return accumulator.(*StreamAccumulator)
	}

	// Create new accumulator if it doesn't exist
	return p.createStreamAccumulator(requestID)
}

// addStreamChunk adds a chunk to the stream accumulator
func (p *LoggerPlugin) addStreamChunk(requestID string, chunk *StreamChunk, object string) error {
	accumulator := p.getOrCreateStreamAccumulator(requestID)

	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()

	// Store object type once (from first chunk)
	if accumulator.Object == "" && object != "" {
		accumulator.Object = object
	}

	// Add chunk to the list (chunks arrive in order)
	accumulator.Chunks = append(accumulator.Chunks, chunk)

	// Check if this is the final chunk
	// Set FinalTimestamp when either FinishReason is present or token usage exists
	// This handles both normal completion chunks and usage-only last chunks
	if chunk.FinishReason != nil || chunk.TokenUsage != nil {
		accumulator.IsComplete = true
		accumulator.FinalTimestamp = chunk.Timestamp
	}

	return nil
}

// processAccumulatedChunks processes all accumulated chunks in order
func (p *LoggerPlugin) processAccumulatedChunks(requestID string) error {
	accumulator := p.getOrCreateStreamAccumulator(requestID)

	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()

	// Ensure cleanup happens
	defer p.cleanupStreamAccumulator(requestID)

	// Build complete message from accumulated chunks
	completeMessage := p.buildCompleteMessageFromChunks(accumulator.Chunks)

	// Calculate final latency
	latency, err := p.calculateLatency(requestID, accumulator.FinalTimestamp, context.Background())
	if err != nil {
		p.logger.Error(fmt.Errorf("failed to calculate latency for request %s: %w", requestID, err))
		latency = 0
	}

	// Update database with complete message
	updates := make(map[string]interface{})
	updates["status"] = "success"
	updates["stream"] = true
	updates["latency"] = latency
	updates["timestamp"] = accumulator.FinalTimestamp

	// Serialize complete message
	tempEntry := &LogEntry{
		OutputMessageParsed: completeMessage,
	}
	if completeMessage.AssistantMessage != nil && completeMessage.AssistantMessage.ToolCalls != nil {
		tempEntry.ToolCallsParsed = completeMessage.AssistantMessage.ToolCalls
	}

	if err := tempEntry.serializeFields(); err != nil {
		return fmt.Errorf("failed to serialize complete message: %w", err)
	}

	updates["output_message"] = tempEntry.OutputMessage
	updates["content_summary"] = tempEntry.ContentSummary
	if tempEntry.ToolCalls != "" {
		updates["tool_calls"] = tempEntry.ToolCalls
	}

	// Update token usage from final chunk if available
	if len(accumulator.Chunks) > 0 {
		lastChunk := accumulator.Chunks[len(accumulator.Chunks)-1]
		if lastChunk.TokenUsage != nil {
			tempEntry.TokenUsageParsed = lastChunk.TokenUsage
			if err := tempEntry.serializeFields(); err == nil {
				updates["token_usage"] = tempEntry.TokenUsage
				updates["prompt_tokens"] = lastChunk.TokenUsage.PromptTokens
				updates["completion_tokens"] = lastChunk.TokenUsage.CompletionTokens
				updates["total_tokens"] = lastChunk.TokenUsage.TotalTokens
			}
		}
	}

	// Update object field from accumulator (stored once for the entire stream)
	if accumulator.Object != "" {
		updates["object_type"] = accumulator.Object
	}

	// Perform final database update
	if err := p.db.Model(&LogEntry{}).Where("id = ?", requestID).Updates(updates).Error; err != nil {
		return fmt.Errorf("failed to update log entry with complete stream: %w", err)
	}

	// Trigger callback
	p.mu.Lock()
	if p.logCallback != nil {
		if updatedEntry, getErr := p.getLogEntry(requestID); getErr == nil {
			p.logCallback(updatedEntry)
		}
	}
	p.mu.Unlock()

	return nil
}

// buildCompleteMessageFromChunks builds a complete message from ordered chunks
func (p *LoggerPlugin) buildCompleteMessageFromChunks(chunks []*StreamChunk) *schemas.BifrostMessage {
	completeMessage := &schemas.BifrostMessage{
		Role:    schemas.ModelChatMessageRoleAssistant,
		Content: schemas.MessageContent{},
	}

	for _, chunk := range chunks {
		if chunk.Delta == nil {
			continue
		}

		// Handle role (usually in first chunk)
		if chunk.Delta.Role != nil {
			completeMessage.Role = schemas.ModelChatMessageRole(*chunk.Delta.Role)
		}

		// Append content
		if chunk.Delta.Content != nil && *chunk.Delta.Content != "" {
			p.appendContentToMessage(completeMessage, *chunk.Delta.Content)
		}

		// Handle refusal
		if chunk.Delta.Refusal != nil && *chunk.Delta.Refusal != "" {
			if completeMessage.AssistantMessage == nil {
				completeMessage.AssistantMessage = &schemas.AssistantMessage{}
			}
			if completeMessage.AssistantMessage.Refusal == nil {
				completeMessage.AssistantMessage.Refusal = chunk.Delta.Refusal
			} else {
				*completeMessage.AssistantMessage.Refusal += *chunk.Delta.Refusal
			}
		}

		// Accumulate tool calls
		if len(chunk.Delta.ToolCalls) > 0 {
			p.accumulateToolCallsInMessage(completeMessage, chunk.Delta.ToolCalls)
		}
	}

	return completeMessage
}

// cleanupStreamAccumulator removes the stream accumulator for a request
func (p *LoggerPlugin) cleanupStreamAccumulator(requestID string) {
	if accumulator, exists := p.streamAccumulators.Load(requestID); exists {
		// Return all chunks to the pool before deleting
		acc := accumulator.(*StreamAccumulator)
		for _, chunk := range acc.Chunks {
			p.putStreamChunk(chunk)
		}
		p.streamAccumulators.Delete(requestID)
	}
}

// cleanupOldStreamAccumulators removes stream accumulators older than 5 minutes
func (p *LoggerPlugin) cleanupOldStreamAccumulators() {
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	cleanedCount := 0

	p.streamAccumulators.Range(func(key, value interface{}) bool {
		requestID := key.(string)
		accumulator := value.(*StreamAccumulator)
		accumulator.mu.Lock()
		defer accumulator.mu.Unlock()

		// Check if this accumulator is old (no activity for 5 minutes)
		// Use the timestamp of the first chunk as a reference
		if len(accumulator.Chunks) > 0 {
			firstChunkTime := accumulator.Chunks[0].Timestamp
			if firstChunkTime.Before(fiveMinutesAgo) {
				// Return all chunks to the pool
				for _, chunk := range accumulator.Chunks {
					p.putStreamChunk(chunk)
				}
				p.streamAccumulators.Delete(requestID)
				cleanedCount++
				p.logger.Debug(fmt.Sprintf("Cleaned up old stream accumulator for request %s", requestID))
			}
		}
		return true
	})

	if cleanedCount > 0 {
		p.logger.Debug(fmt.Sprintf("Cleaned up %d old stream accumulators", cleanedCount))
	}
}

// handleStreamingResponse handles streaming responses with ordered accumulation
func (p *LoggerPlugin) handleStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	requestID, ok := (*ctx).Value(ContextKey("request-id")).(string)
	if !ok || requestID == "" {
		p.logger.Error(fmt.Errorf("request-id not found in context or is empty"))
		return result, err, nil
	}

	// Create chunk from current response using pool
	chunk := p.getStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = err

	if err != nil {
		// Error case - mark as final chunk
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil {
		// Extract delta and other information
		if len(result.Choices) > 0 {
			choice := result.Choices[0]
			if choice.BifrostStreamResponseChoice != nil {
				// Create a deep copy of the Delta to avoid pointing to stack memory
				deltaCopy := choice.BifrostStreamResponseChoice.Delta
				chunk.Delta = &deltaCopy
				chunk.FinishReason = choice.FinishReason
			}
		}

		// Extract token usage
		if result.Usage != nil && result.Usage.TotalTokens > 0 {
			chunk.TokenUsage = result.Usage
		}
	}

	// Add chunk to accumulator synchronously to maintain order
	object := ""
	if result != nil {
		object = result.Object
	}
	if addErr := p.addStreamChunk(requestID, chunk, object); addErr != nil {
		p.logger.Error(fmt.Errorf("failed to add stream chunk for request %s: %w", requestID, addErr))
	}

	// If this is the final chunk, process accumulated chunks asynchronously
	if chunk.FinishReason != nil || chunk.TokenUsage != nil {
		go func() {
			if processErr := p.processAccumulatedChunks(requestID); processErr != nil {
				p.logger.Error(fmt.Errorf("failed to process accumulated chunks for request %s: %w", requestID, processErr))
			}
		}()
	}

	return result, err, nil
}

// isStreamingResponse checks if the response is a streaming delta
func (p *LoggerPlugin) isStreamingResponse(result *schemas.BifrostResponse) bool {
	if result == nil {
		return false
	}

	// Check for streaming choices (text-based streaming)
	if len(result.Choices) > 0 {
		for _, choice := range result.Choices {
			if choice.BifrostStreamResponseChoice != nil {
				return true
			}
		}
	}

	// Check for streaming speech output
	if result.Speech != nil && result.Speech.BifrostSpeechStreamResponse != nil {
		return true
	}

	// Check for streaming transcription output
	if result.Transcribe != nil && result.Transcribe.BifrostTranscribeStreamResponse != nil {
		return true
	}

	return false
}

// isTextStreamingResponse checks if the response is a text-based streaming delta
func (p *LoggerPlugin) isTextStreamingResponse(result *schemas.BifrostResponse) bool {
	if result == nil {
		return false
	}

	// Check for streaming choices (text-based streaming only)
	if len(result.Choices) > 0 {
		for _, choice := range result.Choices {
			if choice.BifrostStreamResponseChoice != nil {
				return true
			}
		}
	}

	return false
}
