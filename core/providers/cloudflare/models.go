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

// cloudflareDecisionModelPrefix names the Clef decision models, which Workers AI files
// under Text Generation but which serve decisions, not chat completions.
const cloudflareDecisionModelPrefix = cloudflareModelNamespace + "clef"

// ToBifrostListModelsResponse converts the Workers AI model search response into
// Bifrost's list models shape. The Clef models Bifrost serves are listed by their bare
// names ("clef-flash"); other Clef variants are left out.
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
		name := model.Name
		if strings.HasPrefix(name, cloudflareDecisionModelPrefix) {
			if !schemas.IsClefModel(name) {
				continue
			}
			name = clefModelName(name)
		}
		for _, result := range pipeline.FilterModel(name) {
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
