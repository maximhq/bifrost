package promptcompression

import "github.com/maximhq/bifrost/core/schemas"

// Provider prompt caches (Anthropic cache_control, Bedrock cachePoint, OpenAI
// prompt_cache_breakpoint) match on a byte-identical prefix of the request. A
// cache marker on a message therefore pins that message and everything before
// it: rewriting any of them would change the prefix and turn a cache hit into a
// full-price miss, defeating the purpose of compressing in the first place.
// The functions below compute how many leading messages must stay untouched.

// protectedCount combines the last marker position with the configured leading
// floor. The floor (e.g. the system prompt) stays protected even when a marker
// sits earlier. lastMarker is -1 when no marker exists.
func protectedCount(lastMarker, leading, total int) int {
	floor := leading
	if floor > total {
		floor = total
	}
	if lastMarker+1 > floor {
		return lastMarker + 1
	}
	return floor
}

// chatProtectedPrefix returns n such that msgs[:n] must remain byte-identical
// so the provider's prompt cache still hits. n is one past the last message
// carrying a cache marker (a content block with CacheControl,
// PromptCacheBreakpoint or CachePoint, or an assistant tool call with
// CacheControl), but never less than min(leading, len(msgs)).
func chatProtectedPrefix(msgs []schemas.ChatMessage, leading int) int {
	last := -1
	for i := range msgs {
		if chatHasCacheMarker(&msgs[i]) {
			last = i
		}
	}
	return protectedCount(last, leading, len(msgs))
}

func chatHasCacheMarker(m *schemas.ChatMessage) bool {
	if m.Content != nil {
		for i := range m.Content.ContentBlocks {
			b := &m.Content.ContentBlocks[i]
			if b.CacheControl != nil || b.PromptCacheBreakpoint != nil || b.CachePoint != nil {
				return true
			}
		}
	}
	if m.ChatAssistantMessage != nil {
		for i := range m.ChatAssistantMessage.ToolCalls {
			if m.ChatAssistantMessage.ToolCalls[i].CacheControl != nil {
				return true
			}
		}
	}
	return false
}

// responsesProtectedPrefix is the Responses API counterpart of
// chatProtectedPrefix. Markers are message-level CacheControl, or CacheControl /
// PromptCacheBreakpoint on a content block, including blocks inside a
// function-call output.
func responsesProtectedPrefix(msgs []schemas.ResponsesMessage, leading int) int {
	last := -1
	for i := range msgs {
		if responsesHasCacheMarker(&msgs[i]) {
			last = i
		}
	}
	return protectedCount(last, leading, len(msgs))
}

func responsesHasCacheMarker(m *schemas.ResponsesMessage) bool {
	if m.CacheControl != nil {
		return true
	}
	if m.Content != nil && responsesBlocksHaveMarker(m.Content.ContentBlocks) {
		return true
	}
	if m.ResponsesToolMessage != nil && m.ResponsesToolMessage.Output != nil &&
		responsesBlocksHaveMarker(m.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks) {
		return true
	}
	return false
}

func responsesBlocksHaveMarker(blocks []schemas.ResponsesMessageContentBlock) bool {
	for i := range blocks {
		if blocks[i].CacheControl != nil || blocks[i].PromptCacheBreakpoint != nil {
			return true
		}
	}
	return false
}
