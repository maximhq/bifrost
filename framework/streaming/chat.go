package streaming

import (
	"fmt"
	"sort"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// deepCopyChatStreamDelta creates a deep copy of ChatStreamResponseChoiceDelta
// to prevent shared data mutation between different chunks
func deepCopyChatStreamDelta(original *schemas.ChatStreamResponseChoiceDelta) *schemas.ChatStreamResponseChoiceDelta {
	if original == nil {
		return nil
	}

	copy := &schemas.ChatStreamResponseChoiceDelta{}

	if original.Role != nil {
		copyRole := *original.Role
		copy.Role = &copyRole
	}

	if original.Content != nil {
		copyContent := *original.Content
		copy.Content = &copyContent
	}

	if original.Refusal != nil {
		copyRefusal := *original.Refusal
		copy.Refusal = &copyRefusal
	}

	if original.Reasoning != nil {
		copyReasoning := *original.Reasoning
		copy.Reasoning = &copyReasoning
	}

	// Deep copy ReasoningDetails slice
	if original.ReasoningDetails != nil {
		copy.ReasoningDetails = make([]schemas.ChatReasoningDetails, len(original.ReasoningDetails))
		for i, rd := range original.ReasoningDetails {
			copyRd := schemas.ChatReasoningDetails{
				Index: rd.Index,
				Type:  rd.Type,
			}
			if rd.ID != nil {
				copyID := *rd.ID
				copyRd.ID = &copyID
			}
			if rd.Text != nil {
				copyText := *rd.Text
				copyRd.Text = &copyText
			}
			if rd.Signature != nil {
				copySig := *rd.Signature
				copyRd.Signature = &copySig
			}
			if rd.Summary != nil {
				copySummary := *rd.Summary
				copyRd.Summary = &copySummary
			}
			if rd.Data != nil {
				copyData := *rd.Data
				copyRd.Data = &copyData
			}
			copy.ReasoningDetails[i] = copyRd
		}
	}

	// Deep copy ToolCalls slice
	if original.ToolCalls != nil {
		copy.ToolCalls = make([]schemas.ChatAssistantMessageToolCall, len(original.ToolCalls))
		for i, tc := range original.ToolCalls {
			copyTc := schemas.ChatAssistantMessageToolCall{
				Index:    tc.Index,
				Function: tc.Function, // struct value, safe to copy directly
			}
			if tc.ID != nil {
				copyID := *tc.ID
				copyTc.ID = &copyID
			}
			if tc.Type != nil {
				copyType := *tc.Type
				copyTc.Type = &copyType
			}
			// Deep copy Function's Name pointer
			if tc.Function.Name != nil {
				copyName := *tc.Function.Name
				copyTc.Function.Name = &copyName
			}
			copy.ToolCalls[i] = copyTc
		}
	}

	// Deep copy Audio
	if original.Audio != nil {
		copy.Audio = &schemas.ChatAudioMessageAudio{
			ID:         original.Audio.ID,
			Data:       original.Audio.Data,
			ExpiresAt:  original.Audio.ExpiresAt,
			Transcript: original.Audio.Transcript,
		}
	}

	return copy
}

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
		// Append content delta
		if chunk.Delta.Content != nil && *chunk.Delta.Content != "" {
			a.appendContentToMessage(completeMessage, *chunk.Delta.Content)
		}
		// Handle refusal delta
		if chunk.Delta.Refusal != nil && *chunk.Delta.Refusal != "" {
			if completeMessage.ChatAssistantMessage == nil {
				completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			if completeMessage.ChatAssistantMessage.Refusal == nil {
				// Deep copy on first assignment
				refusalCopy := *chunk.Delta.Refusal
				completeMessage.ChatAssistantMessage.Refusal = &refusalCopy
			} else {
				*completeMessage.ChatAssistantMessage.Refusal += *chunk.Delta.Refusal
			}
		}
		// Handle reasoning delta
		if chunk.Delta.Reasoning != nil && *chunk.Delta.Reasoning != "" {
			if completeMessage.ChatAssistantMessage == nil {
				completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			if completeMessage.ChatAssistantMessage.Reasoning == nil {
				// Deep copy on first assignment
				reasoningCopy := *chunk.Delta.Reasoning
				completeMessage.ChatAssistantMessage.Reasoning = &reasoningCopy
			} else {
				*completeMessage.ChatAssistantMessage.Reasoning += *chunk.Delta.Reasoning
			}
		}
		// Handle reasoning details delta
		if len(chunk.Delta.ReasoningDetails) > 0 {
			if completeMessage.ChatAssistantMessage == nil {
				completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			// Accumulate reasoning details by index
			for _, rd := range chunk.Delta.ReasoningDetails {
				found := false
				for i := range completeMessage.ChatAssistantMessage.ReasoningDetails {
					existingRd := &completeMessage.ChatAssistantMessage.ReasoningDetails[i]
					if existingRd.Index == rd.Index {
						// Found matching index - accumulate text delta
						if rd.Text != nil && *rd.Text != "" {
							if existingRd.Text == nil {
								// Deep copy on first assignment
								textCopy := *rd.Text
								existingRd.Text = &textCopy
							} else {
								*existingRd.Text += *rd.Text
							}
						}
						// Accumulate summary delta
						if rd.Summary != nil && *rd.Summary != "" {
							if existingRd.Summary == nil {
								summaryCopy := *rd.Summary
								existingRd.Summary = &summaryCopy
							} else {
								*existingRd.Summary += *rd.Summary
							}
						}
						// Accumulate data delta
						if rd.Data != nil && *rd.Data != "" {
							if existingRd.Data == nil {
								dataCopy := *rd.Data
								existingRd.Data = &dataCopy
							} else {
								*existingRd.Data += *rd.Data
							}
						}
						// Overwrite signature (typically sent once at the end)
						if rd.Signature != nil {
							sigCopy := *rd.Signature
							existingRd.Signature = &sigCopy
						}
						// Update type if present
						if rd.Type != "" {
							existingRd.Type = rd.Type
						}
						// Update ID if present
						if rd.ID != nil {
							idCopy := *rd.ID
							existingRd.ID = &idCopy
						}
						found = true
						break
					}
				}
				// If not found, add new entry with deep copied values
				if !found {
					newRd := schemas.ChatReasoningDetails{
						Index: rd.Index,
						Type:  rd.Type,
					}
					if rd.ID != nil {
						idCopy := *rd.ID
						newRd.ID = &idCopy
					}
					if rd.Text != nil {
						textCopy := *rd.Text
						newRd.Text = &textCopy
					}
					if rd.Signature != nil {
						sigCopy := *rd.Signature
						newRd.Signature = &sigCopy
					}
					if rd.Summary != nil {
						summaryCopy := *rd.Summary
						newRd.Summary = &summaryCopy
					}
					if rd.Data != nil {
						dataCopy := *rd.Data
						newRd.Data = &dataCopy
					}
					completeMessage.ChatAssistantMessage.ReasoningDetails = append(
						completeMessage.ChatAssistantMessage.ReasoningDetails, newRd)
				}
			}
		}
		// Handle audio data - accumulate audio data and transcript
		if chunk.Delta.Audio != nil {
			if completeMessage.ChatAssistantMessage == nil {
				completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			if completeMessage.ChatAssistantMessage.Audio == nil {
				// First chunk with audio - initialize with copies
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
	defer accumulator.mu.Unlock()
	// Note: Cleanup is handled by CleanupStreamAccumulator when refcount reaches 0
	// This is called from completeDeferredSpan after streaming ends

	// Calculate Time to First Token (TTFT) in milliseconds
	var ttft int64
	if !accumulator.StartTimestamp.IsZero() && !accumulator.FirstChunkTimestamp.IsZero() {
		ttft = accumulator.FirstChunkTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds() / 1e6
	}

	// Initialize accumulated data
	data := &AccumulatedData{
		RequestID:        requestID,
		Status:           "success",
		Stream:           true,
		StartTimestamp:   accumulator.StartTimestamp,
		EndTimestamp:     accumulator.FinalTimestamp,
		Latency:          0,
		TimeToFirstToken: ttft,
		OutputMessage:    nil,
		ToolCalls:        nil,
		ErrorDetails:     nil,
		TokenUsage:       nil,
		CacheDebug:       nil,
		Cost:             nil,
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
	// Update metadata from the chunk with highest index (contains TokenUsage, Cost, FinishReason)
	if lastChunk := accumulator.getLastChatChunkLocked(); lastChunk != nil {
		if lastChunk.TokenUsage != nil {
			data.TokenUsage = lastChunk.TokenUsage
		}
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
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
	// Extract accumulator ID from context
	requestID, ok := getAccumulatorID(ctx)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("accumulator-id not found in context or is empty")
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
				selectedKeyID, virtualKeyID := modelcatalog.PricingScopeIDsFromContext(ctx)
				cost := a.pricingManager.CalculateCostWithCacheDebugWithScopes(result, selectedKeyID, virtualKeyID)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	} else if result != nil && result.ChatResponse != nil {
		// Extract delta and other information
		if len(result.ChatResponse.Choices) > 0 {
			choice := result.ChatResponse.Choices[0]
			if choice.ChatStreamResponseChoice != nil {
				// Deep copy delta to prevent shared data mutation between chunks
				chunk.Delta = deepCopyChatStreamDelta(choice.ChatStreamResponseChoice.Delta)
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
				selectedKeyID, virtualKeyID := modelcatalog.PricingScopeIDsFromContext(ctx)
				cost := a.pricingManager.CalculateCostWithCacheDebugWithScopes(result, selectedKeyID, virtualKeyID)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	}
	if addErr := a.addChatStreamChunk(requestID, chunk, isFinalChunk); addErr != nil {
		return nil, fmt.Errorf("failed to add stream chunk for request %s: %w", requestID, addErr)
	}
	// If this is the final chunk, process accumulated chunks
	// Always return data on final chunk - multiple plugins may need the result
	if isFinalChunk {
		// Get the accumulator and mark as complete (idempotent)
		accumulator := a.getOrCreateStreamAccumulator(requestID)
		accumulator.mu.Lock()
		if !accumulator.IsComplete {
			accumulator.IsComplete = true
		}
		accumulator.mu.Unlock()

		// Always process and return data on final chunk
		// Multiple plugins can call this - the processing is idempotent
		data, processErr := a.processAccumulatedChatStreamingChunks(requestID, bifrostErr, isFinalChunk)
		if processErr != nil {
			a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
			return nil, processErr
		}
		var rawRequest interface{}
		if result != nil && result.ChatResponse != nil && result.ChatResponse.ExtraFields.RawRequest != nil {
			rawRequest = result.ChatResponse.ExtraFields.RawRequest
		} else if result != nil && result.TextCompletionResponse != nil && result.TextCompletionResponse.ExtraFields.RawRequest != nil {
			rawRequest = result.TextCompletionResponse.ExtraFields.RawRequest
		}
		return &ProcessedStreamResponse{
			RequestID:  requestID,
			StreamType: streamType,
			Provider:   provider,
			Model:      model,
			Data:       data,
			RawRequest: &rawRequest,
		}, nil
	}
	// This is going to be a delta response
	data, processErr := a.processAccumulatedChatStreamingChunks(requestID, bifrostErr, isFinalChunk)
	if processErr != nil {
		a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
		return nil, processErr
	}
	// This is not the final chunk, so we will send back the delta
	return &ProcessedStreamResponse{
		RequestID:  requestID,
		StreamType: streamType,
		Provider:   provider,
		Model:      model,
		Data:       data,
	}, nil
}
