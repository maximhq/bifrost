package bifrost

import (
	"context"
	"reflect"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// contextDimSources lists every enrichment dimension that applyContextSpanAttributes
// is responsible for emitting, paired with the context key it reads from and a
// distinct sentinel value. This is the emit-side counterpart to the connectors'
// read-side parity tests: it pins the live span emission to schemas.EnrichmentDims
// so a dimension cannot be added to the registry (and read by BigQuery/Datadog/
// Splunk) without also being emitted here.
var contextDimSources = []struct {
	spanAttr string
	ctxKey   schemas.BifrostContextKey
	value    any
}{
	{schemas.AttrBifrostSelectedKeyID, schemas.BifrostContextKeySelectedKeyID, "sel-key-id"},
	{schemas.AttrBifrostSelectedKeyName, schemas.BifrostContextKeySelectedKeyName, "sel-key-name"},
	{schemas.AttrBifrostVirtualKeyID, schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-id"},
	{schemas.AttrBifrostVirtualKeyName, schemas.BifrostContextKeyGovernanceVirtualKeyName, "vk-name"},
	{schemas.AttrBifrostRoutingRuleID, schemas.BifrostContextKeyGovernanceRoutingRuleID, "rr-id"},
	{schemas.AttrBifrostRoutingRuleName, schemas.BifrostContextKeyGovernanceRoutingRuleName, "rr-name"},
	{schemas.AttrBifrostTeamID, schemas.BifrostContextKeyGovernanceTeamID, "team-id"},
	{schemas.AttrBifrostTeamName, schemas.BifrostContextKeyGovernanceTeamName, "team-name"},
	{schemas.AttrBifrostCustomerID, schemas.BifrostContextKeyGovernanceCustomerID, "cust-id"},
	{schemas.AttrBifrostCustomerName, schemas.BifrostContextKeyGovernanceCustomerName, "cust-name"},
	{schemas.AttrBifrostBusinessUnitID, schemas.BifrostContextKeyGovernanceBusinessUnitID, "bu-id"},
	{schemas.AttrBifrostBusinessUnitName, schemas.BifrostContextKeyGovernanceBusinessUnitName, "bu-name"},
	{schemas.AttrBifrostTeamIDs, schemas.BifrostContextKeyGovernanceTeamIDs, []string{"team-id-1", "team-id-2"}},
	{schemas.AttrBifrostTeamNames, schemas.BifrostContextKeyGovernanceTeamNames, []string{"team-name-1"}},
	{schemas.AttrBifrostCustomerIDs, schemas.BifrostContextKeyGovernanceCustomerIDs, []string{"cust-id-1"}},
	{schemas.AttrBifrostCustomerNames, schemas.BifrostContextKeyGovernanceCustomerNames, []string{"cust-name-1"}},
	{schemas.AttrBifrostBusinessUnitIDs, schemas.BifrostContextKeyGovernanceBusinessUnitIDs, []string{"bu-id-1"}},
	{schemas.AttrBifrostBusinessUnitNames, schemas.BifrostContextKeyGovernanceBusinessUnitNames, []string{"bu-name-1"}},
	{schemas.AttrBifrostUserID, schemas.BifrostContextKeyUserID, "user-id"},
	{schemas.AttrBifrostUserName, schemas.BifrostContextKeyUserName, "user-name"},
	{schemas.AttrBifrostUserEmail, schemas.BifrostContextKeyUserEmail, "user@example.com"},
	{schemas.AttrBifrostFallbackIndex, schemas.BifrostContextKeyFallbackIndex, 2},
}

// dimsEmittedElsewhere are registry dimensions NOT emitted by
// applyContextSpanAttributes: request-sourced ones written at span creation, and
// framework-field-sourced ones written in framework/tracing from ExtractedFields.
// Listed so the completeness check can account for every registry dimension; the
// value records where each is actually emitted.
var dimsEmittedElsewhere = map[string]string{
	schemas.AttrBifrostProviderName:      "request-sourced: span creation in bifrost.go",
	schemas.AttrRequestModel:             "request-sourced: span creation in bifrost.go",
	schemas.AttrLegacyRequestType:        "request-sourced: span creation in bifrost.go",
	schemas.AttrBifrostAlias:             "framework ExtractedFields: framework/tracing/tracer.go",
	schemas.AttrBifrostRoutingEngineUsed: "framework ExtractedFields: framework/tracing/tracer.go",
}

// TestContextSpanAttributesEmit drives applyContextSpanAttributes with every
// context key populated and asserts each dimension lands on the span under its
// canonical SpanAttr with the exact value. A dead emit (a dim dropped from the
// helper) fails as a missing attribute; a mis-wired read (wrong context key)
// fails as a wrong/missing value, since every sentinel is distinct.
func TestContextSpanAttributesEmit(t *testing.T) {
	ctx := context.Background()
	for _, d := range contextDimSources {
		ctx = context.WithValue(ctx, d.ctxKey, d.value)
	}

	span := &schemas.Span{}
	applyContextSpanAttributes(span, ctx)

	for _, d := range contextDimSources {
		got, ok := span.Attributes[d.spanAttr]
		if !ok {
			t.Errorf("span attribute %q was not emitted (context key %q)", d.spanAttr, d.ctxKey)
			continue
		}
		if !reflect.DeepEqual(got, d.value) {
			t.Errorf("span attribute %q = %v (%T), want %v (%T)", d.spanAttr, got, got, d.value, d.value)
		}
	}
}

// TestEnrichmentRegistryDimsAllEmitted is the drift guard: every dimension in the
// canonical registry must be accounted for as emitted somewhere — either by
// applyContextSpanAttributes (contextDimSources) or in dimsEmittedElsewhere.
// Adding a dimension to schemas.EnrichmentDims (and thus to the connector
// projections) without wiring its emission fails here. It also catches a stale
// classification: a context source or "elsewhere" entry for a key no longer in
// the registry.
func TestEnrichmentRegistryDimsAllEmitted(t *testing.T) {
	inContext := make(map[string]bool, len(contextDimSources))
	for _, d := range contextDimSources {
		inContext[d.spanAttr] = true
	}
	registry := make(map[string]bool)
	for _, d := range schemas.EnrichmentDims {
		registry[d.SpanAttr] = true
		if !inContext[d.SpanAttr] {
			if _, ok := dimsEmittedElsewhere[d.SpanAttr]; !ok {
				t.Errorf("enrichment dim %q (%s) is not emitted: add it to applyContextSpanAttributes + contextDimSources, or document it in dimsEmittedElsewhere", d.Name, d.SpanAttr)
			}
		}
	}
	// Reverse: no stale classification entries for keys no longer in the registry.
	for _, d := range contextDimSources {
		if !registry[d.spanAttr] {
			t.Errorf("contextDimSources references %q which is no longer in EnrichmentDims", d.spanAttr)
		}
	}
	for attr := range dimsEmittedElsewhere {
		if !registry[attr] {
			t.Errorf("dimsEmittedElsewhere references %q which is no longer in EnrichmentDims", attr)
		}
	}
}
