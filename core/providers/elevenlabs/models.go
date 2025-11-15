package elevenlabs

import "github.com/maximhq/bifrost/core/schemas"

func (response *ElevenlabsListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(*response)),
	}

	for _, model := range *response {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:   string(providerKey) + "/" + model.ModelID,
			Name: schemas.Ptr(model.Name),
		})
	}

	return bifrostResponse
}