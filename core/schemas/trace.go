// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"maps"
	"strings"
	"sync"
	"time"
)

// Trace represents a distributed trace that captures the full lifecycle of a request
//
// InternalID vs TraceID:
//   - InternalID is the in-process storage key. It is always unique per request,
//     even when many concurrent requests share the same upstream W3C trace ID.
//     Plugins that key per-request state off a trace (e.g. pendingLogsToInject)
//     should use InternalID, not TraceID, to avoid cross-request collisions.
//   - TraceID is the W3C trace ID used for export to OTEL/Datadog/etc. Multiple
//     concurrent traces can carry the same TraceID; that is the whole point of
//     distributed tracing — sibling spans across services share one trace ID.
type Trace struct {
	InternalID            string            // Opaque in-process storage key (unique per request)
	RequestID             string            // Request ID for the trace
	TraceID               string            // Unique identifier for this trace
	ParentID              string            // Parent trace ID from incoming W3C traceparent header
	RootSpan              *Span             // The root span of this trace
	Spans                 []*Span           // All spans in this trace
	StartTime             time.Time         // When the trace started
	EndTime               time.Time         // When the trace completed
	Attributes            map[string]any    // Additional attributes for the trace
	RequestHeaders        map[string]string // Lowercased request headers, populated only when a connector opts in
	PluginLogs            []PluginLogEntry  // Plugin log entries accumulated during request processing
	redactionReplacements RedactionMapsByPhase
	mu                    sync.Mutex // Mutex for thread-safe span operations
}

// Trace-level attribute keys. Unlike span attributes, trace attributes are never
// exported as OTEL/Datadog span attributes — observability connectors (BigQuery,
// Datadog) read them directly off the completed trace.
const (
	// TraceAttrSessionID holds the session ID from the x-bf-session-id request
	// header. The key matches the header name because connectors already read it.
	TraceAttrSessionID = "x-bf-session-id"
	// TraceAttrDimensions holds the map[string]string of request dimensions
	// parsed from x-bf-dim-* headers, keyed by bare dimension name.
	TraceAttrDimensions = "bifrost.dimensions"
)

// AddSpan adds a span to the trace in a thread-safe manner
func (t *Trace) AddSpan(span *Span) {
	if t == nil || span == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Spans = append(t.Spans, span)
}

// ElectRootOrChildAndAdd appends span and elects the trace's RootSpan atomically.
// If the trace has no root yet, span becomes the root with ParentID="" and
// becameRoot is true. Otherwise span.ParentID is rewritten to the current
// root span's SpanID and becameRoot is false. Both the read of RootSpan and
// the write of RootSpan/Spans happen under t.mu, which prevents the
// "two goroutines both decide they are the root" race that would otherwise
// be visible when StartSpan is called concurrently on a freshly created trace.
func (t *Trace) ElectRootOrChildAndAdd(span *Span) (becameRoot bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.RootSpan == nil {
		span.ParentID = ""
		t.RootSpan = span
		becameRoot = true
	} else {
		span.ParentID = t.RootSpan.SpanID
	}
	t.Spans = append(t.Spans, span)
	return becameRoot
}

// AddChildSpanElectingRoot appends a span whose ParentID was already set by
// the caller (typically to an external W3C parent span ID extracted from a
// traceparent header). If the trace has no root yet, span also becomes the
// root *without* clearing its caller-set ParentID — the upstream link is
// preserved so OTEL backends can stitch the trace across services. Both the
// read and write of RootSpan happen under t.mu.
func (t *Trace) AddChildSpanElectingRoot(span *Span) (becameRoot bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.RootSpan == nil {
		t.RootSpan = span
		becameRoot = true
	}
	t.Spans = append(t.Spans, span)
	return becameRoot
}

// GetSpan retrieves a span by ID
func (t *Trace) GetSpan(spanID string) *Span {
	if t == nil || spanID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, span := range t.Spans {
		if span == nil {
			continue
		}
		if span.SpanID == spanID {
			return span
		}
	}
	return nil
}

// GetRequestID retrieves the request ID from the trace
func (t *Trace) GetRequestID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.RequestID
}

// SetRequestID sets the request ID for the trace
func (t *Trace) SetRequestID(requestID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.RequestID = requestID
}

// SetRequestHeaders sets the captured request headers for the trace.
func (t *Trace) SetRequestHeaders(headers map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.RequestHeaders = headers
}

// SetRedactionReplacements merges connector-facing raw-to-placeholder replacements on the trace.
func (t *Trace) SetRedactionReplacements(phase RedactionPhase, replacements map[string]string) {
	if len(replacements) == 0 {
		return
	}
	copied := make(map[string]string, len(replacements))
	for raw, placeholder := range replacements {
		if raw != "" {
			copied[raw] = placeholder
		}
	}
	if len(copied) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.redactionReplacements.MergePhase(phase, copied)
}

// ApplyRedactionReplacements redacts content attributes on every span in the trace and clears the replacement map.
func (t *Trace) ApplyRedactionReplacements() {
	t.mu.Lock()
	if !t.redactionReplacements.HasReplacements() {
		t.mu.Unlock()
		return
	}
	replacements := t.redactionReplacements.Clone()
	t.redactionReplacements = RedactionMapsByPhase{}
	rootSpan := t.RootSpan
	spans := append([]*Span(nil), t.Spans...)
	t.mu.Unlock()

	redactSpanAttributes(rootSpan, replacements.Input, replacements.Output)
	for _, span := range spans {
		if span == nil || span == rootSpan {
			continue
		}
		redactSpanAttributes(span, replacements.Input, replacements.Output)
	}
}

// SetAttribute sets a trace-level attribute in a thread-safe manner
func (t *Trace) SetAttribute(key string, value any) {
	if value == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Attributes == nil {
		t.Attributes = make(map[string]any)
	}
	t.Attributes[key] = value
}

// GetAttribute retrieves a trace-level attribute in a thread-safe manner.
// The second return value reports whether the key was present.
func (t *Trace) GetAttribute(key string) (any, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	value, ok := t.Attributes[key]
	return value, ok
}

// SnapshotForExport returns a copy of the trace that is safe for concurrent
// read-only use by observability exporters (Datadog, OTEL, ...) while the
// original trace's spans may still be mutated.
//
// Trace- and span-level attribute maps are cloned under their respective locks,
// so an exporter iterating the returned maps can never race a late writer — e.g.
// streaming span finalization (completeDeferredSpan) or redaction replacement —
// that legitimately holds the span lock. Without this, an exporter iterating the
// live span.Attributes map (which cannot take the unexported span lock) triggers
// a fatal "concurrent map iteration and map write" that recover() cannot catch.
//
// Span pointer identity is preserved *within* the returned trace: RootSpan and
// the entries of Spans refer to the same copied *Span values, so pointer-equality
// checks (e.g. span == finalAttempt) still work against the snapshot's own spans.
// Attribute values are copied by reference and must be treated as read-only.
func (t *Trace) SnapshotForExport() *Trace {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	clone := &Trace{
		RequestID:      t.RequestID,
		TraceID:        t.TraceID,
		ParentID:       t.ParentID,
		StartTime:      t.StartTime,
		EndTime:        t.EndTime,
		Attributes:     maps.Clone(t.Attributes),
		RequestHeaders: maps.Clone(t.RequestHeaders),
		PluginLogs:     append([]PluginLogEntry(nil), t.PluginLogs...),
	}
	spans := append([]*Span(nil), t.Spans...)
	rootSpan := t.RootSpan
	t.mu.Unlock()

	spanCopies := make(map[*Span]*Span, len(spans))
	clone.Spans = make([]*Span, 0, len(spans))
	for _, span := range spans {
		if span == nil {
			continue
		}
		cp := span.snapshotForExport()
		spanCopies[span] = cp
		clone.Spans = append(clone.Spans, cp)
	}
	if rootSpan != nil {
		if cp, ok := spanCopies[rootSpan]; ok {
			clone.RootSpan = cp
		} else {
			clone.RootSpan = rootSpan.snapshotForExport()
		}
	}
	return clone
}

// StampOverheadDuration writes Bifrost's own cost onto the root span as
// AttrBifrostOverheadDurationMs: the root span's wall time minus the upstream
// total stamped on it. This is the single definition of the overhead number —
// every trace connector reads the attribute rather than re-deriving it, so the
// span and the overhead metric can never disagree.
//
// Call this on the export snapshot after SnapshotForExport, never on the pooled
// trace: the root span's duration is only known once it has ended, and mutating
// a pooled span at flush time races the late writers the snapshot exists to
// isolate. The snapshot's attribute maps are private clones, so the write here
// is safe on the single flush goroutine before any connector reads them.
//
// No-op when the root span never ended or carried no upstream measurement;
// absent overhead must not be reported as zero.
func (t *Trace) StampOverheadDuration() {
	if t == nil || t.RootSpan == nil {
		return
	}
	root := t.RootSpan
	if root.StartTime.IsZero() || root.EndTime.IsZero() || root.Attributes == nil {
		return
	}
	raw, ok := root.Attributes[AttrBifrostUpstreamDurationMs]
	if !ok {
		return
	}
	var upstreamMs float64
	switch v := raw.(type) {
	case float64:
		upstreamMs = v
	case int64:
		upstreamMs = float64(v)
	case int:
		upstreamMs = float64(v)
	default:
		return
	}
	overheadMs := float64(root.EndTime.Sub(root.StartTime))/float64(time.Millisecond) - upstreamMs
	// Different clocks: a request that is almost entirely upstream can round
	// slightly negative, which is meaningless and would poison a histogram.
	if overheadMs < 0 {
		overheadMs = 0
	}
	root.Attributes[AttrBifrostOverheadDurationMs] = overheadMs
}

// Reset clears the trace for reuse from pool
func (t *Trace) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.InternalID = ""
	t.RequestID = ""
	t.TraceID = ""
	t.ParentID = ""
	t.RootSpan = nil
	for i := range t.Spans {
		t.Spans[i] = nil
	}
	t.Spans = t.Spans[:0]
	t.StartTime = time.Time{}
	t.EndTime = time.Time{}
	t.Attributes = nil
	t.RequestHeaders = nil
	t.redactionReplacements = RedactionMapsByPhase{}
	for i := range t.PluginLogs {
		t.PluginLogs[i] = PluginLogEntry{}
	}
	t.PluginLogs = t.PluginLogs[:0]
}

// AppendPluginLogs appends plugin log entries to the trace in a thread-safe manner.
func (t *Trace) AppendPluginLogs(logs []PluginLogEntry) {
	if len(logs) == 0 {
		return
	}
	t.mu.Lock()
	t.PluginLogs = append(t.PluginLogs, logs...)
	t.mu.Unlock()
}

// traceContentAttributeScope describes which phase can safely redact a trace content attribute.
type traceContentAttributeScope int

const (
	traceContentAttributeScopeNone traceContentAttributeScope = iota
	traceContentAttributeScopeInput
	traceContentAttributeScopeOutput
	traceContentAttributeScopeMixed
)

// traceRedactionReplacementsForAttribute selects the phase-specific replacements for one trace attribute.
func traceRedactionReplacementsForAttribute(key string, inputReplacements map[string]string, outputReplacements map[string]string) map[string]string {
	switch traceContentAttributeScopeForKey(key) {
	case traceContentAttributeScopeInput:
		return inputReplacements
	case traceContentAttributeScopeOutput:
		return outputReplacements
	case traceContentAttributeScopeMixed:
		return mergeTraceRedactionReplacements(inputReplacements, outputReplacements)
	default:
		return nil
	}
}

// traceContentAttributeScopeForKey classifies content attributes by request/response phase.
func traceContentAttributeScopeForKey(key string) traceContentAttributeScope {
	switch key {
	case AttrInputMessages, AttrInputText, AttrInputSpeech, AttrInputEmbedding,
		AttrPrompt, AttrInstructions,
		AttrTools, AttrToolChoiceType, AttrToolChoiceName,
		AttrRespTools, AttrRespToolChoiceType, AttrRespToolChoiceName:
		return traceContentAttributeScopeInput
	case AttrOutputMessages, AttrRespReasoningText:
		return traceContentAttributeScopeOutput
	case AttrToolName, AttrToolCallID, AttrToolCallArguments, AttrToolCallResult, AttrToolType:
		return traceContentAttributeScopeMixed
	default:
		return traceContentAttributeScopeNone
	}
}

// mergeTraceRedactionReplacements returns a combined replacement map for mixed trace attributes.
func mergeTraceRedactionReplacements(inputReplacements map[string]string, outputReplacements map[string]string) map[string]string {
	return mergeRedactionStringMaps(inputReplacements, outputReplacements)
}

// redactSpanAttributes applies phase-aware trace redaction replacements to one span's content attributes.
func redactSpanAttributes(span *Span, inputReplacements map[string]string, outputReplacements map[string]string) {
	if span == nil || (len(inputReplacements) == 0 && len(outputReplacements) == 0) {
		return
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	for key, value := range span.Attributes {
		if replacements := traceRedactionReplacementsForAttribute(key, inputReplacements, outputReplacements); len(replacements) > 0 {
			span.Attributes[key] = RedactAttributeValue(value, replacements)
		}
	}
	for i := range span.Events {
		for key, value := range span.Events[i].Attributes {
			if replacements := traceRedactionReplacementsForAttribute(key, inputReplacements, outputReplacements); len(replacements) > 0 {
				span.Events[i].Attributes[key] = RedactAttributeValue(value, replacements)
			}
		}
	}
}

// Span represents a single operation within a trace
type Span struct {
	SpanID     string         // Unique identifier for this span
	ParentID   string         // Parent span ID (empty for root span)
	TraceID    string         // The trace this span belongs to
	Name       string         // Name of the operation
	Kind       SpanKind       // Type of span (LLM call, plugin, etc.)
	StartTime  time.Time      // When the span started
	EndTime    time.Time      // When the span completed
	Status     SpanStatus     // Status of the operation
	StatusMsg  string         // Optional status message (for errors)
	Attributes map[string]any // Additional attributes for the span
	Events     []SpanEvent    // Events that occurred during the span
	mu         sync.Mutex     // Mutex for thread-safe attribute operations
}

// SetAttribute sets an attribute on the span in a thread-safe manner
func (s *Span) SetAttribute(key string, value any) {
	if s == nil || value == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Attributes == nil {
		s.Attributes = make(map[string]any)
	}
	s.Attributes[key] = value
}

// snapshotForExport returns a copy of the span whose Attributes (and Events)
// are cloned under the span lock, so observability exporters can read them
// concurrently while a late writer (streaming finalization, redaction) may still
// mutate the original span. See Trace.SnapshotForExport. The returned span has a
// fresh zero-value mutex and its attribute values are copied by reference.
func (s *Span) snapshotForExport() *Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := &Span{
		SpanID:     s.SpanID,
		ParentID:   s.ParentID,
		TraceID:    s.TraceID,
		Name:       s.Name,
		Kind:       s.Kind,
		StartTime:  s.StartTime,
		EndTime:    s.EndTime,
		Status:     s.Status,
		StatusMsg:  s.StatusMsg,
		Attributes: maps.Clone(s.Attributes),
	}
	if len(s.Events) > 0 {
		cp.Events = make([]SpanEvent, len(s.Events))
		for i := range s.Events {
			cp.Events[i] = SpanEvent{
				Name:       s.Events[i].Name,
				Timestamp:  s.Events[i].Timestamp,
				Attributes: maps.Clone(s.Events[i].Attributes),
			}
		}
	}
	return cp
}

// AddEvent adds an event to the span in a thread-safe manner
func (s *Span) AddEvent(event SpanEvent) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Events = append(s.Events, event)
}

// End marks the span as complete with the given status
func (s *Span) End(status SpanStatus, statusMsg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.EndTime = time.Now()
	s.Status = status
	s.StatusMsg = statusMsg
}

// Reset clears the span for reuse from pool. It holds s.mu — like every other
// Span mutator — so a straggling writer (e.g. streaming finalization) that races
// pool release can't trigger a fatal concurrent map access on s.Attributes.
func (s *Span) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SpanID = ""
	s.ParentID = ""
	s.TraceID = ""
	s.Name = ""
	s.Kind = SpanKindUnspecified
	s.StartTime = time.Time{}
	s.EndTime = time.Time{}
	s.Status = SpanStatusUnset
	s.StatusMsg = ""
	s.Attributes = nil
	s.Events = s.Events[:0]
}

// SpanEvent represents a time-stamped event within a span
type SpanEvent struct {
	Name       string         // Name of the event
	Timestamp  time.Time      // When the event occurred
	Attributes map[string]any // Additional attributes for the event
}

// SpanKind represents the type of operation a span represents
// These are LLM-specific kinds designed for AI gateway observability
type SpanKind string

const (
	// SpanKindUnspecified is the default span kind
	SpanKindUnspecified SpanKind = ""
	// SpanKindLLMCall represents a call to an LLM provider
	SpanKindLLMCall SpanKind = "llm.call"
	// SpanKindPlugin represents plugin execution (PreLLMHook/PostLLMHook)
	SpanKindPlugin SpanKind = "plugin"
	// SpanKindMCPTool represents an MCP tool invocation
	SpanKindMCPTool SpanKind = "mcp.tool"
	// SpanKindMCPClient represents an MCP client lifecycle operation (connect/ping/list_tools).
	// These run in the background per-client and are not part of an LLM request flow.
	SpanKindMCPClient SpanKind = "mcp.client"
	// SpanKindRetry represents a retry attempt
	SpanKindRetry SpanKind = "retry"
	// SpanKindFallback represents a fallback to another provider
	SpanKindFallback SpanKind = "fallback"
	// SpanKindHTTPRequest represents the root HTTP request span
	SpanKindHTTPRequest SpanKind = "http.request"
	// SpanKindEmbedding represents an embedding request
	SpanKindEmbedding SpanKind = "embedding"
	// SpanKindSpeech represents a text-to-speech request
	SpanKindSpeech SpanKind = "speech"
	// SpanKindTranscription represents a speech-to-text request
	SpanKindTranscription SpanKind = "transcription"
	// SpanKindInternal represents internal operations (key selection, etc.)
	SpanKindInternal SpanKind = "internal"
)

// SpanStatus represents the status of a span's operation
type SpanStatus string

const (
	// SpanStatusUnset indicates status has not been set
	SpanStatusUnset SpanStatus = "unset"
	// SpanStatusOk indicates the operation completed successfully
	SpanStatusOk SpanStatus = "ok"
	// SpanStatusError indicates the operation failed
	SpanStatusError SpanStatus = "error"
)

// LLM Attribute Keys (gen_ai.* namespace)
// These follow the OpenTelemetry semantic conventions for GenAI
// and are compatible with both OTEL and Datadog backends.
const (
	// Provider and Model Attributes
	AttrProviderName  = "gen_ai.provider.name"
	AttrRequestModel  = "gen_ai.request.model"
	AttrOperationName = "gen_ai.operation.name"

	// Request Parameter Attributes
	AttrMaxTokens        = "gen_ai.request.max_tokens"
	AttrTemperature      = "gen_ai.request.temperature"
	AttrTopP             = "gen_ai.request.top_p"
	AttrStopSequences    = "gen_ai.request.stop_sequences"
	AttrPresencePenalty  = "gen_ai.request.presence_penalty"
	AttrFrequencyPenalty = "gen_ai.request.frequency_penalty"
	AttrParallelToolCall = "gen_ai.request.parallel_tool_calls"
	AttrRequestUser      = "gen_ai.request.user"
	AttrBestOf           = "gen_ai.request.best_of"
	AttrEcho             = "gen_ai.request.echo"
	AttrLogitBias        = "gen_ai.request.logit_bias"
	AttrLogProbs         = "gen_ai.request.logprobs"
	AttrN                = "gen_ai.request.n" // legacy: replaced by AttrChoiceCount
	AttrChoiceCount      = "gen_ai.request.choice.count"
	// AttrEmbeddingsDimensionCount is the OTel spec key for embedding dimensions
	// (Bifrost historically emitted AttrDimensions = gen_ai.request.dimensions).
	AttrEmbeddingsDimensionCount = "gen_ai.embeddings.dimension.count"
	AttrSeed                     = "gen_ai.request.seed"
	AttrSuffix                   = "gen_ai.request.suffix"
	AttrDimensions               = "gen_ai.request.dimensions"      // legacy: replaced by AttrEmbeddingsDimensionCount
	AttrEncodingFormat           = "gen_ai.request.encoding_format" // legacy: singular form; replaced by AttrEncodingFormats (string[])
	AttrEncodingFormats          = "gen_ai.request.encoding_formats"
	AttrLanguage                 = "gen_ai.request.language"
	AttrPrompt                   = "gen_ai.request.prompt"
	AttrResponseFormat           = "gen_ai.request.response_format"
	AttrFormat                   = "gen_ai.request.format"
	AttrVoice                    = "gen_ai.request.voice"
	AttrMultiVoiceConfig         = "gen_ai.request.multi_voice_config"
	AttrInstructions             = "gen_ai.request.instructions"
	AttrSpeed                    = "gen_ai.request.speed"
	AttrMessageCount             = "gen_ai.request.message_count"

	// Response Attributes
	AttrResponseID       = "gen_ai.response.id"
	AttrResponseModel    = "gen_ai.response.model"
	AttrFinishReason     = "gen_ai.response.finish_reason"
	AttrFinishReasons    = "gen_ai.response.finish_reasons"
	AttrSystemFprint     = "gen_ai.response.system_fingerprint"
	AttrServiceTier      = "gen_ai.response.service_tier"
	AttrCreated          = "gen_ai.response.created"
	AttrObject           = "gen_ai.response.object"
	AttrTimeToFirstToken = "gen_ai.response.time_to_first_token" // legacy: nanoseconds; replaced by gen_ai.response.time_to_first_chunk (seconds)
	AttrTimeToFirstChunk = "gen_ai.response.time_to_first_chunk"
	AttrTotalChunks      = "gen_ai.response.total_chunks"

	// Plugin Attributes (for aggregated streaming post-hook spans)
	AttrPluginInvocations     = "plugin.invocation_count"
	AttrPluginAvgDurationMs   = "plugin.avg_duration_ms"
	AttrPluginTotalDurationMs = "plugin.total_duration_ms"
	AttrPluginErrorCount      = "plugin.error_count"

	// Usage Attributes
	// legacy: AttrPromptTokens / AttrCompletionTokens are the deprecated OTel names;
	// new code should use AttrInputTokens / AttrOutputTokens. Kept for dashboards.
	AttrPromptTokens     = "gen_ai.usage.prompt_tokens"
	AttrCompletionTokens = "gen_ai.usage.completion_tokens"
	AttrTotalTokens      = "gen_ai.usage.total_tokens"
	AttrInputTokens      = "gen_ai.usage.input_tokens"
	AttrOutputTokens     = "gen_ai.usage.output_tokens"
	AttrUsageCost        = "gen_ai.usage.cost"
	// OTel GenAI spec keys for cache tokens (flat namespace).
	AttrUsageCacheReadInputTokens     = "gen_ai.usage.cache_read.input_tokens"
	AttrUsageCacheCreationInputTokens = "gen_ai.usage.cache_creation.input_tokens"
	// OTel GenAI spec key for reasoning tokens (flat namespace).
	AttrUsageReasoningOutputTokens = "gen_ai.usage.reasoning.output_tokens"
	// Chat completion usage detail attributes
	// legacy: nested namespace; OTel spec uses flat gen_ai.usage.cache_read.input_tokens
	// and gen_ai.usage.cache_creation.input_tokens for the cached_* entries. The
	// non-cached fields below have no spec equivalent and stay as-is.
	AttrPromptTokenDetailsText          = "gen_ai.usage.prompt_token_details.text_tokens"
	AttrPromptTokenDetailsAudio         = "gen_ai.usage.prompt_token_details.audio_tokens"
	AttrPromptTokenDetailsImage         = "gen_ai.usage.prompt_token_details.image_tokens"
	AttrPromptTokenDetailsCachedRead    = "gen_ai.usage.prompt_token_details.cached_read_tokens"  // legacy: see AttrUsageCacheReadInputTokens
	AttrPromptTokenDetailsCachedWrite   = "gen_ai.usage.prompt_token_details.cached_write_tokens" // legacy: see AttrUsageCacheCreationInputTokens
	AttrPromptTokenDetailsCachedWrite5m = "gen_ai.usage.prompt_token_details.cached_write_tokens_5m"
	AttrPromptTokenDetailsCachedWrite1h = "gen_ai.usage.prompt_token_details.cached_write_tokens_1h"
	AttrCompletionTokenDetailsText      = "gen_ai.usage.completion_token_details.text_tokens"
	AttrCompletionTokenDetailsAudio     = "gen_ai.usage.completion_token_details.audio_tokens"
	AttrCompletionTokenDetailsImage     = "gen_ai.usage.completion_token_details.image_tokens"
	AttrCompletionTokenDetailsReason    = "gen_ai.usage.completion_token_details.reasoning_tokens"
	AttrCompletionTokenDetailsAccept    = "gen_ai.usage.completion_token_details.accepted_prediction_tokens"
	AttrCompletionTokenDetailsReject    = "gen_ai.usage.completion_token_details.rejected_prediction_tokens"
	AttrCompletionTokenDetailsCite      = "gen_ai.usage.completion_token_details.citation_tokens"
	AttrCompletionTokenDetailsSearch    = "gen_ai.usage.completion_token_details.num_search_queries"

	// Error Attributes
	AttrError = "gen_ai.error"
	// legacy: AttrErrorType is the gen_ai.* placement; OTel general semconv uses the
	// unprefixed "error.type". Emitted in parallel from PopulateErrorAttributes.
	AttrErrorType = "gen_ai.error.type"
	AttrErrorCode = "gen_ai.error.code"
	// AttrHTTPResponseStatusCode is the OTel semconv HTTP response status code (e.g. 400).
	// Sourced from BifrostError.StatusCode; used as the status_code dimension on error metrics.
	AttrHTTPResponseStatusCode = "http.response.status_code"

	// Input/Output Attributes
	AttrInputText      = "gen_ai.input.text"
	AttrInputMessages  = "gen_ai.input.messages"
	AttrInputSpeech    = "gen_ai.input.speech"
	AttrInputEmbedding = "gen_ai.input.embedding"
	AttrOutputMessages = "gen_ai.output.messages"

	// Bifrost Context Attributes
	// legacy: every key below sits under gen_ai.* but represents a Bifrost-internal
	// concept (governance / routing). The bifrost.* mirrors are the canonical home
	// going forward; these will be dropped once dashboards migrate.
	AttrRequestID       = "gen_ai.request_id"
	AttrVirtualKeyID    = "gen_ai.virtual_key_id"
	AttrVirtualKeyName  = "gen_ai.virtual_key_name"
	AttrSelectedKeyID   = "gen_ai.selected_key_id"
	AttrSelectedKeyName = "gen_ai.selected_key_name"
	AttrRoutingRuleID   = "gen_ai.routing_rule_id"
	AttrRoutingRuleName = "gen_ai.routing_rule_name"
	AttrTeamID          = "gen_ai.team_id"
	AttrTeamName        = "gen_ai.team_name"
	AttrCustomerID      = "gen_ai.customer_id"
	AttrCustomerName    = "gen_ai.customer_name"
	AttrNumberOfRetries = "gen_ai.number_of_retries"
	AttrFallbackIndex   = "gen_ai.fallback_index"

	// Extra Header Attributes
	AttrExtraHeaderPrefix = "gen_ai.request.extra_header."

	// Responses API Request Attributes
	AttrPromptCacheKey      = "gen_ai.request.prompt_cache_key"
	AttrReasoningEffort     = "gen_ai.request.reasoning_effort"
	AttrReasoningSummary    = "gen_ai.request.reasoning_summary"
	AttrReasoningGenSummary = "gen_ai.request.reasoning_generate_summary"
	AttrSafetyIdentifier    = "gen_ai.request.safety_identifier"
	AttrStore               = "gen_ai.request.store"
	AttrTextVerbosity       = "gen_ai.request.text_verbosity"
	AttrTextFormatType      = "gen_ai.request.text_format_type"
	AttrTopLogProbs         = "gen_ai.request.top_logprobs"
	AttrToolChoiceType      = "gen_ai.request.tool_choice_type"
	AttrToolChoiceName      = "gen_ai.request.tool_choice_name"
	AttrTools               = "gen_ai.request.tools"
	AttrTruncation          = "gen_ai.request.truncation"

	// Responses API Response Attributes
	AttrRespInclude          = "gen_ai.responses.include"
	AttrRespMaxOutputTokens  = "gen_ai.responses.max_output_tokens"
	AttrRespMaxToolCalls     = "gen_ai.responses.max_tool_calls"
	AttrRespMetadata         = "gen_ai.responses.metadata"
	AttrRespPreviousRespID   = "gen_ai.responses.previous_response_id"
	AttrRespPromptCacheKey   = "gen_ai.responses.prompt_cache_key"
	AttrRespReasoningText    = "gen_ai.responses.reasoning"
	AttrRespReasoningEffort  = "gen_ai.responses.reasoning_effort"
	AttrRespReasoningGenSum  = "gen_ai.responses.reasoning_generate_summary"
	AttrRespSafetyIdentifier = "gen_ai.responses.safety_identifier"
	AttrRespStore            = "gen_ai.responses.store"
	AttrRespTemperature      = "gen_ai.responses.temperature"
	AttrRespTextVerbosity    = "gen_ai.responses.text_verbosity"
	AttrRespTextFormatType   = "gen_ai.responses.text_format_type"
	AttrRespTopLogProbs      = "gen_ai.responses.top_logprobs"
	AttrRespTopP             = "gen_ai.responses.top_p"
	AttrRespToolChoiceType   = "gen_ai.responses.tool_choice_type"
	AttrRespToolChoiceName   = "gen_ai.responses.tool_choice_name"
	AttrRespTruncation       = "gen_ai.responses.truncation"
	AttrRespTools            = "gen_ai.responses.tools"

	// Batch Operation Attributes
	AttrBatchID             = "gen_ai.batch.id"
	AttrBatchStatus         = "gen_ai.batch.status"
	AttrBatchObject         = "gen_ai.batch.object"
	AttrBatchEndpoint       = "gen_ai.batch.endpoint"
	AttrBatchInputFileID    = "gen_ai.batch.input_file_id"
	AttrBatchOutputFileID   = "gen_ai.batch.output_file_id"
	AttrBatchErrorFileID    = "gen_ai.batch.error_file_id"
	AttrBatchCompletionWin  = "gen_ai.batch.completion_window"
	AttrBatchCreatedAt      = "gen_ai.batch.created_at"
	AttrBatchExpiresAt      = "gen_ai.batch.expires_at"
	AttrBatchRequestsCount  = "gen_ai.batch.requests_count"
	AttrBatchDataCount      = "gen_ai.batch.data_count"
	AttrBatchResultsCount   = "gen_ai.batch.results_count"
	AttrBatchHasMore        = "gen_ai.batch.has_more"
	AttrBatchMetadata       = "gen_ai.batch.metadata"
	AttrBatchLimit          = "gen_ai.batch.limit"
	AttrBatchAfter          = "gen_ai.batch.after"
	AttrBatchBeforeID       = "gen_ai.batch.before_id"
	AttrBatchAfterID        = "gen_ai.batch.after_id"
	AttrBatchPageToken      = "gen_ai.batch.page_token"
	AttrBatchPageSize       = "gen_ai.batch.page_size"
	AttrBatchCountTotal     = "gen_ai.batch.request_counts.total"
	AttrBatchCountCompleted = "gen_ai.batch.request_counts.completed"
	AttrBatchCountFailed    = "gen_ai.batch.request_counts.failed"
	AttrBatchFirstID        = "gen_ai.batch.first_id"
	AttrBatchLastID         = "gen_ai.batch.last_id"
	AttrBatchInProgressAt   = "gen_ai.batch.in_progress_at"
	AttrBatchFinalizingAt   = "gen_ai.batch.finalizing_at"
	AttrBatchCompletedAt    = "gen_ai.batch.completed_at"
	AttrBatchFailedAt       = "gen_ai.batch.failed_at"
	AttrBatchExpiredAt      = "gen_ai.batch.expired_at"
	AttrBatchCancellingAt   = "gen_ai.batch.cancelling_at"
	AttrBatchCancelledAt    = "gen_ai.batch.cancelled_at"
	AttrBatchNextCursor     = "gen_ai.batch.next_cursor"

	// Transcription Response Attributes
	AttrInputTokenDetailsText  = "gen_ai.usage.input_token_details.text_tokens"
	AttrInputTokenDetailsAudio = "gen_ai.usage.input_token_details.audio_tokens"

	// Responses API usage detail attributes
	AttrInputTokenDetailsImage         = "gen_ai.usage.input_token_details.image_tokens"
	AttrInputTokenDetailsCachedRead    = "gen_ai.usage.input_token_details.cached_read_tokens"
	AttrInputTokenDetailsCachedWrite   = "gen_ai.usage.input_token_details.cached_write_tokens"
	AttrInputTokenDetailsCachedWrite5m = "gen_ai.usage.input_token_details.cached_write_tokens_5m"
	AttrInputTokenDetailsCachedWrite1h = "gen_ai.usage.input_token_details.cached_write_tokens_1h"
	AttrOutputTokenDetailsText         = "gen_ai.usage.output_token_details.text_tokens"
	AttrOutputTokenDetailsAudio        = "gen_ai.usage.output_token_details.audio_tokens"
	AttrOutputTokenDetailsImage        = "gen_ai.usage.output_token_details.image_tokens"
	AttrOutputTokenDetailsReason       = "gen_ai.usage.output_token_details.reasoning_tokens"
	AttrOutputTokenDetailsAccept       = "gen_ai.usage.output_token_details.accepted_prediction_tokens"
	AttrOutputTokenDetailsReject       = "gen_ai.usage.output_token_details.rejected_prediction_tokens"
	AttrOutputTokenDetailsCite         = "gen_ai.usage.output_token_details.citation_tokens"
	AttrOutputTokenDetailsSearch       = "gen_ai.usage.output_token_details.num_search_queries"

	// Tool execution attributes (OTel GenAI spec) used on MCP tool spans.
	AttrToolName          = "gen_ai.tool.name"
	AttrToolCallID        = "gen_ai.tool.call.id"
	AttrToolCallArguments = "gen_ai.tool.call.arguments"
	AttrToolCallResult    = "gen_ai.tool.call.result"
	AttrToolType          = "gen_ai.tool.type"

	// OTel MCP semconv attributes on mcp.client spans, read by the duration metric.
	AttrMCPMethodName    = "mcp.method.name"   // e.g. tools/call, tools/list, ping
	AttrNetworkTransport = "network.transport" // pipe (stdio) | tcp (http/sse)

	// Tool-execution latency (ms) — the raw CallTool round-trip — so the duration metric
	// measures it, not span wall-time (which covers the PostHooks). Bifrost-namespaced; not
	// OTel MCP semconv.
	AttrBifrostMCPToolDurationMs = "bifrost.mcp.tool.duration_ms"

	// =====================================================================
	// Bifrost-namespaced attributes (bifrost.*)
	//
	// Canonical home for everything that is NOT part of the OTel GenAI spec:
	//   - Bifrost-internal concepts (routing/governance, request id, retry counters)
	//   - Raw Bifrost short names that mirror canonicalized gen_ai.* values
	//   - Back-compat fallbacks for shape changes (e.g. comma-joined stop_sequences)
	//
	// The corresponding legacy gen_ai.* emissions are tagged "// legacy:" at their
	// call sites and will be removed once dashboards migrate over.
	// =====================================================================
	// Cumulative time (float64 ms) the request spent blocked on sockets outside
	// Bifrost — every provider attempt, plus MCP tool calls and media fetches.
	// Stamped on the ROOT span once per request, so connectors derive Bifrost's
	// own cost as (root span duration - this). Deliberately not a per-attempt
	// value: retries and fallbacks all contribute to the same total.
	AttrBifrostUpstreamDurationMs = "bifrost.upstream.duration_ms"

	// Bifrost's own cost (float64 ms): root span duration minus the upstream
	// total above. Stamped on the ROOT span at export time, since the root's
	// duration is not known while the request is still running. Connectors read
	// this rather than re-deriving it, so the span and the overhead metric can
	// never disagree.
	AttrBifrostOverheadDurationMs = "bifrost.overhead.duration_ms"

	AttrBifrostProviderName        = "bifrost.provider.name"
	AttrBifrostRequestID           = "bifrost.request.id"
	AttrBifrostVirtualKeyID        = "bifrost.virtual_key.id"
	AttrBifrostVirtualKeyName      = "bifrost.virtual_key.name"
	AttrBifrostSelectedKeyID       = "bifrost.selected_key.id"
	AttrBifrostSelectedKeyName     = "bifrost.selected_key.name"
	AttrBifrostRoutingRuleID       = "bifrost.routing_rule.id"
	AttrBifrostRoutingRuleName     = "bifrost.routing_rule.name"
	AttrBifrostTeamID              = "bifrost.team.id"
	AttrBifrostTeamName            = "bifrost.team.name"
	AttrBifrostCustomerID          = "bifrost.customer.id"
	AttrBifrostCustomerName        = "bifrost.customer.name"
	AttrBifrostBusinessUnitID      = "bifrost.business_unit.id"
	AttrBifrostBusinessUnitName    = "bifrost.business_unit.name"
	AttrBifrostTeamIDs             = "bifrost.team.ids"
	AttrBifrostTeamNames           = "bifrost.team.names"
	AttrBifrostCustomerIDs         = "bifrost.customer.ids"
	AttrBifrostCustomerNames       = "bifrost.customer.names"
	AttrBifrostBusinessUnitIDs     = "bifrost.business_unit.ids"
	AttrBifrostBusinessUnitNames   = "bifrost.business_unit.names"
	AttrBifrostUserID              = "bifrost.user.id"
	AttrBifrostUserName            = "bifrost.user.name"
	AttrBifrostUserEmail           = "bifrost.user.email"
	AttrBifrostRetries             = "bifrost.retries"
	AttrBifrostFallbackIndex       = "bifrost.fallback_index"
	AttrBifrostAlias               = "bifrost.alias"               // original requested model when it differs from the resolved model
	AttrBifrostRoutingEngineUsed   = "bifrost.routing_engine_used" // comma-joined routing engines that handled the request
	AttrBifrostStopSequencesJoined = "bifrost.request.stop_sequences"

	// OTel general semconv (no gen_ai prefix). Emitted alongside the legacy
	// gen_ai.error.type from PopulateErrorAttributes.
	AttrErrorTypeSpec = "error.type"

	// legacy: bare unprefixed keys retained for back-compat with existing dashboards.
	// "request.type" is superseded by AttrOperationName; "retry.count" has no spec
	// equivalent but stays under bifrost.retries going forward.
	AttrLegacyRequestType = "request.type"
	AttrLegacyRetryCount  = "retry.count"

	// File Operation Attributes
	AttrFileID             = "gen_ai.file.id"
	AttrFileObject         = "gen_ai.file.object"
	AttrFileFilename       = "gen_ai.file.filename"
	AttrFilePurpose        = "gen_ai.file.purpose"
	AttrFileBytes          = "gen_ai.file.bytes"
	AttrFileCreatedAt      = "gen_ai.file.created_at"
	AttrFileStatus         = "gen_ai.file.status"
	AttrFileStorageBackend = "gen_ai.file.storage_backend"
	AttrFileDataCount      = "gen_ai.file.data_count"
	AttrFileHasMore        = "gen_ai.file.has_more"
	AttrFileDeleted        = "gen_ai.file.deleted"
	AttrFileContentType    = "gen_ai.file.content_type"
	AttrFileContentBytes   = "gen_ai.file.content_bytes"
	AttrFileLimit          = "gen_ai.file.limit"
	AttrFileAfter          = "gen_ai.file.after"
	AttrFileOrder          = "gen_ai.file.order"
)

// RedactedAttrValue is the placeholder recorded in place of a sensitive header
// value, following the OpenTelemetry HTTP semantic-convention guidance for
// redacting credentials.
const RedactedAttrValue = "REDACTED"

// IsSensitiveHeader reports whether a header name carries credentials that must
// not be exported verbatim into span attributes. The match is case-insensitive
// and trims surrounding whitespace so callers using the core SDK directly (which
// bypass the transport-layer security denylist) are still protected. Beyond the
// well-known exact names, substring/suffix patterns catch credential-bearing
// variants like x-auth-token, x-amz-security-token, and provider-specific
// *-api-key headers.
func IsSensitiveHeader(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))

	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}

	return strings.Contains(normalized, "api-key") ||
		strings.Contains(normalized, "authorization") ||
		strings.Contains(normalized, "secret") ||
		strings.HasSuffix(normalized, "-token") ||
		strings.HasSuffix(normalized, "_token")
}
