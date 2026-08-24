package utils

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	maxStreamProbeCharacters = 256
	maxStreamProbeChunks     = 512
	maxStreamProbeBytes      = 1 << 20
	minStreamProbeDuration   = 100 * time.Millisecond
)

// CheckFirstStreamChunkForError probes a stream before exposing its first chunk.
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
// nil channel with nil error. drainDone is already closed.
//
// The ctx argument cancels the background forwarding goroutine if the consumer
// abandons the returned wrapped channel. On ctx.Done the goroutine drains the
// source stream so the upstream provider's blocked send can exit cleanly.
func CheckFirstStreamChunkForError(
	ctx context.Context,
	stream chan *schemas.BifrostStreamChunk,
	guardConfig ...*schemas.StreamThroughputGuardConfig,
) (chan *schemas.BifrostStreamChunk, <-chan struct{}, *schemas.BifrostError) {
	firstChunk, ok := <-stream
	if !ok {
		// Channel closed immediately (empty stream) — return nil so callers
		// can distinguish this from a live stream channel.
		done := make(chan struct{})
		close(done)
		return nil, done, nil
	}

	if err := streamChunkError(firstChunk); err != nil {
		return nil, drainStream(stream), err
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
		return nil, drainStream(stream), newStreamProbeBufferError()
	}

	timer := time.NewTimer(probeDuration)
	defer timer.Stop()
	for {
		select {
		case chunk, streamOpen := <-stream:
			if !streamOpen {
				return closedBufferedStream(buffered)
			}
			if err := streamChunkError(chunk); err != nil {
				return nil, drainStream(stream), err
			}
			buffered = append(buffered, chunk)
			characters += streamChunkCharacters(chunk)
			bufferedBytes += streamChunkSize(chunk)
			if characters >= int(targetCharacters) {
				return wrapStream(ctx, stream, buffered)
			}
			if len(buffered) >= maxStreamProbeChunks || bufferedBytes >= maxStreamProbeBytes {
				return nil, drainStream(stream), newStreamProbeBufferError()
			}
		case <-timer.C:
			return nil, drainStream(stream), newStreamThroughputError(config)
		case <-ctx.Done():
			return nil, drainStream(stream), &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   ctx.Err(),
				},
			}
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

func drainStream(stream chan *schemas.BifrostStreamChunk) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range stream {
		}
	}()
	return done
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
			Code:    schemas.Ptr("stream_throughput_below_minimum"),
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
			Code:    schemas.Ptr("stream_throughput_probe_buffer_exceeded"),
			Message: "stream throughput probe buffer limit reached before the provider rate could be verified",
		},
	}
}
