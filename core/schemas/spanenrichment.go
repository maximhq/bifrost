package schemas

import "context"

// SpanEnrichmentFromContext reads the governance and identity dimensions a
// request was granted under. Returns nil when the context carries none.
//
// This is the single definition of the context-to-dimension mapping; both the
// typed record on the span and the span's attribute keys derive from it, so they
// cannot disagree. TestContextSpanAttributesCoverRegistry pins the emitted set to
// EnrichmentDims.
func SpanEnrichmentFromContext(ctx context.Context) *SpanEnrichment {
	if ctx == nil {
		return nil
	}
	e := &SpanEnrichment{}
	str := func(key BifrostContextKey) string {
		v, _ := ctx.Value(key).(string)
		return v
	}
	strs := func(key BifrostContextKey) []string {
		v, _ := ctx.Value(key).([]string)
		return v
	}

	e.VirtualKeyID = str(BifrostContextKeyGovernanceVirtualKeyID)
	e.VirtualKeyName = str(BifrostContextKeyGovernanceVirtualKeyName)
	e.SelectedKeyID = str(BifrostContextKeySelectedKeyID)
	e.SelectedKeyName = str(BifrostContextKeySelectedKeyName)
	e.RoutingRuleID = str(BifrostContextKeyGovernanceRoutingRuleID)
	e.RoutingRuleName = str(BifrostContextKeyGovernanceRoutingRuleName)
	e.TeamID = str(BifrostContextKeyGovernanceTeamID)
	e.TeamName = str(BifrostContextKeyGovernanceTeamName)
	e.CustomerID = str(BifrostContextKeyGovernanceCustomerID)
	e.CustomerName = str(BifrostContextKeyGovernanceCustomerName)
	e.BusinessUnitID = str(BifrostContextKeyGovernanceBusinessUnitID)
	e.BusinessUnitName = str(BifrostContextKeyGovernanceBusinessUnitName)
	e.ProjectID = str(BifrostContextKeyGovernanceProjectID)
	e.ProjectName = str(BifrostContextKeyGovernanceProjectName)
	e.UserID = str(BifrostContextKeyUserID)
	e.UserName = str(BifrostContextKeyUserName)
	e.UserEmail = str(BifrostContextKeyUserEmail)

	e.TeamIDs = strs(BifrostContextKeyGovernanceTeamIDs)
	e.TeamNames = strs(BifrostContextKeyGovernanceTeamNames)
	e.CustomerIDs = strs(BifrostContextKeyGovernanceCustomerIDs)
	e.CustomerNames = strs(BifrostContextKeyGovernanceCustomerNames)
	e.BusinessUnitIDs = strs(BifrostContextKeyGovernanceBusinessUnitIDs)
	e.BusinessUnitNames = strs(BifrostContextKeyGovernanceBusinessUnitNames)

	if fallbackIndex, ok := ctx.Value(BifrostContextKeyFallbackIndex).(int); ok {
		e.FallbackIndex = &fallbackIndex
	}
	return e
}

// ApplyToSpan attaches the dimensions to the span and writes their attribute
// keys. span.SetAttribute is nil-safe, so a nil span is a no-op.
func (e *SpanEnrichment) ApplyToSpan(span *Span) {
	if e == nil || span == nil {
		return
	}
	span.Enrichment = e

	// The virtual-key and selected-key names are emitted only alongside their
	// IDs, matching the paired writes this replaces.
	if e.VirtualKeyID != "" {
		span.SetAttribute(AttrBifrostVirtualKeyID, e.VirtualKeyID)
		span.SetAttribute(AttrBifrostVirtualKeyName, e.VirtualKeyName)
	}
	if e.SelectedKeyID != "" {
		span.SetAttribute(AttrBifrostSelectedKeyID, e.SelectedKeyID)
		span.SetAttribute(AttrBifrostSelectedKeyName, e.SelectedKeyName)
	}
	if e.RoutingRuleID != "" {
		span.SetAttribute(AttrBifrostRoutingRuleID, e.RoutingRuleID)
		span.SetAttribute(AttrBifrostRoutingRuleName, e.RoutingRuleName)
	}
	setSpanStr(span, AttrBifrostTeamID, e.TeamID)
	setSpanStr(span, AttrBifrostTeamName, e.TeamName)
	setSpanStr(span, AttrBifrostCustomerID, e.CustomerID)
	setSpanStr(span, AttrBifrostCustomerName, e.CustomerName)
	setSpanStr(span, AttrBifrostBusinessUnitID, e.BusinessUnitID)
	setSpanStr(span, AttrBifrostBusinessUnitName, e.BusinessUnitName)
	setSpanStr(span, AttrBifrostProjectID, e.ProjectID)
	setSpanStr(span, AttrBifrostProjectName, e.ProjectName)
	setSpanStr(span, AttrBifrostUserID, e.UserID)
	setSpanStr(span, AttrBifrostUserName, e.UserName)
	setSpanStr(span, AttrBifrostUserEmail, e.UserEmail)

	setSpanStrs(span, AttrBifrostTeamIDs, e.TeamIDs)
	setSpanStrs(span, AttrBifrostTeamNames, e.TeamNames)
	setSpanStrs(span, AttrBifrostCustomerIDs, e.CustomerIDs)
	setSpanStrs(span, AttrBifrostCustomerNames, e.CustomerNames)
	setSpanStrs(span, AttrBifrostBusinessUnitIDs, e.BusinessUnitIDs)
	setSpanStrs(span, AttrBifrostBusinessUnitNames, e.BusinessUnitNames)

	if e.FallbackIndex != nil {
		span.SetAttribute(AttrBifrostFallbackIndex, *e.FallbackIndex)
	}
}

// EnsureEnrichment returns the span's dimension set, creating it if the request
// context carried none.
func (s *Span) EnsureEnrichment() *SpanEnrichment {
	if s == nil {
		return nil
	}
	if s.Enrichment == nil {
		s.Enrichment = &SpanEnrichment{}
	}
	return s.Enrichment
}

// SetRetries records the attempt count on both the span attribute and the typed
// dimensions, creating the dimension set if the context carried none.
func (s *Span) SetRetries(attempts int) {
	if s == nil {
		return
	}
	if s.Enrichment == nil {
		s.Enrichment = &SpanEnrichment{}
	}
	s.Enrichment.Retries = attempts
	s.SetAttribute(AttrBifrostRetries, attempts)
}

func setSpanStr(span *Span, key, value string) {
	if value != "" {
		span.SetAttribute(key, value)
	}
}

func setSpanStrs(span *Span, key string, value []string) {
	if len(value) > 0 {
		span.SetAttribute(key, value)
	}
}
