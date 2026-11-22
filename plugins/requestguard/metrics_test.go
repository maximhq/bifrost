package requestguard

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsRegistrationRollbackPreservesExistingCollectors(t *testing.T) {
	registry := prometheus.NewRegistry()
	preexisting := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "bifrost", Subsystem: "requestguard",
		Name: "capability_failures_total", Help: "Total request capability failures observed by request guard.",
	}, []string{"capability", "provider", "model"})
	registry.MustRegister(preexisting)

	if _, err := NewMetrics(MetricsConfig{Enabled: true, Registerer: registry}); err == nil {
		t.Fatal("NewMetrics() error = nil, want collector type conflict")
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("registry.Gather() error = %v", err)
	}
	for _, family := range families {
		if family.GetName() == "bifrost_requestguard_requests_blocked_total" {
			t.Fatal("newly registered collector remained after failed registration")
		}
	}
	if err := registry.Unregister(preexisting); !err {
		t.Fatal("preexisting collector was removed during rollback")
	}
}

func TestMetricsBoundUnknownModelLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(MetricsConfig{Enabled: true, Registerer: registry})
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}
	metrics.catalog = &fakeCatalog{capabilitiesByRoute: map[string]*schemas.ModelCapabilities{
		routeKey(schemas.OpenAI, "known-model"): {},
	}}
	metrics.RecordRequestBlocked("policy", schemas.OpenAI, "unrecognized-user-model")
	if got := testutil.ToFloat64(metrics.requestsBlocked.WithLabelValues("policy", "openai", "unknown")); got != 1 {
		t.Fatalf("bounded unknown model counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.requestsBlocked.WithLabelValues("policy", "openai", "unrecognized-user-model")); got != 0 {
		t.Fatalf("raw model label counter = %v, want no series", got)
	}
}
