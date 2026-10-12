package huggingface

import (
	"fmt"
	"slices"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	defaultModelFetchLimit = 200
	maxModelFetchLimit     = 1000
)

type huggingFaceModelMetadata struct {
	huggingFaceID    string
	supportedMethods []string
}

type huggingFaceModelMetadataIndex map[string]huggingFaceModelMetadata

func (index huggingFaceModelMetadataIndex) collect(response *HuggingFaceListModelsResponse) {
	for _, model := range response.Models {
		if model.ModelID == "" {
			continue
		}
		key := strings.ToLower(model.ModelID)
		metadata := index[key]
		// A later response may know fields omitted by the first one. Preserve
		// each field independently once a successful response supplies it.
		if metadata.huggingFaceID == "" {
			metadata.huggingFaceID = model.ID
		}
		if len(metadata.supportedMethods) == 0 {
			metadata.supportedMethods = deriveSupportedMethods(model.PipelineTag, model.Tags)
		}
		index[key] = metadata
	}
}

func (index huggingFaceModelMetadataIndex) enrichAliases(models []schemas.Model) {
	for i := range models {
		model := &models[i]
		if model.Alias == nil {
			continue
		}
		// Alias values are exact Hub IDs, even when their organization
		// happens to have an inference provider's name. Listed IDs and
		// friendly display names cannot reliably identify the target.
		metadata := index[strings.ToLower(*model.Alias)]
		if (model.HuggingFaceID == nil || *model.HuggingFaceID == "") && metadata.huggingFaceID != "" {
			model.HuggingFaceID = new(metadata.huggingFaceID)
		}
		if len(model.SupportedMethods) == 0 && len(metadata.supportedMethods) > 0 {
			model.SupportedMethods = metadata.supportedMethods
		}
	}
}

func (response *HuggingFaceListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, inferenceProvider inferenceProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool) *schemas.BifrostListModelsResponse {
	includeUnownedBackfill := len(INFERENCE_PROVIDERS) > 0 && inferenceProvider == INFERENCE_PROVIDERS[0]
	return response.toBifrostListModelsResponse(providerKey, inferenceProvider, allowedModels, blacklistedModels, aliases, unfiltered, includeUnownedBackfill)
}

func (response *HuggingFaceListModelsResponse) toBifrostListModelsResponse(providerKey schemas.ModelProvider, inferenceProvider inferenceProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered, includeUnownedBackfill bool) *schemas.BifrostListModelsResponse {
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
		if model.ModelID == "" {
			continue
		}

		supported := deriveSupportedMethods(model.PipelineTag, model.Tags)
		if len(supported) == 0 {
			continue
		}

		// Aliases apply at the model level (model.ModelID), not at the compound
		// "{providerKey}/{inferenceProvider}/{modelID}" level.
		for _, result := range pipeline.FilterModel(model.ModelID) {
			id := fmt.Sprintf("%s/%s/%s", providerKey, inferenceProvider, result.ResolvedID)
			if result.AliasValue != "" {
				var include bool
				id, include = formatConfiguredModelID(providerKey, inferenceProvider, result.ResolvedID, includeUnownedBackfill)
				if !include {
					continue
				}
			}
			newModel := schemas.Model{
				// Qualified aliases retain their configured inference-provider segment.
				ID:               id,
				Name:             new(model.ModelID),
				SupportedMethods: supported,
				HuggingFaceID:    new(model.ID),
			}
			if result.AliasValue != "" {
				newModel.Alias = new(result.AliasValue)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, newModel)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	// Backfill: use standard pipeline. Note that backfilled HF entries use a simplified
	// compound ID since we don't know which inferenceProvider to assign them to.
	backfilled := pipeline.BackfillModels(included)

	var byModelID map[string]*HuggingFaceModel
	if len(backfilled) > 0 {
		byModelID = make(map[string]*HuggingFaceModel, len(response.Models))
		for i := range response.Models {
			byModelID[strings.ToLower(response.Models[i].ModelID)] = &response.Models[i]
		}
	}

	for _, m := range backfilled {
		rawID := strings.TrimPrefix(m.ID, string(providerKey)+"/")
		// lookupID is the plain HF model ID (no provider/policy segment), used to
		// find the matching entry in response.Models below.
		lookupID := rawID
		// Allowlist entries selected from a previous ListModels response already
		// carry an inference-provider segment (e.g. "featherless-ai/org/model")
		// or the "auto" policy. Prepending another segment here duplicates the
		// provider in the compound ID and breaks request routing (#4215).
		first, modelName, found := strings.Cut(rawID, "/")
		// A two-segment Hub ID names organization/model, even if its
		// organization is also a provider. Keep the configured lookup key and
		// automatic routing intact; aliases still use their exact namespace.
		providerNamedHubID := found && isKnownInferenceProviderOrPolicy(first) && !strings.Contains(modelName, "/") && m.Alias == nil
		if found && isKnownInferenceProviderOrPolicy(first) && !providerNamedHubID {
			m.Name = &modelName
			lookupID = modelName
		}
		var include bool
		if providerNamedHubID {
			m.ID, include = fmt.Sprintf("%s/%s", providerKey, rawID), includeUnownedBackfill
		} else {
			m.ID, include = formatConfiguredModelID(providerKey, inferenceProvider, rawID, includeUnownedBackfill)
		}
		if !include {
			continue
		}

		// BackfillModels only knows about ID/Name/Alias, so a backfilled entry
		// is otherwise missing HuggingFaceID and SupportedMethods. When the
		// model is actually present in this response (e.g. it was skipped
		// upstream for an unrecognized pipeline tag), recover those fields
		// via the lazily-built index above.
		if hfModel := byModelID[strings.ToLower(lookupID)]; hfModel != nil {
			m.HuggingFaceID = new(hfModel.ID)
			if methods := deriveSupportedMethods(hfModel.PipelineTag, hfModel.Tags); len(methods) > 0 {
				m.SupportedMethods = methods
			}
		}

		bifrostResponse.Data = append(bifrostResponse.Data, m)
	}

	return bifrostResponse
}

// formatConfiguredModelID wraps allowlist entries and alias keys, preserving an
// existing inference-provider segment. Raw Hub IDs must not use this helper:
// their organization can have the same name as an inference provider.
func formatConfiguredModelID(providerKey schemas.ModelProvider, provider inferenceProvider, modelID string, includeUnownedBackfill bool) (string, bool) {
	if first, _, found := strings.Cut(modelID, "/"); found && isKnownInferenceProviderOrPolicy(first) {
		if strings.EqualFold(first, string(auto)) || !strings.EqualFold(first, string(provider)) {
			// Active providers own their pass. Auto and retired providers have
			// no active pass, so the aggregate caller assigns them to the first
			// successful response instead of depending on any specific provider.
			if isActiveInferenceProvider(first) || !includeUnownedBackfill {
				return "", false
			}
		}
		return fmt.Sprintf("%s/%s", providerKey, modelID), true
	}
	return fmt.Sprintf("%s/%s/%s", providerKey, provider, modelID), true
}

// isKnownInferenceProviderOrPolicy reports whether segment names one of the
// active or legacy inference providers or the "auto" policy (case-insensitive).
func isKnownInferenceProviderOrPolicy(segment string) bool {
	return slices.ContainsFunc(PROVIDERS_OR_POLICIES, func(p inferenceProvider) bool {
		return strings.EqualFold(segment, string(p))
	})
}

func isActiveInferenceProvider(segment string) bool {
	return slices.ContainsFunc(INFERENCE_PROVIDERS, func(p inferenceProvider) bool {
		return strings.EqualFold(segment, string(p))
	})
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
