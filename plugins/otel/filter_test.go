package otel

import (
	"encoding/json"
	"testing"
)

// TestPluginSpanFilterUnmarshal verifies that plugin_span_filter round-trips
// through JSON correctly, including when embedded in a full Config.
func TestPluginSpanFilterUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantMode PluginSpanFilterMode
		wantList []string
	}{
		{
			name:     "exclude mode",
			raw:      `{"mode":"exclude","plugins":["logging","compat"]}`,
			wantMode: PluginSpanFilterModeExclude,
			wantList: []string{"logging", "compat"},
		},
		{
			name:     "include mode",
			raw:      `{"mode":"include","plugins":["guardrails"]}`,
			wantMode: PluginSpanFilterModeInclude,
			wantList: []string{"guardrails"},
		},
		{
			name:     "empty plugins list",
			raw:      `{"mode":"exclude","plugins":[]}`,
			wantMode: PluginSpanFilterModeExclude,
			wantList: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f PluginSpanFilter
			if err := json.Unmarshal([]byte(tt.raw), &f); err != nil {
				t.Fatalf("unexpected unmarshal error: %v", err)
			}
			if f.Mode != tt.wantMode {
				t.Errorf("Mode = %q, want %q", f.Mode, tt.wantMode)
			}
			if len(f.Plugins) != len(tt.wantList) {
				t.Errorf("Plugins length = %d, want %d", len(f.Plugins), len(tt.wantList))
				return
			}
			for i, p := range tt.wantList {
				if f.Plugins[i] != p {
					t.Errorf("Plugins[%d] = %q, want %q", i, f.Plugins[i], p)
				}
			}
		})
	}
}

// TestConfigPluginSpanFilterField verifies that plugin_span_filter is correctly
// parsed when present inside a full Config JSON blob.
func TestConfigPluginSpanFilterField(t *testing.T) {
	raw := `{
		"collector_url": "localhost:4317",
		"trace_type": "genai_extension",
		"protocol": "grpc",
		"plugin_span_filter": {
			"mode": "exclude",
			"plugins": ["logging", "telemetry"]
		}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if cfg.PluginSpanFilter == nil {
		t.Fatal("expected PluginSpanFilter to be set")
	}
	if cfg.PluginSpanFilter.Mode != PluginSpanFilterModeExclude {
		t.Errorf("Mode = %q, want %q", cfg.PluginSpanFilter.Mode, PluginSpanFilterModeExclude)
	}
	if len(cfg.PluginSpanFilter.Plugins) != 2 {
		t.Errorf("Plugins length = %d, want 2", len(cfg.PluginSpanFilter.Plugins))
	}
}

// TestConfigPluginSpanFilterAbsent verifies that omitting plugin_span_filter
// leaves Config.PluginSpanFilter as nil (no default applied).
func TestConfigPluginSpanFilterAbsent(t *testing.T) {
	raw := `{"collector_url":"localhost:4317","trace_type":"genai_extension","protocol":"grpc"}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if cfg.PluginSpanFilter != nil {
		t.Errorf("expected PluginSpanFilter to be nil when absent, got %+v", cfg.PluginSpanFilter)
	}
}
