package modelcatalog

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func (mc *ModelCatalog) SetCodexPricingMode(provider schemas.ModelProvider, mode schemas.CodexPricingMode) {
	if provider != schemas.Codex {
		return
	}
	if mode == "" {
		mode = schemas.CodexPricingModeIncludedZero
	}
	mc.codexPricingMu.Lock()
	defer mc.codexPricingMu.Unlock()
	mc.codexPricingModes[provider] = mode
}

func (mc *ModelCatalog) DeleteCodexPricingMode(provider schemas.ModelProvider) {
	mc.codexPricingMu.Lock()
	defer mc.codexPricingMu.Unlock()
	delete(mc.codexPricingModes, provider)
}

func (mc *ModelCatalog) getCodexPricingMode(provider schemas.ModelProvider) schemas.CodexPricingMode {
	mc.codexPricingMu.RLock()
	defer mc.codexPricingMu.RUnlock()
	if mode, ok := mc.codexPricingModes[provider]; ok && mode != "" {
		return mode
	}
	return schemas.CodexPricingModeIncludedZero
}

func zeroPricing(provider, model string, requestType schemas.RequestType) *configstoreTables.TableModelPricing {
	return &configstoreTables.TableModelPricing{
		Provider: provider,
		Model:    model,
		Mode:     normalizeRequestType(requestType),
	}
}
