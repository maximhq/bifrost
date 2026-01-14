package mistral

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *MistralListModelsResponse) ToBifrostListModelsResponse(allowedModels []string) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
	}

	for _, model := range response.Data {
		if len(allowedModels) > 0 && !slices.Contains(allowedModels, model.ID) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:            string(schemas.Mistral) + "/" + model.ID,
			Name:          schemas.Ptr(model.Name),
			Description:   schemas.Ptr(model.Description),
			Created:       schemas.Ptr(model.Created),
			ContextLength: schemas.Ptr(int(model.MaxContextLength)),
			OwnedBy:       schemas.Ptr(model.OwnedBy),
		})

	}

	return bifrostResponse
}
