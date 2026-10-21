package generic

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// Optimistic paths tried, in order, when the mapping leaves a field unset. They cover the
// OpenAI, Anthropic and "cost in the body" shapes most APIs borrow.
var (
	optimisticInputTokenPaths  = []string{"usage.prompt_tokens", "usage.input_tokens"}
	optimisticOutputTokenPaths = []string{"usage.completion_tokens", "usage.output_tokens"}
	optimisticTotalTokenPaths  = []string{"usage.total_tokens"}
	optimisticCostPaths        = []string{"usage.cost", "cost"}
)

// isJSONContentType reports whether a response body is worth scanning for usage.
func isJSONContentType(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// tokensConfigured reports whether the mapping names any token path. Once it does, the token
// fields are read from the mapping alone: a half-mapped response must not be topped up from a
// usage block the operator chose not to trust.
func (u resolvedUsage) tokensConfigured() bool {
	return u.inputTokensPath != "" || u.outputTokensPath != "" || u.totalTokensPath != ""
}

// tokenPaths returns the configured path for a token field, or the optimistic candidates when the
// mapping names no token path at all.
func (u resolvedUsage) tokenPaths(configured string, optimistic []string) []string {
	if configured != "" {
		return []string{configured}
	}
	if u.optimistic && !u.tokensConfigured() {
		return optimistic
	}
	return nil
}

// costPaths returns the configured cost path or the optimistic candidates.
func (u resolvedUsage) costPaths() []string {
	if u.costPath != "" {
		return []string{u.costPath}
	}
	if u.optimistic {
		return optimisticCostPaths
	}
	return nil
}

func firstNumber(body []byte, paths []string) (float64, bool) {
	for _, p := range paths {
		if v := providerUtils.GetJSONField(body, p); v.Exists() && v.Type == gjson.Number {
			return v.Float(), true
		}
	}
	return 0, false
}

// firstCost reads a cost that is either a number or an array of numbers (summed).
func firstCost(body []byte, paths []string) (float64, bool) {
	for _, p := range paths {
		v := providerUtils.GetJSONField(body, p)
		if !v.Exists() {
			continue
		}
		if v.Type == gjson.Number {
			return v.Float(), true
		}
		if v.IsArray() {
			var total float64
			found := false
			for _, item := range v.Array() {
				if item.Type == gjson.Number {
					total += item.Float()
					found = true
				}
			}
			if found {
				return total, true
			}
		}
	}
	return 0, false
}

// extractUsage reads tokens and cost from a JSON response body per the mapping and returns the
// usage a billable call reports. It never returns nil: an empty usage still counts as one request.
// The flat per-request fee is folded into Cost so the pricing path sees one provider-reported number.
func extractUsage(u resolvedUsage, contentType string, body []byte) *schemas.BifrostPassthroughUsage {
	usage := &schemas.BifrostPassthroughUsage{}
	var cost float64
	if len(body) > 0 && (isJSONContentType(contentType) || (contentType == "" && gjson.ValidBytes(body))) {
		input, hasInput := firstNumber(body, u.tokenPaths(u.inputTokensPath, optimisticInputTokenPaths))
		output, hasOutput := firstNumber(body, u.tokenPaths(u.outputTokensPath, optimisticOutputTokenPaths))
		total, hasTotal := firstNumber(body, u.tokenPaths(u.totalTokensPath, optimisticTotalTokenPaths))
		if !hasTotal && (hasInput || hasOutput) {
			total, hasTotal = input+output, true
		}
		if hasTotal && total > 0 {
			usage.LLMUsage = &schemas.BifrostLLMUsage{
				PromptTokens:     int(input),
				CompletionTokens: int(output),
				TotalTokens:      int(total),
			}
		}
		if c, ok := firstCost(body, u.costPaths()); ok {
			cost += c
		}
	}
	cost += u.costPerRequest
	if cost > 0 {
		usage.Cost = &schemas.BifrostCost{TotalCost: cost}
	}
	return usage
}

// hasUsageEvent is the cheap gate for streams: only events that carry one of the mapped (or
// optimistic) fields are fully parsed.
func (u resolvedUsage) hasUsageEvent(event []byte) bool {
	for _, group := range [][]string{
		u.tokenPaths(u.totalTokensPath, optimisticTotalTokenPaths),
		u.tokenPaths(u.inputTokensPath, optimisticInputTokenPaths),
		u.costPaths(),
	} {
		for _, p := range group {
			if providerUtils.GetJSONField(event, p).Exists() {
				return true
			}
		}
	}
	return false
}

// observeUsage is the per-event stream observer: it reports usage only when the event actually
// carried tokens or a cost, so a later content-only event never clears what was seen.
func (u resolvedUsage) observeUsage(event []byte) *schemas.BifrostPassthroughUsage {
	usage := extractUsage(u, "application/json", event)
	if usage.LLMUsage == nil && (usage.Cost == nil || usage.Cost.TotalCost <= u.costPerRequest) {
		return nil
	}
	return usage
}

// defaultUsage is what a billable call reports when nothing could be read from the response.
func (u resolvedUsage) defaultUsage() *schemas.BifrostPassthroughUsage {
	usage := &schemas.BifrostPassthroughUsage{}
	if u.costPerRequest > 0 {
		usage.Cost = &schemas.BifrostCost{TotalCost: u.costPerRequest}
	}
	return usage
}
