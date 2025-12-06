package vertex

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// findDeploymentMatch finds a matching deployment value in the deployments map.
// Returns the deployment value and alias if found, empty strings otherwise.
func findDeploymentMatch(deployments map[string]string, customModelID string) (deploymentValue, alias string) {
	// Check exact match by deployment value
	for aliasKey, depValue := range deployments {
		if depValue == customModelID {
			return depValue, aliasKey
		}
	}
	// Check exact match by alias/key
	if deployment, ok := deployments[customModelID]; ok {
		return deployment, customModelID
	}
	return "", ""
}

func (response *VertexListModelsResponse) ToBifrostListModelsResponse(allowedModels []string, deployments map[string]string) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}
	for _, model := range response.Models {
		if len(model.DeployedModels) == 0 {
			continue
		}
		for _, deployedModel := range model.DeployedModels {
			endpoint := strings.TrimSuffix(deployedModel.Endpoint, "/")
			parts := strings.Split(endpoint, "/")
			if len(parts) == 0 {
				continue
			}
			customModelID := parts[len(parts)-1]
			if customModelID == "" {
				continue
			}

			// Filter if model is not present in both lists (when both are non-empty)
			// Empty lists mean "allow all" for that dimension
			var deploymentValue, deploymentAlias string
			shouldFilter := false
			if len(allowedModels) > 0 && len(deployments) > 0 {
				// Both lists are present: model must be in allowedModels AND deployments
				// AND the deployment alias must also be in allowedModels
				deploymentValue, deploymentAlias = findDeploymentMatch(deployments, customModelID)
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
				shouldFilter = !slices.Contains(allowedModels, customModelID)
			} else if len(deployments) > 0 {
				// Only deployments is present: filter if model is not in deployments
				deploymentValue, deploymentAlias = findDeploymentMatch(deployments, customModelID)
				shouldFilter = deploymentValue == ""
			}
			// If both are empty, shouldFilter remains false (allow all)

			if shouldFilter {
				continue
			}

			modelID := customModelID

			modelEntry := schemas.Model{
				ID:          string(schemas.Vertex) + "/" + modelID,
				Name:        schemas.Ptr(model.DisplayName),
				Description: schemas.Ptr(model.Description),
				Created:     schemas.Ptr(model.VersionCreateTime.Unix()),
			}
			// Set deployment info if matched via deployments
			if deploymentValue != "" && deploymentAlias != "" {
				modelEntry.ID = string(schemas.Vertex) + "/" + deploymentAlias
				modelEntry.Deployment = schemas.Ptr(deploymentValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, modelEntry)
		}
	}
	bifrostResponse.NextPageToken = response.NextPageToken

	return bifrostResponse
}
