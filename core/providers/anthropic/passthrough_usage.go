package anthropic

import (
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ClassifyPassthroughPath maps an Anthropic passthrough path to the Bifrost request type it
// addresses: /messages is a Responses-shaped request (Bifrost routes Anthropic Messages through
// the Responses schema), /messages/count_tokens is a token count and /complete a legacy text
// completion. A query string is ignored; anything else returns "". Usage extraction and passthrough
// log parsing both dispatch on this.
func ClassifyPassthroughPath(path string) schemas.RequestType {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}

	switch {
	case strings.HasSuffix(path, "/messages"):
		return schemas.ResponsesRequest
	case strings.HasSuffix(path, "/messages/count_tokens"):
		return schemas.CountTokensRequest
	case strings.HasSuffix(path, "/complete"):
		return schemas.TextCompletionRequest
	}
	return ""
}

// ExtractAnthropicPassthroughUsage extracts usage from a passthrough response payload. path is
// the stripped request path; body is a single SSE data event (streaming) or the full response
// body (non-streaming). Streaming /messages usage is assembled per-event by
// AnthropicPassthroughStreamUsage, so here /messages only ever sees a plain JSON body.
func ExtractAnthropicPassthroughUsage(path string, reqBody, body []byte) *schemas.BifrostPassthroughUsage {
	switch ClassifyPassthroughPath(path) {
	case schemas.ResponsesRequest:
		return extractAnthropicMessagesUsageForRequest(reqBody, body)
	case schemas.TextCompletionRequest:
		return extractAnthropicCompleteUsage(body)
	}
	return nil
}

func HasAnthropicPassthroughUsage(event []byte) bool {
	return providerUtils.GetJSONField(event, "usage").Exists() ||
		providerUtils.GetJSONField(event, "message.usage").Exists()
}

// ExtractAnthropicMessagesUsage parses usage from a non-streaming Messages response body.
// Exported for callers whose path does not carry the /messages suffix ExtractAnthropicPassthroughUsage
// dispatches on — e.g. Vertex's :rawPredict.
func ExtractAnthropicMessagesUsage(body []byte) *schemas.BifrostPassthroughUsage {
	return extractAnthropicMessagesUsage(body)
}

// convertAnthropicUsageIterations converts usage.iterations[] to the neutral per-pass form, which is
// what prices the passes billed at another model's rate (advisor). nil when there are none.
func convertAnthropicUsageIterations(iterations []AnthropicUsage) []schemas.ResponsesResponseUsage {
	if len(iterations) == 0 {
		return nil
	}
	out := make([]schemas.ResponsesResponseUsage, 0, len(iterations))
	for i := range iterations {
		if converted := ConvertAnthropicUsageToBifrostUsage(&iterations[i]); converted != nil {
			out = append(out, *converted)
		}
	}
	return out
}

// buildAnthropicPassthroughUsage converts AnthropicUsage directly into BifrostPassthroughUsage.
// The per-pass breakdown is kept alongside the folded totals, since folding drops it.
func buildAnthropicPassthroughUsage(au *AnthropicUsage) *schemas.BifrostPassthroughUsage {
	if au == nil {
		return nil
	}
	iterations := au.Iterations
	au = billableAnthropicUsage(au)
	cacheCreation := anthropicCacheCreationTotal(au)
	totalInput := au.InputTokens + au.CacheReadInputTokens + cacheCreation
	total := totalInput + au.OutputTokens
	if total == 0 {
		return nil
	}

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     totalInput,
		CompletionTokens: au.OutputTokens,
		TotalTokens:      total,
	}

	if au.CacheReadInputTokens > 0 || cacheCreation > 0 {
		details := &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  au.CacheReadInputTokens,
			CachedWriteTokens: cacheCreation,
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
		usage.ToolUsage = &schemas.ToolUsage{WebSearch: &schemas.WebSearchToolUsage{NumRequests: n}}
	}

	// Extended-thinking tokens are already inside au.OutputTokens, so CompletionTokens
	// and TotalTokens above stay as-is.
	if au.OutputTokensDetails != nil && au.OutputTokensDetails.ThinkingTokens > 0 {
		if usage.CompletionTokensDetails == nil {
			usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
		}
		usage.CompletionTokensDetails.ReasoningTokens = au.OutputTokensDetails.ThinkingTokens
	}

	u := &schemas.BifrostPassthroughUsage{LLMUsage: usage, Iterations: convertAnthropicUsageIterations(iterations)}
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
// message.usage, while message_delta carries it at the top level. Non-prompt fields (output,
// thinking, server tools, tier) take the max across events. The prompt split is resolved by
// resolvePrompt, see ObserveEvent.
//
// usage.iterations[] is kept apart from the folded total: it is cumulative (the latest list names
// every pass so far, so it replaces the previous one), and compaction passes are folded into the
// emitted total once, at emit time.
type AnthropicPassthroughStreamUsage struct {
	// RequestBody is the upstream request, read to decide whether the stream's container is billable.
	RequestBody []byte

	combined   AnthropicUsage
	start      anthropicPromptSplit // per-field max of message.usage prompt counters
	delta      anthropicPromptSplit // per-field max of top-level usage prompt counters, with presence
	deltaFinal bool                 // a top-level usage frame reported a non-zero prompt counter
	emit       AnthropicUsage       // scratch for usage(), reused so emitting does not allocate it
	iterations []AnthropicUsage
	container  *AnthropicResponseContainer
	seen       bool
}

// anthropicPromptSplit is the prompt side of a usage frame: the counters that make up prompt_tokens.
type anthropicPromptSplit struct {
	input, cacheRead, cacheWrite, cacheWrite5m, cacheWrite1h int
	reported                                                 anthropicPromptCounterBit
}

func (p *anthropicPromptSplit) merge(u *AnthropicUsage, reported anthropicPromptCounterBit) {
	p.input = max(p.input, u.InputTokens)
	p.cacheRead = max(p.cacheRead, u.CacheReadInputTokens)
	p.cacheWrite = max(p.cacheWrite, u.CacheCreationInputTokens)
	p.cacheWrite5m = max(p.cacheWrite5m, u.CacheCreation.Ephemeral5mInputTokens)
	p.cacheWrite1h = max(p.cacheWrite1h, u.CacheCreation.Ephemeral1hInputTokens)
	p.reported |= reported
}

// reportedAnthropicPromptCounters returns which prompt counters a top-level usage object carries
// with a non-null value, whatever that value is.
func reportedAnthropicPromptCounters(event []byte) anthropicPromptCounterBit {
	const all = anthropicPromptCounterInputTokens | anthropicPromptCounterCacheCreationInputTokens |
		anthropicPromptCounterCacheReadInputTokens | anthropicPromptCounterCacheCreation
	return all &^ absentAnthropicPromptCounters(event)
}

// ObserveEvent merges one framed SSE data payload's usage into the running total and returns
// the running usage (nil until any usage-bearing event is seen).
//
// The prompt counters are not max-merged field by field across events. Some Anthropic-dialect
// upstreams (observed: QwenCloud, Moonshot/Kimi) put a prompt-scale input_tokens on message_start,
// with cache counters zero or absent, and the real cache-aware split only on message_delta. A
// per-field max keeps the prompt-scale input_tokens and adds the delta's cache_read on top of a
// figure that already includes it (#5510). Instead, message_delta's usage is the final, cumulative
// report (Anthropic documents it that way; with server tools its input_tokens grows past
// message_start's). Once a message_delta reports any non-zero prompt counter, every prompt counter
// it carries replaces message_start's, and counters it omits keep message_start's value. A delta
// whose prompt counters are absent or all zero (Anthropic's minimal output-only delta) is ignored
// for the prompt split. The choice keys on the event shape, not arrival order, so the merge
// stays order-independent.
func (a *AnthropicPassthroughStreamUsage) ObserveEvent(event []byte) *schemas.BifrostPassthroughUsage {
	var evt AnthropicStreamEvent
	if err := sonic.Unmarshal(event, &evt); err != nil {
		return a.usage()
	}
	if evt.Delta != nil && evt.Delta.Container != nil {
		a.container = evt.Delta.Container
	}
	// message_delta carries usage at the top level; message_start nests it under message.usage.
	var u *AnthropicUsage
	topLevel := false
	if evt.Usage != nil {
		u = evt.Usage
		topLevel = true
	} else if evt.Message != nil && evt.Message.Usage != nil {
		u = evt.Message.Usage
	}
	if u == nil {
		return a.usage()
	}

	if len(u.Iterations) > 0 {
		a.iterations = u.Iterations
	}

	a.seen = true
	if topLevel {
		// Only a delta with a non-zero prompt counter speaks for the prompt split, so the presence
		// scan is skipped for output-only deltas.
		if u.InputTokens != 0 || u.CacheReadInputTokens != 0 || u.CacheCreationInputTokens != 0 ||
			u.CacheCreation.Ephemeral5mInputTokens != 0 || u.CacheCreation.Ephemeral1hInputTokens != 0 {
			a.delta.merge(u, reportedAnthropicPromptCounters(event))
			a.deltaFinal = true
		}
	} else {
		a.start.merge(u, 0)
	}

	c := &a.combined
	if u.OutputTokens > c.OutputTokens {
		c.OutputTokens = u.OutputTokens
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

// resolvePrompt returns the stream's prompt split: message_start's, with the counters a final
// message_delta reported taking their place (see ObserveEvent).
func (a *AnthropicPassthroughStreamUsage) resolvePrompt() anthropicPromptSplit {
	p := a.start
	if !a.deltaFinal {
		return p
	}
	d := &a.delta
	if d.reported&anthropicPromptCounterInputTokens != 0 {
		p.input = d.input
	}
	if d.reported&anthropicPromptCounterCacheReadInputTokens != 0 {
		p.cacheRead = d.cacheRead
	}
	if d.reported&anthropicPromptCounterCacheCreationInputTokens != 0 {
		p.cacheWrite = d.cacheWrite
	}
	if d.reported&anthropicPromptCounterCacheCreation != 0 {
		p.cacheWrite5m, p.cacheWrite1h = d.cacheWrite5m, d.cacheWrite1h
	} else if d.reported&anthropicPromptCounterCacheCreationInputTokens != 0 && p.cacheWrite5m+p.cacheWrite1h > p.cacheWrite {
		// message_start's 5m/1h split no longer fits inside the total the delta replaced it with,
		// so it described a different figure; keeping it would bill writes that did not happen.
		p.cacheWrite5m, p.cacheWrite1h = 0, 0
	}
	return p
}

func (a *AnthropicPassthroughStreamUsage) usage() *schemas.BifrostPassthroughUsage {
	if !a.seen {
		return nil
	}
	a.emit = a.combined
	c := &a.emit
	p := a.resolvePrompt()
	c.InputTokens = p.input
	c.CacheReadInputTokens = p.cacheRead
	c.CacheCreationInputTokens = p.cacheWrite
	c.CacheCreation.Ephemeral5mInputTokens = p.cacheWrite5m
	c.CacheCreation.Ephemeral1hInputTokens = p.cacheWrite1h
	// Fold compaction passes from the latest cumulative iterations list exactly once.
	c.Iterations = a.iterations
	u := buildAnthropicPassthroughUsage(c)
	if u != nil && u.LLMUsage != nil {
		recordAnthropicContainerSession(&u.LLMUsage.ToolUsage, a.RequestBody, a.container)
	}
	return u
}

// extractAnthropicMessagesUsage parses usage from a /v1/messages response body. Streaming usage
// is assembled per-event by AnthropicPassthroughStreamUsage, so this only sees a plain JSON
// (non-streaming) body, which carries the full usage block at the top level.
func extractAnthropicMessagesUsage(body []byte) *schemas.BifrostPassthroughUsage {
	return extractAnthropicMessagesUsageForRequest(nil, body)
}

// extractAnthropicMessagesUsageForRequest also reads reqBody to record a billable container session.
func extractAnthropicMessagesUsageForRequest(reqBody, body []byte) *schemas.BifrostPassthroughUsage {
	if len(body) == 0 {
		return nil
	}
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal(body, &resp); err != nil || resp.Usage == nil {
		return nil
	}
	u := buildAnthropicPassthroughUsage(resp.Usage)
	if u != nil && u.LLMUsage != nil {
		recordAnthropicContainerSession(&u.LLMUsage.ToolUsage, reqBody, resp.Container)
	}
	return u
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
