package deepgram

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func (response *DeepgramListModelsResponse) ToBifrostListModelsResponse(
	providerKey schemas.ModelProvider,
	allowedModels schemas.WhiteList,
	blacklistedModels schemas.BlackList,
	aliases schemas.KeyAliases,
	unfiltered bool,
) *schemas.BifrostListModelsResponse {

	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{},
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

	// STT models
	for _, model := range response.STT {
		for _, result := range pipeline.FilterModel(model.CanonicalName) {
			entry := schemas.Model{
				ID:   string(providerKey) + "/" + result.ResolvedID,
				Name: schemas.Ptr(model.Name),
			}

			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}

			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	// TTS models
	for _, model := range response.TTS {
		for _, result := range pipeline.FilterModel(model.CanonicalName) {
			entry := schemas.Model{
				ID:   string(providerKey) + "/" + result.ResolvedID,
				Name: schemas.Ptr(model.Name),
			}

			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}

			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	bifrostResponse.Data = append(
		bifrostResponse.Data,
		pipeline.BackfillModels(included)...,
	)

	return bifrostResponse
}