// Package tracing provides distributed tracing infrastructure for Bifrost
package tracing

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/streaming"
)

const (
	DefaultObservabilityBatchSize     = 1000                                // number of traces to accumulate before flushing to a connector; the count cap is the primary trigger for busy connectors
	DefaultObservabilityMaxBatchBytes = logstore.DefaultWriterMaxBatchBytes // in-memory byte size of traces to accumulate before flushing to a connector
	DefaultObservabilityFlushInterval = 10 * time.Second                    // how often to flush a connector's buffer, even if the count/size caps haven't been hit

	// flushStopTimeout bounds how long Stop waits for in-flight flushes, so a hung
	// connector cannot block shutdown or a config reload.
	flushStopTimeout = 10 * time.Second
)

// obsPluginSlot gives one observability plugin its own batch buffer and a timer goroutine
// that flushes it. Traces accumulate in the buffer; a flush delivers the whole batch by
// injecting each trace in turn. Nothing is dropped — producers always append. Each connector
// has its own buffer so they don't contend on a shared one.
type obsPluginSlot struct {
	plugin schemas.ObservabilityPlugin
	name   string

	batchSize     int
	maxBatchBytes int
	flushInterval time.Duration

	mu         sync.Mutex       // guards batch, batchBytes and closed
	batch      []*schemas.Trace // accumulating buffer; swapped out on flush
	batchBytes int              // estimated in-memory size of batch; reset when the buffer is swapped
	closed     bool             // set once the timer goroutine is stopping; late appends flush immediately

	stop    chan struct{}  // closed to tell the timer goroutine to do a final flush and exit
	stopped sync.Once      // so stop is only closed once (reload and Stop can race)
	wg      sync.WaitGroup // timer goroutine, for a bounded drain

	dropped atomic.Int64 // retained for ObservabilityDropCounts; stays 0 in this design
}

// start launches the timer goroutine that flushes the buffer every flushInterval.
func (s *obsPluginSlot) start(logger schemas.Logger) {
	s.wg.Add(1)
	go s.run(logger)
}

// run flushes on every tick and once more on stop, so buffered traces are delivered even
// when traffic is too light to ever fill a batch.
func (s *obsPluginSlot) run(logger schemas.Logger) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flush(s.take(), logger)
		case <-s.stop:
			s.flush(s.take(), logger) // final flush of whatever is left
			return
		}
	}
}

// enqueue appends a trace and flushes early once the buffer reaches either the count cap or
// the estimated-size cap.
func (s *obsPluginSlot) enqueue(trace *schemas.Trace, logger schemas.Logger) {
	// Estimate outside the lock — the walk is the only non-trivial work here.
	size := estimateTraceSize(trace)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.flush([]*schemas.Trace{trace}, logger)
		return
	}
	s.batch = append(s.batch, trace)
	s.batchBytes += size
	if len(s.batch) < s.batchSize && s.batchBytes < s.maxBatchBytes {
		s.mu.Unlock()
		return
	}
	batch := s.batch
	s.batch = nil
	s.batchBytes = 0
	s.mu.Unlock()
	s.flush(batch, logger)
}

// estimateTraceSize returns a cheap, approximate in-memory byte size of a trace, used only
// to decide when a buffer has grown large enough to flush. It sums the variable-length
// content (where the heavy message payloads live) plus a flat per-span/per-event overhead.
// It walks every field that can carry unbounded content - trace-level attributes, request
// headers, plugin logs, and per-span attributes and events - so a trace whose weight sits
// outside span attributes is not systematically undercounted.
func estimateTraceSize(t *schemas.Trace) int {
	if t == nil {
		return 0
	}
	size := 256 // flat per-trace overhead: ids, timestamps
	for k, v := range t.Attributes {
		size += len(k) + estimateAttrSize(v)
	}
	for k, v := range t.RequestHeaders {
		size += len(k) + len(v)
	}
	for _, pl := range t.PluginLogs {
		size += 32 + len(pl.PluginName) + len(pl.Message)
	}
	for _, span := range t.Spans {
		if span == nil {
			continue
		}
		size += 128 + len(span.Name) + len(span.StatusMsg)
		for k, v := range span.Attributes {
			size += len(k) + estimateAttrSize(v)
		}
		for _, ev := range span.Events {
			size += 32 + len(ev.Name)
			for k, v := range ev.Attributes {
				size += len(k) + estimateAttrSize(v)
			}
		}
	}
	return size
}

// estimateAttrSize approximates the byte size of a span-attribute value. It measures the
// common heavy cases (strings and string slices — where message content lives) exactly and
// falls back to a small constant for scalars and anything else, keeping the walk cheap.
func estimateAttrSize(v any) int {
	switch x := v.(type) {
	case string:
		return len(x)
	case []string:
		n := 0
		for _, s := range x {
			n += len(s)
		}
		return n
	case []any:
		n := 0
		for _, e := range x {
			n += estimateAttrSize(e)
		}
		return n
	case map[string]any:
		n := 0
		for k, e := range x {
			n += len(k) + estimateAttrSize(e)
		}
		return n
	case map[string]string:
		n := 0
		for k, e := range x {
			n += len(k) + len(e)
		}
		return n
	case nil:
		return 0
	default:
		return 16 // numbers, bools, small structs
	}
}

// take atomically swaps the current buffer out for a fresh one and returns it.
func (s *obsPluginSlot) take() []*schemas.Trace {
	s.mu.Lock()
	batch := s.batch
	s.batch = nil
	s.batchBytes = 0
	s.mu.Unlock()
	return batch
}

// flush delivers a batch by injecting each trace in turn. Empty batches are a no-op.
func (s *obsPluginSlot) flush(batch []*schemas.Trace, logger schemas.Logger) {
	if len(batch) == 0 {
		return
	}
	for _, trace := range batch {
		s.inject(trace, logger)
	}
}

// inject hands one trace to the plugin, recovering from panics so a bad backend can't take
// down the flush goroutine.
func (s *obsPluginSlot) inject(trace *schemas.Trace, logger schemas.Logger) {
	defer func() {
		if r := recover(); r != nil && logger != nil {
			logger.Error("observability plugin %s panicked during trace injection: %v", s.name, r)
		}
	}()
	if err := s.plugin.Inject(context.Background(), trace); err != nil && logger != nil {
		logger.Warn("observability plugin %s failed to inject trace: %v", s.name, err)
	}
}

// signalStop tells the timer goroutine to do its final flush and exit. Safe to call twice.
// closed is set before stop is closed so a concurrent enqueue either lands in the buffer the
// final flush will drain, or observes closed and flushes on its own — never lost.
func (s *obsPluginSlot) signalStop() {
	s.stopped.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.stop)
	})
}

// Tracer implements schemas.Tracer using TraceStore.
// It provides the bridge between the core Tracer interface and the
// framework's TraceStore implementation.
// It also embeds a streaming.Accumulator for centralized streaming chunk accumulation.
type Tracer struct {
	store             *TraceStore
	accumulator       *streaming.Accumulator
	pricingManager    *modelcatalog.ModelCatalog
	logger            schemas.Logger
	obsPlugins        atomic.Pointer[[]*obsPluginSlot]
	cachedHdrPatterns atomic.Pointer[[]string]
	flushWG           sync.WaitGroup

	// batchSize, maxBatchBytes, and flushInterval size each connector's buffer, read when the
	// plugin slots are built. They default to the DefaultObservability* values; tests override
	// them before SetObservabilityPlugins.
	batchSize     int
	maxBatchBytes int
	flushInterval time.Duration
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
		obsPlugins:     atomic.Pointer[[]*obsPluginSlot]{},
		batchSize:      DefaultObservabilityBatchSize,
		maxBatchBytes:  DefaultObservabilityMaxBatchBytes,
		flushInterval:  DefaultObservabilityFlushInterval,
	}
}

// SetObservabilityFlushIntervalSeconds sets how often each connector flushes its buffer,
// from a config-supplied seconds value. Non-positive falls back to the default.
// Call before SetObservabilityPlugins for it to take effect.
func (t *Tracer) SetObservabilityFlushIntervalSeconds(seconds int) {
	if t == nil {
		return
	}
	if seconds <= 0 {
		t.flushInterval = DefaultObservabilityFlushInterval
		return
	}
	t.flushInterval = time.Duration(seconds) * time.Second
}

// SetObservabilityPlugins updates the plugins that receive completed traces.
// It also precomputes the deduplicated, normalized union of request-header patterns
// requested by those plugins so the per-request capture path is a single atomic load.
func (t *Tracer) SetObservabilityPlugins(obsPlugins []schemas.ObservabilityPlugin) {
	if t == nil {
		return
	}
	// Build one slot per distinct plugin, each with its own buffer and timer goroutine.
	// Deduplication by name happens here rather than on every flush, so the hot
	// path does not rebuild a map per trace.
	batchSize := t.batchSize
	if batchSize <= 0 {
		batchSize = DefaultObservabilityBatchSize
	}
	maxBatchBytes := t.maxBatchBytes
	if maxBatchBytes <= 0 {
		maxBatchBytes = DefaultObservabilityMaxBatchBytes
	}
	flushInterval := t.flushInterval
	if flushInterval <= 0 {
		flushInterval = DefaultObservabilityFlushInterval
	}
	slots := make([]*obsPluginSlot, 0, len(obsPlugins))
	seenPlugins := make(map[string]struct{}, len(obsPlugins))
	for _, plugin := range obsPlugins {
		if plugin == nil {
			continue
		}
		name := plugin.GetName()
		if _, exists := seenPlugins[name]; exists {
			continue
		}
		seenPlugins[name] = struct{}{}
		slot := &obsPluginSlot{
			plugin:        plugin,
			name:          name,
			batchSize:     batchSize,
			maxBatchBytes: maxBatchBytes,
			flushInterval: flushInterval,
			stop:          make(chan struct{}),
		}
		slot.start(t.logger)
		slots = append(slots, slot)
	}

	// Swap in the new slots, then tell the old ones to do a final flush and wind down. They
	// drain in the background — no need to block the reload on them.
	old := t.obsPlugins.Load()
	t.obsPlugins.Store(&slots)
	if old != nil {
		for _, slot := range *old {
			slot.signalStop()
		}
	}

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
		// Speed and InferenceGeo ride along inside TokenUsage (providers set them on
		// BifrostLLMUsage for exactly that reason), but service_tier lives on the
		// response envelope, so it has to be copied across explicitly or the tier the
		// accumulator resolved across chunks is lost and the row reprices at standard
		// rates.
		accResult.ServiceTier = processedResp.Data.ServiceTier
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
	// Let in-flight producers finish appending (fast — no network), then drain the connector
	// buffers via a final flush. Both phases share a single flushStopTimeout budget — the drain
	// gets whatever the (usually near-instant) producer wait didn't use — so a connector stuck on
	// an unreachable collector can't push total shutdown past one timeout (gRPC exports carry no
	// deadline of their own).
	deadline := time.Now().Add(flushStopTimeout)
	t.waitForFlushes(flushStopTimeout)
	t.drainObsPlugins(time.Until(deadline))
	if t.store != nil {
		t.store.Stop()
	}
	if t.accumulator != nil {
		t.accumulator.Cleanup()
	}
}

// drainObsPlugins tells every connector to do a final flush and waits up to timeout for its
// timer and flush goroutines to finish. Anything still in flight when the budget runs out is
// abandoned — best-effort telemetry must never block shutdown. A non-positive timeout (the
// shared budget already spent by an earlier phase) means don't wait: signal and move on.
func (t *Tracer) drainObsPlugins(timeout time.Duration) {
	if timeout < 0 {
		timeout = 0
	}
	loaded := t.obsPlugins.Load()
	if loaded == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		for _, slot := range *loaded {
			slot.signalStop()
		}
		for _, slot := range *loaded {
			slot.wg.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		if t.logger != nil {
			t.logger.Warn("timed out after %s draining observability plugin buffers; continuing shutdown", timeout)
		}
	}
}

// waitForFlushes waits for in-flight trace exports, giving up after timeout.
// Returns true if every flush completed within the budget.
func (t *Tracer) waitForFlushes(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		t.flushWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		if t.logger != nil {
			t.logger.Warn("timed out after %s waiting for in-flight trace exports; continuing shutdown", timeout)
		}
		return false
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

		var slots []*obsPluginSlot
		if loaded := t.obsPlugins.Load(); loaded != nil {
			slots = *loaded
		}

		// Append the snapshot to each connector's buffer and return. The snapshot is detached
		// from the pooled trace, so it's safe to hold in the buffers after this goroutine
		// releases the pooled object below, and safe for all connectors to share one copy
		// (they only read it). enqueue appends the snapshot and, when a cap is reached,
		// flushes the batch inline before returning.
		for _, slot := range slots {
			slot.enqueue(exportTrace, t.logger)
		}
	})
}

// ObservabilityDropCounts returns, per observability plugin name, how many traces were
// dropped before delivery. Retained for API compatibility; this delivery path buffers rather
// than drops, so counts stay at zero.
func (t *Tracer) ObservabilityDropCounts() map[string]int64 {
	if t == nil {
		return nil
	}
	loaded := t.obsPlugins.Load()
	if loaded == nil {
		return nil
	}
	counts := make(map[string]int64, len(*loaded))
	for _, slot := range *loaded {
		counts[slot.name] = slot.dropped.Load()
	}
	return counts
}

// Ensure Tracer implements schemas.Tracer at compile time
var _ schemas.Tracer = (*Tracer)(nil)
