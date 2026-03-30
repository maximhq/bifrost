package replicate

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostListModelsResponse converts Replicate deployments to a Bifrost list models response.
// Replicate model IDs are composite: "{owner}/{name}" (e.g. "stability-ai/stable-diffusion").
func ToBifrostListModelsResponse(
	deploymentsResponse *ReplicateDeploymentListResponse,
	providerKey schemas.ModelProvider,
	allowedModels schemas.WhiteList,
	blacklistedModels schemas.BlackList,
	aliases map[string]string,
	unfiltered bool,
) *schemas.BifrostListModelsResponse {
	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       providerKey,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	if deploymentsResponse != nil {
		for _, deployment := range deploymentsResponse.Results {
			// Replicate model IDs are composite owner/name
			deploymentID := deployment.Owner + "/" + deployment.Name

			result := pipeline.FilterModel(deploymentID)
			if !result.Include {
				continue
			}

			var created *int64
			if deployment.CurrentRelease != nil && deployment.CurrentRelease.CreatedAt != "" {
				createdTimestamp := ParseReplicateTimestamp(deployment.CurrentRelease.CreatedAt)
				if createdTimestamp > 0 {
					created = schemas.Ptr(createdTimestamp)
				}
			}

			bifrostModel := schemas.Model{
				ID:      string(providerKey) + "/" + result.ResolvedID,
				Name:    schemas.Ptr(deployment.Name),
				OwnedBy: schemas.Ptr(deployment.Owner),
				Created: created,
			}
			if result.AliasValue != "" {
				bifrostModel.Alias = schemas.Ptr(result.AliasValue)
			}

			bifrostResponse.Data = append(bifrostResponse.Data, bifrostModel)
			included[strings.ToLower(result.ResolvedID)] = true
		}

		if deploymentsResponse.Next != nil {
			bifrostResponse.NextPageToken = *deploymentsResponse.Next
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

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
