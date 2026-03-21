package compat

import "github.com/maximhq/bifrost/core/schemas"

// dropUnsupportedParams removes unsupported model parameters from a request in place.
func dropUnsupportedParams(req *schemas.BifrostRequest, supportedParams []string) []string {
	if req == nil {
		return nil
	}

	isSupported := make(map[string]bool, len(supportedParams))
	for _, param := range supportedParams {
		isSupported[param] = true
	}

	var dropped []string

	if req.ChatRequest != nil && req.ChatRequest.Params != nil {
		params := req.ChatRequest.Params

		if params.Audio != nil && !isSupported["audio"] {
			params.Audio = nil
			dropped = append(dropped, "audio")
		}
		if params.FrequencyPenalty != nil && !isSupported["frequency_penalty"] {
			params.FrequencyPenalty = nil
			dropped = append(dropped, "frequency_penalty")
		}
		if params.LogitBias != nil && !isSupported["logit_bias"] {
			params.LogitBias = nil
			dropped = append(dropped, "logit_bias")
		}
		if params.LogProbs != nil && !isSupported["logprobs"] {
			params.LogProbs = nil
			dropped = append(dropped, "logprobs")
		}
		if params.MaxCompletionTokens != nil && !isSupported["max_completion_tokens"] {
			params.MaxCompletionTokens = nil
			dropped = append(dropped, "max_completion_tokens")
		}
		if params.Metadata != nil && !isSupported["metadata"] {
			params.Metadata = nil
			dropped = append(dropped, "metadata")
		}
		if params.ParallelToolCalls != nil && !isSupported["parallel_tool_calls"] {
			params.ParallelToolCalls = nil
			dropped = append(dropped, "parallel_tool_calls")
		}
		if params.Prediction != nil && !isSupported["prediction"] {
			params.Prediction = nil
			dropped = append(dropped, "prediction")
		}
		if params.PresencePenalty != nil && !isSupported["presence_penalty"] {
			params.PresencePenalty = nil
			dropped = append(dropped, "presence_penalty")
		}
		if params.PromptCacheKey != nil && !isSupported["prompt_cache_key"] {
			params.PromptCacheKey = nil
			dropped = append(dropped, "prompt_cache_key")
		}
		if params.PromptCacheRetention != nil && !isSupported["prompt_cache_retention"] {
			params.PromptCacheRetention = nil
			dropped = append(dropped, "prompt_cache_retention")
		}
		if params.Reasoning != nil && !isSupported["reasoning"] {
			params.Reasoning = nil
			dropped = append(dropped, "reasoning")
		}
		if params.ResponseFormat != nil && !isSupported["response_format"] {
			params.ResponseFormat = nil
			dropped = append(dropped, "response_format")
		}
		if params.Seed != nil && !isSupported["seed"] {
			params.Seed = nil
			dropped = append(dropped, "seed")
		}
		if params.ServiceTier != nil && !isSupported["service_tier"] {
			params.ServiceTier = nil
			dropped = append(dropped, "service_tier")
		}
		if len(params.Stop) > 0 && !isSupported["stop"] {
			params.Stop = nil
			dropped = append(dropped, "stop")
		}
		if params.Temperature != nil && !isSupported["temperature"] {
			params.Temperature = nil
			dropped = append(dropped, "temperature")
		}
		if params.TopLogProbs != nil && !isSupported["top_logprobs"] {
			params.TopLogProbs = nil
			dropped = append(dropped, "top_logprobs")
		}
		if params.TopP != nil && !isSupported["top_p"] {
			params.TopP = nil
			dropped = append(dropped, "top_p")
		}
		if params.ToolChoice != nil && !isSupported["tool_choice"] {
			params.ToolChoice = nil
			dropped = append(dropped, "tool_choice")
		}
		if len(params.Tools) > 0 && !isSupported["tools"] {
			params.Tools = nil
			dropped = append(dropped, "tools")
		}
		if params.Verbosity != nil && !isSupported["verbosity"] {
			params.Verbosity = nil
			dropped = append(dropped, "verbosity")
		}
		if params.WebSearchOptions != nil && !isSupported["web_search_options"] {
			params.WebSearchOptions = nil
			dropped = append(dropped, "web_search_options")
		}
	}

	if req.ResponsesRequest != nil && req.ResponsesRequest.Params != nil {
		params := req.ResponsesRequest.Params

		if params.MaxOutputTokens != nil && !isSupported["max_output_tokens"] {
			params.MaxOutputTokens = nil
			dropped = append(dropped, "max_output_tokens")
		}
		if params.MaxToolCalls != nil && !isSupported["max_tool_calls"] {
			params.MaxToolCalls = nil
			dropped = append(dropped, "max_tool_calls")
		}
		if params.Metadata != nil && !isSupported["metadata"] {
			params.Metadata = nil
			dropped = append(dropped, "metadata")
		}
		if params.ParallelToolCalls != nil && !isSupported["parallel_tool_calls"] {
			params.ParallelToolCalls = nil
			dropped = append(dropped, "parallel_tool_calls")
		}
		if params.PromptCacheKey != nil && !isSupported["prompt_cache_key"] {
			params.PromptCacheKey = nil
			dropped = append(dropped, "prompt_cache_key")
		}
		if params.Reasoning != nil && !isSupported["reasoning"] {
			params.Reasoning = nil
			dropped = append(dropped, "reasoning")
		}
		if params.ServiceTier != nil && !isSupported["service_tier"] {
			params.ServiceTier = nil
			dropped = append(dropped, "service_tier")
		}
		if params.Temperature != nil && !isSupported["temperature"] {
			params.Temperature = nil
			dropped = append(dropped, "temperature")
		}
		if params.Text != nil && !isSupported["text"] {
			params.Text = nil
			dropped = append(dropped, "text")
		}
		if params.TopLogProbs != nil && !isSupported["top_logprobs"] {
			params.TopLogProbs = nil
			dropped = append(dropped, "top_logprobs")
		}
		if params.TopP != nil && !isSupported["top_p"] {
			params.TopP = nil
			dropped = append(dropped, "top_p")
		}
		if params.ToolChoice != nil && !isSupported["tool_choice"] {
			params.ToolChoice = nil
			dropped = append(dropped, "tool_choice")
		}
		if len(params.Tools) > 0 && !isSupported["tools"] {
			params.Tools = nil
			dropped = append(dropped, "tools")
		}
	}

	if req.TextCompletionRequest != nil && req.TextCompletionRequest.Params != nil {
		params := req.TextCompletionRequest.Params

		if params.FrequencyPenalty != nil && !isSupported["frequency_penalty"] {
			params.FrequencyPenalty = nil
			dropped = append(dropped, "frequency_penalty")
		}
		if params.LogitBias != nil && !isSupported["logit_bias"] {
			params.LogitBias = nil
			dropped = append(dropped, "logit_bias")
		}
		if params.LogProbs != nil && !isSupported["logprobs"] {
			params.LogProbs = nil
			dropped = append(dropped, "logprobs")
		}
		if params.MaxTokens != nil && !isSupported["max_tokens"] {
			params.MaxTokens = nil
			dropped = append(dropped, "max_tokens")
		}
		if params.N != nil && !isSupported["n"] {
			params.N = nil
			dropped = append(dropped, "n")
		}
		if params.PresencePenalty != nil && !isSupported["presence_penalty"] {
			params.PresencePenalty = nil
			dropped = append(dropped, "presence_penalty")
		}
		if params.Seed != nil && !isSupported["seed"] {
			params.Seed = nil
			dropped = append(dropped, "seed")
		}
		if len(params.Stop) > 0 && !isSupported["stop"] {
			params.Stop = nil
			dropped = append(dropped, "stop")
		}
		if params.Temperature != nil && !isSupported["temperature"] {
			params.Temperature = nil
			dropped = append(dropped, "temperature")
		}
		if params.TopP != nil && !isSupported["top_p"] {
			params.TopP = nil
			dropped = append(dropped, "top_p")
		}
	}

	return dropped
}
