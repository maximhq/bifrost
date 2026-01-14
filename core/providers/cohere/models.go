package cohere

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *CohereListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels []string) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}

	for _, model := range response.Models {
		if len(allowedModels) > 0 && !slices.Contains(allowedModels, model.Name) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:               string(providerKey) + "/" + model.Name,
			Name:             schemas.Ptr(model.Name),
			ContextLength:    schemas.Ptr(int(model.ContextLength)),
			SupportedMethods: model.Endpoints,
		})
	}

	return bifrostResponse
}
