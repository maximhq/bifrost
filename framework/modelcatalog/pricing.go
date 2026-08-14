package modelcatalog

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

type BatchCostDetails = datasheet.BatchCostDetails

type (
	// VideoPricingDimensions is the request+response pricing basis for one video
	// job. See the datasheet type for why it is captured at submission.
	VideoPricingDimensions = datasheet.VideoPricingDimensions
	VideoCostDetails       = datasheet.VideoCostDetails
)

// GetModelCapabilityEntryForModel returns capability metadata for a
// (model, provider) pair. Alias lookups try the canonical model name, wire
// model ID, and original alias key in that order. Within each model, chat,
// responses, then text-completion entries are preferred.
func (mc *ModelCatalog) GetModelCapabilityEntryForModel(model string, provider schemas.ModelProvider) *PricingEntry {
	if alias, ok := mc.keyconf.ResolveAlias(provider, model); ok {
		if alias.Config.ModelName != nil && *alias.Config.ModelName != "" {
			if entry := mc.datasheet.GetCapabilityEntry(*alias.Config.ModelName, provider); entry != nil {
				return entry
			}
		}
		if entry := mc.datasheet.GetCapabilityEntry(alias.Config.ModelID, provider); entry != nil {
			return entry
		}
	}
	return mc.datasheet.GetCapabilityEntry(model, provider)
}

// GetCatalogPricingOverrides returns the scoped pricing overrides relevant to
// a management-catalog row: the global/provider-scope winner for mode (the
// pricing the UI shows as overridden) plus every override matching
// (model, provider) for informational display.
func (mc *ModelCatalog) GetCatalogPricingOverrides(model string, provider schemas.ModelProvider, mode string) CatalogPricingOverrides {
	return mc.datasheet.CatalogPricingOverrides(model, provider, mode)
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

// ResolveModelParameters reads the model-parameters row for model, resolving
// provider-qualified or bare aliases to the datasheet's stored key (exact →
// provider-prefix-stripped → base model → provider-qualified variants).
func (mc *ModelCatalog) ResolveModelParameters(ctx context.Context, model string) (*configstoreTables.TableModelParameters, error) {
	return mc.datasheet.ResolveModelParameters(ctx, model)
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

// CalculateCostBreakdown computes the per-category cost breakdown (input /
// output / cache) for a Bifrost response. Returns nil when there is no cost to
// record. TotalCost equals what CalculateCost returns for the same response.
func (mc *ModelCatalog) CalculateCostBreakdown(result *schemas.BifrostResponse, scopes *PricingLookupScopes) *schemas.BifrostCost {
	return mc.datasheet.CalculateCostBreakdown(result, (*datasheet.LookupScopes)(scopes))
}

// CalculateCostForUsage computes the dollar cost from a bare usage object when
// no full BifrostResponse is available — used to bill partial usage carried on
// a failed/cancelled request (BifrostError.ExtraFields.BilledUsage).
func (mc *ModelCatalog) CalculateCostForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *PricingLookupScopes) float64 {
	return mc.datasheet.CalculateCostForUsage(usage, provider, model, requestType, (*datasheet.LookupScopes)(scopes))
}

// CalculateCostBreakdownForUsage computes the per-category cost breakdown from a
// bare usage object when no full BifrostResponse is available. TotalCost equals
// what CalculateCostForUsage returns for the same usage.
func (mc *ModelCatalog) CalculateCostBreakdownForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *PricingLookupScopes) *schemas.BifrostCost {
	return mc.datasheet.CalculateCostBreakdownForUsage(usage, provider, model, requestType, (*datasheet.LookupScopes)(scopes))
}

// CalculateRoutingEmbeddingCost prices the embedding call recorded in a
// response's RoutingDebug stamp, independent of its CountTowardBudgets flag —
// telemetry uses it to report routing overhead unconditionally, while
// CalculateCost folds the same amount into the request's cost only when the
// flag is set.
func (mc *ModelCatalog) CalculateRoutingEmbeddingCost(routingDebug *schemas.BifrostRoutingDebug, scopes *PricingLookupScopes) float64 {
	return mc.datasheet.RoutingEmbeddingCost(routingDebug, (*datasheet.LookupScopes)(scopes))
}

// CalculateGuardrailCost computes the aggregate cost of guardrail judge calls.
func (mc *ModelCatalog) CalculateGuardrailCost(debug *schemas.BifrostGuardrailDebug, scopes *PricingLookupScopes) float64 {
	return mc.datasheet.CalculateGuardrailCost(debug, (*datasheet.LookupScopes)(scopes))
}

// CalculateCacheEmbeddingCost computes the semantic-cache embedding lookup cost.
func (mc *ModelCatalog) CalculateCacheEmbeddingCost(debug *schemas.BifrostCacheDebug, scopes *PricingLookupScopes) float64 {
	return mc.datasheet.CalculateCacheEmbeddingCost(debug, (*datasheet.LookupScopes)(scopes))
}

// CalculateBatchCostDetailsForUsage computes batch cost and exposes the
// explicit batch rates used for durable accounting metadata.
func (mc *ModelCatalog) CalculateBatchCostDetailsForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *PricingLookupScopes) BatchCostDetails {
	return mc.datasheet.CalculateBatchCostDetailsForUsage(usage, provider, model, requestType, (*datasheet.LookupScopes)(scopes))
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

// CalculateVideoCostDetails prices a video job from its merged request/response
// dimensions, preferring a provider-reported cost over any catalog rate.
func (mc *ModelCatalog) CalculateVideoCostDetails(dims VideoPricingDimensions, provider schemas.ModelProvider, scopes *PricingLookupScopes) VideoCostDetails {
	return mc.datasheet.CalculateVideoCostDetails(dims, provider, (*datasheet.LookupScopes)(scopes))
}

// VideoDimensionsFromResponse reads the pricing dimensions a terminal video
// response reports; what it omits is filled from the dimensions captured at
// submission.
func VideoDimensionsFromResponse(resp *schemas.BifrostVideoGenerationResponse) VideoPricingDimensions {
	return datasheet.VideoDimensionsFromResponse(resp)
}
