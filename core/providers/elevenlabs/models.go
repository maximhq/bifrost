package elevenlabs

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *ElevenlabsListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels []string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(*response)),
	}

	includedModels := make(map[string]bool)
	for _, model := range *response {
		if !unfiltered && len(allowedModels) > 0 && !slices.Contains(allowedModels, model.ModelID) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:   string(providerKey) + "/" + model.ModelID,
			Name: schemas.Ptr(model.Name),
		})
		includedModels[model.ModelID] = true
	}

	// Backfill allowed models that were not in the response
	if !unfiltered && len(allowedModels) > 0 {
		for _, allowedModel := range allowedModels {
			if !includedModels[allowedModel] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   string(providerKey) + "/" + allowedModel,
					Name: schemas.Ptr(allowedModel),
				})
			}
		}
	}

	return bifrostResponse
}
