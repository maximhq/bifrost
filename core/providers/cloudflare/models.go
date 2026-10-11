package cloudflare

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// cloudflareListedTasks are the Workers AI tasks served by the OpenAI-compatible
// endpoints this provider calls; models of other tasks are not listed.
var cloudflareListedTasks = map[string]bool{
	"Text Generation": true,
	"Text Embeddings": true,
}

// ToBifrostListModelsResponse converts the Workers AI model search response into
// Bifrost's list models shape, keeping Cloudflare's model IDs as they are.
func (response *CloudflareListModelsResponse) ToBifrostListModelsResponse(allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Result)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       schemas.Cloudflare,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	for _, model := range response.Result {
		if !cloudflareListedTasks[model.Task.Name] {
			continue
		}
		for _, result := range pipeline.FilterModel(model.Name) {
			entry := schemas.Model{
				ID:          string(schemas.Cloudflare) + "/" + result.ResolvedID,
				Description: schemas.Ptr(model.Description),
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
