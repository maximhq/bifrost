package schemas

import (
	"maps"
	"sort"
	"strings"
)

// RedactionPhase identifies which request lifecycle phase produced a redaction finding.
type RedactionPhase string

const (
	// RedactionPhaseInput marks redaction findings discovered while inspecting request-side content.
	RedactionPhaseInput RedactionPhase = "input"
	// RedactionPhaseOutput marks redaction findings discovered while inspecting response-side content.
	RedactionPhaseOutput RedactionPhase = "output"
)

// RedactionData carries request-scoped redaction data from guardrails to log and trace sinks.
type RedactionData struct {
	LiteralReplacements RedactionMapsByPhase `json:"literal_replacements,omitempty"`
	ReversibleMappings  RedactionMapsByPhase `json:"reversible_mappings,omitempty"`
}

// RedactionMapsByPhase stores replacement maps separately for request and response content.
type RedactionMapsByPhase struct {
	Input  map[string]string `json:"input,omitempty"`
	Output map[string]string `json:"output,omitempty"`
}

// Clone returns an owned copy of the phase-scoped maps.
func (m RedactionMapsByPhase) Clone() RedactionMapsByPhase {
	return RedactionMapsByPhase{
		Input:  maps.Clone(m.Input),
		Output: maps.Clone(m.Output),
	}
}

// HasReplacements reports whether either phase has replacement entries.
func (m RedactionMapsByPhase) HasReplacements() bool {
	return len(m.Input) > 0 || len(m.Output) > 0
}

// MergePhase merges replacements into one phase, copying entries so callers cannot mutate stored state.
func (m *RedactionMapsByPhase) MergePhase(phase RedactionPhase, replacements map[string]string) {
	if m == nil || len(replacements) == 0 {
		return
	}
	switch phase {
	case RedactionPhaseInput:
		m.Input = mergeRedactionStringMaps(m.Input, replacements)
	case RedactionPhaseOutput:
		m.Output = mergeRedactionStringMaps(m.Output, replacements)
	}
}

// MergedForMixedFields returns both phase maps for fields that can contain input and output.
func (m RedactionMapsByPhase) MergedForMixedFields() map[string]string {
	return mergeRedactionStringMaps(m.Input, m.Output)
}

// Clone returns an owned snapshot of the redaction data maps.
//
// Redaction data moves from request context into async log entries. A plain
// struct copy would still share the underlying Go maps, so the log entry could
// observe later request-context mutations. Cloning gives the log its own stable
// data while keeping the copy shallow, which is enough because the maps only
// contain immutable strings.
func (d RedactionData) Clone() RedactionData {
	return RedactionData{
		LiteralReplacements: d.LiteralReplacements.Clone(),
		ReversibleMappings:  d.ReversibleMappings.Clone(),
	}
}

// HasReplacements reports whether any reversible or literal redaction data is present.
func (d RedactionData) HasReplacements() bool {
	return d.LiteralReplacements.HasReplacements() || d.ReversibleMappings.HasReplacements()
}

// RedactionDataFromContext returns the redaction data stored on ctx.
func RedactionDataFromContext(ctx *BifrostContext) (RedactionData, bool) {
	if ctx == nil {
		return RedactionData{}, false
	}
	data, ok := ctx.Value(BifrostContextKeyRedactionData).(RedactionData)
	if !ok || !data.HasReplacements() {
		return RedactionData{}, false
	}
	return data, true
}

// SetRedactionDataOnContext stores non-empty redaction data on ctx.
func SetRedactionDataOnContext(ctx *BifrostContext, data RedactionData) bool {
	if ctx == nil || !data.HasReplacements() {
		return false
	}
	ctx.SetValue(BifrostContextKeyRedactionData, data)
	return true
}

// IsContentAttribute reports whether a span attribute may carry user or model content.
func IsContentAttribute(key string) bool {
	return traceContentAttributeScopeForKey(key) != traceContentAttributeScopeNone
}

// ApplyLiteralReplacements performs deterministic best-effort string redaction.
func ApplyLiteralReplacements(text string, replacements map[string]string) string {
	if text == "" || len(replacements) == 0 {
		return text
	}
	keys := make([]string, 0, len(replacements))
	for raw := range replacements {
		if raw != "" {
			keys = append(keys, raw)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) == len(keys[j]) {
			return keys[i] < keys[j]
		}
		return len(keys[i]) > len(keys[j])
	})
	redacted := text
	for _, raw := range keys {
		redacted = strings.ReplaceAll(redacted, raw, replacements[raw])
	}
	return redacted
}

// RedactAttributeValue applies literal replacements to supported attribute value shapes.
func RedactAttributeValue(value any, replacements map[string]string) any {
	if len(replacements) == 0 {
		return value
	}
	switch v := value.(type) {
	case string:
		return ApplyLiteralReplacements(v, replacements)
	case []string:
		redacted := make([]string, len(v))
		for i, item := range v {
			redacted[i] = ApplyLiteralReplacements(item, replacements)
		}
		return redacted
	case []any:
		redacted := make([]any, len(v))
		for i, item := range v {
			redacted[i] = RedactAttributeValue(item, replacements)
		}
		return redacted
	case map[string]any:
		redacted := make(map[string]any, len(v))
		for key, item := range v {
			redactedKey := ApplyLiteralReplacements(key, replacements)
			redacted[redactedKey] = RedactAttributeValue(item, replacements)
		}
		return redacted
	case map[string]string:
		redacted := make(map[string]string, len(v))
		for key, item := range v {
			redactedKey := ApplyLiteralReplacements(key, replacements)
			redacted[redactedKey] = ApplyLiteralReplacements(item, replacements)
		}
		return redacted
	default:
		return value
	}
}

// mergeRedactionStringMaps returns a copied map containing both map values, with next taking precedence.
func mergeRedactionStringMaps(current map[string]string, next map[string]string) map[string]string {
	if len(current) == 0 && len(next) == 0 {
		return nil
	}
	merged := maps.Clone(current)
	if merged == nil {
		merged = make(map[string]string, len(next))
	}
	for key, value := range next {
		merged[key] = value
	}
	return merged
}
