package anthropic

import "github.com/maximhq/bifrost/core/schemas"

func (response *AnthropicModelListResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{},
	}

	for _, model := range response.Data {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID: model.ID,
			Name: schemas.Ptr(model.DisplayName),
			Created: schemas.Ptr(int(model.CreatedAt.Unix())),
		})
	}

	return bifrostResponse
}