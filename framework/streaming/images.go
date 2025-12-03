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
		if chunk.Delta.PartialB64 != "" {
			if _, ok := images[chunk.ImageIndex]; !ok {
				images[chunk.ImageIndex] = &strings.Builder{}
			}
			images[chunk.ImageIndex].WriteString(chunk.Delta.PartialB64)
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
	finalResponse := &schemas.BifrostImageGenerationResponse{
		ID:      chunks[0].Delta.ID,
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
	if acc.StartTimestamp.IsZero() || acc.FinalTimestamp.IsZero() {
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
			data.TokenUsage = &schemas.BifrostLLMUsage{
				PromptTokens:     lastChunk.Delta.Usage.PromptTokens,
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

	// Update semantic cache debug from final chunk if available
	if len(acc.ImageStreamChunks) > 0 {
		lastChunk := acc.ImageStreamChunks[len(acc.ImageStreamChunks)-1]
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
		data.FinishReason = lastChunk.FinishReason
	}

	return data, nil
}

// processImageStreamingResponse processes an image streaming response
func (a *Accumulator) processImageStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	a.logger.Debug("[streaming] processing image streaming response")
	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("request-id not found in context or is empty")
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
		newDelta := &schemas.BifrostImageGenerationStreamResponse{
			ID:            result.ImageGenerationStreamResponse.ID,
			Type:          result.ImageGenerationStreamResponse.Type,
			Index:         result.ImageGenerationStreamResponse.Index,
			ChunkIndex:    result.ImageGenerationStreamResponse.ChunkIndex,
			PartialB64:    result.ImageGenerationStreamResponse.PartialB64,
			RevisedPrompt: result.ImageGenerationStreamResponse.RevisedPrompt,
			Usage:         result.ImageGenerationStreamResponse.Usage,
			Error:         result.ImageGenerationStreamResponse.Error,
			ExtraFields:   result.ImageGenerationStreamResponse.ExtraFields,
		}
		chunk.Delta = newDelta
		chunk.ChunkIndex = result.ImageGenerationStreamResponse.ChunkIndex
		chunk.ImageIndex = result.ImageGenerationStreamResponse.Index

		// Extract usage if available
		if result.ImageGenerationStreamResponse.Usage != nil {
			// Note: ImageUsage doesn't directly map to BifrostLLMUsage, but we can store it
			// The actual usage will be extracted in processAccumulatedImageStreamingChunks
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
				a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
				return nil, processErr
			}
			return &ProcessedStreamResponse{
				Type:       StreamResponseTypeFinal,
				RequestID:  requestID,
				StreamType: StreamTypeImage,
				Provider:   provider,
				Model:      model,
				Data:       data,
			}, nil
		}

		return nil, nil
	}

	// This is going to be a delta response
	data, processErr := a.processAccumulatedImageStreamingChunks(requestID, bifrostErr, isFinalChunk)
	if processErr != nil {
		a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
		return nil, processErr
	}

	// This is not the final chunk, so we will send back the delta
	return &ProcessedStreamResponse{
		Type:       StreamResponseTypeDelta,
		RequestID:  requestID,
		StreamType: StreamTypeImage,
		Provider:   provider,
		Model:      model,
		Data:       data,
	}, nil
}
