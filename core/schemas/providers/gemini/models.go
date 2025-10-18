package gemini

import "github.com/maximhq/bifrost/core/schemas"

func (response *GeminiModelListResponse) ToBifrostListModelsResponse() *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{},
	}

	for _, model := range response.Models {
		contextLength := model.InputTokenLimit + model.OutputTokenLimit
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID: model.Name,
			Name: schemas.Ptr(model.DisplayName),
			Description: schemas.Ptr(model.Description),
			ContextLength: schemas.Ptr(int(contextLength)),
			SupportedMethods: model.SupportedGenerationMethods,
		})
	}

	return bifrostResponse
}