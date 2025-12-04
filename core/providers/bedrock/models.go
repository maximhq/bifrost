package bedrock

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const globalPrefix = "global."

// findMatchingAllowedModel finds a matching item in a slice, considering both
// exact match and match with/without the "global." prefix,
// and also checks base model matches (ignoring version suffixes).
// Returns the matched item from the slice if found, empty string otherwise.
// If matched via base model, returns the item from slice (not the value parameter).
func findMatchingAllowedModel(slice []string, value string) string {
	// First check exact matches with global prefix variations
	if slices.Contains(slice, value) {
		return value
	}
	// Check with global prefix added/removed
	if strings.HasPrefix(value, globalPrefix) {
		withoutPrefix := strings.TrimPrefix(value, globalPrefix)
		if slices.Contains(slice, withoutPrefix) {
			return withoutPrefix
		}
	} else {
		// Check with global prefix added
		withPrefix := globalPrefix + value
		if slices.Contains(slice, withPrefix) {
			return withPrefix
		}
	}

	// Additional layer: check base model matches (ignoring version suffixes)
	// This handles cases where model versions differ but base model is the same
	// Normalize value by removing global prefix for base model comparison
	valueNormalized := value
	if strings.HasPrefix(value, globalPrefix) {
		valueNormalized = strings.TrimPrefix(value, globalPrefix)
	}

	for _, item := range slice {
		// Normalize item by removing global prefix for base model comparison
		itemNormalized := item
		if strings.HasPrefix(item, globalPrefix) {
			itemNormalized = strings.TrimPrefix(item, globalPrefix)
		}

		// Check base model match with normalized values (prefix removed from both)
		// Return the item from slice (not value) to use the actual name from allowedModels
		if schemas.SameBaseModel(itemNormalized, valueNormalized) {
			return item
		}
	}
	return ""
}

// findDeploymentMatch finds a matching deployment value in the deployments map,
// considering both exact match and match with/without "global." prefix,
// and also checks base model matches (ignoring version suffixes).
// The modelID from the API response should match a deployment value (not the alias/key).
// Returns the deployment value and alias if found, empty strings otherwise.
func findDeploymentMatch(deployments map[string]string, modelID string) (deploymentValue, alias string) {
	// Check if any deployment value matches the modelID (with or without prefix)
	for aliasKey, deploymentValue := range deployments {
		// Exact match
		if deploymentValue == modelID {
			return deploymentValue, aliasKey
		}
		// Check if modelID matches deployment value with global prefix variations
		if strings.HasPrefix(deploymentValue, globalPrefix) {
			// deploymentValue has prefix, check if modelID matches without prefix
			if strings.TrimPrefix(deploymentValue, globalPrefix) == modelID {
				return deploymentValue, aliasKey
			}
		} else {
			// deploymentValue doesn't have prefix, check if modelID matches with prefix
			if globalPrefix+deploymentValue == modelID {
				return deploymentValue, aliasKey
			}
		}
		// Check reverse: modelID has prefix, deployment value doesn't
		if strings.HasPrefix(modelID, globalPrefix) {
			if strings.TrimPrefix(modelID, globalPrefix) == deploymentValue {
				return deploymentValue, aliasKey
			}
		} else {
			// modelID doesn't have prefix, deployment value does
			if globalPrefix+modelID == deploymentValue {
				return deploymentValue, aliasKey
			}
		}

		// Additional layer: check base model matches (ignoring version suffixes)
		// This handles cases where model versions differ but base model is the same
		// Normalize both values by removing global prefix for base model comparison
		deploymentNormalized := deploymentValue
		if strings.HasPrefix(deploymentValue, globalPrefix) {
			deploymentNormalized = strings.TrimPrefix(deploymentValue, globalPrefix)
		}
		modelIDNormalized := modelID
		if strings.HasPrefix(modelID, globalPrefix) {
			modelIDNormalized = strings.TrimPrefix(modelID, globalPrefix)
		}

		// Check base model match with normalized values (prefix removed from both)
		if schemas.SameBaseModel(deploymentNormalized, modelIDNormalized) {
			return deploymentValue, aliasKey
		}
	}
	return "", ""
}

func (response *BedrockListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels []string, deployments map[string]string) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.ModelSummaries)),
	}

	deploymentValues := make([]string, 0, len(deployments))
	for _, deployment := range deployments {
		deploymentValues = append(deploymentValues, deployment)
	}

	for _, model := range response.ModelSummaries {
		modelID := model.ModelID
		matchedAllowedModel := ""
		deploymentValue := ""
		deploymentAlias := ""

		// Filter if model is not present in both lists (when both are non-empty)
		// Empty lists mean "allow all" for that dimension
		// Check considering global prefix variations
		shouldFilter := false
		if len(allowedModels) > 0 && len(deploymentValues) > 0 {
			// Both lists are present: model must be in allowedModels AND deployments
			// AND the deployment alias must also be in allowedModels
			matchedAllowedModel = findMatchingAllowedModel(allowedModels, model.ModelID)
			deploymentValue, deploymentAlias = findDeploymentMatch(deployments, model.ModelID)
			inDeployments := deploymentAlias != ""

			// Check if deployment alias is also in allowedModels (direct string match)
			deploymentAliasInAllowedModels := false
			if deploymentAlias != "" {
				deploymentAliasInAllowedModels = slices.Contains(allowedModels, deploymentAlias)
			}

			// Filter if: model not in deployments OR deployment alias not in allowedModels
			shouldFilter = !inDeployments || !deploymentAliasInAllowedModels
		} else if len(allowedModels) > 0 {
			// Only allowedModels is present: filter if model is not in allowedModels
			matchedAllowedModel = findMatchingAllowedModel(allowedModels, model.ModelID)
			shouldFilter = matchedAllowedModel == ""
		} else if len(deploymentValues) > 0 {
			// Only deployments is present: filter if model is not in deployments
			deploymentValue, deploymentAlias = findDeploymentMatch(deployments, model.ModelID)
			shouldFilter = deploymentValue == ""
		}
		// If both are empty, shouldFilter remains false (allow all)

		if shouldFilter {
			continue
		}

		// Use the matched name from allowedModels or deployments (like Anthropic)
		// Priority: deployment value > matched allowedModel > original model.ModelID
		if deploymentValue != "" {
			modelID = deploymentValue
		} else if matchedAllowedModel != "" {
			modelID = matchedAllowedModel
		}

		modelEntry := schemas.Model{
			ID:      string(providerKey) + "/" + modelID,
			Name:    schemas.Ptr(model.ModelName),
			OwnedBy: schemas.Ptr(model.ProviderName),
			Architecture: &schemas.Architecture{
				InputModalities:  model.InputModalities,
				OutputModalities: model.OutputModalities,
			},
		}
		// Set deployment info if matched via deployments
		if deploymentValue != "" && deploymentAlias != "" {
			modelEntry.ID = string(providerKey) + "/" + deploymentAlias
			// Use the actual deployment value (which might have global prefix)
			modelEntry.Deployment = schemas.Ptr(deploymentValue)
		}
		bifrostResponse.Data = append(bifrostResponse.Data, modelEntry)
	}

	return bifrostResponse
}
