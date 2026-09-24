// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"maps"
	"strings"
	"sync"
	"time"
)

// Trace represents a distributed trace that captures the full lifecycle of a request
type Trace struct {
	RequestID             string            // Request ID for the trace
	TraceID               string            // Exported trace identifier; inherited from the W3C traceparent header when present, so concurrent requests of one distributed trace may share it
	InternalID            string            // Unique per-request handle the trace is stored under; unlike TraceID it is never shared across requests
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

// GetStringAttr returns a string attribute, or "" when absent or another type.
func GetStringAttr(attrs map[string]any, key string) string {
	v, _ := attrs[key].(string)
	return v
}

// GetBoolAttr returns a bool attribute, or false when absent or another type.
func GetBoolAttr(attrs map[string]any, key string) bool {
	v, _ := attrs[key].(bool)
	return v
}

// ContentLoggingDisabledForTrace reports whether the content-logging layers of the request behind
// this trace resolved to off (AttrBifrostContentLoggingDisabled true on the root span). Every
// connector ORs this into its own disable_content_logging: the layers can only tighten what a
// connector exports, never loosen it. Nil-safe, so a connector can call it on any trace.
func ContentLoggingDisabledForTrace(t *Trace) bool {
	if t == nil || t.RootSpan == nil {
		return false
	}
	return GetBoolAttr(t.RootSpan.Attributes, AttrBifrostContentLoggingDisabled)
}

// GetInt64Attr returns an integer attribute, widening int and float64.
func GetInt64Attr(attrs map[string]any, key string) int64 {
	switch v := attrs[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

// GetIntAttr is GetInt64Attr narrowed to int, for callers whose field is int.
func GetIntAttr(attrs map[string]any, key string) int {
	return int(GetInt64Attr(attrs, key))
}

// GetFloat64AttrOK returns a numeric attribute and whether one was present.
func GetFloat64AttrOK(attrs map[string]any, key string) (float64, bool) {
	switch v := attrs[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

// GetFloat64Attr returns a numeric attribute, or 0 when absent.
func GetFloat64Attr(attrs map[string]any, key string) float64 {
	v, _ := GetFloat64AttrOK(attrs, key)
	return v
}

// TraceSessionID returns the session ID trace attribute, or "" when absent.
func TraceSessionID(attrs map[string]any) string {
	v, _ := attrs[TraceAttrSessionID].(string)
	return v
}

// TraceDimensions returns the x-bf-dim-* dimensions, or nil when absent.
// Accepts map[string]any too: a trace decoded from JSON arrives that way.
func TraceDimensions(attrs map[string]any) map[string]string {
	switch m := attrs[TraceAttrDimensions].(type) {
	case map[string]string:
		return m
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		return out
	}
	return nil
}

// AddSpan adds a span to the trace in a thread-safe manner
func (t *Trace) AddSpan(span *Span) {
	if t == nil || span == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Spans = append(t.Spans, span)
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

// FinalAttemptSpan returns the last-ending LLM or retry span, which is the
// attempt a trace's metrics are labelled from. Datadog and Splunk each had a
// copy and they drifted — only one guarded against a nil span.
func FinalAttemptSpan(trace *Trace) *Span {
	if trace == nil {
		return nil
	}
	var final *Span
	for _, span := range trace.Spans {
		if span == nil {
			continue
		}
		if span.Kind != SpanKindLLMCall && span.Kind != SpanKindRetry {
			continue
		}
		if final == nil || span.EndTime.After(final.EndTime) {
			final = span
		}
	}
	return final
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
		InternalID:     t.InternalID,
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

// overheadBreakdownSpanNames are internal phase spans emitted solely to build the
// log-detail overhead breakdown. They are withheld from observability connectors (see
// IsOverheadBreakdownSpan); only a plugin that opts in via OverheadSpanConsumer (the
// logging plugin) receives them.
var overheadBreakdownSpanNames = map[string]struct{}{
	"request-unmarshal":    {},
	"request-marshal":      {},
	"response-parse":       {},
	"response-marshal":     {},
	"convertor":            {},
	"queue-wait":           {},
	"attribute-population": {},
}

// IsOverheadBreakdownSpan reports whether a span exists only to feed the overhead
// breakdown and should not be exported to observability connectors: the internal phase
// spans, the middleware.* auth spans, and the plugin transport-hook stages. Note
// key.selection is deliberately NOT included — it predates the breakdown and carries
// chosen-key attributes worth keeping in traces.
func IsOverheadBreakdownSpan(span *Span) bool {
	if span == nil {
		return false
	}
	switch span.Kind {
	case SpanKindInternal:
		if _, ok := overheadBreakdownSpanNames[span.Name]; ok {
			return true
		}
		return strings.HasPrefix(span.Name, "middleware.")
	case SpanKindPlugin:
		return strings.HasSuffix(span.Name, ".transportprehook") || strings.HasSuffix(span.Name, ".transportposthook")
	}
	return false
}

// WithoutOverheadBreakdownSpans returns a copy of the trace whose Spans slice omits the
// overhead-breakdown spans. It is meant to be called on an export snapshot (which has
// no live writers), so the returned trace shares that snapshot's metadata and its kept
// span pointers; only the span slice differs and reparented spans are copied. A
// retained span parented to an omitted one is reparented to the omitted span's own
// parent so the exported hierarchy stays connected (breakdown spans are leaves today,
// so this is defensive). Returns the receiver unchanged when nothing is stripped, so
// the common path allocates nothing.
func (t *Trace) WithoutOverheadBreakdownSpans() *Trace {
	if t == nil {
		return t
	}
	var omittedParent map[string]string
	for _, s := range t.Spans {
		if IsOverheadBreakdownSpan(s) {
			if omittedParent == nil {
				omittedParent = make(map[string]string)
			}
			omittedParent[s.SpanID] = s.ParentID
		}
	}
	if len(omittedParent) == 0 {
		return t
	}
	kept := make([]*Span, 0, len(t.Spans)-len(omittedParent))
	for _, s := range t.Spans {
		if s == nil {
			continue
		}
		if _, drop := omittedParent[s.SpanID]; drop {
			continue
		}
		if _, reparent := omittedParent[s.ParentID]; reparent {
			// Walk up past any chained omitted ancestors to the nearest kept parent.
			parentID := s.ParentID
			for range omittedParent {
				next, still := omittedParent[parentID]
				if !still {
					break
				}
				parentID = next
			}
			cp := s.snapshotForExport()
			cp.ParentID = parentID
			kept = append(kept, cp)
			continue
		}
		kept = append(kept, s)
	}
	// New Trace (fresh zero mutex, intentional) sharing the snapshot's metadata; only
	// the span slice differs.
	return &Trace{
		RequestID:             t.RequestID,
		TraceID:               t.TraceID,
		InternalID:            t.InternalID,
		ParentID:              t.ParentID,
		RootSpan:              t.RootSpan,
		Spans:                 kept,
		StartTime:             t.StartTime,
		EndTime:               t.EndTime,
		Attributes:            t.Attributes,
		RequestHeaders:        t.RequestHeaders,
		PluginLogs:            t.PluginLogs,
		redactionReplacements: t.redactionReplacements,
	}
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
	t.RequestID = ""
	t.TraceID = ""
	t.InternalID = ""
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
		AttrPrompt, AttrInstructions, AttrSuffix,
		AttrTools, AttrToolChoiceType, AttrToolChoiceName,
		AttrRespTools, AttrRespToolChoiceType, AttrRespToolChoiceName,
		AttrBifrostRawRequest:
		return traceContentAttributeScopeInput
	case AttrOutputMessages, AttrRespReasoningText, AttrBifrostRawResponse:
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
	// The typed payload is a separate carrier from Attributes, so it needs its own
	// pass or connectors reading it would see unredacted content.
	if span.LLM != nil {
		span.LLM.redact(inputReplacements, outputReplacements)
	}
}

// Span represents a single operation within a trace
type Span struct {
	SpanID     string          // Unique identifier for this span
	ParentID   string          // Parent span ID (empty for root span)
	TraceID    string          // The trace this span belongs to
	Name       string          // Name of the operation
	Kind       SpanKind        // Type of span (LLM call, plugin, etc.)
	StartTime  time.Time       // When the span started
	EndTime    time.Time       // When the span completed
	Status     SpanStatus      // Status of the operation
	StatusMsg  string          // Optional status message (for errors)
	Attributes map[string]any  // Additional attributes for the span
	LLM        *LLMSpanData    `json:"-"`
	Enrichment *SpanEnrichment // Governance/identity dimensions read off the request context
	Events     []SpanEvent     // Events that occurred during the span
	mu         sync.Mutex      // Mutex for thread-safe attribute operations
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

// GetAttribute reads one attribute under the span's lock.
func (s *Span) GetAttribute(key string) (any, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Attributes[key]
	return v, ok
}

// SetAttributes merges an already-built attribute map into the span under a
// single lock. Preferred over a caller-side range + SetAttribute loop (one lock
// per entry) when the map already exists (e.g. the Populate*Attributes output).
// Nil values are skipped, matching SetAttribute.
func (s *Span) SetAttributes(attrs map[string]any) {
	if s == nil || len(attrs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Attributes == nil {
		s.Attributes = make(map[string]any, len(attrs))
	}
	for k, v := range attrs {
		if v == nil {
			continue
		}
		s.Attributes[k] = v
	}
}

// snapshotForExport returns a copy of the span whose Attributes (and Events)
// are cloned under the span lock, so observability exporters can read them
// concurrently while a late writer (streaming finalization, redaction) may still
// mutate the original span. See Trace.SnapshotForExport. The returned span has a
// fresh zero-value mutex and its attribute values are copied by reference.
func (s *Span) snapshotForExport() *Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := snapshotSpanPool.Get().(*Span)
	cp.SpanID = s.SpanID
	cp.ParentID = s.ParentID
	cp.TraceID = s.TraceID
	cp.Name = s.Name
	cp.Kind = s.Kind
	cp.StartTime = s.StartTime
	cp.EndTime = s.EndTime
	cp.Status = s.Status
	cp.StatusMsg = s.StatusMsg
	cp.LLM = s.LLM
	cp.Enrichment = s.Enrichment
	clear(cp.Attributes)
	for k, v := range s.Attributes {
		cp.Attributes[k] = v
	}
	cp.Events = cp.Events[:0]
	for i := range s.Events {
		cp.Events = append(cp.Events, SpanEvent{
			Name:       s.Events[i].Name,
			Timestamp:  s.Events[i].Timestamp,
			Attributes: maps.Clone(s.Events[i].Attributes),
		})
	}
	return cp
}

// snapshotSpanPool backs the export snapshots. A snapshot's lifetime is bounded
// by the flush's wg.Wait(), so it can be recycled once every connector has
// returned - see Trace.ReleaseSnapshot.
var snapshotSpanPool = sync.Pool{
	New: func() any {
		return &Span{
			Attributes: make(map[string]any, 32),
			Events:     make([]SpanEvent, 0, 4),
		}
	},
}

// ReleaseSnapshot returns a snapshot's spans to the pool. Call it only after
// every connector has finished reading, and only on a snapshot - never on the
// live trace. Attribute maps are retained for reuse; an outlier-sized one is
// dropped so a single huge request cannot pin it.
func (t *Trace) ReleaseSnapshot() {
	if t == nil {
		return
	}
	for _, span := range t.Spans {
		if span == nil {
			continue
		}
		// Reuse the maps and the event backing array, but drop every value: a
		// pooled span otherwise holds message content live until its next use.
		attrs := span.Attributes
		if len(attrs) > 256 {
			attrs = make(map[string]any, 32)
		} else {
			clear(attrs)
		}
		for i := range span.Events {
			span.Events[i] = SpanEvent{}
		}
		events := span.Events[:0]
		// Whole-struct reset, so a field added to Span later cannot be missed here.
		*span = Span{Attributes: attrs, Events: events}
		snapshotSpanPool.Put(span)
	}
	t.Spans = nil
	t.RootSpan = nil
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

// EndIfMatch ends the span only if its SpanID still equals id, and reports whether
// it did. It exists for the tracer's cached-pointer fast path: a span pooled by
// ReleaseTrace has its SpanID cleared under this same lock (see Reset), and a span
// reused by another trace carries a different SpanID, so a stale handle fails the
// check and the caller falls back to the by-ID store lookup instead of mutating a
// recycled span. The check rides inside the lock End already takes, so it adds no
// extra locking.
func (s *Span) EndIfMatch(id string, status SpanStatus, statusMsg string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SpanID != id {
		return false
	}
	s.EndTime = time.Now()
	s.Status = status
	s.StatusMsg = statusMsg
	return true
}

// SetAttributeIfMatch sets an attribute only if the span's SpanID still equals id,
// and reports whether it did. Same recycling guard as EndIfMatch, for the tracer's
// cached-pointer fast path.
func (s *Span) SetAttributeIfMatch(id, key string, value any) bool {
	if s == nil || value == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SpanID != id {
		return false
	}
	if s.Attributes == nil {
		s.Attributes = make(map[string]any)
	}
	s.Attributes[key] = value
	return true
}

// MatchesID reports whether the span's SpanID still equals id, read under the span
// lock so it does not race a concurrent Reset. Used by the tracer to decide whether
// a cached span pointer is still the one the handle refers to before returning it.
func (s *Span) MatchesID(id string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SpanID == id
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
	s.LLM = nil
	s.Enrichment = nil
	// Reuse the attribute map across pool cycles: tracing/store.go clears and
	// refills it, so nil-ing here forced a fresh map alloc per pooled span every
	// request. Drop only an outlier-sized map so one huge request can't pin it.
	if len(s.Attributes) > 64 {
		s.Attributes = nil
	} else {
		clear(s.Attributes)
	}
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
	AttrChoiceCount      = "gen_ai.request.choice.count"
	// AttrEmbeddingsDimensionCount is the OTel spec key for embedding dimensions.
	AttrEmbeddingsDimensionCount = "gen_ai.embeddings.dimension.count"
	AttrSeed                     = "gen_ai.request.seed"
	AttrSuffix                   = "gen_ai.request.suffix"
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
	AttrTimeToFirstChunk = "gen_ai.response.time_to_first_chunk"
	AttrTotalChunks      = "gen_ai.response.total_chunks"

	// Plugin Attributes (for aggregated streaming post-hook spans)
	AttrPluginInvocations     = "plugin.invocation_count"
	AttrPluginAvgDurationMs   = "plugin.avg_duration_ms"
	AttrPluginTotalDurationMs = "plugin.total_duration_ms"
	AttrPluginErrorCount      = "plugin.error_count"

	// Usage Attributes
	AttrTotalTokens  = "gen_ai.usage.total_tokens"
	AttrInputTokens  = "gen_ai.usage.input_tokens"
	AttrOutputTokens = "gen_ai.usage.output_tokens"
	AttrUsageCost    = "gen_ai.usage.cost"

	// Cost breakdown, under bifrost.* rather than gen_ai.*: OTel has no cost
	// convention and closed the proposal for one (semantic-conventions#1062),
	// leaving cost to the observability platform. AttrUsageCost above predates
	// that and stays for compatibility.
	//
	// Input/output/additional sum to AttrUsageCost; each side's categories sum to
	// that side. The pricing engine produces all of it on the same call that
	// produces the total, so emitting it is free.
	AttrBifrostCostInput      = "bifrost.cost.input"
	AttrBifrostCostOutput     = "bifrost.cost.output"
	AttrBifrostCostAdditional = "bifrost.cost.additional"

	AttrBifrostCostInputText        = "bifrost.cost.input.text"
	AttrBifrostCostInputAudio       = "bifrost.cost.input.audio"
	AttrBifrostCostInputImage       = "bifrost.cost.input.image"
	AttrBifrostCostInputCachedRead  = "bifrost.cost.input.cached_read"
	AttrBifrostCostInputCachedWrite = "bifrost.cost.input.cached_write"
	AttrBifrostCostInputRequest     = "bifrost.cost.input.request"

	AttrBifrostCostOutputText      = "bifrost.cost.output.text"
	AttrBifrostCostOutputAudio     = "bifrost.cost.output.audio"
	AttrBifrostCostOutputImage     = "bifrost.cost.output.image"
	AttrBifrostCostOutputReasoning = "bifrost.cost.output.reasoning"
	AttrBifrostCostOutputCitation  = "bifrost.cost.output.citation"
	AttrBifrostCostOutputSearch    = "bifrost.cost.output.search_queries"

	// Sidecar spend that maps to no token category, and is billed to the request
	// without being produced by the model. Not reconcilable against a provider
	// invoice, so worth slicing separately.
	AttrBifrostCostGuardrail     = "bifrost.cost.additional.guardrail"
	AttrBifrostCostMCP           = "bifrost.cost.additional.mcp"
	AttrBifrostCostSemanticCache = "bifrost.cost.additional.semantic_cache"
	AttrBifrostCostRouting       = "bifrost.cost.additional.routing"
	// OTel GenAI spec keys for cache tokens (flat namespace).
	AttrUsageCacheReadInputTokens     = "gen_ai.usage.cache_read.input_tokens"
	AttrUsageCacheCreationInputTokens = "gen_ai.usage.cache_creation.input_tokens"
	// OTel GenAI spec key for reasoning tokens (flat namespace).
	AttrUsageReasoningOutputTokens = "gen_ai.usage.reasoning.output_tokens"
	// Chat completion usage detail attributes. These non-cached fields have no OTel
	// spec equivalent and stay as-is; cache tokens use the flat gen_ai.usage.cache_*
	// keys (AttrUsageCacheReadInputTokens / AttrUsageCacheCreationInputTokens).
	AttrPromptTokenDetailsText          = "gen_ai.usage.prompt_token_details.text_tokens"
	AttrPromptTokenDetailsAudio         = "gen_ai.usage.prompt_token_details.audio_tokens"
	AttrPromptTokenDetailsImage         = "gen_ai.usage.prompt_token_details.image_tokens"
	AttrPromptTokenDetailsCachedWrite5m = "gen_ai.usage.prompt_token_details.cached_write_tokens_5m"
	AttrPromptTokenDetailsCachedWrite1h = "gen_ai.usage.prompt_token_details.cached_write_tokens_1h"
	AttrCompletionTokenDetailsText      = "gen_ai.usage.completion_token_details.text_tokens"
	AttrCompletionTokenDetailsAudio     = "gen_ai.usage.completion_token_details.audio_tokens"
	AttrCompletionTokenDetailsImage     = "gen_ai.usage.completion_token_details.image_tokens"
	AttrCompletionTokenDetailsAccept    = "gen_ai.usage.completion_token_details.accepted_prediction_tokens"
	AttrCompletionTokenDetailsReject    = "gen_ai.usage.completion_token_details.rejected_prediction_tokens"
	AttrCompletionTokenDetailsCite      = "gen_ai.usage.completion_token_details.citation_tokens"
	AttrCompletionTokenDetailsSearch    = "gen_ai.usage.completion_token_details.num_search_queries"

	// Error Attributes
	AttrError     = "gen_ai.error"
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
	AttrInputTokenDetailsCachedWrite   = "gen_ai.usage.input_token_details.cached_write_tokens"
	AttrInputTokenDetailsCachedWrite5m = "gen_ai.usage.input_token_details.cached_write_tokens_5m"
	AttrInputTokenDetailsCachedWrite1h = "gen_ai.usage.input_token_details.cached_write_tokens_1h"
	AttrOutputTokenDetailsText         = "gen_ai.usage.output_token_details.text_tokens"
	AttrOutputTokenDetailsAudio        = "gen_ai.usage.output_token_details.audio_tokens"
	AttrOutputTokenDetailsImage        = "gen_ai.usage.output_token_details.image_tokens"
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

	// Per-chunk streaming overhead split out of the "core" bucket so a stream's
	// breakdown reads like a unary request's. All float64 ms, stamped on the ROOT
	// span at stream completion. parse/convert fold into the same buckets as their
	// unary equivalents (response-parse, convertor); backpressure has no unary twin
	// and is not Bifrost CPU (the transport/client draining slowly).
	//   - parse:        per-event SSE JSON decode CPU
	//   - convert:      per-event provider->Bifrost struct mapping CPU
	//   - backpressure: time the provider goroutine blocked handing chunks to the transport
	AttrBifrostStreamParseMs        = "bifrost.stream.parse_ms"
	AttrBifrostStreamConvertMs      = "bifrost.stream.convert_ms"
	AttrBifrostStreamBackpressureMs = "bifrost.stream.backpressure_ms"
	// Transport-goroutine per-chunk split, stamped after the send loop drains.
	// Concurrent with the provider, so NOT part of the overhead total: used only
	// as weights to split backpressure into (A) client-write vs (B) transport CPU.
	//   - transport_cpu: outbound Bifrost->client convert + marshal CPU
	//   - client_write:  time blocked writing frames to the client socket
	AttrBifrostStreamTransportCPUMs = "bifrost.stream.transport_cpu_ms"
	AttrBifrostStreamClientWriteMs  = "bifrost.stream.client_write_ms"

	// AttrBifrostWorkerHandoffMs is the scheduling latency between the provider
	// worker sending the result and tryRequest receiving it (the worker->caller
	// goroutine hop). It is real wall-time inside the overhead window that sits on
	// no span; the breakdown carves it into its own "worker-handoff" bucket. The
	// reverse hop (enqueue->dequeue) is already the "queue-wait" span.
	AttrBifrostWorkerHandoffMs = "bifrost.worker.handoff_ms"

	// AttrBifrostContentLoggingDisabled is set to true on the root span when the request's
	// content-logging layers resolve to off (see StampContentLoggingDecision). Connectors read it
	// through ContentLoggingDisabledForTrace and strip content the way their own
	// disable_content_logging would. Only true acts: it is written false solely to undo an earlier
	// true when a higher layer turns content back on, so it can never loosen a connector's own
	// setting.
	AttrBifrostContentLoggingDisabled = "bifrost.content_logging.disabled"

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
	AttrBifrostProjectID           = "bifrost.project.id"
	AttrBifrostProjectName         = "bifrost.project.name"
	AttrBifrostTeamIDs             = "bifrost.team.ids"
	AttrBifrostTeamNames           = "bifrost.team.names"
	AttrBifrostCustomerIDs         = "bifrost.customer.ids"
	AttrBifrostCustomerNames       = "bifrost.customer.names"
	AttrBifrostBusinessUnitIDs     = "bifrost.business_unit.ids"
	AttrBifrostBusinessUnitNames   = "bifrost.business_unit.names"
	AttrBifrostUserID              = "bifrost.user.id"
	AttrBifrostUserName            = "bifrost.user.name"
	AttrBifrostUserEmail           = "bifrost.user.email"
	AttrBifrostApp                 = "bifrost.app"          // calling client, classified from User-Agent
	AttrBifrostRawRequest          = "bifrost.raw_request"  // raw provider request body; content, opt-in
	AttrBifrostRawResponse         = "bifrost.raw_response" // raw provider response body; content, opt-in
	AttrBifrostRetries             = "bifrost.retries"
	AttrBifrostFallbackIndex       = "bifrost.fallback_index"
	AttrBifrostAlias               = "bifrost.alias"                // original requested model when it differs from the resolved model
	AttrBifrostRoutingEngineUsed   = "bifrost.routing_engine_used"  // comma-joined routing engines that handled the request
	AttrBifrostComplexityTier      = "bifrost.complexity_tier"      // complexity tier used for routing (SIMPLE/MEDIUM/COMPLEX); absent when no rule referenced complexity_tier
	AttrBifrostComplexityMechanism = "bifrost.complexity_mechanism" // how the complexity tier was classified (semantic, llm, session, skipped)
	AttrBifrostComplexityScore     = "bifrost.complexity_score"     // numeric confidence score produced by complexity classification
	AttrBifrostStopSequencesJoined = "bifrost.request.stop_sequences"

	// AttrBifrostErrorType is the normalized ErrorType, so span-derived connectors
	// classify identically to the metrics. Absent on success.
	AttrBifrostErrorType = "bifrost.error.type"

	// OTel general semconv (no gen_ai prefix). The canonical error-type key,
	// emitted from PopulateErrorAttributes.
	AttrErrorTypeSpec = "error.type"

	// legacy: bare unprefixed key retained for back-compat with existing dashboards.
	// "request.type" is superseded by AttrOperationName, but still drives the live
	// "method" metric label and request_type column via EnrichmentDims.
	AttrLegacyRequestType = "request.type"

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

// Attribute keys that are declared but never emitted or read. Kept so external
// importers keep compiling; remove in a future major.
const (
	// Deprecated: use AttrUsageReasoningOutputTokens.
	AttrCompletionTokenDetailsReason = "gen_ai.usage.completion_token_details.reasoning_tokens"
	// Deprecated: use AttrUsageReasoningOutputTokens.
	AttrOutputTokenDetailsReason = "gen_ai.usage.output_token_details.reasoning_tokens"
	// Deprecated: use AttrUsageCacheReadInputTokens.
	AttrInputTokenDetailsCachedRead = "gen_ai.usage.input_token_details.cached_read_tokens"
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
//
// Identity-aware-proxy headers are covered explicitly: Cloudflare Access
// (cf-access-*, incl. the cf-access-jwt-assertion signed JWT) and AWS ALB OIDC
// (x-amzn-oidc-*) inject signed identity tokens the substring rules would miss,
// plus generic jwt/assertion-bearing headers.
func IsSensitiveHeader(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))

	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}

	return strings.Contains(normalized, "api-key") ||
		strings.Contains(normalized, "authorization") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "assertion") ||
		strings.Contains(normalized, "jwt") ||
		strings.HasPrefix(normalized, "cf-access-") ||
		strings.HasPrefix(normalized, "x-amzn-oidc-") ||
		strings.HasSuffix(normalized, "-token") ||
		strings.HasSuffix(normalized, "_token")
}
