package modelcatalog

import (
	"fmt"
	"maps"
	"slices"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// GetModelInfo returns pricing and capability metadata for a (provider, model)
// pair in the same shape the /v1/models endpoint reports, or nil when the
// catalog has no entry for it.
//
// Lookup order is pricing-row first (exact model+provider across every request
// mode), then the capability entry, which additionally resolves through the
// canonical base model and the wider model family. That ordering keeps dated
// model IDs like "gpt-4o-2024-08-06" resolvable even when only the family row
// carries capability data.
//
// The returned *schemas.Model is freshly allocated and owned by the caller.
func (mc *ModelCatalog) GetModelInfo(provider schemas.ModelProvider, model string) *schemas.Model {
	if mc == nil || model == "" {
		return nil
	}

	entry := mc.datasheet.GetPricingEntryForModel(model, provider)
	if entry == nil {
		entry = mc.datasheet.GetCapabilityEntry(model, provider)
	}
	if entry == nil {
		return nil
	}

	info := &schemas.Model{ID: model}
	ApplyModelInfo(info, entry)

	if params := mc.datasheet.GetSupportedParameters(model); len(params) > 0 {
		info.SupportedParameters = params
	}
	return info
}

// ApplyModelInfo merges catalog metadata from entry into model, filling only
// fields the caller has not already populated. Provider-reported values always
// win: the catalog is a backfill for what a provider's list-models response
// leaves out, never an override of what it did report.
//
// Exported so the list-models handlers can enrich provider responses through
// the same mapping that GetModelInfo uses.
//
// Every reference-typed field is cloned on the way in. An Entry's maps,
// pointers and slices alias the datasheet's shared pricing row (reading a
// struct out of that map is a shallow copy), so assigning them straight
// through would hand callers - including third-party plugins via
// ctx.GetModelInfo - a live handle into catalog state. A write through such a
// handle silently rewrites the catalog for every later request, and racing the
// pricing sync on the map is a fatal concurrent map access, not a recoverable
// panic.
func ApplyModelInfo(model *schemas.Model, entry *PricingEntry) {
	if model == nil || entry == nil {
		return
	}

	model.IsDeprecated = model.IsDeprecated || entry.IsDeprecated

	if entry.BaseModel != "" && model.NormalizedName == nil {
		model.NormalizedName = new(providerUtils.NormalizeBaseModelSlug(entry.BaseModel))
	}
	if len(entry.AdditionalAttributes) > 0 && model.AdditionalAttributes == nil {
		// A shallow clone is the right depth here: the values are strings, which
		// are immutable, so there is nothing further down to alias.
		model.AdditionalAttributes = maps.Clone(entry.AdditionalAttributes)
	}

	// ContextLength falls back to MaxInputTokens: some datasheet rows carry only
	// the input limit, and callers treat context length as the headline number.
	if model.ContextLength == nil {
		if entry.ContextLength != nil {
			model.ContextLength = new(*entry.ContextLength)
		} else if entry.MaxInputTokens != nil {
			model.ContextLength = new(*entry.MaxInputTokens)
		}
	}
	if entry.MaxInputTokens != nil && model.MaxInputTokens == nil {
		model.MaxInputTokens = new(*entry.MaxInputTokens)
	}
	if entry.MaxOutputTokens != nil && model.MaxOutputTokens == nil {
		model.MaxOutputTokens = new(*entry.MaxOutputTokens)
	}
	if entry.Architecture != nil && model.Architecture == nil {
		arch := *entry.Architecture
		// The scalar pointers need copying too, not just the slices: a struct
		// copy carries them straight through, and the entry's Architecture is
		// the very pointer the datasheet stores.
		if entry.Architecture.Modality != nil {
			arch.Modality = new(*entry.Architecture.Modality)
		}
		if entry.Architecture.Tokenizer != nil {
			arch.Tokenizer = new(*entry.Architecture.Tokenizer)
		}
		if entry.Architecture.InstructType != nil {
			arch.InstructType = new(*entry.Architecture.InstructType)
		}
		arch.InputModalities = slices.Clone(entry.Architecture.InputModalities)
		arch.OutputModalities = slices.Clone(entry.Architecture.OutputModalities)
		model.Architecture = &arch
	}

	if model.Pricing != nil {
		return
	}
	pricing := &schemas.Pricing{}
	if entry.InputCostPerToken != nil {
		pricing.Prompt = new(formatCost(*entry.InputCostPerToken))
	}
	if entry.OutputCostPerToken != nil {
		pricing.Completion = new(formatCost(*entry.OutputCostPerToken))
	}
	if entry.InputCostPerImage != nil {
		pricing.Image = new(formatCost(*entry.InputCostPerImage))
	}
	if entry.CacheReadInputTokenCost != nil {
		pricing.InputCacheRead = new(formatCost(*entry.CacheReadInputTokenCost))
	}
	if entry.CacheCreationInputTokenCost != nil {
		pricing.InputCacheWrite = new(formatCost(*entry.CacheCreationInputTokenCost))
	}
	if entry.SearchContextCostPerQuery != nil {
		pricing.WebSearch = new(formatCost(*entry.SearchContextCostPerQuery))
	}
	if entry.CostPerRequest != nil {
		pricing.Request = new(formatCost(*entry.CostPerRequest))
	}
	model.Pricing = pricing
}

// CalculateRequestCost returns the dollar cost of resp, resolving governance
// pricing overrides (virtual key / user / provider key scopes) from ctx.
//
// This is the plugin-facing entry point behind ctx.CalculateCost. It is a thin
// wrapper over CalculateCost so tiered pricing, batch/priority/flex rates and
// the provider/mode fallback chain all apply unchanged.
//
// Must be called synchronously while ctx is still live; the scope lookup reads
// request identity off the context.
func (mc *ModelCatalog) CalculateRequestCost(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse) float64 {
	if mc == nil || resp == nil {
		return 0
	}
	extraFields := resp.GetExtraFields()
	provider := extraFields.RoutingInfo.Provider
	if provider == "" {
		provider = extraFields.Provider
	}
	return mc.CalculateCost(resp, PricingLookupScopesFromContext(ctx, string(provider)))
}

// formatCost renders a per-unit rate the way the models API reports it. Fixed
// 10-decimal notation rather than %g so sub-cent token rates never surface in
// scientific notation, which clients parsing these as decimals choke on.
func formatCost(v float64) string {
	return fmt.Sprintf("%.10f", v)
}
