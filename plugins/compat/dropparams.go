package compat

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

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

	if req.ChatRequest != nil && req.ChatRequest.Input != nil {
		if req.ChatRequest.Provider != schemas.Bedrock || !isSupported["cachePoint"] {
			droppedKeys := dropCachePoint(req.ChatRequest)
			dropped = append(dropped, droppedKeys...)
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
		if !isSupported["web_search"] {
			droppedKeys := dropWebsearchToolCalls(req)
			dropped = append(dropped, droppedKeys...)
		}
	}

	if req.ResponsesRequest != nil {
		// all anthropic models support cache_control
		// for bedrock models cache_control is converted to cachePoint
		if req.ResponsesRequest.Provider == schemas.Bedrock && !isSupported["cache_control"] {
			droppedKeys := dropCacheControlFromResponsesMessages(req.ResponsesRequest)
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

// dropCachePoint drops cache point (only supported by bedrock) from the request
func dropCachePoint(req *schemas.BifrostChatRequest) []string {
	dropped := []string{}
	for i := range req.Input {
		if req.Input[i].Content != nil && req.Input[i].Content.ContentBlocks != nil {
			blocks := req.Input[i].Content.ContentBlocks
			kept := blocks[:0]
			for j, block := range blocks {
				if block.CachePoint != nil {
					dropped = append(dropped, fmt.Sprintf("input[%d].content.content_blocks[%d].cache_point", i, j))
				} else {
					kept = append(kept, block)
				}
			}
			req.Input[i].Content.ContentBlocks = kept
		}
	}
	return dropped
}

// dropCacheControlFromResponsesMessages clears cache_control from all content blocks.
func dropCacheControlFromResponsesMessages(req *schemas.BifrostResponsesRequest) []string {
	var dropped []string

	if req.Input != nil {
		for i := range req.Input {
			msg := &req.Input[i]
			if msg.CacheControl != nil {
				msg.CacheControl = nil
				dropped = append(dropped, fmt.Sprintf("input[%d].cache_control", i))
			}
			if msg.Content == nil || msg.Content.ContentBlocks == nil {
				continue
			}
			for j := range msg.Content.ContentBlocks {
				if msg.Content.ContentBlocks[j].CacheControl != nil {
					msg.Content.ContentBlocks[j].CacheControl = nil
					dropped = append(dropped, fmt.Sprintf("input[%d].content.content_blocks[%d].cache_control", i, j))
				}
			}
		}
	}

	if req.Params != nil {
		for i := range req.Params.Tools {
			if req.Params.Tools[i].CacheControl != nil {
				req.Params.Tools[i].CacheControl = nil
				dropped = append(dropped, fmt.Sprintf("tools[%d].cache_control", i))
			}
		}
	}
	return dropped
}
