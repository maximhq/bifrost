package streaming

import (
	"context"
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// buildCompleteMessageFromAudioStreamChunks builds a complete message from accumulated audio chunks
func (a *Accumulator) buildCompleteMessageFromAudioStreamChunks(chunks []*AudioStreamChunk) *schemas.BifrostSpeechResponse {
	completeMessage := &schemas.BifrostSpeechResponse{}
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
	defer func() {
		accumulator.mu.Unlock()
		if isFinalChunk {
			// Before unlocking, we cleanup
			defer a.cleanupStreamAccumulator(requestID)
		}
	}()
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
	// Update token usage from final chunk if available
	if len(accumulator.AudioStreamChunks) > 0 {
		lastChunk := accumulator.AudioStreamChunks[len(accumulator.AudioStreamChunks)-1]
		if lastChunk.TokenUsage != nil {
			data.TokenUsage = &schemas.BifrostLLMUsage{
				PromptTokens:     lastChunk.TokenUsage.InputTokens,
				CompletionTokens: lastChunk.TokenUsage.OutputTokens,
				TotalTokens:      lastChunk.TokenUsage.TotalTokens,
			}
		}
	}
	// Update cost from final chunk if available
	if len(accumulator.AudioStreamChunks) > 0 {
		lastChunk := accumulator.AudioStreamChunks[len(accumulator.AudioStreamChunks)-1]
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
	}
	// Update semantic cache debug from final chunk if available
	if len(accumulator.AudioStreamChunks) > 0 {
		lastChunk := accumulator.AudioStreamChunks[len(accumulator.AudioStreamChunks)-1]
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
	}
	return data, nil
}

// processAudioStreamingResponse processes a audio streaming response
func (a *Accumulator) processAudioStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("request-id not found in context or is empty")
	}
	_, provider, model := bifrost.GetResponseFields(result, bifrostErr)
	isFinalChunk := bifrost.IsFinalChunk(ctx)
	// For audio, all the data comes in the final chunk
	chunk := a.getAudioStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr
	if bifrostErr != nil {
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil {
		if result.SpeechStreamResponse != nil {
			// We create a deep copy of the delta to avoid pointing to stack memory
			newDelta := &schemas.BifrostSpeechStreamResponse{
				Type:  result.SpeechStreamResponse.Type,
				Usage: result.SpeechStreamResponse.Usage,
				Audio: result.SpeechStreamResponse.Audio,
			}
			chunk.Delta = newDelta
			if result.SpeechStreamResponse.Usage != nil {
				chunk.TokenUsage = result.SpeechStreamResponse.Usage
			}
		}
	}
	if result != nil {
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	}
	if addErr := a.addAudioStreamChunk(requestID, chunk, isFinalChunk); addErr != nil {
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
			data, processErr := a.processAccumulatedAudioStreamingChunks(requestID, bifrostErr, isFinalChunk)
			if processErr != nil {
				a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
				return nil, processErr
			}
			return &ProcessedStreamResponse{
				Type:       StreamResponseTypeFinal,
				RequestID:  requestID,
				StreamType: StreamTypeAudio,
				Model:      model,
				Provider:   provider,
				Data:       data,
			}, nil
		}
		return nil, nil
	}
	data, processErr := a.processAccumulatedAudioStreamingChunks(requestID, bifrostErr, isFinalChunk)
	if processErr != nil {
		a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
		return nil, processErr
	}
	return &ProcessedStreamResponse{
		Type:       StreamResponseTypeDelta,
		RequestID:  requestID,
		StreamType: StreamTypeAudio,
		Model:      model,
		Provider:   provider,
		Data:       data,
	}, nil
}
