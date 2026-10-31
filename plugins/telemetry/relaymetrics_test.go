package telemetry

import (
	"context"
	"net/http"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/tracing"
)

// TestRelayMetricsFromTrace checks that a completed relay span records one duration sample and
// its cost with the relay, HTTP and governance labels, that a failure carries its error type, and
// that spans of other kinds or without a relay name are ignored.
func TestRelayMetricsFromTrace(t *testing.T) {
	p := newTestPlugin(t)
	start := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		{Kind: schemas.SpanKindRelay, Status: schemas.SpanStatusOk, StartTime: start, EndTime: start.Add(250 * time.Millisecond), Attributes: map[string]any{
			schemas.AttrBifrostRelayName: "tavily", schemas.AttrHTTPRequestMethod: "POST", schemas.AttrBifrostRelayRoutePath: "/search",
			schemas.AttrHTTPResponseStatusCode: 200, schemas.AttrBifrostRelayCost: 0.033, schemas.AttrBifrostVirtualKeyID: "vk-test",
		}},
		{Kind: schemas.SpanKindRelay, Status: schemas.SpanStatusError, StartTime: start, EndTime: start.Add(time.Second), Attributes: map[string]any{
			schemas.AttrBifrostRelayName: "tavily", schemas.AttrHTTPRequestMethod: "POST", schemas.AttrHTTPResponseStatusCode: 403,
			schemas.AttrErrorTypeSpec: "relay_blocked",
		}},
		{Kind: schemas.SpanKindRelay, Status: schemas.SpanStatusOk, StartTime: start, EndTime: start.Add(time.Millisecond), Attributes: map[string]any{
			schemas.AttrBifrostRelayName: "tavily", schemas.AttrHTTPRequestMethod: "PROPFIND", schemas.AttrHTTPResponseStatusCode: 200,
		}},
		{Kind: schemas.SpanKindRelay},
		{Kind: schemas.SpanKindInternal, Attributes: map[string]any{schemas.AttrBifrostRelayName: "ignored"}},
	}}
	if err := p.Inject(context.Background(), trace); err != nil {
		t.Fatal(err)
	}
	families, err := p.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	durations, costs := 0, 0
	for _, family := range families {
		switch family.GetName() {
		case "bifrost_relay_operation_duration_seconds":
			for _, m := range family.GetMetric() {
				durations++
				labels := map[string]string{}
				for _, l := range m.GetLabel() {
					labels[l.GetName()] = l.GetValue()
				}
				if labels["relay_name"] != "tavily" || (labels["method"] != "POST" && labels["method"] != "_OTHER") {
					t.Fatalf("unexpected labels %v", labels)
				}
				if labels["method"] == "_OTHER" {
					continue // a method outside the route set is folded into one label
				}
				switch labels["status_code"] {
				case "200":
					if labels["error_type"] != "" || labels["route_path"] != "/search" || labels["virtual_key_id"] != "vk-test" {
						t.Fatalf("success sample labels = %v", labels)
					}
					if sum := m.GetHistogram().GetSampleSum(); sum < 0.249 || sum > 0.251 {
						t.Fatalf("duration = %v, want 0.25s from the span", sum)
					}
				case "403":
					if labels["error_type"] != "relay_blocked" {
						t.Fatalf("failure sample labels = %v", labels)
					}
				default:
					t.Fatalf("unexpected status_code label %q", labels["status_code"])
				}
			}
		case "bifrost_relay_cost_total":
			for _, m := range family.GetMetric() {
				costs++
				if v := m.GetCounter().GetValue(); v < 0.0329 || v > 0.0331 {
					t.Fatalf("cost = %v, want 0.033", v)
				}
			}
		}
	}
	if durations != 3 {
		t.Fatalf("want 3 duration series (one per completed relay span, the unknown method folded to _OTHER), got %d", durations)
	}
	if costs != 1 {
		t.Fatalf("want 1 cost series (the counted call only), got %d", costs)
	}
}

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

// TestRelayMetricsCountRefusedCalls pins that a call governance refuses before it reaches the
// upstream still lands in the relay request and error totals: the manager records the refusal
// on the operation span, so the metrics derived from it see denied grants, exhausted budgets and
// rate limits, as the MCP post-hook counts a refused tool call.
func TestRelayMetricsCountRefusedCalls(t *testing.T) {
	p := newTestPlugin(t)
	if err := p.Inject(context.Background(), refusedRelayTrace(t)); err != nil {
		t.Fatal(err)
	}
	families, err := p.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	durations, costs := 0, 0
	for _, family := range families {
		switch family.GetName() {
		case "bifrost_relay_operation_duration_seconds":
			for _, m := range family.GetMetric() {
				durations++
				labels := map[string]string{}
				for _, l := range m.GetLabel() {
					labels[l.GetName()] = l.GetValue()
				}
				if labels["relay_name"] != "tavily" || labels["status_code"] != "403" || labels["error_type"] != "403" || labels["method"] != "POST" {
					t.Fatalf("refused call labels = %v", labels)
				}
				if m.GetHistogram().GetSampleCount() != 1 {
					t.Fatalf("refused call counted %d times, want 1", m.GetHistogram().GetSampleCount())
				}
			}
		case "bifrost_relay_cost_total":
			costs += len(family.GetMetric())
		}
	}
	if durations != 1 {
		t.Fatalf("want the refused call in the duration series, got %d series", durations)
	}
	if costs != 0 {
		t.Fatalf("a refused call charges nothing, got %d cost series", costs)
	}
}
