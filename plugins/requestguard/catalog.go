package requestguard

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// Catalog is the model metadata surface needed by request guard policies.
// ModelCatalog implements this interface directly, without an adapter.
type Catalog interface {
	GetModelCapabilities(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities
	GetModelInfo(provider schemas.ModelProvider, model string) *schemas.Model
	GetPricingEntryForModel(model string, provider schemas.ModelProvider) *modelcatalog.PricingEntry
	CalculateCostForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) float64
	GetModelsForProvider(provider schemas.ModelProvider) []string
	GetProvidersForModel(model string) []schemas.ModelProvider
	IsRequestTypeSupported(model string, provider schemas.ModelProvider, requestType schemas.RequestType) bool
}

var _ Catalog = (*modelcatalog.ModelCatalog)(nil)
