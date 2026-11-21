package requestguard

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

type fakeCatalog struct {
	capabilities         *schemas.ModelCapabilities
	modelInfo            *schemas.Model
	pricingEntry         *modelcatalog.PricingEntry
	requestTypeSupported bool
	capabilitiesByRoute  map[string]*schemas.ModelCapabilities
	modelInfoByRoute     map[string]*schemas.Model
	pricingByRoute       map[string]*modelcatalog.PricingEntry
	costByRoute          map[string]float64
	modelsByProvider     map[schemas.ModelProvider][]string
	providersByModel     map[string][]schemas.ModelProvider
}

func routeKey(provider schemas.ModelProvider, model string) string {
	return string(provider) + "/" + model
}

func (f *fakeCatalog) GetModelCapabilities(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
	if value, ok := f.capabilitiesByRoute[routeKey(provider, model)]; ok {
		return value
	}
	return f.capabilities
}

func (f *fakeCatalog) GetModelInfo(provider schemas.ModelProvider, model string) *schemas.Model {
	if value, ok := f.modelInfoByRoute[routeKey(provider, model)]; ok {
		return value
	}
	return f.modelInfo
}

func (f *fakeCatalog) GetPricingEntryForModel(model string, provider schemas.ModelProvider) *modelcatalog.PricingEntry {
	if value, ok := f.pricingByRoute[routeKey(provider, model)]; ok {
		return value
	}
	return f.pricingEntry
}

func (f *fakeCatalog) CalculateCostForUsage(_ *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, _ schemas.RequestType, _ *modelcatalog.PricingLookupScopes) float64 {
	return f.costByRoute[routeKey(provider, model)]
}

func (f *fakeCatalog) GetModelsForProvider(provider schemas.ModelProvider) []string {
	return f.modelsByProvider[provider]
}

func (f *fakeCatalog) GetProvidersForModel(model string) []schemas.ModelProvider {
	return f.providersByModel[model]
}

func (f *fakeCatalog) IsRequestTypeSupported(string, schemas.ModelProvider, schemas.RequestType) bool {
	return f.requestTypeSupported
}

func boolPtr(value bool) *bool       { return &value }
func intPtr(value int) *int          { return &value }
func stringPtr(value string) *string { return &value }
