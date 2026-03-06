// Package streaming provides functionality for accumulating streaming chunks and other chunk-related workflows
package streaming

import (
	"fmt"
	"sync"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// getAccumulatorID extracts the ID for accumulator lookup from context.
// Returns the value of BifrostContextKeyAccumulatorID.
func getAccumulatorID(ctx *schemas.BifrostContext) (string, bool) {
	if id, ok := ctx.Value(schemas.BifrostContextKeyAccumulatorID).(string); ok && id != "" {
		return id, true
	}
	return "", false
}

// Accumulator manages accumulation of streaming chunks
type Accumulator struct {
	logger schemas.Logger

	streamAccumulators sync.Map // Track accumulators by request ID (atomic)

	chatStreamChunkPool          sync.Pool // Pool for reusing StreamChunk structs
	responsesStreamChunkPool     sync.Pool // Pool for reusing ResponsesStreamChunk structs
	audioStreamChunkPool         sync.Pool // Pool for reusing AudioStreamChunk structs
	transcriptionStreamChunkPool sync.Pool // Pool for reusing TranscriptionStreamChunk structs
	imageStreamChunkPool         sync.Pool // Pool for reusing ImageStreamChunk structs

	pricingManager *modelcatalog.ModelCatalog

	stopCleanup   chan struct{}
	cleanupWg     sync.WaitGroup
	cleanupOnce   sync.Once
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
	chunk.RawResponse = nil
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
	chunk.RawResponse = nil
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
	chunk.TokenUsage = nil
	chunk.RawResponse = nil
	a.transcriptionStreamChunkPool.Put(chunk)
}

// getResponsesStreamChunk gets a responses stream chunk from the pool
func (a *Accumulator) getResponsesStreamChunk() *ResponsesStreamChunk {
	return a.responsesStreamChunkPool.Get().(*ResponsesStreamChunk)
}

// putResponsesStreamChunk returns a responses stream chunk to the pool
func (a *Accumulator) putResponsesStreamChunk(chunk *ResponsesStreamChunk) {
	chunk.Timestamp = time.Time{}
	chunk.StreamResponse = nil
	chunk.Cost = nil
	chunk.SemanticCacheDebug = nil
	chunk.ErrorDetails = nil
	chunk.FinishReason = nil
	chunk.TokenUsage = nil
	chunk.RawResponse = nil
	a.responsesStreamChunkPool.Put(chunk)
}

// getImageStreamChunk gets an image stream chunk from the pool
func (a *Accumulator) getImageStreamChunk() *ImageStreamChunk {
	return a.imageStreamChunkPool.Get().(*ImageStreamChunk)
}

// putImageStreamChunk returns an image stream chunk to the pool
func (a *Accumulator) putImageStreamChunk(chunk *ImageStreamChunk) {
	chunk.Timestamp = time.Time{}
	chunk.Delta = nil
	chunk.FinishReason = nil
	chunk.ErrorDetails = nil
	chunk.ChunkIndex = 0
	chunk.ImageIndex = 0
	chunk.Cost = nil
	chunk.SemanticCacheDebug = nil
	chunk.TokenUsage = nil
	chunk.RawResponse = nil
	a.imageStreamChunkPool.Put(chunk)
}

// createStreamAccumulator creates a new stream accumulator for a request
// StartTimestamp is set to current time if not provided via CreateStreamAccumulator
func (a *Accumulator) createStreamAccumulator(requestID string) *StreamAccumulator {
	now := time.Now()
	sc := &StreamAccumulator{
		RequestID:                  requestID,
		ChatStreamChunks:           make([]*ChatStreamChunk, 0),
		ResponsesStreamChunks:      make([]*ResponsesStreamChunk, 0),
		ImageStreamChunks:          make([]*ImageStreamChunk, 0),
		TranscriptionStreamChunks:  make([]*TranscriptionStreamChunk, 0),
		AudioStreamChunks:          make([]*AudioStreamChunk, 0),
		ChatChunksSeen:             make(map[int]struct{}),
		ResponsesChunksSeen:        make(map[int]struct{}),
		TranscriptionChunksSeen:    make(map[int]struct{}),
		AudioChunksSeen:            make(map[int]struct{}),
		ImageChunksSeen:            make(map[string]struct{}),
		MaxChatChunkIndex:          -1,
		MaxResponsesChunkIndex:     -1,
		MaxTranscriptionChunkIndex: -1,
		MaxAudioChunkIndex:         -1,
		IsComplete:                 false,
		mu:                         sync.Mutex{},
		Timestamp:                  now,
		StartTimestamp:             now, // Set default StartTimestamp for proper TTFT/latency calculation
	}
	a.streamAccumulators.Store(requestID, sc)
	return sc
}

// getOrCreateStreamAccumulator gets or creates a stream accumulator for a request
func (a *Accumulator) getOrCreateStreamAccumulator(requestID string) *StreamAccumulator {
	// Fast path: check if already exists (no allocation)
	if acc, exists := a.streamAccumulators.Load(requestID); exists {
		return acc.(*StreamAccumulator)
	}

	// Slow path: create new accumulator
	now := time.Now()
	newAcc := &StreamAccumulator{
		RequestID:                  requestID,
		ChatStreamChunks:           make([]*ChatStreamChunk, 0),
		ResponsesStreamChunks:      make([]*ResponsesStreamChunk, 0),
		ImageStreamChunks:          make([]*ImageStreamChunk, 0),
		TranscriptionStreamChunks:  make([]*TranscriptionStreamChunk, 0),
		AudioStreamChunks:          make([]*AudioStreamChunk, 0),
		ChatChunksSeen:             make(map[int]struct{}),
		ResponsesChunksSeen:        make(map[int]struct{}),
		TranscriptionChunksSeen:    make(map[int]struct{}),
		AudioChunksSeen:            make(map[int]struct{}),
		ImageChunksSeen:            make(map[string]struct{}),
		MaxChatChunkIndex:          -1,
		MaxResponsesChunkIndex:     -1,
		MaxTranscriptionChunkIndex: -1,
		MaxAudioChunkIndex:         -1,
		IsComplete:                 false,
		mu:                         sync.Mutex{},
		Timestamp:                  now,
		StartTimestamp:             now,
	}

	// LoadOrStore atomically: if key exists, return existing; else store new
	actual, _ := a.streamAccumulators.LoadOrStore(requestID, newAcc)
	return actual.(*StreamAccumulator)
}

// AddStreamChunk adds a chunk to the stream accumulator
func (a *Accumulator) addChatStreamChunk(requestID string, chunk *ChatStreamChunk, isFinalChunk bool) error {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.StartTimestamp.IsZero() {
		accumulator.StartTimestamp = chunk.Timestamp
	}
	// Track first chunk timestamp for TTFT calculation
	if accumulator.FirstChunkTimestamp.IsZero() {
		accumulator.FirstChunkTimestamp = chunk.Timestamp
	}
	// De-dup check - only add if not seen (handles out-of-order arrival and multiple plugins)
	if _, seen := accumulator.ChatChunksSeen[chunk.ChunkIndex]; !seen {
		accumulator.ChatChunksSeen[chunk.ChunkIndex] = struct{}{}
		accumulator.ChatStreamChunks = append(accumulator.ChatStreamChunks, chunk)
		// Track max index for metadata extraction
		if chunk.ChunkIndex > accumulator.MaxChatChunkIndex {
			accumulator.MaxChatChunkIndex = chunk.ChunkIndex
		}
	}
	// Check if this is the final chunk
	// Set FinalTimestamp when either FinishReason is present or token usage exists
	// This handles both normal completion chunks and usage-only last chunks
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}
	return nil
}

// AddTranscriptionStreamChunk adds a transcription stream chunk to the stream accumulator
func (a *Accumulator) addTranscriptionStreamChunk(requestID string, chunk *TranscriptionStreamChunk, isFinalChunk bool) error {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.StartTimestamp.IsZero() {
		accumulator.StartTimestamp = chunk.Timestamp
	}
	// Track first chunk timestamp for TTFT calculation
	if accumulator.FirstChunkTimestamp.IsZero() {
		accumulator.FirstChunkTimestamp = chunk.Timestamp
	}
	if _, seen := accumulator.TranscriptionChunksSeen[chunk.ChunkIndex]; !seen {
		accumulator.TranscriptionChunksSeen[chunk.ChunkIndex] = struct{}{}
		accumulator.TranscriptionStreamChunks = append(accumulator.TranscriptionStreamChunks, chunk)
		// Track max index for metadata extraction
		if chunk.ChunkIndex > accumulator.MaxTranscriptionChunkIndex {
			accumulator.MaxTranscriptionChunkIndex = chunk.ChunkIndex
		}
	}
	// Check if this is the final chunk
	// Set FinalTimestamp when either FinishReason is present or token usage exists
	// This handles both normal completion chunks and usage-only last chunks
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}
	return nil
}

// addAudioStreamChunk adds an audio stream chunk to the stream accumulator
func (a *Accumulator) addAudioStreamChunk(requestID string, chunk *AudioStreamChunk, isFinalChunk bool) error {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.StartTimestamp.IsZero() {
		accumulator.StartTimestamp = chunk.Timestamp
	}
	// Track first chunk timestamp for TTFT calculation
	if accumulator.FirstChunkTimestamp.IsZero() {
		accumulator.FirstChunkTimestamp = chunk.Timestamp
	}
	if _, seen := accumulator.AudioChunksSeen[chunk.ChunkIndex]; !seen {
		accumulator.AudioChunksSeen[chunk.ChunkIndex] = struct{}{}
		accumulator.AudioStreamChunks = append(accumulator.AudioStreamChunks, chunk)
		// Track max index for metadata extraction
		if chunk.ChunkIndex > accumulator.MaxAudioChunkIndex {
			accumulator.MaxAudioChunkIndex = chunk.ChunkIndex
		}
	}
	// Check if this is the final chunk
	// Set FinalTimestamp when either FinishReason is present or token usage exists
	// This handles both normal completion chunks and usage-only last chunks
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}
	return nil
}

// addResponsesStreamChunk adds a responses stream chunk to the stream accumulator
func (a *Accumulator) addResponsesStreamChunk(requestID string, chunk *ResponsesStreamChunk, isFinalChunk bool) error {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if accumulator.StartTimestamp.IsZero() {
		accumulator.StartTimestamp = chunk.Timestamp
	}
	// Track first chunk timestamp for TTFT calculation
	if accumulator.FirstChunkTimestamp.IsZero() {
		accumulator.FirstChunkTimestamp = chunk.Timestamp
	}
	if _, seen := accumulator.ResponsesChunksSeen[chunk.ChunkIndex]; !seen {
		accumulator.ResponsesChunksSeen[chunk.ChunkIndex] = struct{}{}
		accumulator.ResponsesStreamChunks = append(accumulator.ResponsesStreamChunks, chunk)
		// Track max index for metadata extraction
		if chunk.ChunkIndex > accumulator.MaxResponsesChunkIndex {
			accumulator.MaxResponsesChunkIndex = chunk.ChunkIndex
		}
	}
	// Check if this is the final chunk
	// Set FinalTimestamp when either FinishReason is present or token usage exists
	// This handles both normal completion chunks and usage-only last chunks
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}
	return nil
}

// imageChunkKey creates a composite key for image chunk de-duplication
func imageChunkKey(imageIndex, chunkIndex int) string {
	return fmt.Sprintf("%d:%d", imageIndex, chunkIndex)
}

// addImageStreamChunk adds an image stream chunk to the stream accumulator
func (a *Accumulator) addImageStreamChunk(requestID string, chunk *ImageStreamChunk, isFinalChunk bool) error {
	acc := a.getOrCreateStreamAccumulator(requestID)
	acc.mu.Lock()
	defer acc.mu.Unlock()

	if acc.StartTimestamp.IsZero() {
		acc.StartTimestamp = chunk.Timestamp
	}
	if acc.FirstChunkTimestamp.IsZero() {
		acc.FirstChunkTimestamp = chunk.Timestamp
	}

	// De-dup check - only add if not seen (handles out-of-order arrival and multiple plugins)
	chunkKey := imageChunkKey(chunk.ImageIndex, chunk.ChunkIndex)
	if _, seen := acc.ImageChunksSeen[chunkKey]; !seen {
		acc.ImageChunksSeen[chunkKey] = struct{}{}
		acc.ImageStreamChunks = append(acc.ImageStreamChunks, chunk)
	}
	// Check if this is the final chunk
	// Set FinalTimestamp when this is the final chunk, regardless of de-dup status
	// This handles cases where final chunk arrives after duplicates or is itself duplicated
	if isFinalChunk {
		acc.FinalTimestamp = chunk.Timestamp
	}
	return nil
}

// cleanupStreamAccumulator removes the stream accumulator for a request.
// IMPORTANT: Caller must hold accumulator.mu lock before calling this function
// to prevent races when returning chunks to pools.
func (a *Accumulator) cleanupStreamAccumulator(requestID string) {
	if accumulator, exists := a.streamAccumulators.Load(requestID); exists {
		acc := accumulator.(*StreamAccumulator)

		// Return all chunks to the pool before deleting
		for _, chunk := range acc.ChatStreamChunks {
			a.putChatStreamChunk(chunk)
		}
		for _, chunk := range acc.ResponsesStreamChunks {
			a.putResponsesStreamChunk(chunk)
		}
		for _, chunk := range acc.AudioStreamChunks {
			a.putAudioStreamChunk(chunk)
		}
		for _, chunk := range acc.TranscriptionStreamChunks {
			a.putTranscriptionStreamChunk(chunk)
		}
		for _, chunk := range acc.ImageStreamChunks {
			a.putImageStreamChunk(chunk)
		}
		a.streamAccumulators.Delete(requestID)
	}
}


// ProcessStreamingResponse processes a streaming response
// It handles chat, audio, and responses streaming responses
func (a *Accumulator) ProcessStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Check if this is a streaming response
	if result == nil {
		return nil, fmt.Errorf("result is nil")
	}
	extraFields := result.GetExtraFields()
	requestType := extraFields.RequestType
	isAudioStreaming := requestType == schemas.SpeechStreamRequest || requestType == schemas.TranscriptionStreamRequest
	isChatStreaming := requestType == schemas.ChatCompletionStreamRequest || requestType == schemas.TextCompletionStreamRequest
	isResponsesStreaming := requestType == schemas.ResponsesStreamRequest
	// Edit images/ Image variation requests will be added here
	isImageStreaming := requestType == schemas.ImageGenerationStreamRequest || requestType == schemas.ImageEditStreamRequest

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
		// Handle responses streaming with responses accumulation
		return a.processResponsesStreamingResponse(ctx, result, bifrostErr)
	} else if isImageStreaming {
		// Handle image streaming
		return a.processImageStreamingResponse(ctx, result, bifrostErr)
	}
	return nil, fmt.Errorf("request type missing/invalid for accumulator: %s", requestType)
}

// Cleanup cleans up the accumulator
func (a *Accumulator) Cleanup() {
	// Clean up all stream accumulators
	a.streamAccumulators.Range(func(key, value interface{}) bool {
		accumulator := value.(*StreamAccumulator)

		// Lock before accessing chunk slices
		accumulator.mu.Lock()
		for _, chunk := range accumulator.ChatStreamChunks {
			a.putChatStreamChunk(chunk)
		}
		for _, chunk := range accumulator.ResponsesStreamChunks {
			a.putResponsesStreamChunk(chunk)
		}
		for _, chunk := range accumulator.TranscriptionStreamChunks {
			a.putTranscriptionStreamChunk(chunk)
		}
		for _, chunk := range accumulator.AudioStreamChunks {
			a.putAudioStreamChunk(chunk)
		}
		for _, chunk := range accumulator.ImageStreamChunks {
			a.putImageStreamChunk(chunk)
		}
		accumulator.mu.Unlock()

		a.streamAccumulators.Delete(key)
		return true
	})
	a.cleanupOnce.Do(func() {
		close(a.stopCleanup)
	})
	a.cleanupTicker.Stop()
	a.cleanupWg.Wait()
}

// CreateStreamAccumulator creates a new stream accumulator for a request
// It increments the reference counter atomically for concurrent access tracking
func (a *Accumulator) CreateStreamAccumulator(requestID string, startTimestamp time.Time) *StreamAccumulator {
	sc := a.getOrCreateStreamAccumulator(requestID)
	// Atomically increment reference counter
	sc.refCount.Add(1)
	// Lock before writing to StartTimestamp
	sc.mu.Lock()
	sc.StartTimestamp = startTimestamp
	sc.mu.Unlock()
	return sc
}

// CleanupStreamAccumulator decrements the reference counter for a stream accumulator.
// The accumulator is only cleaned up when the reference counter reaches 0.
// This function is idempotent - calling it after cleanup has already happened is safe.
func (a *Accumulator) CleanupStreamAccumulator(requestID string) error {
	acc, exists := a.streamAccumulators.Load(requestID)
	if !exists {
		// Accumulator already cleaned up - this is expected when multiple callers
		// (e.g., completeDeferredSpan and HTTP middleware) both call cleanup
		return nil
	}
	if accumulator, ok := acc.(*StreamAccumulator); ok {
		// Atomically decrement reference counter
		newCount := accumulator.refCount.Add(-1)
		// Only cleanup when reference counter reaches 0
		if newCount <= 0 {
			accumulator.mu.Lock()
			defer accumulator.mu.Unlock()
			a.cleanupStreamAccumulator(requestID)
		}
	}
	return nil
}

// cleanupOldAccumulators removes old accumulators
func (a *Accumulator) cleanupOldAccumulators() {
	count := 0
	a.streamAccumulators.Range(func(key, value interface{}) bool {
		accumulator := value.(*StreamAccumulator)
		accumulator.mu.Lock()
		defer accumulator.mu.Unlock()
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
func NewAccumulator(pricingManager *modelcatalog.ModelCatalog, logger schemas.Logger) *Accumulator {
	a := &Accumulator{
		streamAccumulators: sync.Map{},
		chatStreamChunkPool: sync.Pool{
			New: func() any {
				return &ChatStreamChunk{}
			},
		},
		responsesStreamChunkPool: sync.Pool{
			New: func() any {
				return &ResponsesStreamChunk{}
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
		imageStreamChunkPool: sync.Pool{
			New: func() any {
				return &ImageStreamChunk{}
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
		a.responsesStreamChunkPool.Put(&ResponsesStreamChunk{})
		a.audioStreamChunkPool.Put(&AudioStreamChunk{})
		a.transcriptionStreamChunkPool.Put(&TranscriptionStreamChunk{})
		a.imageStreamChunkPool.Put(&ImageStreamChunk{})
	}
	go a.startAccumulatorMapCleanup()
	return a
}
