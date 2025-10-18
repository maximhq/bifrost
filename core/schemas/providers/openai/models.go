package openai

import "github.com/maximhq/bifrost/core/schemas"

func (response *OpenAIModelListResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{},
	}

	for _, model := range response.Data {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID: model.ID,
			Created: schemas.Ptr(int(model.Created)),
			OwnedBy: schemas.Ptr(model.OwnedBy),
			ContextLength: model.ContextWindow,
		})

	}

	return bifrostResponse
}