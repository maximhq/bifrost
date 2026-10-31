package databricks

import (
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// listModelsByKey returns only the key's configured models and aliases; workspace inventory APIs
// are never called because their resource names do not consistently match inference model IDs.
// The framework model catalog unions this with its datasheet (governance_model_pricing).
// The filtered pipeline path is forced as in BytePlus: an unfiltered request skips config backfill.
func (provider *DatabricksProvider) listModelsByKey(_ *schemas.BifrostContext, key schemas.Key, _ *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return (&openai.OpenAIListModelsResponse{}).ToBifrostListModelsResponse(
		schemas.Databricks,
		key.Models,
		key.BlacklistedModels,
		key.Aliases,
		false,
	), nil
}

// ListModels returns each key's configured models and aliases without any Databricks list-models
// HTTP request. The shared multi-key helper preserves normal key-status handling.
func (provider *DatabricksProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, provider.listModelsByKey)
}
