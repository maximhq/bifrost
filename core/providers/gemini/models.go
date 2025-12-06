package gemini

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

func (response *GeminiListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels []string) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}

	for _, model := range response.Models {

		contextLength := model.InputTokenLimit + model.OutputTokenLimit
		// Remove prefix models/ from model.Name
		modelName := strings.TrimPrefix(model.Name, "models/")
		if len(allowedModels) > 0 && !slices.Contains(allowedModels, modelName) {
			continue
		}
		bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
			ID:               string(providerKey) + "/" + modelName,
			Name:             schemas.Ptr(model.DisplayName),
			Description:      schemas.Ptr(model.Description),
			ContextLength:    schemas.Ptr(int(contextLength)),
			MaxInputTokens:   schemas.Ptr(model.InputTokenLimit),
			MaxOutputTokens:  schemas.Ptr(model.OutputTokenLimit),
			SupportedMethods: model.SupportedGenerationMethods,
		})
	}

	return bifrostResponse
}

func ToGeminiListModelsResponse(resp *schemas.BifrostListModelsResponse) *GeminiListModelsResponse {
	if resp == nil {
		return nil
	}

	geminiResponse := &GeminiListModelsResponse{
		Models:        make([]GeminiModel, 0, len(resp.Data)),
		NextPageToken: resp.NextPageToken,
	}

	for _, model := range resp.Data {
		geminiModel := GeminiModel{
			Name:                       model.ID,
			SupportedGenerationMethods: model.SupportedMethods,
		}
		if model.Name != nil {
			geminiModel.DisplayName = *model.Name
		}
		if model.Description != nil {
			geminiModel.Description = *model.Description
		}
		if model.MaxInputTokens != nil {
			geminiModel.InputTokenLimit = *model.MaxInputTokens
		}
		if model.MaxOutputTokens != nil {
			geminiModel.OutputTokenLimit = *model.MaxOutputTokens
		}

		geminiResponse.Models = append(geminiResponse.Models, geminiModel)
	}

	return geminiResponse
}
