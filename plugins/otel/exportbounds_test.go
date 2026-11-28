package otel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
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

func testTarget(t *testing.T, client traceExporter, timeout time.Duration) *otelTarget {
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

func (c *failingClient) emit(_ context.Context, _ []*ResourceSpan) (traceExportResult, error) {
	c.calls.Add(1)
	return traceExportResult{}, errors.New("collector unreachable")
}
func (c *failingClient) Close() error { return nil }

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

type partialSuccessLogger struct {
	testLogger
	warnings []string
}

func (l *partialSuccessLogger) Warn(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

func TestInjectObservesPartialSuccess(t *testing.T) {
	const diagnostic = `deprecated credential "example-credential": collector span limit`
	response := &collectorpb.ExportTraceServiceResponse{
		PartialSuccess: &collectorpb.ExportTracePartialSuccess{
			RejectedSpans: 1,
			ErrorMessage:  diagnostic,
		},
	}
	for _, protocol := range []Protocol{ProtocolHTTP, ProtocolGRPC} {
		t.Run(string(protocol), func(t *testing.T) {
			log := &partialSuccessLogger{}
			oldLogger := logger
			logger = log
			t.Cleanup(func() { logger = oldLogger })
			var client traceExporter
			var calls *atomic.Int64
			switch protocol {
			case ProtocolHTTP:
				payload, err := proto.Marshal(response)
				require.NoError(t, err)
				httpCalls := &atomic.Int64{}
				calls = httpCalls
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					httpCalls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/x-protobuf")
					_, _ = w.Write(payload)
				}))
				t.Cleanup(server.Close)
				client, err = NewOtelClientHTTP(server.URL, map[string]string{"Authorization": "Bearer  example-credential"}, "", false, time.Second)
				require.NoError(t, err)
			case ProtocolGRPC:
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				receiver := &partialSuccessCollector{response: response}
				calls = &receiver.calls
				server := grpc.NewServer()
				collectorpb.RegisterTraceServiceServer(server, receiver)
				go func() { _ = server.Serve(listener) }()
				t.Cleanup(server.Stop)
				client, err = NewOtelClientGRPC(listener.Addr().String(), map[string]string{"Authorization": "Bearer  example-credential"}, "", true)
				require.NoError(t, err)
			}
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			target := testTarget(t, client, time.Second)
			target.profileIndex = 3
			target.consecutiveFailures.Store(2)
			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
			target.metricsExporter = &MetricsExporter{meter: provider.Meter("test")}
			target.metricsExporter.initMetrics()

			plugin := &OtelPlugin{targets: []*otelTarget{target}}
			trace := &schemas.Trace{
				TraceID: "0af7651916cd43dd8448eb211c80319c",
				Spans: []*schemas.Span{
					{SpanID: "b7ad6b7169203331", Name: "first"},
					{SpanID: "b7ad6b7169203332", Name: "second"},
				},
			}
			for range breakerFailureThreshold + 1 {
				require.NoError(t, plugin.Inject(t.Context(), trace))
			}
			require.EqualValues(t, breakerFailureThreshold+1, calls.Load())
			stats := plugin.ExportStats()[target.url]
			require.Zero(t, stats.Failed)
			require.Zero(t, stats.Suppressed)
			require.Zero(t, target.consecutiveFailures.Load())
			require.EqualValues(t, breakerFailureThreshold+1, stats.RejectedSpans)
			require.EqualValues(t, breakerFailureThreshold+1, stats.PartialExports)
			require.Len(t, log.warnings, 1)
			require.Contains(t, log.warnings[0], fmt.Sprintf("%q", `deprecated credential "[redacted]": collector span limit`))
			require.NotContains(t, log.warnings[0], "example-credential")
			var collected metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(t.Context(), &collected))
			counters := map[string]int64{}
			for _, scope := range collected.ScopeMetrics {
				for _, instrument := range scope.Metrics {
					if sum, ok := instrument.Data.(metricdata.Sum[int64]); ok {
						for _, point := range sum.DataPoints {
							if index, ok := point.Attributes.Value(attribute.Key("bifrost.otel.profile_index")); ok {
								require.EqualValues(t, 3, index.AsInt64())
								counters[instrument.Name] = point.Value
							}
						}
					}
				}
			}
			require.Equal(t, map[string]int64{
				"bifrost_otel_trace_rejected_spans_total":  breakerFailureThreshold + 1,
				"bifrost_otel_trace_partial_exports_total": breakerFailureThreshold + 1,
			}, counters)
		})
	}
}

func TestInject_NonProtobuf2xxAckDoesNotFailOrOpenBreaker(t *testing.T) {
	log := &partialSuccessLogger{}
	oldLogger := logger
	logger = log
	t.Cleanup(func() { logger = oldLogger })

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(server.Close)

	client, err := NewOtelClientHTTP(server.URL, nil, "", false, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	target := testTarget(t, client, time.Second)
	target.consecutiveFailures.Store(2)
	plugin := &OtelPlugin{targets: []*otelTarget{target}}
	trace := &schemas.Trace{
		TraceID: "0af7651916cd43dd8448eb211c80319c",
		Spans:   []*schemas.Span{{SpanID: "b7ad6b7169203331", Name: "first"}},
	}
	const totalExports = breakerFailureThreshold + 5
	for range totalExports {
		require.NoError(t, plugin.Inject(t.Context(), trace))
	}
	require.EqualValues(t, totalExports, calls.Load())
	stats := plugin.ExportStats()[target.url]
	require.Zero(t, stats.Failed)
	require.Zero(t, stats.Suppressed)
	require.Zero(t, stats.RejectedSpans)
	require.Zero(t, stats.PartialExports)
	require.Zero(t, target.consecutiveFailures.Load())
	require.Len(t, log.warnings, 1)
}

func TestWarningOnlyAcknowledgement(t *testing.T) {
	log := &partialSuccessLogger{}
	oldLogger := logger
	logger = log
	t.Cleanup(func() { logger = oldLogger })
	target := &otelTarget{}
	message := "collector warning for example-value " + strings.Repeat("界", 100)
	result := traceExportResult{
		partialSuccess: &collectorpb.ExportTracePartialSuccess{ErrorMessage: message},
		headers:        map[string]string{"Authorization": "Bearer example-value"},
	}
	for range exportLogThrottle + 1 {
		target.recordPartialSuccess(t.Context(), result)
	}
	require.Zero(t, target.partialExports.Load())
	require.Zero(t, target.rejectedSpans.Load())
	require.Len(t, log.warnings, 2)
	require.Contains(t, log.warnings[0], "collector warning")
	require.NotContains(t, log.warnings[0], "example-value")
	require.True(t, utf8.ValidString(result.diagnostic()))
	require.LessOrEqual(t, len(result.diagnostic()), maxCollectorDiagnosticBytes+3)
	require.True(t, strings.HasSuffix(result.diagnostic(), "..."))
}

func TestDiagnosticRedactsOverlappingValues(t *testing.T) {
	for _, tenant := range []string{"acme", "token acme"} {
		result := traceExportResult{
			partialSuccess: &collectorpb.ExportTracePartialSuccess{ErrorMessage: "invalid token acme-live-example"},
			headers:        map[string]string{"X-Tenant": tenant, "Authorization": "Bearer acme-live-example"},
		}
		for range 100 {
			diagnostic := result.diagnostic()
			require.NotContains(t, diagnostic, "live-example")
			require.NotContains(t, diagnostic, "acme")
			require.Contains(t, diagnostic, "[redacted]")
		}
	}
}

func TestDiagnosticRedactsAuthorizationWhitespace(t *testing.T) {
	for _, authorization := range []string{
		"Bearer example-credential",
		"Bearer  example-credential",
		"Bearer   example-credential ",
		"Basic  example-credential",
	} {
		t.Run(authorization, func(t *testing.T) {
			result := traceExportResult{
				partialSuccess: &collectorpb.ExportTracePartialSuccess{ErrorMessage: `deprecated credential "example-credential"; tenant example`},
				headers:        map[string]string{"authorization": authorization, "X-Tenant": "example"},
			}
			for range 100 {
				require.Equal(t, `deprecated credential "[redacted]"; tenant [redacted]`, result.diagnostic())
			}
		})
	}
}

func TestDiagnosticBoundsLargeMessageBeforeMatching(t *testing.T) {
	large := strings.Repeat("1", 4<<20)
	result := traceExportResult{
		partialSuccess: &collectorpb.ExportTracePartialSuccess{ErrorMessage: large},
		headers:        map[string]string{"X-Scope-OrgID": "1"},
	}
	start := time.Now()
	got := result.diagnostic()
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.Equal(t, "[redacted]...", got)

	// A secret straddling byte 256 is still redacted cleanly before truncation.
	prefix := strings.Repeat("a", maxCollectorDiagnosticBytes-4)
	secret := "super-secret-boundary-token"
	straddling := traceExportResult{
		partialSuccess: &collectorpb.ExportTracePartialSuccess{ErrorMessage: prefix + secret + "-tail-padding-beyond-limit"},
		headers:        map[string]string{"Authorization": "Bearer " + secret},
	}
	gotStraddle := straddling.diagnostic()
	require.NotContains(t, gotStraddle, "super-secret")
	require.NotContains(t, gotStraddle, "boundary-token")
	require.True(t, strings.HasSuffix(gotStraddle, "..."))
}
