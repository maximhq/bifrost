package bedrock

import "github.com/maximhq/bifrost/core/schemas"

func (response *BedrockModelListResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{},
	}

	for _, model := range response.ModelSummaries {
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID: model.ModelID,
			Name: schemas.Ptr(model.ModelName),
			OwnedBy: schemas.Ptr(model.ProviderName),
			Architecture: &schemas.Architecture{
				InputModalities: model.InputModalities,
				OutputModalities: model.OutputModalities,
			},
		})
	}

	return bifrostResponse
}