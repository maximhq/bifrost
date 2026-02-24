package replicate

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostListModelsResponse converts Replicate models and deployments to a Bifrost list models response
func ToBifrostListModelsResponse(
	deploymentsResponse *ReplicateDeploymentListResponse,
	providerKey schemas.ModelProvider,
	allowedModels []string,
	unfiltered bool,
) *schemas.BifrostListModelsResponse {
	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0),
	}

	includedModels := make(map[string]bool)
	// Add deployments from /v1/deployments endpoint
	if deploymentsResponse != nil {
		for _, deployment := range deploymentsResponse.Results {
			deploymentID := deployment.Owner + "/" + deployment.Name

			modelName := schemas.Ptr(deployment.Name)
			var created *int64

			if !unfiltered && len(allowedModels) > 0 && !slices.Contains(allowedModels, deploymentID) {
				continue
			}

			// Extract information from current release if available
			if deployment.CurrentRelease != nil {
				// Parse created timestamp
				if deployment.CurrentRelease.CreatedAt != "" {
					createdTimestamp := ParseReplicateTimestamp(deployment.CurrentRelease.CreatedAt)
					if createdTimestamp > 0 {
						created = schemas.Ptr(createdTimestamp)
					}
				}
			}

			bifrostModel := schemas.Model{
				ID:         string(providerKey) + "/" + deploymentID,
				Name:       modelName,
				Deployment: modelName,
				OwnedBy:    schemas.Ptr(deployment.Owner),
				Created:    created,
			}

			bifrostResponse.Data = append(bifrostResponse.Data, bifrostModel)
			includedModels[deploymentID] = true
		}

		if deploymentsResponse.Next != nil {
			bifrostResponse.NextPageToken = *deploymentsResponse.Next
		}
	}

	// Backfill allowed models that were not in the response
	if !unfiltered && len(allowedModels) > 0 {
		for _, allowedModel := range allowedModels {
			if !includedModels[allowedModel] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   string(providerKey) + "/" + allowedModel,
					Name: schemas.Ptr(allowedModel),
				})
			}
		}
	}

	return bifrostResponse
}

// ToReplicateListModelsResponse converts a Bifrost list models response to a Replicate list models response
// This is mainly used for testing and compatibility
func ToReplicateListModelsResponse(response *schemas.BifrostListModelsResponse) *ReplicateModelListResponse {
	if response == nil {
		return nil
	}

	replicateResponse := &ReplicateModelListResponse{
		Results: make([]ReplicateModelResponse, 0, len(response.Data)),
	}

	for _, model := range response.Data {
		modelID := strings.TrimPrefix(model.ID, string(schemas.Replicate)+"/")
		replicateModel := ReplicateModelResponse{
			URL:  "https://replicate.com/" + modelID,
			Name: modelID,
		}

		if model.Description != nil {
			replicateModel.Description = model.Description
		}

		if model.OwnedBy != nil {
			replicateModel.Owner = *model.OwnedBy
		}

		replicateResponse.Results = append(replicateResponse.Results, replicateModel)
	}

	// Set next page token if available
	if response.NextPageToken != "" {
		next := response.NextPageToken
		replicateResponse.Next = &next
	}

	return replicateResponse
}
