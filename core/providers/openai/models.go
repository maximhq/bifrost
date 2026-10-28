package openai

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostListModelsResponse converts an OpenAI list models response to a Bifrost list models response
func (response *OpenAIListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Data)),
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

	for _, model := range response.Data {
		for _, result := range pipeline.FilterModel(model.ID) {
			entry := schemas.Model{
				ID:              string(providerKey) + "/" + result.ResolvedID,
				Created:         model.Created,
				OwnedBy:         schemas.Ptr(model.OwnedBy),
				ContextLength:   model.ContextWindow,
				MaxInputTokens:  model.MaxInputTokens,
				MaxOutputTokens: model.MaxOutputTokens,
				DisplayName:     model.DisplayName,
				Reasoning:       model.Reasoning,
			}
			if model.Modalities != nil {
				entry.Architecture = &schemas.Architecture{
					InputModalities:  model.Modalities.Input,
					OutputModalities: model.Modalities.Output,
				}
			}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

	return bifrostResponse
}

// ToBifrostModelRetrieveResponse converts an OpenAI model object to a Bifrost model retrieve response
func (model *OpenAIModel) ToBifrostModelRetrieveResponse(providerKey schemas.ModelProvider) *schemas.BifrostModelRetrieveResponse {
	if model == nil {
		return nil
	}

	// The list path reconciles context aliases at unmarshal time; the retrieve
	// path decodes a bare OpenAIModel, so reconcile here: `context_window`
	// (Groq-style) wins, falling back to `context_length`.
	contextLength := model.ContextWindow
	if contextLength == nil {
		contextLength = model.ContextLength
	}

	entry := &schemas.BifrostModelRetrieveResponse{
		Model: schemas.Model{
			ID:              string(providerKey) + "/" + model.ID,
			Created:         model.Created,
			OwnedBy:         schemas.Ptr(model.OwnedBy),
			ContextLength:   contextLength,
			MaxInputTokens:  model.MaxInputTokens,
			MaxOutputTokens: model.MaxOutputTokens,
			DisplayName:     model.DisplayName,
			Reasoning:       model.Reasoning,
			ShutdownDate:    model.ShutdownDate,
		},
	}
	if model.Modalities != nil {
		entry.Architecture = &schemas.Architecture{
			InputModalities:  model.Modalities.Input,
			OutputModalities: model.Modalities.Output,
		}
	}
	return entry
}

// ToOpenAIModelRetrieveResponse converts a Bifrost model retrieve response to an OpenAI model object.
// The ID keeps its provider prefix so this surface agrees with ToOpenAIListModelsResponse.
func ToOpenAIModelRetrieveResponse(response *schemas.BifrostModelRetrieveResponse) *OpenAIModel {
	if response == nil {
		return nil
	}

	openaiModel := &OpenAIModel{
		ID:           response.ID,
		Object:       "model",
		Created:      response.Created,
		ShutdownDate: response.ShutdownDate,

		MaxInputTokens:  response.MaxInputTokens,
		MaxOutputTokens: response.MaxOutputTokens,
		DisplayName:     response.DisplayName,
		Reasoning:       response.Reasoning,
	}
	if response.OwnedBy != nil {
		openaiModel.OwnedBy = *response.OwnedBy
	}
	// context_window keeps its legacy max_input_tokens fallback; context_length
	// reports only an actual context length and is never derived from the input limit.
	openaiModel.ContextWindow = response.ContextLength
	if openaiModel.ContextWindow == nil {
		openaiModel.ContextWindow = response.MaxInputTokens
	}
	openaiModel.ContextLength = response.ContextLength
	if response.Architecture != nil {
		openaiModel.Modalities = &OpenAIModelModalities{
			Input:  response.Architecture.InputModalities,
			Output: response.Architecture.OutputModalities,
		}
	}
	return openaiModel
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

			MaxInputTokens:  model.MaxInputTokens,
			MaxOutputTokens: model.MaxOutputTokens,
			DisplayName:     model.DisplayName,
			Reasoning:       model.Reasoning,
		}
		if model.Created != nil {
			openaiModel.Created = model.Created
		}
		if model.OwnedBy != nil {
			openaiModel.OwnedBy = *model.OwnedBy
		}
		// context_window keeps its legacy max_input_tokens fallback; context_length
		// reports only an actual context length and is never derived from the input limit.
		openaiModel.ContextWindow = model.ContextLength
		if openaiModel.ContextWindow == nil {
			openaiModel.ContextWindow = model.MaxInputTokens
		}
		openaiModel.ContextLength = model.ContextLength
		if model.Architecture != nil {
			openaiModel.Modalities = &OpenAIModelModalities{
				Input:  model.Architecture.InputModalities,
				Output: model.Architecture.OutputModalities,
			}
		}

		openaiResponse.Data = append(openaiResponse.Data, openaiModel)

	}
	return openaiResponse
}
