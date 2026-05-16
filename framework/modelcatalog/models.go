package modelcatalog

import (
	"fmt"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// GetModelCapabilityEntryForModel returns capability metadata for a model/provider pair.
// It prefers chat, then responses, then text-completion entries; if none exist,
// it falls back to the lexicographically first available mode for deterministic behavior.
func (mc *ModelCatalog) GetModelCapabilityEntryForModel(model string, provider schemas.ModelProvider) *PricingEntry {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if entry := mc.getCapabilityEntryForExactModelUnsafe(model, provider); entry != nil {
		return entry
	}

	baseModel := mc.getBaseModelNameUnsafe(model)
	if baseModel != model {
		if entry := mc.getCapabilityEntryForExactModelUnsafe(baseModel, provider); entry != nil {
			return entry
		}
	}

	if entry := mc.getCapabilityEntryForModelFamilyUnsafe(baseModel, provider); entry != nil {
		return entry
	}

	return nil
}

// GetModelsForProvider returns all available models for a given provider (thread-safe)
func (mc *ModelCatalog) GetModelsForProvider(provider schemas.ModelProvider) []string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	models, exists := mc.modelPool[provider]
	if !exists {
		return []string{}
	}

	// Return a copy to prevent external modification
	result := make([]string, len(models))
	copy(result, models)
	return result
}

// GetUnfilteredModelsForProvider returns all available models for a given provider (thread-safe)
func (mc *ModelCatalog) GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	models, exists := mc.unfilteredModelPool[provider]
	if !exists {
		return []string{}
	}

	// Return a copy to prevent external modification
	result := make([]string, len(models))
	copy(result, models)
	return result
}

// GetDistinctBaseModelNames returns all unique base model names from the catalog (thread-safe).
// This is used for governance model selection when no specific provider is chosen.
func (mc *ModelCatalog) GetDistinctBaseModelNames() []string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	seen := make(map[string]bool)
	for _, baseName := range mc.baseModelIndex {
		seen[baseName] = true
	}

	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	return result
}

// GetProvidersForModel returns all providers for a given model (thread-safe)
func (mc *ModelCatalog) GetProvidersForModel(model string) []schemas.ModelProvider {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	providers := make([]schemas.ModelProvider, 0)
	for provider, models := range mc.modelPool {
		isModelMatch := false
		for _, m := range models {
			if m == model || mc.getBaseModelNameUnsafe(m) == mc.getBaseModelNameUnsafe(model) {
				isModelMatch = true
				break
			}
		}
		if isModelMatch {
			providers = append(providers, provider)
		}
	}

	// Handler special provider cases
	// 1. Handler openrouter models
	if !slices.Contains(providers, schemas.OpenRouter) {
		for _, provider := range providers {
			if openRouterModels, ok := mc.modelPool[schemas.OpenRouter]; ok {
				if slices.Contains(openRouterModels, string(provider)+"/"+model) {
					providers = append(providers, schemas.OpenRouter)
				}
			}
		}
	}

	// 2. Handle vertex models
	if !slices.Contains(providers, schemas.Vertex) {
		for _, provider := range providers {
			if vertexModels, ok := mc.modelPool[schemas.Vertex]; ok {
				if slices.Contains(vertexModels, string(provider)+"/"+model) {
					providers = append(providers, schemas.Vertex)
				}
			}
		}
	}

	// 3. Handle openai models for groq
	if !slices.Contains(providers, schemas.Groq) && strings.Contains(model, "gpt-") {
		if groqModels, ok := mc.modelPool[schemas.Groq]; ok {
			if slices.Contains(groqModels, "openai/"+model) {
				providers = append(providers, schemas.Groq)
			}
		}
	}

	// 4. Handle anthropic models for bedrock
	if !slices.Contains(providers, schemas.Bedrock) && strings.Contains(model, "claude") {
		if bedrockModels, ok := mc.modelPool[schemas.Bedrock]; ok {
			for _, bedrockModel := range bedrockModels {
				if strings.Contains(bedrockModel, model) {
					providers = append(providers, schemas.Bedrock)
					break
				}
			}
		}
	}

	return providers
}

// IsModelAllowedForProvider checks if a model is allowed for a specific provider
// based on the allowed models list and catalog data. It handles all cross-provider
// logic including provider-prefixed models and special routing rules.
//
// Parameters:
//   - provider: The provider to check against
//   - model: The model name (without provider prefix, e.g., "gpt-4o" or "claude-3-5-sonnet")
//   - allowedModels: List of allowed model names (can be empty, can include provider prefixes)
//
// Behavior:
//   - If allowedModels is ["*"]: Uses model catalog to check if provider supports the model
//     (delegates to GetProvidersForModel which handles all cross-provider logic)
//   - If allowedModels is empty ([]): Deny-by-default — returns false for any provider/model pair
//   - If allowedModels is not empty: Checks if model matches any entry in the list
//     Provider-specific validation:
//   - Direct matches: "gpt-4o" in allowedModels for any provider
//   - Prefixed matches: Only if the prefixed model exists in provider's catalog
//     (e.g., "openai/gpt-4o" in allowedModels only matches if openrouter's catalog
//     contains "openai/gpt-4o" AND the model part matches the request)
//
// Returns:
//   - bool: true if the model is allowed for the provider, false otherwise
//
// Examples:
//
//	// Wildcard allowedModels - uses catalog to check provider support
//	mc.IsModelAllowedForProvider("openrouter", "claude-3-5-sonnet", []string{"*"})
//	// Returns: true (catalog knows openrouter has "anthropic/claude-3-5-sonnet")
//
//	// Empty allowedModels - deny all (deny-by-default)
//	mc.IsModelAllowedForProvider("openrouter", "claude-3-5-sonnet", []string{})
//	// Returns: false (no models are permitted)
//
//	// Explicit allowedModels with prefix - validates against catalog
//	mc.IsModelAllowedForProvider("openrouter", "gpt-4o", []string{"openai/gpt-4o"})
//	// Returns: true (openrouter's catalog contains "openai/gpt-4o" AND model part is "gpt-4o")
//
//	// Explicit allowedModels with prefix - wrong model
//	mc.IsModelAllowedForProvider("openrouter", "claude-3-5-sonnet", []string{"openai/gpt-4o"})
//	// Returns: false (model part "gpt-4o" doesn't match request "claude-3-5-sonnet")
//
//	// Explicit allowedModels without prefix
//	mc.IsModelAllowedForProvider("openai", "gpt-4o", []string{"gpt-4o"})
//	// Returns: true (direct match)
func (mc *ModelCatalog) IsModelAllowedForProvider(provider schemas.ModelProvider, model string, providerConfig *configstore.ProviderConfig, allowedModels schemas.WhiteList) bool {
	isCustomProvider := false
	hasListModelsEndpointDisabled := false
	if providerConfig != nil {
		isCustomProvider = providerConfig.CustomProviderConfig != nil
		hasListModelsEndpointDisabled = !providerConfig.CustomProviderConfig.IsOperationAllowed(schemas.ListModelsRequest)
	}

	// Case 1: ["*"] = allow all models; use catalog to determine support
	// Empty allowedModels = deny all (fail-safe deny-by-default)
	if allowedModels.IsUnrestricted() {
		if isCustomProvider && hasListModelsEndpointDisabled {
			return true
		}
		supportedProviders := mc.GetProvidersForModel(model)
		return slices.Contains(supportedProviders, provider)
	}
	if allowedModels.IsEmpty() {
		return false
	}

	// Case 2: Explicit allowedModels = check if model matches any entry
	// Get provider's catalog models for validation of prefixed entries
	providerCatalogModels := mc.GetModelsForProvider(provider)

	for _, allowedModel := range allowedModels {
		// Direct match: "gpt-4o" == "gpt-4o"
		if allowedModel == model {
			return true
		}

		// Provider-prefixed match: verify it exists in provider's catalog first
		// This ensures we only allow provider-specific model combinations that are actually supported
		if strings.Contains(allowedModel, "/") {
			// Check if this exact prefixed model exists in the provider's catalog
			// e.g., for openrouter, check if "openai/gpt-4o" is in its catalog
			if slices.Contains(providerCatalogModels, allowedModel) {
				// Extract the model part and compare with request
				_, modelPart := schemas.ParseModelString(allowedModel, "")
				if modelPart == model {
					return true
				}
			}
		}
	}

	return false
}

// GetBaseModelName returns the canonical base model name for a given model string.
// It uses the pre-computed base_model from the pricing catalog when available,
// falling back to algorithmic date/version stripping for models not in the catalog.
//
// Examples:
//
//	mc.GetBaseModelName("gpt-4o")                    // Returns: "gpt-4o"
//	mc.GetBaseModelName("openai/gpt-4o")             // Returns: "gpt-4o"
//	mc.GetBaseModelName("gpt-4o-2024-08-06")         // Returns: "gpt-4o" (algorithmic fallback)
func (mc *ModelCatalog) GetBaseModelName(model string) string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.getBaseModelNameUnsafe(model)
}

// getBaseModelNameUnsafe returns the canonical base model name for a given model string without locking.
// This is used to avoid locking overhead when getting the base model name for many models.
// Make sure the caller function is holding the read lock before calling this function.
// It is not safe to use this function when the model pool is being updated.
func (mc *ModelCatalog) getBaseModelNameUnsafe(model string) string {
	// Step 1: Direct lookup in base model index
	if base, ok := mc.baseModelIndex[model]; ok {
		return base
	}

	// Step 2: Strip provider prefix and try again
	_, baseName := schemas.ParseModelString(model, "")
	if baseName != model {
		if base, ok := mc.baseModelIndex[baseName]; ok {
			return base
		}
	}

	// Step 3: Fallback to algorithmic date/version stripping
	// (for models not in the catalog, e.g., user-configured custom models)
	return schemas.BaseModelName(baseName)
}

// IsSameModel checks if two model strings refer to the same underlying model.
// It compares the canonical base model names derived from the pricing catalog
// (or algorithmic fallback for models not in the catalog).
//
// Examples:
//
//	mc.IsSameModel("gpt-4o", "gpt-4o")                            // true (direct match)
//	mc.IsSameModel("openai/gpt-4o", "gpt-4o")                     // true (same base model)
//	mc.IsSameModel("gpt-4o", "claude-3-5-sonnet")                  // false (different models)
//	mc.IsSameModel("openai/gpt-4o", "anthropic/claude-3-5-sonnet") // false
func (mc *ModelCatalog) IsSameModel(model1, model2 string) bool {
	if model1 == model2 {
		return true
	}
	return mc.GetBaseModelName(model1) == mc.GetBaseModelName(model2)
}

// DeleteModelDataForProvider deletes all model data from the pool for a given provider
func (mc *ModelCatalog) DeleteModelDataForProvider(provider schemas.ModelProvider) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	delete(mc.modelPool, provider)
	delete(mc.unfilteredModelPool, provider)
}

// UpsertModelDataForProvider upserts model data for a given provider
func (mc *ModelCatalog) UpsertModelDataForProvider(provider schemas.ModelProvider, modelData *schemas.BifrostListModelsResponse, allowedModels []schemas.Model) {
	if modelData == nil {
		return
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Populating models from pricing data for the given provider
	// Provider models map
	providerModels := []string{}
	// Iterate through all pricing data to collect models per provider
	for _, pricing := range mc.pricingData {
		// Normalize provider before adding to model pool
		normalizedProvider := schemas.ModelProvider(normalizeProvider(pricing.Provider))
		// We will only add models for the given provider
		if normalizedProvider != provider {
			continue
		}
		// Add model to the provider's model set (using map for deduplication)
		if slices.Contains(providerModels, pricing.Model) {
			continue
		}
		providerModels = append(providerModels, pricing.Model)
		// Build base model index from pre-computed base_model field
		if pricing.BaseModel != "" {
			mc.baseModelIndex[pricing.Model] = pricing.BaseModel
		}
	}
	// If modelData is empty, then we allow all models
	if len(modelData.Data) == 0 && len(allowedModels) == 0 {
		mc.modelPool[provider] = providerModels
		return
	}
	// Here we make sure that we still keep the backup for model catalog intact
	// So we start with a existing model pool and add the new models from incoming data
	finalModelList := make([]string, 0)
	seenModels := make(map[string]bool)
	// Case where list models failed but we have allowed models from keys
	if len(modelData.Data) == 0 && len(allowedModels) > 0 {
		for _, allowedModel := range allowedModels {
			parsedProvider, parsedModel := schemas.ParseModelString(allowedModel.ID, "")
			if parsedProvider != provider {
				continue
			}
			if !seenModels[parsedModel] {
				seenModels[parsedModel] = true
				finalModelList = append(finalModelList, parsedModel)
			}
		}
	}
	for _, model := range modelData.Data {
		parsedProvider, parsedModel := schemas.ParseModelString(model.ID, "")
		if parsedProvider != provider {
			continue
		}
		if !seenModels[parsedModel] {
			seenModels[parsedModel] = true
			finalModelList = append(finalModelList, parsedModel)
		}
	}

	if len(allowedModels) == 0 {
		for _, model := range providerModels {
			if !seenModels[model] {
				seenModels[model] = true
				finalModelList = append(finalModelList, model)
			}
		}
	}
	mc.modelPool[provider] = finalModelList
}

// UpsertUnfilteredModelDataForProvider upserts unfiltered model data for a given provider
func (mc *ModelCatalog) UpsertUnfilteredModelDataForProvider(provider schemas.ModelProvider, modelData *schemas.BifrostListModelsResponse) {
	if modelData == nil {
		return
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Populating models from pricing data for the given provider
	providerModels := []string{}
	seenModels := make(map[string]bool)
	for _, pricing := range mc.pricingData {
		normalizedProvider := schemas.ModelProvider(normalizeProvider(pricing.Provider))
		if normalizedProvider != provider {
			continue
		}
		if !seenModels[pricing.Model] {
			seenModels[pricing.Model] = true
			providerModels = append(providerModels, pricing.Model)
		}
	}
	for _, model := range modelData.Data {
		parsedProvider, parsedModel := schemas.ParseModelString(model.ID, "")
		if parsedProvider != provider {
			continue
		}
		if !seenModels[parsedModel] {
			seenModels[parsedModel] = true
			providerModels = append(providerModels, parsedModel)
		}
	}
	mc.unfilteredModelPool[provider] = providerModels
}

// RefineModelForProvider refines the model for a given provider by performing a lookup
// in mc.modelPool and using schemas.ParseModelString to extract provider and model parts.
// e.g. "gpt-oss-120b" for groq provider -> "openai/gpt-oss-120b"
//
// Behavior:
// - When the provider's catalog (mc.modelPool) yields multiple matching models, returns an error
// - When exactly one match is found, returns the fully-qualified model (provider/model format)
// - When the provider is not handled or no refinement is needed, returns the original model unchanged
func (mc *ModelCatalog) RefineModelForProvider(provider schemas.ModelProvider, model string) (string, error) {
	switch provider {
	case schemas.Groq:
		if strings.Contains(model, "gpt-") {
			return "openai/" + model, nil
		}
		return mc.refineNestedProviderModel(provider, model)
	case schemas.Replicate:
		return mc.refineNestedProviderModel(provider, model)
	}
	return model, nil
}

// SetPricingOverrides replaces the full in-memory pricing override set.
func (mc *ModelCatalog) SetPricingOverrides(rows []configstoreTables.TablePricingOverride) error {
	seen := make(map[string]int, len(rows))
	overrides := make([]PricingOverride, 0, len(rows))
	for i := range rows {
		o, err := convertTablePricingOverrideToPricingOverride(&rows[i])
		if err != nil {
			return err
		}
		if idx, exists := seen[o.ID]; exists {
			overrides[idx] = o // last entry wins for duplicate IDs
		} else {
			seen[o.ID] = len(overrides)
			overrides = append(overrides, o)
		}
	}
	mc.overridesMu.Lock()
	mc.rawOverrides = overrides
	mc.customPricing = buildCustomPricingData(overrides)
	mc.overridesMu.Unlock()
	return nil
}

// UpsertPricingOverrides inserts or replaces one or more pricing overrides in a single
// operation, rebuilding the lookup map only once at the end.
func (mc *ModelCatalog) UpsertPricingOverrides(rows ...*configstoreTables.TablePricingOverride) error {
	// Deduplicate the input batch by ID (last entry wins) and build the
	// incoming set for O(1) lookup when filtering existing rawOverrides.
	seenIncoming := make(map[string]int, len(rows))
	overrides := make([]PricingOverride, 0, len(rows))
	for _, row := range rows {
		o, err := convertTablePricingOverrideToPricingOverride(row)
		if err != nil {
			return err
		}
		if idx, exists := seenIncoming[o.ID]; exists {
			overrides[idx] = o // last entry wins for duplicate IDs
		} else {
			seenIncoming[o.ID] = len(overrides)
			overrides = append(overrides, o)
		}
	}

	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()

	updated := make([]PricingOverride, 0, len(mc.rawOverrides)+len(overrides))
	for _, o := range mc.rawOverrides {
		if _, replacing := seenIncoming[o.ID]; !replacing {
			updated = append(updated, o)
		}
	}
	updated = append(updated, overrides...)
	mc.rawOverrides = updated
	mc.customPricing = buildCustomPricingData(updated)
	return nil
}

// DeletePricingOverride removes a pricing override by ID.
func (mc *ModelCatalog) DeletePricingOverride(id string) {
	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()

	updated := make([]PricingOverride, 0, len(mc.rawOverrides))
	for _, o := range mc.rawOverrides {
		if o.ID != id {
			updated = append(updated, o)
		}
	}
	mc.rawOverrides = updated
	mc.customPricing = buildCustomPricingData(updated)
}

// IsTextCompletionSupported checks if a model supports text completion for the given provider.
// Returns true if the model has pricing data for text completion ("text_completion"),
// false otherwise. This is used by the litellmcompat plugin to determine whether to
// convert text completion requests to chat completion requests.
func (mc *ModelCatalog) IsTextCompletionSupported(model string, provider schemas.ModelProvider) bool {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	// Check for text completion mode in pricing data
	key := makeKey(model, normalizeProvider(string(provider)), normalizeRequestType(schemas.TextCompletionRequest))
	_, ok := mc.pricingData[key]
	return ok
}

// HELPER FUNCTIONS

func (mc *ModelCatalog) getCapabilityEntryForExactModelUnsafe(model string, provider schemas.ModelProvider) *PricingEntry {
	preferredModes := []schemas.RequestType{
		schemas.ChatCompletionRequest,
		schemas.ResponsesRequest,
		schemas.TextCompletionRequest,
	}

	for _, mode := range preferredModes {
		key := makeKey(model, string(provider), normalizeRequestType(mode))
		pricing, ok := mc.pricingData[key]
		if ok {
			return convertTableModelPricingToPricingData(&pricing)
		}
	}

	prefix := model + "|" + string(provider) + "|"
	matchingKeys := make([]string, 0)
	for key := range mc.pricingData {
		if strings.HasPrefix(key, prefix) {
			matchingKeys = append(matchingKeys, key)
		}
	}
	return mc.selectCapabilityEntryFromKeysUnsafe(matchingKeys)
}

func (mc *ModelCatalog) getCapabilityEntryForModelFamilyUnsafe(baseModel string, provider schemas.ModelProvider) *PricingEntry {
	if baseModel == "" {
		return nil
	}

	matchingKeys := make([]string, 0)
	for key, pricing := range mc.pricingData {
		if normalizeProvider(pricing.Provider) != string(provider) {
			continue
		}
		if mc.getBaseModelNameUnsafe(pricing.Model) != baseModel {
			continue
		}
		matchingKeys = append(matchingKeys, key)
	}
	return mc.selectCapabilityEntryFromKeysUnsafe(matchingKeys)
}

func (mc *ModelCatalog) selectCapabilityEntryFromKeysUnsafe(matchingKeys []string) *PricingEntry {
	if len(matchingKeys) == 0 {
		return nil
	}

	preferredModes := []string{
		normalizeRequestType(schemas.ChatCompletionRequest),
		normalizeRequestType(schemas.ResponsesRequest),
		normalizeRequestType(schemas.TextCompletionRequest),
	}

	for _, mode := range preferredModes {
		modeMatches := make([]string, 0)
		for _, key := range matchingKeys {
			parts := strings.SplitN(key, "|", 3)
			if len(parts) != 3 || parts[2] != mode {
				continue
			}
			modeMatches = append(modeMatches, key)
		}
		if len(modeMatches) == 0 {
			continue
		}
		slices.Sort(modeMatches)
		pricing := mc.pricingData[modeMatches[0]]
		return convertTableModelPricingToPricingData(&pricing)
	}

	slices.Sort(matchingKeys)
	pricing := mc.pricingData[matchingKeys[0]]
	return convertTableModelPricingToPricingData(&pricing)
}

// refineNestedProviderModel resolves provider-native model slugs such as
// "openai/gpt-5-nano" from a base model request like "gpt-5-nano".
// It only considers catalog entries whose leading segment is a known Bifrost provider,
// so Replicate owner/model identifiers like "meta/llama-3-8b" are left untouched.
func (mc *ModelCatalog) refineNestedProviderModel(provider schemas.ModelProvider, model string) (string, error) {
	mc.mu.RLock()
	models, ok := mc.modelPool[provider]
	mc.mu.RUnlock()
	if !ok {
		return model, nil
	}

	candidateModels := make([]string, 0)
	seenCandidates := make(map[string]struct{})
	for _, poolModel := range models {
		providerPart, modelPart := schemas.ParseModelString(poolModel, "")
		if providerPart == "" || model != modelPart {
			continue
		}

		candidate := string(providerPart) + "/" + modelPart
		if _, seen := seenCandidates[candidate]; seen {
			continue
		}
		seenCandidates[candidate] = struct{}{}
		candidateModels = append(candidateModels, candidate)
	}

	switch len(candidateModels) {
	case 0:
		return model, nil
	case 1:
		return candidateModels[0], nil
	default:
		return "", fmt.Errorf("multiple compatible models found for model %s: %v", model, candidateModels)
	}
}
