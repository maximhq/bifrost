package utils

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	maxStreamProbeCharacters = 256
	maxStreamProbeChunks     = 512
	maxStreamProbeBytes      = 1 << 20
	minStreamProbeDuration   = 100 * time.Millisecond
	maxStreamPreambleChunks  = 64
	maxStreamPreambleBytes   = 256 * 1024
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
			return nil, drain(), newStreamContextError(ctx)

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

// CheckFirstStreamChunkForError probes a stream before exposing its first chunk.
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
// With a throughput guard, lifecycle and content chunks remain buffered until
// enough output characters arrive, the stream completes, or the probe rejects
// the attempt. Without a guard, a valid first chunk commits immediately. A
// committed stream is returned through a wrapped channel in its original order;
// drainDone closes when the wrapper finishes forwarding the source stream.
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
	guardConfig ...*schemas.StreamThroughputGuardConfig,
) (chan *schemas.BifrostStreamChunk, <-chan struct{}, *schemas.BifrostError) {
	if stream == nil {
		done := make(chan struct{})
		close(done)
		return nil, done, nil
	}
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
		}
	}
	if !ok {
		// Channel closed immediately (empty stream) — return nil so callers
		// can distinguish this from a live stream channel.
		done := make(chan struct{})
		close(done)
		return nil, done, nil
	}

	if err := streamChunkError(firstChunk); err != nil {
		return nil, drainInBackground(stream), err
	}

	if len(guardConfig) == 0 || guardConfig[0] == nil || guardConfig[0].MinimumOutputCharactersPerSecond <= 0 {
		return wrapStream(ctx, stream, []*schemas.BifrostStreamChunk{firstChunk})
	}

	config := guardConfig[0]
	windowSeconds := config.ProbeWindowInSeconds
	if windowSeconds <= 0 {
		windowSeconds = schemas.DefaultStreamThroughputProbeWindowInSeconds
	}
	targetCharacters := int64(config.MinimumOutputCharactersPerSecond) * int64(windowSeconds)
	if targetCharacters > maxStreamProbeCharacters {
		targetCharacters = maxStreamProbeCharacters
	}
	probeDuration := time.Duration(targetCharacters) * time.Second / time.Duration(config.MinimumOutputCharactersPerSecond)
	if probeDuration < minStreamProbeDuration {
		probeDuration = minStreamProbeDuration
	}

	buffered := []*schemas.BifrostStreamChunk{firstChunk}
	characters := streamChunkCharacters(firstChunk)
	bufferedBytes := streamChunkSize(firstChunk)
	if characters >= int(targetCharacters) {
		return wrapStream(ctx, stream, buffered)
	}
	if len(buffered) >= maxStreamProbeChunks || bufferedBytes >= maxStreamProbeBytes {
		return nil, drainInBackground(stream), newStreamProbeBufferError()
	}

	// consume folds one receive into the probe; done reports a final verdict.
	consume := func(chunk *schemas.BifrostStreamChunk, streamOpen bool) (wrapped chan *schemas.BifrostStreamChunk, drainDone <-chan struct{}, err *schemas.BifrostError, done bool) {
		if !streamOpen {
			wrapped, drainDone, err = closedBufferedStream(buffered)
			return wrapped, drainDone, err, true
		}
		if err = streamChunkError(chunk); err != nil {
			return nil, drainInBackground(stream), err, true
		}
		buffered = append(buffered, chunk)
		characters += streamChunkCharacters(chunk)
		bufferedBytes += streamChunkSize(chunk)
		if characters >= int(targetCharacters) {
			wrapped, drainDone, err = wrapStream(ctx, stream, buffered)
			return wrapped, drainDone, err, true
		}
		if len(buffered) >= maxStreamProbeChunks || bufferedBytes >= maxStreamProbeBytes {
			return nil, drainInBackground(stream), newStreamProbeBufferError(), true
		}
		return nil, nil, nil, false
	}
	// consumeReady folds every already-delivered chunk (or a close) before a
	// deadline or cancellation verdict: select picks randomly among ready cases,
	// so without this a completed short stream could be rejected as too slow.
	consumeReady := func() (wrapped chan *schemas.BifrostStreamChunk, drainDone <-chan struct{}, err *schemas.BifrostError, done bool) {
		for {
			select {
			case chunk, streamOpen := <-stream:
				if wrapped, drainDone, err, done = consume(chunk, streamOpen); done {
					return wrapped, drainDone, err, true
				}
			default:
				return nil, nil, nil, false
			}
		}
	}

	timer := time.NewTimer(probeDuration)
	defer timer.Stop()
	for {
		select {
		case chunk, streamOpen := <-stream:
			if wrapped, drainDone, err, done := consume(chunk, streamOpen); done {
				return wrapped, drainDone, err
			}
		case <-ctx.Done():
			if wrapped, drainDone, err, done := consumeReady(); done {
				return wrapped, drainDone, err
			}
			return nil, drainInBackground(stream), newStreamContextError(ctx)
		case <-timer.C:
			if wrapped, drainDone, err, done := consumeReady(); done {
				return wrapped, drainDone, err
			}
			if ctx.Err() != nil {
				return nil, drainInBackground(stream), newStreamContextError(ctx)
			}
			return nil, drainInBackground(stream), newStreamThroughputError(config)
		}
	}
}

func wrapStream(ctx context.Context, stream chan *schemas.BifrostStreamChunk, buffered []*schemas.BifrostStreamChunk) (chan *schemas.BifrostStreamChunk, <-chan struct{}, *schemas.BifrostError) {
	done := make(chan struct{})
	wrapped := make(chan *schemas.BifrostStreamChunk, max(cap(stream), len(buffered), 1))
	for _, chunk := range buffered {
		wrapped <- chunk
	}
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

func closedBufferedStream(buffered []*schemas.BifrostStreamChunk) (chan *schemas.BifrostStreamChunk, <-chan struct{}, *schemas.BifrostError) {
	done := make(chan struct{})
	wrapped := make(chan *schemas.BifrostStreamChunk, len(buffered))
	for _, chunk := range buffered {
		wrapped <- chunk
	}
	close(wrapped)
	close(done)
	return wrapped, done, nil
}

func streamChunkError(chunk *schemas.BifrostStreamChunk) *schemas.BifrostError {
	if chunk == nil || chunk.BifrostError == nil || chunk.BifrostError.Error == nil {
		return nil
	}
	if chunk.BifrostError.Error.Message == "" && chunk.BifrostError.Error.Code == nil && chunk.BifrostError.Error.Type == nil {
		return nil
	}
	return chunk.BifrostError
}

func streamChunkCharacters(chunk *schemas.BifrostStreamChunk) int {
	if chunk == nil {
		return 0
	}
	characters := 0
	if chunk.BifrostTextCompletionResponse != nil {
		for _, choice := range chunk.BifrostTextCompletionResponse.Choices {
			if choice.TextCompletionResponseChoice != nil && choice.Text != nil {
				characters += utf8.RuneCountInString(*choice.Text)
			}
		}
	}
	if chunk.BifrostChatResponse != nil {
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice == nil || choice.Delta == nil {
				continue
			}
			delta := choice.Delta
			for _, text := range []*string{delta.Content, delta.Refusal, delta.Reasoning} {
				if text != nil {
					characters += utf8.RuneCountInString(*text)
				}
			}
			if delta.Reasoning == nil {
				for _, detail := range delta.ReasoningDetails {
					for _, text := range []*string{detail.Summary, detail.Text} {
						if text != nil {
							characters += utf8.RuneCountInString(*text)
						}
					}
				}
			}
			for _, toolCall := range delta.ToolCalls {
				if toolCall.Function.Name != nil {
					characters += utf8.RuneCountInString(*toolCall.Function.Name)
				}
				characters += utf8.RuneCountInString(toolCall.Function.Arguments)
			}
		}
	}
	if response := chunk.BifrostResponsesStreamResponse; response != nil {
		switch response.Type {
		case schemas.ResponsesStreamResponseTypeOutputTextDelta,
			schemas.ResponsesStreamResponseTypeRefusalDelta,
			schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
			schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDelta,
			schemas.ResponsesStreamResponseTypeCustomToolCallInputDelta:
			if response.Delta != nil {
				characters += utf8.RuneCountInString(*response.Delta)
			}
		case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			schemas.ResponsesStreamResponseTypeMCPCallArgumentsDelta:
			if response.Arguments != nil {
				characters += utf8.RuneCountInString(*response.Arguments)
			}
		}
	}
	if response := chunk.BifrostTranscriptionStreamResponse; response != nil && response.Delta != nil {
		characters += utf8.RuneCountInString(*response.Delta)
	}
	return characters
}

func streamChunkSize(chunk *schemas.BifrostStreamChunk) int {
	if chunk == nil {
		return 0
	}
	data, err := chunk.MarshalJSON()
	if err != nil {
		return 0
	}
	return len(data)
}

func newStreamThroughputError(config *schemas.StreamThroughputGuardConfig) *schemas.BifrostError {
	allowFallbacks := true
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(503),
		AllowFallbacks: &allowFallbacks,
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr(schemas.ProviderConnectionFailed),
			Code:    schemas.Ptr(schemas.ErrCodeStreamThroughputBelowMinimum),
			Message: fmt.Sprintf("%s (%d output characters/second)", schemas.ErrProviderStreamThroughput, config.MinimumOutputCharactersPerSecond),
		},
	}
}

func newStreamProbeBufferError() *schemas.BifrostError {
	allowFallbacks := true
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(503),
		AllowFallbacks: &allowFallbacks,
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr(schemas.ProviderConnectionFailed),
			Code:    schemas.Ptr(schemas.ErrCodeStreamThroughputProbeBufferExceeded),
			Message: "stream throughput probe buffer limit reached before the provider rate could be verified",
		},
	}
}
