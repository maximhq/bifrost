package otel

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestRecordHTTPMetricsEmitsSemconvServerDuration asserts http.server.request.duration is
// recorded with the stable semconv attributes, taking http.route from the matched route
// template on the context and omitting it for unmatched paths.
func TestRecordHTTPMetricsEmitsSemconvServerDuration(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := &MetricsExporter{provider: provider, meter: provider.Meter("test")}
	m.initMetrics()
	p := &OtelPlugin{targets: []*otelTarget{{metricsExporter: m}}}

	matched := context.WithValue(context.Background(), string(schemas.BifrostContextKeyHTTPRoute), "/v1/chat/completions")
	p.RecordHTTPMetrics(matched, "/v1/chat/completions", "POST", "200", 1.5, 100, 200)
	p.RecordHTTPMetrics(context.Background(), "/wp-admin/setup.php", "GET", "404", 0.01, 0, 0)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	var hist *metricdata.Histogram[float64]
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			if md.Name == "http.server.request.duration" {
				if md.Unit != "s" {
					t.Errorf("unit = %q, want s", md.Unit)
				}
				h := md.Data.(metricdata.Histogram[float64])
				hist = &h
			}
		}
	}
	if hist == nil {
		t.Fatal("http.server.request.duration not recorded")
	}
	if len(hist.DataPoints) != 2 {
		t.Fatalf("got %d datapoints, want 2", len(hist.DataPoints))
	}

	for _, dp := range hist.DataPoints {
		method, _ := dp.Attributes.Value("http.request.method")
		status, _ := dp.Attributes.Value("http.response.status_code")
		route, hasRoute := dp.Attributes.Value("http.route")
		switch method.AsString() {
		case "POST":
			if status.Type() != attribute.INT64 || status.AsInt64() != 200 {
				t.Errorf("status = %v, want int 200", status.String())
			}
			if !hasRoute || route.AsString() != "/v1/chat/completions" {
				t.Errorf("http.route = %q (present=%v), want /v1/chat/completions", route.AsString(), hasRoute)
			}
			if dp.Sum != 1.5 {
				t.Errorf("sum = %v, want 1.5", dp.Sum)
			}
		case "GET":
			if status.AsInt64() != 404 {
				t.Errorf("status = %v, want 404", status.String())
			}
			if hasRoute {
				t.Errorf("unmatched path must not set http.route, got %q", route.AsString())
			}
		default:
			t.Errorf("unexpected method %q", method.AsString())
		}
	}
}
