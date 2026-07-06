package otel

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
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
