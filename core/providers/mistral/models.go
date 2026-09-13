package mistral

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func (response *MistralListModelsResponse) ToBifrostListModelsResponse(access schemas.ModelAccessRule, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		Access:      access,
		Aliases:     aliases,
		Unfiltered:  unfiltered,
		ProviderKey: schemas.Mistral,
		MatchFns:    providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	for _, model := range response.Data {
		for _, result := range pipeline.FilterModel(model.ID) {
			entry := schemas.Model{
				ID:            string(schemas.Mistral) + "/" + result.ResolvedID,
				Name:          schemas.Ptr(model.Name),
				Description:   schemas.Ptr(model.Description),
				Created:       schemas.Ptr(model.Created),
				ContextLength: schemas.Ptr(int(model.MaxContextLength)),
				OwnedBy:       schemas.Ptr(model.OwnedBy),
			}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

	return bifrostResponse
}
