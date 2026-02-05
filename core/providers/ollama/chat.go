// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains converters for chat completion requests and responses.
package ollama

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// UnmarshalJSON implements custom JSON unmarshaling for OllamaThinkValue.
// Accepts both bool (true/false) and string ("low"/"medium"/"high").
func (t *OllamaThinkValue) UnmarshalJSON(b []byte) error {
	var boolVal bool
	if err := json.Unmarshal(b, &boolVal); err == nil {
		t.BoolVal = &boolVal
		t.StringVal = nil
		return nil
	}

	var strVal string
	if err := json.Unmarshal(b, &strVal); err == nil {
		t.BoolVal = nil
		t.StringVal = &strVal
		return nil
	}

	return fmt.Errorf("ollama think value must be bool or string")
}

// MarshalJSON implements custom JSON marshaling for OllamaThinkValue.
func (t OllamaThinkValue) MarshalJSON() ([]byte, error) {
	if t.BoolVal != nil {
		return json.Marshal(*t.BoolVal)
	}
	if t.StringVal != nil {
		return json.Marshal(*t.StringVal)
	}
	return json.Marshal(nil)
}

// NewThinkBool creates an OllamaThinkValue from a bool.
func NewThinkBool(v bool) *OllamaThinkValue {
	return &OllamaThinkValue{BoolVal: &v}
}

// NewThinkString creates an OllamaThinkValue from a string.
func NewThinkString(v string) *OllamaThinkValue {
	return &OllamaThinkValue{StringVal: &v}
}

// ToOllamaChatRequest converts a Bifrost chat request to Ollama native format.
func ToOllamaChatRequest(bifrostReq *schemas.BifrostChatRequest) *OllamaChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	ollamaReq := &OllamaChatRequest{
		Model:    bifrostReq.Model,
		Messages: convertMessagesToOllama(bifrostReq.Input),
	}

	// Convert parameters
	if bifrostReq.Params != nil {
		options := &OllamaOptions{}
		hasOptions := false

		// Map standard parameters
		if bifrostReq.Params.MaxCompletionTokens != nil {
			options.NumPredict = bifrostReq.Params.MaxCompletionTokens
			hasOptions = true
		}
		if bifrostReq.Params.Temperature != nil {
			options.Temperature = bifrostReq.Params.Temperature
			hasOptions = true
		}
		if bifrostReq.Params.TopP != nil {
			options.TopP = bifrostReq.Params.TopP
			hasOptions = true
		}
		if bifrostReq.Params.PresencePenalty != nil {
			options.PresencePenalty = bifrostReq.Params.PresencePenalty
			hasOptions = true
		}
		if bifrostReq.Params.FrequencyPenalty != nil {
			options.FrequencyPenalty = bifrostReq.Params.FrequencyPenalty
			hasOptions = true
		}
		if bifrostReq.Params.Stop != nil {
			options.Stop = bifrostReq.Params.Stop
			hasOptions = true
		}
		if bifrostReq.Params.Seed != nil {
			options.Seed = bifrostReq.Params.Seed
			hasOptions = true
		}

		// Handle extra parameters for Ollama-specific fields
		if bifrostReq.Params.ExtraParams != nil {
			// Top-k sampling
			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				options.TopK = topK
				hasOptions = true
			}

			// Context window size
			if numCtx, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_ctx"]); ok {
				options.NumCtx = numCtx
				hasOptions = true
			}

			// Repeat penalty
			if repeatPenalty, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["repeat_penalty"]); ok {
				options.RepeatPenalty = repeatPenalty
				hasOptions = true
			}

			// Repeat last N
			if repeatLastN, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["repeat_last_n"]); ok {
				options.RepeatLastN = repeatLastN
				hasOptions = true
			}

			// Mirostat sampling
			if mirostat, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["mirostat"]); ok {
				options.Mirostat = mirostat
				hasOptions = true
			}
			if mirostatEta, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["mirostat_eta"]); ok {
				options.MirostatEta = mirostatEta
				hasOptions = true
			}
			if mirostatTau, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["mirostat_tau"]); ok {
				options.MirostatTau = mirostatTau
				hasOptions = true
			}

			// TFS-Z sampling
			if tfsZ, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["tfs_z"]); ok {
				options.TfsZ = tfsZ
				hasOptions = true
			}

			// Typical-P sampling
			if typicalP, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["typical_p"]); ok {
				options.TypicalP = typicalP
				hasOptions = true
			}

			// Performance options
			if numBatch, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_batch"]); ok {
				options.NumBatch = numBatch
				hasOptions = true
			}
			if numGPU, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_gpu"]); ok {
				options.NumGPU = numGPU
				hasOptions = true
			}
			if numThread, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_thread"]); ok {
				options.NumThread = numThread
				hasOptions = true
			}

			// Keep-alive duration
			if keepAlive, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["keep_alive"]); ok {
				ollamaReq.KeepAlive = keepAlive
			}

			// Min-p sampling
			if minP, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["min_p"]); ok {
				options.MinP = minP
				hasOptions = true
			}

			// Enable thinking mode (for thinking-specific models)
			// Supports bool (true/false) or string ("low"/"medium"/"high" for GPT-OSS)
			if think, exists := bifrostReq.Params.ExtraParams["think"]; exists && think != nil {
				switch v := think.(type) {
				case bool:
					ollamaReq.Think = NewThinkBool(v)
				case string:
					ollamaReq.Think = NewThinkString(v)
				}
			}

			// Log probabilities
			if logprobs, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["logprobs"]); ok {
				ollamaReq.Logprobs = logprobs
			}
			if topLogprobs, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_logprobs"]); ok {
				ollamaReq.TopLogprobs = topLogprobs
			}
		}

		if hasOptions {
			ollamaReq.Options = options
		}

		// Handle response format (JSON mode)
		if bifrostReq.Params.ResponseFormat != nil {
			if rf, ok := (*bifrostReq.Params.ResponseFormat).(map[string]interface{}); ok {
				if t, exists := rf["type"]; exists && t == "json_object" {
					ollamaReq.Format = "json"
				} else if schema, exists := rf["json_schema"]; exists {
					// Pass JSON schema directly for structured output
					ollamaReq.Format = schema
				}
			}
		}

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			ollamaReq.Tools = convertToolsToOllama(bifrostReq.Params.Tools)
		}
	}

	return ollamaReq
}

// ToBifrostChatRequest converts an Ollama chat request to Bifrost format.
// This is used for passthrough/reverse conversion scenarios.
func (r *OllamaChatRequest) ToBifrostChatRequest() *schemas.BifrostChatRequest {
	if r == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(r.Model, schemas.Ollama)

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input:    convertMessagesFromOllama(r.Messages),
	}

	// Convert options to parameters
	if r.Options != nil {
		params := &schemas.ChatParameters{
			ExtraParams: make(map[string]interface{}),
		}

		if r.Options.NumPredict != nil {
			params.MaxCompletionTokens = r.Options.NumPredict
		}
		if r.Options.Temperature != nil {
			params.Temperature = r.Options.Temperature
		}
		if r.Options.TopP != nil {
			params.TopP = r.Options.TopP
		}
		if r.Options.Stop != nil {
			params.Stop = r.Options.Stop
		}
		if r.Options.PresencePenalty != nil {
			params.PresencePenalty = r.Options.PresencePenalty
		}
		if r.Options.FrequencyPenalty != nil {
			params.FrequencyPenalty = r.Options.FrequencyPenalty
		}
		if r.Options.Seed != nil {
			params.Seed = r.Options.Seed
		}

		// Map Ollama-specific parameters to ExtraParams
		if r.Options.TopK != nil {
			params.ExtraParams["top_k"] = *r.Options.TopK
		}
		if r.Options.NumCtx != nil {
			params.ExtraParams["num_ctx"] = *r.Options.NumCtx
		}
		if r.Options.RepeatPenalty != nil {
			params.ExtraParams["repeat_penalty"] = *r.Options.RepeatPenalty
		}

		bifrostReq.Params = params
	}

	// Convert tools
	if r.Tools != nil {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ChatParameters{}
		}
		bifrostReq.Params.Tools = convertToolsFromOllama(r.Tools)
	}

	return bifrostReq
}

// ==================== RESPONSE CONVERTERS ====================

// ToBifrostChatResponse converts an Ollama chat response to Bifrost format.
func (r *OllamaChatResponse) ToBifrostChatResponse(model string) *schemas.BifrostChatResponse {
	if r == nil {
		return nil
	}

	// Parse timestamp
	created := int(time.Now().Unix())
	if r.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err == nil {
			created = int(t.Unix())
		}
	}

	response := &schemas.BifrostChatResponse{
		Model:   model,
		Created: created,
		Object:  "chat.completion",
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.Ollama,
		},
	}

	// Build the choice
	if r.Message != nil {
		var toolCalls []schemas.ChatAssistantMessageToolCall
		if len(r.Message.ToolCalls) > 0 {
			for _, tc := range r.Message.ToolCalls {
				args, _ := json.Marshal(tc.Function.Arguments)
				// Use Ollama's tool call ID if provided, otherwise fall back to function name
				id := tc.Function.Name
				if tc.ID != "" {
					id = tc.ID
				}
				toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
					Index: uint16(tc.Function.Index),
					Type:  schemas.Ptr("function"),
					ID:    schemas.Ptr(id),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr(tc.Function.Name),
						Arguments: string(args),
					},
				})
			}
		}

		var assistantMessage *schemas.ChatAssistantMessage
		if len(toolCalls) > 0 {
			assistantMessage = &schemas.ChatAssistantMessage{
				ToolCalls: toolCalls,
			}
		}

		// Handle thinking content for non-streaming responses
		// Store thinking in tool call ExtraContent (similar to how we preserve it in message conversion)
		if r.Message.Thinking != nil && *r.Message.Thinking != "" {
			if assistantMessage == nil {
				assistantMessage = &schemas.ChatAssistantMessage{}
			}
			// If we have tool calls, store thinking in the first one's ExtraContent
			// Otherwise, create a placeholder tool call to preserve thinking
			if len(assistantMessage.ToolCalls) > 0 {
				if assistantMessage.ToolCalls[0].ExtraContent == nil {
					assistantMessage.ToolCalls[0].ExtraContent = make(map[string]interface{})
				}
				assistantMessage.ToolCalls[0].ExtraContent["ollama"] = map[string]interface{}{
					"thinking": *r.Message.Thinking,
				}
			} else {
				// Create placeholder tool call to preserve thinking
				assistantMessage.ToolCalls = []schemas.ChatAssistantMessageToolCall{
					{
						Index: 0,
						Type:  schemas.Ptr("function"),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      schemas.Ptr("_thinking_placeholder"),
							Arguments: "{}",
						},
						ExtraContent: map[string]interface{}{
							"ollama": map[string]interface{}{
								"thinking": *r.Message.Thinking,
							},
						},
					},
				}
			}
		}

		choice := schemas.BifrostResponseChoice{
			Index: 0,
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
				Message: &schemas.ChatMessage{
					Role: schemas.ChatMessageRole(r.Message.Role),
					Content: &schemas.ChatMessageContent{
						ContentStr: &r.Message.Content,
					},
					ChatAssistantMessage: assistantMessage,
				},
			},
			FinishReason: r.mapFinishReason(),
		}
		response.Choices = []schemas.BifrostResponseChoice{choice}
	}

	// Map usage
	response.Usage = r.toUsage()

	return response
}

// mapFinishReason maps Ollama's done_reason to Bifrost format.
func (r *OllamaChatResponse) mapFinishReason() *string {
	if r.DoneReason == nil {
		if r.Done {
			return schemas.Ptr("stop")
		}
		return nil
	}

	switch *r.DoneReason {
	case "stop":
		return schemas.Ptr("stop")
	case "length":
		return schemas.Ptr("length")
	case "load", "unload":
		return schemas.Ptr("stop")
	default:
		return r.DoneReason
	}
}

// toUsage converts Ollama usage info to Bifrost format.
func (r *OllamaChatResponse) toUsage() *schemas.BifrostLLMUsage {
	usage := &schemas.BifrostLLMUsage{}

	if r.PromptEvalCount != nil {
		usage.PromptTokens = *r.PromptEvalCount
	}
	if r.EvalCount != nil {
		usage.CompletionTokens = *r.EvalCount
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	return usage
}

// ToBifrostStreamResponse converts an Ollama streaming chunk to Bifrost format.
func (r *OllamaStreamResponse) ToBifrostStreamResponse(chunkIndex int) (*schemas.BifrostChatResponse, bool) {
	if r == nil {
		return nil, false
	}

	response := &schemas.BifrostChatResponse{
		Model:  r.Model,
		Object: "chat.completion.chunk",
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionStreamRequest,
			Provider:    schemas.Ollama,
			ChunkIndex:  chunkIndex,
		},
	}

	// Parse timestamp
	if r.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err == nil {
			response.Created = int(t.Unix())
		}
	}

	// Build delta content
	if r.Message != nil {
		var toolCalls []schemas.ChatAssistantMessageToolCall
		if len(r.Message.ToolCalls) > 0 {
			for _, tc := range r.Message.ToolCalls {
				args, _ := json.Marshal(tc.Function.Arguments)
				// Use Ollama's tool call ID if provided, otherwise fall back to function name
				id := tc.Function.Name
				if tc.ID != "" {
					id = tc.ID
				}
				toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
					Index: uint16(tc.Function.Index),
					Type:  schemas.Ptr("function"),
					ID:    schemas.Ptr(id),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr(tc.Function.Name),
						Arguments: string(args),
					},
				})
			}
		}

		delta := &schemas.ChatStreamResponseChoiceDelta{}

		if r.Message.Role != "" {
			role := string(r.Message.Role)
			delta.Role = &role
		}

		if r.Message.Content != "" {
			delta.Content = &r.Message.Content
		}

		// Handle thinking content (for thinking-specific models)
		// Ollama may send thinking incrementally in streaming chunks, similar to content
		if r.Message.Thinking != nil && *r.Message.Thinking != "" {
			delta.Reasoning = r.Message.Thinking
		}

		if len(toolCalls) > 0 {
			delta.ToolCalls = toolCalls
		}

		// Always create a choice if we have any delta content (content, thinking, tool calls, or role)
		hasDelta := delta.Role != nil || delta.Content != nil || delta.Reasoning != nil || len(delta.ToolCalls) > 0
		if hasDelta {
			choice := schemas.BifrostResponseChoice{
				Index: 0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: delta,
				},
			}

			// Set finish reason on final chunk
			if r.Done {
				if r.DoneReason != nil {
					switch *r.DoneReason {
					case "stop":
						choice.FinishReason = schemas.Ptr("stop")
					case "length":
						choice.FinishReason = schemas.Ptr("length")
					default:
						choice.FinishReason = schemas.Ptr("stop")
					}
				} else {
					choice.FinishReason = schemas.Ptr("stop")
				}
			}

			response.Choices = []schemas.BifrostResponseChoice{choice}
		}
	}

	// Add usage on final chunk
	if r.Done {
		usage := &schemas.BifrostLLMUsage{}
		if r.PromptEvalCount != nil {
			usage.PromptTokens = *r.PromptEvalCount
		}
		if r.EvalCount != nil {
			usage.CompletionTokens = *r.EvalCount
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		response.Usage = usage
	}

	return response, r.Done
}
