// Package governance provides utility functions for the governance plugin
package governance

import (
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

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
	if vkHeader != "" {
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
	models []schemas.Model,
	virtualKeyValue string,
) []schemas.Model {
	// Get virtual key configuration
	vk, exists := p.store.GetVirtualKey(virtualKeyValue)
	if !exists {
		p.logger.Warn("[Governance] Virtual key not found for list models filtering: %s", virtualKeyValue)
		return []schemas.Model{} // VK not found, return empty list
	}

	// Empty ProviderConfigs means all models are allowed
	if len(vk.ProviderConfigs) == 0 {
		return models
	}

	// Filter models based on ProviderConfigs
	filteredModels := make([]schemas.Model, 0, len(models))
	for _, model := range models {
		provider, modelName := schemas.ParseModelString(model.ID, "")

		// Check if this provider/model combination is allowed
		isAllowed := false
		for _, pc := range vk.ProviderConfigs {
			if pc.Provider == string(provider) {
				if p.modelCatalog.IsModelAllowedForProvider(provider, modelName, pc.AllowedModels) {
					isAllowed = true
					break
				}
			}
		}

		if isAllowed {
			filteredModels = append(filteredModels, model)
		}
	}

	return filteredModels
}
