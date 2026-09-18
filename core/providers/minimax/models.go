package minimax

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

var languageModelMethods = []string{
	string(schemas.ChatCompletionRequest),
	string(schemas.ChatCompletionStreamRequest),
	string(schemas.ResponsesRequest),
	string(schemas.ResponsesStreamRequest),
}

var speechModelMethods = []string{
	string(schemas.SpeechRequest),
	string(schemas.SpeechStreamRequest),
}

func staticModelsResponse(providerKey schemas.ModelProvider, key schemas.Key, unfiltered bool) *schemas.BifrostListModelsResponse {
	response := &schemas.BifrostListModelsResponse{Data: make([]schemas.Model, 0, len(supportedLanguageModels)+len(supportedSpeechModels))}
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

	included := make(map[string]bool, len(supportedLanguageModels)+len(supportedSpeechModels))
	appendModels := func(models, methods []string) {
		for _, model := range models {
			for _, result := range pipeline.FilterModel(model) {
				entry := schemas.Model{
					ID:               string(providerKey) + "/" + result.ResolvedID,
					OwnedBy:          schemas.Ptr("minimax"),
					SupportedMethods: append([]string(nil), methods...),
				}
				if result.AliasValue != "" {
					entry.Alias = schemas.Ptr(result.AliasValue)
				}
				response.Data = append(response.Data, entry)
				included[strings.ToLower(result.ResolvedID)] = true
			}
		}
	}
	appendModels(supportedLanguageModels, languageModelMethods)
	appendModels(supportedSpeechModels, speechModelMethods)
	response.Data = append(response.Data, pipeline.BackfillModels(included)...)
	return response
}
