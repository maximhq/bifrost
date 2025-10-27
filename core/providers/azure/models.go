package azure

import "github.com/maximhq/bifrost/core/schemas"

func (response *AzureListModelsResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
	}

	for _, model := range response.Data {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:      string(schemas.Azure) + "/" + model.ID,
			Created: schemas.Ptr(model.CreatedAt),
			Name:    schemas.Ptr(model.Model),
		})
	}
	return bifrostResponse
}
