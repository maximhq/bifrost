package streaming

import (
	"fmt"
	"sort"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// buildCompleteMessageFromChunks builds a complete message from accumulated chunks
func (a *Accumulator) buildCompleteMessageFromChatStreamChunks(chunks []*ChatStreamChunk) *schemas.ChatMessage {
	completeMessage := &schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{},
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
				completeMessage.ChatAssistantMessage.Refusal = bifrost.Ptr(*chunk.Delta.Refusal)
			} else {
				*completeMessage.ChatAssistantMessage.Refusal += *chunk.Delta.Refusal
			}
		}
		// Handle reasoning
		if chunk.Delta.Reasoning != nil && *chunk.Delta.Reasoning != "" {
			if completeMessage.ChatAssistantMessage == nil {
				completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			if completeMessage.ChatAssistantMessage.Reasoning == nil {
				completeMessage.ChatAssistantMessage.Reasoning = bifrost.Ptr(*chunk.Delta.Reasoning)
			} else {
				*completeMessage.ChatAssistantMessage.Reasoning += *chunk.Delta.Reasoning
			}
		}
		// Handle reasoning details
		if len(chunk.Delta.ReasoningDetails) > 0 {
			if completeMessage.ChatAssistantMessage == nil {
				completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			// Check if the reasoning detail already exists on that index, if so, update it else add it to the list
			for _, reasoningDetail := range chunk.Delta.ReasoningDetails {
				found := false
				for i := range completeMessage.ChatAssistantMessage.ReasoningDetails {
					existingReasoningDetail := &completeMessage.ChatAssistantMessage.ReasoningDetails[i]
					if existingReasoningDetail.Index == reasoningDetail.Index {
						// Update text - accumulate if both exist
						if reasoningDetail.Text != nil {
							if existingReasoningDetail.Text == nil {
								existingReasoningDetail.Text = reasoningDetail.Text
							} else {
								*existingReasoningDetail.Text += *reasoningDetail.Text
							}
						}
						// Update signature - overwrite (signatures are typically final)
						if reasoningDetail.Signature != nil {
							existingReasoningDetail.Signature = reasoningDetail.Signature
						}
						// Update other fields if present
						if reasoningDetail.Summary != nil {
							if existingReasoningDetail.Summary == nil {
								existingReasoningDetail.Summary = reasoningDetail.Summary
							} else {
								*existingReasoningDetail.Summary += *reasoningDetail.Summary
							}
						}
						if reasoningDetail.Data != nil {
							if existingReasoningDetail.Data == nil {
								existingReasoningDetail.Data = reasoningDetail.Data
							} else {
								*existingReasoningDetail.Data += *reasoningDetail.Data
							}
						}
						if reasoningDetail.Type != "" {
							existingReasoningDetail.Type = reasoningDetail.Type
						}
						found = true
						break
					}
				}
				// If not found, add it to the list
				if !found {
					completeMessage.ChatAssistantMessage.ReasoningDetails = append(completeMessage.ChatAssistantMessage.ReasoningDetails, reasoningDetail)
				}
			}
		}
		// Handle audio data - accumulate audio data and transcript
		if chunk.Delta.Audio != nil {
			if completeMessage.ChatAssistantMessage == nil {
				completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			if completeMessage.ChatAssistantMessage.Audio == nil {
				// First chunk with audio - initialize
				completeMessage.ChatAssistantMessage.Audio = &schemas.ChatAudioMessageAudio{
					ID:         chunk.Delta.Audio.ID,
					Data:       chunk.Delta.Audio.Data,
					ExpiresAt:  chunk.Delta.Audio.ExpiresAt,
					Transcript: chunk.Delta.Audio.Transcript,
				}
			} else {
				// Subsequent chunks - accumulate data and transcript
				if chunk.Delta.Audio.Data != "" {
					completeMessage.ChatAssistantMessage.Audio.Data += chunk.Delta.Audio.Data
				}
				if chunk.Delta.Audio.Transcript != "" {
					completeMessage.ChatAssistantMessage.Audio.Transcript += chunk.Delta.Audio.Transcript
				}
				// Update ID and ExpiresAt if present (they should be consistent or final)
				if chunk.Delta.Audio.ID != "" {
					completeMessage.ChatAssistantMessage.Audio.ID = chunk.Delta.Audio.ID
				}
				if chunk.Delta.Audio.ExpiresAt != 0 {
					completeMessage.ChatAssistantMessage.Audio.ExpiresAt = chunk.Delta.Audio.ExpiresAt
				}
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
		if isFinalChunk {
			// Cleanup BEFORE unlocking to prevent other goroutines from accessing chunks being returned to pool
			a.cleanupStreamAccumulator(requestID)
		}
		accumulator.mu.Unlock()
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
		data.Latency = accumulator.FinalTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds() / 1e6
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
	// Accumulate raw response
	if len(accumulator.ChatStreamChunks) > 0 {
		// Sort chunks by chunk index
		sort.Slice(accumulator.ChatStreamChunks, func(i, j int) bool {
			return accumulator.ChatStreamChunks[i].ChunkIndex < accumulator.ChatStreamChunks[j].ChunkIndex
		})
		for _, chunk := range accumulator.ChatStreamChunks {
			if chunk.RawResponse != nil {
				if data.RawResponse == nil {
					data.RawResponse = bifrost.Ptr(*chunk.RawResponse)
				} else {
					*data.RawResponse += "\n\n" + *chunk.RawResponse
				}
			}
		}
	}
	return data, nil
}

// processChatStreamingResponse processes a chat streaming response
func (a *Accumulator) processChatStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	a.logger.Debug("[streaming] processing chat streaming response")
	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("request-id not found in context or is empty")
	}
	requestType, provider, model := bifrost.GetResponseFields(result, bifrostErr)

	streamType := StreamTypeChat
	if requestType == schemas.TextCompletionStreamRequest {
		streamType = StreamTypeText
	}

	isFinalChunk := bifrost.IsFinalChunk(ctx)
	chunk := a.getChatStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr
	if bifrostErr != nil {
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil && result.TextCompletionResponse != nil {
		// Handle text completion response directly
		if len(result.TextCompletionResponse.Choices) > 0 {
			choice := result.TextCompletionResponse.Choices[0]

			if choice.TextCompletionResponseChoice != nil {
				deltaCopy := choice.TextCompletionResponseChoice.Text
				chunk.Delta = &schemas.ChatStreamResponseChoiceDelta{
					Content: deltaCopy,
				}
				chunk.FinishReason = choice.FinishReason
			}
		}
		// Extract token usage
		if result.TextCompletionResponse.Usage != nil && result.TextCompletionResponse.Usage.TotalTokens > 0 {
			chunk.TokenUsage = result.TextCompletionResponse.Usage
		}
		chunk.ChunkIndex = result.TextCompletionResponse.ExtraFields.ChunkIndex
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	} else if result != nil && result.ChatResponse != nil {
		// Extract delta and other information
		if len(result.ChatResponse.Choices) > 0 {
			choice := result.ChatResponse.Choices[0]
			if choice.ChatStreamResponseChoice != nil {
				// Shallow-copy struct and deep-copy slices to avoid aliasing
				copied := choice.ChatStreamResponseChoice.Delta
				chunk.Delta = copied
				chunk.FinishReason = choice.FinishReason
			}
		}
		// Extract token usage
		if result.ChatResponse.Usage != nil && result.ChatResponse.Usage.TotalTokens > 0 {
			chunk.TokenUsage = result.ChatResponse.Usage
		}
		chunk.ChunkIndex = result.ChatResponse.ExtraFields.ChunkIndex
		if result.ChatResponse.ExtraFields.RawResponse != nil {
			chunk.RawResponse = bifrost.Ptr(fmt.Sprintf("%v", result.ChatResponse.ExtraFields.RawResponse))
		}
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	}
	if addErr := a.addChatStreamChunk(requestID, chunk, isFinalChunk); addErr != nil {
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
			var rawRequest interface{}
			if result != nil && result.ChatResponse != nil && result.ChatResponse.ExtraFields.RawRequest != nil {
				rawRequest = result.ChatResponse.ExtraFields.RawRequest
			}
			return &ProcessedStreamResponse{
				Type:       StreamResponseTypeFinal,
				RequestID:  requestID,
				StreamType: streamType,
				Provider:   provider,
				Model:      model,
				Data:       data,
				RawRequest: &rawRequest,
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
		StreamType: streamType,
		Provider:   provider,
		Model:      model,
		Data:       data,
	}, nil
}
