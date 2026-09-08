package utils

import (
	"context"
	"sync"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	maxStreamPreambleChunks = 64
	maxStreamPreambleBytes  = 256 * 1024
)

// Include the source channel to isolate attempts sharing a request ID.
type streamPreambleKey struct {
	requestID string
	source    chan *schemas.BifrostStreamChunk
}

type streamPreambleBuffer struct {
	chunks []*schemas.BifrostStreamChunk
	bytes  int
}

// Entries are owned by the startup checker, then its replay goroutine.
// The owner must delete its entry on error, cancellation, or replay completion.
var streamPreambles sync.Map // map[streamPreambleKey]*streamPreambleBuffer

// tryAppend returns false when the caller should commit the stream.
// On false, the chunk remains unbuffered; the caller must forward it
// after replaying the buffered prefix.
func (buffer *streamPreambleBuffer) tryAppend(chunk *schemas.BifrostStreamChunk) bool {
	if len(buffer.chunks)+1 >= maxStreamPreambleChunks {
		return false
	}
	encoded, err := MarshalSorted(chunk)
	if err != nil || len(encoded) >= maxStreamPreambleBytes-buffer.bytes {
		return false
	}
	buffer.chunks = append(buffer.chunks, chunk)
	buffer.bytes += len(encoded)
	return true
}

// replayStreamPreamble transfers buffer ownership to the forwarding goroutine.
// first is the unbuffered chunk that committed the stream, or nil at EOF.
func replayStreamPreamble(
	ctx context.Context,
	key streamPreambleKey,
	buffer *streamPreambleBuffer,
	first *schemas.BifrostStreamChunk,
) (chan *schemas.BifrostStreamChunk, <-chan struct{}) {
	wrapped := make(chan *schemas.BifrostStreamChunk, max(cap(key.source), 1))
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(wrapped)
		defer func() {
			buffer.chunks = nil
			buffer.bytes = 0
			streamPreambles.CompareAndDelete(key, buffer)
			// Unblock the producer if cancellation interrupted forwarding.
			for range key.source {
			}
		}()

		send := func(chunk *schemas.BifrostStreamChunk) bool {
			if ctx.Err() != nil {
				return false
			}
			select {
			case wrapped <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for i, chunk := range buffer.chunks {
			if !send(chunk) {
				return
			}
			buffer.chunks[i] = nil
		}
		buffer.chunks = nil
		buffer.bytes = 0
		if first != nil && !send(first) {
			return
		}
		first = nil

		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-key.source:
				if !ok {
					return
				}
				if !send(chunk) {
					return
				}
			}
		}
	}()
	return wrapped, done
}

// CheckStreamPreambleForError checks for errors before meaningful output.
// On success, buffered startup events are replayed in their original order.
// Callers must await drainDone after an error before starting another attempt.
func CheckStreamPreambleForError(
	ctx context.Context,
	requestID string,
	stream chan *schemas.BifrostStreamChunk,
	isPreamble func(*schemas.BifrostStreamChunk) bool,
) (chan *schemas.BifrostStreamChunk, <-chan struct{}, *schemas.BifrostError) {
	if stream == nil {
		done := make(chan struct{})
		close(done)
		return nil, done, nil
	}
	if isPreamble == nil {
		isPreamble = func(*schemas.BifrostStreamChunk) bool { return false }
	}

	key := streamPreambleKey{requestID: requestID, source: stream}
	buffer := &streamPreambleBuffer{}
	streamPreambles.Store(key, buffer)
	release := func() {
		buffer.chunks = nil
		buffer.bytes = 0
		streamPreambles.CompareAndDelete(key, buffer)
	}
	drain := func() <-chan struct{} {
		release()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range stream {
			}
		}()
		return done
	}

	for {
		select {
		case <-ctx.Done():
			err := NewBifrostOperationError(schemas.ErrRequestCancelled, ctx.Err())
			err.StatusCode = schemas.Ptr(499)
			err.Error.Type = schemas.Ptr(schemas.RequestCancelled)
			if ctx.Err() == context.DeadlineExceeded {
				err = NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, ctx.Err())
			} else {
				err.AllowFallbacks = schemas.Ptr(false)
			}
			return nil, drain(), err

		case chunk, ok := <-stream:
			if !ok {
				if len(buffer.chunks) == 0 {
					release()
					done := make(chan struct{})
					close(done)
					return nil, done, nil
				}
				wrapped, done := replayStreamPreamble(ctx, key, buffer, nil)
				return wrapped, done, nil
			}
			if chunk == nil {
				continue
			}
			if err := chunk.BifrostError; err != nil && err.Error != nil &&
				(err.Error.Message != "" || err.Error.Code != nil || err.Error.Type != nil) {
				return nil, drain(), err
			}
			if isPreamble(chunk) && buffer.tryAppend(chunk) {
				continue
			}
			wrapped, done := replayStreamPreamble(ctx, key, buffer, chunk)
			return wrapped, done, nil
		}
	}
}

// CheckFirstStreamChunkForError reads the first chunk from a streaming channel to detect
// errors returned inside HTTP 200 SSE streams (e.g., providers that send rate limit
// errors as SSE events instead of HTTP 429).
//
// If the first chunk is an error, it drains the source channel in the background
// (so the provider goroutine can exit cleanly) and returns the error for synchronous
// handling, enabling retries and fallbacks. The returned drainDone channel is closed
// once the drain completes — callers must wait on it before releasing any resources
// (e.g., plugin pipelines) that the provider goroutine's postHookRunner may still reference.
//
// If the first chunk is valid data, it returns a wrapped channel that re-emits
// the first chunk followed by all remaining chunks from the source. drainDone is
// closed when the wrapper goroutine finishes forwarding the source stream.
//
// If the source channel is closed immediately (empty stream), it returns a
// nil channel with nil error. drainDone is already closed.
//
// The ctx argument cancels the background forwarding goroutine if the consumer
// abandons the returned wrapped channel. On ctx.Done the goroutine drains the
// source stream so the upstream provider's blocked send can exit cleanly.
func CheckFirstStreamChunkForError(
	ctx context.Context,
	stream chan *schemas.BifrostStreamChunk,
) (chan *schemas.BifrostStreamChunk, <-chan struct{}, *schemas.BifrostError) {
	firstChunk, ok := <-stream
	if !ok {
		// Channel closed immediately (empty stream) — return nil so callers
		// can distinguish this from a live stream channel.
		done := make(chan struct{})
		close(done)
		return nil, done, nil
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
			select {
			case wrapped <- chunk:
			case <-ctx.Done():
				// Consumer abandoned the wrapped channel. Drain the source so the
				// provider's blocked send unblocks and its goroutine can exit.
				for range stream {
				}
				return
			}
		}
	}()
	return wrapped, done, nil
}
