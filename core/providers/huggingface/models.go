package huggingface

import (
	"fmt"
	"slices"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	defaultModelFetchLimit = 200
	maxModelFetchLimit     = 1000
)

func (response *HuggingFaceListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, inferenceProvider inferenceProvider, allowedModels []string, unfiltered bool) *schemas.BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(response.Models)),
	}

	includedModels := make(map[string]bool)
	for _, model := range response.Models {
		if model.ModelID == "" {
			continue
		}

		supported := deriveSupportedMethods(model.PipelineTag, model.Tags)
		if len(supported) == 0 {
			continue
		}

		if !unfiltered && len(allowedModels) > 0 && !slices.Contains(allowedModels, model.ModelID) {
			continue
		}

		newModel := schemas.Model{
			ID:               fmt.Sprintf("%s/%s/%s", providerKey, inferenceProvider, model.ModelID),
			Name:             schemas.Ptr(model.ModelID),
			SupportedMethods: supported,
			HuggingFaceID:    schemas.Ptr(model.ID),
		}

		bifrostResponse.Data = append(bifrostResponse.Data, newModel)
		includedModels[model.ModelID] = true
	}

	// Backfill allowed models that were not in the response
	if !unfiltered && len(allowedModels) > 0 {
		for _, allowedModel := range allowedModels {
			if !includedModels[allowedModel] {
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.Model{
					ID:   fmt.Sprintf("%s/%s/%s", providerKey, inferenceProvider, allowedModel),
					Name: schemas.Ptr(allowedModel),
				})
			}
		}
	}

	return bifrostResponse
}

func deriveSupportedMethods(pipeline string, tags []string) []string {
	normalized := strings.TrimSpace(strings.ToLower(pipeline))

	methodsSet := map[schemas.RequestType]struct{}{}

	addMethods := func(methods ...schemas.RequestType) {
		for _, method := range methods {
			methodsSet[method] = struct{}{}
		}
	}

	switch normalized {
	case "conversational", "chat-completion":
		addMethods(schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest,
			schemas.ResponsesRequest, schemas.ResponsesStreamRequest)
	case "feature-extraction":
		addMethods(schemas.EmbeddingRequest)
	case "text-to-speech":
		addMethods(schemas.SpeechRequest)
	case "automatic-speech-recognition":
		addMethods(schemas.TranscriptionRequest)
	case "text-to-image":
		addMethods(schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest)
	}

	for _, tag := range tags {
		tagLower := strings.ToLower(tag)
		switch {
		case tagLower == "text-embedding" || tagLower == "sentence-similarity" ||
			tagLower == "feature-extraction" || tagLower == "embeddings" ||
			tagLower == "sentence-transformers" || strings.Contains(tagLower, "embedding"):
			addMethods(schemas.EmbeddingRequest)
		case tagLower == "text-generation" || tagLower == "summarization" ||
			tagLower == "conversational" || tagLower == "chat-completion" ||
			tagLower == "text2text-generation" || tagLower == "question-answering" ||
			strings.Contains(tagLower, "chat") || strings.Contains(tagLower, "completion"):
			addMethods(schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest,
				schemas.ResponsesRequest, schemas.ResponsesStreamRequest)
		case tagLower == "text-to-speech" || tagLower == "tts" ||
			strings.Contains(tagLower, "text-to-speech"):
			addMethods(schemas.SpeechRequest)
		case tagLower == "automatic-speech-recognition" ||
			tagLower == "speech-to-text" || strings.Contains(tagLower, "speech-recognition"):
			addMethods(schemas.TranscriptionRequest)
		case tagLower == "text-to-image" || strings.Contains(tagLower, "image-generation"):
			addMethods(schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest)
		}
	}

	if len(methodsSet) == 0 {
		return nil
	}

	methods := make([]string, 0, len(methodsSet))
	for method := range methodsSet {
		methods = append(methods, string(method))
	}

	slices.Sort(methods)
	return methods
}
