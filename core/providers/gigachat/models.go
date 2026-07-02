package gigachat

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostListModelsResponse converts GigaChat model metadata to Bifrost format.
func (response *GigaChatListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
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
		ProviderKey:       providerKey,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)
	for _, model := range response.Data {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}

		for _, result := range pipeline.FilterModel(modelID) {
			entry := schemas.Model{
				ID:               string(providerKey) + "/" + result.ResolvedID,
				OwnedBy:          stringPtrIfNotEmpty(model.OwnedBy),
				SupportedMethods: toGigaChatSupportedMethods(model.Type),
			}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data, pipeline.BackfillModels(included)...)
	return bifrostResponse
}

func stringPtrIfNotEmpty(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return schemas.Ptr(value)
}

func toGigaChatSupportedMethods(modelType string) []string {
	switch strings.ToLower(strings.TrimSpace(modelType)) {
	case "chat":
		return []string{
			string(schemas.ChatCompletionRequest),
			string(schemas.ChatCompletionStreamRequest),
			string(schemas.ResponsesRequest),
			string(schemas.ResponsesStreamRequest),
		}
	case "embedder", "embedding", "embeddings":
		return []string{string(schemas.EmbeddingRequest)}
	default:
		return nil
	}
}
