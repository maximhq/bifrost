// Package streaming provides functionality for accumulating streaming chunks and other chunk-related workflows
package streaming

import (
	"context"
	"fmt"
	"sync"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/pricing"
)

// Accumulator manages accumulation of streaming chunks
type Accumulator struct {
	logger schemas.Logger

	streamAccumulators sync.Map // Track accumulators by request ID (atomic)

	chatStreamChunkPool          sync.Pool // Pool for reusing StreamChunk structs
	audioStreamChunkPool         sync.Pool // Pool for reusing AudioStreamChunk structs
	transcriptionStreamChunkPool sync.Pool // Pool for reusing TranscriptionStreamChunk structs
	responsesStreamChunkPool     sync.Pool // Pool for reusing ResponsesStreamChunk structs

	pricingManager *pricing.PricingManager

	stopCleanup   chan struct{}
	cleanupWg     sync.WaitGroup
	ttl           time.Duration
	cleanupTicker *time.Ticker
}

// getChatStreamChunk gets a chat stream chunk from the pool
func (a *Accumulator) getChatStreamChunk() *ChatStreamChunk {
	return a.chatStreamChunkPool.Get().(*ChatStreamChunk)
}

// putChatStreamChunk returns a chat stream chunk to the pool
func (a *Accumulator) putChatStreamChunk(chunk *ChatStreamChunk) {
	chunk.Timestamp = time.Time{}
	chunk.Delta = nil
	chunk.Cost = nil
	chunk.SemanticCacheDebug = nil
	chunk.ErrorDetails = nil
	chunk.FinishReason = nil
	chunk.TokenUsage = nil
	a.chatStreamChunkPool.Put(chunk)
}

// GetAudioStreamChunk gets an audio stream chunk from the pool
func (a *Accumulator) getAudioStreamChunk() *AudioStreamChunk {
	return a.audioStreamChunkPool.Get().(*AudioStreamChunk)
}

// PutAudioStreamChunk returns an audio stream chunk to the pool
func (a *Accumulator) putAudioStreamChunk(chunk *AudioStreamChunk) {
	chunk.Timestamp = time.Time{}
	chunk.Delta = nil
	chunk.Cost = nil
	chunk.SemanticCacheDebug = nil
	chunk.ErrorDetails = nil
	chunk.FinishReason = nil
	chunk.TokenUsage = nil
	a.audioStreamChunkPool.Put(chunk)
}

// getTranscriptionStreamChunk gets a transcription stream chunk from the pool
func (a *Accumulator) getTranscriptionStreamChunk() *TranscriptionStreamChunk {
	return a.transcriptionStreamChunkPool.Get().(*TranscriptionStreamChunk)
}

// putTranscriptionStreamChunk returns a transcription stream chunk to the pool
func (a *Accumulator) putTranscriptionStreamChunk(chunk *TranscriptionStreamChunk) {
	chunk.Timestamp = time.Time{}
	chunk.Delta = nil
	chunk.Cost = nil
	chunk.SemanticCacheDebug = nil
	chunk.ErrorDetails = nil
	chunk.FinishReason = nil
	chunk.TranscriptionUsage = nil
	a.transcriptionStreamChunkPool.Put(chunk)
}

// getResponsesStreamChunk gets a responses stream chunk from the pool
func (a *Accumulator) getResponsesStreamChunk() *ResponsesStreamChunk {
	return a.responsesStreamChunkPool.Get().(*ResponsesStreamChunk)
}

// putResponsesStreamChunk returns a responses stream chunk to the pool
func (a *Accumulator) putResponsesStreamChunk(chunk *ResponsesStreamChunk) {
	chunk.Timestamp = time.Time{}
	chunk.Event = nil
	chunk.Cost = nil
	chunk.SemanticCacheDebug = nil
	chunk.ErrorDetails = nil
	chunk.TokenUsage = nil
	a.responsesStreamChunkPool.Put(chunk)
}

// CreateStreamAccumulator creates a new stream accumulator for a request
func (a *Accumulator) createStreamAccumulator(requestID string) *StreamAccumulator {
	sc := &StreamAccumulator{
		RequestID:             requestID,
		ChatStreamChunks:      make([]*ChatStreamChunk, 0),
		ResponsesStreamChunks: make([]*ResponsesStreamChunk, 0),
		IsComplete:            false,
		Timestamp:             time.Now(),
		Object:                "",
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
func (a *Accumulator) addChatStreamChunk(requestID string, chunk *ChatStreamChunk, object string, isFinalChunk bool) error {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.StartTimestamp.IsZero() {
		accumulator.StartTimestamp = chunk.Timestamp
	}
	// Store object type once (from first chunk)
	if accumulator.Object == "" && object != "" {
		accumulator.Object = object
	}
	// Add chunk to the list (chunks arrive in order)
	accumulator.ChatStreamChunks = append(accumulator.ChatStreamChunks, chunk)
	// Check if this is the final chunk
	// Set FinalTimestamp when either FinishReason is present or token usage exists
	// This handles both normal completion chunks and usage-only last chunks
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}
	return nil
}

// AddTranscriptionStreamChunk adds a transcription stream chunk to the stream accumulator
func (a *Accumulator) addTranscriptionStreamChunk(requestID string, chunk *TranscriptionStreamChunk, object string, isFinalChunk bool) error {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.StartTimestamp.IsZero() {
		accumulator.StartTimestamp = chunk.Timestamp
	}
	// Store object type once (from first chunk)
	if accumulator.Object == "" && object != "" {
		accumulator.Object = object
	}
	// Add chunk to the list (chunks arrive in order)
	accumulator.TranscriptionStreamChunks = append(accumulator.TranscriptionStreamChunks, chunk)
	// Check if this is the final chunk
	// Set FinalTimestamp when either FinishReason is present or token usage exists
	// This handles both normal completion chunks and usage-only last chunks
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}
	return nil
}

// AddAudioStreamChunk adds an audio stream chunk to the stream accumulator
func (a *Accumulator) addAudioStreamChunk(requestID string, chunk *AudioStreamChunk, object string, isFinalChunk bool) error {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.StartTimestamp.IsZero() {
		accumulator.StartTimestamp = chunk.Timestamp
	}
	// Store object type once (from first chunk)
	if accumulator.Object == "" && object != "" {
		accumulator.Object = object
	}
	// Add chunk to the list (chunks arrive in order)
	accumulator.AudioStreamChunks = append(accumulator.AudioStreamChunks, chunk)
	// Check if this is the final chunk
	// Set FinalTimestamp when either FinishReason is present or token usage exists
	// This handles both normal completion chunks and usage-only last chunks
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}
	return nil
}

// addResponsesStreamChunk adds a responses stream chunk to the stream accumulator
func (a *Accumulator) addResponsesStreamChunk(requestID string, chunk *ResponsesStreamChunk, object string, isFinalChunk bool) error {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.StartTimestamp.IsZero() {
		accumulator.StartTimestamp = chunk.Timestamp
	}
	if accumulator.Object == "" && object != "" {
		accumulator.Object = object
	}
	accumulator.ResponsesStreamChunks = append(accumulator.ResponsesStreamChunks, chunk)
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
		for _, chunk := range acc.ChatStreamChunks {
			a.putChatStreamChunk(chunk)
		}
		for _, chunk := range acc.AudioStreamChunks {
			a.putAudioStreamChunk(chunk)
		}
		for _, chunk := range acc.TranscriptionStreamChunks {
			a.putTranscriptionStreamChunk(chunk)
		}
		for _, chunk := range acc.ResponsesStreamChunks {
			a.putResponsesStreamChunk(chunk)
		}
		a.streamAccumulators.Delete(requestID)
	}
}

// accumulateToolCallsInMessage efficiently accumulates tool calls in a message
func (a *Accumulator) accumulateToolCallsInMessage(message *schemas.ChatMessage, deltaToolCalls []schemas.ChatAssistantMessageToolCall) {
	if message == nil {
		return
	}
	if message.ChatAssistantMessage == nil {
		message.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
	}
	existingToolCalls := message.ChatAssistantMessage.ToolCalls
	for _, deltaToolCall := range deltaToolCalls {
		var toolCallToModify *schemas.ChatAssistantMessageToolCall
		// Checking if delta tool name is present,
		// If present, then it could be different tool call
		if deltaToolCall.Function.Name != nil {
			// Creating a new tool call
			// Only set arguments if they're not empty or just empty braces
			args := deltaToolCall.Function.Arguments
			if args == "{}" {
				args = "" // Reset empty braces to empty string to avoid duplication
			}
			toolCallToModify = &schemas.ChatAssistantMessageToolCall{
				ID: deltaToolCall.ID,
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      deltaToolCall.Function.Name,
					Arguments: args,
				},
			}
			existingToolCalls = append(existingToolCalls, *toolCallToModify)
		} else {
			// Ensure there's at least one tool call to modify
			if len(existingToolCalls) == 0 {
				a.logger.Warn("received tool call delta without name, but no existing tool calls to append to")
				continue
			}
			// Otherwise we will modify the last tool call
			toolCallToModify = &existingToolCalls[len(existingToolCalls)-1]
			toolCallToModify.Function.Arguments += deltaToolCall.Function.Arguments
		}
	}
	message.ChatAssistantMessage.ToolCalls = existingToolCalls
}

// appendContentToMessage efficiently appends content to a message
func (a *Accumulator) appendContentToMessage(message *schemas.ChatMessage, newContent string) {
	if message == nil {
		return
	}
	if message.Content.ContentStr != nil {
		// Append to existing string content
		*message.Content.ContentStr += newContent
	} else if message.Content.ContentBlocks != nil {
		// Find the last text block and append, or create new one
		blocks := message.Content.ContentBlocks
		if len(blocks) > 0 && blocks[len(blocks)-1].Type == schemas.ChatContentBlockTypeText && blocks[len(blocks)-1].Text != nil {
			// Append to last text block
			*blocks[len(blocks)-1].Text += newContent
		} else {
			// Create new text block
			blocks = append(blocks, schemas.ChatContentBlock{
				Type: schemas.ChatContentBlockTypeText,
				Text: &newContent,
			})
			message.Content.ContentBlocks = blocks
		}
	} else {
		// Initialize with string content
		message.Content.ContentStr = &newContent
	}
}

// ProcessStreamingResponse processes a streaming response
// It handles both audio and chat streaming responses
func (a *Accumulator) ProcessStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Check if this is a streaming response
	if result == nil {
		return nil, fmt.Errorf("result is nil")
	}
	requestType := result.ExtraFields.RequestType
	isAudioStreaming := requestType == schemas.SpeechStreamRequest || requestType == schemas.TranscriptionStreamRequest
	isChatStreaming := requestType == schemas.ChatCompletionStreamRequest || requestType == schemas.TextCompletionStreamRequest
	isResponsesStreaming := requestType == schemas.ResponsesStreamRequest
	if isChatStreaming {
		// Handle text-based streaming with ordered accumulation
		return a.processChatStreamingResponse(ctx, result, bifrostErr)
	} else if isAudioStreaming {
		// Handle speech/transcription streaming with original flow
		if requestType == schemas.TranscriptionStreamRequest {
			return a.processTranscriptionStreamingResponse(ctx, result, bifrostErr)
		}
		if requestType == schemas.SpeechStreamRequest {
			return a.processAudioStreamingResponse(ctx, result, bifrostErr)
		}
	} else if isResponsesStreaming {
		return a.processResponsesStreamingResponse(ctx, result, bifrostErr)
	}
	return nil, fmt.Errorf("request type missing/invalid for accumulator")
}

// Cleanup cleans up the accumulator
func (a *Accumulator) Cleanup() {
	// Clean up all stream accumulators
	a.streamAccumulators.Range(func(key, value interface{}) bool {
		accumulator := value.(*StreamAccumulator)
		for _, chunk := range accumulator.ChatStreamChunks {
			a.chatStreamChunkPool.Put(chunk)
		}
		for _, chunk := range accumulator.TranscriptionStreamChunks {
			a.transcriptionStreamChunkPool.Put(chunk)
		}
		for _, chunk := range accumulator.AudioStreamChunks {
			a.audioStreamChunkPool.Put(chunk)
		}
		for _, chunk := range accumulator.ResponsesStreamChunks {
			a.responsesStreamChunkPool.Put(chunk)
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

// CleanupStreamAccumulator cleans up the stream accumulator for a request
func (a *Accumulator) CleanupStreamAccumulator(requestID string) error {
	a.cleanupStreamAccumulator(requestID)
	return nil
}

// cleanupOldAccumulators removes old accumulators
func (a *Accumulator) cleanupOldAccumulators() {
	count := 0
	a.streamAccumulators.Range(func(key, value interface{}) bool {
		accumulator := value.(*StreamAccumulator)
		if accumulator.Timestamp.Before(time.Now().Add(-a.ttl)) {
			a.cleanupStreamAccumulator(key.(string))
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
		chatStreamChunkPool: sync.Pool{
			New: func() any {
				return &ChatStreamChunk{}
			},
		},
		audioStreamChunkPool: sync.Pool{
			New: func() any {
				return &AudioStreamChunk{}
			},
		},
		transcriptionStreamChunkPool: sync.Pool{
			New: func() any {
				return &TranscriptionStreamChunk{}
			},
		},
		responsesStreamChunkPool: sync.Pool{
			New: func() any {
				return &ResponsesStreamChunk{}
			},
		},
		pricingManager: pricingManager,
		logger:         logger,
		ttl:            30 * time.Minute,
		cleanupTicker:  time.NewTicker(1 * time.Minute),
		cleanupWg:      sync.WaitGroup{},
		stopCleanup:    make(chan struct{}),
	}
	a.cleanupWg.Add(1)
	// Prewarm the pools for better performance at startup
	for range 1000 {
		a.chatStreamChunkPool.Put(&ChatStreamChunk{})
		a.audioStreamChunkPool.Put(&AudioStreamChunk{})
		a.transcriptionStreamChunkPool.Put(&TranscriptionStreamChunk{})
		a.responsesStreamChunkPool.Put(&ResponsesStreamChunk{})
	}
	go a.startAccumulatorMapCleanup()
	return a
}
