package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestA2AMetricsFromTrace checks wire latency, failure classification, bounded
// identity labels and filtering through the actual completed-trace entry point.
func TestA2AMetricsFromTrace(t *testing.T) {
	p := newTestPlugin(t)
	start := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		{Kind: schemas.SpanKindA2AOperation, Status: schemas.SpanStatusError, StartTime: start, EndTime: start.Add(time.Second), Attributes: map[string]any{
			schemas.AttrBifrostA2AOperationName: "SendMessage", schemas.AttrA2AMethodName: "SendMessage",
			schemas.AttrBifrostA2AUpstreamTransport: "GRPC", schemas.AttrBifrostA2AOperationDurationMs: int64(250),
			schemas.AttrErrorTypeSpec: "a2a.task.failed", schemas.AttrBifrostVirtualKeyID: "vk-test",
			schemas.AttrA2ATaskID: "must-not-be-a-label",
		}},
		{Kind: schemas.SpanKindA2AOperation, Status: schemas.SpanStatusError, StartTime: start, EndTime: start.Add(2 * time.Second), Attributes: map[string]any{schemas.AttrBifrostA2AOperationName: "GetTask"}},
		{Kind: schemas.SpanKindA2AOperation},
		{Kind: schemas.SpanKindInternal, Attributes: map[string]any{schemas.AttrBifrostA2AOperationName: "Ignore"}},
	}}
	if err := p.Inject(context.Background(), trace); err != nil {
		t.Fatal(err)
	}
	families, err := p.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, family := range families {
		if family.GetName() != "bifrost_a2a_client_operation_duration_seconds" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			want := 2.0
			if labels["operation"] == "SendMessage" {
				want = .25
				if labels["error_type"] != "a2a.task.failed" || labels["virtual_key_id"] != "vk-test" || labels["upstream_transport"] != "GRPC" {
					t.Fatalf("unexpected labels: %v", labels)
				}
			} else if labels["operation"] != "GetTask" || labels["error_type"] != "_OTHER" {
				t.Fatalf("unexpected fallback labels: %v", labels)
			}
			if metric.Histogram.GetSampleCount() != 1 || metric.Histogram.GetSampleSum() != want {
				t.Fatalf("unexpected histogram: %v", metric.Histogram)
			}
			if _, ok := labels["a2a_task_id"]; ok {
				t.Fatal("task ID must not be a metric label")
			}
			count++
		}
	}
	if count != 2 {
		t.Fatalf("got %d series, want 2", count)
	}
}
