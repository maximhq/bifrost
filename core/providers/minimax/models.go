package minimax

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func staticModelsResponse(providerKey schemas.ModelProvider, key schemas.Key, unfiltered bool) *schemas.BifrostListModelsResponse {
	response := &schemas.BifrostListModelsResponse{Data: make([]schemas.Model, 0, len(supportedSpeechModels))}
	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     key.Models,
		BlacklistedModels: key.BlacklistedModels,
		Aliases:           key.Aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       providerKey,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return response
	}

	included := make(map[string]bool, len(supportedSpeechModels))
	for _, model := range supportedSpeechModels {
		for _, result := range pipeline.FilterModel(model) {
			entry := schemas.Model{
				ID:               string(providerKey) + "/" + result.ResolvedID,
				OwnedBy:          schemas.Ptr("minimax"),
				SupportedMethods: []string{string(schemas.SpeechRequest), string(schemas.SpeechStreamRequest)},
			}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			response.Data = append(response.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}
	response.Data = append(response.Data, pipeline.BackfillModels(included)...)
	return response
}
