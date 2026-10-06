package kiro

import (
	"regexp"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// kiroModels is the static model catalog. Kiro has no public discovery endpoint that works for
// every account type, and OpenCodex ships the same fixed list, so ListModels serves this.
// "kiro-auto" is the Kiro router; it is sent on the wire as "auto".
var kiroModels = []string{
	"kiro-auto",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-6-sol",
	"gpt-6-luna",
	"gpt-6.1-sol",
	"claude-sonnet-5.5",
	"claude-sonnet-5",
	"claude-opus-5.5",
	"claude-opus-5",
	"claude-opus-4.8",
	"claude-opus-4.7",
	"claude-opus-4.6",
	"claude-opus-4.5",
	"claude-sonnet-4.6",
	"claude-sonnet-4.5",
	"claude-sonnet-4.0",
	"claude-haiku-4.5",
	"deepseek-3.2",
	"minimax-m2.5",
	"minimax-m2.1",
	"glm-5",
	"qwen3-coder-next",
}

// kiroContextWindows maps normalized wire model ids to their context window. "auto" has none.
var kiroContextWindows = map[string]int{
	"gpt-5.6-sol":       1_000_000,
	"gpt-5.6-terra":     1_000_000,
	"gpt-5.6-luna":      1_000_000,
	"gpt-6-sol":         272_000,
	"gpt-6-luna":        272_000,
	"gpt-6.1-sol":       272_000,
	"claude-sonnet-5.5": 1_000_000,
	"claude-sonnet-5":   1_000_000,
	"claude-opus-5.5":   1_000_000,
	"claude-opus-5":     1_000_000,
	"claude-opus-4.8":   1_000_000,
	"claude-opus-4.7":   1_000_000,
	"claude-opus-4.6":   1_000_000,
	"claude-sonnet-4.6": 1_000_000,
	"claude-opus-4.5":   200_000,
	"claude-sonnet-4.5": 200_000,
	"claude-sonnet-4.0": 200_000,
	"claude-haiku-4.5":  200_000,
	"deepseek-3.2":      128_000,
	"minimax-m2.5":      200_000,
	"minimax-m2.1":      200_000,
	"glm-5":             200_000,
	"qwen3-coder-next":  256_000,
}

// kiroNativeEffortFields lists the models that take a verified native reasoning-effort field in
// additionalModelRequestFields, and the name of that field. Every other model receives emulated
// thinking instructions instead.
var kiroNativeEffortFields = map[string]string{
	"gpt-5.6-sol":     "reasoning",
	"gpt-5.6-terra":   "reasoning",
	"gpt-5.6-luna":    "reasoning",
	"gpt-6-sol":       "reasoning",
	"gpt-6-luna":      "reasoning",
	"gpt-6.1-sol":     "reasoning",
	"claude-opus-5":   "output_config",
	"claude-opus-5.5": "output_config",
}

// kiroNativeEfforts are the effort values the native field accepts.
var kiroNativeEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// kiroLunaTerraNativeEfforts are the only rungs verified natively for luna/terra; xhigh keeps
// the emulated path there.
var kiroLunaTerraNativeEfforts = map[string]bool{"low": true, "medium": true, "high": true, "max": true}

var (
	dateSuffixPattern    = regexp.MustCompile(`-\d{8}$`)
	effortSuffixPattern  = regexp.MustCompile(`-(low|medium|high|xhigh|max)$`)
	dashedVersionPattern = regexp.MustCompile(`(\d+)-(\d+)`)
	claudeLegacyPattern  = regexp.MustCompile(`^claude-([\d.]+)-(sonnet|opus|haiku)$`)
)

// normalizeKiroModelId maps a user-facing model id to the CodeWhisperer wire model id:
// lowercase, no kiro/ or kiro- prefix, no date or effort suffix, dot versions, and the
// claude-<family>-<version> word order. "kiro-auto" becomes "auto".
func normalizeKiroModelId(id string) string {
	model := strings.ToLower(strings.TrimSpace(id))
	model = strings.TrimPrefix(model, "kiro/")
	model = strings.TrimPrefix(model, "kiro-")
	if model == "auto" {
		return "auto"
	}
	model = dateSuffixPattern.ReplaceAllString(model, "")
	model = effortSuffixPattern.ReplaceAllString(model, "")
	model = dashedVersionPattern.ReplaceAllString(model, "$1.$2")
	model = claudeLegacyPattern.ReplaceAllString(model, "claude-$2-$1")
	return model
}

// kiroContextWindow returns the context window for a model id, or 0 when unknown.
func kiroContextWindow(model string) int {
	return kiroContextWindows[normalizeKiroModelId(model)]
}

// kiroNativeEffortField returns the native effort field for the model and effort, or "" when the
// model needs emulated thinking for that effort.
func kiroNativeEffortField(model, effort string) string {
	normalized := normalizeKiroModelId(model)
	if (normalized == "gpt-5.6-luna" || normalized == "gpt-5.6-terra") && effort != "" && !kiroLunaTerraNativeEfforts[effort] {
		return ""
	}
	return kiroNativeEffortFields[normalized]
}

// listModelsForKey serves the static catalog filtered through the key's allow/deny lists and
// aliases, the same pipeline the API-backed providers use.
func listModelsForKey(key schemas.Key, unfiltered bool) *schemas.BifrostListModelsResponse {
	response := &schemas.BifrostListModelsResponse{Data: make([]schemas.Model, 0, len(kiroModels))}
	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     key.Models,
		BlacklistedModels: key.BlacklistedModels,
		Aliases:           key.Aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       schemas.Kiro,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return response
	}
	included := make(map[string]bool, len(kiroModels))
	for _, id := range kiroModels {
		for _, result := range pipeline.FilterModel(id) {
			entry := schemas.Model{
				ID:      string(schemas.Kiro) + "/" + result.ResolvedID,
				Name:    schemas.Ptr(providerUtils.ToDisplayName(id)),
				OwnedBy: schemas.Ptr("kiro"),
			}
			if window := kiroContextWindow(id); window > 0 {
				entry.ContextLength = schemas.Ptr(window)
			}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			response.Data = append(response.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}
	response.Data = append(response.Data, pipeline.BackfillModels(included)...)
	return response
}
