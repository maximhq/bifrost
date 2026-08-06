package otel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

// fakeLogClient records what would have been exported, and can be made to fail.
type fakeLogClient struct {
	calls    atomic.Int64
	fail     bool
	lastLogs atomic.Value // []*ResourceLog
}

func (c *fakeLogClient) EmitLogs(_ context.Context, logs []*ResourceLog) error {
	c.calls.Add(1)
	c.lastLogs.Store(logs)
	if c.fail {
		return errors.New("logs collector unreachable")
	}
	return nil
}
func (c *fakeLogClient) Close() error { return nil }

// recordingClient records successful trace exports.
type recordingClient struct{ calls atomic.Int64 }

func (c *recordingClient) Emit(_ context.Context, _ []*ResourceSpan) error {
	c.calls.Add(1)
	return nil
}
func (c *recordingClient) Close() error { return nil }

// TestInject_LogFailuresDoNotSuppressTraces is the independent-failure-domain guarantee:
// a permanently broken logs endpoint opens only the logs breaker, while traces keep
// exporting on every request.
func TestInject_LogFailuresDoNotSuppressTraces(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)

	traceClient := &recordingClient{}
	logClient := &fakeLogClient{fail: true}
	target := &otelTarget{
		serviceName:   "svc",
		url:           "test://traces",
		logsURL:       "test://logs",
		client:        traceClient,
		logClient:     logClient,
		exportTimeout: time.Second,
	}
	plugin := &OtelPlugin{targets: []*otelTarget{target}}

	const attempts = breakerFailureThreshold + 20
	for range attempts {
		if err := plugin.Inject(context.Background(), chatTrace()); err != nil {
			t.Fatalf("Inject: %v", err)
		}
	}

	if got := traceClient.calls.Load(); got != attempts {
		t.Errorf("trace exports = %d, want %d — a failing logs endpoint must not suppress traces", got, attempts)
	}
	if got := logClient.calls.Load(); got > breakerFailureThreshold {
		t.Errorf("log exports = %d, want the logs breaker to open at %d", got, breakerFailureThreshold)
	}
	if target.breakerOpen() {
		t.Error("the trace breaker must stay closed while only logs are failing")
	}
	if target.logBreaker.suppressedExports.Load() == 0 {
		t.Error("expected suppressed log exports once the logs breaker opened")
	}
}

// TestInject_TraceFailuresDoNotSuppressLogs is the mirror case: an open trace breaker must
// not stop GenAI events from reaching the logs pipeline.
func TestInject_TraceFailuresDoNotSuppressLogs(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)

	logClient := &fakeLogClient{}
	target := &otelTarget{
		serviceName:   "svc",
		url:           "test://traces",
		logsURL:       "test://logs",
		client:        &failingClient{},
		logClient:     logClient,
		exportTimeout: time.Second,
	}
	plugin := &OtelPlugin{targets: []*otelTarget{target}}

	const attempts = breakerFailureThreshold + 20
	for range attempts {
		if err := plugin.Inject(context.Background(), chatTrace()); err != nil {
			t.Fatalf("Inject: %v", err)
		}
	}

	if got := logClient.calls.Load(); got != attempts {
		t.Errorf("log exports = %d, want %d — an open trace breaker must not suppress logs", got, attempts)
	}
}

// TestInject_NoLogClientSkipsConversion asserts profiles without log export do no work.
func TestInject_NoLogClientSkipsConversion(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	target := &otelTarget{serviceName: "svc", url: "test://traces", client: &recordingClient{}, exportTimeout: time.Second}
	plugin := &OtelPlugin{targets: []*otelTarget{target}}
	if err := plugin.Inject(context.Background(), chatTrace()); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if target.logBreaker.failedExports.Load() != 0 {
		t.Error("a profile without log export should never touch the logs breaker")
	}
}

// TestExportStats_ReportsLogsEndpointSeparately asserts logs health is visible under its
// own endpoint, since it fails independently of traces.
func TestExportStats_ReportsLogsEndpointSeparately(t *testing.T) {
	target := &otelTarget{url: "http://collector:4318/v1/traces", logsURL: "http://collector:4318/v1/logs", logClient: &fakeLogClient{}}
	target.tripBreaker()
	target.logBreaker.tripBreaker()
	target.logBreaker.tripBreaker()

	stats := (&OtelPlugin{targets: []*otelTarget{target}}).ExportStats()
	if got := stats["http://collector:4318/v1/traces"].Failed; got != 1 {
		t.Errorf("trace failures = %d, want 1", got)
	}
	if got := stats["http://collector:4318/v1/logs"].Failed; got != 2 {
		t.Errorf("log failures = %d, want 2", got)
	}
}

// TestOtelLogClientHTTP_SendsValidExportRequest asserts the wire payload is a decodable
// ExportLogsServiceRequest with the right content type and configured headers.
func TestOtelLogClientHTTP_SendsValidExportRequest(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)

	type captured struct {
		contentType string
		auth        string
		req         *collectorlogspb.ExportLogsServiceRequest
	}
	got := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var decoded collectorlogspb.ExportLogsServiceRequest
		if err := proto.Unmarshal(body, &decoded); err != nil {
			t.Errorf("payload is not an ExportLogsServiceRequest: %v", err)
		}
		got <- captured{contentType: r.Header.Get("Content-Type"), auth: r.Header.Get("Authorization"), req: &decoded}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewOtelLogClientHTTP(srv.URL, map[string]string{"Authorization": "Bearer t"}, "", true, 2*time.Second)
	if err != nil {
		t.Fatalf("NewOtelLogClientHTTP: %v", err)
	}
	defer client.Close()

	rl := (&OtelPlugin{}).convertTraceToResourceLogs("svc", chatTrace(), logTestTarget(false, false))
	if err := client.EmitLogs(context.Background(), []*ResourceLog{rl}); err != nil {
		t.Fatalf("EmitLogs: %v", err)
	}

	c := <-got
	if c.contentType != "application/x-protobuf" {
		t.Errorf("Content-Type = %q, want application/x-protobuf", c.contentType)
	}
	if c.auth != "Bearer t" {
		t.Errorf("Authorization = %q, want the configured header", c.auth)
	}
	records := c.req.ResourceLogs[0].ScopeLogs[0].LogRecords
	if len(records) != 1 || records[0].EventName != EventNameInferenceDetails {
		t.Errorf("decoded records = %v, want one inference-details event", records)
	}
}

// TestOtelLogClientHTTP_NonOKIsAnError asserts a rejecting collector surfaces as an error
// so the breaker can trip.
func TestOtelLogClientHTTP_NonOKIsAnError(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	client, err := NewOtelLogClientHTTP(srv.URL, nil, "", true, 2*time.Second)
	if err != nil {
		t.Fatalf("NewOtelLogClientHTTP: %v", err)
	}
	defer client.Close()
	if err := client.EmitLogs(context.Background(), []*ResourceLog{{}}); err == nil {
		t.Error("expected an error when the collector rejects the export")
	}
}

// TestOtelLogClientHTTP_HonoursTimeout covers the export bound on the logs path: a
// black-holed endpoint must not hold the flush goroutine open.
func TestOtelLogClientHTTP_HonoursTimeout(t *testing.T) {
	addr, stop := blackholeListener(t)
	defer stop()
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)

	const timeout = 400 * time.Millisecond
	client, err := NewOtelLogClientHTTP("http://"+addr, nil, "", true, timeout)
	if err != nil {
		t.Fatalf("NewOtelLogClientHTTP: %v", err)
	}
	defer client.Close()

	start := time.Now()
	if err := client.EmitLogs(context.Background(), []*ResourceLog{{}}); err == nil {
		t.Error("expected an error against a black-holed endpoint")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("EmitLogs took %v, expected it bounded near %v", elapsed, timeout)
	}
}

// TestLogsConfigStorageRoundTrip asserts the new logs fields survive persistence,
// including env-var indirection on the endpoint.
func TestLogsConfigStorageRoundTrip(t *testing.T) {
	t.Setenv("OTEL_LOGS_URL", "http://collector:4318/v1/logs")
	raw := `{"profiles": [{
		"service_name": "svc",
		"collector_url": "http://collector:4318/v1/traces",
		"trace_type": "genai_extension",
		"protocol": "http",
		"logs_enabled": true,
		"logs_endpoint": "env.OTEL_LOGS_URL",
		"logs_disable_content_logging": true
	}]}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stored, err := cfg.MarshalForStorage()
	if err != nil {
		t.Fatalf("MarshalForStorage: %v", err)
	}

	var asMap map[string]any
	if err := sonic.Unmarshal(stored, &asMap); err != nil {
		t.Fatalf("stored not an object: %v", err)
	}
	profile := asMap["profiles"].([]any)[0].(map[string]any)
	if got := profile["logs_endpoint"]; got != "env.OTEL_LOGS_URL" {
		t.Errorf("stored logs_endpoint = %v, want the env reference flattened to a string", got)
	}

	var back Config
	if err := json.Unmarshal(stored, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	p := back.Profiles[0]
	if !p.LogsEnabled {
		t.Error("logs_enabled lost in round-trip")
	}
	if !p.LogsDisableContentLogging {
		t.Error("logs_disable_content_logging lost in round-trip")
	}
	if got := p.LogsEndpoint.GetValue(); got != "http://collector:4318/v1/logs" {
		t.Errorf("round-trip logs_endpoint = %q, want the resolved env value", got)
	}

	// Redaction keeps the env var name (so the UI can round-trip the edit) and hides the
	// resolved value.
	redacted := back.Redacted().Profiles[0]
	if redacted.LogsEndpoint.GetValue() == "http://collector:4318/v1/logs" {
		t.Error("redacted logs_endpoint still exposes the resolved env value")
	}
}

// TestBuildTargetRequiresLogsEndpoint asserts logs_enabled without an endpoint is a
// config error rather than a silently disabled signal, matching metrics_endpoint.
func TestBuildTargetRequiresLogsEndpoint(t *testing.T) {
	logger = testLogger{}
	raw := `{"profiles": [{
		"collector_url": "http://collector:4318/v1/traces",
		"trace_type": "genai_extension",
		"protocol": "http",
		"logs_enabled": true
	}]}`
	var cfg Config
	if err := sonic.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := Init(context.Background(), &cfg, testLogger{}, nil, ""); err == nil {
		t.Error("expected an error when logs_enabled is set without logs_endpoint")
	}
}

// TestInitBuildsLogClient asserts an enabled profile ends up with a live logs client
// pointed at its own endpoint.
func TestInitBuildsLogClient(t *testing.T) {
	logger = testLogger{}
	raw := `{"profiles": [{
		"collector_url": "http://collector:4318/v1/traces",
		"trace_type": "genai_extension",
		"protocol": "http",
		"logs_enabled": true,
		"logs_endpoint": "http://collector:4318/v1/logs"
	}]}`
	var cfg Config
	if err := sonic.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugin, err := Init(context.Background(), &cfg, testLogger{}, nil, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
	if plugin.targets[0].logClient == nil {
		t.Fatal("logs_enabled profile has no logs client")
	}
	if got := plugin.targets[0].logsURL; got != "http://collector:4318/v1/logs" {
		t.Errorf("logsURL = %q, want the configured endpoint", got)
	}
}

// TestLogsDisabledByDefault asserts existing configs are untouched: no logs client unless
// the profile opts in.
func TestLogsDisabledByDefault(t *testing.T) {
	logger = testLogger{}
	var cfg Config
	if err := sonic.Unmarshal([]byte(`{"profiles": [{"collector_url": "a:4317", "trace_type": "genai_extension", "protocol": "grpc"}]}`), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Profiles[0].LogsEnabled {
		t.Error("logs_enabled should default to false when omitted")
	}
	plugin, err := Init(context.Background(), &cfg, testLogger{}, nil, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
	if plugin.targets[0].logClient != nil {
		t.Error("no logs client should be built for a profile that did not opt in")
	}
}
