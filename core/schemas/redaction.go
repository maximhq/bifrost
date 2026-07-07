package schemas

import (
	"maps"
	"sort"
	"strings"
)

// RedactionData carries request-scoped redaction data from guardrails to log and trace sinks.
type RedactionData struct {
	ReversibleMappings  map[string]string `json:"reversible_mappings,omitempty"`
	LiteralReplacements map[string]string `json:"literal_replacements,omitempty"`
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
		ReversibleMappings:  maps.Clone(d.ReversibleMappings),
		LiteralReplacements: maps.Clone(d.LiteralReplacements),
	}
}

// RedactionDataFromContext returns the redaction data stored on ctx.
func RedactionDataFromContext(ctx *BifrostContext) (RedactionData, bool) {
	if ctx == nil {
		return RedactionData{}, false
	}
	data, ok := ctx.Value(BifrostContextKeyRedactionData).(RedactionData)
	if !ok || (len(data.ReversibleMappings) == 0 && len(data.LiteralReplacements) == 0) {
		return RedactionData{}, false
	}
	return data, true
}

// SetRedactionDataOnContext stores non-empty redaction data on ctx.
func SetRedactionDataOnContext(ctx *BifrostContext, data RedactionData) bool {
	if ctx == nil || (len(data.ReversibleMappings) == 0 && len(data.LiteralReplacements) == 0) {
		return false
	}
	ctx.SetValue(BifrostContextKeyRedactionData, data)
	return true
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
