// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"context"
	"time"
)

// SpanHandle is an opaque handle to a span, implementation-specific.
// Different Tracer implementations can use their own concrete types.
type SpanHandle interface{}

// StreamAccumulatorResult contains the accumulated data from streaming chunks.
// This is the return type for tracer's streaming accumulation methods.
type StreamAccumulatorResult struct {
	RequestID             string                          // Request ID
	RequestedModel        string                          // Original model requested by the caller
	ResolvedModel         string                          // Actual model used by the provider (equals RequestedModel when no alias mapping exists)
	Provider              ModelProvider                   // Provider used
	Status                string                          // Status of the stream
	Latency               int64                           // Latency in milliseconds
	TimeToFirstToken      int64                           // Time to first token in milliseconds
	OutputMessage         *ChatMessage                    // Accumulated output message
	OutputMessages        []ResponsesMessage              // For responses API
	TokenUsage            *BifrostLLMUsage                // Token usage
	Cost                  *float64                        // Cost in dollars
	CacheDebug            *BifrostCacheDebug              // Semantic cache debug info if available
	GuardrailDebug        *BifrostGuardrailDebug          // Guardrail debug info if available
	ErrorDetails          *BifrostError                   // Error details if any
	AudioOutput           *BifrostSpeechResponse          // For speech streaming
	TranscriptionOutput   *BifrostTranscriptionResponse   // For transcription streaming
	ImageGenerationOutput *BifrostImageGenerationResponse // For image generation streaming
	PassthroughOutput     *BifrostPassthroughResponse     // For passthrough streaming
	FinishReason          *string                         // Finish reason
	RawResponse           *string                         // Raw response
	RawRequest            interface{}                     // Raw request
}

// Tracer defines the interface for distributed tracing in Bifrost.
// Implementations can be injected via BifrostConfig to enable automatic instrumentation.
// The interface is designed to be minimal and implementation-agnostic.
type Tracer interface {
	// CreateTrace creates a new trace with optional parent ID and returns the trace ID.
	// The parentID can be extracted from W3C traceparent headers for distributed tracing.
	// The requestID is optional and can be used to identify the request.
	CreateTrace(parentID string, requestID ...string) string

	// EndTrace completes a trace and returns the trace data for observation/export.
	// After this call, the trace is removed from active tracking and returned for cleanup.
	// Returns nil if trace not found.
	EndTrace(traceID string) *Trace

	// StartSpan creates a new span as a child of the current span in context.
	// Returns updated context with new span and a handle for the span.
	// The context should be used for subsequent operations to maintain span hierarchy.
	StartSpan(ctx context.Context, name string, kind SpanKind) (context.Context, SpanHandle)

	// EndSpan completes a span with status and optional message.
	// Should be called when the operation represented by the span is complete.
	EndSpan(handle SpanHandle, status SpanStatus, statusMsg string)

	// SetAttribute sets an attribute on the span.
	// Attributes provide additional context about the operation.
	SetAttribute(handle SpanHandle, key string, value any)

	// GetSpanHandleByID retrieves a span handle for the given trace and span ID.
	// If spanID is nil, returns a handle for the trace's root span.
	GetSpanHandleByID(traceID string, spanID *string) SpanHandle

	// AddEvent adds a timestamped event to the span.
	// Events represent discrete occurrences during the span's lifetime.
	AddEvent(handle SpanHandle, name string, attrs map[string]any)

	// PopulateLLMRequestAttributes populates all LLM-specific request attributes on the span.
	// This includes model parameters, input messages, temperature, max tokens, etc.
	PopulateLLMRequestAttributes(handle SpanHandle, req *BifrostRequest)

	// PopulateLLMResponseAttributes populates all LLM-specific response attributes on the span.
	// This includes output messages, tokens, usage stats, and error information if present.
	PopulateLLMResponseAttributes(ctx *BifrostContext, handle SpanHandle, resp *BifrostResponse, err *BifrostError)

	// StoreDeferredSpan stores a span handle for later completion (used for streaming requests).
	// The span handle is stored keyed by trace ID so it can be retrieved when the stream completes.
	StoreDeferredSpan(traceID string, handle SpanHandle)

	// GetDeferredSpanHandle retrieves a deferred span handle by trace ID.
	// Returns nil if no deferred span exists for the given trace ID.
	GetDeferredSpanHandle(traceID string) SpanHandle

	// ClearDeferredSpan removes the deferred span handle for a trace ID.
	// Should be called after the deferred span has been completed.
	ClearDeferredSpan(traceID string)

	// GetDeferredSpanID returns the span ID for the deferred span.
	// Returns empty string if no deferred span exists.
	GetDeferredSpanID(traceID string) string

	// AddStreamingChunk accumulates a streaming chunk for the deferred span.
	// Pass the full BifrostResponse to capture content, tool calls, reasoning, etc.
	// This is called for each streaming chunk to build up the complete response.
	AddStreamingChunk(traceID string, response *BifrostResponse)

	// GetAccumulatedChunks returns the accumulated response, TTFT, and chunk count for a deferred span.
	// The response is built from the streaming accumulator during the final ProcessStreamingChunk call.
	// Returns nil response if no plugin has called ProcessStreamingChunk (callers should nil-check).
	// Returns nil, 0, 0 if no accumulated data exists.
	GetAccumulatedChunks(traceID string) (response *BifrostResponse, ttftNs int64, chunkCount int)

	// CreateStreamAccumulator creates a new stream accumulator for the given trace ID.
	// This should be called at the start of a streaming request.
	CreateStreamAccumulator(traceID string, startTime time.Time)

	// CleanupStreamAccumulator removes the stream accumulator for the given trace ID.
	// This should be called after the streaming request is complete.
	CleanupStreamAccumulator(traceID string)

	// ProcessStreamingChunk processes a streaming chunk and accumulates it.
	// Returns the accumulated result. IsFinal will be true when the stream is complete.
	// This method is used by plugins to access accumulated streaming data.
	// The ctx parameter must contain the stream end indicator for proper final chunk detection.
	ProcessStreamingChunk(ctx *BifrostContext, traceID string, isFinalChunk bool, result *BifrostResponse, err *BifrostError) *StreamAccumulatorResult

	// PauseStream marks the streaming response identified by traceID as paused.
	// While paused, post-processed chunks are buffered (not delivered) but plugin
	// hooks continue to fire. Idempotent. No-op if no active stream is found.
	PauseStream(traceID string)

	// ResumeStream resumes a previously paused stream. Buffered chunks are flushed
	// to the client in order, then live streaming continues. Idempotent.
	ResumeStream(traceID string)

	// EndStream terminates the stream. If err is non-nil, it is delivered to the
	// client as a final error chunk after any buffered chunks are flushed. After
	// EndStream, all further chunks for this stream are dropped (post-hooks still
	// run but no client delivery happens). Idempotent.
	EndStream(traceID string, err *BifrostError)

	// WaitForFlusher blocks until the gate flusher goroutine for traceID has
	// fully drained and exited. Provider stream goroutines call this from
	// their deferred close so the consumer-facing channel is not closed while
	// the gate still has buffered chunks pending delivery (e.g. a stream that
	// was paused by a plugin and not yet resumed). Returns immediately when no
	// flusher is active.
	WaitForFlusher(traceID string)

	// IsStreamEnded reports whether the gate for traceID is in the Ended
	// state. Returns false if no accumulator exists for traceID.
	IsStreamEnded(traceID string) bool

	// IsStreamPaused reports whether the gate for traceID is currently
	// Paused. Returns false if no accumulator exists for traceID.
	IsStreamPaused(traceID string) bool

	// GetAccumulatedResponse returns the *current* accumulated response
	// snapshot for traceID — built on demand from chunks accumulated so far,
	// not from the post-final-chunk stored copy. Useful for plugins that need
	// to inspect the assembled output mid-stream (e.g. while paused). Returns
	// nil if no accumulator exists, no chunks have been accumulated yet, or
	// the stream type cannot be determined.
	GetAccumulatedResponse(traceID string) *BifrostResponse

	// GateSend is called by stream producers (provider helpers) instead of writing
	// directly to the response channel. It implements the pause/resume/end gate:
	//   - Active state: chunk is forwarded to ch (with ctx.Done() guard)
	//   - Paused state: chunk is buffered for later replay
	//   - Ended state:  chunk is dropped
	// Final chunks (isFinal) and hard provider errors (isHardErr) bypass the gate
	// and force-flush + transition to Ended.
	// Returns true if the chunk was handled (delivered or buffered), false if the
	// caller should stop sending (ctx done or stream ended).
	GateSend(traceID string, chunk *BifrostStreamChunk, isFinal, isHardErr bool, ch chan *BifrostStreamChunk, ctx *BifrostContext) bool

	// AttachPluginLogs appends plugin log entries to the trace identified by traceID.
	// Thread-safe. Should be called after plugin hooks complete, before trace completion.
	AttachPluginLogs(traceID string, logs []PluginLogEntry)

	// SetTraceRedactionReplacements stores phase-scoped connector-facing replacements on a trace.
	SetTraceRedactionReplacements(traceID string, phase RedactionPhase, replacements map[string]string)

	// CompleteAndFlushTrace ends a trace, exports it to observability plugins, and
	// releases the trace resources. Used by transports that bypass normal HTTP trace completion.
	CompleteAndFlushTrace(traceID string)

	// Stop releases resources associated with the tracer.
	// Should be called during shutdown to stop background goroutines.
	Stop()
}

// NoOpTracer is a tracer that does nothing (default when tracing disabled).
// It satisfies the Tracer interface but performs no actual tracing operations.
type NoOpTracer struct{}

// CreateTrace returns an empty string (no trace created).
func (n *NoOpTracer) CreateTrace(_ string, _ ...string) string { return "" }

// EndTrace returns nil (no trace to end).
func (n *NoOpTracer) EndTrace(_ string) *Trace { return nil }

// StartSpan returns the context unchanged and a nil handle.
func (n *NoOpTracer) StartSpan(ctx context.Context, _ string, _ SpanKind) (context.Context, SpanHandle) {
	return ctx, nil
}

// EndSpan does nothing.
func (n *NoOpTracer) EndSpan(_ SpanHandle, _ SpanStatus, _ string) {}

// SetAttribute does nothing.
func (n *NoOpTracer) SetAttribute(_ SpanHandle, _ string, _ any) {}

// GetSpanHandleByID returns nil.
func (n *NoOpTracer) GetSpanHandleByID(_ string, _ *string) SpanHandle { return nil }

// AddEvent does nothing.
func (n *NoOpTracer) AddEvent(_ SpanHandle, _ string, _ map[string]any) {}

// PopulateLLMRequestAttributes does nothing.
func (n *NoOpTracer) PopulateLLMRequestAttributes(_ SpanHandle, _ *BifrostRequest) {}

// PopulateLLMResponseAttributes does nothing.
func (n *NoOpTracer) PopulateLLMResponseAttributes(_ *BifrostContext, _ SpanHandle, _ *BifrostResponse, _ *BifrostError) {
}

// StoreDeferredSpan does nothing.
func (n *NoOpTracer) StoreDeferredSpan(_ string, _ SpanHandle) {}

// GetDeferredSpanHandle returns nil.
func (n *NoOpTracer) GetDeferredSpanHandle(_ string) SpanHandle { return nil }

// ClearDeferredSpan does nothing.
func (n *NoOpTracer) ClearDeferredSpan(_ string) {}

// GetDeferredSpanID returns empty string.
func (n *NoOpTracer) GetDeferredSpanID(_ string) string { return "" }

// AddStreamingChunk does nothing.
func (n *NoOpTracer) AddStreamingChunk(_ string, _ *BifrostResponse) {}

// GetAccumulatedChunks returns nil, 0, 0.
func (n *NoOpTracer) GetAccumulatedChunks(_ string) (*BifrostResponse, int64, int) { return nil, 0, 0 }

// CreateStreamAccumulator does nothing.
func (n *NoOpTracer) CreateStreamAccumulator(_ string, _ time.Time) {}

// CleanupStreamAccumulator does nothing.
func (n *NoOpTracer) CleanupStreamAccumulator(_ string) {}

// ProcessStreamingChunk returns nil.
func (n *NoOpTracer) ProcessStreamingChunk(_ *BifrostContext, _ string, _ bool, _ *BifrostResponse, _ *BifrostError) *StreamAccumulatorResult {
	return nil
}

// PauseStream does nothing.
func (n *NoOpTracer) PauseStream(_ string) {}

// ResumeStream does nothing.
func (n *NoOpTracer) ResumeStream(_ string) {}

// EndStream does nothing.
func (n *NoOpTracer) EndStream(_ string, _ *BifrostError) {}

// WaitForFlusher does nothing — NoOpTracer has no gate or flusher.
func (n *NoOpTracer) WaitForFlusher(_ string) {}

// IsStreamEnded returns false — NoOpTracer has no gate state.
func (n *NoOpTracer) IsStreamEnded(_ string) bool { return false }

// IsStreamPaused returns false — NoOpTracer has no gate state.
func (n *NoOpTracer) IsStreamPaused(_ string) bool { return false }

// GetAccumulatedResponse returns nil — NoOpTracer has no accumulator.
func (n *NoOpTracer) GetAccumulatedResponse(_ string) *BifrostResponse { return nil }

// GateSend forwards the chunk directly to the channel with ctx.Done() guard.
// NoOpTracer has no gate state, so this is a pure passthrough. Recovers from
// "send on closed channel" so a closed consumer cannot crash the producer.
func (n *NoOpTracer) GateSend(_ string, chunk *BifrostStreamChunk, _ bool, _ bool, ch chan *BifrostStreamChunk, ctx *BifrostContext) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if ctx == nil {
		ch <- chunk
		return true
	}
	select {
	case ch <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

// AttachPluginLogs does nothing.
func (n *NoOpTracer) AttachPluginLogs(_ string, _ []PluginLogEntry) {}

// SetTraceRedactionReplacements does nothing.
func (n *NoOpTracer) SetTraceRedactionReplacements(_ string, _ RedactionPhase, _ map[string]string) {}

// CompleteAndFlushTrace does nothing.
func (n *NoOpTracer) CompleteAndFlushTrace(_ string) {}

// Stop does nothing.
func (n *NoOpTracer) Stop() {}

// DefaultTracer returns a no-op tracer for use when tracing is disabled.
//
// All Tracer-mediated features are inert under DefaultTracer: trace/span
// creation, accumulator, deferred spans, AND the streaming pause/resume/end
// gate. Callers who need real gate behavior (chunk buffering on pause,
// in-order replay on resume, terminal-error delivery on end) MUST inject a
// real Tracer via the Bifrost config — typically `framework/streaming/Accumulator`,
// which is what production deployments wire in. `core/schemas` cannot
// import `framework/streaming` (would be a circular dep), so the gate impl
// cannot live here. This is the same fall-back contract every other Tracer
// feature follows when tracing is disabled.
func DefaultTracer() Tracer {
	return &NoOpTracer{}
}

// Ensure NoOpTracer implements Tracer at compile time
var _ Tracer = (*NoOpTracer)(nil)
