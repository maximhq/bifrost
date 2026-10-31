package utils

import (
	"fmt"
	"math/bits"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// knownAnthropicMaxOutputTokens provides static fallback defaults for Claude models
// when both cache and DB miss handler return nothing. Only Anthropic requires max_tokens.
var knownAnthropicMaxOutputTokens = map[string]int{
	"claude-opus-5":     128000,
	"claude-mythos":     128000,
	"claude-fable-5":    128000,
	"claude-opus-4-8":   128000,
	"claude-opus-4-7":   128000,
	"claude-opus-4-6":   128000,
	"claude-sonnet-5":   128000,
	"claude-sonnet-4-6": 128000,
	"claude-haiku-4-5":  64000,
	"claude-sonnet-4-5": 64000,
	"claude-opus-4-5":   64000,
	"claude-opus-4-1":   32000,
	"claude-sonnet-4":   64000,
	"claude-opus-4":     32000,
	"claude-sonnet-4-0": 64000,
	"claude-opus-4-0":   32000,
	"claude-3-5-sonnet": 8192,
	"claude-3-5-haiku":  8192,
	"claude-3-7-sonnet": 8192,
	"claude-3-opus":     4096,
	"claude-3-sonnet":   4096,
	"claude-3-haiku":    4096,
}

// SetCapabilityResolver installs the lookup backing CapabilitiesFor.
// The hook lives in schemas, next to the capability record it resolves.
func SetCapabilityResolver(fn func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities) {
	schemas.SetCapabilityResolver(fn)
}

// CapabilitiesFor returns the capability record for a (provider, model) pair, or
// nil when no resolver is installed or the catalog has no record.
func CapabilitiesFor(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
	return schemas.CapabilitiesFor(provider, model)
}

// GetMaxOutputTokensOrDefault returns the (provider, model)'s max_output_tokens
// from the capability record, or the provided default on miss. Claude falls back
// to the static table first, since Anthropic rejects a request without
// max_tokens and a cold or incomplete datasheet must not cap a model at the
// caller's generic default.
func GetMaxOutputTokensOrDefault(provider schemas.ModelProvider, model string, defaultValue int) int {
	if caps := CapabilitiesFor(provider, model); caps != nil && caps.MaxOutputTokens != nil {
		return *caps.MaxOutputTokens
	}
	if m := KnownClaudeMaxOutputTokens(model); m > 0 {
		return m
	}
	return defaultValue
}

// KnownClaudeMaxOutputTokens returns the static max_output_tokens for a Claude model, or 0 when the table has none.
func KnownClaudeMaxOutputTokens(model string) int {
	if !strings.Contains(model, "claude") {
		return 0
	}
	return knownAnthropicMaxOutputTokens[normalizeClaudeModelName(model)]
}

// claudeMinThinkingBudget is Anthropic's floor on thinking.budget_tokens.
const claudeMinThinkingBudget = 1024

// geminiBudgetRanges are the published thinking-budget limits. Longest prefix
// first, since "gemini-2.5-flash" also prefixes "gemini-2.5-flash-lite".
var geminiBudgetRanges = []struct {
	prefix   string
	min, max int
}{
	{"gemini-2.5-flash-lite", 512, 24576},
	{"gemini-2.5-pro", 128, 32768},
	{"gemini-2.5-flash", 0, 24576},
}

// KnownGeminiReasoningBudget returns the published Gemini thinking-budget range, or nil when the model publishes none.
func KnownGeminiReasoningBudget(model string) *schemas.BudgetControl {
	modelLower := strings.ToLower(model)
	for _, entry := range geminiBudgetRanges {
		if strings.Contains(modelLower, entry.prefix) {
			return &schemas.BudgetControl{Min: new(entry.min), Max: new(entry.max)}
		}
	}
	return nil
}

// knownReasoningBudget returns the static thinking-budget range for a model whose datasheet row publishes none.
func knownReasoningBudget(model string) *schemas.BudgetControl {
	if strings.Contains(model, "claude") {
		return &schemas.BudgetControl{Min: new(claudeMinThinkingBudget)}
	}
	return KnownGeminiReasoningBudget(model)
}

// FitOutputTokens fits a sent output cap and thinking budget into the model's limits, returning one "param: from -> to" entry per change.
// defaultMax is the output limit used when caps has none; the Claude table backs it up.
func FitOutputTokens(caps schemas.ModelCaps, defaultMax int, maxParam string, maxTokens, budget *int) (*int, *int, []string) {
	var changes []string
	if defaultMax <= 0 {
		defaultMax = KnownClaudeMaxOutputTokens(caps.Model())
	}
	ceiling := caps.MaxOutputTokens(defaultMax)
	// limit is the cap a budget must stay under; zero when the caller's own cap stands.
	limit := ceiling
	lowered := false
	fittedMax := maxTokens
	if maxTokens != nil {
		limit = 0
		v := *maxTokens
		if ceiling > 0 && v > ceiling {
			v, limit, lowered = ceiling, ceiling, true
		} else if floor := caps.MinOutputTokens(0); floor > 0 && v < floor {
			v = floor
		}
		if v != *maxTokens {
			changes = append(changes, fmt.Sprintf("%s: %d -> %d", maxParam, *maxTokens, v))
			fittedMax = new(v)
		}
	}
	if budget == nil || *budget <= 0 {
		return fittedMax, budget, changes
	}
	b := *budget
	if lowered {
		// Scale with the cap so the budget keeps the share of output the caller gave it.
		// 128-bit product so a huge budget cannot overflow; the quotient is below b, so it fits.
		hiProd, loProd := bits.Mul64(uint64(b), uint64(limit))
		scaled, _ := bits.Div64(hiProd, loProd, uint64(*maxTokens))
		b = max(1, int(scaled))
	}
	lo, hi, ranged := caps.ReasoningBudgetRange(0, knownReasoningBudget(caps.Model()))
	if ranged {
		if hi > 0 && b > hi {
			b = hi
		}
		b = max(b, lo)
	}
	if limit > 1 && b >= limit && (!ranged || limit-1 >= lo) {
		b = limit - 1
	}
	if b != *budget {
		changes = append(changes, fmt.Sprintf("reasoning.max_tokens: %d -> %d", *budget, b))
		budget = new(b)
	}
	return fittedMax, budget, changes
}

// IsVertexMultiRegionOnlyModel reports whether the given model is flagged in the
// datasheet as only available on Google Vertex multi-region pool endpoints
// (aiplatform.{region}.rep.googleapis.com). Returns false when the flag is not set.
func IsVertexMultiRegionOnlyModel(model string) bool {
	if caps := CapabilitiesFor(schemas.Vertex, model); caps != nil && caps.IsVertexMultiRegionOnly != nil {
		return *caps.IsVertexMultiRegionOnly
	}
	return false
}

// normalizeClaudeModelName extracts the base Claude model name from
// provider-specific model ID formats.
//
// Examples:
//
//	"claude-sonnet-4-20250514"                     → "claude-sonnet-4"
//	"anthropic.claude-sonnet-4-20250514-v1:0"      → "claude-sonnet-4"
//	"us.anthropic.claude-sonnet-4-20250514-v1:0"   → "claude-sonnet-4"
//	"claude-3-5-sonnet-20241022"                   → "claude-3-5-sonnet"
func normalizeClaudeModelName(model string) string {
	// Strip region + provider prefixes (us.anthropic., anthropic., etc.)
	if idx := strings.LastIndex(model, "."); idx >= 0 {
		model = model[idx+1:]
	}
	// Strip "@version" alias marker (Vertex/Bedrock, e.g. "...-4-5@20251001")
	if idx := strings.Index(model, "@"); idx >= 0 {
		model = model[:idx]
	}
	// Strip Bedrock version suffix (":0", ":1", etc.) and the preceding "-v1"/"-v2"
	if idx := strings.Index(model, ":"); idx >= 0 {
		model = model[:idx]
		if len(model) >= 3 {
			suffix := model[len(model)-3:]
			if suffix == "-v1" || suffix == "-v2" {
				model = model[:len(model)-3]
			}
		}
	}
	// Strip "-v1", "-v2" even without colon (e.g., "anthropic.claude-opus-4-6-v1")
	if strings.HasSuffix(model, "-v1") || strings.HasSuffix(model, "-v2") {
		model = model[:len(model)-3]
	}
	// Strip date version suffix using schemas.BaseModelName
	return schemas.BaseModelName(model)
}
