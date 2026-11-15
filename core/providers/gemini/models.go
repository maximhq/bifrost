package gemini

import (
	"slices"
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

// geminiModelArchitecture derives the modality lists for a Gemini models.list entry.
// The Model resource has no modality fields, and TTS, image and chat models all list
// generateContent, so the model family in the name is the only signal that separates
// them; embedContent without generateContent marks an embedding model. The lists follow
// the per-model Inputs/Output tables in Google's model docs. Returns nil for plain
// text-generation models, so the model catalog can still fill their full input
// modalities from its datasheet.
func geminiModelArchitecture(modelName string, methods []string) *schemas.Architecture {
	name := strings.ToLower(strings.TrimPrefix(modelName, "models/"))
	tokens := strings.Split(name, "-")
	var inputs, outputs []string
	switch {
	case slices.Contains(tokens, "tts"):
		inputs, outputs = []string{"text"}, []string{"audio"}
	case slices.Contains(tokens, "live") && slices.Contains(tokens, "translate"):
		inputs, outputs = []string{"audio"}, []string{"audio", "text"}
	case strings.Contains(name, "native-audio"):
		inputs, outputs = []string{"audio", "text", "video"}, []string{"audio", "text"}
	case slices.Contains(tokens, "live"):
		inputs, outputs = []string{"audio", "image", "text"}, []string{"audio", "text"}
	case slices.Contains(tokens, "transcribe"):
		inputs, outputs = []string{"audio"}, []string{"text"}
	case schemas.IsImagenModel(name):
		inputs, outputs = []string{"text"}, []string{"image"}
	case slices.Contains(tokens, "veo"):
		inputs, outputs = []string{"image", "text"}, []string{"video"}
	case slices.Contains(tokens, "omni"):
		inputs, outputs = []string{"image", "text", "video"}, []string{"text", "video"}
	case slices.Contains(tokens, "lyria") && slices.Contains(tokens, "realtime"):
		inputs, outputs = []string{"text"}, []string{"audio"}
	case slices.Contains(tokens, "lyria"):
		inputs, outputs = []string{"image", "text"}, []string{"audio", "text"}
	case slices.Contains(tokens, "image") || strings.Contains(name, "nano-banana"):
		// Gemini 2.x image models and Gemini 3 Pro Image take images and text; the
		// later image models also take video and PDF.
		if strings.HasPrefix(name, "gemini-2") || strings.HasPrefix(name, "gemini-3-pro-image") {
			inputs = []string{"image", "text"}
		} else {
			inputs = []string{"file", "image", "text", "video"}
		}
		outputs = []string{"image", "text"}
	case slices.Contains(tokens, "embedding") || (slices.Contains(methods, "embedContent") && !slices.Contains(methods, "generateContent")):
		// gemini-embedding-2 embeds text, images, video, audio and PDF; the earlier
		// embedding models take text only.
		if strings.HasPrefix(name, "gemini-embedding-2") {
			inputs = []string{"audio", "file", "image", "text", "video"}
		} else {
			inputs = []string{"text"}
		}
		outputs = []string{"embeddings"}
	default:
		return nil
	}
	return &schemas.Architecture{InputModalities: inputs, OutputModalities: outputs}
}

func (response *GeminiListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
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

		for _, result := range pipeline.FilterModel(modelName) {
			entry := schemas.Model{
				ID:               string(providerKey) + "/" + result.ResolvedID,
				Name:             schemas.Ptr(model.DisplayName),
				Description:      schemas.Ptr(model.Description),
				ContextLength:    schemas.Ptr(int(contextLength)),
				MaxInputTokens:   schemas.Ptr(model.InputTokenLimit),
				MaxOutputTokens:  schemas.Ptr(model.OutputTokenLimit),
				SupportedMethods: model.SupportedGenerationMethods,
				Architecture:     geminiModelArchitecture(model.Name, model.SupportedGenerationMethods),
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

// ToBifrostModelRetrieveResponse converts a Gemini model resource to a Bifrost model retrieve response
func (model *GeminiModel) ToBifrostModelRetrieveResponse(providerKey schemas.ModelProvider) *schemas.BifrostModelRetrieveResponse {
	if model == nil {
		return nil
	}

	return &schemas.BifrostModelRetrieveResponse{
		Model: schemas.Model{
			ID:               string(providerKey) + "/" + strings.TrimPrefix(model.Name, "models/"),
			Name:             schemas.Ptr(model.DisplayName),
			Description:      schemas.Ptr(model.Description),
			ContextLength:    schemas.Ptr(model.InputTokenLimit),
			MaxInputTokens:   schemas.Ptr(model.InputTokenLimit),
			MaxOutputTokens:  schemas.Ptr(model.OutputTokenLimit),
			SupportedMethods: model.SupportedGenerationMethods,
			Architecture:     geminiModelArchitecture(model.Name, model.SupportedGenerationMethods),
		},
	}
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
