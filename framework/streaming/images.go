package streaming

import (
	"fmt"
	"sort"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// buildCompleteImageFromImageStreamChunks builds a complete image generation response from accumulated chunks
func (a *Accumulator) buildCompleteImageFromImageStreamChunks(chunks []*ImageStreamChunk) *schemas.BifrostImageGenerationResponse {

	// Special case for final chunk, return the complete image response
	for i := range len(chunks) {
		if chunks[i].Delta != nil && (chunks[i].FinishReason != nil || chunks[i].Delta.Type == schemas.ImageGenerationEventTypeCompleted || chunks[i].Delta.Type == schemas.ImageEditEventTypeCompleted) {
			finalResponse := &schemas.BifrostImageGenerationResponse{
				ID:      chunks[i].Delta.ID,
				Created: chunks[i].Delta.CreatedAt,
				Model:   chunks[i].Delta.ExtraFields.ModelRequested,
				Data: []schemas.ImageData{
					{
						B64JSON:       chunks[i].Delta.B64JSON,
						URL:           chunks[i].Delta.URL,
						Index:         chunks[i].ImageIndex,
						RevisedPrompt: chunks[i].Delta.RevisedPrompt,
					},
				},
			}
			return finalResponse
		}
	}
	// Fallback for knitting image generation response from chunks
	// Sort chunks by ImageIndex, then ChunkIndex
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].ImageIndex != chunks[j].ImageIndex {
			return chunks[i].ImageIndex < chunks[j].ImageIndex
		}
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})

	// Reconstruct complete images from chunks
	images := make(map[int]*strings.Builder)
	var model string
	var revisedPrompts map[int]string = make(map[int]string)

	for _, chunk := range chunks {
		if chunk.Delta == nil {
			continue
		}

		// Extract metadata
		if model == "" && chunk.Delta.ExtraFields.ModelRequested != "" {
			model = chunk.Delta.ExtraFields.ModelRequested
		}

		// Store revised prompt if present (usually in first chunk)
		if chunk.Delta.RevisedPrompt != "" {
			revisedPrompts[chunk.ImageIndex] = chunk.Delta.RevisedPrompt
		}

		// Reconstruct base64 for each image
		if chunk.Delta.B64JSON != "" {
			if _, ok := images[chunk.ImageIndex]; !ok {
				images[chunk.ImageIndex] = &strings.Builder{}
			}
			images[chunk.ImageIndex].WriteString(chunk.Delta.B64JSON)
		}
	}

	if len(images) == 0 {
		return nil
	}
	// Build ImageData array in deterministic manner (if indexes are not in order)
	imageIndexes := make([]int, 0, len(images))
	for idx := range images {
		imageIndexes = append(imageIndexes, idx)
	}
	sort.Ints(imageIndexes)

	imageData := make([]schemas.ImageData, 0, len(images))
	for _, imageIndex := range imageIndexes {
		builder := images[imageIndex]
		if builder == nil {
			continue
		}
		imageData = append(imageData, schemas.ImageData{
			B64JSON:       builder.String(),
			Index:         imageIndex,
			RevisedPrompt: revisedPrompts[imageIndex],
		})
	}

	// Build final response
	var responseID string
	for _, chunk := range chunks {
		if chunk.Delta != nil && chunk.Delta.ID != "" {
			responseID = chunk.Delta.ID
			break
		}
	}

	finalResponse := &schemas.BifrostImageGenerationResponse{
		ID:      responseID,
		Created: time.Now().Unix(),
		Model:   model,
		Data:    imageData,
	}

	return finalResponse
}

// processAccumulatedImageStreamingChunks processes all accumulated image chunks in order
func (a *Accumulator) processAccumulatedImageStreamingChunks(requestID string, bifrostErr *schemas.BifrostError, isFinalChunk bool) (*AccumulatedData, error) {
	acc := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	acc.mu.Lock()
	defer func() {
		if isFinalChunk {
			// Cleanup BEFORE unlocking to prevent other goroutines from accessing chunks being returned to pool
			a.cleanupStreamAccumulator(requestID)
		}
		acc.mu.Unlock()
	}()

	// Initialize accumulated data
	data := &AccumulatedData{
		RequestID:      requestID,
		Status:         "success",
		Stream:         true,
		StartTimestamp: acc.StartTimestamp,
		EndTimestamp:   acc.FinalTimestamp,
		Latency:        0,
		OutputMessage:  nil,
		ToolCalls:      nil,
		ErrorDetails:   nil,
		TokenUsage:     nil,
		CacheDebug:     nil,
		Cost:           nil,
	}

	// Build complete message from accumulated chunks
	completeImage := a.buildCompleteImageFromImageStreamChunks(acc.ImageStreamChunks)
	if !isFinalChunk {
		data.ImageGenerationOutput = completeImage
		return data, nil
	}

	// Update database with complete message
	data.Status = "success"
	if bifrostErr != nil {
		data.Status = "error"
	}
	if len(acc.ImageStreamChunks) > 0 {
		lastChunk := acc.ImageStreamChunks[len(acc.ImageStreamChunks)-1]
		if lastChunk.Delta != nil && lastChunk.Delta.ExtraFields.Latency > 0 {
			// Use latency from provider
			data.Latency = lastChunk.Delta.ExtraFields.Latency
		}
	} else if acc.StartTimestamp.IsZero() || acc.FinalTimestamp.IsZero() {
		data.Latency = 0
	} else {
		data.Latency = acc.FinalTimestamp.Sub(acc.StartTimestamp).Nanoseconds() / 1e6
	}
	data.EndTimestamp = acc.FinalTimestamp
	data.ImageGenerationOutput = completeImage
	data.ErrorDetails = bifrostErr

	// Update token usage from final chunk if available
	if len(acc.ImageStreamChunks) > 0 {
		lastChunk := acc.ImageStreamChunks[len(acc.ImageStreamChunks)-1]
		if lastChunk.Delta != nil && lastChunk.Delta.Usage != nil {
			promptTokens := lastChunk.Delta.Usage.InputTokens
			if lastChunk.Delta.Usage.InputTokensDetails != nil {
				promptTokens = lastChunk.Delta.Usage.InputTokensDetails.TextTokens
			}
			data.TokenUsage = &schemas.BifrostLLMUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: 0, // Image generation doesn't have completion tokens
				TotalTokens:      lastChunk.Delta.Usage.TotalTokens,
			}
		}
	}

	// Update cost from final chunk if available
	if len(acc.ImageStreamChunks) > 0 {
		lastChunk := acc.ImageStreamChunks[len(acc.ImageStreamChunks)-1]
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
	}

	// Update semantic cache debug and raw response from final chunk if available
	if len(acc.ImageStreamChunks) > 0 {
		lastChunk := acc.ImageStreamChunks[len(acc.ImageStreamChunks)-1]
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
		if lastChunk.RawResponse != nil {
			data.RawResponse = lastChunk.RawResponse
		}
		data.FinishReason = lastChunk.FinishReason
	}

	return data, nil
}

// processImageStreamingResponse processes an image streaming response
func (a *Accumulator) processImageStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Extract request ID from context
	requestID, ok := getAccumulatorID(ctx)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("accumulator-id not found in context or is empty")
	}
	_, provider, model := bifrost.GetResponseFields(result, bifrostErr)

	isFinalChunk := bifrost.IsFinalChunk(ctx)
	chunk := a.getImageStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr
	if bifrostErr != nil {
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil && result.ImageGenerationStreamResponse != nil {
		// Create a deep copy of the delta to avoid pointing to stack memory
		var partialImageIndex *int
		if result.ImageGenerationStreamResponse.PartialImageIndex != nil {
			idx := *result.ImageGenerationStreamResponse.PartialImageIndex
			partialImageIndex = &idx
		}
		newDelta := &schemas.BifrostImageGenerationStreamResponse{
			ID:                result.ImageGenerationStreamResponse.ID,
			Type:              result.ImageGenerationStreamResponse.Type,
			SequenceNumber:    result.ImageGenerationStreamResponse.SequenceNumber,
			PartialImageIndex: partialImageIndex,
			B64JSON:           result.ImageGenerationStreamResponse.B64JSON,
			URL:               result.ImageGenerationStreamResponse.URL,
			CreatedAt:         result.ImageGenerationStreamResponse.CreatedAt,
			Size:              result.ImageGenerationStreamResponse.Size,
			Quality:           result.ImageGenerationStreamResponse.Quality,
			Background:        result.ImageGenerationStreamResponse.Background,
			OutputFormat:      result.ImageGenerationStreamResponse.OutputFormat,
			RevisedPrompt:     result.ImageGenerationStreamResponse.RevisedPrompt,
			Usage:             result.ImageGenerationStreamResponse.Usage,
			Error:             result.ImageGenerationStreamResponse.Error,
			ExtraFields:       result.ImageGenerationStreamResponse.ExtraFields,
		}
		chunk.Delta = newDelta
		// Prioritize ExtraFields.ChunkIndex over PartialImageIndex (HuggingFace uses ExtraFields.ChunkIndex)
		if result.ImageGenerationStreamResponse.ExtraFields.ChunkIndex > 0 {
			chunk.ChunkIndex = result.ImageGenerationStreamResponse.ExtraFields.ChunkIndex
		} else if result.ImageGenerationStreamResponse.PartialImageIndex != nil {
			chunk.ChunkIndex = *result.ImageGenerationStreamResponse.PartialImageIndex
		}
		// Prioritize Index over SequenceNumber
		if result.ImageGenerationStreamResponse.Index >= 0 {
			chunk.ImageIndex = result.ImageGenerationStreamResponse.Index
		} else {
			chunk.ImageIndex = result.ImageGenerationStreamResponse.SequenceNumber
		}

		// Extract raw response if available
		if result.ImageGenerationStreamResponse.ExtraFields.RawResponse != nil {
			chunk.RawResponse = bifrost.Ptr(fmt.Sprintf("%v", result.ImageGenerationStreamResponse.ExtraFields.RawResponse))
		}

		// Extract usage if available
		if result.ImageGenerationStreamResponse.Usage != nil {
			chunk.TokenUsage = result.ImageGenerationStreamResponse.Usage
		}

		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
			chunk.FinishReason = bifrost.Ptr("completed")
		}
	}

	if addErr := a.addImageStreamChunk(requestID, chunk, isFinalChunk); addErr != nil {
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
			data, processErr := a.processAccumulatedImageStreamingChunks(requestID, bifrostErr, isFinalChunk)
			if processErr != nil {
				a.logger.Error(fmt.Sprintf("failed to process accumulated chunks for request %s: %v", requestID, processErr))
				return nil, processErr
			}
			var rawRequest interface{}
			if result != nil && result.ImageGenerationStreamResponse != nil && result.ImageGenerationStreamResponse.ExtraFields.RawRequest != nil {
				rawRequest = result.ImageGenerationStreamResponse.ExtraFields.RawRequest
			}
			return &ProcessedStreamResponse{
				RequestID:  requestID,
				StreamType: StreamTypeImage,
				Provider:   provider,
				Model:      model,
				Data:       data,
				RawRequest: &rawRequest,
			}, nil
		}

		return nil, nil
	}

	// Non-final chunk: skip expensive rebuild since no consumer uses intermediate data.
	// Both logging and maxim plugins return early when !isFinalChunk.
	return &ProcessedStreamResponse{
		RequestID:  requestID,
		StreamType: StreamTypeImage,
		Provider:   provider,
		Model:      model,
		Data:       nil,
	}, nil
}
