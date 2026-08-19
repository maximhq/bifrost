package schemas

import (
	"slices"
)

// Reasoning effort labels, ascending. "none" disables reasoning entirely.
const (
	ReasoningEffortNone    = "none"
	ReasoningEffortMinimal = "minimal"
	ReasoningEffortLow     = "low"
	ReasoningEffortMedium  = "medium"
	ReasoningEffortHigh    = "high"
	ReasoningEffortXHigh   = "xhigh"
	ReasoningEffortMax     = "max"
)

// DefaultReasoningBudgetMin is the floor used for models that take a token
// budget but publish no explicit minimum.
const DefaultReasoningBudgetMin = 1024

// baseEffortLevels are accepted by every reasoning model, and answer when
// neither the row nor the caller publishes a ladder.
var baseEffortLevels = []string{ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh}

// effortLadder orders the effort labels for downgrade resolution. "none" is
// absent on purpose: it turns reasoning off rather than lowering it, so it must
// never be reached by walking down from a real effort.
var effortLadder = []string{
	ReasoningEffortMinimal, ReasoningEffortLow, ReasoningEffortMedium,
	ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax,
}

// SupportsReasoning reports whether the model reasons at all.
func (c ModelCaps) SupportsReasoning(fallback bool) bool {
	if c.record != nil && c.record.SupportsReasoning != nil {
		return *c.record.SupportsReasoning
	}
	return fallback
}

// ReasoningEffortLevels returns the effort labels the model accepts, ascending.
// Prefers the row's ladder, then the caller's name-based fallback, then the
// base set.
func (c ModelCaps) ReasoningEffortLevels(fallback *EffortControl) []string {
	if c.record != nil && len(c.record.ReasoningEffortLevels) > 0 {
		return c.record.ReasoningEffortLevels
	}
	if fallback != nil && len(fallback.Levels) > 0 {
		return fallback.Levels
	}
	return slices.Clone(baseEffortLevels)
}

// SupportsAdaptiveThinking returns true if the model supports thinking.type: "adaptive".
// Currently supported on Claude Opus 4.6, Claude Sonnet 4.6, Claude Sonnet 5+, Claude
// Opus 4.7+, and the Claude Fable/Mythos family. On Opus 4.7+, Sonnet 5+, and
// Fable/Mythos adaptive is the only thinking-on mode; on Opus 4.6 and Sonnet 4.6 it
// coexists with the deprecated budget_tokens-based extended thinking. On Fable/Mythos
// adaptive is always on and thinking:{type:"disabled"} is rejected (see IsFableFamily).
//
// Prefers the datasheet's supports_adaptive_thinking boolean, falling back to
// substring detection on the bare model name.
func (c ModelCaps) SupportsAdaptiveThinking(fallback bool) bool {
	if c.record != nil && c.record.SupportsAdaptiveThinking != nil {
		return *c.record.SupportsAdaptiveThinking
	}
	return fallback
}

// AdaptiveOnlyThinking returns true for models where budget_tokens extended
// thinking is removed (adaptive is the only thinking-on mode) and
// temperature/top_p/top_k are rejected with a 400. Covers Opus 4.7+, Sonnet 5+,
// and the Fable/Mythos family.
//
// The datasheet flags these models with supports_sampling_params == false (the
// sampling knobs and budget_tokens are removed together on the same models), so
// this reads !supports_sampling_params; falls back to substring detection.
func (c ModelCaps) AdaptiveOnlyThinking(fallback bool) bool {
	if c.record != nil && c.record.SupportsSamplingParams != nil {
		return !*c.record.SupportsSamplingParams
	}
	return fallback
}

// SupportsReasoningContentBlocks reports whether the model carries replayed
// reasoning text as content blocks rather than summary[] entries. gpt-oss reads
// content blocks; the o-series and GPT-5 read summaries. Drives both the shape
// sent on replay and which reasoning-only messages are worth sending at all.
//
// Prefers the datasheet's supports_reasoning_content_blocks boolean, falling
// back to name detection.
func (c ModelCaps) SupportsReasoningContentBlocks(fallback bool) bool {
	if c.record != nil && c.record.SupportsReasoningContentBlocks != nil {
		return *c.record.SupportsReasoningContentBlocks
	}
	return fallback
}

// SupportsReasoningEffort reports whether the model takes a categorical effort
// label on the wire (Gemini's thinkingLevel, OpenAI's reasoning.effort). Models
// exposing only a numeric budget need the effort converted before sending.
//
// Prefers the row's explicit flag, then treats a published effort ladder as
// asserting the surface exists; fallback answers when the row is silent.
func (c ModelCaps) SupportsReasoningEffort(fallback bool) bool {
	if c.record == nil {
		return fallback
	}
	if c.record.SupportsReasoningEffort != nil {
		return *c.record.SupportsReasoningEffort
	}
	if len(c.record.ReasoningEffortLevels) > 0 {
		return true
	}
	return fallback
}

// NormalizeReasoningEffort maps a requested effort onto one the model accepts,
// downgrading to the nearest supported level when the exact label is rejected.
func (c ModelCaps) NormalizeReasoningEffort(effort string, fallback *EffortControl) string {
	var renames map[string]string
	switch {
	case c.record != nil && c.record.ReasoningEffortRenames != nil:
		renames = c.record.ReasoningEffortRenames
	case fallback != nil:
		renames = fallback.Renames
	}
	if renamed, ok := renames[effort]; ok {
		return renamed
	}
	levels := c.ReasoningEffortLevels(fallback)
	if slices.Contains(levels, effort) {
		return effort
	}
	// Snap onto the nearest rung the model actually publishes. Hard-coding
	// "high"/"low" as targets breaks on a ladder that omits them — ["medium",
	// "high"] would take a requested "minimal" down to a rejected "low".
	//
	// Ties break upward, so a clamp never silently spends less reasoning than
	// the caller asked for: on ["low","high"], a requested "medium" resolves to
	// "high" rather than "low".
	rank := slices.Index(effortLadder, effort)
	if rank < 0 {
		return effort
	}
	best, bestDistance := "", 0
	for _, candidate := range levels {
		idx := slices.Index(effortLadder, candidate)
		if idx < 0 {
			continue
		}
		distance := idx - rank
		if distance < 0 {
			distance = -distance
		}
		if best == "" || distance < bestDistance || (distance == bestDistance && idx > rank) {
			best, bestDistance = candidate, distance
		}
	}
	if best == "" {
		return effort
	}
	return best
}

// ReasoningBudgetRange returns the model's allowed thinking-budget range. ok is
// false when no range is known, in which case callers should skip validation
// rather than enforce the returned defaults.
func (c ModelCaps) ReasoningBudgetRange(defaultMax int, fallback *BudgetControl) (min, max int, ok bool) {
	var b *BudgetControl
	if c.record != nil {
		b = c.record.ReasoningBudget
	}
	if b == nil || (b.Min == nil && b.Max == nil) {
		b = fallback
	}
	if b != nil && (b.Min != nil || b.Max != nil) {
		min, max = DefaultReasoningBudgetMin, defaultMax
		if b.Min != nil {
			min = *b.Min
		}
		if b.Max != nil {
			max = *b.Max
		}
		return min, max, true
	}
	return DefaultReasoningBudgetMin, defaultMax, false
}

// CanDisableReasoning reports whether the model accepts an explicit "off".
// Models that reject it need the thinking config omitted entirely.
func (c ModelCaps) CanDisableReasoning(fallback bool) bool {
	if c.record != nil && c.record.SupportsReasoningDisable != nil {
		return *c.record.SupportsReasoningDisable
	}
	return fallback
}

// SupportsDynamicReasoningBudget reports whether the model accepts a
// self-managed thinking budget (Gemini's thinkingBudget: -1) rather than an
// explicit token count.
func (c ModelCaps) SupportsDynamicReasoningBudget(fallback bool) bool {
	if c.record != nil && c.record.SupportsDynamicReasoningBudget != nil {
		return *c.record.SupportsDynamicReasoningBudget
	}
	return fallback
}

// ---- Name-based fallbacks, relocated verbatim from the provider packages ----
