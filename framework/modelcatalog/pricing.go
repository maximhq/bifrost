package modelcatalog

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// GetModelCapabilityEntryForModel returns capability metadata for a
// (model, provider) pair. Prefers chat, then responses, then text-completion
// entries; falls back to the lexicographically first available mode for
// deterministic behavior.
func (mc *ModelCatalog) GetModelCapabilityEntryForModel(model string, provider schemas.ModelProvider) *PricingEntry {
	return mc.datasheet.GetCapabilityEntry(model, provider)
}

// IsRequestTypeSupported preserves the historical (model, provider,
// requestType) signature; provider is ignored (the underlying datasheet
// index is keyed by model only).
func (mc *ModelCatalog) IsRequestTypeSupported(model string, provider schemas.ModelProvider, requestType schemas.RequestType) bool {
	return mc.datasheet.IsRequestTypeSupported(model, requestType)
}

func (mc *ModelCatalog) GetSupportedParameters(model string) []string {
	return mc.datasheet.GetSupportedParameters(model)
}

func (mc *ModelCatalog) IsTextCompletionSupported(model string, provider schemas.ModelProvider) bool {
	return mc.datasheet.IsTextCompletionSupported(model, provider)
}

// GetPricingEntryForModel returns any pricing entry for the model across
// known modes. Used by the inference handler to enrich list-models responses.
func (mc *ModelCatalog) GetPricingEntryForModel(model string, provider schemas.ModelProvider) *PricingEntry {
	return mc.datasheet.GetPricingEntryForModel(model, provider)
}

// CalculateCost computes the dollar cost for a Bifrost response.
func (mc *ModelCatalog) CalculateCost(result *schemas.BifrostResponse, scopes *PricingLookupScopes) float64 {
	return mc.datasheet.CalculateCost(result, (*datasheet.LookupScopes)(scopes))
}

// CalculateCostForUsage computes the dollar cost from a bare usage object when
// no full BifrostResponse is available — used to bill partial usage carried on
// a failed/cancelled request (BifrostError.ExtraFields.BilledUsage).
func (mc *ModelCatalog) CalculateCostForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *PricingLookupScopes) float64 {
	return mc.datasheet.CalculateCostForUsage(usage, provider, model, requestType, (*datasheet.LookupScopes)(scopes))
}

// UpsertModelPricingAttributes writes additional_attributes for every row
// matching (model, provider) and reloads the pricing cache.
func (mc *ModelCatalog) UpsertModelPricingAttributes(ctx context.Context, model string, provider schemas.ModelProvider, attrs map[string]string) (int64, error) {
	return mc.datasheet.UpsertModelPricingAttributes(ctx, model, provider, attrs)
}

func (mc *ModelCatalog) SetPricingOverrides(rows []configstoreTables.TablePricingOverride) error {
	return mc.datasheet.SetOverrides(rows)
}

func (mc *ModelCatalog) UpsertPricingOverrides(rows ...*configstoreTables.TablePricingOverride) error {
	return mc.datasheet.UpsertOverrides(rows...)
}

func (mc *ModelCatalog) DeletePricingOverride(id string) {
	mc.datasheet.DeleteOverride(id)
}
