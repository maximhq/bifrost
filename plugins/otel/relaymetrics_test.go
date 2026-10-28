package otel

import (
	"context"
	"net/http"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/tracing"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// refusingRelayPipeline is the narrowest real pipeline: its pre-hook refuses every call the way
// governance refuses a key without a grant, and its post-hook passes the refusal through.
type refusingRelayPipeline struct{}

func (refusingRelayPipeline) RunRelayPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostRelayRequest, entered func(int)) (*schemas.BifrostRelayRequest, *schemas.RelayPluginShortCircuit, int) {
	entered(1)
	status := http.StatusForbidden
	return req, &schemas.RelayPluginShortCircuit{Error: &schemas.BifrostError{StatusCode: &status, Error: &schemas.ErrorField{Message: "virtual key has no grant for relay tavily"}}}, 1
}

func (refusingRelayPipeline) RunRelayPostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostRelayResponse, bifrostErr *schemas.BifrostError, _ int) (*schemas.BifrostRelayResponse, *schemas.BifrostError) {
	return resp, bifrostErr
}

// refusedRelayTrace relays one call through a real manager whose pre-hook refuses it, and returns
// the trace that reaches the observability plugins, so the test covers the manager's refusal path
// rather than a hand-built span.
func refusedRelayTrace(t *testing.T) *schemas.Trace {
	t.Helper()
	log := bifrost.NewDefaultLogger(schemas.LogLevelError)
	tracer := tracing.NewTracer(tracing.NewTraceStore(time.Minute, log), nil, log)
	t.Cleanup(tracer.Stop)
	manager := relay.NewManager(nil, log)
	if err := manager.Upsert(&schemas.RelayConfig{
		Name: "tavily", BaseURL: "https://search.example.com", SkipRouteCheck: true,
		Auth: schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer}, Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("sk-endpoint")},
	}); err != nil {
		t.Fatal(err)
	}
	manager.SetPluginPipeline(func() relay.PluginPipeline { return refusingRelayPipeline{} }, func(relay.PluginPipeline) {})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	id := tracer.CreateTrace("")
	ctx.SetValue(schemas.BifrostContextKeyTraceID, id)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	_, root := tracer.StartSpan(ctx, "request", schemas.SpanKindHTTPRequest)
	ctx.SetValue(schemas.BifrostContextKeySpanID, tracer.SpanFromHandle(root).SpanID)
	_, bifrostErr := manager.Serve(ctx, relay.Request{Endpoint: "tavily", Method: "POST", Path: "/search", Headers: map[string]string{}})
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusForbidden {
		t.Fatalf("got %+v, want the 403 refusal", bifrostErr)
	}
	tracer.EndSpan(root, schemas.SpanStatusError, "")
	trace := tracer.EndTrace(id)
	if trace == nil {
		t.Fatal("no trace")
	}
	t.Cleanup(func() { tracer.ReleaseTrace(trace) })
	return trace
}

// TestRecordRelayMetricsCountRefusedCalls pins the same for the OTel exporter: the refusal the
// manager records on the operation span reaches bifrost.relay.operation.duration with its status
// and error type, and records no cost.
func TestRecordRelayMetricsCountRefusedCalls(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	m := &MetricsExporter{provider: provider, meter: provider.Meter("test")}
	m.initMetrics()

	(&OtelPlugin{}).recordRelayMetricsFromTrace(context.Background(), m, refusedRelayTrace(t))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var durations int
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			switch metric.Name {
			case "bifrost.relay.operation.duration":
				hist, ok := metric.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("duration data = %T, want a float64 histogram", metric.Data)
				}
				for _, dp := range hist.DataPoints {
					durations++
					status, _ := dp.Attributes.Value(attribute.Key(schemas.AttrHTTPResponseStatusCode))
					errType, _ := dp.Attributes.Value(attribute.Key(schemas.AttrErrorTypeSpec))
					name, _ := dp.Attributes.Value(attribute.Key(schemas.AttrBifrostRelayName))
					if name.AsString() != "tavily" || status.AsInt64() != http.StatusForbidden || errType.AsString() != "403" || dp.Count != 1 {
						t.Fatalf("refused call point = %v (count %d)", dp.Attributes, dp.Count)
					}
				}
			case "bifrost.relay.cost":
				t.Fatal("a refused call must record no cost")
			}
		}
	}
	if durations != 1 {
		t.Fatalf("want the refused call in bifrost.relay.operation.duration, got %d points", durations)
	}
}
