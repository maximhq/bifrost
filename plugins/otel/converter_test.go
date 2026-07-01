package otel

import (
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

func TestShouldExportSpan(t *testing.T) {
	tests := []struct {
		name   string
		filter *PluginSpanFilter
		span   *schemas.Span
		want   bool
	}{
		{
			name:   "nil filter exports everything",
			filter: nil,
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   true,
		},
		{
			name:   "non-plugin span always exported regardless of filter",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}},
			span:   makeSpan("1", "", "llm.call", schemas.SpanKindLLMCall),
			want:   true,
		},
		{
			name:   "exclude mode: plugin in list is suppressed",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging", "compat"}},
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   false,
		},
		{
			name:   "exclude mode: plugin not in list is exported",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}},
			span:   makeSpan("1", "", "plugin.governance.posthook", schemas.SpanKindPlugin),
			want:   true,
		},
		{
			name:   "exclude mode: posthook variant suppressed the same as prehook",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}},
			span:   makeSpan("1", "", "plugin.logging.posthook", schemas.SpanKindPlugin),
			want:   false,
		},
		{
			name:   "include mode: plugin in list is exported",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{"guardrails"}},
			span:   makeSpan("1", "", "plugin.guardrails.prehook", schemas.SpanKindPlugin),
			want:   true,
		},
		{
			name:   "include mode: plugin not in list is suppressed",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{"guardrails"}},
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   false,
		},
		{
			name:   "exclude mode: empty list suppresses nothing",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{}},
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   true,
		},
		{
			name:   "include mode: empty list suppresses everything",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeInclude, Plugins: []string{}},
			span:   makeSpan("1", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			want:   false,
		},
		{
			name:   "span name without dots passes through",
			filter: &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}},
			span:   makeSpan("1", "", "nodots", schemas.SpanKindPlugin),
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &OtelPlugin{pluginSpanFilter: tt.filter}
			if got := p.shouldExportSpan(tt.span); got != tt.want {
				t.Errorf("shouldExportSpan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildReparentMap(t *testing.T) {
	excludeLogging := &PluginSpanFilter{Mode: PluginSpanFilterModeExclude, Plugins: []string{"logging"}}

	t.Run("nil filter returns nil map", func(t *testing.T) {
		p := &OtelPlugin{pluginSpanFilter: nil}
		spans := []*schemas.Span{makeSpan("a", "root", "plugin.logging.prehook", schemas.SpanKindPlugin)}
		if m := p.buildReparentMap(spans); m != nil {
			t.Errorf("expected nil, got %v", m)
		}
	})

	t.Run("no filtered spans returns nil map", func(t *testing.T) {
		p := &OtelPlugin{pluginSpanFilter: excludeLogging}
		spans := []*schemas.Span{
			makeSpan("a", "root", "plugin.governance.prehook", schemas.SpanKindPlugin),
		}
		if m := p.buildReparentMap(spans); m != nil {
			t.Errorf("expected nil, got %v", m)
		}
	})

	t.Run("single filtered span maps to its direct parent", func(t *testing.T) {
		p := &OtelPlugin{pluginSpanFilter: excludeLogging}
		// root -> logging (filtered) -> governance
		spans := []*schemas.Span{
			makeSpan("root", "", "request", schemas.SpanKindInternal),
			makeSpan("log-pre", "root", "plugin.logging.prehook", schemas.SpanKindPlugin),
			makeSpan("gov-pre", "log-pre", "plugin.governance.prehook", schemas.SpanKindPlugin),
		}
		m := p.buildReparentMap(spans)
		if m == nil {
			t.Fatal("expected non-nil map")
		}
		if got := m["log-pre"]; got != "root" {
			t.Errorf("filtered span should map to parent 'root', got %q", got)
		}
	})

	t.Run("chain of filtered spans resolves to nearest exported ancestor", func(t *testing.T) {
		// root -> telemetry (filtered) -> logging (filtered) -> governance
		p := &OtelPlugin{pluginSpanFilter: &PluginSpanFilter{
			Mode:    PluginSpanFilterModeExclude,
			Plugins: []string{"telemetry", "logging"},
		}}
		spans := []*schemas.Span{
			makeSpan("root", "", "request", schemas.SpanKindInternal),
			makeSpan("tel-pre", "root", "plugin.telemetry.prehook", schemas.SpanKindPlugin),
			makeSpan("log-pre", "tel-pre", "plugin.logging.prehook", schemas.SpanKindPlugin),
			makeSpan("gov-pre", "log-pre", "plugin.governance.prehook", schemas.SpanKindPlugin),
		}
		m := p.buildReparentMap(spans)
		if m == nil {
			t.Fatal("expected non-nil map")
		}
		// Both filtered spans must resolve to "root" so governance.prehook re-parents there.
		if got := m["tel-pre"]; got != "root" {
			t.Errorf("tel-pre should resolve to 'root', got %q", got)
		}
		if got := m["log-pre"]; got != "root" {
			t.Errorf("log-pre should skip the chain and resolve to 'root', got %q", got)
		}
	})

	t.Run("filtered span with no parent resolves to empty string", func(t *testing.T) {
		p := &OtelPlugin{pluginSpanFilter: excludeLogging}
		spans := []*schemas.Span{
			// logging span has no parent (root of trace)
			makeSpan("log-pre", "", "plugin.logging.prehook", schemas.SpanKindPlugin),
			makeSpan("gov-pre", "log-pre", "plugin.governance.prehook", schemas.SpanKindPlugin),
		}
		m := p.buildReparentMap(spans)
		if m == nil {
			t.Fatal("expected non-nil map")
		}
		if got := m["log-pre"]; got != "" {
			t.Errorf("root-level filtered span should resolve to empty string, got %q", got)
		}
	})
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

	rs := p.convertTraceToResourceSpan("", trace, nil, false, true)
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

	rs := p.convertTraceToResourceSpan("", trace, nil, false, true)
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
		rs := p.convertTraceToResourceSpan("", trace, nil, false, true)
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
		rs := p.convertTraceToResourceSpan("", trace, nil, false, true)
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

	rs := p.convertTraceToResourceSpan("", trace, nil, false, true)
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

	rs := p.convertTraceToResourceSpan("", trace, nil, false, true)
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
