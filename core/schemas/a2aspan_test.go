package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestA2ASpanContentLifecycle protects the separate raw carrier through snapshot,
// redaction, connector exclusions, content suppression, and pooled span reset.
func TestA2ASpanContentLifecycle(t *testing.T) {
	span := &Span{SpanID: "op", Kind: SpanKindA2AOperation, Attributes: map[string]any{AttrInputMessages: `{"text":"input-secret"}`, AttrOutputMessages: `{"text":"output-secret"}`}}
	span.SetA2ARawPayloads(span.SpanID, &A2ASpanData{RawRequest: `{"text":"input-secret"}`, RawResponse: `{"text":"output-secret"}`})
	span.AddEvent(SpanEvent{Name: "a2a.message", Attributes: map[string]any{AttrOutputMessages: `{"text":"output-secret"}`}})
	trace := &Trace{RootSpan: span, Spans: []*Span{span}}
	snapshot := trace.SnapshotForExport()
	defer snapshot.ReleaseSnapshot()
	require.Equal(t, span.A2A, snapshot.RootSpan.A2A)
	trace.SetRedactionReplacements(RedactionPhaseInput, map[string]string{"input-secret": "INPUT"})
	trace.SetRedactionReplacements(RedactionPhaseOutput, map[string]string{"output-secret": "OUTPUT"})
	trace.ApplyRedactionReplacements()
	req, resp := span.RawPayloads()
	require.Contains(t, req, "INPUT")
	require.Contains(t, resp, "OUTPUT")
	require.Contains(t, span.A2AStreamOutput(span.SpanID), "OUTPUT")
	require.Contains(t, snapshot.RootSpan.A2A.RawRequest, "input-secret", "redaction must copy immutable raw carrier")
	stripped := StripTraceAttributes(trace, []string{AttrBifrostRawRequest})
	req, resp = stripped.RootSpan.RawPayloads()
	require.Empty(t, req)
	require.NotEmpty(t, resp)
	require.NotEmpty(t, span.A2A.RawRequest, "exclusion must not mutate other connectors' source")
	suppressed := StripTraceContent(trace)
	require.Nil(t, suppressed.RootSpan.A2A)
	require.Empty(t, suppressed.RootSpan.Attributes)
	serialized, err := json.Marshal(trace)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "RawRequest")
	span.Reset()
	require.Nil(t, span.A2A)
	for _, event := range span.Events[:cap(span.Events)] {
		require.Nil(t, event.Attributes, "pooled event storage must release content")
	}
	span.SpanID = "reused"
	span.SetA2ARawPayloads("op", &A2ASpanData{RawRequest: "stale"})
	require.Nil(t, span.A2A, "stale completion must not write into a recycled span")
	require.Empty(t, span.A2AStreamOutput("op"))
}
