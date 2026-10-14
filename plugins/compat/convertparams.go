package compat

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// convertUnsupportedParamValues fits the output cap and thinking budget to the
// model's limits, returning one "param: from -> to" entry per change. Only values
// the caller sent are touched. req must be a clone: fitted values are swapped in
// as new pointers, so the caller's request keeps what was sent.
func (p *CompatPlugin) convertUnsupportedParamValues(req *schemas.BifrostRequest, provider schemas.ModelProvider, model string) []string {
	if req == nil {
		return nil
	}
	var maxParam string
	var maxTokens, budget **int
	switch {
	case req.ChatRequest != nil && req.ChatRequest.Params != nil:
		// max_tokens is folded into max_completion_tokens before PreLLMHook runs.
		maxParam, maxTokens = "max_completion_tokens", &req.ChatRequest.Params.MaxCompletionTokens
		if reasoning := req.ChatRequest.Params.Reasoning; reasoning != nil {
			budget = &reasoning.MaxTokens
		}
	case req.ResponsesRequest != nil && req.ResponsesRequest.Params != nil:
		maxParam, maxTokens = "max_output_tokens", &req.ResponsesRequest.Params.MaxOutputTokens
		if reasoning := req.ResponsesRequest.Params.Reasoning; reasoning != nil {
			budget = &reasoning.MaxTokens
		}
	case req.TextCompletionRequest != nil && req.TextCompletionRequest.Params != nil:
		maxParam, maxTokens = "max_tokens", &req.TextCompletionRequest.Params.MaxTokens
	default:
		return nil
	}
	var sentBudget *int
	if budget != nil {
		sentBudget = *budget
	}
	if *maxTokens == nil && sentBudget == nil {
		return nil
	}

	caps := schemas.ResolveModelCaps(provider, p.canonicalModel(provider, model))
	// The catalog's pricing data is in memory, so this limit holds without a config store.
	fittedMax, fittedBudget, changes := providerUtils.FitOutputTokens(caps, p.modelCatalog.GetMaxOutputTokens(model, provider), maxParam, *maxTokens, sentBudget)
	*maxTokens = fittedMax
	if budget != nil {
		*budget = fittedBudget
	}
	return changes
}

// canonicalModel resolves a key alias to the model it names, so capability
// lookups and the built-in Claude and Gemini tables see the real model.
func (p *CompatPlugin) canonicalModel(provider schemas.ModelProvider, model string) string {
	alias, ok := p.modelCatalog.ResolveAlias(provider, model)
	if !ok {
		return model
	}
	if alias.Config.ModelName != nil && *alias.Config.ModelName != "" {
		return *alias.Config.ModelName
	}
	if alias.Config.ModelID != "" {
		return alias.Config.ModelID
	}
	return model
}
