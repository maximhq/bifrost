package utils

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ErrStreamFirstTokenTimeout closes the socket of a stream attempt that missed
// its first-token deadline.
var ErrStreamFirstTokenTimeout = errors.New(schemas.ErrStreamFirstTokenTimeout)

// AttemptAbort cuts off one stream attempt that produces no output before its
// first-token deadline, without cancelling the request context the primary,
// retries and fallbacks share. Cancelling that context would end the whole
// request and block fallbacks (RequestCancelled).
//
// Bifrost stores it on the context under BifrostContextKeyStreamAttemptAbort
// for one attempt. The header-wait watcher (contextTransport), the body
// watcher (SetupStreamCancellation), net/http requests (DoHTTPRequest) and the
// first-chunk checks all select on Done. Disarm, called when the first output
// chunk arrives, releases them. Fired and Disarm race on one atomic, so
// exactly one of "timed out" and "first token arrived" wins.
type AttemptAbort struct {
	timeout time.Duration
	state   atomic.Int32 // attemptAbortArmed, attemptAbortFired or attemptAbortDisarmed
	done    chan struct{}
	stopped chan struct{}
	timer   *time.Timer
}

const (
	attemptAbortArmed int32 = iota
	attemptAbortFired
	attemptAbortDisarmed
)

// NewAttemptAbort arms a first-token deadline of d. It returns nil when d <= 0;
// every method is safe on a nil receiver and then never fires.
func NewAttemptAbort(d time.Duration) *AttemptAbort {
	if d <= 0 {
		return nil
	}
	a := &AttemptAbort{
		timeout: d,
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	a.timer = time.AfterFunc(d, a.fire)
	return a
}

func (a *AttemptAbort) fire() {
	if a.state.CompareAndSwap(attemptAbortArmed, attemptAbortFired) {
		close(a.done)
	}
}

// Expire fires the deadline now, as if it had elapsed, so every watcher cuts
// the attempt. A no-op once the deadline fired or was disarmed.
func (a *AttemptAbort) Expire() {
	if a == nil {
		return
	}
	a.timer.Stop()
	a.fire()
}

// Done closes when the deadline fires. A nil receiver returns a nil channel,
// which a select never picks.
func (a *AttemptAbort) Done() <-chan struct{} {
	if a == nil {
		return nil
	}
	return a.done
}

// Stopped closes when Disarm wins, so watchers can stop waiting on Done.
func (a *AttemptAbort) Stopped() <-chan struct{} {
	if a == nil {
		return nil
	}
	return a.stopped
}

// Disarm stops the deadline because the attempt produced output. It returns
// false when the deadline already fired: the attempt must then be treated as
// timed out even if a chunk is in hand. Idempotent.
func (a *AttemptAbort) Disarm() bool {
	if a == nil {
		return true
	}
	if a.state.CompareAndSwap(attemptAbortArmed, attemptAbortDisarmed) {
		a.timer.Stop()
		close(a.stopped)
		return true
	}
	return a.state.Load() == attemptAbortDisarmed
}

// Fired reports whether the deadline fired.
func (a *AttemptAbort) Fired() bool {
	return a != nil && a.state.Load() == attemptAbortFired
}

// Timeout returns the configured first-token deadline.
func (a *AttemptAbort) Timeout() time.Duration {
	if a == nil {
		return 0
	}
	return a.timeout
}

// AttemptAbortFromContext returns the abort handle for the current stream
// attempt, or nil when the attempt has no first-token deadline.
func AttemptAbortFromContext(ctx context.Context) *AttemptAbort {
	if ctx == nil {
		return nil
	}
	abort, _ := ctx.Value(schemas.BifrostContextKeyStreamAttemptAbort).(*AttemptAbort)
	return abort
}

// NewFirstTokenTimeoutError is the error for a stream attempt that missed its
// first-token deadline. IsBifrostError stops the retry loop on the same
// provider, and AllowFallbacks stays unset so the next fallback runs.
func NewFirstTokenTimeoutError(d time.Duration) *schemas.BifrostError {
	err := NewBifrostTimeoutError(fmt.Sprintf("%s of %s", schemas.ErrStreamFirstTokenTimeout, d), ErrStreamFirstTokenTimeout)
	err.Error.Code = new(schemas.FirstTokenTimeoutErrorCode)
	return err
}

const (
	maxStreamPreambleChunks = 64
	maxStreamPreambleBytes  = 256 * 1024
	// response.created and response.in_progress each echo the request's
	// instructions and tools, so a large request passes 256 KB on its second
	// startup event. Under a TTFT deadline a full buffer ends the attempt, so
	// the budget must fit that echo.
	maxStreamPreambleBytesUnderDeadline = 4 * 1024 * 1024
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

// tryAppend returns false when the buffer cannot hold chunk within maxBytes
// or the chunk cap. On false, the chunk remains unbuffered; the caller either
// commits the stream (forwarding it after the buffered prefix) or, under a
// TTFT deadline, ends the attempt.
func (buffer *streamPreambleBuffer) tryAppend(chunk *schemas.BifrostStreamChunk, maxBytes int) bool {
	if len(buffer.chunks)+1 >= maxStreamPreambleChunks {
		return false
	}
	encoded, err := MarshalSorted(chunk)
	if err != nil || len(encoded) >= maxBytes-buffer.bytes {
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

	// A first-token deadline on this attempt fires abort. Startup chunks do not
	// disarm it; only the chunk that commits the stream does.
	abort := AttemptAbortFromContext(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil, drain(), newStreamContextError(ctx)

		case <-abort.Done():
			return nil, drain(), NewFirstTokenTimeoutError(abort.Timeout())

		case chunk, ok := <-stream:
			if !ok {
				if !abort.Disarm() {
					// The deadline fired as the provider closed the stream, which
					// cutting the socket can cause. Not an empty success.
					return nil, drain(), NewFirstTokenTimeoutError(abort.Timeout())
				}
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
			if isPreamble(chunk) {
				if abort == nil {
					if buffer.tryAppend(chunk, maxStreamPreambleBytes) {
						continue
					}
				} else {
					if buffer.tryAppend(chunk, maxStreamPreambleBytesUnderDeadline) {
						continue
					}
					// Committing would disarm the deadline with no output, and
					// buffering on would be unbounded. End the attempt as a TTFT
					// miss now; the next fallback runs.
					abort.Expire()
					return nil, drain(), NewFirstTokenTimeoutError(abort.Timeout())
				}
			}
			if !abort.Disarm() {
				// The deadline fired while this chunk was in flight.
				return nil, drain(), NewFirstTokenTimeoutError(abort.Timeout())
			}
			wrapped, done := replayStreamPreamble(ctx, key, buffer, chunk)
			return wrapped, done, nil
		}
	}
}

// newStreamContextError maps a done request context onto the error shape the
// retry loop already terminates on: RequestCancelled (499, fallbacks off) or
// RequestTimedOut (504, default fallback eligibility).
func newStreamContextError(ctx context.Context) *schemas.BifrostError {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, ctx.Err())
	}
	err := NewBifrostOperationError(schemas.ErrRequestCancelled, ctx.Err())
	err.StatusCode = new(499)
	err.Error.Type = new(schemas.RequestCancelled)
	err.AllowFallbacks = new(false)
	return err
}

// drainInBackground consumes stream until the producer closes it so a
// provider goroutine blocked on send can exit. The returned channel closes
// when the drain completes.
func drainInBackground(stream chan *schemas.BifrostStreamChunk) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range stream {
		}
	}()
	return done
}

// CheckFirstStreamChunkForError reads the first chunk from a streaming channel to detect
// errors returned inside HTTP 200 SSE streams (e.g., providers that send rate limit
// errors as SSE events instead of HTTP 429).
//
// If ctx ends before the first chunk arrives, it returns a RequestCancelled (499)
// or RequestTimedOut (504) error immediately and drains the source in the
// background, so the calling worker is released instead of waiting out the
// provider's stream idle timeout (maximhq/bifrost#6974). A chunk the producer
// already delivered takes precedence over ctx.
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
// nil channel with nil error. drainDone is already closed. A nil source is
// treated the same way, matching CheckStreamPreambleForError, so no goroutine
// is ever parked on a channel that can never be ready or closed.
//
// The ctx argument cancels the background forwarding goroutine if the consumer
// abandons the returned wrapped channel. On ctx.Done the goroutine drains the
// source stream so the upstream provider's blocked send can exit cleanly.
func CheckFirstStreamChunkForError(
	ctx context.Context,
	stream chan *schemas.BifrostStreamChunk,
) (chan *schemas.BifrostStreamChunk, <-chan struct{}, *schemas.BifrostError) {
	if stream == nil {
		done := make(chan struct{})
		close(done)
		return nil, done, nil
	}
	abort := AttemptAbortFromContext(ctx)
	var firstChunk *schemas.BifrostStreamChunk
	var ok bool
	select {
	case firstChunk, ok = <-stream:
		// A chunk the producer already delivered wins over ctx: a provider
		// goroutine that saw the cancellation itself emits a richer
		// RequestCancelled chunk (billed usage, raw request) through
		// HandleStreamCancellation.
	default:
		select {
		case firstChunk, ok = <-stream:
		case <-ctx.Done():
			// The producer accepted the request but has emitted nothing and
			// the request is gone. Release the worker now instead of waiting
			// out the provider's stream idle timeout. Drain in the background
			// so the producer's eventual send and close still complete.
			return nil, drainInBackground(stream), newStreamContextError(ctx)
		case <-abort.Done():
			return nil, drainInBackground(stream), NewFirstTokenTimeoutError(abort.Timeout())
		}
	}
	if !ok {
		if !abort.Disarm() {
			// The deadline fired as the provider closed the stream, which
			// cutting the socket can cause. Not an empty success.
			return nil, drainInBackground(stream), NewFirstTokenTimeoutError(abort.Timeout())
		}
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
		return nil, drainInBackground(stream), firstChunk.BifrostError
	}

	if !abort.Disarm() {
		// The deadline fired while this chunk was in flight.
		return nil, drainInBackground(stream), NewFirstTokenTimeoutError(abort.Timeout())
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
