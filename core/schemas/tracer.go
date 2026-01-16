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
	Model                 string                          // Model used
	Provider              ModelProvider                   // Provider used
	Status                string                          // Status of the stream
	Latency               int64                           // Latency in milliseconds
	TimeToFirstToken      int64                           // Time to first token in milliseconds
	OutputMessage         *ChatMessage                    // Accumulated output message
	OutputMessages        []ResponsesMessage              // For responses API
	TokenUsage            *BifrostLLMUsage                // Token usage
	Cost                  *float64                        // Cost in dollars
	ErrorDetails          *BifrostError                   // Error details if any
	AudioOutput           *BifrostSpeechResponse          // For speech streaming
	TranscriptionOutput   *BifrostTranscriptionResponse   // For transcription streaming
	ImageGenerationOutput *BifrostImageGenerationResponse // For image generation streaming
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
	CreateTrace(parentID string) string

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

	// AddEvent adds a timestamped event to the span.
	// Events represent discrete occurrences during the span's lifetime.
	AddEvent(handle SpanHandle, name string, attrs map[string]any)

	// PopulateLLMRequestAttributes populates all LLM-specific request attributes on the span.
	// This includes model parameters, input messages, temperature, max tokens, etc.
	PopulateLLMRequestAttributes(handle SpanHandle, req *BifrostRequest)

	// PopulateLLMResponseAttributes populates all LLM-specific response attributes on the span.
	// This includes output messages, tokens, usage stats, and error information if present.
	PopulateLLMResponseAttributes(handle SpanHandle, resp *BifrostResponse, err *BifrostError)

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

	// GetAccumulatedChunks returns the accumulated BifrostResponse, TTFT, and chunk count for a deferred span.
	// Returns the built response (with content, tool calls, etc.), time-to-first-token in ms, and total chunk count.
	// Returns nil, 0, 0 if no accumulated data exists.
	GetAccumulatedChunks(traceID string) (response *BifrostResponse, ttftMs int64, chunkCount int)

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
	ProcessStreamingChunk(traceID string, isFinalChunk bool, result *BifrostResponse, err *BifrostError) *StreamAccumulatorResult

	// Stop releases resources associated with the tracer.
	// Should be called during shutdown to stop background goroutines.
	Stop()
}

// NoOpTracer is a tracer that does nothing (default when tracing disabled).
// It satisfies the Tracer interface but performs no actual tracing operations.
type NoOpTracer struct{}

// CreateTrace returns an empty string (no trace created).
func (n *NoOpTracer) CreateTrace(_ string) string { return "" }

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

// AddEvent does nothing.
func (n *NoOpTracer) AddEvent(_ SpanHandle, _ string, _ map[string]any) {}

// PopulateLLMRequestAttributes does nothing.
func (n *NoOpTracer) PopulateLLMRequestAttributes(_ SpanHandle, _ *BifrostRequest) {}

// PopulateLLMResponseAttributes does nothing.
func (n *NoOpTracer) PopulateLLMResponseAttributes(_ SpanHandle, _ *BifrostResponse, _ *BifrostError) {
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
func (n *NoOpTracer) ProcessStreamingChunk(_ string, _ bool, _ *BifrostResponse, _ *BifrostError) *StreamAccumulatorResult {
	return nil
}

// Stop does nothing.
func (n *NoOpTracer) Stop() {}

// DefaultTracer returns a no-op tracer for use when tracing is disabled.
func DefaultTracer() Tracer {
	return &NoOpTracer{}
}

// Ensure NoOpTracer implements Tracer at compile time
var _ Tracer = (*NoOpTracer)(nil)
