package streaming

import (
	"context"
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// buildCompleteMessageFromTranscriptionStreamChunks builds a complete message from accumulated transcription chunks
func (a *Accumulator) buildCompleteMessageFromTranscriptionStreamChunks(chunks []*TranscriptionStreamChunk) *schemas.BifrostTranscribe {
	completeMessage := &schemas.BifrostTranscribe{}
	finalContent := ""
	for _, chunk := range chunks {
		if chunk.Delta == nil {
			continue
		}
		if chunk.Delta.Type != nil && *chunk.Delta.Type == "transcript.text.delta" && chunk.Delta.Delta != nil {
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
		Object:         "audio.transcription",
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
		data.Latency = float64(accumulator.FinalTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds()) / 1e6
	}
	data.EndTimestamp = accumulator.FinalTimestamp
	data.TranscriptionOutput = completeMessage
	data.ErrorDetails = bifrostErr
	// Update token usage from final chunk if available
	if len(accumulator.TranscriptionStreamChunks) > 0 {
		lastChunk := accumulator.TranscriptionStreamChunks[len(accumulator.TranscriptionStreamChunks)-1]
		if lastChunk.TranscriptionUsage != nil {
			data.TokenUsage = &schemas.LLMUsage{}
			if lastChunk.TranscriptionUsage.InputTokens != nil {
				data.TokenUsage.PromptTokens = *lastChunk.TranscriptionUsage.InputTokens
			}
			if lastChunk.TranscriptionUsage.OutputTokens != nil {
				data.TokenUsage.CompletionTokens = *lastChunk.TranscriptionUsage.OutputTokens
			}
			if lastChunk.TranscriptionUsage.TotalTokens != nil {
				data.TokenUsage.TotalTokens = *lastChunk.TranscriptionUsage.TotalTokens
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
	// Update object field from accumulator (stored once for the entire stream)
	if accumulator.Object != "" {
		data.Object = accumulator.Object
	}
	return data, nil
}

// processTranscriptionStreamingResponse processes a transcription streaming response
func (a *Accumulator) processTranscriptionStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("request-id not found in context or is empty")
	}
	_, provider, model := bifrost.GetRequestFields(result, bifrostErr)
	isFinalChunk := bifrost.IsFinalChunk(ctx)
	// For audio, all the data comes in the final chunk
	chunk := a.getTranscriptionStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr
	if bifrostErr != nil {
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil {
		if result.Transcribe != nil && result.Transcribe.BifrostTranscribeStreamResponse != nil {
			// We create a deep copy of the delta to avoid pointing to stack memory
			newDelta := &schemas.BifrostTranscribeStreamResponse{
				Type:  result.Transcribe.BifrostTranscribeStreamResponse.Type,
				Delta: result.Transcribe.BifrostTranscribeStreamResponse.Delta,
			}
			chunk.Delta = newDelta
			chunk.TranscriptionUsage = result.Transcribe.Usage
		}
		chunk.TokenUsage = result.Usage
	}
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
	if addErr := a.addTranscriptionStreamChunk(requestID, chunk, object, isFinalChunk); addErr != nil {
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
			return &ProcessedStreamResponse{
				Type:       StreamResponseTypeFinal,
				RequestID:  requestID,
				StreamType: StreamTypeTranscription,
				Provider:   provider,
				Model:      model,
				Data:       data,
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
