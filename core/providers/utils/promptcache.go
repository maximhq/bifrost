package utils

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// MaxInjectedCacheBreakpoints caps how many markers injection may create.
//
// Anthropic rejects a request carrying more than four blocks with cache_control
// outright ("A maximum of 4 blocks with cache_control may be provided. Found 5."),
// and every downstream dialect derives from that ceiling. The per-provider clamps
// (clampAnthropicCacheBreakpoints, clampBedrockCachePoints) are the backstop, but
// injection must not rely on them: a clamp silently discards the earliest marker,
// so overshooting here would quietly throw away work rather than fail loudly.
const MaxInjectedCacheBreakpoints = 4

// PromptCacheInjectionEnabled reports whether breakpoint injection should run for
// this provider and model.
//
// Two gates, and both must pass. The operator has to have opted in (Bifrost never
// invents a breakpoint by default — the four-marker budget is scarce and spending one
// is a cost decision that belongs to them), and the model has to be able to act on a
// per-block marker at all. The capability gate is what makes this safe to call
// unconditionally for all providers: implicit-caching endpoints answer false and are
// never sent a marker they would reject or ignore.
func PromptCacheInjectionEnabled(cfg *schemas.PromptCacheConfig, provider schemas.ModelProvider, model string) bool {
	if cfg == nil || (!cfg.AutoInject && len(cfg.InjectionPoints) == 0) {
		return false
	}
	caps := schemas.ResolveModelCaps(provider, model)
	return caps.SupportsPromptCaching(schemas.ModelSupportsPromptCaching(provider, model))
}

// ResolvePromptCacheConfig applies a per-request override of auto_inject on top of the
// provider config, mirroring how the raw-capture flags resolve.
//
// The override cannot manufacture opt-in. A provider with no prompt_cache config at all
// is one whose operator has expressed no opinion, and a request header must not spend a
// cache checkpoint — or change the billing profile — on their behalf. Once the operator
// has configured prompt_cache, the header flips auto_inject in either direction: on for
// a single request without a config change, or off for a request that should not be
// touched.
//
// Only auto_inject is overridable. Injection points stay a config-level decision, since
// a caller that wants specific placement can simply send the markers itself. The model
// capability gate applies either way, so a header cannot force a marker onto a model
// that has no use for one.
//
// The returned config is a copy whenever the override changes anything: the provider
// config is shared across every request to that provider and must never be written
// through.
func ResolvePromptCacheConfig(ctx *schemas.BifrostContext, cfg *schemas.PromptCacheConfig) *schemas.PromptCacheConfig {
	if ctx == nil || cfg == nil {
		return cfg
	}
	override, ok := ctx.Value(schemas.BifrostContextKeyPromptCacheAutoInject).(bool)
	if !ok || cfg.AutoInject == override {
		return cfg
	}
	cp := *cfg
	cp.AutoInject = override
	return &cp
}

// InjectResponsesCacheBreakpoints returns input with cache_control markers added
// where the config asks for them, or input unchanged when it does not apply.
//
// The returned slice is safe to hand to a provider: messages that are marked are
// deep-copied first. The caller's slice, its ResponsesMessageContent pointers and the
// block arrays beneath them are never written to. That matters because
// bifrostReq.Input is shared with the plugin pipeline and the fallback chain — the
// in-place mutation in the closed PR #6181 is what leaked provider-specific cache
// settings into reused requests.
//
// A caller that supplied any marker of its own is left completely alone. Injection is
// a default for clients that say nothing, never an override of a client that spoke.
func InjectResponsesCacheBreakpoints(cfg *schemas.PromptCacheConfig, input []schemas.ResponsesMessage) []schemas.ResponsesMessage {
	if cfg == nil || len(input) == 0 || responsesHasCacheMarker(input) {
		return input
	}

	targets := responsesInjectionTargets(cfg, input)
	if len(targets) == 0 {
		return input
	}

	marker := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: cfg.TTL}
	out := make([]schemas.ResponsesMessage, len(input))
	copy(out, input)
	copied := make(map[int]bool, len(targets))
	for _, t := range targets {
		if !copied[t.msg] {
			out[t.msg] = schemas.DeepCopyResponsesMessage(input[t.msg])
			copied[t.msg] = true
		}
		msg := &out[t.msg]
		if t.promoteStr {
			// A bare string has nowhere to hang a marker, so it becomes a single
			// text block. Deterministic: the same message always renders the same
			// way, so the cached prefix stays byte-identical across turns.
			text := *msg.Content.ContentStr
			msg.Content.ContentStr = nil
			msg.Content.ContentBlocks = []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: &text,
			}}
			msg.Content.ContentBlocks[0].CacheControl = marker
			continue
		}
		msg.Content.ContentBlocks[t.block].CacheControl = marker
	}
	return out
}

// InjectChatCacheBreakpoints is the Chat Completions parallel of
// InjectResponsesCacheBreakpoints. Same rules, same copy-on-write guarantee.
func InjectChatCacheBreakpoints(cfg *schemas.PromptCacheConfig, input []schemas.ChatMessage) []schemas.ChatMessage {
	if cfg == nil || len(input) == 0 || chatHasCacheMarker(input) {
		return input
	}

	targets := chatInjectionTargets(cfg, input)
	if len(targets) == 0 {
		return input
	}

	marker := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: cfg.TTL}
	out := make([]schemas.ChatMessage, len(input))
	copy(out, input)
	copied := make(map[int]bool, len(targets))
	for _, t := range targets {
		if !copied[t.msg] {
			out[t.msg] = schemas.DeepCopyChatMessage(input[t.msg])
			copied[t.msg] = true
		}
		msg := &out[t.msg]
		if t.promoteStr {
			text := *msg.Content.ContentStr
			msg.Content.ContentStr = nil
			msg.Content.ContentBlocks = []schemas.ChatContentBlock{{
				Type: schemas.ChatContentBlockTypeText,
				Text: &text,
			}}
			msg.Content.ContentBlocks[0].CacheControl = marker
			continue
		}
		msg.Content.ContentBlocks[t.block].CacheControl = marker
	}
	return out
}

// injectionTarget names one place to write a marker. promoteStr means the message
// carries a bare ContentStr that must become a block first, in which case block is
// meaningless.
type injectionTarget struct {
	msg        int
	block      int
	promoteStr bool
}

// responsesHasCacheMarker reports whether the caller already expressed caching
// intent anywhere — either dialect counts, since a request that already carries
// prompt_cache_breakpoint is just as much an explicit statement as cache_control.
func responsesHasCacheMarker(input []schemas.ResponsesMessage) bool {
	for i := range input {
		if input[i].CacheControl != nil {
			return true
		}
		if input[i].Content == nil {
			continue
		}
		for j := range input[i].Content.ContentBlocks {
			b := &input[i].Content.ContentBlocks[j]
			if b.CacheControl != nil || b.PromptCacheBreakpoint != nil {
				return true
			}
		}
	}
	return false
}

func chatHasCacheMarker(input []schemas.ChatMessage) bool {
	for i := range input {
		if input[i].Content == nil {
			continue
		}
		for j := range input[i].Content.ContentBlocks {
			b := &input[i].Content.ContentBlocks[j]
			if b.CacheControl != nil || b.PromptCacheBreakpoint != nil {
				return true
			}
		}
	}
	return false
}

// responsesInjectionTargets resolves the configured strategy into concrete positions.
// InjectionPoints replaces AutoInject rather than adding to it, so an operator who
// configures explicit points gets exactly those and no surprise extra marker.
func responsesInjectionTargets(cfg *schemas.PromptCacheConfig, input []schemas.ResponsesMessage) []injectionTarget {
	if len(cfg.InjectionPoints) > 0 {
		return responsesPointTargets(cfg.InjectionPoints, input)
	}
	if !cfg.AutoInject {
		return nil
	}
	// Default strategy: the FIRST cacheable block. That is the prefix an agent loop
	// replays verbatim every turn, so the cached region stays stable and turn 2
	// onward is a read. Marking the last block instead would move the boundary every
	// turn and bill a write each time — the exact failure #6180 reported.
	for i := range input {
		if input[i].Content == nil {
			continue
		}
		if s := input[i].Content.ContentStr; s != nil && *s != "" {
			return []injectionTarget{{msg: i, promoteStr: true}}
		}
		for j := range input[i].Content.ContentBlocks {
			if isCacheableResponsesBlock(input[i].Content.ContentBlocks[j].Type) {
				return []injectionTarget{{msg: i, block: j}}
			}
		}
	}
	return nil
}

func chatInjectionTargets(cfg *schemas.PromptCacheConfig, input []schemas.ChatMessage) []injectionTarget {
	if len(cfg.InjectionPoints) > 0 {
		return chatPointTargets(cfg.InjectionPoints, input)
	}
	if !cfg.AutoInject {
		return nil
	}
	for i := range input {
		if input[i].Content == nil {
			continue
		}
		if s := input[i].Content.ContentStr; s != nil && *s != "" {
			return []injectionTarget{{msg: i, promoteStr: true}}
		}
		for j := range input[i].Content.ContentBlocks {
			if isCacheableChatBlock(input[i].Content.ContentBlocks[j].Type) {
				return []injectionTarget{{msg: i, block: j}}
			}
		}
	}
	return nil
}

// responsesPointTargets applies LiteLLM's injection-point semantics: match messages
// by role and/or index, and mark the LAST content block of each match. Last rather
// than first here because a point names a message the operator wants cached through
// to its end, unlike the default strategy which names a prefix boundary.
func responsesPointTargets(points []schemas.CacheControlInjectionPoint, input []schemas.ResponsesMessage) []injectionTarget {
	var out []injectionTarget
	seen := make(map[int]bool)
	for _, p := range points {
		for _, idx := range matchMessageIndices(p, len(input), func(i int) string {
			if input[i].Role == nil {
				return ""
			}
			return string(*input[i].Role)
		}) {
			if seen[idx] || len(out) >= MaxInjectedCacheBreakpoints {
				continue
			}
			msg := input[idx]
			if msg.Content == nil {
				continue
			}
			if s := msg.Content.ContentStr; s != nil && *s != "" {
				out = append(out, injectionTarget{msg: idx, promoteStr: true})
				seen[idx] = true
				continue
			}
			if b := lastCacheableResponsesBlock(msg.Content.ContentBlocks); b >= 0 {
				out = append(out, injectionTarget{msg: idx, block: b})
				seen[idx] = true
			}
		}
	}
	return out
}

func chatPointTargets(points []schemas.CacheControlInjectionPoint, input []schemas.ChatMessage) []injectionTarget {
	var out []injectionTarget
	seen := make(map[int]bool)
	for _, p := range points {
		for _, idx := range matchMessageIndices(p, len(input), func(i int) string {
			return string(input[i].Role)
		}) {
			if seen[idx] || len(out) >= MaxInjectedCacheBreakpoints {
				continue
			}
			msg := input[idx]
			if msg.Content == nil {
				continue
			}
			if s := msg.Content.ContentStr; s != nil && *s != "" {
				out = append(out, injectionTarget{msg: idx, promoteStr: true})
				seen[idx] = true
				continue
			}
			if b := lastCacheableChatBlock(msg.Content.ContentBlocks); b >= 0 {
				out = append(out, injectionTarget{msg: idx, block: b})
				seen[idx] = true
			}
		}
	}
	return out
}

// matchMessageIndices resolves one injection point to message positions. A point
// with neither Role nor Index matches nothing: it is almost certainly a
// misconfiguration, and marking every message would burn the whole budget.
//
// Out-of-range indices match nothing rather than clamping. A conversation shorter
// than the configured index is ordinary — early turns of a session that will grow —
// so clamping would silently mark a different message than the operator named.
func matchMessageIndices(p schemas.CacheControlInjectionPoint, n int, roleAt func(int) string) []int {
	if p.Location != "" && p.Location != schemas.CacheControlInjectionLocationMessage {
		return nil
	}
	if p.Index != nil {
		idx := *p.Index
		if idx < 0 {
			idx += n
		}
		if idx < 0 || idx >= n {
			return nil
		}
		if p.Role != nil && roleAt(idx) != *p.Role {
			return nil
		}
		return []int{idx}
	}
	if p.Role == nil {
		return nil
	}
	var out []int
	for i := 0; i < n; i++ {
		if roleAt(i) == *p.Role {
			out = append(out, i)
		}
	}
	return out
}

// isCacheableResponsesBlock reports whether a marker on this block type means
// anything upstream. Text, image and file blocks carry prompt content and count
// toward the cached prefix; reasoning, refusal and tool-call plumbing do not.
func isCacheableResponsesBlock(t schemas.ResponsesMessageContentBlockType) bool {
	switch t {
	case schemas.ResponsesInputMessageContentBlockTypeText,
		schemas.ResponsesInputMessageContentBlockTypeImage,
		schemas.ResponsesInputMessageContentBlockTypeFile,
		schemas.ResponsesOutputMessageContentTypeText:
		return true
	default:
		return false
	}
}

func isCacheableChatBlock(t schemas.ChatContentBlockType) bool {
	switch t {
	case schemas.ChatContentBlockTypeText, schemas.ChatContentBlockTypeImage:
		return true
	default:
		return false
	}
}

func lastCacheableResponsesBlock(blocks []schemas.ResponsesMessageContentBlock) int {
	for i := len(blocks) - 1; i >= 0; i-- {
		if isCacheableResponsesBlock(blocks[i].Type) {
			return i
		}
	}
	return -1
}

func lastCacheableChatBlock(blocks []schemas.ChatContentBlock) int {
	for i := len(blocks) - 1; i >= 0; i-- {
		if isCacheableChatBlock(blocks[i].Type) {
			return i
		}
	}
	return -1
}
