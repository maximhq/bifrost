package traceingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Attributes the export puts on a connector trace and its spans, so a backend can tell an SDK trace
// from a gateway request and find the ids the SDK sent.
const (
	AttrTraceOrigin     = "bifrost.trace.origin" // TraceOriginSDK
	AttrTraceSource     = "bifrost.trace.source" // braintrust | langfuse | arize | otel
	AttrTraceProjectID  = "bifrost.trace.project_id"
	AttrTraceOriginalID = "bifrost.trace.original_id"
	AttrSpanOriginalID  = "bifrost.span.original_id"
	AttrSpanKind        = "bifrost.span.kind"
	// AttrSpanLinkMethod is set on a span whose model call this gateway served, and says how the two
	// were matched. The connectors receive that call once more as a gateway trace; a backend that
	// counts usage counts it there.
	AttrSpanLinkMethod = "bifrost.span.link_method"

	// TraceOriginSDK marks a trace received from an SDK, not made by a gateway request.
	TraceOriginSDK = "sdk"
)

// TraceExporter hands a trace to the observability connectors. The HTTP server implements it with
// the tracer's own fan-out, so an SDK trace reaches the same connectors as gateway traffic.
type TraceExporter interface {
	ExportTrace(ctx context.Context, trace *schemas.Trace)
}

// exportConcurrency bounds how many exports run at once, off the rollup loop.
const exportConcurrency = 4

// exportMemory is how long the export remembers which spans of a trace it sent. A span that arrives
// later than this after its trace was exported is sent again with the spans that came after it.
const exportMemory = time.Hour

type exportState struct {
	exporter TraceExporter
	slots    chan struct{}
	wg       sync.WaitGroup

	mu   sync.Mutex
	sent map[logstore.TraceKey]*exportedTrace
}

type exportedTrace struct {
	spans map[string]struct{}
	at    time.Time
}

// SetExporter sets where completed traces are sent. Without one, nothing is exported. Call it before
// the first request.
func (s *Service) SetExporter(exporter TraceExporter) {
	s.export = &exportState{exporter: exporter, slots: make(chan struct{}, exportConcurrency), sent: make(map[logstore.TraceKey]*exportedTrace)}
}

// exportTrace sends the spans of a completed trace the connectors have not received yet. The spans
// are read and converted here, and handed to the exporter off the rollup loop.
func (s *Service) exportTrace(ctx context.Context, trace *logstore.Trace) {
	state := s.export
	if state == nil {
		return
	}
	spans, err := s.store.ListTraceSpans(ctx, trace.ProjectID, trace.TraceID)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("traceingest: failed to load trace %s for export: %v", trace.TraceID, err)
		}
		return
	}
	key := logstore.TraceKey{ProjectID: trace.ProjectID, TraceID: trace.TraceID}
	now := time.Now()
	state.mu.Lock()
	for stale, entry := range state.sent {
		if now.Sub(entry.at) > exportMemory {
			delete(state.sent, stale)
		}
	}
	entry := state.sent[key]
	if entry == nil {
		entry = &exportedTrace{spans: make(map[string]struct{}, len(spans))}
		state.sent[key] = entry
	}
	fresh := make([]*logstore.TraceSpan, 0, len(spans))
	for _, span := range spans {
		if _, done := entry.spans[span.SpanID]; !done {
			entry.spans[span.SpanID] = struct{}{}
			fresh = append(fresh, span)
		}
	}
	entry.at = now
	state.mu.Unlock()
	if len(fresh) == 0 {
		return
	}
	converted := connectorTrace(trace, fresh)
	state.wg.Add(1)
	go func() {
		defer state.wg.Done()
		select {
		case state.slots <- struct{}{}:
			defer func() { <-state.slots }()
		case <-s.done:
			return
		}
		exportCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		state.exporter.ExportTrace(exportCtx, converted)
	}()
}

// waitForExports blocks until every export handed off so far has returned. Close calls it, and so do
// tests.
func (s *Service) waitForExports() {
	if s.export != nil {
		s.export.wg.Wait()
	}
}

// connectorTrace converts stored spans of one trace into the trace the connectors consume. Ids
// become W3C-sized hex ids (see connectorID), the GenAI fields go under their OTel GenAI names so
// every connector maps them as it maps gateway calls, and what the SDK sent is kept beside them.
func connectorTrace(trace *logstore.Trace, spans []*logstore.TraceSpan) *schemas.Trace {
	traceID := connectorID(trace.TraceID, 32)
	out := &schemas.Trace{
		TraceID:    traceID,
		InternalID: trace.ProjectID + ":" + trace.TraceID,
		StartTime:  trace.StartTime,
		Attributes: map[string]any{
			AttrTraceOrigin: TraceOriginSDK, AttrTraceSource: trace.Source, AttrTraceProjectID: trace.ProjectID, AttrTraceOriginalID: trace.TraceID,
		},
		Spans: make([]*schemas.Span, 0, len(spans)),
	}
	if trace.EndTime != nil {
		out.EndTime = *trace.EndTime
	}
	if trace.SessionID != "" {
		out.Attributes[schemas.TraceAttrSessionID] = trace.SessionID
	}
	for _, span := range spans {
		converted := connectorSpan(traceID, span)
		out.Spans = append(out.Spans, converted)
		if span.SpanID == trace.RootSpanID {
			out.RootSpan = converted
		}
	}
	return out
}

func connectorSpan(traceID string, span *logstore.TraceSpan) *schemas.Span {
	attributes := make(map[string]any, len(span.AttributesParsed)+16)
	for key, value := range span.AttributesParsed {
		attributes[key] = value
	}
	set := func(key string, value any) {
		switch v := value.(type) {
		case string:
			if v == "" {
				return
			}
		case int:
			if v == 0 {
				return
			}
		}
		attributes[key] = value
	}
	set(AttrSpanOriginalID, span.SpanID)
	set(AttrSpanKind, span.Kind)
	set(schemas.AttrOperationName, genAIOperation(span.Kind))
	set(schemas.AttrRequestModel, span.Model)
	set(schemas.AttrProviderName, span.Provider)
	set(schemas.AttrInputTokens, span.PromptTokens)
	set(schemas.AttrOutputTokens, span.CompletionTokens)
	set(schemas.AttrUsageCacheReadInputTokens, span.CachedReadTokens)
	set(schemas.AttrConversationID, span.SessionID)
	set("user.id", span.UserID)
	set(schemas.AttrInputMessages, span.Input)
	set(schemas.AttrOutputMessages, span.Output)
	if span.Cost != nil {
		attributes[schemas.AttrUsageCost] = *span.Cost
	}
	if span.LinkSnapshotParsed != nil {
		set(AttrSpanLinkMethod, span.LinkMethod)
	}
	for key, value := range span.MetadataParsed {
		if _, taken := attributes["metadata."+key]; !taken {
			attributes["metadata."+key] = value
		}
	}
	out := &schemas.Span{
		SpanID: connectorID(span.SpanID, 16), ParentID: connectorID(span.ParentSpanID, 16), TraceID: traceID,
		Name: span.Name, Kind: connectorKind(span.Kind), StartTime: span.StartTime, Status: schemas.SpanStatusOk,
		Attributes: attributes,
	}
	if span.EndTime != nil {
		out.EndTime = *span.EndTime
	}
	if span.Status == logstore.TraceSpanStatusError {
		out.Status, out.StatusMsg = schemas.SpanStatusError, span.Error
	}
	return out
}

// connectorKind maps a Bifrost span kind onto the core span kinds the connectors understand. Kinds
// with no core counterpart are internal spans; their Bifrost kind travels in AttrSpanKind.
func connectorKind(kind string) schemas.SpanKind {
	switch kind {
	case logstore.TraceSpanKindLLM:
		return schemas.SpanKindLLMCall
	case logstore.TraceSpanKindEmbedding:
		return schemas.SpanKindEmbedding
	case logstore.TraceSpanKindHTTP:
		return schemas.SpanKindHTTPRequest
	}
	return schemas.SpanKindInternal
}

// genAIOperation is the OTel GenAI operation name of a span kind, empty when there is none.
func genAIOperation(kind string) string {
	switch kind {
	case logstore.TraceSpanKindLLM:
		return "chat"
	case logstore.TraceSpanKindEmbedding:
		return "embeddings"
	case logstore.TraceSpanKindTool:
		return "execute_tool"
	case logstore.TraceSpanKindAgent:
		return "invoke_agent"
	}
	return ""
}

// connectorID returns id as a lowercase hex id of size characters (32 for a trace, 16 for a span).
// OTLP ids already are one and are kept. Anything else (a UUID, a Braintrust or Langfuse id) becomes
// the hex SHA-256 of it, cut to size: the same id always maps to the same connector id, and
// connectors that read ids as hex get a valid one.
func connectorID(id string, size int) string {
	if id == "" {
		return ""
	}
	if len(id) == size && isLowerHex(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:size]
}

func isLowerHex(id string) bool {
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
