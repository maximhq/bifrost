package generic

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// Optimistic paths tried, in order, when the mapping leaves a field unset. They cover the
// OpenAI and Anthropic shapes most APIs borrow. Cost is only read from a usage block: a root
// "cost" is as likely to be a product price as a bill.
var (
	optimisticInputTokenPaths  = []string{"usage.prompt_tokens", "usage.input_tokens"}
	optimisticOutputTokenPaths = []string{"usage.completion_tokens", "usage.output_tokens"}
	optimisticTotalTokenPaths  = []string{"usage.total_tokens"}
	optimisticCostPaths        = []string{"usage.cost"}
)

// isJSONContentType reports whether a body is worth scanning for usage. Any JSON-derived media
// type counts, including application/x-amz-json-1.1 (Textract, Translate) and +json suffixes.
func isJSONContentType(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.Contains(strings.ToLower(mediaType), "json")
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

// effectiveModelPath is the request body path the model is read from: the mapping's, else the
// optimistic default, else nothing when optimistic parsing is off.
func (u resolvedUsage) effectiveModelPath() string {
	if u.modelPath != "" {
		return u.modelPath
	}
	if u.optimistic {
		return "model"
	}
	return ""
}

// firstNumber reads the first non-negative number found at paths. Negative values are treated
// as absent: a count below zero is never a measurement.
func firstNumber(body []byte, paths []string) (float64, bool) {
	for _, p := range paths {
		if v := providerUtils.GetJSONField(body, p); v.Exists() && v.Type == gjson.Number && v.Float() >= 0 {
			return v.Float(), true
		}
	}
	return 0, false
}

// firstCost reads a cost that is either a number or an array of numbers (summed), ignoring
// negative entries.
func firstCost(body []byte, paths []string) (float64, bool) {
	for _, p := range paths {
		v := providerUtils.GetJSONField(body, p)
		if !v.Exists() {
			continue
		}
		if v.Type == gjson.Number {
			if v.Float() < 0 {
				continue
			}
			return v.Float(), true
		}
		if v.IsArray() {
			var total float64
			found := false
			for _, item := range v.Array() {
				if item.Type == gjson.Number && item.Float() >= 0 {
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

// usageFields is what one body (or one stream event) said about usage. Presence is tracked per
// field so events that each carry part of the picture can be merged.
type usageFields struct {
	input, output, total, cost, units                float64
	hasInput, hasOutput, hasTotal, hasCost, hasUnits bool
}

func (f usageFields) any() bool {
	return f.hasInput || f.hasOutput || f.hasTotal || f.hasCost || f.hasUnits
}

// billsUnits reports whether the mapping prices a quantity.
func (u resolvedUsage) billsUnits() bool {
	return u.costPerUnit > 0 && (u.unitsPath != "" || u.unitsMeasure == schemas.GenericUnitsBodyBytes)
}

// measureUnits reads the billable quantity from a body per the mapping's measure.
func (u resolvedUsage) measureUnits(contentType string, body []byte) (float64, bool) {
	switch u.unitsMeasure {
	case schemas.GenericUnitsBodyBytes:
		return float64(len(body)), len(body) > 0
	case schemas.GenericUnitsChars:
		if !(isJSONContentType(contentType) || (contentType == "" && gjson.ValidBytes(body))) {
			return 0, false
		}
		if v := providerUtils.GetJSONField(body, u.unitsPath); v.Exists() && v.Type == gjson.String {
			return float64(len([]rune(v.Str))), true
		}
		return 0, false
	default:
		if !(isJSONContentType(contentType) || (contentType == "" && gjson.ValidBytes(body))) {
			return 0, false
		}
		return firstNumber(body, []string{u.unitsPath})
	}
}

// requestUnits reads the quantity from the caller's body when the mapping bills request-side units
// (Translate text, TTS markup). It is known before the upstream answers, so it seeds both the
// buffered and the streaming paths.
func (u resolvedUsage) requestUnits(contentType string, body []byte) usageFields {
	var f usageFields
	if u.billsUnits() && u.unitsSource == schemas.GenericUnitsFromRequest {
		f.units, f.hasUnits = u.measureUnits(contentType, body)
	}
	return f
}

// merge overlays the fields o carries onto f.
func (f *usageFields) merge(o usageFields) {
	if o.hasInput {
		f.input, f.hasInput = o.input, true
	}
	if o.hasOutput {
		f.output, f.hasOutput = o.output, true
	}
	if o.hasTotal {
		f.total, f.hasTotal = o.total, true
	}
	if o.hasCost {
		f.cost, f.hasCost = o.cost, true
	}
	if o.hasUnits {
		f.units, f.hasUnits = o.units, true
	}
}

// parse reads the mapped fields from a response body (or one stream event). Non-JSON bodies yield
// nothing, except a response-side body_bytes quantity.
func (u resolvedUsage) parse(contentType string, body []byte) usageFields {
	var f usageFields
	if len(body) == 0 {
		return f
	}
	if u.billsUnits() && u.unitsSource == schemas.GenericUnitsFromResponse {
		f.units, f.hasUnits = u.measureUnits(contentType, body)
	}
	if !(isJSONContentType(contentType) || (contentType == "" && gjson.ValidBytes(body))) {
		return f
	}
	f.input, f.hasInput = firstNumber(body, u.tokenPaths(u.inputTokensPath, optimisticInputTokenPaths))
	f.output, f.hasOutput = firstNumber(body, u.tokenPaths(u.outputTokensPath, optimisticOutputTokenPaths))
	f.total, f.hasTotal = firstNumber(body, u.tokenPaths(u.totalTokensPath, optimisticTotalTokenPaths))
	f.cost, f.hasCost = firstCost(body, u.costPaths())
	return f
}

// build turns parsed fields into the usage a billable call reports. It never returns nil: an
// empty usage still counts as one request. The flat per-request fee is folded into Cost so the
// pricing path sees one provider-reported number.
func (u resolvedUsage) build(f usageFields) *schemas.BifrostPassthroughUsage {
	usage := &schemas.BifrostPassthroughUsage{}
	total, hasTotal := f.total, f.hasTotal
	if !hasTotal && (f.hasInput || f.hasOutput) {
		total, hasTotal = f.input+f.output, true
	}
	if hasTotal && total > 0 {
		usage.LLMUsage = &schemas.BifrostLLMUsage{
			PromptTokens:     int(f.input),
			CompletionTokens: int(f.output),
			TotalTokens:      int(total),
		}
	}
	cost := u.costPerRequest
	if f.hasCost {
		cost += f.cost
	}
	if f.hasUnits && u.costPerUnit > 0 {
		cost += f.units * u.costPerUnit
	}
	if cost > 0 {
		usage.Cost = &schemas.BifrostCost{TotalCost: cost}
	}
	return usage
}

// extractUsage reads tokens, cost and a response-side quantity from a buffered response body and
// merges them over whatever the request already established (request-side units).
func extractUsage(u resolvedUsage, contentType string, body []byte, initial usageFields) *schemas.BifrostPassthroughUsage {
	f := initial
	f.merge(u.parse(contentType, body))
	return u.build(f)
}

// hasUsageEvent is the cheap gate for streams: only events that carry one of the mapped (or
// optimistic) fields are fully parsed.
func (u resolvedUsage) hasUsageEvent(event []byte) bool {
	groups := [][]string{
		u.tokenPaths(u.totalTokensPath, optimisticTotalTokenPaths),
		u.tokenPaths(u.inputTokensPath, optimisticInputTokenPaths),
		u.tokenPaths(u.outputTokensPath, optimisticOutputTokenPaths),
		u.costPaths(),
	}
	if u.billsUnits() && u.unitsSource == schemas.GenericUnitsFromResponse && u.unitsPath != "" {
		groups = append(groups, []string{u.unitsPath})
	}
	for _, group := range groups {
		for _, p := range group {
			if providerUtils.GetJSONField(event, p).Exists() {
				return true
			}
		}
	}
	return false
}

// streamObserver returns the per-event observer for one stream. Fields accumulate across events,
// so a stream that reports input tokens on its first event and output tokens on its last (the
// Anthropic shape) is billed for both; an event that carries nothing leaves what was seen alone.
// initial carries what the request already established (request-side units).
func (u resolvedUsage) streamObserver(initial usageFields) func(event []byte) *schemas.BifrostPassthroughUsage {
	acc := initial
	return func(event []byte) *schemas.BifrostPassthroughUsage {
		f := u.parse("application/json", event)
		if !f.any() {
			return nil
		}
		acc.merge(f)
		return u.build(acc)
	}
}

// defaultUsage is what a billable call reports when nothing could be read from the response:
// the flat fee plus any request-side units.
func (u resolvedUsage) defaultUsage(initial usageFields) *schemas.BifrostPassthroughUsage {
	return u.build(initial)
}
