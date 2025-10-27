package bedrock

import "github.com/maximhq/bifrost/core/schemas"

func (response *BedrockListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.ModelSummaries)),
	}

	for _, model := range response.ModelSummaries {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:      string(providerKey) + "/" + model.ModelID,
			Name:    schemas.Ptr(model.ModelName),
			OwnedBy: schemas.Ptr(model.ProviderName),
			Architecture: &schemas.Architecture{
				InputModalities:  model.InputModalities,
				OutputModalities: model.OutputModalities,
			},
		})
	}

	return bifrostResponse
}
