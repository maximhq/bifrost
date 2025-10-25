package maxim

import (
	"context"
	"sort"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// appendContentToMessage efficiently appends content to a message
func (p *Plugin) appendContentToMessage(message *schemas.BifrostMessage, newContent string) {
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
func (p *Plugin) accumulateToolCallsInMessage(message *schemas.BifrostMessage, deltaToolCalls []schemas.ToolCall) {
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
func (p *Plugin) createStreamAccumulator(requestID string) *StreamAccumulator {
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
func (p *Plugin) getOrCreateStreamAccumulator(requestID string) *StreamAccumulator {
	if accumulator, exists := p.streamAccumulators.Load(requestID); exists {
		return accumulator.(*StreamAccumulator)
	}

	// Create new accumulator if it doesn't exist
	return p.createStreamAccumulator(requestID)
}

// addStreamChunk adds a chunk to the stream accumulator
func (p *Plugin) addStreamChunk(requestID string, chunk *StreamChunk, object string, isFinalChunk bool) error {
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
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}

	return nil
}

// buildCompleteMessageFromChunks builds a complete message from ordered chunks
func (p *Plugin) buildCompleteMessageFromChunks(chunks []*StreamChunk) *schemas.BifrostMessage {
	completeMessage := &schemas.BifrostMessage{
		Role:    schemas.ModelChatMessageRoleAssistant,
		Content: schemas.MessageContent{},
	}

	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})

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
func (p *Plugin) cleanupStreamAccumulator(requestID string) {
	if _, exists := p.streamAccumulators.Load(requestID); exists {
		p.streamAccumulators.Delete(requestID)
	}
}

// cleanupOldStreamAccumulators removes stream accumulators older than 5 minutes
func (p *Plugin) cleanupOldStreamAccumulators() {
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
				p.streamAccumulators.Delete(requestID)
				cleanedCount++
				p.logger.Debug("cleaned up old stream accumulator for request %s")
			}
		}
		return true
	})

	if cleanedCount > 0 {
		p.logger.Debug("cleaned up %d old stream accumulators", cleanedCount)
	}
}

// handleStreamingResponse handles streaming responses with ordered accumulation
func (p *Plugin) handleStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	requestID, ok := (*ctx).Value(schemas.BifrostContextKey("request-id")).(string)
	if !ok || requestID == "" {
		p.logger.Error("request-id not found in context or is empty")
		return result, err, nil
	}

	// Create chunk from current response using pool
	chunk := &StreamChunk{}
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = err
	chunk.ChunkIndex = result.ExtraFields.ChunkIndex

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

	isFinalChunk := bifrost.IsFinalChunk(ctx)

	// Add chunk to accumulator synchronously to maintain order
	object := ""
	if result != nil {
		object = result.Object
	}
	if addErr := p.addStreamChunk(requestID, chunk, object, isFinalChunk); addErr != nil {
		p.logger.Error("failed to add stream chunk for request %s: %v", requestID, addErr)
	}

	return result, err, nil
}
