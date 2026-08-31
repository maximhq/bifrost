package databricks

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// listModelsByKey intentionally returns no live models. Databricks exposes model inventory
// through workspace APIs whose resource names do not consistently match inference model IDs.
// Until that mapping is reliable, the framework model catalog uses its datasheet
// (governance_model_pricing) as the sole source of Databricks models.
func (provider *DatabricksProvider) listModelsByKey(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return &schemas.BifrostListModelsResponse{Data: []schemas.Model{}}, nil
}

// ListModels returns an empty successful live result for every configured key. The shared
// multi-key helper preserves normal key-status handling while ensuring no Databricks list-models
// HTTP request is made. The HTTP transport subsequently serves Databricks models exclusively
// from the datasheet-backed model catalog.
func (provider *DatabricksProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, provider.listModelsByKey)
}
