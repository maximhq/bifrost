package streaming

import (
	"fmt"
	"sort"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// buildCompleteMessageFromTranscriptionStreamChunks builds a complete message from accumulated transcription chunks
func (a *Accumulator) buildCompleteMessageFromTranscriptionStreamChunks(chunks []*TranscriptionStreamChunk) *schemas.BifrostTranscriptionResponse {
	completeMessage := &schemas.BifrostTranscriptionResponse{}
	finalContent := ""
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})
	for _, chunk := range chunks {
		if chunk.Delta == nil {
			continue
		}
		if chunk.Delta.Type == schemas.TranscriptionStreamResponseTypeDelta && chunk.Delta.Delta != nil {
			finalContent += *chunk.Delta.Delta
		}
	}
	// Add final content to the message
	completeMessage.Text = finalContent
	return completeMessage
}

// processAccumulatedTranscriptionStreamingChunks processes all accumulated transcription chunks in order
func (a *Accumulator) processAccumulatedTranscriptionStreamingChunks(requestID string, bifrostErr *schemas.BifrostError, isFinalChunk bool) (*AccumulatedData, error) {
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

	// Calculate Time to First Token (TTFT) in milliseconds
	var ttft int64
	if !accumulator.StartTimestamp.IsZero() && !accumulator.FirstChunkTimestamp.IsZero() {
		ttft = accumulator.FirstChunkTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds() / 1e6
	}

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
	completeMessage := a.buildCompleteMessageFromTranscriptionStreamChunks(accumulator.TranscriptionStreamChunks)
	if !isFinalChunk {
		data.TranscriptionOutput = completeMessage
		return data, nil
	}
	data.Status = "success"
	if bifrostErr != nil {
		data.Status = "error"
	}
	if accumulator.StartTimestamp.IsZero() || accumulator.FinalTimestamp.IsZero() {
		data.Latency = 0
	} else {
		data.Latency = accumulator.FinalTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds() / 1e6
	}
	data.EndTimestamp = accumulator.FinalTimestamp
	data.TranscriptionOutput = completeMessage
	data.ErrorDetails = bifrostErr
	// Update token usage from final chunk if available
	if len(accumulator.TranscriptionStreamChunks) > 0 {
		lastChunk := accumulator.TranscriptionStreamChunks[len(accumulator.TranscriptionStreamChunks)-1]
		if lastChunk.TokenUsage != nil {
			data.TokenUsage = &schemas.BifrostLLMUsage{}
			if lastChunk.TokenUsage.InputTokens != nil {
				data.TokenUsage.PromptTokens = *lastChunk.TokenUsage.InputTokens
			}
			if lastChunk.TokenUsage.OutputTokens != nil {
				data.TokenUsage.CompletionTokens = *lastChunk.TokenUsage.OutputTokens
			}
			if lastChunk.TokenUsage.TotalTokens != nil {
				data.TokenUsage.TotalTokens = *lastChunk.TokenUsage.TotalTokens
			}
		}
	}
	// Update cost from final chunk if available
	if len(accumulator.TranscriptionStreamChunks) > 0 {
		lastChunk := accumulator.TranscriptionStreamChunks[len(accumulator.TranscriptionStreamChunks)-1]
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
	}
	// Update semantic cache debug from final chunk if available
	if len(accumulator.TranscriptionStreamChunks) > 0 {
		lastChunk := accumulator.TranscriptionStreamChunks[len(accumulator.TranscriptionStreamChunks)-1]
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
	}
	// Accumulate raw response
	if len(accumulator.TranscriptionStreamChunks) > 0 {
		// Sort chunks by chunk index
		sort.Slice(accumulator.TranscriptionStreamChunks, func(i, j int) bool {
			return accumulator.TranscriptionStreamChunks[i].ChunkIndex < accumulator.TranscriptionStreamChunks[j].ChunkIndex
		})
		for _, chunk := range accumulator.TranscriptionStreamChunks {
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

// processTranscriptionStreamingResponse processes a transcription streaming response
func (a *Accumulator) processTranscriptionStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("request-id not found in context or is empty")
	}
	_, provider, model := bifrost.GetResponseFields(result, bifrostErr)
	isFinalChunk := bifrost.IsFinalChunk(ctx)
	// For audio, all the data comes in the final chunk
	chunk := a.getTranscriptionStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr
	if bifrostErr != nil {
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil && result.TranscriptionStreamResponse != nil {
		// Set delta for all chunks (not just final chunks with usage)
		// We create a deep copy of the delta to avoid pointing to stack memory
		var deltaCopy *string
		if result.TranscriptionStreamResponse.Delta != nil {
			deltaValue := *result.TranscriptionStreamResponse.Delta
			deltaCopy = &deltaValue
		}
		newDelta := &schemas.BifrostTranscriptionStreamResponse{
			Type:  result.TranscriptionStreamResponse.Type,
			Delta: deltaCopy,
		}
		chunk.Delta = newDelta

		// Set token usage if available (typically only in final chunk)
		if result.TranscriptionStreamResponse.Usage != nil {
			chunk.TokenUsage = result.TranscriptionStreamResponse.Usage
		}
		chunk.ChunkIndex = result.TranscriptionStreamResponse.ExtraFields.ChunkIndex
		if result.TranscriptionStreamResponse.ExtraFields.RawResponse != nil {
			chunk.RawResponse = bifrost.Ptr(fmt.Sprintf("%v", result.TranscriptionStreamResponse.ExtraFields.RawResponse))
		}
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	}
	if addErr := a.addTranscriptionStreamChunk(requestID, chunk, isFinalChunk); addErr != nil {
		return nil, fmt.Errorf("failed to add stream chunk for request %s: %w", requestID, addErr)
	}
	if isFinalChunk {
		shouldProcess := false
		accumulator := a.getOrCreateStreamAccumulator(requestID)
		accumulator.mu.Lock()
		shouldProcess = !accumulator.IsComplete
		if shouldProcess {
			accumulator.IsComplete = true
		}
		accumulator.mu.Unlock()
		if shouldProcess {
			data, processErr := a.processAccumulatedTranscriptionStreamingChunks(requestID, bifrostErr, isFinalChunk)
			if processErr != nil {
				a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
				return nil, processErr
			}
			var rawRequest interface{}
			if result != nil && result.TranscriptionStreamResponse != nil && result.TranscriptionStreamResponse.ExtraFields.RawRequest != nil {
				rawRequest = result.TranscriptionStreamResponse.ExtraFields.RawRequest
			}
			return &ProcessedStreamResponse{
				Type:       StreamResponseTypeFinal,
				RequestID:  requestID,
				StreamType: StreamTypeTranscription,
				Provider:   provider,
				Model:      model,
				Data:       data,
				RawRequest: &rawRequest,
			}, nil
		}
		return nil, nil
	}
	data, processErr := a.processAccumulatedTranscriptionStreamingChunks(requestID, bifrostErr, isFinalChunk)
	if processErr != nil {
		a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
		return nil, processErr
	}
	return &ProcessedStreamResponse{
		Type:       StreamResponseTypeDelta,
		RequestID:  requestID,
		StreamType: StreamTypeTranscription,
		Provider:   provider,
		Model:      model,
		Data:       data,
	}, nil
}
