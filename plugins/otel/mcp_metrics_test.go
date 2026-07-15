package otel

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// attrMap collapses a []attribute.KeyValue into a lookup for assertions.
func attrMap(kvs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value.AsString()
	}
	return out
}

// TestBuildMCPSpanAttrsSemconvAndGovernance asserts the MCP metric attribute set carries
// the OTel semconv dimensions plus the flat-named governance labels, and that absent
// optional dimensions are omitted (no empty-value label churn).
func TestBuildMCPSpanAttrsSemconvAndGovernance(t *testing.T) {
	span := &schemas.Span{
		Kind: schemas.SpanKindMCPTool, // a tools/call is a tool invocation
		Attributes: map[string]any{
			schemas.AttrMCPMethodName:       "tools/call",
			schemas.AttrToolName:            "search",
			schemas.AttrNetworkTransport:    "pipe",
			schemas.AttrBifrostVirtualKeyID: "vk_123",
			schemas.AttrBifrostTeamName:     "platform",
			// business unit / customer intentionally absent
		},
	}

	got := attrMap(buildMCPSpanAttrs(span))

	want := map[string]string{
		schemas.AttrMCPMethodName:    "tools/call",
		schemas.AttrToolName:         "search",
		schemas.AttrNetworkTransport: "pipe",
		"virtual_key_id":             "vk_123",
		"team_name":                  "platform",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attr %q = %q, want %q", k, got[k], v)
		}
	}
	// error.type is added by the recorder only on failure, never by buildMCPSpanAttrs.
	if _, ok := got[schemas.AttrErrorTypeSpec]; ok {
		t.Error("error.type must not be present on a non-error span's attrs")
	}
	// Absent optional governance dims must not appear as empty labels.
	for _, absent := range []string{"customer_id", "business_unit_id", "team_id"} {
		if _, ok := got[absent]; ok {
			t.Errorf("absent dimension %q should be omitted, got %q", absent, got[absent])
		}
	}
}

// TestRecordMCPMetricsFromTraceRecordsBothKinds asserts the recorder emits one
// mcp.client.operation.duration sample per MCP span — for BOTH SpanKindMCPTool (tool
// calls) and SpanKindMCPClient (lifecycle) — preferring the wire latency over wall-time,
// tagging error.type on failures, and skipping un-enriched and non-MCP spans.
func TestRecordMCPMetricsFromTraceRecordsBothKinds(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := &MetricsExporter{provider: provider, meter: provider.Meter("test")}
	m.initMetrics()

	now := time.Now()
	trace := &schemas.Trace{
		Spans: []*schemas.Span{
			{ // tool call → MCPTool; duration from the 2000ms wire latency, not 5s wall-time
				Kind:      schemas.SpanKindMCPTool,
				StartTime: now, EndTime: now.Add(5 * time.Second),
				Status: schemas.SpanStatusOk,
				Attributes: map[string]any{
					schemas.AttrMCPMethodName:            "tools/call",
					schemas.AttrToolName:                 "search",
					schemas.AttrBifrostMCPToolDurationMs: int64(2000),
				},
			},
			{ // lifecycle → MCPClient; wall-time fallback (1s) + error.type
				Kind:      schemas.SpanKindMCPClient,
				StartTime: now, EndTime: now.Add(1 * time.Second),
				Status: schemas.SpanStatusError,
				Attributes: map[string]any{
					schemas.AttrMCPMethodName: "tools/list",
					schemas.AttrErrorTypeSpec: "timeout",
				},
			},
			{ // un-enriched MCP span (no mcp.method.name) → skipped
				Kind:       schemas.SpanKindMCPClient,
				Attributes: map[string]any{},
			},
			{ // non-MCP span → ignored even with an mcp attr present
				Kind:       schemas.SpanKindLLMCall,
				Attributes: map[string]any{schemas.AttrMCPMethodName: "tools/call"},
			},
		},
	}

	(&OtelPlugin{}).recordMCPMetricsFromTrace(context.Background(), m, trace)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	hist := findMCPHistogram(t, &rm)
	if len(hist.DataPoints) != 2 {
		t.Fatalf("data points = %d, want 2 (MCPTool + MCPClient; un-enriched + LLM skipped)", len(hist.DataPoints))
	}

	sawToolCall, sawListTools := false, false
	for _, dp := range hist.DataPoints {
		method, _ := dp.Attributes.Value(attribute.Key(schemas.AttrMCPMethodName))
		switch method.AsString() {
		case "tools/call":
			sawToolCall = true
			if dp.Count != 1 || dp.Sum != 2.0 {
				t.Errorf("tools/call: count=%d sum=%v, want 1 and 2.0 (wire latency)", dp.Count, dp.Sum)
			}
			if _, ok := dp.Attributes.Value(attribute.Key(schemas.AttrErrorTypeSpec)); ok {
				t.Error("tools/call: error.type must be absent on success")
			}
		case "tools/list":
			sawListTools = true
			if dp.Count != 1 || dp.Sum != 1.0 {
				t.Errorf("tools/list: count=%d sum=%v, want 1 and 1.0 (wall-time)", dp.Count, dp.Sum)
			}
			et, ok := dp.Attributes.Value(attribute.Key(schemas.AttrErrorTypeSpec))
			if !ok || et.AsString() != "timeout" {
				t.Errorf("tools/list: error.type = %q (present=%v), want timeout", et.AsString(), ok)
			}
		default:
			t.Errorf("unexpected mcp.method.name %q", method.AsString())
		}
	}
	if !sawToolCall || !sawListTools {
		t.Fatalf("missing data points: tools/call=%v tools/list=%v", sawToolCall, sawListTools)
	}
}

// findMCPHistogram returns the mcp.client.operation.duration histogram from collected metrics.
func findMCPHistogram(t *testing.T, rm *metricdata.ResourceMetrics) metricdata.Histogram[float64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, mtr := range sm.Metrics {
			if mtr.Name != "mcp.client.operation.duration" {
				continue
			}
			hist, ok := mtr.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %q is %T, want Histogram[float64]", mtr.Name, mtr.Data)
			}
			return hist
		}
	}
	t.Fatal("mcp.client.operation.duration metric not found")
	return metricdata.Histogram[float64]{}
}

// TestMCPRequestTypeOTelMethodName pins the semconv method-name mapping used to stamp
// mcp.method.name onto the span.
func TestMCPRequestTypeOTelMethodName(t *testing.T) {
	cases := map[schemas.MCPRequestType]string{
		schemas.MCPRequestTypeExecuteTool:       "tools/call",
		schemas.MCPRequestTypeChatToolCall:      "tools/call",
		schemas.MCPRequestTypeResponsesToolCall: "tools/call",
		schemas.MCPRequestTypeListTools:         "tools/list",
		schemas.MCPRequestTypePing:              "ping",
	}
	for in, want := range cases {
		if got := in.OTelMethodName(); got != want {
			t.Errorf("OTelMethodName(%q) = %q, want %q", in, got, want)
		}
	}
	// Unknown types fall back to the raw string rather than being dropped.
	if got := schemas.MCPRequestType("custom").OTelMethodName(); got != "custom" {
		t.Errorf("unknown fallback = %q, want %q", got, "custom")
	}
}

// TestMCPConnectionTypeOTelNetworkTransport pins the transport mapping.
func TestMCPConnectionTypeOTelNetworkTransport(t *testing.T) {
	cases := map[schemas.MCPConnectionType]string{
		schemas.MCPConnectionTypeSTDIO:     "pipe",
		schemas.MCPConnectionTypeHTTP:      "tcp",
		schemas.MCPConnectionTypeSSE:       "tcp",
		schemas.MCPConnectionTypeInProcess: "", // no network transport
	}
	for in, want := range cases {
		if got := in.OTelNetworkTransport(); got != want {
			t.Errorf("OTelNetworkTransport(%q) = %q, want %q", in, got, want)
		}
	}
}
