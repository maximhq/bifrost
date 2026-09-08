package bifrost

import (
	"context"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// applyContextSpanAttributes writes the governance / identity enrichment
// attributes carried on ctx onto span. It is the single live emitter of the
// context-sourced dimensions in schemas.EnrichmentDims (virtual key, selected
// key, routing rule, team, customer, business unit, user, fallback index), so
// TestContextSpanAttributesCoverRegistry pins this set to that registry: a
// dimension cannot be added to the registry (and read by the curated connectors)
// without being emitted here, and a renamed key surfaces as a mismatch.
//
// span.SetAttribute is nil-safe, so a nil span (e.g. NoOpTracer) makes this a
// no-op with no branch at the call site.
func applyContextSpanAttributes(span *schemas.Span, ctx context.Context) {
	if selectedKeyID, ok := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string); ok && selectedKeyID != "" {
		span.SetAttribute(schemas.AttrBifrostSelectedKeyID, selectedKeyID)
	}
	if selectedKeyName, ok := ctx.Value(schemas.BifrostContextKeySelectedKeyName).(string); ok && selectedKeyName != "" {
		span.SetAttribute(schemas.AttrBifrostSelectedKeyName, selectedKeyName)
	}
	if virtualKeyID, ok := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string); ok && virtualKeyID != "" {
		span.SetAttribute(schemas.AttrBifrostVirtualKeyID, virtualKeyID)
	}
	if virtualKeyName, ok := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyName).(string); ok && virtualKeyName != "" {
		span.SetAttribute(schemas.AttrBifrostVirtualKeyName, virtualKeyName)
	}
	if routingRuleID, ok := ctx.Value(schemas.BifrostContextKeyGovernanceRoutingRuleID).(string); ok && routingRuleID != "" {
		span.SetAttribute(schemas.AttrBifrostRoutingRuleID, routingRuleID)
	}
	if routingRuleName, ok := ctx.Value(schemas.BifrostContextKeyGovernanceRoutingRuleName).(string); ok && routingRuleName != "" {
		span.SetAttribute(schemas.AttrBifrostRoutingRuleName, routingRuleName)
	}
	if teamID, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamID).(string); ok && teamID != "" {
		span.SetAttribute(schemas.AttrBifrostTeamID, teamID)
	}
	if teamName, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamName).(string); ok && teamName != "" {
		span.SetAttribute(schemas.AttrBifrostTeamName, teamName)
	}
	if customerID, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerID).(string); ok && customerID != "" {
		span.SetAttribute(schemas.AttrBifrostCustomerID, customerID)
	}
	if customerName, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerName).(string); ok && customerName != "" {
		span.SetAttribute(schemas.AttrBifrostCustomerName, customerName)
	}
	if businessUnitID, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBusinessUnitID).(string); ok && businessUnitID != "" {
		span.SetAttribute(schemas.AttrBifrostBusinessUnitID, businessUnitID)
	}
	if businessUnitName, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBusinessUnitName).(string); ok && businessUnitName != "" {
		span.SetAttribute(schemas.AttrBifrostBusinessUnitName, businessUnitName)
	}
	if projectId, ok := ctx.Value(schemas.BifrostContextKeyGovernanceProjectID).(string); ok && projectId != "" {
		span.SetAttribute(schemas.AttrBifrostProjectID, projectId)
	}
	if projectName, ok := ctx.Value(schemas.BifrostContextKeyGovernanceProjectName).(string); ok && projectName != "" {
		span.SetAttribute(schemas.AttrBifrostProjectName, projectName)
	}
	if teamIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamIDs).([]string); ok && len(teamIDs) > 0 {
		span.SetAttribute(schemas.AttrBifrostTeamIDs, teamIDs)
	}
	if teamNames, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamNames).([]string); ok && len(teamNames) > 0 {
		span.SetAttribute(schemas.AttrBifrostTeamNames, teamNames)
	}
	if customerIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerIDs).([]string); ok && len(customerIDs) > 0 {
		span.SetAttribute(schemas.AttrBifrostCustomerIDs, customerIDs)
	}
	if customerNames, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerNames).([]string); ok && len(customerNames) > 0 {
		span.SetAttribute(schemas.AttrBifrostCustomerNames, customerNames)
	}
	if businessUnitIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBusinessUnitIDs).([]string); ok && len(businessUnitIDs) > 0 {
		span.SetAttribute(schemas.AttrBifrostBusinessUnitIDs, businessUnitIDs)
	}
	if businessUnitNames, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBusinessUnitNames).([]string); ok && len(businessUnitNames) > 0 {
		span.SetAttribute(schemas.AttrBifrostBusinessUnitNames, businessUnitNames)
	}
	if userID, ok := ctx.Value(schemas.BifrostContextKeyUserID).(string); ok && userID != "" {
		span.SetAttribute(schemas.AttrBifrostUserID, userID)
	}
	if userName, ok := ctx.Value(schemas.BifrostContextKeyUserName).(string); ok && userName != "" {
		span.SetAttribute(schemas.AttrBifrostUserName, userName)
	}
	if userEmail, ok := ctx.Value(schemas.BifrostContextKeyUserEmail).(string); ok && userEmail != "" {
		span.SetAttribute(schemas.AttrBifrostUserEmail, userEmail)
	}
	if fallbackIndex, ok := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int); ok {
		span.SetAttribute(schemas.AttrBifrostFallbackIndex, fallbackIndex)
	}
}
