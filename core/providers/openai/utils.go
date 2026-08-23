package openai

import (
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// CustomResponseHandler is a function that produces a Bifrost response from a Bifrost request.
// T is the concrete Bifrost response type (e.g. BifrostEmbeddingResponse, BifrostTextCompletionResponse, BifrostChatResponse, BifrostResponsesResponse, BifrostImageGenerationResponse, BifrostTranscriptionResponse).
type responseHandler[T any] func(responseBody []byte, response *T, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (rawRequest interface{}, rawResponse interface{}, bifrostErr *schemas.BifrostError)

// ---- Name-based capability defaults ----
//
// Used when the datasheet publishes no row for a (provider, model) pair. Caps
// gates in this package pass one of these as their fallback.

// defaultSupportsReasoningContentBlocks: gpt-oss carries replayed reasoning as
// content blocks; the o-series and GPT-5 carry it as summary[] entries.
func defaultSupportsReasoningContentBlocks(model string) bool {
	return strings.Contains(strings.ToLower(model), "gpt-oss")
}

// IsOpenAIReasoningModel matches OpenAI-family models that accept
// reasoning.effort: the o1/o3/o4 series, GPT-5.x, and gpt-oss. Broader than
// isOSeriesModel, which covers the o-series alone.
func IsOpenAIReasoningModel(model string) bool {
	_, parsedModel := schemas.ParseModelString(model, schemas.OpenAI)
	if parsedModel != "" {
		model = parsedModel
	}
	modelLower := strings.ToLower(model)
	if strings.Contains(modelLower, "gpt-oss") {
		return true
	}
	for _, prefix := range []string{"o1", "o3", "o4"} {
		if strings.HasPrefix(modelLower, prefix) {
			if len(modelLower) == len(prefix) ||
				modelLower[len(prefix)] == '-' ||
				modelLower[len(prefix)] == '_' {
				return true
			}
		}
		if strings.Contains(modelLower, "-"+prefix+"-") ||
			strings.Contains(modelLower, "_"+prefix+"_") ||
			strings.HasSuffix(modelLower, "-"+prefix) ||
			strings.HasSuffix(modelLower, "_"+prefix) {
			return true
		}
	}
	return strings.Contains(modelLower, "gpt-5")
}

// defaultEffortControl widens the base low/medium/high ladder with the effort
// levels a model natively accepts. Only the widening is name-derived; the
// datasheet's per-level booleans take precedence when a row exists.
func defaultEffortControl(model string) *schemas.EffortControl {
	levels := []string{schemas.ReasoningEffortLow, schemas.ReasoningEffortMedium, schemas.ReasoningEffortHigh}
	if acceptsMinimalEffort(model) {
		levels = append([]string{schemas.ReasoningEffortMinimal}, levels...)
	}
	if acceptsXHighEffort(model) {
		levels = append(levels, schemas.ReasoningEffortXHigh)
	}
	if acceptsMaxEffort(model) {
		levels = append(levels, schemas.ReasoningEffortMax)
	}
	// GLM-5.3 narrowed reasoning_effort to exactly max/high/low and rejects
	// every other value on the General API (low is newly added; medium/minimal/
	// none/xhigh are gone). Publishing the narrowed ladder lets the shared
	// normalizer clamp wider tiers onto the nearest rung — mirroring Zhipu's
	// own Coding Plan coercion table: xhigh→max, medium→high, minimal→low —
	// on every OpenAI-dialect mount (zhipu, zai-style custom providers).
	// "none" is unranked in the downgrade ladder by design (it disables
	// reasoning rather than lowering it), so it reaches "low" through Renames,
	// which is consulted before the ladder. GLM-5.2 keeps the full legacy enum;
	// the vendor maps the extra tiers itself.
	if isGLM53OrLater(bareModelLower(model)) {
		return &schemas.EffortControl{
			Levels:  []string{schemas.ReasoningEffortLow, schemas.ReasoningEffortHigh, schemas.ReasoningEffortMax},
			Renames: map[string]string{schemas.ReasoningEffortNone: schemas.ReasoningEffortLow},
		}
	}
	return &schemas.EffortControl{Levels: levels}
}

// acceptsXHighEffort reports models that natively accept "xhigh" effort. The
// ladder is shared by every OpenAI-dialect provider, so non-OpenAI families
// supporting the tier are recognised here too — otherwise their "xhigh" is
// downgraded to "high" before the provider-specific compat pass ever runs.
// qwen3.8-max's OpenAI-compatible enum tops out at xhigh
// (none/minimal/low/medium/high/xhigh; the vendor 400'd "max" until ~2026-08-22),
// so publishing xhigh here lets the shared normalizer clamp a requested "max"
// down to xhigh instead of forwarding a value the vendor only lately tolerates.
func acceptsXHighEffort(model string) bool {
	modelLower := bareModelLower(model)
	if schemas.SupportsGrokXHighReasoningEffort(modelLower) {
		return true
	}
	// Substring, not prefix: bareModelLower strips only one leading segment, so
	// real catalog IDs keep a region or vendor namespace ("azure/eu/gpt-5.2" →
	// "eu/gpt-5.2"). These needles carry the full version, so they cannot
	// over-match a different revision.
	return strings.Contains(modelLower, "gpt-5.2") ||
		strings.Contains(modelLower, "gpt-5.3-codex") ||
		strings.Contains(modelLower, "gpt-5.4") ||
		strings.Contains(modelLower, "gpt-5.5") ||
		strings.Contains(modelLower, "gpt-5.6") ||
		strings.Contains(modelLower, "qwen3.8-max")
}

// acceptsMinimalEffort reports models that natively accept "minimal" effort:
// only the original GPT-5 trio. Every later dot-revision dropped it from the
// reasoning.effort enum in favour of "none"/"xhigh"/"max".
//
// Matched on a boundary rather than a substring: "gpt-5" is a literal prefix of
// "gpt-5.1", "gpt-5.2" and so on, so strings.Contains would grant minimal to
// exactly the revisions that removed it.
func acceptsMinimalEffort(model string) bool {
	m := bareModelLower(model)
	i := strings.Index(m, "gpt-5")
	if i < 0 {
		return false
	}
	rest := strings.TrimPrefix(m[i+len("gpt-5"):], "-mini")
	rest = strings.TrimPrefix(rest, "-nano")
	// "" is the base model and "-2025-08-07" a dated snapshot. A leading "." is
	// a dot-revision; anything else is a variant (-codex, -pro, -chat, -search).
	return rest == "" || strings.HasPrefix(rest, "-2")
}

// acceptsMaxEffort reports models that natively accept "max" effort
// (e.g. GPT-5.6, DeepSeek V4, GLM-5.2+). qwen3.8-max is NOT here: its
// OpenAI-compatible enum tops out at xhigh, and the gateway clamps a requested
// "max" down to xhigh via the ladder published by acceptsXHighEffort.
func acceptsMaxEffort(model string) bool {
	modelLower := bareModelLower(model)
	return strings.Contains(modelLower, "gpt-5.6") ||
		strings.Contains(modelLower, "deepseek-v4") ||
		isGLM52OrLater(modelLower) ||
		strings.Contains(modelLower, "kimi-k3") ||
		// Kimi Code stable aliases for Kimi K3 (k3, k3-256k).
		modelLower == "k3" || strings.HasPrefix(modelLower, "k3-")
}

// glm5Minor returns the GLM-5.x minor revision of a (lowercased) model name,
// or -1 when the model is not a glm-5.<digit>... shape. Covers the Coding Plan
// aliases (glm-5.2[1m]) and -air/-flash style variants via the leading-digit
// parse. Substring-anchored so multi-segment catalog IDs
// ("vendor/region/glm-5.3") match as well.
func glm5Minor(modelLower string) int {
	i := strings.Index(modelLower, "glm-5.")
	if i < 0 {
		return -1
	}
	rest := modelLower[i+len("glm-5."):]
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return -1
	}
	minor, err := strconv.Atoi(rest[:digits])
	if err != nil {
		return -1
	}
	return minor
}

// isGLM52OrLater reports whether the (lowercased) model is a GLM-5.x revision
// from 5.2 onward — the first Zhipu series whose OpenAI-compatible surface
// accepts top-level reasoning_effort with the full enum including "max".
// GLM-4.x and GLM-5.0/5.1 ignore or 400 the field (their thinking is controlled
// via the `thinking` extra param), so the floor stays at 5.2. Matching the
// family by version floor instead of listing each revision keeps future GLM
// releases (glm-5.3, glm-5.5, ...) working without a gateway patch.
func isGLM52OrLater(modelLower string) bool {
	return glm5Minor(modelLower) >= 2
}

// isGLM53OrLater reports whether the (lowercased) model is a GLM-5.x revision
// from 5.3 onward — the revision that narrowed reasoning_effort to exactly
// max/high/low and made thinking non-disableable.
func isGLM53OrLater(modelLower string) bool {
	return glm5Minor(modelLower) >= 3
}

// isGLM53OrLaterModel reports whether the (possibly provider-prefixed) model is
// a GLM-5.x revision from 5.3 onward — the revision that narrowed
// reasoning_effort to exactly max/high/low.
func isGLM53OrLaterModel(model string) bool {
	modelLower := bareModelLower(model)
	return isGLM53OrLater(modelLower)
}

// bareModelLower strips any provider prefix and lowercases, so the effort
// ladders match on "gpt-5.4" whether or not the caller sent "openai/gpt-5.4".
func bareModelLower(model string) string {
	if _, parsed := schemas.ParseModelString(model, schemas.OpenAI); parsed != "" {
		model = parsed
	}
	return strings.ToLower(model)
}

func ConvertOpenAIMessagesToBifrostMessages(messages []OpenAIMessage) []schemas.ChatMessage {
	bifrostMessages := make([]schemas.ChatMessage, len(messages))
	for i, message := range messages {
		bifrostMessages[i] = schemas.ChatMessage{
			Name:            message.Name,
			Role:            message.Role,
			Content:         message.Content,
			ChatToolMessage: message.ChatToolMessage,
		}
		if message.OpenAIChatAssistantMessage != nil {
			// Callers replay assistant reasoning under any of three keys. Normalize them
			// onto Reasoning so downstream provider logic sees replayed reasoning
			// regardless of spelling — DeepSeek in particular gates thinking on it.
			reasoning := message.OpenAIChatAssistantMessage.Reasoning
			if reasoning == nil {
				reasoning = message.OpenAIChatAssistantMessage.ReasoningAlias
			}
			if reasoning == nil {
				for _, detail := range message.OpenAIChatAssistantMessage.ReasoningDetails {
					if detail.Text != nil {
						reasoning = detail.Text
						break
					}
				}
			}
			bifrostMessages[i].ChatAssistantMessage = &schemas.ChatAssistantMessage{
				Refusal:          message.OpenAIChatAssistantMessage.Refusal,
				Reasoning:        reasoning,
				ReasoningDetails: message.OpenAIChatAssistantMessage.ReasoningDetails,
				Annotations:      message.OpenAIChatAssistantMessage.Annotations,
				ToolCalls:        message.OpenAIChatAssistantMessage.ToolCalls,
			}
		}
	}
	return bifrostMessages
}

// ConvertBifrostMessagesToOpenAIMessages converts Bifrost chat messages to the
// OpenAI wire format, dropping neutral-format fields the OpenAI wire has no
// carrier for. Over-long tool call IDs are stripped of embedded provider
// reasoning signatures, and tool messages lose is_error so providers that reject
// unknown message parameters never see it.
// The caller's messages are never mutated: shared pointers are cloned before edit.
func ConvertBifrostMessagesToOpenAIMessages(messages []schemas.ChatMessage) []OpenAIMessage {
	openaiMessages := make([]OpenAIMessage, len(messages))
	for i, message := range messages {
		openaiMessages[i] = OpenAIMessage{
			Name:            message.Name,
			Role:            message.Role,
			Content:         message.Content,
			ChatToolMessage: message.ChatToolMessage,
		}
		// Strip provider reasoning signatures (e.g. Gemini thoughtSignatures embedded in
		// call_id as "<baseID>_ts_<sig>") from the tool result's tool_call_id, but only when it
		// exceeds OpenAI's limit — shorter IDs are left intact so distinct upstream IDs are
		// preserved. Clone first — ChatToolMessage is shared with the caller's input.
		if message.ChatToolMessage != nil && message.ChatToolMessage.ToolCallID != nil &&
			len(*message.ChatToolMessage.ToolCallID) > MaxToolCallIDLength {
			if stripped := utils.StripThoughtSignature(*message.ChatToolMessage.ToolCallID); stripped != *message.ChatToolMessage.ToolCallID {
				toolMsgCopy := *message.ChatToolMessage
				toolMsgCopy.ToolCallID = &stripped
				openaiMessages[i].ChatToolMessage = &toolMsgCopy
			}
		}
		// The OpenAI wire format has no tool-error field; strip is_error so
		// providers that reject unknown message parameters never see it. Clone
		// first — ChatToolMessage is shared with the caller's input.
		if openaiMessages[i].ChatToolMessage != nil && openaiMessages[i].ChatToolMessage.IsError != nil {
			toolMsgCopy := *openaiMessages[i].ChatToolMessage
			toolMsgCopy.IsError = nil
			openaiMessages[i].ChatToolMessage = &toolMsgCopy
		}
		if message.ChatAssistantMessage != nil {
			// Strip the same embedded signature from over-long assistant tool call IDs. Clone the
			// slice only when a strip is actually needed so the caller's input is never mutated.
			toolCalls := message.ChatAssistantMessage.ToolCalls
			needsStrip := false
			for j := range toolCalls {
				if toolCalls[j].ID != nil && len(*toolCalls[j].ID) > MaxToolCallIDLength &&
					strings.Contains(*toolCalls[j].ID, utils.ThoughtSignatureSeparator) {
					needsStrip = true
					break
				}
			}
			if needsStrip {
				cloned := make([]schemas.ChatAssistantMessageToolCall, len(toolCalls))
				copy(cloned, toolCalls)
				for j := range cloned {
					if cloned[j].ID != nil && len(*cloned[j].ID) > MaxToolCallIDLength {
						stripped := utils.StripThoughtSignature(*cloned[j].ID)
						cloned[j].ID = &stripped
					}
				}
				toolCalls = cloned
			}
			openaiMessages[i].OpenAIChatAssistantMessage = &OpenAIChatAssistantMessage{
				Refusal:     message.ChatAssistantMessage.Refusal,
				Reasoning:   message.ChatAssistantMessage.Reasoning,
				Annotations: message.ChatAssistantMessage.Annotations,
				ToolCalls:   toolCalls,
			}
		}
	}
	return openaiMessages
}

// MaxUserFieldLength for OpenAI enforces a 64 character maximum on the user field
const MaxUserFieldLength = 64

// MaxToolCallIDLength is OpenAI's 64 character maximum on tool call IDs (call_id / input[].id).
const MaxToolCallIDLength = 64

// SanitizeUserField returns nil if user exceeds MaxUserFieldLength, otherwise returns the original value
func SanitizeUserField(user *string) *string {
	if user != nil && len(*user) > MaxUserFieldLength {
		return nil
	}
	return user
}
