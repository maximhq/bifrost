// Package governance provides utility functions for the governance plugin
package governance

import (
	"context"
	"regexp"
	"sort"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/valyala/fasthttp"
)

var (
	modelEqualsLiteralRegexp = regexp.MustCompile(`\bmodel\s*==\s*(?:"([^"]+)"|'([^']+)')`)
	modelInLiteralRegexp     = regexp.MustCompile(`\bmodel\s+in\s*\[([^\]]*)\]`)
	stringLiteralRegexp      = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
)

// ParseVirtualKeyFromFastHTTPRequest parses the virtual key from FastHTTP request headers.
// Parameters:
//   - req: The FastHTTP request containing headers to parse
//
// Returns:
//   - *string: The virtual key if found, nil otherwise
func ParseVirtualKeyFromFastHTTPRequest(req *fasthttp.RequestCtx) *string {
	vkHeader := string(req.Request.Header.Peek("x-bf-vk"))
	if vkHeader != "" && strings.HasPrefix(strings.ToLower(vkHeader), VirtualKeyPrefix) {
		return bifrost.Ptr(vkHeader)
	}
	authHeader := string(req.Request.Header.Peek("Authorization"))
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			authHeaderValue := strings.TrimSpace(authHeader[7:]) // Remove "Bearer " prefix
			if authHeaderValue != "" && strings.HasPrefix(strings.ToLower(authHeaderValue), VirtualKeyPrefix) {
				return bifrost.Ptr(authHeaderValue)
			}
		}
	}
	xAPIKey := string(req.Request.Header.Peek("x-api-key"))
	if xAPIKey != "" && strings.HasPrefix(strings.ToLower(xAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xAPIKey)
	}
	xGoogleAPIKey := string(req.Request.Header.Peek("x-goog-api-key"))
	if xGoogleAPIKey != "" && strings.HasPrefix(strings.ToLower(xGoogleAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xGoogleAPIKey)
	}
	return nil
}

// parseVirtualKeyFromHTTPRequest parses the virtual key from HTTP request headers.
// It checks multiple headers in order: x-bf-vk, Authorization (Bearer token), x-api-key, and x-goog-api-key.
// Parameters:
//   - req: The HTTP request containing headers to parse
//
// Returns:
//   - *string: The virtual key if found, nil otherwise
func parseVirtualKeyFromHTTPRequest(req *schemas.HTTPRequest) *string {
	var virtualKeyValue string
	vkHeader := req.CaseInsensitiveHeaderLookup("x-bf-vk")
	if vkHeader != "" && strings.HasPrefix(strings.ToLower(vkHeader), VirtualKeyPrefix) {
		return bifrost.Ptr(vkHeader)
	}
	authHeader := req.CaseInsensitiveHeaderLookup("Authorization")
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			authHeaderValue := strings.TrimSpace(authHeader[7:]) // Remove "Bearer " prefix
			if authHeaderValue != "" && strings.HasPrefix(strings.ToLower(authHeaderValue), VirtualKeyPrefix) {
				virtualKeyValue = authHeaderValue
			}
		}
	}
	if virtualKeyValue != "" {
		return bifrost.Ptr(virtualKeyValue)
	}
	xAPIKey := req.CaseInsensitiveHeaderLookup("x-api-key")
	if xAPIKey != "" && strings.HasPrefix(strings.ToLower(xAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xAPIKey)
	}
	// Checking x-goog-api-key header
	xGoogleAPIKey := req.CaseInsensitiveHeaderLookup("x-goog-api-key")
	if xGoogleAPIKey != "" && strings.HasPrefix(strings.ToLower(xGoogleAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xGoogleAPIKey)
	}
	return nil
}

// getWeight safely dereferences a *float64 weight pointer, returning 1.0 as default if nil.
// This allows distinguishing between "not set" (nil -> 1.0) and "explicitly set to 0" (0.0).
func getWeight(w *float64) float64 {
	if w == nil {
		return 1.0
	}
	return *w
}

// filterModelsForVirtualKey filters models based on virtual key's provider configs
// Returns only models that are allowed by the virtual key's ProviderConfigs
func (p *GovernancePlugin) filterModelsForVirtualKey(
	ctx context.Context,
	models []schemas.Model,
	virtualKeyValue string,
	currentProvider schemas.ModelProvider,
) []schemas.Model {
	// Get virtual key configuration
	vk, exists := p.store.GetVirtualKey(ctx, virtualKeyValue)
	if !exists {
		p.logger.Warn("[Governance] Virtual key not found for list models filtering: %s", virtualKeyValue)
		return []schemas.Model{} // VK not found, return empty list
	}

	// Empty ProviderConfigs means no models are allowed (deny-by-default)
	if len(vk.ProviderConfigs) == 0 {
		return []schemas.Model{}
	}

	// Filter models based on ProviderConfigs
	filteredModels := make([]schemas.Model, 0, len(models))
	for _, model := range models {
		provider, modelName := schemas.ParseModelString(model.ID, "")

		// Check if this provider/model combination is allowed
		isAllowed := false
		for _, pc := range vk.ProviderConfigs {
			if pc.Provider == string(provider) {
				if p.modelCatalog != nil && p.inMemoryStore != nil {
					providerConfig, ok := p.inMemoryStore.GetConfiguredProviders()[provider]
					providerConfigPtr := &providerConfig
					if !ok {
						providerConfigPtr = nil
					}
					if p.modelCatalog.IsModelAllowedForProvider(provider, modelName, providerConfigPtr, pc.AllowedModels) {
						isAllowed = true
						break
					}
				} else {
					if pc.AllowedModels.IsAllowed(modelName) {
						isAllowed = true
						break
					}
				}
			}
		}

		if isAllowed {
			filteredModels = append(filteredModels, model)
		}
	}

	filteredModels = p.appendRoutingRuleModelsForVirtualKey(ctx, filteredModels, vk, currentProvider)
	sort.Slice(filteredModels, func(i, j int) bool {
		return strings.ToLower(filteredModels[i].ID) < strings.ToLower(filteredModels[j].ID)
	})

	return filteredModels
}

func (p *GovernancePlugin) appendRoutingRuleModelsForVirtualKey(ctx context.Context, models []schemas.Model, vk *configstoreTables.TableVirtualKey, currentProvider schemas.ModelProvider) []schemas.Model {
	if p.store == nil || vk == nil || currentProvider == "" || !p.store.HasRoutingRules(ctx) {
		return models
	}

	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		seen[strings.ToLower(model.ID)] = struct{}{}
	}

	for _, scope := range buildScopeChain(vk) {
		for _, rule := range p.store.GetScopedRoutingRules(ctx, scope.ScopeName, scope.ScopeID) {
			if rule == nil || !rule.EnabledValue() || !p.routingRuleListsOnProvider(vk, rule, currentProvider) {
				continue
			}
			for _, modelID := range routingRuleModelLiterals(rule.CelExpression) {
				if strings.TrimSpace(modelID) == "" {
					continue
				}
				key := strings.ToLower(modelID)
				if _, ok := seen[key]; ok {
					continue
				}
				models = append(models, schemas.Model{
					ID:   modelID,
					Name: schemas.Ptr(modelID),
				})
				seen[key] = struct{}{}
			}
		}
	}

	return models
}

func routingRuleModelLiterals(expression string) []string {
	seen := map[string]struct{}{}
	var models []string
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		models = append(models, model)
	}

	for _, match := range modelEqualsLiteralRegexp.FindAllStringSubmatch(expression, -1) {
		add(match[1])
	}
	for _, match := range modelInLiteralRegexp.FindAllStringSubmatch(expression, -1) {
		for _, literal := range stringLiteralRegexp.FindAllStringSubmatch(match[1], -1) {
			add(literal[1])
		}
	}

	sort.Slice(models, func(i, j int) bool {
		return strings.ToLower(models[i]) < strings.ToLower(models[j])
	})
	return models
}

func (p *GovernancePlugin) routingRuleListsOnProvider(vk *configstoreTables.TableVirtualKey, rule *configstoreTables.TableRoutingRule, currentProvider schemas.ModelProvider) bool {
	if provider := p.firstAllowedRoutingRulePrimaryProvider(vk, rule, currentProvider); provider != "" {
		return strings.EqualFold(provider, string(currentProvider))
	}
	if provider := p.firstAllowedRoutingRuleFallbackProvider(vk, rule); provider != "" {
		return strings.EqualFold(provider, string(currentProvider))
	}
	return false
}

func (p *GovernancePlugin) firstAllowedRoutingRulePrimaryProvider(vk *configstoreTables.TableVirtualKey, rule *configstoreTables.TableRoutingRule, currentProvider schemas.ModelProvider) string {
	for _, target := range rule.Targets {
		if target.Provider != nil {
			if p.routingTargetAllowedForVirtualKey(vk, target.Provider, target.Model) {
				return *target.Provider
			}
			continue
		}

		currentProviderString := string(currentProvider)
		if currentProviderString == "" {
			continue
		}
		if p.routingTargetAllowedForVirtualKey(vk, &currentProviderString, target.Model) {
			return currentProviderString
		}
	}
	return ""
}

func (p *GovernancePlugin) firstAllowedRoutingRuleFallbackProvider(vk *configstoreTables.TableVirtualKey, rule *configstoreTables.TableRoutingRule) string {
	for _, fallback := range rule.ParsedFallbacks {
		provider, model := schemas.ParseModelString(fallback, "")
		providerString := string(provider)
		if providerString == "" || model == "" {
			continue
		}
		if p.routingTargetAllowedForVirtualKey(vk, &providerString, &model) {
			return providerString
		}
	}
	return ""
}

func (p *GovernancePlugin) routingTargetAllowedForVirtualKey(vk *configstoreTables.TableVirtualKey, provider *string, model *string) bool {
	if vk == nil || len(vk.ProviderConfigs) == 0 {
		return false
	}

	for _, pc := range vk.ProviderConfigs {
		if provider != nil && !strings.EqualFold(pc.Provider, *provider) {
			continue
		}
		if model == nil || strings.TrimSpace(*model) == "" {
			return true
		}
		if p.modelCatalog != nil && p.inMemoryStore != nil {
			providerConfig, ok := p.inMemoryStore.GetConfiguredProviders()[schemas.ModelProvider(pc.Provider)]
			providerConfigPtr := &providerConfig
			if !ok {
				providerConfigPtr = nil
			}
			if p.modelCatalog.IsModelAllowedForProvider(schemas.ModelProvider(pc.Provider), *model, providerConfigPtr, pc.AllowedModels) {
				return true
			}
			continue
		}
		if pc.AllowedModels.IsAllowed(*model) {
			return true
		}
	}

	return false
}
