package openai

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostListModelsResponse converts an OpenAI list models response to a Bifrost list models response
func (response *OpenAIListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels []string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
	}

	includedModels := make(map[string]bool)
	for _, model := range response.Data {
		if !unfiltered && len(allowedModels) > 0 && !slices.Contains(allowedModels, model.ID) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:            string(providerKey) + "/" + model.ID,
			Created:       model.Created,
			OwnedBy:       schemas.Ptr(model.OwnedBy),
			ContextLength: model.ContextWindow,
		})
		includedModels[model.ID] = true
	}

	// Backfill allowed models that were not in the response
	if !unfiltered && len(allowedModels) > 0 {
		for _, allowedModel := range allowedModels {
			if !includedModels[allowedModel] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   string(providerKey) + "/" + allowedModel,
					Name: schemas.Ptr(allowedModel),
				})
			}
		}
	}

	return bifrostResponse
}

// ToOpenAIListModelsResponse converts a Bifrost list models response to an OpenAI list models response
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
