package compat

import (
	"github.com/maximhq/bifrost/core/schemas"
)

type paramCheck struct {
	name      string
	isPresent bool
}

// computeUnsupportedParams checks each parameter field on the request's Params
// and returns the JSON field names of parameters that are set but not in the
// model's supported parameters allowlist. It does NOT mutate the request.
func computeUnsupportedParams(req *schemas.BifrostRequest, supportedParams []string) []string {
	if req == nil {
		return nil
	}

	isSupported := make(map[string]bool, len(supportedParams))
	for _, s := range supportedParams {
		isSupported[s] = true
	}

	switch {
	case req.ChatRequest != nil && req.ChatRequest.Params != nil:
		return getUnsupportedParams(isSupported, getChatChecks(req.ChatRequest.Params))
	case req.ResponsesRequest != nil && req.ResponsesRequest.Params != nil:
		return getUnsupportedParams(isSupported, getResponsesChecks(req.ResponsesRequest.Params))
	case req.TextCompletionRequest != nil && req.TextCompletionRequest.Params != nil:
		return getUnsupportedParams(isSupported, getTextChecks(req.TextCompletionRequest.Params))
	}
	return nil
}

func getUnsupportedParams(isSupported map[string]bool, checks []paramCheck) []string {
	var dropped []string
	for _, c := range checks {
		if c.isPresent && !isSupported[c.name] {
			dropped = append(dropped, c.name)
		}
	}
	return dropped
}

func getChatChecks(p *schemas.ChatParameters) []paramCheck {
	return []paramCheck{
		{"audio", p.Audio != nil},
		{"frequency_penalty", p.FrequencyPenalty != nil},
		{"logit_bias", p.LogitBias != nil},
		{"logprobs", p.LogProbs != nil},
		{"max_completion_tokens", p.MaxCompletionTokens != nil},
		{"metadata", p.Metadata != nil},
		{"parallel_tool_calls", p.ParallelToolCalls != nil},
		{"prediction", p.Prediction != nil},
		{"presence_penalty", p.PresencePenalty != nil},
		{"prompt_cache_key", p.PromptCacheKey != nil},
		{"prompt_cache_retention", p.PromptCacheRetention != nil},
		{"reasoning", p.Reasoning != nil},
		{"response_format", p.ResponseFormat != nil},
		{"seed", p.Seed != nil},
		{"service_tier", p.ServiceTier != nil},
		{"stop", len(p.Stop) > 0},
		{"temperature", p.Temperature != nil},
		{"top_logprobs", p.TopLogProbs != nil},
		{"top_p", p.TopP != nil},
		{"tool_choice", p.ToolChoice != nil},
		{"tools", len(p.Tools) > 0},
		{"verbosity", p.Verbosity != nil},
	}
}

func getResponsesChecks(p *schemas.ResponsesParameters) []paramCheck {
	return []paramCheck{
		{"max_output_tokens", p.MaxOutputTokens != nil},
		{"max_tool_calls", p.MaxToolCalls != nil},
		{"metadata", p.Metadata != nil},
		{"parallel_tool_calls", p.ParallelToolCalls != nil},
		{"prompt_cache_key", p.PromptCacheKey != nil},
		{"reasoning", p.Reasoning != nil},
		{"service_tier", p.ServiceTier != nil},
		{"temperature", p.Temperature != nil},
		{"text", p.Text != nil},
		{"top_logprobs", p.TopLogProbs != nil},
		{"top_p", p.TopP != nil},
		{"tool_choice", p.ToolChoice != nil},
		{"tools", len(p.Tools) > 0},
	}
}

func getTextChecks(p *schemas.TextCompletionParameters) []paramCheck {
	return []paramCheck{
		{"frequency_penalty", p.FrequencyPenalty != nil},
		{"logit_bias", p.LogitBias != nil},
		{"logprobs", p.LogProbs != nil},
		{"max_tokens", p.MaxTokens != nil},
		{"n", p.N != nil},
		{"presence_penalty", p.PresencePenalty != nil},
		{"seed", p.Seed != nil},
		{"stop", len(p.Stop) > 0},
		{"temperature", p.Temperature != nil},
		{"top_p", p.TopP != nil},
	}
}
