package utils

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// PassthroughStreamParams configures StreamPassthrough.
type PassthroughStreamParams struct {
	StatusCode int
	Headers    map[string]string
	Path       string
	// RawRequest is attached to the final chunk only (the streaming accumulator reads it there).
	RawRequest []byte
	// CancellationBody is forwarded to cancellation/timeout handlers.
	CancellationBody []byte
	StartTime        time.Time
	// UseTerminalDetector finalizes the stream early when a terminal marker (finishReason)
	// appears in a framed event — for providers (Gemini/Vertex) that emit it before the HTTP
	// body closes.
	UseTerminalDetector bool
	Logger              schemas.Logger
	// HasUsage is an optional cheap gjson presence check: it returns true only when an event
	// carries a usage field worth parsing. When set, Observe is skipped (no full unmarshal) for
	// events that fail it — the common case on long streams (content deltas, pings). When nil,
	// every event is passed to Observe.
	HasUsage func(event []byte) bool
	// Observe is called once per complete SSE data event (JSON payload) as it streams, and
	// returns the running usage (nil when the event adds nothing). The last non-nil value is
	// attached to the final chunk. Implementations populate usage directly from the event —
	// no full response body is retained.
	Observe func(event []byte) *schemas.BifrostPassthroughUsage
}

// StreamPassthrough runs the shared passthrough streaming loop. It forwards each raw upstream
// chunk to the client unchanged (byte-exact, unbounded — forwarding never depends on usage
// parsing), and in parallel frames complete SSE events into a bounded buffer, feeding each to
// params.Observe to build usage incrementally. On a terminal marker or EOF it emits the final
// chunk carrying RawRequest + the observed usage. No full response body is accumulated.
//
// This owns the idle-timeout wrapper, cancellation hookup, response release, and goroutine.
func StreamPassthrough(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	resp *fasthttp.Response,
	rawBodyStream io.Reader,
	params PassthroughStreamParams,
) chan *schemas.BifrostStreamChunk {
	// Wrap reader with idle timeout to detect stalled streams.
	bodyStream, stopIdleTimeout := NewIdleTimeoutReader(rawBodyStream, rawBodyStream, GetStreamIdleTimeout(ctx), ctx)
	// Cancellation must close the raw stream to unblock reads.
	stopCancellation := SetupStreamCancellation(ctx, rawBodyStream, params.Logger)

	extraFields := schemas.BifrostResponseExtraFields{
		ProviderResponseHeaders: params.Headers,
		PassthroughPath:         params.Path,
	}

	ch := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	go func() {
		defer EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				HandleStreamCancellation(ctx, postHookRunner, ch, params.Logger, postHookSpanFinalizer, params.CancellationBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				HandleStreamTimeout(ctx, postHookRunner, ch, params.Logger, postHookSpanFinalizer, params.CancellationBody)
			}
			close(ch)
		}()
		defer ReleaseStreamingResponse(ctx, resp)
		defer stopIdleTimeout()
		defer stopCancellation()

		var pending bytes.Buffer
		var usage *schemas.BifrostPassthroughUsage

		success := params.StatusCode >= 200 && params.StatusCode < 300

		observe := func(payload []byte) (terminal bool) {
			if len(bytes.TrimSpace(payload)) == 0 {
				return false
			}
			// Cheap gjson gate: only fully parse events that actually carry usage.
			if success && params.Observe != nil && (params.HasUsage == nil || params.HasUsage(payload)) {
				if u := params.Observe(payload); u != nil {
					usage = u
				}
			}
			return params.UseTerminalDetector && isTerminalSSEPayload(payload)
		}

		// drainFrames extracts every complete SSE event currently buffered and observes each.
		// Returns true when a terminal event is seen.
		drainFrames := func() bool {
			for {
				data := pending.Bytes()
				idx, delimLen := findFirstSSEFrameDelimiter(data)
				if idx < 0 {
					break
				}
				frame := append([]byte(nil), data[:idx]...)
				pending.Next(idx + delimLen)
				if observe(extractSSEDataPayload(frame)) {
					return true
				}
			}
			// Bound the buffer. A single SSE event can be large — e.g. an
			// image_generation.completed event carries the full base64 image with `usage` at
			// its tail, so the whole event must be buffered to read usage. Match the native SSE
			// scanner's per-line ceiling (sseMaxBufSize). Only when an undelimited event exceeds
			// that do we drop to the last frame boundary (or reset) to stay bounded.
			if pending.Len() > sseMaxBufSize {
				drain := pending.Bytes()
				if idx, delimLen := findLastSSEFrameDelimiter(drain); idx >= 0 {
					pending.Next(idx + delimLen)
				} else {
					pending.Reset()
				}
			}
			return false
		}

		finalize := func() {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			extraFields.Latency = time.Since(params.StartTime).Milliseconds()
			extraFields.RawRequest = params.RawRequest
			ProcessAndSendResponse(ctx, postHookRunner, &schemas.BifrostResponse{
				PassthroughResponse: &schemas.BifrostPassthroughResponse{
					StatusCode:       params.StatusCode,
					Headers:          params.Headers,
					ExtraFields:      extraFields,
					PassthroughUsage: usage,
				},
			}, ch, postHookSpanFinalizer)
		}

		buf := make([]byte, 4096)
		for {
			n, readErr := bodyStream.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				// Forward the raw chunk to the client unchanged.
				ProcessAndSendResponse(ctx, postHookRunner, &schemas.BifrostResponse{
					PassthroughResponse: &schemas.BifrostPassthroughResponse{
						StatusCode:  params.StatusCode,
						Headers:     params.Headers,
						Body:        chunk,
						ExtraFields: extraFields,
					},
				}, ch, postHookSpanFinalizer)

				pending.Write(chunk)
				if drainFrames() {
					finalize()
					return
				}
			}
			if readErr == io.EOF {
				// Flush a trailing event not terminated by a delimiter.
				if pending.Len() > 0 {
					observe(extractSSEDataPayload(pending.Bytes()))
				}
				finalize()
				return
			}
			if readErr != nil {
				if ctx.Err() != nil {
					return // let defer handle cancel/timeout
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				extraFields.Latency = time.Since(params.StartTime).Milliseconds()
				ProcessAndSendError(ctx, postHookRunner, readErr, ch, params.Logger, postHookSpanFinalizer)
				return
			}
		}
	}()

	return ch
}

// isTerminalSSEPayload reports whether a framed SSE data payload signals stream completion
// via a finishReason/usage terminal marker. ([DONE] is handled by the SSE readers as EOF.)
func isTerminalSSEPayload(payload []byte) bool {
	p := bytes.TrimSpace(payload)
	if len(p) == 0 {
		return false
	}
	return hasFinishReasonMarker(p)
}
