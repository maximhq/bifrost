package datasheet

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// Azure GPT-5.6 cache writes are billed at 1.25× the matching input rate.
const gpt56CacheWriteMultiplier = 1.25

// Upstream Azure GPT-5.6 rows omit cache-write; Azure bills those at 1.25× input.
func fillAzureGPT56CacheCreation(pricingData map[string]Entry) {
	for key, entry := range pricingData {
		if !isAzureGPT56(key, entry) {
			continue
		}
		fillGPT56CacheCreation(&entry)
		pricingData[key] = entry
	}
}

func isAzureGPT56(modelKey string, entry Entry) bool {
	if normalizeProvider(entry.Provider) != string(schemas.Azure) {
		return false
	}
	return strings.Contains(strings.ToLower(modelKey), "gpt-5.6")
}

func fillGPT56CacheCreation(entry *Entry) {
	scaleIfNil(&entry.CacheCreationInputTokenCost, entry.InputCostPerToken)
	scaleIfNil(&entry.CacheCreationInputTokenCostAbove272kTokens, entry.InputCostPerTokenAbove272kTokens)
	scaleIfNil(&entry.CacheCreationInputTokenCostPriority, entry.InputCostPerTokenPriority)
	scaleIfNil(&entry.CacheCreationInputTokenCostFlex, entry.InputCostPerTokenFlex)
	scaleIfNil(&entry.CacheCreationInputTokenCostFlexAbove272kTokens, entry.InputCostPerTokenFlexAbove272kTokens)
}

func scaleIfNil(dst **float64, src *float64) {
	if dst == nil || *dst != nil || src == nil {
		return
	}
	v := *src * gpt56CacheWriteMultiplier
	*dst = &v
}
