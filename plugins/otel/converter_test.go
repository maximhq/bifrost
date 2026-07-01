package otel

import (
	"bytes"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func makeSpan(id, parentID, name string, kind schemas.SpanKind) *schemas.Span {
	return &schemas.Span{
		SpanID:    id,
		ParentID:  parentID,
		Name:      name,
		Kind:      kind,
		StartTime: time.Now(),
		EndTime:   time.Now(),
	}
}

// TestConvertTraceToResourceSpan_PluginSpanFilter exercises the OTEL converter's end-to-end
// filtering behavior (the parts unique to this package; the filter/reparent logic itself is
// covered by core/schemas/span_filter_test.go). It asserts that filtered plugin spans are
// dropped from the exported ResourceSpan and that an exported child whose direct parent was
// filtered is re-parented to the nearest exported ancestor.
func TestConvertTraceToResourceSpan_PluginSpanFilter(t *testing.T) {
	p := &OtelPlugin{pluginSpanFilter: &PluginSpanFilter{
		Mode:    PluginSpanFilterModeExclude,
		Plugins: []string{"logging"},
	}}

	// Span tree: root (internal) -> logging.prehook (filtered) -> governance.prehook (kept).
	root := makeSpan("aaaa", "", "request", schemas.SpanKindInternal)
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: root,
		Spans: []*schemas.Span{
			root,
			makeSpan("bbbb", "aaaa", "plugin.logging.prehook", schemas.SpanKindPlugin),
			makeSpan("cccc", "bbbb", "plugin.governance.prehook", schemas.SpanKindPlugin),
		},
	}

	rs := p.convertTraceToResourceSpan("svc", trace, nil, false, false, false, false)
	spans := rs.ScopeSpans[0].Spans

	// The filtered logging span is dropped; root + governance remain.
	if len(spans) != 2 {
		t.Fatalf("expected 2 exported spans (logging dropped), got %d", len(spans))
	}
	byID := make(map[string]*Span, len(spans))
	for _, s := range spans {
		byID[string(s.SpanId)] = s
	}
	if _, ok := byID[string(hexToBytes("bbbb", 8))]; ok {
		t.Error("filtered logging span should not be exported")
	}
	gov, ok := byID[string(hexToBytes("cccc", 8))]
	if !ok {
		t.Fatal("governance span should be exported")
	}
	// governance's direct parent (logging) was filtered, so its parent must be rewritten to
	// the nearest exported ancestor (root), not left dangling at the dropped logging span.
	if !bytes.Equal(gov.ParentSpanId, hexToBytes("aaaa", 8)) {
		t.Errorf("governance ParentSpanId = %x, want %x (reparented to root)", gov.ParentSpanId, hexToBytes("aaaa", 8))
	}
}

// TestConvertTraceToResourceSpan_DisableRootSpanContent asserts that the disableRootSpanContent
// flag drops content attributes from the root span only, leaving child (llm.call) spans with
// their full input/output content intact. This is the storage-saving knob that stops the
// framework's input/output duplication onto the root span from reaching the collector (BF-1512).
func TestConvertTraceToResourceSpan_DisableRootSpanContent(t *testing.T) {
	p := &OtelPlugin{pluginSpanFilter: &PluginSpanFilter{}}

	makeContentTrace := func() *schemas.Trace {
		root := makeSpan("aaaa", "", "request", schemas.SpanKindInternal)
		root.Attributes = map[string]any{
			schemas.AttrInputMessages:  "hello",
			schemas.AttrOutputMessages: "hi there",
			schemas.AttrRequestModel:   "gpt-4o-mini",
		}
		child := makeSpan("bbbb", "aaaa", "chat", schemas.SpanKindLLMCall)
		child.Attributes = map[string]any{
			schemas.AttrInputMessages:  `[{"role":"user","content":"hello"}]`,
			schemas.AttrOutputMessages: `[{"role":"assistant","content":"hi there"}]`,
		}
		return &schemas.Trace{
			TraceID:  "00000000000000000000000000000002",
			RootSpan: root,
			Spans:    []*schemas.Span{root, child},
		}
	}

	childSpan := func(spans []*Span) *Span {
		for _, s := range spans {
			if bytes.Equal(s.SpanId, hexToBytes("bbbb", 8)) {
				return s
			}
		}
		return nil
	}

	// Flag off: root keeps its content (current default behavior).
	off := p.convertTraceToResourceSpan("svc", makeContentTrace(), nil, false, false, false, false)
	if root := findRoot(off.ScopeSpans[0].Spans); attrString(root, schemas.AttrInputMessages) == "" {
		t.Error("with flag off, root span should retain input content")
	}

	// Flag on: root content dropped, request model retained, child content untouched.
	on := p.convertTraceToResourceSpan("svc", makeContentTrace(), nil, false, false, false, true)
	root := findRoot(on.ScopeSpans[0].Spans)
	if got := attrString(root, schemas.AttrInputMessages); got != "" {
		t.Errorf("root input content = %q, want empty when disableRootSpanContent is set", got)
	}
	if got := attrString(root, schemas.AttrOutputMessages); got != "" {
		t.Errorf("root output content = %q, want empty when disableRootSpanContent is set", got)
	}
	if got := attrString(root, schemas.AttrRequestModel); got != "gpt-4o-mini" {
		t.Errorf("root model = %q, want non-content metadata preserved", got)
	}
	child := childSpan(on.ScopeSpans[0].Spans)
	if attrString(child, schemas.AttrInputMessages) == "" {
		t.Error("child llm.call span should retain full input content")
	}
	if attrString(child, schemas.AttrOutputMessages) == "" {
		t.Error("child llm.call span should retain full output content")
	}
}

// TestSessionDerivedIDsAreDeterministicAndValid asserts the session-derived trace and parent
// span IDs are stable across calls and have the byte widths OTEL requires (16-byte trace,
// 8-byte span).
func TestSessionDerivedIDsAreDeterministicAndValid(t *testing.T) {
	const sess = "session-abc-123"
	if got := sessionTraceID(sess); got != sessionTraceID(sess) {
		t.Fatal("sessionTraceID is not deterministic")
	}
	if got := sessionParentSpanID(sess); got != sessionParentSpanID(sess) {
		t.Fatal("sessionParentSpanID is not deterministic")
	}
	if sessionTraceID(sess) == sessionTraceID("other-session") {
		t.Error("distinct sessions produced the same trace ID")
	}
	if n := len(sessionTraceID(sess)); n != 32 { // 16 bytes in hex
		t.Errorf("session trace ID hex length = %d, want 32", n)
	}
	if n := len(sessionParentSpanID(sess)); n != 16 { // 8 bytes in hex
		t.Errorf("session parent span ID hex length = %d, want 16", n)
	}
}

// attrString returns the string value of the named attribute on a span, or "" if absent.
func attrString(s *Span, key string) string {
	for _, kv := range s.Attributes {
		if kv.Key == key {
			return kv.Value.GetStringValue()
		}
	}
	return ""
}

// findRoot returns the exported root span (SpanID "aaaa") from a converted span slice.
func findRoot(spans []*Span) *Span {
	for _, s := range spans {
		if bytes.Equal(s.SpanId, hexToBytes("aaaa", 8)) {
			return s
		}
	}
	return nil
}

// makeSessionTrace builds a single-request trace (root + one LLM child) optionally carrying an
// x-bf-session-id attribute and an inbound traceparent parent on the root span.
func makeSessionTrace(traceID, sessionID, rootParentID string) *schemas.Trace {
	root := makeSpan("aaaa", rootParentID, "request", schemas.SpanKindInternal)
	child := makeSpan("bbbb", "aaaa", "chat", schemas.SpanKindLLMCall)
	tr := &schemas.Trace{
		TraceID:  traceID,
		RootSpan: root,
		Spans:    []*schemas.Span{root, child},
	}
	if sessionID != "" {
		tr.Attributes = map[string]any{"x-bf-session-id": sessionID}
	}
	return tr
}

// TestSessionGroupingOverridesTraceID asserts that, with grouping enabled and a session ID
// present (and no inbound traceparent), every span adopts the session-derived trace ID, two
// requests in the same session share that trace ID, and each root span is parented to the
// synthetic session span so they render as siblings.
func TestSessionGroupingOverridesTraceID(t *testing.T) {
	p := &OtelPlugin{}
	const sess = "user-42"
	wantTrace := hexToBytes(sessionTraceID(sess), 16)
	wantParent := hexToBytes(sessionParentSpanID(sess), 8)

	for _, original := range []string{"00000000000000000000000000000001", "00000000000000000000000000000002"} {
		rs := p.convertTraceToResourceSpan("svc", makeSessionTrace(original, sess, ""), nil, false, false, true, false)
		spans := rs.ScopeSpans[0].Spans
		if len(spans) != 2 {
			t.Fatalf("expected 2 spans, got %d", len(spans))
		}
		for _, s := range spans {
			if !bytes.Equal(s.TraceId, wantTrace) {
				t.Errorf("span %q TraceId = %x, want session trace %x", s.Name, s.TraceId, wantTrace)
			}
		}
		root := findRoot(spans)
		if root == nil {
			t.Fatal("root span not found")
		}
		if !bytes.Equal(root.ParentSpanId, wantParent) {
			t.Errorf("root ParentSpanId = %x, want session parent %x", root.ParentSpanId, wantParent)
		}
		if got := attrString(root, "session.id"); got != sess {
			t.Errorf("root session.id = %q, want %q", got, sess)
		}
	}
}

// TestSessionGroupingTraceparentWins asserts that an inbound W3C traceparent (root span has a
// ParentID) takes precedence: the request keeps its original trace ID and root parent even when
// grouping is enabled and a session ID is present.
func TestSessionGroupingTraceparentWins(t *testing.T) {
	p := &OtelPlugin{}
	const original = "0123456789abcdef0123456789abcdef"
	const inboundParent = "fedcba9876543210"
	rs := p.convertTraceToResourceSpan("svc", makeSessionTrace(original, "user-42", inboundParent), nil, false, false, true, false)
	spans := rs.ScopeSpans[0].Spans

	wantTrace := hexToBytes(original, 16)
	for _, s := range spans {
		if !bytes.Equal(s.TraceId, wantTrace) {
			t.Errorf("span %q TraceId = %x, want original %x (traceparent wins)", s.Name, s.TraceId, wantTrace)
		}
	}
	root := findRoot(spans)
	if root == nil {
		t.Fatal("root span not found")
	}
	if !bytes.Equal(root.ParentSpanId, hexToBytes(inboundParent, 8)) {
		t.Errorf("root ParentSpanId = %x, want inbound traceparent %x", root.ParentSpanId, hexToBytes(inboundParent, 8))
	}
	// session.id is still tagged even though grouping was skipped (traceparent wins).
	if got := attrString(root, "session.id"); got != "user-42" {
		t.Errorf("root session.id = %q, want %q", got, "user-42")
	}
}

// TestSessionGroupingDisabled asserts that with grouping off the original trace ID is kept and
// the root span has no parent, but the session ID is still tagged on the root as session.id.
func TestSessionGroupingDisabled(t *testing.T) {
	p := &OtelPlugin{}
	const original = "00000000000000000000000000000009"
	rs := p.convertTraceToResourceSpan("svc", makeSessionTrace(original, "user-42", ""), nil, false, false, false, false)
	spans := rs.ScopeSpans[0].Spans

	wantTrace := hexToBytes(original, 16)
	for _, s := range spans {
		if !bytes.Equal(s.TraceId, wantTrace) {
			t.Errorf("span %q TraceId = %x, want original %x", s.Name, s.TraceId, wantTrace)
		}
	}
	root := findRoot(spans)
	if root == nil {
		t.Fatal("root span not found")
	}
	if root.ParentSpanId != nil {
		t.Errorf("root ParentSpanId = %x, want nil (grouping disabled)", root.ParentSpanId)
	}
	if got := attrString(root, "session.id"); got != "user-42" {
		t.Errorf("root session.id = %q, want %q (always tagged)", got, "user-42")
	}
}

// TestNoSessionIDNoTag asserts that when no x-bf-session-id is present the root span carries no
// session.id attribute.
func TestNoSessionIDNoTag(t *testing.T) {
	p := &OtelPlugin{}
	const original = "0000000000000000000000000000000a"
	rs := p.convertTraceToResourceSpan("svc", makeSessionTrace(original, "", ""), nil, false, false, true, false)
	root := findRoot(rs.ScopeSpans[0].Spans)
	if root == nil {
		t.Fatal("root span not found")
	}
	if got := attrString(root, "session.id"); got != "" {
		t.Errorf("root session.id = %q, want empty (no session header)", got)
	}
	// With no session ID, grouping cannot apply: trace ID stays original.
	if !bytes.Equal(root.TraceId, hexToBytes(original, 16)) {
		t.Errorf("root TraceId = %x, want original %x", root.TraceId, hexToBytes(original, 16))
	}
}

// findAttr returns the OTEL KeyValue with the given key from a span, or nil if
// not present. Used by trace-attribute propagation tests below.
func findAttr(span *Span, key string) *KeyValue {
	for _, kv := range span.Attributes {
		if kv.Key == key {
			return kv
		}
	}
	return nil
}

// TestConvertTraceToResourceSpan_PropagatesTraceAttributesToChildSpans verifies
// that attributes set on trace.Attributes (e.g. via tracer.SetTraceAttributes
// from x-bf-dim-* headers) are merged onto every exported span, including the
// root and all child kinds (llm.call, plugin, retry).
func TestConvertTraceToResourceSpan_PropagatesTraceAttributesToChildSpans(t *testing.T) {
	p := &OtelPlugin{}

	root := makeSpan("root", "", "POST /chat", schemas.SpanKindHTTPRequest)
	llm := makeSpan("llm", "root", "llm.call", schemas.SpanKindLLMCall)
	plugin := makeSpan("plug", "root", "plugin.governance.prehook", schemas.SpanKindPlugin)
	retry := makeSpan("retry", "llm", "retry", schemas.SpanKindRetry)

	trace := &schemas.Trace{
		TraceID:  "0123456789abcdef0123456789abcdef",
		RootSpan: root,
		Spans:    []*schemas.Span{root, llm, plugin, retry},
		Attributes: map[string]any{
			"customer_id": "acme",
			"environment": "prod",
		},
	}

	rs := p.convertTraceToResourceSpan("", trace, nil, false, true, false, false)
	if rs == nil || len(rs.ScopeSpans) == 0 {
		t.Fatal("convertTraceToResourceSpan returned nil/empty result")
	}
	spans := rs.ScopeSpans[0].Spans
	if len(spans) != 4 {
		t.Fatalf("expected 4 exported spans, got %d", len(spans))
	}

	for _, sp := range spans {
		cust := findAttr(sp, "customer_id")
		if cust == nil {
			t.Errorf("span %q missing customer_id", sp.Name)
			continue
		}
		if got := cust.Value.GetStringValue(); got != "acme" {
			t.Errorf("span %q customer_id = %q, want %q", sp.Name, got, "acme")
		}
		env := findAttr(sp, "environment")
		if env == nil {
			t.Errorf("span %q missing environment", sp.Name)
			continue
		}
		if got := env.Value.GetStringValue(); got != "prod" {
			t.Errorf("span %q environment = %q, want %q", sp.Name, got, "prod")
		}
	}
}

// TestConvertTraceToResourceSpan_SpanAttributePrecedence verifies that when a
// trace-level attribute and a span-level attribute share the same key, the
// span-level value wins. Trace attributes are background context; explicit
// per-span values are authoritative.
func TestConvertTraceToResourceSpan_SpanAttributePrecedence(t *testing.T) {
	p := &OtelPlugin{}

	root := makeSpan("root", "", "POST /chat", schemas.SpanKindHTTPRequest)
	root.Attributes = map[string]any{"foo": "span-level"}

	trace := &schemas.Trace{
		TraceID:    "0123456789abcdef0123456789abcdef",
		RootSpan:   root,
		Spans:      []*schemas.Span{root},
		Attributes: map[string]any{"foo": "trace-level"},
	}

	rs := p.convertTraceToResourceSpan("", trace, nil, false, true, false, false)
	spans := rs.ScopeSpans[0].Spans
	if len(spans) != 1 {
		t.Fatalf("expected 1 exported span, got %d", len(spans))
	}

	foo := findAttr(spans[0], "foo")
	if foo == nil {
		t.Fatal("missing foo attribute")
	}
	if got := foo.Value.GetStringValue(); got != "span-level" {
		t.Errorf("foo = %q, want %q (span-level should override trace-level)", got, "span-level")
	}

	// Sanity: only one entry for the key — we did not append a duplicate.
	count := 0
	for _, kv := range spans[0].Attributes {
		if kv.Key == "foo" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one 'foo' entry on the span, got %d", count)
	}
}

// TestConvertTraceToResourceSpan_NoTraceAttributes_NoRegression verifies that a
// trace with empty/nil Attributes adds zero merged key-values — the existing
// behavior before this feature is preserved.
func TestConvertTraceToResourceSpan_NoTraceAttributes_NoRegression(t *testing.T) {
	p := &OtelPlugin{}

	root := makeSpan("root", "", "POST /chat", schemas.SpanKindHTTPRequest)
	root.Attributes = map[string]any{"http.method": "POST"}

	t.Run("nil Attributes", func(t *testing.T) {
		trace := &schemas.Trace{
			TraceID:  "0123456789abcdef0123456789abcdef",
			RootSpan: root,
			Spans:    []*schemas.Span{root},
		}
		rs := p.convertTraceToResourceSpan("", trace, nil, false, true, false, false)
		spans := rs.ScopeSpans[0].Spans
		// One attribute: http.method (no AttrRequestID because RequestID is unset).
		if len(spans[0].Attributes) != 1 {
			t.Errorf("expected 1 attribute, got %d (%v)", len(spans[0].Attributes), spans[0].Attributes)
		}
	})

	t.Run("empty Attributes", func(t *testing.T) {
		trace := &schemas.Trace{
			TraceID:    "0123456789abcdef0123456789abcdef",
			RootSpan:   root,
			Spans:      []*schemas.Span{root},
			Attributes: map[string]any{},
		}
		rs := p.convertTraceToResourceSpan("", trace, nil, false, true, false, false)
		spans := rs.ScopeSpans[0].Spans
		if len(spans[0].Attributes) != 1 {
			t.Errorf("expected 1 attribute, got %d (%v)", len(spans[0].Attributes), spans[0].Attributes)
		}
	})
}

// TestConvertTraceToResourceSpan_EmptyStringDimValue_NoCrash verifies that a
// trace attribute with an empty-string value is silently dropped (anyToKeyValue
// returns nil for ""). Without the nil-check in the merge loop, appending a nil
// *KeyValue here would crash OTLP marshaling downstream.
func TestConvertTraceToResourceSpan_EmptyStringDimValue_NoCrash(t *testing.T) {
	p := &OtelPlugin{}

	root := makeSpan("root", "", "POST /chat", schemas.SpanKindHTTPRequest)
	trace := &schemas.Trace{
		TraceID:  "0123456789abcdef0123456789abcdef",
		RootSpan: root,
		Spans:    []*schemas.Span{root},
		Attributes: map[string]any{
			"foo":         "", // empty string — anyToKeyValue returns nil
			"customer_id": "acme",
		},
	}

	rs := p.convertTraceToResourceSpan("", trace, nil, false, true, false, false)
	spans := rs.ScopeSpans[0].Spans
	if len(spans) != 1 {
		t.Fatalf("expected 1 exported span, got %d", len(spans))
	}

	if findAttr(spans[0], "foo") != nil {
		t.Error("empty-string dim 'foo' should have been dropped")
	}
	cust := findAttr(spans[0], "customer_id")
	if cust == nil {
		t.Fatal("non-empty dim 'customer_id' should still be present")
	}
	if got := cust.Value.GetStringValue(); got != "acme" {
		t.Errorf("customer_id = %q, want %q", got, "acme")
	}
	// Guard against nil entries sneaking into the slice — they would crash
	// OTLP marshaling.
	for i, kv := range spans[0].Attributes {
		if kv == nil {
			t.Errorf("Attributes[%d] is nil — would crash OTLP marshaling", i)
		}
	}
}

// TestConvertTraceToResourceSpan_FilteredSpansDoNotReceiveAttrs verifies that
// spans filtered out by pluginSpanFilter never appear in the exported output
// (and therefore can't carry the trace-level dims at all). This guards against
// a hypothetical regression where the merge loop accidentally exports a
// filtered span.
func TestConvertTraceToResourceSpan_FilteredSpansDoNotReceiveAttrs(t *testing.T) {
	p := &OtelPlugin{
		pluginSpanFilter: &PluginSpanFilter{
			Mode:    PluginSpanFilterModeExclude,
			Plugins: []string{"logging"},
		},
	}

	root := makeSpan("root", "", "POST /chat", schemas.SpanKindHTTPRequest)
	loggingPre := makeSpan("log-pre", "root", "plugin.logging.prehook", schemas.SpanKindPlugin)
	govPre := makeSpan("gov-pre", "root", "plugin.governance.prehook", schemas.SpanKindPlugin)

	trace := &schemas.Trace{
		TraceID:    "0123456789abcdef0123456789abcdef",
		RootSpan:   root,
		Spans:      []*schemas.Span{root, loggingPre, govPre},
		Attributes: map[string]any{"customer_id": "acme"},
	}

	rs := p.convertTraceToResourceSpan("", trace, nil, false, true, false, false)
	spans := rs.ScopeSpans[0].Spans
	if len(spans) != 2 {
		t.Fatalf("expected 2 exported spans (root + gov-pre), got %d", len(spans))
	}

	for _, sp := range spans {
		if sp.Name == "plugin.logging.prehook" {
			t.Errorf("filtered span %q was exported", sp.Name)
		}
		if findAttr(sp, "customer_id") == nil {
			t.Errorf("exported span %q missing customer_id", sp.Name)
		}
	}
}
