package sarvam

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// unsupportedReasoningEfforts are reasoning_effort values real OpenAI accepts
// (as ways to disable reasoning) that Sarvam's API rejects outright
// ("Input should be 'low', 'medium' or 'high'"), verified live against the
// real API. Sarvam has no way to disable reasoning at all, so the closest
// available behavior is to drop the field (leaving Sarvam's reasoning-on-by-
// default behavior) rather than surface a 400 for a value real OpenAI itself
// would accept.
//
// Deliberately excludes "minimal": that value already has a real Sarvam-
// compatible mapping - core/providers/openai/chat.go's generic
// filterOpenAISpecificParameters (which Sarvam goes through via its
// `default:` case, same as any other non-special-cased provider) already
// normalizes "minimal" -> "low" before the request is built, and "low" is
// one of Sarvam's three accepted values. Dropping "minimal" here instead of
// letting the generic mapping run would silently downgrade an explicit
// low-effort request to Sarvam's full-reasoning default. Mirrors the same
// none-only distinction core/providers/openai/chat.go's Vertex case makes.
var unsupportedReasoningEfforts = map[string]struct{}{
	"none": {},
}

// normalizeRequest applies every Sarvam-specific wire-compatibility fixup to a
// chat request in one pass, each individually a no-op (same pointer, no copy)
// when it doesn't apply. Order doesn't matter between these three - they each
// touch disjoint parts of the request (message content, message role,
// reasoning params).
func normalizeRequest(request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	if request == nil {
		return request
	}
	return normalizeReasoningEffort(normalizeDeveloperRole(flattenMultiPartMessageContent(request)))
}

// normalizeReasoningEffort returns a shallow copy of request with an
// unsupported reasoning_effort value (see unsupportedReasoningEfforts) cleared
// so the request doesn't get rejected outright.
func normalizeReasoningEffort(request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	if request.Params == nil || request.Params.Reasoning == nil || request.Params.Reasoning.Effort == nil {
		return request
	}
	if _, unsupported := unsupportedReasoningEfforts[strings.ToLower(strings.TrimSpace(*request.Params.Reasoning.Effort))]; !unsupported {
		return request
	}

	normalized := *request
	normalizedParams := *request.Params
	normalizedReasoning := *request.Params.Reasoning
	normalizedReasoning.Effort = nil
	// Also clear MaxTokens: core/providers/openai/chat.go's
	// normalizeReasoningEffort infers a fresh Effort from MaxTokens whenever
	// Effort is nil, so leaving a caller-supplied MaxTokens budget in place
	// would silently recreate the exact effort this function just dropped.
	// Sarvam doesn't support a token-budget style reasoning control anyway.
	normalizedReasoning.MaxTokens = nil
	normalizedParams.Reasoning = &normalizedReasoning
	normalized.Params = &normalizedParams

	return &normalized
}

// isTextOnlyContentBlocks reports whether every block is a plain text block
// with non-nil Text, i.e. safe to collapse into a single string without
// losing multimodal content (images/audio/files) or silently dropping a
// malformed text block. The nil-Text rejection follows the same defensive
// convention as core/providers/openai/types.go's
// isFunctionCallOutputBlocksFlattenable, which also rejects a text-typed
// block with a nil Text field rather than skipping it - though the two
// diverge on an empty slice: that helper treats it as non-flattenable
// (returns false), while this one treats it as trivially flattenable
// (returns true, collapsing to ContentStr: ""), since an empty array is
// exactly the kind of "array" shape Sarvam's API rejects outright.
func isTextOnlyContentBlocks(blocks []schemas.ChatContentBlock) bool {
	for _, block := range blocks {
		if block.Type != schemas.ChatContentBlockTypeText || block.Text == nil {
			return false
		}
	}
	return true
}

// flattenMultiPartMessageContent returns a shallow copy of request with any
// message's multi-part, text-only Content (ContentBlocks, e.g. from OpenAI
// Responses API callers like Codex that send instructions as several
// {"type":"text",...} blocks) collapsed into a single plain string.
//
// Unlike OpenAI, Sarvam's API rejects message.content as an array outright -
// even a single-element one - with "Input should be a valid string", on any
// role (system and user both verified live against the real API). A plain
// string always succeeds. Messages containing non-text blocks (images/audio/
// files) are left untouched - Sarvam's text-only chat models don't support
// those anyway and should surface their own clear rejection rather than have
// this silently drop content.
//
// Scope: only rewrites request.Input, so it has no effect when the caller
// uses raw-request-body or large-payload passthrough (both bypass Input
// entirely and forward the original bytes verbatim) - a caller relying on
// either of those with array-content or a "developer" role message will
// still get rejected by Sarvam. Accepted gap: those modes are an explicit
// opt-in to exact byte-for-byte forwarding, so normalizing them would
// contradict their purpose.
//
// Note: only the block's Text is preserved - any CacheControl/Citations
// metadata on a text block is dropped along with the array structure. Sarvam
// doesn't support prompt caching or citations, so this has no behavioral
// effect for Sarvam today, but would need revisiting if this helper were
// ever reused for a provider that does.
func flattenMultiPartMessageContent(request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	needsFlattening := false
	for _, msg := range request.Input {
		if msg.Content != nil && msg.Content.ContentBlocks != nil && isTextOnlyContentBlocks(msg.Content.ContentBlocks) {
			needsFlattening = true
			break
		}
	}
	if !needsFlattening {
		return request
	}

	flattened := *request
	flattened.Input = make([]schemas.ChatMessage, len(request.Input))
	copy(flattened.Input, request.Input)

	for i, msg := range flattened.Input {
		if msg.Content == nil || msg.Content.ContentBlocks == nil || !isTextOnlyContentBlocks(msg.Content.ContentBlocks) {
			continue
		}
		// isTextOnlyContentBlocks (checked above) guarantees every block here
		// has Type == text and Text != nil, so no nil-check/skip is needed -
		// matches core/providers/openai/types.go's flattenFunctionCallOutputBlocks.
		var text strings.Builder
		for j, block := range msg.Content.ContentBlocks {
			if j > 0 {
				text.WriteString("\n")
			}
			text.WriteString(*block.Text)
		}
		flattenedStr := text.String()
		flattened.Input[i].Content = &schemas.ChatMessageContent{ContentStr: &flattenedStr}
	}

	return &flattened
}

// normalizeDeveloperRole returns a shallow copy of request with any
// "developer"-role message's role rewritten to "system".
//
// Real OpenAI accepts "developer" (the newer replacement for "system") on
// both its Responses and Chat Completions endpoints, so Bifrost's core
// Responses-to-Chat fallback (schemas.BifrostResponsesRequest.ToChatRequest,
// via normalizeDeveloperRoleForChatFallback) already normalizes it for that
// path - see the identical pattern in core/providers/anthropic/chat.go,
// which treats ChatMessageRoleSystem and ChatMessageRoleDeveloper the same.
// Sarvam's API rejects "developer" outright ("Must be one of: assistant,
// system, tool, user"), verified live via a direct /v1/chat/completions
// call (bypassing the Responses fallback, so the core normalization above
// never runs), so this is Sarvam's own equivalent for that entry point.
func normalizeDeveloperRole(request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	needsNormalizing := false
	for _, msg := range request.Input {
		if msg.Role == schemas.ChatMessageRoleDeveloper {
			needsNormalizing = true
			break
		}
	}
	if !needsNormalizing {
		return request
	}

	normalized := *request
	normalized.Input = make([]schemas.ChatMessage, len(request.Input))
	copy(normalized.Input, request.Input)

	for i, msg := range normalized.Input {
		if msg.Role == schemas.ChatMessageRoleDeveloper {
			normalized.Input[i].Role = schemas.ChatMessageRoleSystem
		}
	}

	return &normalized
}
