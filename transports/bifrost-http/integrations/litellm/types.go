package litellm

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}

// LiteLLM provides OpenAI-compatible API, so we'll use similar structures
// with support for multiple provider model routing

// LiteLLMMessage represents a message in LiteLLM chat format (OpenAI-compatible)
type LiteLLMMessage struct {
	Role       string             `json:"role"`
	Content    *string            `json:"content,omitempty"`
	Name       *string            `json:"name,omitempty"`
	ToolCalls  *[]LiteLLMToolCall `json:"tool_calls,omitempty"`
	ToolCallID *string            `json:"tool_call_id,omitempty"`
}

// LiteLLMToolCall represents a tool call in LiteLLM format
type LiteLLMToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function LiteLLMFunctionCall `json:"function"`
}

// LiteLLMFunctionCall represents a function call in LiteLLM format
type LiteLLMFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// LiteLLMTool represents a tool in LiteLLM format
type LiteLLMTool struct {
	Type     string          `json:"type"`
	Function LiteLLMFunction `json:"function"`
}

// LiteLLMFunction represents a function definition in LiteLLM format
type LiteLLMFunction struct {
	Name        string      `json:"name"`
	Description *string     `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

// LiteLLMChatRequest represents a LiteLLM chat completion request
type LiteLLMChatRequest struct {
	Model            string             `json:"model"`
	Messages         []LiteLLMMessage   `json:"messages"`
	MaxTokens        *int               `json:"max_tokens,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
	N                *int               `json:"n,omitempty"`
	Stop             interface{}        `json:"stop,omitempty"`
	PresencePenalty  *float64           `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64           `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]float64 `json:"logit_bias,omitempty"`
	User             *string            `json:"user,omitempty"`
	Tools            *[]LiteLLMTool     `json:"tools,omitempty"`
	ToolChoice       interface{}        `json:"tool_choice,omitempty"`
	Stream           *bool              `json:"stream,omitempty"`
	ResponseFormat   interface{}        `json:"response_format,omitempty"`
	Seed             *int               `json:"seed,omitempty"`
	// LiteLLM-specific parameters
	APIBase     *string                `json:"api_base,omitempty"`
	APIVersion  *string                `json:"api_version,omitempty"`
	APIKey      *string                `json:"api_key,omitempty"`
	Drop_params *bool                  `json:"drop_params,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// ConvertToBifrostRequest converts a LiteLLM chat request to Bifrost format
func (r *LiteLLMChatRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	// LiteLLM can route to any provider, but we'll determine the appropriate provider from the model
	provider := determineProviderFromModel(r.Model)

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    r.Model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{},
		},
	}

	// Convert messages
	for _, msg := range r.Messages {
		var bifrostMsg schemas.BifrostMessage
		bifrostMsg.Role = schemas.ModelChatMessageRole(msg.Role)
		bifrostMsg.Content = msg.Content

		// Handle tool calls for assistant messages
		if msg.ToolCalls != nil {
			toolCalls := []schemas.ToolCall{}
			for _, toolCall := range *msg.ToolCalls {
				tc := schemas.ToolCall{
					Type: stringPtr(toolCall.Type),
					ID:   &toolCall.ID,
					Function: schemas.FunctionCall{
						Name:      &toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
					},
				}
				toolCalls = append(toolCalls, tc)
			}
			bifrostMsg.AssistantMessage = &schemas.AssistantMessage{
				ToolCalls: &toolCalls,
			}
		}

		// Handle tool messages
		if msg.ToolCallID != nil {
			bifrostMsg.ToolMessage = &schemas.ToolMessage{
				ToolCallID: msg.ToolCallID,
			}
		}

		*bifrostReq.Input.ChatCompletionInput = append(*bifrostReq.Input.ChatCompletionInput, bifrostMsg)
	}

	// Convert parameters
	if r.MaxTokens != nil || r.Temperature != nil || r.TopP != nil || r.PresencePenalty != nil ||
		r.FrequencyPenalty != nil || r.N != nil {
		params := &schemas.ModelParameters{
			ExtraParams: make(map[string]interface{}),
		}

		if r.MaxTokens != nil {
			params.MaxTokens = r.MaxTokens
		}
		if r.Temperature != nil {
			params.Temperature = r.Temperature
		}
		if r.TopP != nil {
			params.TopP = r.TopP
		}
		if r.PresencePenalty != nil {
			params.PresencePenalty = r.PresencePenalty
		}
		if r.FrequencyPenalty != nil {
			params.FrequencyPenalty = r.FrequencyPenalty
		}
		if r.N != nil {
			params.ExtraParams["n"] = r.N
		}

		// Add LiteLLM-specific params
		if r.APIBase != nil {
			params.ExtraParams["api_base"] = r.APIBase
		}
		if r.APIVersion != nil {
			params.ExtraParams["api_version"] = r.APIVersion
		}
		if r.Drop_params != nil {
			params.ExtraParams["drop_params"] = r.Drop_params
		}
		if r.Metadata != nil {
			params.ExtraParams["metadata"] = r.Metadata
		}

		bifrostReq.Params = params
	}

	// Convert tools
	if r.Tools != nil {
		tools := []schemas.Tool{}
		for _, tool := range *r.Tools {
			description := ""
			if tool.Function.Description != nil {
				description = *tool.Function.Description
			}

			// Convert parameters interface{} to FunctionParameters
			params := schemas.FunctionParameters{
				Type: "object",
			}
			if tool.Function.Parameters != nil {
				if paramMap, ok := tool.Function.Parameters.(map[string]interface{}); ok {
					if typeVal, ok := paramMap["type"].(string); ok {
						params.Type = typeVal
					}
					if desc, ok := paramMap["description"].(string); ok {
						params.Description = &desc
					}
					if required, ok := paramMap["required"].([]interface{}); ok {
						reqStrings := make([]string, len(required))
						for i, req := range required {
							if reqStr, ok := req.(string); ok {
								reqStrings[i] = reqStr
							}
						}
						params.Required = reqStrings
					}
					if properties, ok := paramMap["properties"].(map[string]interface{}); ok {
						params.Properties = properties
					}
					if enum, ok := paramMap["enum"].([]interface{}); ok {
						enumStrings := make([]string, len(enum))
						for i, e := range enum {
							if eStr, ok := e.(string); ok {
								enumStrings[i] = eStr
							}
						}
						params.Enum = &enumStrings
					}
				}
			}

			t := schemas.Tool{
				Type: tool.Type,
				Function: schemas.Function{
					Name:        tool.Function.Name,
					Description: description,
					Parameters:  params,
				},
			}
			tools = append(tools, t)
		}
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ModelParameters{}
		}
		bifrostReq.Params.Tools = &tools
	}

	return bifrostReq
}

// LiteLLMCompletionRequest represents a LiteLLM text completion request
type LiteLLMCompletionRequest struct {
	Model            string             `json:"model"`
	Prompt           string             `json:"prompt"`
	MaxTokens        *int               `json:"max_tokens,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
	N                *int               `json:"n,omitempty"`
	Stream           *bool              `json:"stream,omitempty"`
	LogProbs         *int               `json:"logprobs,omitempty"`
	Echo             *bool              `json:"echo,omitempty"`
	Stop             interface{}        `json:"stop,omitempty"`
	PresencePenalty  *float64           `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64           `json:"frequency_penalty,omitempty"`
	BestOf           *int               `json:"best_of,omitempty"`
	LogitBias        map[string]float64 `json:"logit_bias,omitempty"`
	User             *string            `json:"user,omitempty"`
	// LiteLLM-specific parameters
	APIBase     *string                `json:"api_base,omitempty"`
	APIVersion  *string                `json:"api_version,omitempty"`
	APIKey      *string                `json:"api_key,omitempty"`
	Drop_params *bool                  `json:"drop_params,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// ConvertToBifrostRequest converts a LiteLLM completion request to Bifrost format
func (r *LiteLLMCompletionRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	provider := determineProviderFromModel(r.Model)

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    r.Model,
		Input: schemas.RequestInput{
			TextCompletionInput: &r.Prompt,
		},
	}

	// Convert parameters
	if r.MaxTokens != nil || r.Temperature != nil || r.TopP != nil || r.PresencePenalty != nil ||
		r.FrequencyPenalty != nil || r.N != nil {
		params := &schemas.ModelParameters{
			ExtraParams: make(map[string]interface{}),
		}

		if r.MaxTokens != nil {
			params.MaxTokens = r.MaxTokens
		}
		if r.Temperature != nil {
			params.Temperature = r.Temperature
		}
		if r.TopP != nil {
			params.TopP = r.TopP
		}
		if r.PresencePenalty != nil {
			params.PresencePenalty = r.PresencePenalty
		}
		if r.FrequencyPenalty != nil {
			params.FrequencyPenalty = r.FrequencyPenalty
		}
		if r.N != nil {
			params.ExtraParams["n"] = r.N
		}
		if r.LogProbs != nil {
			params.ExtraParams["logprobs"] = r.LogProbs
		}
		if r.Echo != nil {
			params.ExtraParams["echo"] = r.Echo
		}
		if r.BestOf != nil {
			params.ExtraParams["best_of"] = r.BestOf
		}

		// Add LiteLLM-specific params
		if r.APIBase != nil {
			params.ExtraParams["api_base"] = r.APIBase
		}
		if r.APIVersion != nil {
			params.ExtraParams["api_version"] = r.APIVersion
		}
		if r.Drop_params != nil {
			params.ExtraParams["drop_params"] = r.Drop_params
		}
		if r.Metadata != nil {
			params.ExtraParams["metadata"] = r.Metadata
		}

		bifrostReq.Params = params
	}

	return bifrostReq
}

// Helper function to determine provider from model name
func determineProviderFromModel(model string) schemas.ModelProvider {
	// LiteLLM uses prefixes or model names to determine provider
	// This is a simplified version - in production you'd have more sophisticated routing
	if contains(model, "gpt") || contains(model, "o1") {
		return schemas.OpenAI
	} else if contains(model, "claude") {
		return schemas.Anthropic
	} else if contains(model, "gemini") || contains(model, "vertex") {
		return schemas.Vertex
	} else if contains(model, "bedrock") {
		return schemas.Bedrock
	} else if contains(model, "cohere") {
		return schemas.Cohere
	}
	// Default to OpenAI for unknown models
	return schemas.OpenAI
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// Response structures

// LiteLLMChatResponse represents a LiteLLM chat completion response
type LiteLLMChatResponse struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int             `json:"created"`
	Model   string          `json:"model"`
	Choices []LiteLLMChoice `json:"choices"`
	Usage   *LiteLLMUsage   `json:"usage,omitempty"`
}

// LiteLLMChoice represents a choice in the LiteLLM response
type LiteLLMChoice struct {
	Index        int            `json:"index"`
	Message      LiteLLMMessage `json:"message"`
	FinishReason *string        `json:"finish_reason,omitempty"`
}

// LiteLLMCompletionResponse represents a LiteLLM text completion response
type LiteLLMCompletionResponse struct {
	ID      string                    `json:"id"`
	Object  string                    `json:"object"`
	Created int                       `json:"created"`
	Model   string                    `json:"model"`
	Choices []LiteLLMCompletionChoice `json:"choices"`
	Usage   *LiteLLMUsage             `json:"usage,omitempty"`
}

// LiteLLMCompletionChoice represents a choice in the LiteLLM completion response
type LiteLLMCompletionChoice struct {
	Text         string  `json:"text"`
	Index        int     `json:"index"`
	FinishReason *string `json:"finish_reason,omitempty"`
}

// LiteLLMUsage represents usage information in LiteLLM format
type LiteLLMUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// DeriveLiteLLMFromBifrostResponse converts a Bifrost response to LiteLLM chat format
func DeriveLiteLLMFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *LiteLLMChatResponse {
	if bifrostResp == nil {
		return nil
	}

	litellmResp := &LiteLLMChatResponse{
		ID:      bifrostResp.ID,
		Object:  "chat.completion",
		Created: bifrostResp.Created,
		Model:   bifrostResp.Model,
		Choices: make([]LiteLLMChoice, len(bifrostResp.Choices)),
	}

	// Convert usage information
	if bifrostResp.Usage != (schemas.LLMUsage{}) {
		litellmResp.Usage = &LiteLLMUsage{
			PromptTokens:     bifrostResp.Usage.PromptTokens,
			CompletionTokens: bifrostResp.Usage.CompletionTokens,
			TotalTokens:      bifrostResp.Usage.TotalTokens,
		}
	}

	// Convert choices
	for i, choice := range bifrostResp.Choices {
		litellmChoice := LiteLLMChoice{
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
		}

		// Convert message
		msg := LiteLLMMessage{
			Role:    string(choice.Message.Role),
			Content: choice.Message.Content,
		}

		// Convert tool calls
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			toolCalls := []LiteLLMToolCall{}
			for _, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
				tc := LiteLLMToolCall{
					Type: *toolCall.Type,
					Function: LiteLLMFunctionCall{
						Name:      *toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
					},
				}
				if toolCall.ID != nil {
					tc.ID = *toolCall.ID
				}
				toolCalls = append(toolCalls, tc)
			}
			msg.ToolCalls = &toolCalls
		}

		litellmChoice.Message = msg
		litellmResp.Choices[i] = litellmChoice
	}

	return litellmResp
}

// DeriveLiteLLMCompletionFromBifrostResponse converts a Bifrost response to LiteLLM completion format
func DeriveLiteLLMCompletionFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *LiteLLMCompletionResponse {
	if bifrostResp == nil {
		return nil
	}

	litellmResp := &LiteLLMCompletionResponse{
		ID:      bifrostResp.ID,
		Object:  "text_completion",
		Created: bifrostResp.Created,
		Model:   bifrostResp.Model,
		Choices: make([]LiteLLMCompletionChoice, len(bifrostResp.Choices)),
	}

	// Convert usage information
	if bifrostResp.Usage != (schemas.LLMUsage{}) {
		litellmResp.Usage = &LiteLLMUsage{
			PromptTokens:     bifrostResp.Usage.PromptTokens,
			CompletionTokens: bifrostResp.Usage.CompletionTokens,
			TotalTokens:      bifrostResp.Usage.TotalTokens,
		}
	}

	// Convert choices
	for i, choice := range bifrostResp.Choices {
		text := ""
		if choice.Message.Content != nil {
			text = *choice.Message.Content
		}

		litellmChoice := LiteLLMCompletionChoice{
			Text:         text,
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
		}

		litellmResp.Choices[i] = litellmChoice
	}

	return litellmResp
}
