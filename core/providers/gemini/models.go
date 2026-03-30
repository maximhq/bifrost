package gemini

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func toGeminiModelResourceName(modelID string) string {
	if strings.HasPrefix(modelID, "models/") {
		return modelID
	}
	if idx := strings.Index(modelID, "/"); idx >= 0 && idx+1 < len(modelID) {
		return "models/" + modelID[idx+1:]
	}
	return "models/" + modelID
}

func (response *GeminiListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases map[string]string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
		Aliases:           aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       providerKey,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse
	}

	included := make(map[string]bool)

	for _, model := range response.Models {
		contextLength := model.InputTokenLimit + model.OutputTokenLimit
		// Gemini returns model names with a "models/" prefix — strip it before filtering
		// so that allowedModels entries like "gemini-1.5-pro" match correctly.
		modelName := strings.TrimPrefix(model.Name, "models/")

		result := pipeline.FilterModel(modelName)
		if !result.Include {
			continue
		}
		entry := schemas.Model{
			ID:               string(providerKey) + "/" + result.ResolvedID,
			Name:             schemas.Ptr(model.DisplayName),
			Description:      schemas.Ptr(model.Description),
			ContextLength:    schemas.Ptr(int(contextLength)),
			MaxInputTokens:   schemas.Ptr(model.InputTokenLimit),
			MaxOutputTokens:  schemas.Ptr(model.OutputTokenLimit),
			SupportedMethods: model.SupportedGenerationMethods,
		}
		if result.AliasValue != "" {
			entry.Alias = schemas.Ptr(result.AliasValue)
		}
		bifrostResponse.Data = append(bifrostResponse.Data, entry)
		included[strings.ToLower(result.ResolvedID)] = true
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

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
			Name:                       toGeminiModelResourceName(model.ID),
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
