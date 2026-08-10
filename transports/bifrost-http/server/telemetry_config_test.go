package server

import (
	"reflect"
	"testing"
)

// Regression for #5881: plugins.telemetry.config.custom_labels was accepted
// by the schema and persisted, but the config merge only copied
// PushGateway/MetricsEnabled, so the labels never reached the running plugin.
func TestBuildTelemetryConfigMergesCustomLabels(t *testing.T) {
	tests := []struct {
		name         string
		clientLabels []string
		pluginConfig any
		want         []string
	}{
		{
			name:         "plugin config labels reach the plugin",
			clientLabels: nil,
			pluginConfig: map[string]any{"custom_labels": []any{"team", "env"}},
			want:         []string{"team", "env"},
		},
		{
			name:         "unioned with client labels, client first",
			clientLabels: []string{"cluster"},
			pluginConfig: map[string]any{"custom_labels": []any{"team"}},
			want:         []string{"cluster", "team"},
		},
		{
			name:         "duplicates across sources skipped",
			clientLabels: []string{"team", "cluster"},
			pluginConfig: map[string]any{"custom_labels": []any{"team", "env"}},
			want:         []string{"team", "cluster", "env"},
		},
		{
			name:         "no plugin config keeps client labels",
			clientLabels: []string{"cluster"},
			pluginConfig: nil,
			want:         []string{"cluster"},
		},
		{
			name:         "plugin config without labels keeps client labels",
			clientLabels: []string{"cluster"},
			pluginConfig: map[string]any{"metrics_enabled": true},
			want:         []string{"cluster"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := buildTelemetryConfig(tt.clientLabels, tt.pluginConfig)
			if err != nil {
				t.Fatalf("buildTelemetryConfig: %v", err)
			}
			if !reflect.DeepEqual(cfg.CustomLabels, tt.want) {
				t.Errorf("CustomLabels = %v, want %v", cfg.CustomLabels, tt.want)
			}
		})
	}
}

// PushGateway and MetricsEnabled merging must survive the refactor unchanged.
func TestBuildTelemetryConfigPreservesExistingMerge(t *testing.T) {
	enabled := true
	cfg, err := buildTelemetryConfig(nil, map[string]any{
		"metrics_enabled": enabled,
		"push_gateway":    map[string]any{"enabled": true, "push_gateway_url": "http://pgw:9091"},
	})
	if err != nil {
		t.Fatalf("buildTelemetryConfig: %v", err)
	}
	if cfg.MetricsEnabled == nil || !*cfg.MetricsEnabled {
		t.Errorf("MetricsEnabled not merged: %+v", cfg.MetricsEnabled)
	}
	if cfg.PushGateway == nil || !cfg.PushGateway.Enabled {
		t.Errorf("PushGateway not merged: %+v", cfg.PushGateway)
	}
}
