package azure

import (
	"maps"
	"slices"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// azureSpeechModels returns the Azure AI Speech models a key with use_speech_endpoints serves:
// "neural", each voice suffix in voices, and the transcription models.
func azureSpeechModels(voices []AzureSpeechVoice) []AzureModel {
	suffixes := make(map[string]struct{})
	for _, voice := range voices {
		if _, suffix, ok := strings.Cut(voice.ShortName, ":"); ok && suffix != "" {
			suffixes[suffix] = struct{}{}
		}
	}
	ids := slices.Concat([]string{azureSpeechNeuralModel}, slices.Sorted(maps.Keys(suffixes)), azureSpeechTranscriptionModels)
	models := make([]AzureModel, 0, len(ids))
	for _, id := range ids {
		models = append(models, AzureModel{ID: id})
	}
	return models
}

func (response *AzureListModelsResponse) ToBifrostListModelsResponse(allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       schemas.Azure,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	for _, model := range response.Data {
		for _, result := range pipeline.FilterModel(model.ID) {
			entry := schemas.Model{
				ID: string(schemas.Azure) + "/" + result.ResolvedID,
			}
			if model.CreatedAt != 0 {
				entry.Created = schemas.Ptr(model.CreatedAt)
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
