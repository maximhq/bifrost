package schemas

import "testing"

func TestIsOverheadBreakdownSpan(t *testing.T) {
	cases := []struct {
		name string
		span *Span
		want bool
	}{
		{"nil", nil, false},
		{"internal convertor", &Span{Kind: SpanKindInternal, Name: "convertor"}, true},
		{"internal queue-wait", &Span{Kind: SpanKindInternal, Name: "queue-wait"}, true},
		{"internal request-marshal", &Span{Kind: SpanKindInternal, Name: "request-marshal"}, true},
		{"internal request-unmarshal", &Span{Kind: SpanKindInternal, Name: "request-unmarshal"}, true},
		{"internal response-parse", &Span{Kind: SpanKindInternal, Name: "response-parse"}, true},
		{"internal response-marshal", &Span{Kind: SpanKindInternal, Name: "response-marshal"}, true},
		{"internal attribute-population", &Span{Kind: SpanKindInternal, Name: "attribute-population"}, true},
		{"middleware prefix", &Span{Kind: SpanKindInternal, Name: "middleware.auth"}, true},
		// key.selection predates the breakdown and carries chosen-key attributes: retained.
		{"key.selection retained", &Span{Kind: SpanKindInternal, Name: "key.selection"}, false},
		{"other internal", &Span{Kind: SpanKindInternal, Name: "something-else"}, false},
		{"plugin transport prehook", &Span{Kind: SpanKindPlugin, Name: "plugin.governance.transportprehook"}, true},
		{"plugin transport posthook", &Span{Kind: SpanKindPlugin, Name: "plugin.governance.transportposthook"}, true},
		{"plugin normal prehook", &Span{Kind: SpanKindPlugin, Name: "plugin.governance.prehook"}, false},
		{"llm call", &Span{Kind: SpanKindLLMCall, Name: "chat gpt-4o"}, false},
		{"http request root", &Span{Kind: SpanKindHTTPRequest, Name: "/v1/chat/completions"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOverheadBreakdownSpan(tc.span); got != tc.want {
				t.Errorf("IsOverheadBreakdownSpan = %v, want %v", got, tc.want)
			}
		})
	}
}

func findSpanByID(tr *Trace, id string) *Span {
	for _, s := range tr.Spans {
		if s != nil && s.SpanID == id {
			return s
		}
	}
	return nil
}

// With nothing to strip, the receiver is returned unchanged so the common path
// allocates nothing.
func TestWithoutOverheadBreakdownSpans_NoBreakdownReturnsReceiver(t *testing.T) {
	tr := &Trace{Spans: []*Span{
		{SpanID: "root", Kind: SpanKindHTTPRequest, Name: "/v1/chat/completions"},
		{SpanID: "key", ParentID: "root", Kind: SpanKindInternal, Name: "key.selection"},
		{SpanID: "llm", ParentID: "root", Kind: SpanKindLLMCall, Name: "chat"},
	}}
	if got := tr.WithoutOverheadBreakdownSpans(); got != tr {
		t.Error("expected the receiver returned unchanged when nothing is stripped")
	}
}

// A retained span whose direct parent is a stripped breakdown span is reparented to
// that breakdown span's own parent; the source span is left untouched.
func TestWithoutOverheadBreakdownSpans_DirectOmittedParentReparented(t *testing.T) {
	tr := &Trace{Spans: []*Span{
		{SpanID: "root", Kind: SpanKindHTTPRequest, Name: "/v1/chat/completions"},
		{SpanID: "conv", ParentID: "root", Kind: SpanKindInternal, Name: "convertor"},
		{SpanID: "llm", ParentID: "conv", Kind: SpanKindLLMCall, Name: "chat"},
	}}
	got := tr.WithoutOverheadBreakdownSpans()
	if got == tr {
		t.Fatal("expected a new trace when a breakdown span is stripped")
	}
	if findSpanByID(got, "conv") != nil {
		t.Error("breakdown span should be omitted from the export trace")
	}
	llm := findSpanByID(got, "llm")
	if llm == nil {
		t.Fatal("retained span missing from export trace")
	}
	if llm.ParentID != "root" {
		t.Errorf("reparented ParentID = %q, want root", llm.ParentID)
	}
	// Source span is unchanged (reparenting works on a copy).
	if src := findSpanByID(tr, "llm"); src == nil || src.ParentID != "conv" {
		t.Errorf("source span mutated: ParentID = %q, want conv", src.ParentID)
	}
}

// A chain of stripped breakdown ancestors collapses: the retained descendant is
// reparented to the nearest kept ancestor.
func TestWithoutOverheadBreakdownSpans_ChainedOmittedParentsReparented(t *testing.T) {
	tr := &Trace{Spans: []*Span{
		{SpanID: "root", Kind: SpanKindHTTPRequest, Name: "/v1/chat/completions"},
		{SpanID: "conv", ParentID: "root", Kind: SpanKindInternal, Name: "convertor"},
		{SpanID: "qw", ParentID: "conv", Kind: SpanKindInternal, Name: "queue-wait"},
		{SpanID: "llm", ParentID: "qw", Kind: SpanKindLLMCall, Name: "chat"},
	}}
	got := tr.WithoutOverheadBreakdownSpans()
	if findSpanByID(got, "conv") != nil || findSpanByID(got, "qw") != nil {
		t.Error("chained breakdown spans should all be omitted")
	}
	llm := findSpanByID(got, "llm")
	if llm == nil {
		t.Fatal("retained span missing from export trace")
	}
	if llm.ParentID != "root" {
		t.Errorf("chained reparent ParentID = %q, want root", llm.ParentID)
	}
}
