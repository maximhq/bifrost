// Package tracing provides distributed tracing infrastructure for Bifrost
package tracing

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/streaming"
)

// Tracer implements schemas.Tracer using TraceStore.
// It provides the bridge between the core Tracer interface and the
// framework's TraceStore implementation.
// It also embeds a streaming.Accumulator for centralized streaming chunk accumulation.
type Tracer struct {
	store          *TraceStore
	accumulator    *streaming.Accumulator
	pricingManager *modelcatalog.ModelCatalog
}

// NewTracer creates a new Tracer wrapping the given TraceStore.
// The accumulator is embedded for centralized streaming chunk accumulation.
// The pricingManager is used for cost calculation in span attributes.
func NewTracer(store *TraceStore, pricingManager *modelcatalog.ModelCatalog, logger schemas.Logger) *Tracer {
	return &Tracer{
		store:          store,
		accumulator:    streaming.NewAccumulator(pricingManager, logger),
		pricingManager: pricingManager,
	}
}

// CreateTrace creates a new trace with optional parent ID and returns the trace ID.
func (t *Tracer) CreateTrace(parentID string) string {
	return t.store.CreateTrace(parentID)
}

// EndTrace completes a trace and returns the trace data for observation/export.
// The returned trace should be released after use by calling ReleaseTrace.
func (t *Tracer) EndTrace(traceID string) *schemas.Trace {
	trace := t.store.CompleteTrace(traceID)
	if trace == nil {
		return nil
	}
	// Note: Caller is responsible for releasing the trace after plugin processing
	// by calling ReleaseTrace on the store or letting GC handle it
	return trace
}

// ReleaseTrace returns the trace to the pool for reuse.
// Should be called after EndTrace when the trace data is no longer needed.
func (t *Tracer) ReleaseTrace(trace *schemas.Trace) {
	t.store.ReleaseTrace(trace)
}

// spanHandle is the concrete implementation of schemas.SpanHandle for Tracer.
// It contains the trace and span IDs needed to reference the span in the store.
type spanHandle struct {
	traceID string
	spanID  string
}

// StartSpan creates a new span as a child of the current span in context.
// It reads the trace ID and parent span ID from context, creates the span,
// and returns an updated context with the new span ID.
//
// Parent span resolution order:
// 1. BifrostContextKeySpanID - existing span in this service (for child spans)
// 2. BifrostContextKeyParentSpanID - incoming parent from W3C traceparent (for root spans)
// 3. No parent - creates a root span with no parent
func (t *Tracer) StartSpan(ctx context.Context, name string, kind schemas.SpanKind) (context.Context, schemas.SpanHandle) {
	traceID := GetTraceID(ctx)
	if traceID == "" {
		return ctx, nil
	}

	// Get parent span ID from context - first check for existing span in this service
	parentSpanID, _ := ctx.Value(schemas.BifrostContextKeySpanID).(string)

	// If no existing span, check for incoming parent span ID from W3C traceparent header
	// This links the root span of this service to the upstream service's span
	if parentSpanID == "" {
		parentSpanID, _ = ctx.Value(schemas.BifrostContextKeyParentSpanID).(string)
	}

	var span *schemas.Span
	if parentSpanID != "" {
		span = t.store.StartChildSpan(traceID, parentSpanID, name, kind)
	} else {
		span = t.store.StartSpan(traceID, name, kind)
	}
	if span == nil {
		return ctx, nil
	}
	// Update context with new span ID
	newCtx := context.WithValue(ctx, schemas.BifrostContextKeySpanID, span.SpanID)
	return newCtx, &spanHandle{traceID: traceID, spanID: span.SpanID}
}

// EndSpan completes a span with the given status and message.
func (t *Tracer) EndSpan(handle schemas.SpanHandle, status schemas.SpanStatus, statusMsg string) {
	h, ok := handle.(*spanHandle)
	if !ok || h == nil {
		return
	}
	t.store.EndSpan(h.traceID, h.spanID, status, statusMsg, nil)
}

// SetAttribute sets an attribute on the span identified by the handle.
func (t *Tracer) SetAttribute(handle schemas.SpanHandle, key string, value any) {
	h, ok := handle.(*spanHandle)
	if !ok || h == nil {
		return
	}
	trace := t.store.GetTrace(h.traceID)
	if trace == nil {
		return
	}
	span := trace.GetSpan(h.spanID)
	if span != nil {
		span.SetAttribute(key, value)
	}
}

// AddEvent adds a timestamped event to the span identified by the handle.
func (t *Tracer) AddEvent(handle schemas.SpanHandle, name string, attrs map[string]any) {
	h, ok := handle.(*spanHandle)
	if !ok || h == nil {
		return
	}
	trace := t.store.GetTrace(h.traceID)
	if trace == nil {
		return
	}
	span := trace.GetSpan(h.spanID)
	if span != nil {
		span.AddEvent(schemas.SpanEvent{
			Name:       name,
			Timestamp:  time.Now(),
			Attributes: attrs,
		})
	}
}

// PopulateLLMRequestAttributes populates all LLM-specific request attributes on the span.
func (t *Tracer) PopulateLLMRequestAttributes(handle schemas.SpanHandle, req *schemas.BifrostRequest) {
	h, ok := handle.(*spanHandle)
	if !ok || h == nil || req == nil {
		return
	}
	trace := t.store.GetTrace(h.traceID)
	if trace == nil {
		return
	}
	span := trace.GetSpan(h.spanID)
	if span == nil {
		return
	}

	for k, v := range PopulateRequestAttributes(req) {
		span.SetAttribute(k, v)
	}
}

// PopulateLLMResponseAttributes populates all LLM-specific response attributes on the span.
func (t *Tracer) PopulateLLMResponseAttributes(handle schemas.SpanHandle, resp *schemas.BifrostResponse, err *schemas.BifrostError) {
	h, ok := handle.(*spanHandle)
	if !ok || h == nil {
		return
	}
	trace := t.store.GetTrace(h.traceID)
	if trace == nil {
		return
	}
	span := trace.GetSpan(h.spanID)
	if span == nil {
		return
	}	
	for k, v := range PopulateResponseAttributes(resp) {
		span.SetAttribute(k, v)
	}
	for k, v := range PopulateErrorAttributes(err) {
		span.SetAttribute(k, v)
	}
	// Populate cost attribute using pricing manager
	if t.pricingManager != nil && resp != nil {
		cost := t.pricingManager.CalculateCostWithCacheDebug(resp)
		span.SetAttribute(schemas.AttrUsageCost, cost)
	}
}

// StoreDeferredSpan stores a span handle for later completion (used for streaming requests).
// The span handle is stored keyed by trace ID so it can be retrieved when the stream completes.
func (t *Tracer) StoreDeferredSpan(traceID string, handle schemas.SpanHandle) {
	h, ok := handle.(*spanHandle)
	if !ok || h == nil {
		return
	}
	t.store.StoreDeferredSpan(traceID, h.spanID)
}

// GetDeferredSpanHandle retrieves a deferred span handle by trace ID.
// Returns nil if no deferred span exists for the given trace ID.
func (t *Tracer) GetDeferredSpanHandle(traceID string) schemas.SpanHandle {
	info := t.store.GetDeferredSpan(traceID)
	if info == nil {
		return nil
	}
	return &spanHandle{traceID: traceID, spanID: info.SpanID}
}

// ClearDeferredSpan removes the deferred span handle for a trace ID.
// Should be called after the deferred span has been completed.
func (t *Tracer) ClearDeferredSpan(traceID string) {
	t.store.ClearDeferredSpan(traceID)
}

// GetDeferredSpanID returns the span ID for the deferred span.
// Returns empty string if no deferred span exists.
func (t *Tracer) GetDeferredSpanID(traceID string) string {
	info := t.store.GetDeferredSpan(traceID)
	if info == nil {
		return ""
	}
	return info.SpanID
}

// AddStreamingChunk tracks TTFT and chunk count for the deferred span.
// Chunk contents are no longer stored here; full content accumulation is handled
// by the embedded streaming.Accumulator (via ProcessStreamingChunk) for plugins.
func (t *Tracer) AddStreamingChunk(traceID string, response *schemas.BifrostResponse) {
	if traceID == "" || response == nil {
		return
	}
	t.store.AppendStreamingChunk(traceID, response)
}

// GetAccumulatedChunks returns TTFT and chunk count for the deferred span.
// The response is always nil — span attributes are populated from the final chunk
// in completeDeferredSpan. Full content accumulation for plugins is handled by
// the embedded streaming.Accumulator via ProcessStreamingChunk.
func (t *Tracer) GetAccumulatedChunks(traceID string) (*schemas.BifrostResponse, int64, int) {
	ttftMs, chunkCount := t.store.GetAccumulatedData(traceID)
	return nil, ttftMs, chunkCount
}

// CreateStreamAccumulator creates a new stream accumulator for the given trace ID.
// This should be called at the start of a streaming request.
func (t *Tracer) CreateStreamAccumulator(traceID string, startTime time.Time) {
	if traceID == "" || t.accumulator == nil {
		return
	}
	t.accumulator.CreateStreamAccumulator(traceID, startTime)
}

// CleanupStreamAccumulator removes the stream accumulator for the given trace ID.
// This should be called after the streaming request is complete.
func (t *Tracer) CleanupStreamAccumulator(traceID string) {
	if traceID == "" || t.accumulator == nil {
		if t.store != nil && t.store.logger != nil {
			t.store.logger.Error("traceID or accumulator is nil in CleanupStreamAccumulator")
		}
		return
	}
	if err := t.accumulator.CleanupStreamAccumulator(traceID); err != nil {
		if t.store != nil && t.store.logger != nil {
			t.store.logger.Error("error in CleanupStreamAccumulator: %v", err)
		}
	}
}

// ProcessStreamingChunk processes a streaming chunk and accumulates it.
// Returns the accumulated result. IsFinal will be true when the stream is complete.
// This method is used by plugins to access accumulated streaming data.
// The ctx parameter must contain the stream end indicator for proper final chunk detection.
func (t *Tracer) ProcessStreamingChunk(traceID string, isFinalChunk bool, result *schemas.BifrostResponse, err *schemas.BifrostError) *schemas.StreamAccumulatorResult {
	if traceID == "" || t.accumulator == nil {
		return nil
	}

	// Create a new context for accumulator that sets the traceID as the accumulator lookup ID.
	accumCtx := schemas.NewBifrostContext(context.Background(), time.Time{})
	accumCtx.SetValue(schemas.BifrostContextKeyAccumulatorID, traceID)
	accumCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, isFinalChunk)

	processedResp, processErr := t.accumulator.ProcessStreamingResponse(accumCtx, result, err)
	if processErr != nil || processedResp == nil {
		return nil
	}

	// Convert ProcessedStreamResponse to StreamAccumulatorResult
	accResult := &schemas.StreamAccumulatorResult{
		RequestID: processedResp.RequestID,
		Model:     processedResp.Model,
		Provider:  processedResp.Provider,
	}

	if processedResp.Data != nil {
		accResult.Status = processedResp.Data.Status
		accResult.Latency = processedResp.Data.Latency
		accResult.TimeToFirstToken = processedResp.Data.TimeToFirstToken
		accResult.OutputMessage = processedResp.Data.OutputMessage
		accResult.OutputMessages = processedResp.Data.OutputMessages
		accResult.TokenUsage = processedResp.Data.TokenUsage
		accResult.Cost = processedResp.Data.Cost
		accResult.ErrorDetails = processedResp.Data.ErrorDetails
		accResult.AudioOutput = processedResp.Data.AudioOutput
		accResult.TranscriptionOutput = processedResp.Data.TranscriptionOutput
		accResult.ImageGenerationOutput = processedResp.Data.ImageGenerationOutput
		accResult.FinishReason = processedResp.Data.FinishReason
		accResult.RawResponse = processedResp.Data.RawResponse

		if (accResult.Cost == nil || *accResult.Cost == 0.0) && accResult.TokenUsage != nil && accResult.TokenUsage.Cost != nil {
			accResult.Cost = &accResult.TokenUsage.Cost.TotalCost
		}
	}

	if processedResp.RawRequest != nil {
		accResult.RawRequest = *processedResp.RawRequest
	}

	return accResult
}

// GetAccumulator returns the embedded streaming accumulator.
// This is useful for plugins that need direct access to accumulator methods.
func (t *Tracer) GetAccumulator() *streaming.Accumulator {
	return t.accumulator
}

// Stop stops the tracer and releases its resources.
// This stops the internal TraceStore's cleanup goroutine.
func (t *Tracer) Stop() {
	if t.store != nil {
		t.store.Stop()
	}
	if t.accumulator != nil {
		t.accumulator.Cleanup()
	}
}

// Ensure Tracer implements schemas.Tracer at compile time
var _ schemas.Tracer = (*Tracer)(nil)
