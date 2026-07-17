package openai

import (
	"encoding/json"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostListModelsResponse converts an OpenAI list models response to a Bifrost list models response.
// When includeCustomModelFields is true, any non-standard fields the upstream provider returned on a
// model entry are preserved on Model.ProviderExtra for later reuse by the single-model retrieve endpoint.
func (response *OpenAIListModelsResponse) ToBifrostListModelsResponse(providerKey schemas.ModelProvider, allowedModels schemas.WhiteList, blacklistedModels schemas.BlackList, aliases schemas.KeyAliases, unfiltered bool, includeCustomModelFields bool) *schemas.BifrostListModelsResponse {
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
				ID:            string(providerKey) + "/" + result.ResolvedID,
				Created:       model.Created,
				OwnedBy:       schemas.Ptr(model.OwnedBy),
				ContextLength: model.ContextWindow,
			}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			if includeCustomModelFields {
				if extra := withActiveField(model.Extra, model.Active); len(extra) > 0 {
					if raw, err := sonic.Marshal(extra); err == nil {
						entry.ProviderExtra = raw
					}
				}
			}
			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data,
		pipeline.BackfillModels(included)...)

	return bifrostResponse
}

// withActiveField folds OpenAIModel's typed Groq-specific Active field into a copy of extra
// (without mutating the original map) when present. Active is a known OpenAIModel field, so it
// never lands in Extra on its own - but schemas.Model has no field for it either, so without
// this it would be silently dropped even with includeCustomModelFields enabled.
func withActiveField(extra map[string]json.RawMessage, active *bool) map[string]json.RawMessage {
	if active == nil {
		return extra
	}
	raw, err := sonic.Marshal(*active)
	if err != nil {
		return extra
	}
	merged := make(map[string]json.RawMessage, len(extra)+1)
	for key, value := range extra {
		merged[key] = value
	}
	merged["active"] = raw
	return merged
}

// ToOpenAIModel converts a single Bifrost Model into its OpenAI wire representation,
// rehydrating any non-standard upstream fields captured on ProviderExtra so a single-model
// retrieve response can round-trip them back out. Used by GET /v1/models/{model_id}; the
// list endpoint (ToOpenAIListModelsResponse) intentionally does not do this.
func ToOpenAIModel(model *schemas.Model) *OpenAIModel {
	if model == nil {
		return nil
	}
	openaiModel := &OpenAIModel{
		ID:     model.ID,
		Object: "model",
	}
	if model.Created != nil {
		openaiModel.Created = model.Created
	}
	if model.OwnedBy != nil {
		openaiModel.OwnedBy = *model.OwnedBy
	}
	if model.ContextLength != nil {
		openaiModel.ContextWindow = model.ContextLength
	} else if model.MaxInputTokens != nil {
		openaiModel.ContextWindow = model.MaxInputTokens
	}
	if len(model.ProviderExtra) > 0 {
		var extra map[string]json.RawMessage
		if err := sonic.Unmarshal(model.ProviderExtra, &extra); err == nil {
			// "active" is a known OpenAIModel field (folded into ProviderExtra by
			// withActiveField since schemas.Model has no field for it) rather than a
			// genuine extra - rehydrate it into the typed field instead of leaving it
			// in Extra, which MarshalJSON would otherwise skip merging back ("known
			// fields always win"), silently dropping it from the response.
			if raw, ok := extra["active"]; ok {
				var active bool
				if err := sonic.Unmarshal(raw, &active); err == nil {
					openaiModel.Active = &active
				}
				delete(extra, "active")
			}
			openaiModel.Extra = extra
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
		}
		if model.Created != nil {
			openaiModel.Created = model.Created
		}
		if model.OwnedBy != nil {
			openaiModel.OwnedBy = *model.OwnedBy
		}
		if model.ContextLength != nil {
			openaiModel.ContextWindow = model.ContextLength
		} else if model.MaxInputTokens != nil {
			openaiModel.ContextWindow = model.MaxInputTokens // Fallback to MaxInputTokens if ContextLength is not set
		}

		openaiResponse.Data = append(openaiResponse.Data, openaiModel)

	}
	return openaiResponse
}
