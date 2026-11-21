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
}

func (f *fakeCatalog) GetModelCapabilities(schemas.ModelProvider, string) *schemas.ModelCapabilities {
	return f.capabilities
}

func (f *fakeCatalog) GetModelInfo(schemas.ModelProvider, string) *schemas.Model {
	return f.modelInfo
}

func (f *fakeCatalog) GetPricingEntryForModel(string, schemas.ModelProvider) *modelcatalog.PricingEntry {
	return f.pricingEntry
}

func (*fakeCatalog) CalculateCostForUsage(*schemas.BifrostLLMUsage, schemas.ModelProvider, string, schemas.RequestType, *modelcatalog.PricingLookupScopes) float64 {
	return 0
}

func (*fakeCatalog) GetModelsForProvider(schemas.ModelProvider) []string {
	return nil
}

func (*fakeCatalog) GetProvidersForModel(string) []schemas.ModelProvider {
	return nil
}

func (f *fakeCatalog) IsRequestTypeSupported(string, schemas.ModelProvider, schemas.RequestType) bool {
	return f.requestTypeSupported
}

func boolPtr(value bool) *bool       { return &value }
func intPtr(value int) *int          { return &value }
func stringPtr(value string) *string { return &value }
