package elevenlabs

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *ElevenlabsListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(*response)),
	}

	if !unfiltered && allowedModels.IsEmpty() {
		return bifrostResponse
	}

	includedModels := make(map[string]bool)
	for _, model := range *response {
		if !unfiltered && allowedModels.IsRestricted() && !allowedModels.Contains(model.ModelID) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:   string(providerKey) + "/" + model.ModelID,
			Name: schemas.Ptr(model.Name),
		})
		includedModels[strings.ToLower(model.ModelID)] = true
	}

	// Backfill allowed models that were not in the response
	if !unfiltered && allowedModels.IsRestricted() {
		for _, allowedModel := range allowedModels {
			if !includedModels[strings.ToLower(allowedModel)] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   string(providerKey) + "/" + allowedModel,
					Name: schemas.Ptr(allowedModel),
				})
			}
		}
	}

	return bifrostResponse
}
