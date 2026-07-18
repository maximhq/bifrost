package anthropic

import (
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ExtractAnthropicPassthroughUsage extracts usage from a passthrough response payload. path is
// the stripped request path; body is a single SSE data event (streaming) or the full response
// body (non-streaming). Streaming /messages usage is assembled per-event by
// AnthropicPassthroughStreamUsage, so here /messages only ever sees a plain JSON body.
func ExtractAnthropicPassthroughUsage(path string, _, body []byte) *schemas.BifrostPassthroughUsage {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}

	switch {
	case strings.HasSuffix(path, "/messages"):
		return extractAnthropicMessagesUsage(body)
	case strings.HasSuffix(path, "/complete"):
		return extractAnthropicCompleteUsage(body)
	}
	return nil
}

func HasAnthropicPassthroughUsage(event []byte) bool {
	return providerUtils.GetJSONField(event, "usage").Exists() ||
		providerUtils.GetJSONField(event, "message.usage").Exists()
}

// buildAnthropicPassthroughUsage converts AnthropicUsage directly into BifrostPassthroughUsage.
func buildAnthropicPassthroughUsage(au *AnthropicUsage) *schemas.BifrostPassthroughUsage {
	if au == nil {
		return nil
	}
	totalInput := au.InputTokens + au.CacheReadInputTokens + au.CacheCreationInputTokens
	total := totalInput + au.OutputTokens
	if total == 0 {
		return nil
	}

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     totalInput,
		CompletionTokens: au.OutputTokens,
		TotalTokens:      total,
	}

	if au.CacheReadInputTokens > 0 || au.CacheCreationInputTokens > 0 {
		details := &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  au.CacheReadInputTokens,
			CachedWriteTokens: au.CacheCreationInputTokens,
		}
		if au.CacheCreation.Ephemeral5mInputTokens > 0 || au.CacheCreation.Ephemeral1hInputTokens > 0 {
			details.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: au.CacheCreation.Ephemeral5mInputTokens,
				CachedWriteTokens1h: au.CacheCreation.Ephemeral1hInputTokens,
			}
		}
		usage.PromptTokensDetails = details
	}

	if au.ServerToolUse != nil && au.ServerToolUse.WebSearchRequests > 0 {
		n := au.ServerToolUse.WebSearchRequests
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			NumSearchQueries: &n,
		}
	}

	if au.OutputTokensDetails != nil && au.OutputTokensDetails.ThinkingTokens > 0 {
		if usage.CompletionTokensDetails == nil {
			usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
		}
		usage.CompletionTokensDetails.ReasoningTokens = au.OutputTokensDetails.ThinkingTokens
	}

	u := &schemas.BifrostPassthroughUsage{LLMUsage: usage}
	if au.ServiceTier != nil {
		t := MapAnthropicServiceTierToBifrost(*au.ServiceTier)
		u.ServiceTier = &t
	}
	if au.Speed != nil {
		u.Speed = au.Speed
	}
	if au.InferenceGeo != nil {
		u.InferenceGeo = au.InferenceGeo
	}
	return u
}

// AnthropicPassthroughStreamUsage incrementally merges /v1/messages stream usage across events
// without retaining the response body. Anthropic splits usage: message_start nests it under
// message.usage (input, cache tokens incl. 5m/1h split, service_tier), while message_delta has
// it at the top level (final output). Taking the max of each field across events combines them
// order-independently — the same merge the native Anthropic stream does (anthropic.go).
type AnthropicPassthroughStreamUsage struct {
	combined AnthropicUsage
	seen     bool
}

// ObserveEvent merges one framed SSE data payload's usage into the running total and returns
// the running usage (nil until any usage-bearing event is seen).
func (a *AnthropicPassthroughStreamUsage) ObserveEvent(event []byte) *schemas.BifrostPassthroughUsage {
	var evt AnthropicStreamEvent
	if err := sonic.Unmarshal(event, &evt); err != nil {
		return a.usage()
	}
	// message_delta carries usage at the top level; message_start nests it under message.usage.
	var u *AnthropicUsage
	if evt.Usage != nil {
		u = evt.Usage
	} else if evt.Message != nil && evt.Message.Usage != nil {
		u = evt.Message.Usage
	}
	if u == nil {
		return a.usage()
	}

	a.seen = true
	c := &a.combined
	if u.InputTokens > c.InputTokens {
		c.InputTokens = u.InputTokens
	}
	if u.OutputTokens > c.OutputTokens {
		c.OutputTokens = u.OutputTokens
	}
	if u.CacheReadInputTokens > c.CacheReadInputTokens {
		c.CacheReadInputTokens = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens > c.CacheCreationInputTokens {
		c.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	if u.CacheCreation.Ephemeral5mInputTokens > c.CacheCreation.Ephemeral5mInputTokens {
		c.CacheCreation.Ephemeral5mInputTokens = u.CacheCreation.Ephemeral5mInputTokens
	}
	if u.CacheCreation.Ephemeral1hInputTokens > c.CacheCreation.Ephemeral1hInputTokens {
		c.CacheCreation.Ephemeral1hInputTokens = u.CacheCreation.Ephemeral1hInputTokens
	}
	if u.ServerToolUse != nil {
		if c.ServerToolUse == nil {
			c.ServerToolUse = &AnthropicServerToolUseUsage{}
		}
		if u.ServerToolUse.WebSearchRequests > c.ServerToolUse.WebSearchRequests {
			c.ServerToolUse.WebSearchRequests = u.ServerToolUse.WebSearchRequests
		}
	}
	if u.OutputTokensDetails != nil {
		if c.OutputTokensDetails == nil {
			c.OutputTokensDetails = &AnthropicOutputTokensDetails{}
		}
		if u.OutputTokensDetails.ThinkingTokens > c.OutputTokensDetails.ThinkingTokens {
			c.OutputTokensDetails.ThinkingTokens = u.OutputTokensDetails.ThinkingTokens
		}
	}
	if u.ServiceTier != nil {
		c.ServiceTier = u.ServiceTier
	}
	if u.Speed != nil {
		c.Speed = u.Speed
	}
	if u.InferenceGeo != nil {
		c.InferenceGeo = u.InferenceGeo
	}
	return a.usage()
}

func (a *AnthropicPassthroughStreamUsage) usage() *schemas.BifrostPassthroughUsage {
	if !a.seen {
		return nil
	}
	return buildAnthropicPassthroughUsage(&a.combined)
}

// extractAnthropicMessagesUsage parses usage from a /v1/messages response body. Streaming usage
// is assembled per-event by AnthropicPassthroughStreamUsage, so this only sees a plain JSON
// (non-streaming) body, which carries the full usage block at the top level.
func extractAnthropicMessagesUsage(body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) == 0 {
		return nil
	}
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal(body, &resp); err != nil || resp.Usage == nil {
		return nil
	}
	return buildAnthropicPassthroughUsage(resp.Usage)
}

// extractAnthropicCompleteUsage handles the legacy /v1/complete endpoint.
func extractAnthropicCompleteUsage(body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) == 0 {
		return nil
	}
	var resp struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := sonic.Unmarshal(body, &resp); err != nil || resp.Usage == nil {
		return nil
	}
	total := resp.Usage.InputTokens + resp.Usage.OutputTokens
	if total == 0 {
		return nil
	}
	return &schemas.BifrostPassthroughUsage{
		LLMUsage: &schemas.BifrostLLMUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      total,
		},
	}
}
