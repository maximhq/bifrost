package openai

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *OpenAIListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels []string) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
	}

	for _, model := range response.Data {
		if len(allowedModels) > 0 && !slices.Contains(allowedModels, model.ID) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:            string(providerKey) + "/" + model.ID,
			Created:       model.Created,
			OwnedBy:       schemas.Ptr(model.OwnedBy),
			ContextLength: model.ContextWindow,
		})

	}

	return bifrostResponse
}

func ToOpenAIListModelsResponse(response *schemas.BifrostListModelsResponse) *OpenAIListModelsResponse {

	if response == nil {
		return nil
	}

	openaiResponse := &OpenAIListModelsResponse{
		Data: make([]OpenAIModel, 0, len(response.Data)),
	}

	for _, model := range response.Data {
		openaiModel := OpenAIModel{
			ID:     model.ID,
			Object: "model",
		}
		if model.Created != nil {
			openaiModel.Created = model.Created
		}
		if model.OwnedBy != nil {
			openaiModel.OwnedBy = *model.OwnedBy
		}

		openaiResponse.Data = append(openaiResponse.Data, openaiModel)

	}

	return openaiResponse
}
