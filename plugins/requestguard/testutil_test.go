package requestguard

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
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

func requestGuardTestContext() *schemas.BifrostContext {
	return requestGuardTestContextForIdentity("requestguard-test-user", "requestguard-test-key", "requestguard-test-customer")
}

func requestGuardTestContextForIdentity(userID, virtualKeyID, customerID string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	var user *schemas.UserRef
	if userID != "" {
		user = &schemas.UserRef{ID: userID}
	}
	var virtualKey *schemas.EntityRef
	if virtualKeyID != "" {
		virtualKey = &schemas.EntityRef{ID: virtualKeyID}
	}
	var customers []schemas.EntityRef
	if customerID != "" {
		customers = []schemas.EntityRef{{ID: customerID}}
	}
	g := grant.New()
	g.SetIdentity(grant.NewIdentity(schemas.Credential{Kind: "requestguard-test"}, user, virtualKey, nil, customers, nil, nil))
	if !ctx.SetGrant(g) {
		panic("failed to install requestguard test grant")
	}
	return ctx
}

func requestGuardUnscopedTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}
