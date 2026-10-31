package logstore

import (
	"fmt"
	"slices"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Guardrail outcome values. SearchFilters.GuardrailOutcomes accepts blocked
// and detected; ObjectStorageRouteMatch accepts all four.
const (
	// GuardrailOutcomeBlocked is a recorded intervention, or a
	// guardrail_intervention error logged before interventions were recorded.
	GuardrailOutcomeBlocked = "blocked"
	// GuardrailOutcomeDetected is any recorded detection, enforced or not.
	GuardrailOutcomeDetected = "detected"
	// GuardrailOutcomeRedacted is a detection whose action redacted content.
	GuardrailOutcomeRedacted = "redacted"
	// GuardrailOutcomeTransformed is a detection whose action transformed content.
	GuardrailOutcomeTransformed = "transformed"
)

// guardrailInterventionErrorType is the error type a guardrail returns when it
// blocks a request. Matching it keeps blocks logged before interventions were
// recorded in guardrail_debug visible to the outcome filter and to routes.
const guardrailInterventionErrorType = "guardrail_intervention"

// routeGuardrailOutcomes lists the outcomes a route match may name.
var routeGuardrailOutcomes = []string{
	GuardrailOutcomeBlocked,
	GuardrailOutcomeDetected,
	GuardrailOutcomeRedacted,
	GuardrailOutcomeTransformed,
}

// ObjectStorageRouteMatch selects the logs an object storage route receives.
// Fields are AND-ed and the values within a field are OR-ed; an empty field
// places no constraint. The guardrail fields are evaluated together against
// each recorded guardrail entry, so outcome "blocked" with phase "output"
// needs one output-phase intervention, not any intervention plus any output
// entry. Phases, such as input or output, are matched exactly as recorded.
// RequestTypes takes the same values as object_storage_exclude_request_types,
// passthrough types included.
type ObjectStorageRouteMatch struct {
	GuardrailOutcomes  []string `json:"guardrail_outcomes,omitempty"`  // blocked, detected, redacted, transformed
	GuardrailPhases    []string `json:"guardrail_phases,omitempty"`    // phase an entry was recorded in
	GuardrailRules     []string `json:"guardrail_rules,omitempty"`     // rule_name an entry was recorded under
	GuardrailProviders []string `json:"guardrail_providers,omitempty"` // guardrail_provider an entry was recorded under
	ErrorTypes         []string `json:"error_types,omitempty"`         // error_details type, or its error.type
	StatusCodes        []int    `json:"status_codes,omitempty"`        // error_details status_code
	RequestTypes       []string `json:"request_types,omitempty"`       // log object type
}

// IsEmpty reports whether the match places no constraint at all. An empty
// match is rejected by Validate rather than treated as "every log", so a
// route never mirrors all traffic by accident.
func (m ObjectStorageRouteMatch) IsEmpty() bool {
	return len(m.GuardrailOutcomes) == 0 && len(m.GuardrailPhases) == 0 &&
		len(m.GuardrailRules) == 0 && len(m.GuardrailProviders) == 0 &&
		len(m.ErrorTypes) == 0 && len(m.StatusCodes) == 0 && len(m.RequestTypes) == 0
}

// Validate rejects an empty match, an unknown guardrail outcome and an
// impossible status code, so a typo fails at startup instead of silently
// matching nothing.
func (m ObjectStorageRouteMatch) Validate() error {
	if m.IsEmpty() {
		return fmt.Errorf("match must set at least one field")
	}
	for _, outcome := range m.GuardrailOutcomes {
		if !slices.Contains(routeGuardrailOutcomes, outcome) {
			return fmt.Errorf("unknown guardrail outcome %q (want one of %v)", outcome, routeGuardrailOutcomes)
		}
	}
	for _, code := range m.StatusCodes {
		if code < 100 || code > 599 {
			return fmt.Errorf("invalid status code %d", code)
		}
	}
	return nil
}

// Matches reports whether l satisfies every set field of m. It reads the
// parsed error and guardrail fields, falling back to their serialized
// columns, so it works on entries before and after SerializeFields.
func (m ObjectStorageRouteMatch) Matches(l *Log) bool {
	if l == nil || m.IsEmpty() {
		return false
	}
	if len(m.RequestTypes) > 0 && !slices.Contains(m.RequestTypes, l.Object) && !slices.Contains(m.RequestTypes, l.PassthroughObjectType()) {
		return false
	}
	errDetails := routeErrorDetails(l)
	if len(m.ErrorTypes) > 0 && !matchesErrorType(m.ErrorTypes, errDetails) {
		return false
	}
	if len(m.StatusCodes) > 0 && (errDetails == nil || errDetails.StatusCode == nil || !slices.Contains(m.StatusCodes, *errDetails.StatusCode)) {
		return false
	}
	if m.hasGuardrailConstraint() && !m.matchesGuardrail(routeGuardrailDebug(l), errDetails) {
		return false
	}
	return true
}

// hasGuardrailConstraint reports whether any guardrail field is set.
func (m ObjectStorageRouteMatch) hasGuardrailConstraint() bool {
	return len(m.GuardrailOutcomes) > 0 || len(m.GuardrailPhases) > 0 ||
		len(m.GuardrailRules) > 0 || len(m.GuardrailProviders) > 0
}

// matchesGuardrail reports whether one recorded guardrail entry satisfies
// every guardrail field. A legacy block, known only by its error type, has no
// phase, rule or provider, so it counts only when none of those are set. A
// monitor-only detection (enforced false) records what its action would have
// done without changing the content, so it counts as detected but never as
// redacted or transformed.
func (m ObjectStorageRouteMatch) matchesGuardrail(debug *schemas.BifrostGuardrailMetadata, errDetails *schemas.BifrostError) bool {
	wantOutcome := func(outcome string) bool {
		return len(m.GuardrailOutcomes) == 0 || slices.Contains(m.GuardrailOutcomes, outcome)
	}
	if debug != nil {
		for _, iv := range debug.Interventions {
			if wantOutcome(GuardrailOutcomeBlocked) && m.matchesEntry(iv.Phase, iv.RuleName, iv.GuardrailProvider) {
				return true
			}
		}
		for _, d := range debug.Detections {
			if !m.matchesEntry(d.Phase, d.RuleName, d.GuardrailProvider) {
				continue
			}
			acted := d.Enforced == nil || *d.Enforced
			if wantOutcome(GuardrailOutcomeDetected) ||
				(acted && d.Action == schemas.GuardrailDetectionActionRedact && wantOutcome(GuardrailOutcomeRedacted)) ||
				(acted && d.Action == schemas.GuardrailDetectionActionTransform && wantOutcome(GuardrailOutcomeTransformed)) {
				return true
			}
		}
	}
	if len(m.GuardrailPhases) == 0 && len(m.GuardrailRules) == 0 && len(m.GuardrailProviders) == 0 &&
		wantOutcome(GuardrailOutcomeBlocked) && matchesErrorType([]string{guardrailInterventionErrorType}, errDetails) {
		return true
	}
	return false
}

// matchesEntry reports whether a guardrail entry's phase, rule and provider
// pass the phase, rule and provider fields.
func (m ObjectStorageRouteMatch) matchesEntry(phase, rule, provider string) bool {
	return (len(m.GuardrailPhases) == 0 || slices.Contains(m.GuardrailPhases, phase)) &&
		(len(m.GuardrailRules) == 0 || slices.Contains(m.GuardrailRules, rule)) &&
		(len(m.GuardrailProviders) == 0 || slices.Contains(m.GuardrailProviders, provider))
}

// matchesErrorType reports whether the error's top-level type or its
// error.type is one of types.
func matchesErrorType(types []string, errDetails *schemas.BifrostError) bool {
	if errDetails == nil {
		return false
	}
	if errDetails.Type != nil && slices.Contains(types, *errDetails.Type) {
		return true
	}
	return errDetails.Error != nil && errDetails.Error.Type != nil && slices.Contains(types, *errDetails.Error.Type)
}

// routeErrorDetails returns l's parsed error, decoding the serialized column
// when only that is set. l itself is never modified.
func routeErrorDetails(l *Log) *schemas.BifrostError {
	if l.ErrorDetailsParsed != nil || l.ErrorDetails == "" {
		return l.ErrorDetailsParsed
	}
	var parsed schemas.BifrostError
	if err := sonic.UnmarshalString(l.ErrorDetails, &parsed); err != nil {
		return nil
	}
	return &parsed
}

// routeGuardrailDebug returns l's parsed guardrail metadata, decoding the
// serialized column when only that is set. l itself is never modified.
func routeGuardrailDebug(l *Log) *schemas.BifrostGuardrailMetadata {
	if l.GuardrailDebugParsed != nil || l.GuardrailDebug == "" {
		return l.GuardrailDebugParsed
	}
	var parsed schemas.BifrostGuardrailMetadata
	if err := sonic.UnmarshalString(l.GuardrailDebug, &parsed); err != nil {
		return nil
	}
	return &parsed
}
