package schemas

import (
	"context"
	"testing"
)

// TestArrayDimsAreNeverMetricSafe keeps arrays out of both metric tiers: a value
// like "team-a,team-b" is one label value per combination.
func TestArrayDimsAreNeverMetricSafe(t *testing.T) {
	for _, d := range EnrichmentDims {
		if d.Multi && d.MetricAllowedHighCardinality() {
			t.Errorf("dimension %q is Multi (array) and permitted as a metric label — arrays must stay record-tier (cardinality explosion)", d.Name)
		}
	}
}

// TestTierIsExplicit guards the silent failure: the zero value is TierRecord, so
// a metric dimension left untiered never appears as a label and nothing errors.
func TestTierIsExplicit(t *testing.T) {
	for _, d := range EnrichmentDims {
		if d.SpanAttr == "" {
			t.Errorf("dimension %q has no SpanAttr; connectors cannot read it", d.Name)
		}
		switch d.Tier {
		case TierRecord, TierMetric, TierHighCardinalityMetric:
		case tierUnset:
			t.Errorf("dimension %q omits Tier; classify it explicitly rather than "+
				"relying on the zero value", d.Name)
		default:
			t.Errorf("dimension %q has an unknown tier %d", d.Name, d.Tier)
		}
	}
}

// TestHighCardinalityTierIsSupersetOfMetric: bounded dimensions are also allowed
// where unbounded ones are, so that list can never be the smaller.
func TestHighCardinalityTierIsSupersetOfMetric(t *testing.T) {
	allowed := map[string]bool{}
	for _, n := range HighCardinalityMetricEnrichmentDimNames() {
		allowed[n] = true
	}
	for _, n := range MetricSafeEnrichmentDimNames() {
		if !allowed[n] {
			t.Errorf("dimension %q is metric-safe but absent from the high-cardinality set", n)
		}
	}
	if len(HighCardinalityMetricEnrichmentDimNames()) < len(MetricSafeEnrichmentDimNames()) {
		t.Error("high-cardinality metric set is smaller than the metric-safe set")
	}
}

// TestEnrichmentDimNamesUnique catches a copy-paste duplicate, which would
// double-emit a label or column.
func TestEnrichmentDimNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range EnrichmentDims {
		if seen[d.Name] {
			t.Errorf("duplicate enrichment dimension name %q", d.Name)
		}
		seen[d.Name] = true
	}
}

// app is derived from the User-Agent, not carried on the context like the other
// dimensions, so the derivation is pinned here.
func TestAppDerivedFromUserAgent(t *testing.T) {
	for _, tc := range []struct{ ua, want string }{
		{"claude-code/1.2.3", "Claude Code"},
		{"Cursor/0.42 (darwin)", "Cursor"},
		{"python-requests/2.31", UserAgentAppOther},
		{"", ""},
	} {
		ctx := context.WithValue(context.Background(), BifrostContextKeyUserAgent, tc.ua)
		e := SpanEnrichmentFromContext(ctx)
		if e.App != tc.want {
			t.Errorf("UA %q: App = %q, want %q", tc.ua, e.App, tc.want)
		}
		span := &Span{Attributes: map[string]any{}}
		e.ApplyToSpan(span)
		got, _ := span.Attributes[AttrBifrostApp].(string)
		if got != tc.want {
			t.Errorf("UA %q: span attr = %q, want %q", tc.ua, got, tc.want)
		}
	}
}
