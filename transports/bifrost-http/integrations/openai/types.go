package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}

var fnTypePtr = stringPtr(string(schemas.ToolChoiceTypeFunction))

// OpenAIMessage represents a message in the OpenAI chat format
type OpenAIMessage struct {
	Role         string              `json:"role"`
	Content      *string             `json:"content,omitempty"`
	Name         *string             `json:"name,omitempty"`
	ToolCalls    *[]OpenAIToolCall   `json:"tool_calls,omitempty"`
	ToolCallID   *string             `json:"tool_call_id,omitempty"`
	FunctionCall *OpenAIFunctionCall `json:"function_call,omitempty"`
}

// OpenAIToolCall represents a tool call in OpenAI format
type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function OpenAIFunctionCall `json:"function"`
}

// OpenAIFunctionCall represents a function call in OpenAI format
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAITool represents a tool in OpenAI format
type OpenAITool struct {
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

// OpenAIFunction represents a function definition in OpenAI format
type OpenAIFunction struct {
	Name        string      `json:"name"`
	Description *string     `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model            string             `json:"model"`
	Messages         []OpenAIMessage    `json:"messages"`
	MaxTokens        *int               `json:"max_tokens,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
	N                *int               `json:"n,omitempty"`
	Stop             interface{}        `json:"stop,omitempty"`
	PresencePenalty  *float64           `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64           `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]float64 `json:"logit_bias,omitempty"`
	User             *string            `json:"user,omitempty"`
	Functions        *[]OpenAIFunction  `json:"functions,omitempty"`
	FunctionCall     interface{}        `json:"function_call,omitempty"`
	Tools            *[]OpenAITool      `json:"tools,omitempty"`
	ToolChoice       interface{}        `json:"tool_choice,omitempty"`
	Stream           *bool              `json:"stream,omitempty"`
	LogProbs         *bool              `json:"logprobs,omitempty"`
	TopLogProbs      *int               `json:"top_logprobs,omitempty"`
	ResponseFormat   interface{}        `json:"response_format,omitempty"`
	Seed             *int               `json:"seed,omitempty"`
}

// ConvertToBifrostRequest converts an OpenAI chat request to Bifrost format
func (r *OpenAIChatRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	bifrostReq := &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
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

		// Handle tool calls and function calls for assistant messages
		var toolCalls []schemas.ToolCall

		// Add modern tool calls
		if msg.ToolCalls != nil {
			for _, toolCall := range *msg.ToolCalls {
				tc := schemas.ToolCall{
					Type: &toolCall.Type,
					ID:   &toolCall.ID,
					Function: schemas.FunctionCall{
						Name:      &toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
					},
				}
				toolCalls = append(toolCalls, tc)
			}
		}

		// Add legacy function calls
		if msg.FunctionCall != nil {
			tc := schemas.ToolCall{
				Type: fnTypePtr,
				Function: schemas.FunctionCall{
					Name:      &msg.FunctionCall.Name,
					Arguments: msg.FunctionCall.Arguments,
				},
			}
			toolCalls = append(toolCalls, tc)
		}

		// Assign AssistantMessage only if we have tool calls
		if len(toolCalls) > 0 {
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
		r.FrequencyPenalty != nil || r.N != nil || r.LogProbs != nil || r.TopLogProbs != nil ||
		r.Stop != nil || r.LogitBias != nil {
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
		if r.TopLogProbs != nil {
			params.ExtraParams["top_logprobs"] = r.TopLogProbs
		}
		if r.Stop != nil {
			params.ExtraParams["stop"] = r.Stop
		}
		if r.LogitBias != nil {
			params.ExtraParams["logit_bias"] = r.LogitBias
		}

		bifrostReq.Params = params
	}

	// Convert tools and functions (legacy)
	var allTools []schemas.Tool

	// Handle modern Tools field
	if r.Tools != nil {
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
			allTools = append(allTools, t)
		}
	}

	// Handle legacy Functions field
	if r.Functions != nil {
		for _, function := range *r.Functions {
			description := ""
			if function.Description != nil {
				description = *function.Description
			}

			// Convert parameters interface{} to FunctionParameters
			params := schemas.FunctionParameters{
				Type: "object",
			}
			if function.Parameters != nil {
				if paramMap, ok := function.Parameters.(map[string]interface{}); ok {
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
				Type: "function",
				Function: schemas.Function{
					Name:        function.Name,
					Description: description,
					Parameters:  params,
				},
			}
			allTools = append(allTools, t)
		}
	}

	// Set tools if any were found
	if len(allTools) > 0 {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ModelParameters{}
		}
		bifrostReq.Params.Tools = &allTools
	}

	// Convert tool choice (from either tool_choice or function_call)
	if r.ToolChoice != nil || r.FunctionCall != nil {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ModelParameters{}
		}

		// Handle ToolChoice (modern format)
		if r.ToolChoice != nil {
			toolChoice := &schemas.ToolChoice{}

			switch tc := r.ToolChoice.(type) {
			case string:
				// Handle "none", "auto", etc.
				switch tc {
				case "none":
					toolChoice.Type = schemas.ToolChoiceTypeNone
				case "auto":
					toolChoice.Type = schemas.ToolChoiceTypeAuto
				case "required":
					toolChoice.Type = schemas.ToolChoiceTypeRequired
				default:
					toolChoice.Type = schemas.ToolChoiceTypeAuto // fallback
				}
			case map[string]interface{}:
				// Handle object format like {"type": "function", "function": {"name": "get_weather"}}
				if typeVal, ok := tc["type"].(string); ok {
					switch typeVal {
					case "function":
						toolChoice.Type = schemas.ToolChoiceTypeFunction
						if functionVal, ok := tc["function"].(map[string]interface{}); ok {
							if name, ok := functionVal["name"].(string); ok {
								toolChoice.Function = schemas.ToolChoiceFunction{Name: name}
							}
						}
					case "none":
						toolChoice.Type = schemas.ToolChoiceTypeNone
					case "auto":
						toolChoice.Type = schemas.ToolChoiceTypeAuto
					case "required":
						toolChoice.Type = schemas.ToolChoiceTypeRequired
					default:
						toolChoice.Type = schemas.ToolChoiceTypeAuto // fallback
					}
				}
			}

			bifrostReq.Params.ToolChoice = toolChoice
		} else if r.FunctionCall != nil {
			// Handle legacy FunctionCall
			toolChoice := &schemas.ToolChoice{}

			switch fc := r.FunctionCall.(type) {
			case string:
				// Handle "none", "auto"
				switch fc {
				case "none":
					toolChoice.Type = schemas.ToolChoiceTypeNone
				case "auto":
					toolChoice.Type = schemas.ToolChoiceTypeAuto
				default:
					toolChoice.Type = schemas.ToolChoiceTypeAuto // fallback
				}
			case map[string]interface{}:
				// Handle object format like {"name": "get_weather"}
				if name, ok := fc["name"].(string); ok {
					toolChoice.Type = schemas.ToolChoiceTypeFunction
					toolChoice.Function = schemas.ToolChoiceFunction{Name: name}
				}
			}

			bifrostReq.Params.ToolChoice = toolChoice
		}
	}

	return bifrostReq
}

// OpenAIChatResponse represents an OpenAI chat completion response
type OpenAIChatResponse struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int            `json:"created"`
	Model             string         `json:"model"`
	Choices           []OpenAIChoice `json:"choices"`
	Usage             *OpenAIUsage   `json:"usage,omitempty"`
	SystemFingerprint *string        `json:"system_fingerprint,omitempty"`
}

// OpenAIChoice represents a choice in the OpenAI response
type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason *string       `json:"finish_reason,omitempty"`
	LogProbs     interface{}   `json:"logprobs,omitempty"`
}

// OpenAIUsage represents usage information in OpenAI format
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// DeriveOpenAIFromBifrostResponse converts a Bifrost response to OpenAI format
func DeriveOpenAIFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *OpenAIChatResponse {
	if bifrostResp == nil {
		return nil
	}

	openaiResp := &OpenAIChatResponse{
		ID:      bifrostResp.ID,
		Object:  "chat.completion",
		Created: bifrostResp.Created,
		Model:   bifrostResp.Model,
		Choices: make([]OpenAIChoice, len(bifrostResp.Choices)),
	}

	if bifrostResp.SystemFingerprint != nil {
		openaiResp.SystemFingerprint = bifrostResp.SystemFingerprint
	}

	// Convert usage information
	if bifrostResp.Usage != (schemas.LLMUsage{}) {
		openaiResp.Usage = &OpenAIUsage{
			PromptTokens:     bifrostResp.Usage.PromptTokens,
			CompletionTokens: bifrostResp.Usage.CompletionTokens,
			TotalTokens:      bifrostResp.Usage.TotalTokens,
		}
	}

	// Convert choices
	for i, choice := range bifrostResp.Choices {
		openaiChoice := OpenAIChoice{
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
		}

		// Convert message
		msg := OpenAIMessage{
			Role:    string(choice.Message.Role),
			Content: choice.Message.Content,
		}

		// Convert tool calls for assistant messages
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			toolCalls := []OpenAIToolCall{}
			for _, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
				tc := OpenAIToolCall{
					Type: *toolCall.Type,
					Function: OpenAIFunctionCall{
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

			// Re-emit legacy function_call field when exactly one function tool-call is present
			if len(toolCalls) == 1 && toolCalls[0].Type == "function" {
				msg.FunctionCall = &OpenAIFunctionCall{
					Name:      toolCalls[0].Function.Name,
					Arguments: toolCalls[0].Function.Arguments,
				}
			}
		}

		// Handle tool messages - propagate tool_call_id
		if choice.Message.ToolMessage != nil && choice.Message.ToolMessage.ToolCallID != nil {
			msg.ToolCallID = choice.Message.ToolMessage.ToolCallID
		}

		openaiChoice.Message = msg
		openaiResp.Choices[i] = openaiChoice
	}

	return openaiResp
}
