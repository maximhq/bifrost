package streaming

import (
	"fmt"
	"sort"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// buildCompleteMessageFromAudioStreamChunks builds a complete message from accumulated audio chunks
func (a *Accumulator) buildCompleteMessageFromAudioStreamChunks(chunks []*AudioStreamChunk) *schemas.BifrostSpeechResponse {
	completeMessage := &schemas.BifrostSpeechResponse{}
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})
	for _, chunk := range chunks {
		if chunk.Delta != nil {
			completeMessage.Audio = append(completeMessage.Audio, chunk.Delta.Audio...)
		}
	}
	return completeMessage
}

// processAccumulatedAudioStreamingChunks processes all accumulated audio chunks in order
func (a *Accumulator) processAccumulatedAudioStreamingChunks(requestID string, bifrostErr *schemas.BifrostError, isFinalChunk bool) (*AccumulatedData, error) {
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
	completeMessage := a.buildCompleteMessageFromAudioStreamChunks(accumulator.AudioStreamChunks)
	if !isFinalChunk {
		data.AudioOutput = completeMessage
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
	data.AudioOutput = completeMessage
	data.ErrorDetails = bifrostErr
	// Update metadata from the chunk with highest index (contains TokenUsage, Cost, CacheDebug)
	if lastChunk := accumulator.getLastAudioChunkLocked(); lastChunk != nil {
		if lastChunk.TokenUsage != nil {
			data.TokenUsage = &schemas.BifrostLLMUsage{
				PromptTokens:     lastChunk.TokenUsage.InputTokens,
				CompletionTokens: lastChunk.TokenUsage.OutputTokens,
				TotalTokens:      lastChunk.TokenUsage.TotalTokens,
			}
		}
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
	}
	// Accumulate raw response
	if len(accumulator.AudioStreamChunks) > 0 {
		// Sort chunks by chunk index
		sort.Slice(accumulator.AudioStreamChunks, func(i, j int) bool {
			return accumulator.AudioStreamChunks[i].ChunkIndex < accumulator.AudioStreamChunks[j].ChunkIndex
		})
		for _, chunk := range accumulator.AudioStreamChunks {
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

// processAudioStreamingResponse processes a audio streaming response
func (a *Accumulator) processAudioStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Extract accumulator ID from context
	requestID, ok := getAccumulatorID(ctx)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("accumulator-id not found in context or is empty")
	}
	_, provider, model := bifrost.GetResponseFields(result, bifrostErr)
	isFinalChunk := bifrost.IsFinalChunk(ctx)
	// For audio, all the data comes in the final chunk
	chunk := a.getAudioStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr
	if bifrostErr != nil {
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil && result.SpeechStreamResponse != nil {
		// We create a deep copy of the delta to avoid pointing to stack memory
		newDelta := &schemas.BifrostSpeechStreamResponse{
			Type:  result.SpeechStreamResponse.Type,
			Usage: result.SpeechStreamResponse.Usage,
			Audio: result.SpeechStreamResponse.Audio,
		}
		chunk.Delta = newDelta
		if result.SpeechStreamResponse.ExtraFields.RawResponse != nil {
			chunk.RawResponse = bifrost.Ptr(fmt.Sprintf("%v", result.SpeechStreamResponse.ExtraFields.RawResponse))
		}
		if result.SpeechStreamResponse.Usage != nil {
			chunk.TokenUsage = result.SpeechStreamResponse.Usage
		}
		chunk.ChunkIndex = result.SpeechStreamResponse.ExtraFields.ChunkIndex
		if isFinalChunk {
			if a.pricingManager != nil {
				selectedKeyID, virtualKeyID := modelcatalog.PricingScopeIDsFromContext(ctx)
				cost := a.pricingManager.CalculateCostWithCacheDebugWithScopes(result, selectedKeyID, virtualKeyID)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	}
	if addErr := a.addAudioStreamChunk(requestID, chunk, isFinalChunk); addErr != nil {
		return nil, fmt.Errorf("failed to add stream chunk for request %s: %w", requestID, addErr)
	}
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
		data, processErr := a.processAccumulatedAudioStreamingChunks(requestID, bifrostErr, isFinalChunk)
		if processErr != nil {
			a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
			return nil, processErr
		}
		var rawRequest interface{}
		if result != nil && result.SpeechStreamResponse != nil && result.SpeechStreamResponse.ExtraFields.RawRequest != nil {
			rawRequest = result.SpeechStreamResponse.ExtraFields.RawRequest
		}
		return &ProcessedStreamResponse{
			RequestID:  requestID,
			StreamType: StreamTypeAudio,
			Model:      model,
			Provider:   provider,
			Data:       data,
			RawRequest: &rawRequest,
		}, nil
	}
	data, processErr := a.processAccumulatedAudioStreamingChunks(requestID, bifrostErr, isFinalChunk)
	if processErr != nil {
		a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
		return nil, processErr
	}
	return &ProcessedStreamResponse{
		RequestID:  requestID,
		StreamType: StreamTypeAudio,
		Model:      model,
		Provider:   provider,
		Data:       data,
	}, nil
}
