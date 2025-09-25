// Package streaming provides functionality for accumulating streaming chunks and other chunk-related workflows
package streaming

import (
	"context"
	"fmt"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/pricing"
)

// Accumulator manages accumulation of streaming chunks
type Accumulator struct {
	logger schemas.Logger

	streamAccumulators sync.Map  // Track accumulators by request ID (atomic)
	streamChunkPool    sync.Pool // Pool for reusing StreamChunk structs

	pricingManager *pricing.PricingManager

	stopCleanup   chan struct{}
	cleanupWg     sync.WaitGroup
	ttl           time.Duration
	cleanupTicker *time.Ticker
}

// GetStreamChunk gets a stream chunk from the pool
func (a *Accumulator) getStreamChunk() *StreamChunk {
	return a.streamChunkPool.Get().(*StreamChunk)
}

// PutStreamChunk returns a stream chunk to the pool
func (a *Accumulator) putStreamChunk(chunk *StreamChunk) {
	chunk.Timestamp = time.Time{}
	chunk.Delta = nil
	chunk.Cost = nil
	chunk.SemanticCacheDebug = nil
	chunk.ErrorDetails = nil
	chunk.FinishReason = nil
	chunk.TokenUsage = nil
	chunk.ErrorDetails = nil
	a.streamChunkPool.Put(chunk)
}

// CreateStreamAccumulator creates a new stream accumulator for a request
func (a *Accumulator) createStreamAccumulator(requestID string) *StreamAccumulator {
	sc := &StreamAccumulator{
		RequestID:  requestID,
		Chunks:     make([]*StreamChunk, 0),
		IsComplete: false,
		Timestamp:  time.Now(),
	}
	a.streamAccumulators.Store(requestID, sc)
	return sc
}

// GetOrCreateStreamAccumulator gets or creates a stream accumulator for a request
func (a *Accumulator) getOrCreateStreamAccumulator(requestID string) *StreamAccumulator {
	if accumulator, exists := a.streamAccumulators.Load(requestID); exists {
		return accumulator.(*StreamAccumulator)
	}
	// Create new accumulator if it doesn't exist
	return a.createStreamAccumulator(requestID)
}

// AddStreamChunk adds a chunk to the stream accumulator
func (a *Accumulator) addStreamChunk(requestID string, chunk *StreamChunk, object string, isFinalChunk bool) error {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	// Store object type once (from first chunk)
	if accumulator.Object == "" && object != "" {
		accumulator.Object = object
	}
	// Add chunk to the list (chunks arrive in order)
	accumulator.Chunks = append(accumulator.Chunks, chunk)
	// Check if this is the final chunk
	// Set FinalTimestamp when either FinishReason is present or token usage exists
	// This handles both normal completion chunks and usage-only last chunks
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}
	return nil
}

// cleanupStreamAccumulator removes the stream accumulator for a request
func (a *Accumulator) cleanupStreamAccumulator(requestID string) {
	if accumulator, exists := a.streamAccumulators.Load(requestID); exists {
		// Return all chunks to the pool before deleting
		acc := accumulator.(*StreamAccumulator)
		for _, chunk := range acc.Chunks {
			a.putStreamChunk(chunk)
		}
		a.streamAccumulators.Delete(requestID)
	}
}

// accumulateToolCallsInMessage efficiently accumulates tool calls in a message
func (a *Accumulator) accumulateToolCallsInMessage(message *schemas.BifrostMessage, deltaToolCalls []schemas.ToolCall) {
	if message == nil {
		return
	}
	if message.AssistantMessage == nil {
		message.AssistantMessage = &schemas.AssistantMessage{}
	}
	if message.AssistantMessage.ToolCalls == nil {
		message.AssistantMessage.ToolCalls = []schemas.ToolCall{}
	}
	existingToolCalls := message.AssistantMessage.ToolCalls
	for _, deltaToolCall := range deltaToolCalls {
		// Find existing tool call with same ID or create new one
		found := false
		for i := range existingToolCalls {
			if existingToolCalls[i].ID != nil && deltaToolCall.ID != nil &&
				*existingToolCalls[i].ID == *deltaToolCall.ID {
				// Append arguments to existing tool call
				existingToolCalls[i].Function.Arguments += deltaToolCall.Function.Arguments
				found = true
				break
			}
		}
		if !found {
			// Add new tool call
			existingToolCalls = append(existingToolCalls, deltaToolCall)
		}
	}
	message.AssistantMessage.ToolCalls = existingToolCalls
}

// appendContentToMessage efficiently appends content to a message
func (a *Accumulator) appendContentToMessage(message *schemas.BifrostMessage, newContent string) {
	if message == nil {
		return
	}
	if message.Content.ContentStr != nil {
		// Append to existing string content
		*message.Content.ContentStr += newContent
	} else if message.Content.ContentBlocks != nil {
		// Find the last text block and append, or create new one
		blocks := message.Content.ContentBlocks
		if len(blocks) > 0 && blocks[len(blocks)-1].Type == schemas.ContentBlockTypeText && blocks[len(blocks)-1].Text != nil {
			// Append to last text block
			*blocks[len(blocks)-1].Text += newContent
		} else {
			// Create new text block
			blocks = append(blocks, schemas.ContentBlock{
				Type: schemas.ContentBlockTypeText,
				Text: &newContent,
			})
			message.Content.ContentBlocks = blocks
		}
	} else {
		// Initialize with string content
		message.Content.ContentStr = &newContent
	}
}

// buildCompleteMessageFromChunks builds a complete message from accumulated chunks
func (a *Accumulator) buildCompleteMessageFromChunks(chunks []*StreamChunk) *schemas.BifrostMessage {
	completeMessage := &schemas.BifrostMessage{
		Role:    schemas.ModelChatMessageRoleAssistant,
		Content: schemas.MessageContent{},
	}
	for _, chunk := range chunks {
		if chunk.Delta == nil {
			continue
		}
		// Handle role (usually in first chunk)
		if chunk.Delta.Role != nil {
			completeMessage.Role = schemas.ModelChatMessageRole(*chunk.Delta.Role)
		}
		// Append content
		if chunk.Delta.Content != nil && *chunk.Delta.Content != "" {
			a.appendContentToMessage(completeMessage, *chunk.Delta.Content)
		}
		// Handle refusal
		if chunk.Delta.Refusal != nil && *chunk.Delta.Refusal != "" {
			if completeMessage.AssistantMessage == nil {
				completeMessage.AssistantMessage = &schemas.AssistantMessage{}
			}
			if completeMessage.AssistantMessage.Refusal == nil {
				completeMessage.AssistantMessage.Refusal = chunk.Delta.Refusal
			} else {
				*completeMessage.AssistantMessage.Refusal += *chunk.Delta.Refusal
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
func (a *Accumulator) processAccumulatedChunks(requestID string, respErr *schemas.BifrostError, isFinalChunk bool) (*AccumulatedData, error) {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	if isFinalChunk {
		accumulator.mu.Lock()
		// First we cleanup and then unlock
		defer accumulator.mu.Unlock()		
		defer a.cleanupStreamAccumulator(requestID)
	}
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
	completeMessage := a.buildCompleteMessageFromChunks(accumulator.Chunks)
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
	if data.OutputMessage.AssistantMessage != nil && data.OutputMessage.AssistantMessage.ToolCalls != nil {
		data.ToolCalls = data.OutputMessage.AssistantMessage.ToolCalls
	}
	data.ErrorDetails = respErr
	// Update token usage from final chunk if available
	if len(accumulator.Chunks) > 0 {
		lastChunk := accumulator.Chunks[len(accumulator.Chunks)-1]
		if lastChunk.TokenUsage != nil {
			data.TokenUsage = lastChunk.TokenUsage
		}
		// Handle cache debug
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
	}
	// Update cost from final chunk if available
	if len(accumulator.Chunks) > 0 {
		lastChunk := accumulator.Chunks[len(accumulator.Chunks)-1]
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
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
	provider, ok := (*ctx).Value(schemas.BifrostContextKeyRequestProvider).(schemas.ModelProvider)
	if !ok {
		return nil, fmt.Errorf("provider not found in context")
	}
	model, ok := (*ctx).Value(schemas.BifrostContextKeyRequestModel).(string)
	if !ok {
		return nil, fmt.Errorf("model not found in context")
	}
	isFinalChunk := bifrost.IsFinalChunk(ctx)
	chunk := a.getStreamChunk()
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
				cost := a.pricingManager.CalculateCostWithCacheDebug(result, provider, model, schemas.ChatCompletionStreamRequest)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.ExtraFields.CacheDebug
		}
		object = result.Object
	}	
	if addErr := a.addStreamChunk(requestID, chunk, object, isFinalChunk); addErr != nil {
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
			data, processErr := a.processAccumulatedChunks(requestID, bifrostErr, isFinalChunk)
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
	}
	// This is going to be a delta response
	data, processErr := a.processAccumulatedChunks(requestID, bifrostErr, isFinalChunk)
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

// processAudioStreamingResponse processes a audio streaming response
func (a *Accumulator) processAudioStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("request-id not found in context or is empty")
	}
	provider, ok := (*ctx).Value(schemas.BifrostContextKeyRequestProvider).(schemas.ModelProvider)
	if !ok {
		return nil, fmt.Errorf("provider not found in context")
	}
	model, ok := (*ctx).Value(schemas.BifrostContextKeyRequestModel).(string)
	if !ok {
		return nil, fmt.Errorf("model not found in context")
	}
	isFinalChunk := bifrost.IsFinalChunk(ctx)
	// For audio, all the data comes in the final chunk
	if isFinalChunk {
		return nil, nil
	}
	data := &AccumulatedData{
		RequestID:           requestID,
		Model:               model,
		Status:              "success",
		Stream:              true,
		StartTimestamp:      time.Time{},
		EndTimestamp:        time.Time{},
		Latency:             0,
		OutputMessage:       nil,
		ToolCalls:           nil,
		ErrorDetails:        bifrostErr,
		TokenUsage:          nil,
		CacheDebug:          nil,
		Cost:                nil,
		Object:              "",
		TranscriptionOutput: nil,
		FinishReason:        nil,
	}
	if bifrostErr != nil {
		// Error case
		data.ErrorDetails = bifrostErr
	} else if result != nil {
		if result.Model != "" {
			data.Model = model
		}
		// Update object type if available
		if result.Object != "" {
			data.Object = result.Object
		}
		// Token usage
		if result.Usage != nil && result.Usage.TotalTokens > 0 {
			data.TokenUsage = result.Usage
		}
		// Extract token usage from speech and transcription streaming (lightweight)
		if result.Speech != nil && result.Speech.Usage != nil && data.TokenUsage == nil {
			data.TokenUsage = &schemas.LLMUsage{
				PromptTokens:     result.Speech.Usage.InputTokens,
				CompletionTokens: result.Speech.Usage.OutputTokens,
				TotalTokens:      result.Speech.Usage.TotalTokens,
			}
		}
		if result.Transcribe != nil && result.Transcribe.Usage != nil && data.TokenUsage == nil {
			transcriptionUsage := result.Transcribe.Usage
			data.TokenUsage = &schemas.LLMUsage{}

			if transcriptionUsage.InputTokens != nil {
				data.TokenUsage.PromptTokens = *transcriptionUsage.InputTokens
			}
			if transcriptionUsage.OutputTokens != nil {
				data.TokenUsage.CompletionTokens = *transcriptionUsage.OutputTokens
			}
			if transcriptionUsage.TotalTokens != nil {
				data.TokenUsage.TotalTokens = *transcriptionUsage.TotalTokens
			}
		}
		if result.Transcribe != nil && result.Transcribe.BifrostTranscribeStreamResponse != nil && result.Transcribe.Text != "" {
			data.TranscriptionOutput = result.Transcribe
		}
	}
	return &ProcessedStreamResponse{
		Type:       StreamResponseTypeFinal,
		RequestID:  requestID,
		StreamType: StreamTypeAudio,
		Provider:   provider,
		Model:      model,
		Data:       data,
	}, nil
}

// ProcessStreamingResponse processes a streaming response
// It handles both audio and chat streaming responses
func (a *Accumulator) ProcessStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Check if this is a streaming response
	requestType, ok := (*ctx).Value(schemas.BifrostContextKeyRequestType).(schemas.RequestType)
	if !ok {
		return nil, fmt.Errorf("request type missing/invalid")
	}
	isAudioStreaming := requestType == schemas.SpeechStreamRequest || requestType == schemas.TranscriptionStreamRequest
	isChatStreaming := requestType == schemas.ChatCompletionStreamRequest
	if isChatStreaming {
		// Handle text-based streaming with ordered accumulation
		return a.processChatStreamingResponse(ctx, result, bifrostErr)
	} else if isAudioStreaming {
		// Handle speech/transcription streaming with original flow
		return a.processAudioStreamingResponse(ctx, result, bifrostErr)
	}
	return nil, fmt.Errorf("request type missing/invalid for accumulator")
}

// Cleanup cleans up the accumulator
func (a *Accumulator) Cleanup() {
	// Clean up all stream accumulators
	a.streamAccumulators.Range(func(key, value interface{}) bool {
		accumulator := value.(*StreamAccumulator)
		for _, chunk := range accumulator.Chunks {
			a.streamChunkPool.Put(chunk)
		}
		a.streamAccumulators.Delete(key)
		return true
	})
	close(a.stopCleanup)
	a.cleanupTicker.Stop()
	a.cleanupWg.Wait()
}

// CreateStreamAccumulator creates a new stream accumulator for a request
func (a *Accumulator) CreateStreamAccumulator(requestID string, startTimestamp time.Time) *StreamAccumulator {
	sc := a.getOrCreateStreamAccumulator(requestID)
	sc.StartTimestamp = startTimestamp
	return sc
}

// cleanupOldAccumulators removes old accumulators
func (a *Accumulator) cleanupOldAccumulators() {
	count := 0
	a.streamAccumulators.Range(func(key, value interface{}) bool {
		accumulator := value.(*StreamAccumulator)
		if accumulator.Timestamp.Before(time.Now().Add(-a.ttl)) {
			a.streamAccumulators.Delete(key)		
		}
		count++
		return true
	})

	a.logger.Debug("[streaming] cleanup old accumulators done. current size: %d entries", count)
}

// startCleanup runs in a background goroutine to periodically remove expired entries
func (a *Accumulator) startAccumulatorMapCleanup() {
	defer a.cleanupWg.Done()

	for {
		select {
		case <-a.cleanupTicker.C:
			a.cleanupOldAccumulators()
		case <-a.stopCleanup:
			return
		}
	}
}

// NewAccumulator creates a new accumulator
func NewAccumulator(pricingManager *pricing.PricingManager, logger schemas.Logger) *Accumulator {
	a := &Accumulator{
		streamAccumulators: sync.Map{},
		streamChunkPool: sync.Pool{
			New: func() any {
				return &StreamChunk{}
			},
		},
		pricingManager: pricingManager,
		logger:         logger,
		ttl:            30 * time.Minute,
		cleanupTicker:  time.NewTicker(1 * time.Minute),
		cleanupWg:      sync.WaitGroup{},
	}
	// Prewarm the pools for better performance at startup
	for range 1000 {
		a.streamChunkPool.Put(&StreamChunk{})
	}
	go a.startAccumulatorMapCleanup()
	return a
}

// IsStreamingResponse checks if the request is a streaming response
func IsStreamingResponse(ctx *context.Context) bool {
	requestType, ok := (*ctx).Value(schemas.BifrostContextKeyRequestType).(schemas.RequestType)
	if !ok {
		return false
	}
	return requestType == schemas.SpeechStreamRequest || requestType == schemas.TranscriptionStreamRequest || requestType == schemas.ChatCompletionStreamRequest
}
