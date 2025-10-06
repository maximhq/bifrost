package streaming

import (
	"context"
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// buildCompleteMessageFromChunks builds a complete message from accumulated chunks
func (a *Accumulator) buildCompleteMessageFromChatStreamChunks(chunks []*ChatStreamChunk) *schemas.ChatMessage {
	completeMessage := &schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: schemas.ChatMessageContent{},
	}
	for _, chunk := range chunks {
		if chunk.Delta == nil {
			continue
		}
		// Handle role (usually in first chunk)
		if chunk.Delta.Role != nil {
			completeMessage.Role = schemas.ChatMessageRole(*chunk.Delta.Role)
		}
		// Append content
		if chunk.Delta.Content != nil && *chunk.Delta.Content != "" {
			a.appendContentToMessage(completeMessage, *chunk.Delta.Content)
		}
		// Handle refusal
		if chunk.Delta.Refusal != nil && *chunk.Delta.Refusal != "" {
			if completeMessage.ChatAssistantMessage == nil {
				completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			if completeMessage.ChatAssistantMessage.Refusal == nil {
				completeMessage.ChatAssistantMessage.Refusal = chunk.Delta.Refusal
			} else {
				*completeMessage.ChatAssistantMessage.Refusal += *chunk.Delta.Refusal
			}
		}
		// Accumulate tool calls
		if len(chunk.Delta.ToolCalls) > 0 {
			a.accumulateToolCallsInMessage(completeMessage, chunk.Delta.ToolCalls)
		}
	}
	return completeMessage
}

// processAccumulatedChunks processes all accumulated chunks in order
func (a *Accumulator) processAccumulatedChatStreamingChunks(requestID string, respErr *schemas.BifrostError, isFinalChunk bool) (*AccumulatedData, error) {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer func() {
		accumulator.mu.Unlock()
		if isFinalChunk {
			// Before unlocking, we cleanup
			defer a.cleanupStreamAccumulator(requestID)
		}
	}()
	// Initialize accumulated data
	data := &AccumulatedData{
		RequestID:      requestID,
		Status:         "success",
		Stream:         true,
		StartTimestamp: accumulator.StartTimestamp,
		EndTimestamp:   accumulator.FinalTimestamp,
		Latency:        0,
		OutputMessage:  nil,
		ToolCalls:      nil,
		ErrorDetails:   nil,
		TokenUsage:     nil,
		CacheDebug:     nil,
		Cost:           nil,
		Object:         "",
	}
	// Build complete message from accumulated chunks
	completeMessage := a.buildCompleteMessageFromChatStreamChunks(accumulator.ChatStreamChunks)
	if !isFinalChunk {
		data.OutputMessage = completeMessage
		return data, nil
	}
	// Update database with complete message
	data.Status = "success"
	if respErr != nil {
		data.Status = "error"
	}
	if accumulator.StartTimestamp.IsZero() || accumulator.FinalTimestamp.IsZero() {
		data.Latency = 0
	} else {
		data.Latency = float64(accumulator.FinalTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds()) / 1e6
	}
	data.EndTimestamp = accumulator.FinalTimestamp
	data.OutputMessage = completeMessage
	if data.OutputMessage.ChatAssistantMessage != nil && data.OutputMessage.ChatAssistantMessage.ToolCalls != nil {
		data.ToolCalls = data.OutputMessage.ChatAssistantMessage.ToolCalls
	}
	data.ErrorDetails = respErr
	// Update token usage from final chunk if available
	if len(accumulator.ChatStreamChunks) > 0 {
		lastChunk := accumulator.ChatStreamChunks[len(accumulator.ChatStreamChunks)-1]
		if lastChunk.TokenUsage != nil {
			data.TokenUsage = lastChunk.TokenUsage
		}
		// Handle cache debug
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
	}
	// Update cost from final chunk if available
	if len(accumulator.ChatStreamChunks) > 0 {
		lastChunk := accumulator.ChatStreamChunks[len(accumulator.ChatStreamChunks)-1]
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
		data.FinishReason = lastChunk.FinishReason
	}
	// Update object field from accumulator (stored once for the entire stream)
	if accumulator.Object != "" {
		data.Object = accumulator.Object
	}	
	return data, nil
}

// processChatStreamingResponse processes a chat streaming response
func (a *Accumulator) processChatStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	a.logger.Debug("[streaming] processing chat streaming response")
	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("request-id not found in context or is empty")
	}
	_, provider, model := bifrost.GetRequestFields(result, bifrostErr)
	isFinalChunk := bifrost.IsFinalChunk(ctx)
	chunk := a.getChatStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr
	if bifrostErr != nil {
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
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.ExtraFields.CacheDebug
		}
		object = result.Object
	}
	if addErr := a.addChatStreamChunk(requestID, chunk, object, isFinalChunk); addErr != nil {
		return nil, fmt.Errorf("failed to add stream chunk for request %s: %w", requestID, addErr)
	}
	// If this is the final chunk, process accumulated chunks asynchronously
	// Use the IsComplete flag to prevent duplicate processing
	if isFinalChunk {
		shouldProcess := false
		// Get the accumulator to check if processing has already been triggered
		accumulator := a.getOrCreateStreamAccumulator(requestID)
		accumulator.mu.Lock()
		shouldProcess = !accumulator.IsComplete
		// Mark as complete when we're about to process
		if shouldProcess {
			accumulator.IsComplete = true
		}
		accumulator.mu.Unlock()
		if shouldProcess {
			data, processErr := a.processAccumulatedChatStreamingChunks(requestID, bifrostErr, isFinalChunk)
			if processErr != nil {
				a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
				return nil, processErr
			}
			return &ProcessedStreamResponse{
				Type:       StreamResponseTypeFinal,
				RequestID:  requestID,
				StreamType: StreamTypeChat,
				Provider:   provider,
				Model:      model,
				Data:       data,
			}, nil
		}
		return nil, nil
	}
	// This is going to be a delta response
	data, processErr := a.processAccumulatedChatStreamingChunks(requestID, bifrostErr, isFinalChunk)
	if processErr != nil {
		a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
		return nil, processErr
	}
	// This is not the final chunk, so we will send back the delta
	return &ProcessedStreamResponse{
		Type:       StreamResponseTypeDelta,
		RequestID:  requestID,
		StreamType: StreamTypeChat,
		Provider:   provider,
		Model:      model,
		Data:       data,
	}, nil
}
