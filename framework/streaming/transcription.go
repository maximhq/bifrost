package streaming

import (
	"fmt"
	"sort"
	"strings"
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
	// Update metadata from the chunk with highest index (contains TokenUsage, Cost, CacheDebug)
	if lastChunk := accumulator.getLastTranscriptionChunkLocked(); lastChunk != nil {
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
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
	}
	// Accumulate raw response using strings.Builder to avoid O(n^2) string concatenation
	if len(accumulator.TranscriptionStreamChunks) > 0 {
		// Sort chunks by chunk index
		sort.Slice(accumulator.TranscriptionStreamChunks, func(i, j int) bool {
			return accumulator.TranscriptionStreamChunks[i].ChunkIndex < accumulator.TranscriptionStreamChunks[j].ChunkIndex
		})
		var rawBuilder strings.Builder
		for _, chunk := range accumulator.TranscriptionStreamChunks {
			if chunk.RawResponse != nil {
				if rawBuilder.Len() > 0 {
					rawBuilder.WriteString("\n\n")
				}
				rawBuilder.WriteString(*chunk.RawResponse)
			}
		}
		if rawBuilder.Len() > 0 {
			s := rawBuilder.String()
			data.RawResponse = &s
		}
	}
	return data, nil
}

// processTranscriptionStreamingResponse processes a transcription streaming response
func (a *Accumulator) processTranscriptionStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Extract accumulator ID from context
	requestID, ok := getAccumulatorID(ctx)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("accumulator-id not found in context or is empty")
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
			RequestID:  requestID,
			StreamType: StreamTypeTranscription,
			Provider:   provider,
			Model:      model,
			Data:       data,
			RawRequest: &rawRequest,
		}, nil
	}
	// Non-final chunk: skip expensive rebuild since no consumer uses intermediate data.
	// Both logging and maxim plugins return early when !isFinalChunk.
	return &ProcessedStreamResponse{
		RequestID:  requestID,
		StreamType: StreamTypeTranscription,
		Provider:   provider,
		Model:      model,
		Data:       nil,
	}, nil
}
