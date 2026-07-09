package schemas

import "testing"

// TestArrayDimsAreNeverMetricSafe is the structural guard that keeps array
// (Multi) dimensions out of the metric tier — i.e. out of Prometheus labels and
// Datadog metric tags. An array value like "team-a,team-b,team-c" would become a
// distinct label/tag value per team combination and explode series cardinality,
// so a Multi dimension must never be MetricSafe. The curated connectors derive
// their metric-tier lists from MetricSafeEnrichmentDims(), so this invariant is
// what actually prevents arrays from ever being shared as Prometheus labels.
func TestArrayDimsAreNeverMetricSafe(t *testing.T) {
	for _, d := range EnrichmentDims {
		if d.Multi && d.MetricSafe {
			t.Errorf("dimension %q is Multi (array) AND MetricSafe — arrays must never be metric labels/tags (cardinality explosion)", d.Name)
		}
	}
}

// TestEnrichmentDimNamesUnique guards against a copy-paste duplicate slipping into
// the registry, which would double-emit a label/column.
func TestEnrichmentDimNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range EnrichmentDims {
		if seen[d.Name] {
			t.Errorf("duplicate enrichment dimension name %q", d.Name)
		}
		seen[d.Name] = true
	}
}
