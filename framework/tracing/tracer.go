// Package tracing provides distributed tracing infrastructure for Bifrost
package tracing

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
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
	store             *TraceStore
	accumulator       *streaming.Accumulator
	pricingManager    *modelcatalog.ModelCatalog
	logger            schemas.Logger
	obsPlugins        atomic.Pointer[[]schemas.ObservabilityPlugin]
	cachedHdrPatterns atomic.Pointer[[]string]
	flushWG           sync.WaitGroup
}

// NewTracer creates a new Tracer wrapping the given TraceStore.
// The accumulator is embedded for centralized streaming chunk accumulation.
// The pricingManager is used for cost calculation in span attributes.
func NewTracer(store *TraceStore, pricingManager *modelcatalog.ModelCatalog, logger schemas.Logger) *Tracer {
	return &Tracer{
		store:          store,
		accumulator:    streaming.NewAccumulator(pricingManager, logger),
		pricingManager: pricingManager,
		logger:         logger,
		obsPlugins:     atomic.Pointer[[]schemas.ObservabilityPlugin]{},
	}
}

// SetObservabilityPlugins updates the plugins that receive completed traces.
// It also precomputes the deduplicated, normalized union of request-header patterns
// requested by those plugins so the per-request capture path is a single atomic load.
func (t *Tracer) SetObservabilityPlugins(obsPlugins []schemas.ObservabilityPlugin) {
	if t == nil {
		return
	}
	t.obsPlugins.Store(&obsPlugins)

	seen := make(map[string]struct{})
	var patterns []string
	for _, plugin := range obsPlugins {
		if w, ok := plugin.(interface{ RequestHeaderPatterns() []string }); ok {
			for _, p := range w.RequestHeaderPatterns() {
				normalized := strings.ToLower(strings.TrimSpace(p))
				if normalized == "" {
					continue
				}
				if _, exists := seen[normalized]; !exists {
					seen[normalized] = struct{}{}
					patterns = append(patterns, normalized)
				}
			}
		}
	}
	t.cachedHdrPatterns.Store(&patterns)
}

// ShouldCaptureRequestHeaders reports whether any observability plugin has opted into
// request-header capture (by implementing RequestHeaderPatterns). Derived from the cached
// pattern union computed in SetObservabilityPlugins, so there is no per-request recompute.
func (t *Tracer) ShouldCaptureRequestHeaders() bool {
	cached := t.cachedHdrPatterns.Load()
	return cached != nil && len(*cached) > 0
}

// CollectRequestHeaderPatterns returns the deduplicated union of header patterns
// requested by all observability plugins. The middleware uses this to capture only
// matched headers onto the trace, keeping the trace lean. The union is precomputed in
// SetObservabilityPlugins; this is a single atomic load.
func (t *Tracer) CollectRequestHeaderPatterns() []string {
	cached := t.cachedHdrPatterns.Load()
	if cached == nil {
		return nil
	}
	return *cached
}

// SetTraceRequestHeaders filters the given request headers down to the union of
// patterns requested by observability plugins and stores the matched subset on the
// trace. Header keys are expected to be lowercased by the caller.
func (t *Tracer) SetTraceRequestHeaders(traceID string, headers map[string]string) {
	if len(headers) == 0 {
		return
	}
	patterns := t.CollectRequestHeaderPatterns()
	matched := schemas.FilterHeaders(headers, patterns)
	if len(matched) == 0 {
		return
	}
	t.store.SetRequestHeaders(traceID, matched)
}

// SetTraceAttribute sets a trace-level attribute. Trace attributes are never
// exported as OTEL/Datadog span attributes; observability connectors read them
// directly off the completed trace.
func (t *Tracer) SetTraceAttribute(traceID string, key string, value any) {
	t.store.SetTraceAttribute(traceID, key, value)
}

// SetTraceRedactionReplacements stores phase-scoped connector-facing replacements on a trace.
func (t *Tracer) SetTraceRedactionReplacements(traceID string, phase schemas.RedactionPhase, replacements map[string]string) {
	if t == nil || t.store == nil || strings.TrimSpace(traceID) == "" || len(replacements) == 0 {
		return
	}
	trace := t.store.GetTrace(strings.TrimSpace(traceID))
	if trace == nil {
		return
	}
	trace.SetRedactionReplacements(phase, replacements)
}

// CreateTrace creates a new trace with optional parent ID and returns the trace ID.
func (t *Tracer) CreateTrace(parentID string, requestID ...string) string {
	return t.store.CreateTrace(parentID, requestID...)
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

// GetSpanHandleByID retrieves a span handle for the given trace and span ID.
// If spanID is nil, it returns a handle for the trace's root span.
func (t *Tracer) GetSpanHandleByID(traceID string, spanID *string) schemas.SpanHandle {
	if traceID == "" {
		return nil
	}
	trace := t.store.GetTrace(traceID)
	if trace == nil {
		return nil
	}
	if spanID == nil {
		if trace.RootSpan == nil {
			return nil
		}
		return &spanHandle{traceID: traceID, spanID: trace.RootSpan.SpanID}
	}
	if *spanID == "" || trace.GetSpan(*spanID) == nil {
		return nil
	}
	return &spanHandle{traceID: traceID, spanID: *spanID}
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

	attrs := PopulateRequestAttributes(req)
	for k, v := range attrs {
		span.SetAttribute(k, v)
	}

	// Propagate input messages and request model to root span so observability backends (e.g. Langfuse)
	// can display Input and model name at the top-level trace without requiring users to drill into llm.call.
	if rootSpan := trace.RootSpan; rootSpan != nil && rootSpan.SpanID != span.SpanID {
		var inputText string
		switch req.RequestType {
		case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
			if req.ChatRequest != nil && len(req.ChatRequest.Input) > 0 {
				last := req.ChatRequest.Input[len(req.ChatRequest.Input)-1]
				inputText = extractMessageContent(last.Content)
			}
		case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
			if req.ResponsesRequest != nil && len(req.ResponsesRequest.Input) > 0 {
				last := req.ResponsesRequest.Input[len(req.ResponsesRequest.Input)-1]
				inputText = extractResponsesMessageTextContent(&last)
			}
		}
		if inputText != "" {
			rootSpan.SetAttribute(schemas.AttrInputMessages, inputText)
		} else if v, ok := attrs[schemas.AttrInputMessages]; ok {
			rootSpan.SetAttribute(schemas.AttrInputMessages, v)
		}
		if v, ok := attrs[schemas.AttrRequestModel]; ok {
			rootSpan.SetAttribute(schemas.AttrRequestModel, v)
		}
		if v, ok := attrs[schemas.AttrProviderName]; ok {
			rootSpan.SetAttribute(schemas.AttrProviderName, v)
		}
	}
}

// PopulateLLMResponseAttributes populates all LLM-specific response attributes on the span.
func (t *Tracer) PopulateLLMResponseAttributes(ctx *schemas.BifrostContext, handle schemas.SpanHandle, resp *schemas.BifrostResponse, err *schemas.BifrostError) {
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
	respAttrs := PopulateResponseAttributes(resp)
	for k, v := range respAttrs {
		if k == schemas.AttrFinishReasons {
			// Spec: gen_ai.response.finish_reasons (string[]) belongs on the GenAI (llm.call) span.
			span.SetAttribute(schemas.AttrFinishReasons, v)
			// legacy: also expose the singular scalar finish_reason (first element) for back-compat.
			if reasons, ok := v.([]string); ok && len(reasons) > 0 {
				span.SetAttribute(schemas.AttrFinishReason, reasons[0])
			}
			continue
		}
		span.SetAttribute(k, v)
	}
	for k, v := range PopulateErrorAttributes(err) {
		span.SetAttribute(k, v)
	}

	// Enrichment dimensions derivable only post-response, attached here so every
	// connector reads them from one place (see core/schemas EnrichmentDims):
	//   - alias: the originally requested model when it differs from the resolved
	//     model (an alias was matched or a fallback swapped the model).
	//   - routing_engine_used: the comma-joined set of routing engines that handled
	//     the request; the context list is only complete once routing has run.
	if resp != nil {
		ef := resp.GetExtraFields()
		if ef.ResolvedModelUsed != "" && ef.ResolvedModelUsed != ef.OriginalModelRequested && ef.OriginalModelRequested != "" {
			span.SetAttribute(schemas.AttrBifrostAlias, ef.OriginalModelRequested)
		}
	}
	if engines, ok := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string); ok && len(engines) > 0 {
		span.SetAttribute(schemas.AttrBifrostRoutingEngineUsed, strings.Join(engines, ","))
	}

	// Populate cost attribute using pricing manager
	if t.pricingManager != nil && resp != nil {
		cost := t.pricingManager.CalculateCost(resp, modelcatalog.PricingLookupScopesFromContext(ctx, string(resp.GetExtraFields().Provider)))
		span.SetAttribute(schemas.AttrUsageCost, cost)
	}

	// Propagate output messages, response model, and finish reasons to root span so observability backends (e.g. Langfuse)
	// can display Output and model name at the top-level trace without requiring users to drill into llm.call.
	if rootSpan := trace.RootSpan; rootSpan != nil && rootSpan.SpanID != span.SpanID {
		var outputText string
		if resp != nil {
			if resp.ChatResponse != nil && len(resp.ChatResponse.Choices) > 0 {
				choice := resp.ChatResponse.Choices[0]
				if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil {
					outputText = extractMessageContent(choice.ChatNonStreamResponseChoice.Message.Content)
				}
			} else if resp.ResponsesResponse != nil {
				for _, msg := range extractResponsesOutputMessages(resp.ResponsesResponse) {
					if msg.Content != "" {
						outputText = msg.Content
						break
					}
				}
			}
		}
		if outputText != "" {
			rootSpan.SetAttribute(schemas.AttrOutputMessages, outputText)
		} else if v, ok := respAttrs[schemas.AttrOutputMessages]; ok {
			rootSpan.SetAttribute(schemas.AttrOutputMessages, v)
		}
		if v, ok := respAttrs[schemas.AttrResponseModel]; ok {
			rootSpan.SetAttribute(schemas.AttrResponseModel, v)
		}
		if v, ok := respAttrs[schemas.AttrFinishReasons]; ok {
			rootSpan.SetAttribute(schemas.AttrFinishReasons, v)
		}
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

// GetAccumulatedChunks returns the accumulated response, TTFT, and chunk count for the deferred span.
// The response is built from the streaming accumulator during the final ProcessStreamingChunk call
// and stored on the DeferredSpanInfo. Returns nil response if no accumulated data is available
// (e.g., when no plugin calls ProcessStreamingChunk).
func (t *Tracer) GetAccumulatedChunks(traceID string) (*schemas.BifrostResponse, int64, int) {
	ttftNs, chunkCount := t.store.GetAccumulatedData(traceID)
	resp := t.store.GetAccumulatedResponse(traceID)
	return resp, ttftNs, chunkCount
}

// CreateStreamAccumulator creates a new stream accumulator for the given trace ID.
// This should be called at the start of a streaming request.
func (t *Tracer) CreateStreamAccumulator(traceID string, startTime time.Time) {
	if traceID == "" || t.accumulator == nil {
		return
	}
	t.accumulator.CreateStreamAccumulator(traceID, startTime)
}

// PauseStream marks the active streaming response identified by traceID as paused.
// While paused, post-processed chunks are buffered (not delivered to the client) but
// PostLLMHooks continue to fire. Idempotent. No-op if no accumulator is associated.
func (t *Tracer) PauseStream(traceID string) {
	if traceID == "" || t.accumulator == nil {
		return
	}
	t.accumulator.PauseStream(traceID)
}

// ResumeStream resumes a previously paused stream. Buffered chunks are flushed to
// the client in order, then live streaming continues. Idempotent.
func (t *Tracer) ResumeStream(traceID string) {
	if traceID == "" || t.accumulator == nil {
		return
	}
	t.accumulator.ResumeStream(traceID)
}

// ResumeStreamWithReplayInterval arms fixed-interval replay after the in-flight chunk reaches the core gate.
func (t *Tracer) ResumeStreamWithReplayInterval(traceID string, eventInterval time.Duration) bool {
	if traceID == "" || t.accumulator == nil {
		return false
	}
	return t.accumulator.ResumeStreamWithReplayInterval(traceID, eventInterval)
}

// ClearPausedStreamBuffer drops chunks buffered while traceID is paused.
func (t *Tracer) ClearPausedStreamBuffer(traceID string) error {
	if traceID == "" || t.accumulator == nil {
		return nil
	}
	return t.accumulator.ClearPausedStreamBuffer(traceID)
}

// EndStream terminates the streaming response. Any buffered chunks are flushed
// first; if err is non-nil it is then delivered as a terminal error chunk. After
// EndStream, all further provider chunks are dropped (PostLLMHook still fires).
func (t *Tracer) EndStream(traceID string, err *schemas.BifrostError) {
	if traceID == "" || t.accumulator == nil {
		return
	}
	t.accumulator.EndStream(traceID, err)
}

// WaitForFlusher blocks until the gate flusher for traceID has finished
// delivering buffered chunks (or aborted via ctx cancellation). Used by
// provider close paths to coordinate with paused streams. See
// schemas.Tracer.WaitForFlusher for full semantics.
func (t *Tracer) WaitForFlusher(traceID string) {
	if traceID == "" || t.accumulator == nil {
		return
	}
	t.accumulator.WaitForFlusher(traceID)
}

// IsStreamEnded reports whether the gate for traceID is in the Ended state.
// See schemas.Tracer.IsStreamEnded for full semantics.
func (t *Tracer) IsStreamEnded(traceID string) bool {
	if traceID == "" || t.accumulator == nil {
		return false
	}
	return t.accumulator.IsStreamEnded(traceID)
}

// IsStreamPaused reports whether the gate for traceID is currently Paused.
// See schemas.Tracer.IsStreamPaused for full semantics.
func (t *Tracer) IsStreamPaused(traceID string) bool {
	if traceID == "" || t.accumulator == nil {
		return false
	}
	return t.accumulator.IsStreamPaused(traceID)
}

// GetAccumulatedResponse returns a snapshot BifrostResponse built on demand
// from the accumulator's current chunks. See schemas.Tracer.GetAccumulatedResponse
// for full semantics.
func (t *Tracer) GetAccumulatedResponse(traceID string) *schemas.BifrostResponse {
	if traceID == "" || t.accumulator == nil {
		return nil
	}
	return t.accumulator.GetAccumulatedResponse(traceID)
}

// GateSend delivers a stream chunk through the pause/resume/end gate. Replaces
// direct channel sends in provider helpers so plugin-driven pause/resume can
// take effect. See schemas.Tracer.GateSend for full semantics.
func (t *Tracer) GateSend(traceID string, chunk *schemas.BifrostStreamChunk, isFinal, isHardErr bool, ch chan *schemas.BifrostStreamChunk, ctx *schemas.BifrostContext) (ok bool) {
	if t.accumulator == nil || traceID == "" {
		// Fallback to direct send when no accumulator is wired (defensive).
		// Recover from "send on closed channel" so a closed consumer cannot
		// crash the provider goroutine — matches NoOpTracer.GateSend and
		// GateSendChunk's non-gated fast path.
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
	return t.accumulator.GateSend(traceID, chunk, isFinal, isHardErr, ch, ctx)
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

// ForceCleanupStreamAccumulator reaps the stream accumulator for the given trace
// ID regardless of its reference counter. It is the guaranteed end-of-stream
// backstop, called from the transport's trace completer once the stream has fully
// drained, so an aborted or otherwise non-cleanly-terminated stream (or a
// multi-plugin refcount imbalance) cannot leak its accumulator.
func (t *Tracer) ForceCleanupStreamAccumulator(traceID string) {
	if traceID == "" || t.accumulator == nil {
		return
	}
	t.accumulator.ForceCleanupStreamAccumulator(traceID)
}

// ProcessStreamingChunk processes a streaming chunk and accumulates it.
// Returns the accumulated result when isFinalChunk is true and the stream is complete;
// returns nil for non-final chunks.
// This method is used by plugins to access accumulated streaming data.
// Set isFinalChunk to indicate whether the current chunk is the last in the stream.
func (t *Tracer) ProcessStreamingChunk(ctx *schemas.BifrostContext, traceID string, isFinalChunk bool, result *schemas.BifrostResponse, err *schemas.BifrostError) *schemas.StreamAccumulatorResult {
	if traceID == "" || t.accumulator == nil {
		return nil
	}

	// Create a new context for accumulator that sets the traceID as the accumulator lookup ID.
	accumCtx := schemas.NewBifrostContext(context.Background(), time.Time{})
	accumCtx.SetValue(schemas.BifrostContextKeyAccumulatorID, traceID)
	accumCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, isFinalChunk)

	// Forward relevant context values to the new context
	if ctx != nil {
		accumCtx.SetValue(schemas.BifrostContextKeySelectedKeyID, ctx.Value(schemas.BifrostContextKeySelectedKeyID))
		accumCtx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID))
	}

	processedResp, processErr := t.accumulator.ProcessStreamingResponse(accumCtx, result, err)
	if processErr != nil || processedResp == nil {
		return nil
	}

	// On final chunk, store the accumulated BifrostResponse on the deferred span
	// so that completeDeferredSpan can populate span attributes (e.g., gen_ai.output.messages)
	if isFinalChunk {
		if bifrostResp := processedResp.ToBifrostResponse(); bifrostResp != nil &&
			(bifrostResp.ChatResponse != nil ||
				bifrostResp.TextCompletionResponse != nil ||
				bifrostResp.SpeechResponse != nil ||
				bifrostResp.TranscriptionResponse != nil ||
				bifrostResp.ImageGenerationResponse != nil ||
				bifrostResp.ResponsesResponse != nil) {
			t.store.SetAccumulatedResponse(traceID, bifrostResp)
		}
	}

	// Convert ProcessedStreamResponse to StreamAccumulatorResult
	accResult := &schemas.StreamAccumulatorResult{
		RequestID:      processedResp.RequestID,
		RequestedModel: processedResp.RequestedModel,
		ResolvedModel:  processedResp.ResolvedModel,
		Provider:       processedResp.Provider,
	}

	if processedResp.Data != nil {
		accResult.Status = processedResp.Data.Status
		accResult.Latency = processedResp.Data.Latency
		accResult.TimeToFirstToken = processedResp.Data.TimeToFirstToken
		accResult.OutputMessage = processedResp.Data.OutputMessage
		accResult.OutputMessages = processedResp.Data.OutputMessages
		accResult.TokenUsage = processedResp.Data.TokenUsage
		accResult.Cost = processedResp.Data.Cost
		accResult.CacheDebug = processedResp.Data.CacheDebug
		accResult.GuardrailDebug = processedResp.Data.GuardrailDebug
		accResult.ErrorDetails = processedResp.Data.ErrorDetails
		accResult.AudioOutput = processedResp.Data.AudioOutput
		accResult.TranscriptionOutput = processedResp.Data.TranscriptionOutput
		accResult.ImageGenerationOutput = processedResp.Data.ImageGenerationOutput
		accResult.PassthroughOutput = processedResp.Data.PassthroughOutput
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

// AttachPluginLogs appends plugin log entries to the trace identified by traceID.
func (t *Tracer) AttachPluginLogs(traceID string, logs []schemas.PluginLogEntry) {
	if len(logs) == 0 || traceID == "" {
		return
	}
	trace := t.store.GetTrace(traceID)
	if trace == nil {
		return
	}
	trace.AppendPluginLogs(logs)
}

// Stop stops the tracer and releases its resources.
// This stops the internal TraceStore's cleanup goroutine.
func (t *Tracer) Stop() {
	t.flushWG.Wait()
	if t.store != nil {
		t.store.Stop()
	}
	if t.accumulator != nil {
		t.accumulator.Cleanup()
	}
}

// CompleteAndFlushTrace ends a trace and forwards it to any observability
// plugins asynchronously. Realtime transports need this explicit flush because
// they bypass the HTTP tracing middleware that normally injects completed traces.
func (t *Tracer) CompleteAndFlushTrace(traceID string) {
	if t == nil {
		return
	}
	if strings.TrimSpace(traceID) == "" {
		return
	}
	t.flushWG.Go(func() {
		completedTrace := t.EndTrace(strings.TrimSpace(traceID))
		if completedTrace == nil {
			return
		}
		// Defer release so the pooled trace is returned even if a plugin panics;
		// otherwise an unrecovered panic in this detached goroutine leaks the
		// trace object and takes down the whole process.
		defer t.ReleaseTrace(completedTrace)

		completedTrace.ApplyRedactionReplacements()

		// Give every connector a private, lock-safe snapshot. Late writers may
		// still mutate the pooled spans under the span lock (streaming
		// finalization, redaction), and connectors iterate the attribute maps
		// (directly or via Marshal) — racing them fatals with "concurrent map
		// iteration and map write", which recover() can't catch. One snapshot
		// here covers all connectors.
		exportTrace := completedTrace.SnapshotForExport()

		// Stamp Bifrost's overhead onto the snapshot's root span now that it has
		// ended. Done here — one write, before any connector reads the snapshot —
		// so every trace connector sees the same value on the root span.
		exportTrace.StampOverheadDuration()

		var obsPlugins []schemas.ObservabilityPlugin
		if loaded := t.obsPlugins.Load(); loaded != nil {
			obsPlugins = *loaded
		}
		seen := make(map[string]struct{}, len(obsPlugins))
		for _, plugin := range obsPlugins {
			if plugin == nil {
				continue
			}
			// Isolate each plugin callback — one bad observability backend should
			// not crash the server or prevent other plugins from receiving the trace.
			func(plugin schemas.ObservabilityPlugin) {
				name := "<unknown>"
				defer func() {
					if r := recover(); r != nil && t.logger != nil {
						t.logger.Error("observability plugin %s panicked during trace injection: %v", name, r)
					}
				}()
				name = plugin.GetName()
				if _, exists := seen[name]; exists {
					return
				}
				seen[name] = struct{}{}
				if err := plugin.Inject(context.Background(), exportTrace); err != nil && t.logger != nil {
					t.logger.Warn("observability plugin %s failed to inject trace: %v", name, err)
				}
			}(plugin)
		}
	})
}

// Ensure Tracer implements schemas.Tracer at compile time
var _ schemas.Tracer = (*Tracer)(nil)
