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

			var created *int64
			if deployment.CurrentRelease != nil && deployment.CurrentRelease.CreatedAt != "" {
				createdTimestamp := ParseReplicateTimestamp(deployment.CurrentRelease.CreatedAt)
				if createdTimestamp > 0 {
					created = schemas.Ptr(createdTimestamp)
				}
			}

			for _, result := range pipeline.FilterModel(deploymentID) {
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
		}

		if deploymentsResponse.Next != nil {
			bifrostResponse.NextPageToken = *deploymentsResponse.Next
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

	return bifrostResponse
}
