package mistral

import "github.com/maximhq/bifrost/core/schemas"

func (response *MistralModelListResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{},
	}

	for _, model := range response.Data {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID: model.ID,
			Name: schemas.Ptr(model.Name),
			Description: schemas.Ptr(model.Description),
			Created: schemas.Ptr(int(model.Created)),
			ContextLength: schemas.Ptr(int(model.MaxContextLength)),
			OwnedBy: schemas.Ptr(model.OwnedBy),
		})

	}

	return bifrostResponse
}