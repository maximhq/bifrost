package compat

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// dropUnsupportedParams removes unsupported model parameters from a request in place.
func dropUnsupportedParams(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, supportedParams []string) []string {
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
		hasSupportedTools := len(params.Tools) > 0 && isSupported["tools"]

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
		// max_tokens is converted to max_completion_tokens before compat plugin's PreLLMHook is called.
		// so if either max_tokens or max_completion_tokens is supported, we let max_completion_tokens pass through.
		if params.MaxCompletionTokens != nil && !isSupported["max_completion_tokens"] && !isSupported["max_tokens"] {
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
		// Reasoning + tools is only a problem on the chat completions wire; a
		// request already headed for /responses keeps its reasoning as sent.
		reasoningWithToolsUnsupported := hasSupportedTools && !isSupported["reasoning_with_tool_calls"] && !isConvertedToResponses(ctx)
		if params.Reasoning != nil {
			// for chat completions, some models do not support reasoning_effort
			// with tools
			if !isSupported["reasoning"] {
				params.Reasoning = nil
				dropped = append(dropped, "reasoning")
			} else if reasoningWithToolsUnsupported {
				// models like gpt-5.6 series models defaults to reasoning, even when
				// reasoning_effort is not set.
				if isSupported["supports_none_reasoning_effort"] {
					params.Reasoning = &schemas.ChatReasoning{Effort: new("none")}
					dropped = append(dropped, "reasoning")
				} else {
					params.Reasoning = nil
					dropped = append(dropped, "reasoning")
				}
			}
		} else if isSupported["reasoning"] && isSupported["supports_none_reasoning_effort"] && reasoningWithToolsUnsupported {
			params.Reasoning = &schemas.ChatReasoning{Effort: new("none")}
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

		// max_output_tokens is the Responses-API equivalent of chat max_tokens / max_completion_tokens.
		// so if any of those token-cap spellings is supported, we let max_output_tokens pass through.
		if params.MaxOutputTokens != nil &&
			!isSupported["max_output_tokens"] &&
			!isSupported["max_tokens"] &&
			!isSupported["max_completion_tokens"] {
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
		if params.Reasoning != nil {
			if !isSupported["reasoning"] {
				params.Reasoning = nil
				dropped = append(dropped, "reasoning")
			} else if isAzureDeepSeekResponsesRequest(req) && !isConvertedToChatCompletions(ctx) {
				// Azure's Responses endpoint rejects reasoning.effort for DeepSeek.
				params.Reasoning = nil
				dropped = append(dropped, "reasoning")
			} else if params.Reasoning.Summary != nil && *params.Reasoning.Summary != "auto" &&
				schemas.IsAzureModelRouter(req.ResponsesRequest.Model) {
				// model-router only supports "auto" summary
				params.Reasoning.Summary = nil
				dropped = append(dropped, "reasoning.summary")
			}
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
		if !isSupported["web_search"] {
			droppedKeys := dropWebsearchToolCalls(req)
			if len(droppedKeys) > 0 {
				ctx.Log(schemas.LogLevelWarn, fmt.Sprintf("dropped %d web search tool(s) - the model does not support web_search: %s", len(droppedKeys), strings.Join(droppedKeys, ", ")))
			}
			dropped = append(dropped, droppedKeys...)
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

	if !isSupported["assistant_prefill"] {
		ctx.Log(schemas.LogLevelDebug, "model does not support assistant prefill, assistant messages will be trimmed")
	}
	ctx.SetValue(schemas.BifrostContextKeySupportsAssistantPrefill, isSupported["assistant_prefill"])

	return dropped
}

// dropWebsearchToolCalls drops web search tool calls from the request
func dropWebsearchToolCalls(req *schemas.BifrostRequest) []string {
	dropped := []string{}
	tools := req.ResponsesRequest.Params.Tools
	kept := tools[:0]
	for i, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeWebSearch || tool.Type == schemas.ResponsesToolTypeWebSearchPreview {
			dropped = append(dropped, fmt.Sprintf("tools[%d].%s", i, tool.Type))
		} else {
			kept = append(kept, tool)
		}
	}
	req.ResponsesRequest.Params.Tools = kept
	return dropped
}
