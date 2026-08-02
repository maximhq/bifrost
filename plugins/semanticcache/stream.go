package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// chunkSortKey returns the (Index, ChunkIndex) tuple used to order
// accumulated stream chunks before flush. Image-generation responses use
// both fields; every other response shape uses ChunkIndex with Index=0.
// Nil chunks/responses sort to the end via a max-int sentinel so they're
// dropped deterministically by the consumer.
func chunkSortKey(c *StreamChunk) (int, int) {
	const sentinel = int(^uint(0) >> 1) // math.MaxInt without the import
	if c == nil || c.Response == nil {
		return sentinel, sentinel
	}
	r := c.Response
	switch {
	case r.TextCompletionResponse != nil:
		return 0, r.TextCompletionResponse.ExtraFields.ChunkIndex
	case r.ChatResponse != nil:
		return 0, r.ChatResponse.ExtraFields.ChunkIndex
	case r.ResponsesResponse != nil:
		return 0, r.ResponsesResponse.ExtraFields.ChunkIndex
	case r.ResponsesStreamResponse != nil:
		return 0, r.ResponsesStreamResponse.ExtraFields.ChunkIndex
	case r.SpeechResponse != nil:
		return 0, r.SpeechResponse.ExtraFields.ChunkIndex
	case r.SpeechStreamResponse != nil:
		return 0, r.SpeechStreamResponse.ExtraFields.ChunkIndex
	case r.TranscriptionResponse != nil:
		return 0, r.TranscriptionResponse.ExtraFields.ChunkIndex
	case r.TranscriptionStreamResponse != nil:
		return 0, r.TranscriptionStreamResponse.ExtraFields.ChunkIndex
	case r.ImageGenerationStreamResponse != nil:
		return r.ImageGenerationStreamResponse.Index, r.ImageGenerationStreamResponse.ChunkIndex
	}
	return sentinel, sentinel
}

// getOrCreateStreamAccumulator returns the StreamAccumulator for requestID,
// creating one if none exists. Concurrency-safe: the underlying sync.Map's
// LoadOrStore guarantees a single accumulator per request even under racing
// PostLLMHook invocations.
func (plugin *Plugin) getOrCreateStreamAccumulator(requestID string, storageID string, embedding []float32, metadata map[string]interface{}, ttl time.Duration) *StreamAccumulator {
	if existing, ok := plugin.streamAccumulators.Load(requestID); ok {
		return existing.(*StreamAccumulator)
	}
	newAccumulator := &StreamAccumulator{
		RequestID:  requestID,
		StorageID:  storageID,
		Chunks:     make([]*StreamChunk, 0),
		LastSeenAt: time.Now(),
		Embedding:  embedding,
		Metadata:   metadata,
		TTL:        ttl,
	}
	actual, _ := plugin.streamAccumulators.LoadOrStore(requestID, newAccumulator)
	return actual.(*StreamAccumulator)
}

// addStreamChunk appends a chunk to the request's accumulator and refreshes
// LastSeenAt so the reaper treats the stream as still active.
func (plugin *Plugin) addStreamChunk(requestID string, chunk *StreamChunk) error {
	accumulatorInterface, exists := plugin.streamAccumulators.Load(requestID)
	if !exists {
		return fmt.Errorf("stream accumulator not found for request %s", requestID)
	}
	accumulator := accumulatorInterface.(*StreamAccumulator)
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	accumulator.Chunks = append(accumulator.Chunks, chunk)
	accumulator.LastSeenAt = chunk.Timestamp
	return nil
}

// processAccumulatedStream serializes and stores the accumulated chunks as a
// single cache entry. Called once per stream when the final chunk arrives.
func (plugin *Plugin) processAccumulatedStream(ctx context.Context, requestID string) error {
	accumulatorInterface, exists := plugin.streamAccumulators.Load(requestID)
	if !exists {
		return fmt.Errorf("stream accumulator not found for request %s", requestID)
	}

	accumulator := accumulatorInterface.(*StreamAccumulator)
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	defer plugin.cleanupStreamAccumulator(requestID)

	sort.SliceStable(accumulator.Chunks, func(i, j int) bool {
		ai, bi := chunkSortKey(accumulator.Chunks[i])
		aj, bj := chunkSortKey(accumulator.Chunks[j])
		if ai != aj {
			return ai < aj
		}
		return bi < bj
	})

	streamResponses := make([]string, 0, len(accumulator.Chunks))
	for i, chunk := range accumulator.Chunks {
		if chunk.Response == nil {
			continue
		}
		chunkData, err := json.Marshal(chunk.Response)
		if err != nil {
			plugin.logger.Warn("Failed to marshal stream chunk %d: %v", i, err)
			continue
		}
		streamResponses = append(streamResponses, string(chunkData))
	}

	if len(streamResponses) == 0 {
		plugin.logger.Warn("Stream for request %s has no valid response chunks, skipping cache storage", requestID)
		return nil
	}

	finalMetadata := make(map[string]interface{}, len(accumulator.Metadata)+1)
	for k, v := range accumulator.Metadata {
		finalMetadata[k] = v
	}
	finalMetadata["stream_chunks"] = streamResponses

	if err := plugin.store.Add(ctx, plugin.config.VectorStoreNamespace, accumulator.StorageID, accumulator.Embedding, finalMetadata); err != nil {
		return fmt.Errorf("failed to store complete streaming cache entry: %w", err)
	}

	plugin.logger.Debug("Cached stream with %d chunks, storageID=%s", len(streamResponses), accumulator.StorageID)
	return nil
}

// cleanupStreamAccumulator drops the accumulator for requestID. Safe to call
// when no entry exists.
func (plugin *Plugin) cleanupStreamAccumulator(requestID string) {
	plugin.streamAccumulators.Delete(requestID)
}

// streamAccumulatorMaxAge is how long a stream accumulator may live without
// reaching its final chunk before it's reaped by the periodic cleanup.
const streamAccumulatorMaxAge = 5 * time.Minute

// streamCleanupInterval bounds the worst-case staleness of an abandoned
// accumulator to ~maxAge + interval.
const streamCleanupInterval = 1 * time.Minute

// cleanupOldStreamAccumulators reaps accumulators whose most recent chunk is
// older than streamAccumulatorMaxAge. Called both periodically and at
// shutdown to prevent abandoned streams (client disconnect, mid-stream
// error) from accumulating in memory; reaping by LastSeenAt rather than
// first-chunk time keeps long-running streams alive while they're still
// receiving chunks.
func (plugin *Plugin) cleanupOldStreamAccumulators() {
	cutoff := time.Now().Add(-streamAccumulatorMaxAge)
	var toDelete []string

	plugin.streamAccumulators.Range(func(key, value interface{}) bool {
		requestID := key.(string)
		accumulator := value.(*StreamAccumulator)
		accumulator.mu.Lock()
		if accumulator.LastSeenAt.Before(cutoff) {
			toDelete = append(toDelete, requestID)
		}
		accumulator.mu.Unlock()
		return true
	})

	for _, requestID := range toDelete {
		plugin.streamAccumulators.Delete(requestID)
	}

	if len(toDelete) > 0 {
		plugin.logger.Debug("Reaped %d stale stream accumulators", len(toDelete))
	}
}

// runStreamCleanupLoop runs cleanupOldStreamAccumulators on a ticker until
// stopCh is closed. Started by Init, stopped by Cleanup.
func (plugin *Plugin) runStreamCleanupLoop() {
	defer plugin.cleanupWg.Done()
	ticker := time.NewTicker(streamCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-plugin.stopCh:
			return
		case <-ticker.C:
			plugin.cleanupOldStreamAccumulators()
		}
	}
}
