package azure

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// findMatchingAllowedModel finds a matching item in a whitelist, considering both
// exact match and base model matches (ignoring version suffixes).
// Returns the matched item from the whitelist if found, empty string otherwise.
// If matched via base model, returns the item from whitelist (not the value parameter).
func findMatchingAllowedModel(wl schemas.WhiteList, value string) string {
	// First check exact match (case-insensitive)
	if wl.Contains(value) {
		return value
	}

	// Additional layer: check base model matches (ignoring version suffixes)
	// This handles cases where model versions differ but base model is the same
	// Return the item from whitelist (not value) to use the actual name from allowedModels
	for _, item := range wl {
		if schemas.SameBaseModel(item, value) {
			return item
		}
	}
	return ""
}

// findDeploymentMatch finds a matching deployment value in the deployments map,
// considering both exact match and base model matches (ignoring version suffixes).
// Returns the deployment value and alias if found, empty strings otherwise.
func findDeploymentMatch(deployments map[string]string, modelID string) (deploymentValue, alias string) {
	// Check exact match first (by alias/key)
	if deployment, ok := deployments[modelID]; ok {
		return deployment, modelID
	}

	// Check exact match by deployment value
	for aliasKey, depValue := range deployments {
		if depValue == modelID {
			return depValue, aliasKey
		}
	}

	// Additional layer: check base model matches (ignoring version suffixes)
	// This handles cases where model versions differ but base model is the same
	for aliasKey, deploymentValue := range deployments {
		// Check if modelID's base matches deploymentValue's base
		if schemas.SameBaseModel(deploymentValue, modelID) {
			return deploymentValue, aliasKey
		}
		// Also check if modelID's base matches alias's base (for cases where alias is used as deployment)
		if schemas.SameBaseModel(aliasKey, modelID) {
			return deploymentValue, aliasKey
		}
	}
	return "", ""
}

// matchesBlacklist reports whether modelID matches any entry in the blacklist,
// using the same matching logic as findMatchingAllowedModel (exact and base-model).
func matchesBlacklist(bl schemas.BlackList, modelID string) bool {
	if bl.IsEmpty() {
		return false
	}
	if bl.Contains(modelID) {
		return true
	}
	for _, item := range bl {
		if schemas.SameBaseModel(item, modelID) {
			return true
		}
	}
	return false
}

func (response *AzureListModelsResponse) ToBifrostListModelsResponse(allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, deployments map[string]string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
	}

	if !unfiltered && (allowedModels.IsEmpty() && len(deployments) == 0 || blacklistedModels.IsBlockAll()) {
		return bifrostResponse
	}

	restrictAllowed := !unfiltered && allowedModels.IsRestricted()

	includedModels := make(map[string]bool)
	for _, model := range response.Data {
		modelID := model.ID
		matchedAllowedModel := ""
		deploymentValue := ""
		deploymentAlias := ""

		// Filter if model is not present in both lists (when both are non-empty)
		// Empty lists mean "allow all" for that dimension
		// Check considering base model matches (ignoring version suffixes)
		shouldFilter := false
		if restrictAllowed && len(deployments) > 0 {
			// Both lists are present: model must be in allowedModels AND deployments
			// AND the deployment alias must also be in allowedModels
			matchedAllowedModel = findMatchingAllowedModel(allowedModels, model.ID)
			deploymentValue, deploymentAlias = findDeploymentMatch(deployments, model.ID)
			inDeployments := deploymentAlias != ""

			// Check if deployment alias is also in allowedModels (direct string match)
			deploymentAliasInAllowedModels := false
			if deploymentAlias != "" {
				deploymentAliasInAllowedModels = allowedModels.Contains(deploymentAlias)
			}

			// Filter if: model not in deployments OR deployment alias not in allowedModels
			shouldFilter = !inDeployments || !deploymentAliasInAllowedModels
		} else if restrictAllowed {
			// Only allowedModels is present: filter if model is not in allowedModels
			matchedAllowedModel = findMatchingAllowedModel(allowedModels, model.ID)
			shouldFilter = matchedAllowedModel == ""
		} else if !unfiltered && len(deployments) > 0 {
			// Only deployments is present: filter if model is not in deployments
			deploymentValue, deploymentAlias = findDeploymentMatch(deployments, model.ID)
			shouldFilter = deploymentValue == ""
		}
		// If both are empty (or allowedModels is unrestricted and no deployments), shouldFilter remains false

		if shouldFilter {
			continue
		}
		if !unfiltered && (matchesBlacklist(blacklistedModels, model.ID) ||
			(deploymentAlias != "" && matchesBlacklist(blacklistedModels, deploymentAlias))) {
			continue
		}

		// Use the matched name from allowedModels or deployments (like Anthropic)
		// Priority: deployment value > matched allowedModel > original model.ID
		if deploymentValue != "" {
			modelID = deploymentValue
		} else if matchedAllowedModel != "" {
			modelID = matchedAllowedModel
		}

		modelEntry := schemas.Model{
			ID:      string(schemas.Azure) + "/" + modelID,
			Created: schemas.Ptr(model.CreatedAt),
		}
		// Set deployment info if matched via deployments
		if deploymentValue != "" && deploymentAlias != "" {
			modelEntry.ID = string(schemas.Azure) + "/" + deploymentAlias
			modelEntry.Deployment = schemas.Ptr(deploymentValue)
			includedModels[strings.ToLower(deploymentAlias)] = true
		} else {
			includedModels[strings.ToLower(modelID)] = true
		}

		bifrostResponse.Data = append(bifrostResponse.Data, modelEntry)
	}

	// Backfill deployments that were not matched from the API response
	if !unfiltered && len(deployments) > 0 {
		for alias, deploymentValue := range deployments {
			if includedModels[strings.ToLower(alias)] {
				continue
			}
			// If allowedModels is restricted, only include if alias is in the list
			if restrictAllowed && !allowedModels.Contains(alias) {
				continue
			}
			if !unfiltered && matchesBlacklist(blacklistedModels, alias) {
				continue
			}
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
				ID:         string(schemas.Azure) + "/" + alias,
				Name:       schemas.Ptr(alias),
				Deployment: schemas.Ptr(deploymentValue),
			})
			includedModels[strings.ToLower(alias)] = true
		}
	}

	// Backfill allowed models that were not in the response
	if restrictAllowed {
		for _, allowedModel := range allowedModels {
			if matchesBlacklist(blacklistedModels, allowedModel) {
				continue
			}
			if !includedModels[strings.ToLower(allowedModel)] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   string(schemas.Azure) + "/" + allowedModel,
					Name: schemas.Ptr(allowedModel),
				})
			}
		}
	}

	return bifrostResponse
}
