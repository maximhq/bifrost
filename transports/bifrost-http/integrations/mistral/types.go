package mistral

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}

// Since Mistral uses OpenAI-compatible format, we'll reuse similar structures
// but map to Mistral provider

// MistralMessage represents a message in the Mistral chat format (OpenAI-compatible)
type MistralMessage struct {
	Role       string             `json:"role"`
	Content    *string            `json:"content,omitempty"`
	Name       *string            `json:"name,omitempty"`
	ToolCalls  *[]MistralToolCall `json:"tool_calls,omitempty"`
	ToolCallID *string            `json:"tool_call_id,omitempty"`
}

// MistralToolCall represents a tool call in Mistral format
type MistralToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function MistralFunctionCall `json:"function"`
}

// MistralFunctionCall represents a function call in Mistral format
type MistralFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// MistralTool represents a tool in Mistral format
type MistralTool struct {
	Type     string          `json:"type"`
	Function MistralFunction `json:"function"`
}

// MistralFunction represents a function definition in Mistral format
type MistralFunction struct {
	Name        string      `json:"name"`
	Description *string     `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

// MistralChatRequest represents a Mistral chat completion request
type MistralChatRequest struct {
	Model          string           `json:"model"`
	Messages       []MistralMessage `json:"messages"`
	MaxTokens      *int             `json:"max_tokens,omitempty"`
	Temperature    *float64         `json:"temperature,omitempty"`
	TopP           *float64         `json:"top_p,omitempty"`
	RandomSeed     *int             `json:"random_seed,omitempty"`
	SafePrompt     *bool            `json:"safe_prompt,omitempty"`
	Tools          *[]MistralTool   `json:"tools,omitempty"`
	ToolChoice     interface{}      `json:"tool_choice,omitempty"`
	Stream         *bool            `json:"stream,omitempty"`
	ResponseFormat interface{}      `json:"response_format,omitempty"`
}

// ConvertToBifrostRequest converts a Mistral chat request to Bifrost format
func (r *MistralChatRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	// Note: Mistral uses OpenAI-compatible API format, so we use OpenAI provider
	// This is the correct approach since Mistral follows OpenAI's API specification
	bifrostReq := &schemas.BifrostRequest{
		Provider: schemas.OpenAI, // Mistral is OpenAI-compatible
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
	if r.MaxTokens != nil || r.Temperature != nil || r.TopP != nil || r.RandomSeed != nil || r.SafePrompt != nil {
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
		if r.RandomSeed != nil {
			params.ExtraParams["random_seed"] = r.RandomSeed
		}
		if r.SafePrompt != nil {
			params.ExtraParams["safe_prompt"] = r.SafePrompt
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

// MistralChatResponse represents a Mistral chat completion response
type MistralChatResponse struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int             `json:"created"`
	Model   string          `json:"model"`
	Choices []MistralChoice `json:"choices"`
	Usage   *MistralUsage   `json:"usage,omitempty"`
}

// MistralChoice represents a choice in the Mistral response
type MistralChoice struct {
	Index        int            `json:"index"`
	Message      MistralMessage `json:"message"`
	FinishReason *string        `json:"finish_reason,omitempty"`
}

// MistralUsage represents usage information in Mistral format
type MistralUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// DeriveMistralFromBifrostResponse converts a Bifrost response to Mistral format
func DeriveMistralFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *MistralChatResponse {
	if bifrostResp == nil {
		return nil
	}

	mistralResp := &MistralChatResponse{
		ID:      bifrostResp.ID,
		Object:  "chat.completion",
		Created: bifrostResp.Created,
		Model:   bifrostResp.Model,
		Choices: make([]MistralChoice, len(bifrostResp.Choices)),
	}

	// Convert usage information
	if bifrostResp.Usage != (schemas.LLMUsage{}) {
		mistralResp.Usage = &MistralUsage{
			PromptTokens:     bifrostResp.Usage.PromptTokens,
			CompletionTokens: bifrostResp.Usage.CompletionTokens,
			TotalTokens:      bifrostResp.Usage.TotalTokens,
		}
	}

	// Convert choices
	for i, choice := range bifrostResp.Choices {
		mistralChoice := MistralChoice{
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
		}

		// Convert message
		msg := MistralMessage{
			Role:    string(choice.Message.Role),
			Content: choice.Message.Content,
		}

		// Convert tool calls
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			toolCalls := []MistralToolCall{}
			for _, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
				tc := MistralToolCall{
					Type: *toolCall.Type,
					Function: MistralFunctionCall{
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

		mistralChoice.Message = msg
		mistralResp.Choices[i] = mistralChoice
	}

	return mistralResp
}
