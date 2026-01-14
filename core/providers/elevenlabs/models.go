package elevenlabs

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *ElevenlabsListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels []string) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(*response)),
	}

	for _, model := range *response {
		if len(allowedModels) > 0 && !slices.Contains(allowedModels, model.ModelID) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:   string(providerKey) + "/" + model.ModelID,
			Name: schemas.Ptr(model.Name),
		})
	}

	return bifrostResponse
}
