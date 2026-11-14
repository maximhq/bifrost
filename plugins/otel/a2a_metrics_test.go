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

// TestBuildA2ASpanAttrsSemconvAndGovernance asserts the A2A metric attribute set
// carries the method and transport dimensions plus flat-named governance labels,
// omits absent optional dimensions, and keeps the high-cardinality Agent name
// on the span only.
func TestBuildA2ASpanAttrsSemconvAndGovernance(t *testing.T) {
	span := &schemas.Span{
		Kind: schemas.SpanKindA2AOperation,
		Attributes: map[string]any{
			schemas.AttrBifrostA2AOperationName:     "SendMessage",
			schemas.AttrA2AMethodName:               "SendMessage",
			schemas.AttrAgentName:                   "library-research",
			schemas.AttrBifrostA2AUpstreamTransport: "JSONRPC",
			schemas.AttrBifrostVirtualKeyID:         "vk_123",
			schemas.AttrBifrostTeamName:             "platform",
		},
	}

	got := attrMap(buildA2ASpanAttrs(span))

	want := map[string]string{
		schemas.AttrBifrostA2AOperationName:     "SendMessage",
		schemas.AttrA2AMethodName:               "SendMessage",
		schemas.AttrBifrostA2AUpstreamTransport: "JSONRPC",
		"virtual_key_id":                        "vk_123",
		"team_name":                             "platform",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attr %q = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got[schemas.AttrErrorTypeSpec]; ok {
		t.Error("error.type must not be present on a non-error span's attrs")
	}
	for _, absent := range []string{schemas.AttrAgentName, "customer_id", "business_unit_id", "project_id", "team_id"} {
		if _, ok := got[absent]; ok {
			t.Errorf("absent dimension %q should be omitted, got %q", absent, got[absent])
		}
	}
}

// TestRecordA2AMetricsFromTrace asserts the recorder emits one
// a2a.client.operation.duration sample per a2a.operation span — preferring the
// gateway-measured wire latency over wall-time, tagging error.type on failures,
// and skipping un-enriched and non-A2A spans.
func TestRecordA2AMetricsFromTrace(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := &MetricsExporter{provider: provider, meter: provider.Meter("test")}
	m.initMetrics()

	now := time.Now()
	trace := &schemas.Trace{
		Spans: []*schemas.Span{
			{ // duration from the 2000ms wire latency, not the 5s wall-time
				Kind:      schemas.SpanKindA2AOperation,
				StartTime: now, EndTime: now.Add(5 * time.Second),
				Status: schemas.SpanStatusOk,
				Attributes: map[string]any{
					schemas.AttrBifrostA2AOperationName:       "SendMessage",
					schemas.AttrA2AMethodName:                 "SendMessage",
					schemas.AttrAgentName:                     "library-research",
					schemas.AttrBifrostA2AOperationDurationMs: int64(2000),
				},
			},
			{ // wall-time fallback (1s) + error.type
				Kind:      schemas.SpanKindA2AOperation,
				StartTime: now, EndTime: now.Add(1 * time.Second),
				Status: schemas.SpanStatusError,
				Attributes: map[string]any{
					schemas.AttrBifrostA2AOperationName: "GetTask",
					schemas.AttrA2AMethodName:           "GetTask",
					schemas.AttrErrorTypeSpec:           "timeout",
				},
			},
			{ // gateway-owned operation has no JSON-RPC method
				Kind:      schemas.SpanKindA2AOperation,
				StartTime: now, EndTime: now.Add(500 * time.Millisecond),
				Status: schemas.SpanStatusOk,
				Attributes: map[string]any{
					schemas.AttrBifrostA2AOperationName: "push_delivery",
				},
			},
			{ // un-enriched a2a span (no operation name) → skipped
				Kind:       schemas.SpanKindA2AOperation,
				Attributes: map[string]any{},
			},
			{ // non-A2A span → ignored even with Agent attributes present
				Kind: schemas.SpanKindLLMCall,
				Attributes: map[string]any{
					schemas.AttrBifrostA2AOperationName: "SendMessage",
					schemas.AttrA2AMethodName:           "SendMessage",
				},
			},
		},
	}

	(&OtelPlugin{}).recordA2AMetricsFromTrace(context.Background(), m, trace)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	hist := findA2AHistogram(t, &rm)
	if len(hist.DataPoints) != 3 {
		t.Fatalf("data points = %d, want 3 (un-enriched + LLM skipped)", len(hist.DataPoints))
	}

	sawSend, sawGet, sawPush := false, false, false
	for _, dp := range hist.DataPoints {
		operation, _ := dp.Attributes.Value(attribute.Key(schemas.AttrBifrostA2AOperationName))
		method, hasMethod := dp.Attributes.Value(attribute.Key(schemas.AttrA2AMethodName))
		switch operation.AsString() {
		case "SendMessage":
			sawSend = true
			if !hasMethod || method.AsString() != "SendMessage" {
				t.Errorf("SendMessage: method = %q (present=%v)", method.AsString(), hasMethod)
			}
			if dp.Count != 1 || dp.Sum != 2.0 {
				t.Errorf("SendMessage: count=%d sum=%v, want 1 and 2.0 (wire latency)", dp.Count, dp.Sum)
			}
			if _, ok := dp.Attributes.Value(attribute.Key(schemas.AttrErrorTypeSpec)); ok {
				t.Error("SendMessage: error.type must be absent on success")
			}
		case "GetTask":
			sawGet = true
			if !hasMethod || method.AsString() != "GetTask" {
				t.Errorf("GetTask: method = %q (present=%v)", method.AsString(), hasMethod)
			}
			if dp.Count != 1 || dp.Sum != 1.0 {
				t.Errorf("GetTask: count=%d sum=%v, want 1 and 1.0 (wall-time)", dp.Count, dp.Sum)
			}
			et, ok := dp.Attributes.Value(attribute.Key(schemas.AttrErrorTypeSpec))
			if !ok || et.AsString() != "timeout" {
				t.Errorf("GetTask: error.type = %q (present=%v), want timeout", et.AsString(), ok)
			}
		case "push_delivery":
			sawPush = true
			if hasMethod {
				t.Error("push_delivery: a2a.method.name must be absent")
			}
			if dp.Count != 1 || dp.Sum != 0.5 {
				t.Errorf("push_delivery: count=%d sum=%v, want 1 and 0.5", dp.Count, dp.Sum)
			}
		default:
			t.Errorf("unexpected operation %q", operation.AsString())
		}
	}
	if !sawSend || !sawGet || !sawPush {
		t.Fatalf("missing data points: SendMessage=%v GetTask=%v push_delivery=%v", sawSend, sawGet, sawPush)
	}
}

// findA2AHistogram returns the a2a.client.operation.duration histogram from collected metrics.
func findA2AHistogram(t *testing.T, rm *metricdata.ResourceMetrics) metricdata.Histogram[float64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, mtr := range sm.Metrics {
			if mtr.Name != "a2a.client.operation.duration" {
				continue
			}
			hist, ok := mtr.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %q is %T, want Histogram[float64]", mtr.Name, mtr.Data)
			}
			return hist
		}
	}
	t.Fatal("a2a.client.operation.duration metric not found")
	return metricdata.Histogram[float64]{}
}
