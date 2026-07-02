package schemas

import (
	"sort"
	"strings"

	"github.com/bytedance/sonic"
)

// RedactionPayload carries request-scoped redaction data from guardrails to log and trace sinks.
type RedactionPayload struct {
	ReversibleMappings      map[string]string `json:"reversible_mappings,omitempty"`
	LiteralReplacements     map[string]string `json:"literal_replacements,omitempty"`
	InputHistory            string            `json:"input_history,omitempty"`
	ResponsesInputHistory   string            `json:"responses_input_history,omitempty"`
	OutputMessage           string            `json:"output_message,omitempty"`
	ResponsesOutput         string            `json:"responses_output,omitempty"`
	RawRequest              string            `json:"raw_request,omitempty"`
	RawResponse             string            `json:"raw_response,omitempty"`
	PassthroughRequestBody  string            `json:"passthrough_request_body,omitempty"`
	PassthroughResponseBody string            `json:"passthrough_response_body,omitempty"`
	ContentSummary          string            `json:"content_summary,omitempty"`
}

// HasRedactionPayload reports whether a payload carries any redaction data worth preserving.
func HasRedactionPayload(payload RedactionPayload) bool {
	return len(payload.ReversibleMappings) > 0 ||
		len(payload.LiteralReplacements) > 0 ||
		payload.InputHistory != "" ||
		payload.ResponsesInputHistory != "" ||
		payload.OutputMessage != "" ||
		payload.ResponsesOutput != "" ||
		payload.RawRequest != "" ||
		payload.RawResponse != "" ||
		payload.PassthroughRequestBody != "" ||
		payload.PassthroughResponseBody != "" ||
		payload.ContentSummary != ""
}

// MarshalRedactionPayload serializes a redaction payload for transient log transport.
func MarshalRedactionPayload(payload RedactionPayload) (string, error) {
	return sonic.MarshalString(payload)
}

// RedactionPayloadFromContext returns the redaction payload stored on ctx.
func RedactionPayloadFromContext(ctx *BifrostContext) (RedactionPayload, bool) {
	if ctx == nil {
		return RedactionPayload{}, false
	}
	switch value := ctx.Value(BifrostContextKeyRedactionData).(type) {
	case RedactionPayload:
		return value, HasRedactionPayload(value)
	case *RedactionPayload:
		if value == nil {
			return RedactionPayload{}, false
		}
		return *value, HasRedactionPayload(*value)
	case string:
		if value == "" {
			return RedactionPayload{}, false
		}
		var payload RedactionPayload
		if err := sonic.Unmarshal([]byte(value), &payload); err != nil {
			return RedactionPayload{}, false
		}
		return payload, HasRedactionPayload(payload)
	case []byte:
		if len(value) == 0 {
			return RedactionPayload{}, false
		}
		var payload RedactionPayload
		if err := sonic.Unmarshal(value, &payload); err != nil {
			return RedactionPayload{}, false
		}
		return payload, HasRedactionPayload(payload)
	default:
		return RedactionPayload{}, false
	}
}

// RedactionPayloadStringFromContext returns the payload as JSON for transient log entries.
func RedactionPayloadStringFromContext(ctx *BifrostContext) (string, bool) {
	if ctx == nil {
		return "", false
	}
	switch value := ctx.Value(BifrostContextKeyRedactionData).(type) {
	case string:
		return value, value != ""
	case []byte:
		return string(value), len(value) > 0
	default:
		payload, ok := RedactionPayloadFromContext(ctx)
		if !ok {
			return "", false
		}
		serialized, err := MarshalRedactionPayload(payload)
		if err != nil {
			return "", false
		}
		return serialized, serialized != ""
	}
}

// SetRedactionPayloadOnContext stores a non-empty redaction payload on ctx.
func SetRedactionPayloadOnContext(ctx *BifrostContext, payload RedactionPayload) bool {
	if ctx == nil || !HasRedactionPayload(payload) {
		return false
	}
	ctx.SetValue(BifrostContextKeyRedactionData, payload)
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
		copy(redacted, v)
		for i, item := range redacted {
			if text, ok := item.(string); ok {
				redacted[i] = ApplyLiteralReplacements(text, replacements)
			}
		}
		return redacted
	default:
		return value
	}
}
