package telemetry

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"
)

// TestPrometheusLabelsMatchEnrichmentRegistry keeps the Prometheus bifrost label
// set in parity with the canonical enrichment registry (core/schemas): every
// metric-safe dimension must be a label, and no record-tier (high cardinality)
// dimension may be. Known divergences are enumerated in the allowlist below; the
// test fails on any new drift and when a listed entry no longer applies.
func TestPrometheusLabelsMatchEnrichmentRegistry(t *testing.T) {
	labels := map[string]bool{}
	for _, l := range defaultBifrostLabelNames {
		labels[l] = true
	}

	metricSafe := map[string]bool{}
	for _, n := range schemas.MetricSafeEnrichmentDimNames() {
		metricSafe[n] = true
	}
	allDims := map[string]bool{}
	for _, n := range schemas.EnrichmentDimNames() {
		allDims[n] = true
	}

	// Metric-safe dims the telemetry plugin does not expose as labels. Empty: the
	// Prometheus label set covers the full metric-safe registry.
	knownMissing := map[string]string{}

	// 1) Every metric-safe dim must be a label, unless a known gap.
	for n := range metricSafe {
		if !labels[n] && knownMissing[n] == "" {
			t.Errorf("Prometheus labels are missing registry metric-safe dimension %q", n)
		}
	}
	// 2) Every enrichment-dim label must be metric-safe (no high-cardinality labels).
	for l := range labels {
		if !allDims[l] {
			continue // non-enrichment label (e.g. status_code) — out of scope
		}
		if !metricSafe[l] {
			t.Errorf("Prometheus exposes record-tier dimension %q as a label (cardinality risk)", l)
		}
	}
	// 3) Keep the allowlist honest: a gap that has closed must be removed.
	for n := range knownMissing {
		if labels[n] {
			t.Errorf("dimension %q is now a label — remove it from knownMissing", n)
		}
	}
}

// TestUserLabelsAreOptIn keeps user labels out of the default set and pins them
// to real registry dimensions.
func TestUserLabelsAreOptIn(t *testing.T) {
	dims := map[string]bool{}
	for _, n := range schemas.EnrichmentDimNames() {
		dims[n] = true
	}
	for _, name := range userLabelNames {
		if containsLabel(defaultBifrostLabelNames, name) {
			t.Errorf("%q is in the default label set; it must stay behind user_labels_enabled", name)
		}
		if !dims[name] {
			t.Errorf("%q is not an enrichment dimension", name)
		}
	}
}

// TestUserLabelsEnabledMatchesLabelSet guards a silent outage: a value map
// disagreeing with the registered labels makes Prometheus reject everything.
func TestUserLabelsEnabledMatchesLabelSet(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		plugin, err := Init(&Config{
			Registry:          prometheus.NewRegistry(),
			UserLabelsEnabled: &enabled,
		}, nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
		if err != nil {
			t.Fatalf("Init(user_labels_enabled=%v): %v", enabled, err)
		}
		if got := plugin.userLabelsEnabled.Load(); got != enabled {
			t.Errorf("userLabelsEnabled = %v, want %v", got, enabled)
		}
		plugin.Cleanup()
	}
}
