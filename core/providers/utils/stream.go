package utils

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// CheckFirstStreamChunkForError reads the first chunk from a streaming channel to detect
// errors returned inside HTTP 200 SSE streams (e.g., providers that send rate limit
// errors as SSE events instead of HTTP 429).
//
// If the first chunk is an error, it drains the source channel (so the provider
// goroutine can exit cleanly) and returns the error for synchronous handling,
// enabling retries and fallbacks.
//
// If the first chunk is valid data, it returns a wrapped channel that re-emits
// the first chunk followed by all remaining chunks from the source.
//
// If the source channel is closed immediately (empty stream), it returns a
// closed channel with nil error.
func CheckFirstStreamChunkForError(
	stream chan *schemas.BifrostStreamChunk,
) (chan *schemas.BifrostStreamChunk, <-chan struct{}, *schemas.BifrostError) {
	firstChunk, ok := <-stream
	if !ok {
		// Channel closed immediately (empty stream)
		ch := make(chan *schemas.BifrostStreamChunk)
		close(ch)
		done := make(chan struct{})
		close(done)
		return ch, done, nil
	}

	// Check if first chunk is an error
	if firstChunk.BifrostError != nil && firstChunk.BifrostError.Error != nil &&
		(firstChunk.BifrostError.Error.Message != "" || firstChunk.BifrostError.Error.Code != nil || firstChunk.BifrostError.Error.Type != nil) {
		// Drain source channel to let the provider goroutine exit cleanly
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range stream {
			}
		}()
		return nil, done, firstChunk.BifrostError
	}

	// First chunk is valid data — wrap channel to re-inject it
	done := make(chan struct{})
	wrapped := make(chan *schemas.BifrostStreamChunk, max(cap(stream), 1))
	wrapped <- firstChunk
	go func() {
		defer close(done)
		defer close(wrapped)
		for chunk := range stream {
			wrapped <- chunk
		}
	}()
	return wrapped, done, nil
}
