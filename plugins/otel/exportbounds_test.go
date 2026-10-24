package otel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	metricscollector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

// blackholeListener accepts TCP connections and then never speaks. This is the shape of a
// realistic misconfiguration — a wrong port, or an ingress that terminates the connection
// but routes nowhere — and it is strictly worse than a closed port, which fails fast.
func blackholeListener(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	var conns atomic.Value
	conns.Store([]net.Conn{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				close(done)
				return
			}
			// Hold the connection open without reading or writing.
			existing, _ := conns.Load().([]net.Conn)
			conns.Store(append(existing, c))
		}
	}()
	return ln.Addr().String(), func() {
		_ = ln.Close()
		<-done
		if held, ok := conns.Load().([]net.Conn); ok {
			for _, c := range held {
				_ = c.Close()
			}
		}
	}
}

func testTarget(t *testing.T, client OtelClient, timeout time.Duration) *otelTarget {
	t.Helper()
	return &otelTarget{
		serviceName:   "test-svc",
		url:           "test://target",
		traceType:     TraceTypeGenAIExtension,
		client:        client,
		exportTimeout: timeout,
	}
}

// TestInject_GRPCExportHonoursTimeout is the regression for the outage. gRPC exports
// carried no deadline of their own and the caller passes context.Background(), so an
// endpoint that completed a TCP handshake but never replied blocked the flush goroutine
// forever — leaking a goroutine and a full trace snapshot per request.
func TestInject_GRPCExportHonoursTimeout(t *testing.T) {
	addr, stop := blackholeListener(t)
	defer stop()

	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)

	client, err := NewOtelClientGRPC(addr, nil, "", true)
	if err != nil {
		t.Fatalf("NewOtelClientGRPC: %v", err)
	}
	defer client.Close()

	const timeout = 400 * time.Millisecond
	plugin := &OtelPlugin{targets: []*otelTarget{testTarget(t, client, timeout)}}

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- plugin.Inject(context.Background(), &schemas.Trace{TraceID: "trace-grpc-timeout"}) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Inject never returned against a black-holed gRPC endpoint")
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Inject took %v against a black-holed endpoint, expected it bounded near %v", elapsed, timeout)
	}
}

// TestInject_HTTPExportHonoursTimeout covers the same bound on the HTTP path, which
// previously relied on a hardcoded 30s client timeout.
func TestInject_HTTPExportHonoursTimeout(t *testing.T) {
	addr, stop := blackholeListener(t)
	defer stop()

	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)

	const timeout = 400 * time.Millisecond
	client, err := NewOtelClientHTTP("http://"+addr, nil, "", true, timeout)
	if err != nil {
		t.Fatalf("NewOtelClientHTTP: %v", err)
	}
	defer client.Close()

	plugin := &OtelPlugin{targets: []*otelTarget{testTarget(t, client, timeout)}}

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- plugin.Inject(context.Background(), &schemas.Trace{TraceID: "trace-http-timeout"}) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Inject never returned against a black-holed HTTP endpoint")
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Inject took %v against a black-holed endpoint, expected it bounded near %v", elapsed, timeout)
	}
}

// failingClient always errors, standing in for a permanently misconfigured collector.
type failingClient struct{ calls atomic.Int64 }

func (c *failingClient) Emit(_ context.Context, _ []*ResourceSpan) error {
	c.calls.Add(1)
	return errors.New("collector unreachable")
}
func (c *failingClient) Close() error { return nil }

// TestTraceExportMetrics verifies registration, per-profile totals and recovery
// through Init -> Inject -> OTLP, including collection without new requests.
func TestTraceExportMetrics(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE", "cumulative")
	var recovered atomic.Bool
	var traceCalls atomic.Int64
	var mu sync.Mutex
	var received []*metricspb.Metric
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/failing-traces":
			traceCalls.Add(1)
			if !recovered.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case "/healthy-traces":
		case "/v1/metrics":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read metrics: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var req metricscollector.ExportMetricsServiceRequest
			if err := proto.Unmarshal(body, &req); err != nil {
				t.Errorf("decode OTLP metrics: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			for _, rm := range req.ResourceMetrics {
				for _, sm := range rm.ScopeMetrics {
					received = append(received, sm.Metrics...)
				}
			}
			mu.Unlock()
		default:
			t.Errorf("unexpected collector path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(collector.Close)

	// Profiles sharing a service and destination must still have distinct series.
	profiles := []*Profile{{Enabled: false}}
	for _, path := range []string{"/failing-traces", "/healthy-traces"} {
		profiles = append(profiles, &Profile{
			Enabled: true, TracesEnabled: true, MetricsEnabled: true,
			ServiceName: "same-service", Protocol: ProtocolHTTP, Insecure: true,
			CollectorURL:        schemas.NewSecretVar(collector.URL + path),
			MetricsEndpoint:     schemas.NewSecretVar(collector.URL + "/v1/metrics"),
			MetricsPushInterval: 300,
		})
	}
	metricsOnly, tracesOnly := *profiles[2], *profiles[2]
	metricsOnly.TracesEnabled, metricsOnly.CollectorURL = false, nil
	tracesOnly.MetricsEnabled, tracesOnly.MetricsEndpoint = false, nil
	profiles = append(profiles, &metricsOnly, &tracesOnly)
	plugin, err := Init(context.Background(), &Config{Profiles: profiles}, testLogger{}, nil, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })

	check := func(wantFailed, wantSuppressed int64) {
		t.Helper()
		mu.Lock()
		received = nil
		mu.Unlock()
		for _, target := range plugin.targets {
			if target.metricsExporter != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := target.metricsExporter.provider.ForceFlush(ctx)
				cancel()
				require.NoError(t, err)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		want := map[string]map[int64]int64{
			"bifrost_otel_trace_exports_failed_total":     {1: wantFailed, 2: 0},
			"bifrost_otel_trace_exports_suppressed_total": {1: wantSuppressed, 2: 0},
		}
		seen := make(map[string]map[int64]int64)
		for _, m := range received {
			if _, ok := want[m.Name]; !ok {
				continue
			}
			sum := m.GetSum()
			require.NotNil(t, sum, m.Name)
			require.True(t, sum.IsMonotonic, m.Name)
			require.Equal(t, metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, sum.AggregationTemporality)
			require.Len(t, sum.DataPoints, 1)
			dp := sum.DataPoints[0]
			index, instance := int64(-1), ""
			for _, attr := range dp.Attributes {
				switch attr.Key {
				case "otel_profile_index":
					index = attr.Value.GetIntValue()
				case "bifrost_instance_id":
					instance = attr.Value.GetStringValue()
				}
			}
			require.Len(t, dp.Attributes, 2)
			require.Equal(t, serviceInstanceID, instance)
			if seen[m.Name] == nil {
				seen[m.Name] = make(map[int64]int64)
			}
			require.NotContains(t, seen[m.Name], index, "duplicate series for %s", m.Name)
			seen[m.Name][index] = dp.GetAsInt()
		}
		require.Equal(t, want, seen)
	}

	check(0, 0)
	trace := &schemas.Trace{TraceID: "export-health"}
	for range breakerFailureThreshold + 3 {
		require.NoError(t, plugin.Inject(context.Background(), trace))
	}
	require.EqualValues(t, breakerFailureThreshold, traceCalls.Load())
	check(breakerFailureThreshold, 3)
	check(breakerFailureThreshold, 3) // Idle collection must not reset or re-add totals.

	recovered.Store(true)
	plugin.targets[0].breakerOpenUntil.Store(time.Now().Add(-time.Second).UnixNano())
	require.NoError(t, plugin.Inject(context.Background(), trace))
	require.Zero(t, plugin.targets[0].breakerOpenUntil.Load())
	check(breakerFailureThreshold, 3) // Recovery keeps the lifetime totals.
}

// Initialization and final metrics export are outside the timed loop. Trace export
// is stubbed to isolate Inject from network I/O, with an empty trace in each case.
func BenchmarkInjectExportHealth(b *testing.B) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	b.Cleanup(collector.Close)
	for _, tc := range []struct {
		name           string
		metricsEnabled bool
		breakerOpen    bool
	}{
		{"metrics_disabled/healthy", false, false},
		{"metrics_disabled/breaker_open", false, true},
		{"metrics_enabled/healthy", true, false},
		{"metrics_enabled/breaker_open", true, true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			plugin, err := Init(context.Background(), &Config{Profiles: []*Profile{{
				Enabled: true, TracesEnabled: true, MetricsEnabled: tc.metricsEnabled,
				Protocol: ProtocolHTTP, Insecure: true,
				CollectorURL:        schemas.NewSecretVar(collector.URL + "/v1/traces"),
				MetricsEndpoint:     schemas.NewSecretVar(collector.URL + "/v1/metrics"),
				MetricsPushInterval: 300,
			}}}, testLogger{}, nil, "")
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = plugin.Cleanup() })
			target := plugin.targets[0]
			_ = target.client.Close()
			target.client = stubOtelClient{}
			if tc.breakerOpen {
				target.breakerOpenUntil.Store(time.Now().Add(time.Hour).UnixNano())
			}
			trace := &schemas.Trace{TraceID: "bench"}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = plugin.Inject(context.Background(), trace)
			}
		})
	}
}

// TestInject_CircuitBreakerStopsDialling verifies that a permanently bad endpoint stops
// costing an export attempt per request. Without the breaker, every request pays the full
// export timeout for as long as the misconfiguration lasts.
func TestInject_CircuitBreakerStopsDialling(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)

	client := &failingClient{}
	target := testTarget(t, client, time.Second)
	plugin := &OtelPlugin{targets: []*otelTarget{target}}

	// Drive well past the threshold.
	const attempts = breakerFailureThreshold + 50
	for i := range attempts {
		if err := plugin.Inject(context.Background(), &schemas.Trace{TraceID: "trace-breaker"}); err != nil {
			t.Fatalf("Inject returned an error on attempt %d: %v", i, err)
		}
	}

	if calls := client.calls.Load(); calls > breakerFailureThreshold {
		t.Fatalf("breaker did not open: %d export attempts for %d injects (threshold %d)", calls, attempts, breakerFailureThreshold)
	}
	if suppressed := target.suppressedExports.Load(); suppressed == 0 {
		t.Fatal("expected suppressed exports once the breaker opened, got 0")
	}
}

// TestBreakerRecoversAfterCooldown verifies the breaker lets a probe through once the
// cooldown elapses, so a collector that comes back is picked up again.
func TestBreakerRecoversAfterCooldown(t *testing.T) {
	target := &otelTarget{url: "test://recover"}

	for range breakerFailureThreshold {
		target.tripBreaker()
	}
	if !target.breakerOpen() {
		t.Fatal("breaker should be open after reaching the failure threshold")
	}

	// Expire the cooldown.
	target.breakerOpenUntil.Store(time.Now().Add(-time.Millisecond).UnixNano())
	if target.breakerOpen() {
		t.Fatal("breaker should allow a probe once the cooldown has elapsed")
	}

	target.resetBreaker()
	if target.breakerOpen() {
		t.Fatal("breaker should stay closed after a successful export")
	}
	if got := target.consecutiveFailures.Load(); got != 0 {
		t.Fatalf("consecutiveFailures = %d after reset, want 0", got)
	}
}

// TestBreakerAllowsSingleProbeUnderConcurrency locks in the half-open guarantee. A batch of
// traces flushing at the instant the cooldown expires races on the same transition, and only
// one of them may dial a collector that is probably still dead. If every racer got through,
// each would block for a full exportTimeout, which is the cost the breaker exists to avoid.
func TestBreakerAllowsSingleProbeUnderConcurrency(t *testing.T) {
	target := &otelTarget{url: "test://concurrent-probe"}

	for range breakerFailureThreshold {
		target.tripBreaker()
	}
	// Expire the cooldown so every goroutine below sees the breaker as probe-ready.
	target.breakerOpenUntil.Store(time.Now().Add(-time.Millisecond).UnixNano())

	const callers = 64
	var allowed atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			if !target.breakerOpen() {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != 1 {
		t.Fatalf("breaker allowed %d probes through, want exactly 1", got)
	}
	if got := target.suppressedExports.Load(); got != callers-1 {
		t.Fatalf("suppressedExports = %d, want %d", got, callers-1)
	}
}

func TestResolveExportTimeout(t *testing.T) {
	cases := []struct {
		name    string
		seconds int
		want    time.Duration
		wantErr bool
	}{
		{name: "unset uses default", seconds: 0, want: DefaultExportTimeout},
		{name: "explicit value", seconds: 12, want: 12 * time.Second},
		{name: "at max", seconds: int(MaxExportTimeout / time.Second), want: MaxExportTimeout},
		{name: "negative rejected", seconds: -1, wantErr: true},
		{name: "above max rejected", seconds: int(MaxExportTimeout/time.Second) + 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveExportTimeout(tc.seconds)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %d seconds", tc.seconds)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveExportTimeout(%d) = %v, want %v", tc.seconds, got, tc.want)
			}
		})
	}
}
