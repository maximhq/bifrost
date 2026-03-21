package litellmcompat

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

// computeUnsupportedParams checks each parameter field on the request's Params
// and returns the JSON field names of parameters that are set but not in the
// model's supported parameters allowlist. It does NOT mutate the request.
func computeUnsupportedParams(req *schemas.BifrostRequest, supportedParams []string) []string {
	if req == nil {
		return nil
	}
	switch {
	case req.ChatRequest != nil && req.ChatRequest.Params != nil:
		return unsupportedChatParams(req.ChatRequest.Params, supportedParams)
	case req.ResponsesRequest != nil && req.ResponsesRequest.Params != nil:
		return unsupportedResponsesParams(req.ResponsesRequest.Params, supportedParams)
	case req.TextCompletionRequest != nil && req.TextCompletionRequest.Params != nil:
		return unsupportedTextCompletionParams(req.TextCompletionRequest.Params, supportedParams)
	}
	return nil
}

func unsupportedChatParams(p *schemas.ChatParameters, supported []string) []string {
	var dropped []string
	if p.Audio != nil && !slices.Contains(supported, "audio") {
		dropped = append(dropped, "audio")
	}
	if p.FrequencyPenalty != nil && !slices.Contains(supported, "frequency_penalty") {
		dropped = append(dropped, "frequency_penalty")
	}
	if p.LogitBias != nil && !slices.Contains(supported, "logit_bias") {
		dropped = append(dropped, "logit_bias")
	}
	if p.LogProbs != nil && !slices.Contains(supported, "logprobs") {
		dropped = append(dropped, "logprobs")
	}
	if p.MaxCompletionTokens != nil && !slices.Contains(supported, "max_completion_tokens") {
		dropped = append(dropped, "max_completion_tokens")
	}
	if p.Metadata != nil && !slices.Contains(supported, "metadata") {
		dropped = append(dropped, "metadata")
	}
	if p.ParallelToolCalls != nil && !slices.Contains(supported, "parallel_tool_calls") {
		dropped = append(dropped, "parallel_tool_calls")
	}
	if p.Prediction != nil && !slices.Contains(supported, "prediction") {
		dropped = append(dropped, "prediction")
	}
	if p.PresencePenalty != nil && !slices.Contains(supported, "presence_penalty") {
		dropped = append(dropped, "presence_penalty")
	}
	if p.PromptCacheKey != nil && !slices.Contains(supported, "prompt_cache_key") {
		dropped = append(dropped, "prompt_cache_key")
	}
	if p.PromptCacheRetention != nil && !slices.Contains(supported, "prompt_cache_retention") {
		dropped = append(dropped, "prompt_cache_retention")
	}
	if p.Reasoning != nil && !slices.Contains(supported, "reasoning") {
		dropped = append(dropped, "reasoning")
	}
	if p.ResponseFormat != nil && !slices.Contains(supported, "response_format") {
		dropped = append(dropped, "response_format")
	}
	if p.Seed != nil && !slices.Contains(supported, "seed") {
		dropped = append(dropped, "seed")
	}
	if p.ServiceTier != nil && !slices.Contains(supported, "service_tier") {
		dropped = append(dropped, "service_tier")
	}
	if len(p.Stop) > 0 && !slices.Contains(supported, "stop") {
		dropped = append(dropped, "stop")
	}
	if p.Temperature != nil && !slices.Contains(supported, "temperature") {
		dropped = append(dropped, "temperature")
	}
	if p.TopLogProbs != nil && !slices.Contains(supported, "top_logprobs") {
		dropped = append(dropped, "top_logprobs")
	}
	if p.TopP != nil && !slices.Contains(supported, "top_p") {
		dropped = append(dropped, "top_p")
	}
	if p.ToolChoice != nil && !slices.Contains(supported, "tool_choice") {
		dropped = append(dropped, "tool_choice")
	}
	if len(p.Tools) > 0 && !slices.Contains(supported, "tools") {
		dropped = append(dropped, "tools")
	}
	if p.Verbosity != nil && !slices.Contains(supported, "verbosity") {
		dropped = append(dropped, "verbosity")
	}
	return dropped
}

func unsupportedResponsesParams(p *schemas.ResponsesParameters, supported []string) []string {
	var dropped []string
	if p.MaxOutputTokens != nil && !slices.Contains(supported, "max_output_tokens") {
		dropped = append(dropped, "max_output_tokens")
	}
	if p.MaxToolCalls != nil && !slices.Contains(supported, "max_tool_calls") {
		dropped = append(dropped, "max_tool_calls")
	}
	if p.Metadata != nil && !slices.Contains(supported, "metadata") {
		dropped = append(dropped, "metadata")
	}
	if p.ParallelToolCalls != nil && !slices.Contains(supported, "parallel_tool_calls") {
		dropped = append(dropped, "parallel_tool_calls")
	}
	if p.PromptCacheKey != nil && !slices.Contains(supported, "prompt_cache_key") {
		dropped = append(dropped, "prompt_cache_key")
	}
	if p.Reasoning != nil && !slices.Contains(supported, "reasoning") {
		dropped = append(dropped, "reasoning")
	}
	if p.ServiceTier != nil && !slices.Contains(supported, "service_tier") {
		dropped = append(dropped, "service_tier")
	}
	if p.Temperature != nil && !slices.Contains(supported, "temperature") {
		dropped = append(dropped, "temperature")
	}
	if p.Text != nil && !slices.Contains(supported, "text") {
		dropped = append(dropped, "text")
	}
	if p.TopLogProbs != nil && !slices.Contains(supported, "top_logprobs") {
		dropped = append(dropped, "top_logprobs")
	}
	if p.TopP != nil && !slices.Contains(supported, "top_p") {
		dropped = append(dropped, "top_p")
	}
	if p.ToolChoice != nil && !slices.Contains(supported, "tool_choice") {
		dropped = append(dropped, "tool_choice")
	}
	if len(p.Tools) > 0 && !slices.Contains(supported, "tools") {
		dropped = append(dropped, "tools")
	}
	return dropped
}

func unsupportedTextCompletionParams(p *schemas.TextCompletionParameters, supported []string) []string {
	var dropped []string
	if p.FrequencyPenalty != nil && !slices.Contains(supported, "frequency_penalty") {
		dropped = append(dropped, "frequency_penalty")
	}
	if p.LogitBias != nil && !slices.Contains(supported, "logit_bias") {
		dropped = append(dropped, "logit_bias")
	}
	if p.LogProbs != nil && !slices.Contains(supported, "logprobs") {
		dropped = append(dropped, "logprobs")
	}
	if p.MaxTokens != nil && !slices.Contains(supported, "max_tokens") {
		dropped = append(dropped, "max_tokens")
	}
	if p.N != nil && !slices.Contains(supported, "n") {
		dropped = append(dropped, "n")
	}
	if p.PresencePenalty != nil && !slices.Contains(supported, "presence_penalty") {
		dropped = append(dropped, "presence_penalty")
	}
	if p.Seed != nil && !slices.Contains(supported, "seed") {
		dropped = append(dropped, "seed")
	}
	if len(p.Stop) > 0 && !slices.Contains(supported, "stop") {
		dropped = append(dropped, "stop")
	}
	if p.Temperature != nil && !slices.Contains(supported, "temperature") {
		dropped = append(dropped, "temperature")
	}
	if p.TopP != nil && !slices.Contains(supported, "top_p") {
		dropped = append(dropped, "top_p")
	}
	return dropped
}
