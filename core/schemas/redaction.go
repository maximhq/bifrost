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

// IsContentAttribute reports whether a span attribute may carry user or model content.
func IsContentAttribute(key string) bool {
	switch key {
	case AttrInputMessages, AttrOutputMessages,
		AttrInputText, AttrInputSpeech,
		AttrInputEmbedding,
		AttrPrompt, AttrInstructions,
		AttrRespReasoningText:
		return true
	case AttrTools, AttrRespTools,
		AttrToolName, AttrToolCallID,
		AttrToolCallArguments, AttrToolCallResult,
		AttrToolType,
		AttrToolChoiceType, AttrToolChoiceName,
		AttrRespToolChoiceType, AttrRespToolChoiceName:
		return true
	default:
		return false
	}
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
