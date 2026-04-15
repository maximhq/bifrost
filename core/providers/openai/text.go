package openai

import (
	"maps"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAITextCompletionRequest converts a Bifrost text completion request to OpenAI format
func ToOpenAITextCompletionRequest(bifrostReq *schemas.BifrostTextCompletionRequest) *OpenAITextCompletionRequest {
	if bifrostReq == nil {
		return nil
	}
	params := bifrostReq.Params
	openaiReq := &OpenAITextCompletionRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input,
	}
	if params != nil {
		openaiReq.TextCompletionParameters = *params
		// Drop user field if it exceeds OpenAI's 64 character limit
		openaiReq.TextCompletionParameters.User = SanitizeUserField(openaiReq.TextCompletionParameters.User)
		if bifrostReq.Params.ExtraParams != nil {
			openaiReq.ExtraParams = maps.Clone(bifrostReq.Params.ExtraParams)
			openaiReq.TextCompletionParameters.ExtraParams = openaiReq.ExtraParams
		}
	}
	if bifrostReq.Provider == schemas.Fireworks {
		openaiReq.applyFireworksTextCompletionCompatibility()
	}
	return openaiReq
}

// applyFireworksTextCompletionCompatibility maps Fireworks-specific text fields.
func (req *OpenAITextCompletionRequest) applyFireworksTextCompletionCompatibility() {
	if req == nil || req.ExtraParams == nil {
		return
	}

	// Fireworks uses prompt_cache_isolation_key for text-completion cache isolation.
	if req.PromptCacheIsolationKey == nil {
		if value, ok := req.ExtraParams["prompt_cache_key"]; ok {
			switch typed := value.(type) {
			case string:
				if typed != "" {
					req.PromptCacheIsolationKey = &typed
				}
			case *string:
				if typed != nil && *typed != "" {
					req.PromptCacheIsolationKey = typed
				}
			}
		}
	}
	delete(req.ExtraParams, "prompt_cache_key")
	req.TextCompletionParameters.ExtraParams = req.ExtraParams
}

// ToBifrostTextCompletionRequest converts an OpenAI text completion request to Bifrost format
func (req *OpenAITextCompletionRequest) ToBifrostTextCompletionRequest(ctx *schemas.BifrostContext) *schemas.BifrostTextCompletionRequest {
	if req == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(req.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.OpenAI))

	return &schemas.BifrostTextCompletionRequest{
		Provider:  provider,
		Model:     model,
		Input:     req.Prompt,
		Params:    &req.TextCompletionParameters,
		Fallbacks: schemas.ParseFallbacks(req.Fallbacks),
	}
}
