package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Streaming State Management Methods

// createStreamAccumulator creates a new stream accumulator for a request
func (plugin *Plugin) createStreamAccumulator(requestID string, embedding []float32, metadata map[string]interface{}, ttl time.Duration) *StreamAccumulator {
	accumulator := &StreamAccumulator{
		RequestID:  requestID,
		Chunks:     make([]*StreamChunk, 0),
		IsComplete: false,
		Embedding:  embedding,
		Metadata:   metadata,
		TTL:        ttl,
	}

	plugin.streamAccumulators.Store(requestID, accumulator)
	return accumulator
}

// getOrCreateStreamAccumulator gets or creates a stream accumulator for a request
func (plugin *Plugin) getOrCreateStreamAccumulator(requestID string, embedding []float32, metadata map[string]interface{}, ttl time.Duration) *StreamAccumulator {
	if accumulator, exists := plugin.streamAccumulators.Load(requestID); exists {
		return accumulator.(*StreamAccumulator)
	}

	// Create new accumulator if it doesn't exist
	return plugin.createStreamAccumulator(requestID, embedding, metadata, ttl)
}

// addStreamChunk adds a chunk to the stream accumulator
func (plugin *Plugin) addStreamChunk(requestID string, chunk *StreamChunk, isFinalChunk bool) error {
	// Get accumulator (should exist if properly initialized)
	accumulatorInterface, exists := plugin.streamAccumulators.Load(requestID)
	if !exists {
		return fmt.Errorf("stream accumulator not found for request %s", requestID)
	}

	accumulator := accumulatorInterface.(*StreamAccumulator)
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()

	// Add chunk to the list (chunks arrive in order)
	accumulator.Chunks = append(accumulator.Chunks, chunk)

	// Set FinalTimestamp when FinishReason is present
	// This handles both normal completion chunks and usage-only last chunks
	if isFinalChunk {
		accumulator.FinalTimestamp = chunk.Timestamp
	}

	plugin.logger.Debug(fmt.Sprintf("%s Added chunk to stream accumulator for request %s", PluginLoggerPrefix, requestID))

	return nil
}

// processAccumulatedStream processes all accumulated chunks and caches the complete stream
// Flow: Collect everything → Check for ANY errors → If no errors, order and send to .Add() → If any errors, drop operation
func (plugin *Plugin) processAccumulatedStream(ctx context.Context, requestID string) error {
	accumulatorInterface, exists := plugin.streamAccumulators.Load(requestID)
	if !exists {
		return fmt.Errorf("stream accumulator not found for request %s", requestID)
	}

	accumulator := accumulatorInterface.(*StreamAccumulator)
	accumulator.mu.Lock()

	// Ensure cleanup happens
	defer plugin.cleanupStreamAccumulator(requestID)
	defer accumulator.mu.Unlock()

	// STEP 1: Check if any chunk in the entire stream had an error
	if accumulator.HasError {
		plugin.logger.Debug(fmt.Sprintf("%s Stream for request %s had errors, dropping entire operation (not caching)", PluginLoggerPrefix, requestID))
		return nil
	}

	// STEP 2: All chunks are clean, now sort and build ordered stream for caching
	plugin.logger.Debug(fmt.Sprintf("%s Stream for request %s completed successfully, processing %d chunks for caching", PluginLoggerPrefix, requestID, len(accumulator.Chunks)))

	// Sort chunks by their ChunkIndex to ensure proper order (stable + nil-safe)
	sort.SliceStable(accumulator.Chunks, func(i, j int) bool {
		if accumulator.Chunks[i].Response == nil || accumulator.Chunks[j].Response == nil {
			// Push nils to the end deterministically
			return accumulator.Chunks[j].Response != nil
		}
		return accumulator.Chunks[i].Response.ExtraFields.ChunkIndex < accumulator.Chunks[j].Response.ExtraFields.ChunkIndex
	})

	var streamResponses []string
	for i, chunk := range accumulator.Chunks {
		if chunk.Response != nil {
			chunkData, err := json.Marshal(chunk.Response)
			if err != nil {
				plugin.logger.Warn(fmt.Sprintf("%s Failed to marshal stream chunk %d: %v", PluginLoggerPrefix, i, err))
				continue
			}
			streamResponses = append(streamResponses, string(chunkData))
		}
	}

	// STEP 3: Validate we have valid chunks to cache
	if len(streamResponses) == 0 {
		plugin.logger.Warn(fmt.Sprintf("%s Stream for request %s has no valid response chunks, skipping cache storage", PluginLoggerPrefix, requestID))
		return nil
	}

	// STEP 4: Build final metadata and submit to .Add() method
	finalMetadata := make(map[string]interface{})
	for k, v := range accumulator.Metadata {
		finalMetadata[k] = v
	}
	finalMetadata["stream_chunks"] = streamResponses

	// Store complete unified entry using original requestID - this is the final .Add() call
	if err := plugin.store.Add(ctx, plugin.config.VectorStoreNamespace, requestID, accumulator.Embedding, finalMetadata); err != nil {
		return fmt.Errorf("failed to store complete streaming cache entry: %w", err)
	}

	plugin.logger.Debug(fmt.Sprintf("%s Successfully cached complete stream with %d ordered chunks, ID: %s", PluginLoggerPrefix, len(streamResponses), requestID))
	return nil
}

// cleanupStreamAccumulator removes the stream accumulator for a request
func (plugin *Plugin) cleanupStreamAccumulator(requestID string) {
	plugin.streamAccumulators.Delete(requestID)
}

// cleanupOldStreamAccumulators removes stream accumulators older than 5 minutes
func (plugin *Plugin) cleanupOldStreamAccumulators() {
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	cleanedCount := 0
	toDelete := make([]string, 0)

	plugin.streamAccumulators.Range(func(key, value interface{}) bool {
		requestID := key.(string)
		accumulator := value.(*StreamAccumulator)

		// Check if this accumulator is old (no activity for 5 minutes)
		accumulator.mu.Lock()
		if len(accumulator.Chunks) > 0 {
			firstChunkTime := accumulator.Chunks[0].Timestamp
			if firstChunkTime.Before(fiveMinutesAgo) {
				toDelete = append(toDelete, requestID)
				plugin.logger.Debug(fmt.Sprintf("%s Cleaned up old stream accumulator for request %s", PluginLoggerPrefix, requestID))
			}
		}
		accumulator.mu.Unlock()
		return true
	})

	// Delete outside the Range loop to avoid concurrent modification
	for _, requestID := range toDelete {
		plugin.streamAccumulators.Delete(requestID)
		cleanedCount++
	}

	if cleanedCount > 0 {
		plugin.logger.Debug(fmt.Sprintf("%s Cleaned up %d old stream accumulators", PluginLoggerPrefix, cleanedCount))
	}
}
