package cohere

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func (response *CohereModelListResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{},
	}

	for _, model := range response.Models {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID: model.Name,
			Name: schemas.Ptr(model.Name),
			ContextLength: schemas.Ptr(int(model.ContextLength)),
			SupportedMethods: model.Endpoints,
		})
	}

	return bifrostResponse
}