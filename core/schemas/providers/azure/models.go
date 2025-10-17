package azure

import "github.com/maximhq/bifrost/core/schemas"

func (response *AzureModelListResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{},
	}

	for _, model := range response.Data {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID: model.ID,
			Created: schemas.Ptr(model.CreatedAt),
			Name: schemas.Ptr(model.Model),
		})
	}
	return bifrostResponse
}