package otel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

// TestConfigUnmarshalLegacySingleObject verifies that a legacy single-object config
// (no "profiles" key) is normalized into a one-element Profiles slice, with its
// plugin_span_filter hoisted to the shared Config level.
func TestConfigUnmarshalLegacySingleObject(t *testing.T) {
	raw := `{
		"service_name": "svc",
		"collector_url": "localhost:4317",
		"trace_type": "genai_extension",
		"protocol": "grpc",
		"headers": {"Authorization": "env.OTEL_TOKEN"},
		"plugin_span_filter": {"mode": "exclude", "plugins": ["logging"]}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Profiles) != 1 {
		t.Fatalf("Profiles len = %d, want 1", len(cfg.Profiles))
	}
	p := cfg.Profiles[0]
	if p.ServiceName != "svc" {
		t.Errorf("ServiceName = %q, want svc", p.ServiceName)
	}
	if p.CollectorURL.GetValue() != "localhost:4317" {
		t.Errorf("CollectorURL = %q, want localhost:4317", p.CollectorURL.GetValue())
	}
	if p.Protocol != ProtocolGRPC {
		t.Errorf("Protocol = %q, want grpc", p.Protocol)
	}
	if p.Headers["Authorization"] != "env.OTEL_TOKEN" {
		t.Errorf("Headers[Authorization] = %q, want env.OTEL_TOKEN", p.Headers["Authorization"])
	}
	if cfg.PluginSpanFilter == nil || cfg.PluginSpanFilter.Mode != PluginSpanFilterModeExclude {
		t.Fatalf("PluginSpanFilter not hoisted: %+v", cfg.PluginSpanFilter)
	}
}

// TestConfigUnmarshalWrapperArray verifies the canonical wrapper with multiple profiles.
func TestConfigUnmarshalWrapperArray(t *testing.T) {
	raw := `{
		"profiles": [
			{"collector_url": "host-a:4317", "trace_type": "genai_extension", "protocol": "grpc"},
			{"collector_url": "host-b:4318", "trace_type": "genai_extension", "protocol": "http"}
		],
		"plugin_span_filter": {"mode": "include", "plugins": ["guardrails"]}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Profiles) != 2 {
		t.Fatalf("Profiles len = %d, want 2", len(cfg.Profiles))
	}
	if cfg.Profiles[0].CollectorURL.GetValue() != "host-a:4317" || cfg.Profiles[0].Protocol != ProtocolGRPC {
		t.Errorf("profile 0 wrong: %+v", cfg.Profiles[0])
	}
	if cfg.Profiles[1].CollectorURL.GetValue() != "host-b:4318" || cfg.Profiles[1].Protocol != ProtocolHTTP {
		t.Errorf("profile 1 wrong: %+v", cfg.Profiles[1])
	}
	if cfg.PluginSpanFilter == nil || cfg.PluginSpanFilter.Mode != PluginSpanFilterModeInclude {
		t.Fatalf("PluginSpanFilter = %+v, want include", cfg.PluginSpanFilter)
	}
}

// TestConfigUnmarshalHoistFromFirstProfile verifies that when the top-level
// plugin_span_filter is absent in a wrapper, it is hoisted from the first profile
// that carries one.
func TestConfigUnmarshalHoistFromFirstProfile(t *testing.T) {
	raw := `{
		"profiles": [
			{"collector_url": "a:4317", "trace_type": "genai_extension", "protocol": "grpc"},
			{"collector_url": "b:4317", "trace_type": "genai_extension", "protocol": "grpc",
			 "plugin_span_filter": {"mode": "exclude", "plugins": ["telemetry"]}}
		]
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.PluginSpanFilter == nil || cfg.PluginSpanFilter.Mode != PluginSpanFilterModeExclude {
		t.Fatalf("PluginSpanFilter not hoisted from profile: %+v", cfg.PluginSpanFilter)
	}
	if len(cfg.PluginSpanFilter.Plugins) != 1 || cfg.PluginSpanFilter.Plugins[0] != "telemetry" {
		t.Errorf("hoisted filter plugins = %v, want [telemetry]", cfg.PluginSpanFilter.Plugins)
	}
}

// TestProfileInsecureDefault verifies Insecure defaults to true when omitted and is
// honored when set explicitly — per profile.
func TestProfileInsecureDefault(t *testing.T) {
	raw := `{
		"profiles": [
			{"collector_url": "a:4317", "trace_type": "genai_extension", "protocol": "grpc"},
			{"collector_url": "b:4317", "trace_type": "genai_extension", "protocol": "grpc", "insecure": false}
		]
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Profiles[0].Insecure {
		t.Errorf("profile 0 Insecure = false, want true (default)")
	}
	if cfg.Profiles[1].Insecure {
		t.Errorf("profile 1 Insecure = true, want false (explicit)")
	}
}

// TestProfileEnabledDefault verifies Enabled defaults to true when omitted and is honored
// when set explicitly.
func TestProfileEnabledDefault(t *testing.T) {
	raw := `{
		"profiles": [
			{"collector_url": "a:4317", "trace_type": "genai_extension", "protocol": "grpc"},
			{"collector_url": "b:4317", "trace_type": "genai_extension", "protocol": "grpc", "enabled": false}
		]
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Profiles[0].Enabled {
		t.Errorf("profile 0 Enabled = false, want true (default)")
	}
	if cfg.Profiles[1].Enabled {
		t.Errorf("profile 1 Enabled = true, want false (explicit)")
	}
}

// TestInitSkipsDisabledProfile verifies a disabled profile is not field-validated,
// so an incomplete-but-disabled profile is allowed alongside a valid enabled one.
func TestInitSkipsDisabledProfile(t *testing.T) {
	raw := `{"profiles": [
		{"collector_url": "a:4317", "trace_type": "genai_extension", "protocol": "grpc"},
		{"enabled": false}
	]}`

	var cfg Config
	if err := sonic.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Profiles) != 2 {
		t.Errorf("profiles len = %d, want 2", len(cfg.Profiles))
	}
	plugin, err := Init(context.Background(), &cfg, testLogger{}, nil, "")
	if err != nil {
		t.Fatalf("Init with disabled incomplete profile: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
	if len(plugin.targets) != 1 {
		t.Errorf("targets len = %d, want 1", len(plugin.targets))
	}
}

// TestMarshalForStorageRoundTrip verifies storage marshalling produces the canonical
// wrapper with SecretVar fields flattened to strings, and that it round-trips back.
func TestMarshalForStorageRoundTrip(t *testing.T) {
	t.Setenv("OTEL_TOKEN", "secret-token")
	t.Setenv("OTEL_SECOND_TOKEN", "second-token")
	t.Setenv("OTEL_URL", "collector:4317")
	raw := `{
		"profiles": [
			{
				"service_name": "svc-a",
				"collector_url": "env.OTEL_URL",
				"trace_type": "genai_extension",
				"protocol": "grpc",
				"overhead_breakdown_enabled": true,
				"headers": {"Authorization": "env.OTEL_TOKEN", "X-Tenant": "acme"}
			},
			{
				"service_name": "svc-b",
				"collector_url": "http://collector-b:4318/v1/traces",
				"trace_type": "genai_extension",
				"protocol": "http",
				"headers": {"Authorization": "env.OTEL_SECOND_TOKEN", "X-Tenant": "beta"}
			}
		],
		"plugin_span_filter": {"mode": "exclude", "plugins": ["logging"]}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	stored, err := cfg.MarshalForStorage()
	if err != nil {
		t.Fatalf("MarshalForStorage: %v", err)
	}

	// Storage form must be a wrapper object with a profiles array.
	var asMap map[string]any
	if err := sonic.Unmarshal(stored, &asMap); err != nil {
		t.Fatalf("stored not an object: %v", err)
	}
	profiles, ok := asMap["profiles"].([]any)
	if !ok || len(profiles) != 2 {
		t.Fatalf("stored profiles = %v, want 2-element array", asMap["profiles"])
	}
	if _, ok := asMap["plugin_span_filter"]; !ok {
		t.Errorf("plugin_span_filter missing from stored config")
	}

	// Round-trip back into a Config.
	var back Config
	if err := json.Unmarshal(stored, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if len(back.Profiles) != 2 {
		t.Fatalf("round-trip profiles len = %d, want 2", len(back.Profiles))
	}
	// overhead_breakdown_enabled must survive the storage whitelist (profileForStorage).
	if !back.Profiles[0].OverheadBreakdownEnabled {
		t.Errorf("round-trip profile 0 overhead_breakdown_enabled = false, want true (dropped by storage whitelist)")
	}
	if back.Profiles[1].OverheadBreakdownEnabled {
		t.Errorf("round-trip profile 1 overhead_breakdown_enabled = true, want false")
	}
	if back.PluginSpanFilter == nil || back.PluginSpanFilter.Mode != PluginSpanFilterModeExclude {
		t.Fatalf("round-trip plugin_span_filter = %+v, want exclude", back.PluginSpanFilter)
	}
	if len(back.PluginSpanFilter.Plugins) != 1 || back.PluginSpanFilter.Plugins[0] != "logging" {
		t.Errorf("round-trip plugin_span_filter plugins = %v, want [logging]", back.PluginSpanFilter.Plugins)
	}
	// Profile 0 CollectorURL was an env ref; stored as "env.OTEL_URL" and re-resolved on load.
	if back.Profiles[0].CollectorURL.GetValue() != "collector:4317" {
		t.Errorf("round-trip profile 0 collector_url = %q, want collector:4317", back.Profiles[0].CollectorURL.GetValue())
	}
	if back.Profiles[0].Headers["Authorization"] != "env.OTEL_TOKEN" {
		t.Errorf("round-trip profile 0 header env ref not preserved: %q", back.Profiles[0].Headers["Authorization"])
	}
	if back.Profiles[0].Headers["X-Tenant"] != "acme" {
		t.Errorf("round-trip profile 0 literal header lost: %q", back.Profiles[0].Headers["X-Tenant"])
	}
	if back.Profiles[1].CollectorURL.GetValue() != "http://collector-b:4318/v1/traces" {
		t.Errorf("round-trip profile 1 collector_url = %q, want http://collector-b:4318/v1/traces", back.Profiles[1].CollectorURL.GetValue())
	}
	if back.Profiles[1].Headers["Authorization"] != "env.OTEL_SECOND_TOKEN" {
		t.Errorf("round-trip profile 1 header env ref not preserved: %q", back.Profiles[1].Headers["Authorization"])
	}
	if back.Profiles[1].Headers["X-Tenant"] != "beta" {
		t.Errorf("round-trip profile 1 literal header lost: %q", back.Profiles[1].Headers["X-Tenant"])
	}
}

// TestRedactedHeaders verifies header redaction: env references are preserved while
// literal values are masked.
func TestRedactedHeaders(t *testing.T) {
	raw := `{
		"profiles": [
			{
				"collector_url": "localhost:4317",
				"trace_type": "genai_extension",
				"protocol": "grpc",
				"headers": {"Authorization": "env.OTEL_TOKEN", "X-Api-Key": "supersecretvalue123"}
			}
		]
	}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Profiles[0].Enabled {
		t.Fatalf("profile Enabled = false, want true from JSON default")
	}
	if !cfg.Profiles[0].Insecure {
		t.Fatalf("profile Insecure = false, want true from JSON default")
	}
	red := cfg.Redacted()
	got := red.Profiles[0].Headers
	if got["Authorization"] != "env.OTEL_TOKEN" {
		t.Errorf("env header redacted = %q, want env.OTEL_TOKEN (preserved)", got["Authorization"])
	}
	if got["X-Api-Key"] == "supersecretvalue123" {
		t.Errorf("literal header was not masked")
	}
	// Original must be untouched.
	if cfg.Profiles[0].Headers["X-Api-Key"] != "supersecretvalue123" {
		t.Errorf("Redacted mutated the original config")
	}
}

// TestInjectEnvToHeaders verifies env resolution and the missing-var error.
func TestInjectEnvToHeaders(t *testing.T) {
	t.Setenv("OTEL_TOKEN", "resolved")
	h := map[string]string{"Authorization": "env.OTEL_TOKEN", "X-Plain": "literal"}
	if err := injectEnvToHeaders(h); err != nil {
		t.Fatalf("injectEnvToHeaders: %v", err)
	}
	if h["Authorization"] != "resolved" {
		t.Errorf("Authorization = %q, want resolved", h["Authorization"])
	}
	if h["X-Plain"] != "literal" {
		t.Errorf("X-Plain = %q, want literal (unchanged)", h["X-Plain"])
	}

	missing := map[string]string{"Authorization": "env.OTEL_MISSING_VAR"}
	if err := injectEnvToHeaders(missing); err == nil {
		t.Errorf("expected error for missing env var, got nil")
	}
}

// TestInitMultiProfileValidation verifies per-profile validation errors.
func TestInitMultiProfileValidation(t *testing.T) {
	// Missing profiles entirely.
	var empty Config
	if err := sonic.Unmarshal([]byte(`{"profiles": []}`), &empty); err != nil {
		t.Fatalf("unmarshal empty profiles: %v", err)
	}
	if _, err := Init(context.Background(), &empty, testLogger{}, nil, ""); err == nil {
		t.Errorf("expected error for empty profiles")
	}

	// Second profile missing protocol.
	bad := `{"profiles": [
		{"collector_url": "a:4317", "trace_type": "genai_extension", "protocol": "grpc"},
		{"collector_url": "b:4317", "trace_type": "genai_extension"}
	]}`
	var badCfg Config
	if err := sonic.Unmarshal([]byte(bad), &badCfg); err != nil {
		t.Fatalf("unmarshal bad profiles: %v", err)
	}
	if _, err := Init(context.Background(), &badCfg, testLogger{}, nil, ""); err == nil {
		t.Errorf("expected error for profile missing protocol")
	}

	// Valid multi-profile.
	good := `{"profiles": [
		{"collector_url": "a:4317", "trace_type": "genai_extension", "protocol": "grpc"},
		{"collector_url": "b:4318", "trace_type": "genai_extension", "protocol": "http"}
	]}`
	var cfg Config
	if err := sonic.Unmarshal([]byte(good), &cfg); err != nil {
		t.Fatalf("unmarshal valid profiles: %v", err)
	}
	if len(cfg.Profiles) != 2 {
		t.Errorf("profiles len = %d, want 2", len(cfg.Profiles))
	}
	plugin, err := Init(context.Background(), &cfg, testLogger{}, nil, "")
	if err != nil {
		t.Fatalf("Init valid profiles: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
	if len(plugin.targets) != 2 {
		t.Errorf("targets len = %d, want 2", len(plugin.targets))
	}
}

// TestProfileTracesEnabledDefault verifies TracesEnabled defaults to true when omitted
// and is honored when set explicitly.
func TestProfileTracesEnabledDefault(t *testing.T) {
	raw := `{
		"profiles": [
			{"collector_url": "a:4317", "trace_type": "genai_extension", "protocol": "grpc"},
			{"protocol": "http", "metrics_enabled": true, "metrics_endpoint": "b:4318", "traces_enabled": false}
		]
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Profiles[0].TracesEnabled {
		t.Errorf("profile 0 TracesEnabled = false, want true (default)")
	}
	if cfg.Profiles[1].TracesEnabled {
		t.Errorf("profile 1 TracesEnabled = true, want false (explicit)")
	}
}

// TestInitMetricsOnlyProfile verifies a metrics-only profile (traces disabled, no
// collector_url) builds a target with a metrics exporter but no trace client.
func TestInitMetricsOnlyProfile(t *testing.T) {
	raw := `{"profiles": [
		{"traces_enabled": false, "protocol": "http", "metrics_enabled": true, "metrics_endpoint": "localhost:4318"}
	]}`

	var cfg Config
	if err := sonic.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugin, err := Init(context.Background(), &cfg, testLogger{}, nil, "")
	if err != nil {
		t.Fatalf("Init metrics-only profile: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
	if len(plugin.targets) != 1 {
		t.Fatalf("targets len = %d, want 1", len(plugin.targets))
	}
	if plugin.targets[0].client != nil {
		t.Errorf("metrics-only target has a trace client, want nil")
	}
	if plugin.targets[0].metricsExporter == nil {
		t.Errorf("metrics-only target has no metrics exporter, want one")
	}
}

// TestInitMetricsOnlyPushIntervalTooLarge verifies a metrics-only profile (nil trace
// client) with metrics_push_interval > 300 returns the validation error instead of
// panicking on a nil client.Close().
func TestInitMetricsOnlyPushIntervalTooLarge(t *testing.T) {
	raw := `{"profiles": [
		{"traces_enabled": false, "protocol": "http", "metrics_enabled": true, "metrics_endpoint": "localhost:4318", "metrics_push_interval": 301}
	]}`

	var cfg Config
	if err := sonic.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugin, err := Init(context.Background(), &cfg, testLogger{}, nil, "")
	if err == nil {
		if plugin != nil {
			_ = plugin.Cleanup()
		}
		t.Fatalf("expected error for metrics_push_interval > 300, got nil")
	}
}

// TestInitTracesOnlyProfileNeedsCollectorURL verifies a traces-enabled profile still
// requires collector_url.
func TestInitTracesOnlyProfileNeedsCollectorURL(t *testing.T) {
	raw := `{"profiles": [
		{"trace_type": "genai_extension", "protocol": "grpc"}
	]}`

	var cfg Config
	if err := sonic.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := Init(context.Background(), &cfg, testLogger{}, nil, ""); err == nil {
		t.Errorf("expected error for traces-enabled profile missing collector_url")
	}
}

// TestInitBothDisabledProfile verifies a profile with both traces and metrics disabled
// is allowed as a no-op (matching the telemetry plugin, where pull and push are
// independent and both may be off): it builds a target with no client or exporter.
func TestInitBothDisabledProfile(t *testing.T) {
	raw := `{"profiles": [
		{"traces_enabled": false}
	]}`

	var cfg Config
	if err := sonic.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugin, err := Init(context.Background(), &cfg, testLogger{}, nil, "")
	if err != nil {
		t.Fatalf("Init both-disabled profile: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
	if len(plugin.targets) != 1 {
		t.Fatalf("targets len = %d, want 1", len(plugin.targets))
	}
	if plugin.targets[0].client != nil || plugin.targets[0].metricsExporter != nil {
		t.Errorf("both-disabled target should have no client or exporter")
	}
}

// TestTracesEnabledStorageRoundTrip verifies traces_enabled survives storage marshalling.
func TestTracesEnabledStorageRoundTrip(t *testing.T) {
	raw := `{"profiles": [
		{"traces_enabled": false, "protocol": "http", "metrics_enabled": true, "metrics_endpoint": "localhost:4318"}
	]}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stored, err := cfg.MarshalForStorage()
	if err != nil {
		t.Fatalf("MarshalForStorage: %v", err)
	}
	var back Config
	if err := json.Unmarshal(stored, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if back.Profiles[0].TracesEnabled {
		t.Errorf("round-trip TracesEnabled = true, want false")
	}
}

// TestMergedResolvedHeaders verifies per-signal headers overlay the common ones (same key
// wins), env refs resolve, and the inputs are not mutated.
func TestMergedResolvedHeaders(t *testing.T) {
	t.Setenv("OTEL_TOKEN", "resolved")
	common := map[string]string{"Authorization": "env.OTEL_TOKEN", "X-Shared": "base"}
	overlay := map[string]string{"X-Shared": "override", "X-Table": "my_table"}

	merged, err := mergedResolvedHeaders(common, overlay)
	if err != nil {
		t.Fatalf("mergedResolvedHeaders: %v", err)
	}
	if merged["Authorization"] != "resolved" {
		t.Errorf("Authorization = %q, want resolved", merged["Authorization"])
	}
	if merged["X-Shared"] != "override" {
		t.Errorf("X-Shared = %q, want override (overlay wins)", merged["X-Shared"])
	}
	if merged["X-Table"] != "my_table" {
		t.Errorf("X-Table = %q, want my_table", merged["X-Table"])
	}
	// Inputs untouched.
	if common["Authorization"] != "env.OTEL_TOKEN" || common["X-Shared"] != "base" {
		t.Errorf("common map was mutated: %v", common)
	}
	if overlay["X-Shared"] != "override" {
		t.Errorf("overlay map was mutated: %v", overlay)
	}
}

// TestPerSignalHeadersStorageRoundTrip verifies trace_headers and metrics_headers survive
// storage marshalling and redaction.
func TestPerSignalHeadersStorageRoundTrip(t *testing.T) {
	raw := `{"profiles": [
		{
			"collector_url": "a:4317", "trace_type": "genai_extension", "protocol": "grpc",
			"headers": {"Authorization": "env.OTEL_TOKEN"},
			"trace_headers": {"X-Trace": "t"},
			"metrics_enabled": true, "metrics_endpoint": "a:4318",
			"metrics_headers": {"X-Databricks-Table": "my_table"}
		}
	]}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stored, err := cfg.MarshalForStorage()
	if err != nil {
		t.Fatalf("MarshalForStorage: %v", err)
	}
	var back Config
	if err := json.Unmarshal(stored, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	p := back.Profiles[0]
	if p.TraceHeaders["X-Trace"] != "t" {
		t.Errorf("trace_headers lost: %v", p.TraceHeaders)
	}
	if p.MetricsHeaders["X-Databricks-Table"] != "my_table" {
		t.Errorf("metrics_headers lost: %v", p.MetricsHeaders)
	}

	// Redaction preserves env refs and masks literals across all three maps.
	red := cfg.Redacted().Profiles[0]
	if red.Headers["Authorization"] != "env.OTEL_TOKEN" {
		t.Errorf("common env header not preserved: %q", red.Headers["Authorization"])
	}
	if red.MetricsHeaders["X-Databricks-Table"] == "my_table" {
		t.Errorf("metrics literal header was not masked")
	}
}

// TestInitMetricsOnlyIgnoresTraceHeaderEnv verifies a metrics-only profile does not
// resolve trace_headers, so an unset env reference there does not fail Init.
func TestInitMetricsOnlyIgnoresTraceHeaderEnv(t *testing.T) {
	raw := `{"profiles": [
		{
			"traces_enabled": false, "protocol": "http",
			"trace_headers": {"X-Trace": "env.OTEL_UNSET_TRACE_XYZ"},
			"metrics_enabled": true, "metrics_endpoint": "localhost:4318"
		}
	]}`
	var cfg Config
	if err := sonic.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugin, err := Init(context.Background(), &cfg, testLogger{}, nil, "")
	if err != nil {
		t.Fatalf("Init metrics-only with unset trace_headers env: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
}

// TestInitTracesOnlyIgnoresMetricsHeaderEnv verifies a traces-only profile does not
// resolve metrics_headers, so an unset env reference there does not fail Init.
func TestInitTracesOnlyIgnoresMetricsHeaderEnv(t *testing.T) {
	raw := `{"profiles": [
		{
			"collector_url": "localhost:4317", "trace_type": "genai_extension", "protocol": "grpc",
			"metrics_enabled": false,
			"metrics_headers": {"X-Table": "env.OTEL_UNSET_METRICS_XYZ"}
		}
	]}`
	var cfg Config
	if err := sonic.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugin, err := Init(context.Background(), &cfg, testLogger{}, nil, "")
	if err != nil {
		t.Fatalf("Init traces-only with unset metrics_headers env: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
}

type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// oneMetric is the smallest payload that produces a real OTLP upload.
func oneMetric() *metricdata.ResourceMetrics {
	return &metricdata.ResourceMetrics{
		Resource: resource.Empty(),
		ScopeMetrics: []metricdata.ScopeMetrics{{
			Scope: instrumentation.Scope{Name: "test"},
			Metrics: []metricdata.Metrics{{
				Name: "probe",
				Data: metricdata.Gauge[int64]{
					DataPoints: []metricdata.DataPoint[int64]{{Value: 1}},
				},
			}},
		}},
	}
}

// insecure defaults to true, which forced plaintext and silently dropped every metric
// sent to an https:// endpoint (#7446).
func TestHTTPMetricsExporterHonoursEndpointScheme(t *testing.T) {
	ctx := context.Background()

	newReceiver := func(t *testing.T, tls bool) (string, *atomic.Int32) {
		t.Helper()
		var hits atomic.Int32
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		})
		var srv *httptest.Server
		if tls {
			srv = httptest.NewTLSServer(h)
		} else {
			srv = httptest.NewServer(h)
		}
		t.Cleanup(srv.Close)
		return srv.URL + "/v1/metrics", &hits
	}

	t.Run("https endpoint with insecure default exports over TLS", func(t *testing.T) {
		endpoint, hits := newReceiver(t, true)
		// insecure is the default; for https:// it must mean skip-verify, not plaintext.
		exp, err := createHTTPExporter(ctx, &MetricsConfig{Endpoint: endpoint, Insecure: true})
		if err != nil {
			t.Fatalf("createHTTPExporter: %v", err)
		}
		defer func() { _ = exp.Shutdown(ctx) }()

		if err := exp.Export(ctx, oneMetric()); err != nil {
			t.Fatalf("Export over TLS failed: %v", err)
		}
		if got := hits.Load(); got != 1 {
			t.Errorf("TLS receiver saw %d requests, want 1", got)
		}
	})

	// Positive control: guards against a fix that just disables verification everywhere.
	t.Run("insecure false still verifies the certificate", func(t *testing.T) {
		endpoint, hits := newReceiver(t, true)
		exp, err := createHTTPExporter(ctx, &MetricsConfig{Endpoint: endpoint, Insecure: false})
		if err != nil {
			t.Fatalf("createHTTPExporter: %v", err)
		}
		defer func() { _ = exp.Shutdown(ctx) }()

		// The test server's cert is self-signed, so a verifying client must reject it.
		if err := exp.Export(ctx, oneMetric()); err == nil {
			t.Error("Export succeeded against a self-signed certificate, want a verification failure")
		}
		if got := hits.Load(); got != 0 {
			t.Errorf("receiver saw %d requests, want 0 (handshake must fail)", got)
		}
	})

	// A self-signed collector verifies when its certificate is pinned via tls_ca_cert,
	// which is the secure alternative to leaving insecure at its default.
	t.Run("self-signed collector verifies against a pinned CA", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		caPath := filepath.Join(t.TempDir(), "ca.pem")
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
		if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
			t.Fatalf("write CA: %v", err)
		}

		exp, err := createHTTPExporter(ctx, &MetricsConfig{
			Endpoint:  srv.URL + "/v1/metrics",
			TLSCACert: caPath,
			Insecure:  false,
		})
		if err != nil {
			t.Fatalf("createHTTPExporter: %v", err)
		}
		defer func() { _ = exp.Shutdown(ctx) }()

		if err := exp.Export(ctx, oneMetric()); err != nil {
			t.Errorf("Export against a pinned self-signed CA failed: %v", err)
		}
	})

	// tls_ca_cert outranks insecure: pinning a CA must enforce verification even though
	// insecure defaults to true, otherwise pinning would silently degrade to skip-verify.
	t.Run("pinned CA outranks the insecure default", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		tmpl := &x509.Certificate{
			SerialNumber:          big.NewInt(1),
			Subject:               pkix.Name{CommonName: "unrelated-ca"},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(time.Hour),
			IsCA:                  true,
			KeyUsage:              x509.KeyUsageCertSign,
			BasicConstraintsValid: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatalf("create cert: %v", err)
		}
		caPath := filepath.Join(t.TempDir(), "unrelated.pem")
		if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
			t.Fatalf("write CA: %v", err)
		}

		exp, err := createHTTPExporter(ctx, &MetricsConfig{
			Endpoint:  srv.URL + "/v1/metrics",
			TLSCACert: caPath,
			Insecure:  true,
		})
		if err != nil {
			t.Fatalf("createHTTPExporter: %v", err)
		}
		defer func() { _ = exp.Shutdown(ctx) }()

		if err := exp.Export(ctx, oneMetric()); err == nil {
			t.Error("Export succeeded against a CA that did not sign the server certificate")
		}
	})

	t.Run("plain http endpoint still exports over plaintext", func(t *testing.T) {
		endpoint, hits := newReceiver(t, false)
		exp, err := createHTTPExporter(ctx, &MetricsConfig{Endpoint: endpoint, Insecure: true})
		if err != nil {
			t.Fatalf("createHTTPExporter: %v", err)
		}
		defer func() { _ = exp.Shutdown(ctx) }()

		if err := exp.Export(ctx, oneMetric()); err != nil {
			t.Fatalf("Export over plaintext failed: %v", err)
		}
		if got := hits.Load(); got != 1 {
			t.Errorf("plaintext receiver saw %d requests, want 1", got)
		}
	})
}
