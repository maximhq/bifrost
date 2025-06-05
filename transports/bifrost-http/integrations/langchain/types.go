package langchain

import (
	"encoding/json"

	"github.com/maximhq/bifrost/core/schemas"
)

// LangChain-style message types

// LangChainMessage represents a message in LangChain format
type LangChainMessage struct {
	Type       string               `json:"type"`                   // "human", "ai", "system", "tool"
	Content    string               `json:"content"`                // Text content
	Name       *string              `json:"name,omitempty"`         // Optional name
	ToolCalls  *[]LangChainToolCall `json:"tool_calls,omitempty"`   // For AI messages with tool calls
	ToolCallID *string              `json:"tool_call_id,omitempty"` // For tool messages
}

// LangChainToolCall represents a tool call in LangChain format
type LangChainToolCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
	ID   *string                `json:"id,omitempty"`
}

// LangChainTool represents a tool definition in LangChain format
type LangChainTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	ArgsSchema  map[string]interface{} `json:"args_schema"`
}

// Request structures

// LangChainChatRequest represents a simplified LangChain chat request
type LangChainChatRequest struct {
	Model       string                 `json:"model"`
	Provider    *string                `json:"provider,omitempty"` // Optional explicit provider
	Messages    []LangChainMessage     `json:"messages"`
	Temperature *float64               `json:"temperature,omitempty"`
	MaxTokens   *int                   `json:"max_tokens,omitempty"`
	TopP        *float64               `json:"top_p,omitempty"`
	Tools       *[]LangChainTool       `json:"tools,omitempty"`
	StopWords   *[]string              `json:"stop_words,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// ConvertToBifrostRequest converts a LangChain chat request to Bifrost format
func (r *LangChainChatRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	provider := schemas.OpenAI // Default
	if r.Provider != nil {
		provider = schemas.ModelProvider(*r.Provider)
	}

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

		// Map LangChain message types to Bifrost roles
		switch msg.Type {
		case "human":
			bifrostMsg.Role = schemas.ModelChatMessageRoleUser
		case "ai":
			bifrostMsg.Role = schemas.ModelChatMessageRoleAssistant
		case "system":
			bifrostMsg.Role = schemas.ModelChatMessageRoleSystem
		case "tool":
			bifrostMsg.Role = schemas.ModelChatMessageRoleTool
		default:
			bifrostMsg.Role = schemas.ModelChatMessageRoleUser
		}

		bifrostMsg.Content = &msg.Content

		// Handle tool calls for AI messages
		if msg.ToolCalls != nil {
			toolCalls := []schemas.ToolCall{}
			for _, toolCall := range *msg.ToolCalls {
				// Convert args map to JSON string
				argsJSON := "{}"
				if len(toolCall.Args) > 0 {
					// In production, use proper JSON marshaling
					argsJSON = mapToJSONString(toolCall.Args)
				}

				tc := schemas.ToolCall{
					Type: stringPtr("function"),
					ID:   toolCall.ID,
					Function: schemas.FunctionCall{
						Name:      &toolCall.Name,
						Arguments: argsJSON,
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
	if r.Temperature != nil || r.MaxTokens != nil || r.TopP != nil || r.StopWords != nil {
		params := &schemas.ModelParameters{}

		if r.Temperature != nil {
			params.Temperature = r.Temperature
		}
		if r.MaxTokens != nil {
			params.MaxTokens = r.MaxTokens
		}
		if r.TopP != nil {
			params.TopP = r.TopP
		}
		if r.StopWords != nil {
			params.StopSequences = r.StopWords
		}

		bifrostReq.Params = params
	}

	// Convert tools
	if r.Tools != nil {
		tools := []schemas.Tool{}
		for _, tool := range *r.Tools {
			// Convert args_schema to FunctionParameters
			params := schemas.FunctionParameters{
				Type: "object",
			}
			if tool.ArgsSchema != nil {
				if typeVal, ok := tool.ArgsSchema["type"].(string); ok {
					params.Type = typeVal
				}
				if desc, ok := tool.ArgsSchema["description"].(string); ok {
					params.Description = &desc
				}
				if required, ok := tool.ArgsSchema["required"].([]interface{}); ok {
					reqStrings := make([]string, len(required))
					for i, req := range required {
						if reqStr, ok := req.(string); ok {
							reqStrings[i] = reqStr
						}
					}
					params.Required = reqStrings
				}
				if properties, ok := tool.ArgsSchema["properties"].(map[string]interface{}); ok {
					params.Properties = properties
				}
				if enum, ok := tool.ArgsSchema["enum"].([]interface{}); ok {
					enumStrings := make([]string, len(enum))
					for i, e := range enum {
						if eStr, ok := e.(string); ok {
							enumStrings[i] = eStr
						}
					}
					params.Enum = &enumStrings
				}
			}

			t := schemas.Tool{
				Type: "function",
				Function: schemas.Function{
					Name:        tool.Name,
					Description: tool.Description,
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

// LangChainInvokeRequest represents a general LangChain invoke request
type LangChainInvokeRequest struct {
	Type     string                 `json:"type"` // "chat" or "completion"
	Model    string                 `json:"model"`
	Provider *string                `json:"provider,omitempty"`
	Input    interface{}            `json:"input"` // Can be string or messages array
	Config   map[string]interface{} `json:"config,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ConvertToBifrostRequest converts a LangChain invoke request to Bifrost format
func (r *LangChainInvokeRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	provider := schemas.OpenAI // Default
	if r.Provider != nil {
		provider = schemas.ModelProvider(*r.Provider)
	}

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    r.Model,
	}

	if r.Type == "chat" {
		// Handle chat input - could be messages array or string
		if messages, ok := r.Input.([]interface{}); ok {
			chatInput := []schemas.BifrostMessage{}
			for _, msgInterface := range messages {
				if msgMap, ok := msgInterface.(map[string]interface{}); ok {
					msg := schemas.BifrostMessage{
						Role: schemas.ModelChatMessageRoleUser,
					}
					if content, ok := msgMap["content"].(string); ok {
						msg.Content = &content
					}
					if role, ok := msgMap["type"].(string); ok {
						switch role {
						case "human":
							msg.Role = schemas.ModelChatMessageRoleUser
						case "ai":
							msg.Role = schemas.ModelChatMessageRoleAssistant
						case "system":
							msg.Role = schemas.ModelChatMessageRoleSystem
						}
					}
					chatInput = append(chatInput, msg)
				}
			}
			bifrostReq.Input = schemas.RequestInput{
				ChatCompletionInput: &chatInput,
			}
		} else if inputStr, ok := r.Input.(string); ok {
			// Single string input - convert to user message
			chatInput := []schemas.BifrostMessage{
				{
					Role:    schemas.ModelChatMessageRoleUser,
					Content: &inputStr,
				},
			}
			bifrostReq.Input = schemas.RequestInput{
				ChatCompletionInput: &chatInput,
			}
		}
	} else {
		// Text completion
		if inputStr, ok := r.Input.(string); ok {
			bifrostReq.Input = schemas.RequestInput{
				TextCompletionInput: &inputStr,
			}
		}
	}

	// Convert config to parameters
	if r.Config != nil {
		params := &schemas.ModelParameters{}

		if temp, ok := r.Config["temperature"].(float64); ok {
			params.Temperature = &temp
		}
		if maxTokens, ok := r.Config["max_tokens"].(float64); ok {
			maxTokensInt := int(maxTokens)
			params.MaxTokens = &maxTokensInt
		}
		if topP, ok := r.Config["top_p"].(float64); ok {
			params.TopP = &topP
		}

		bifrostReq.Params = params
	}

	return bifrostReq
}

// LangChainBatchRequest represents a batch request
type LangChainBatchRequest struct {
	Inputs []LangChainInvokeRequest `json:"inputs"`
	Config map[string]interface{}   `json:"config,omitempty"`
}

// Response structures

// LangChainChatResponse represents a LangChain chat response
type LangChainChatResponse struct {
	Type      string                 `json:"type"` // "ai"
	Content   string                 `json:"content"`
	ToolCalls *[]LangChainToolCall   `json:"tool_calls,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Usage     *LangChainUsage        `json:"usage,omitempty"`
}

// LangChainInvokeResponse represents a general invoke response
type LangChainInvokeResponse struct {
	Output   interface{}            `json:"output"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Usage    *LangChainUsage        `json:"usage,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

// LangChainBatchResponse represents a batch response
type LangChainBatchResponse struct {
	Results []LangChainInvokeResponse `json:"results"`
}

// LangChainUsage represents usage information
type LangChainUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Conversion functions

// DeriveLangChainFromBifrostResponse converts a Bifrost response to LangChain chat format
func DeriveLangChainFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *LangChainChatResponse {
	if bifrostResp == nil {
		return nil
	}

	response := &LangChainChatResponse{
		Type:     "ai",
		Metadata: make(map[string]interface{}),
	}

	// Convert usage information
	if bifrostResp.Usage != (schemas.LLMUsage{}) {
		response.Usage = &LangChainUsage{
			InputTokens:  bifrostResp.Usage.PromptTokens,
			OutputTokens: bifrostResp.Usage.CompletionTokens,
			TotalTokens:  bifrostResp.Usage.TotalTokens,
		}
	}

	// Get first choice
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0]

		if choice.Message.Content != nil {
			response.Content = *choice.Message.Content
		}

		// Convert tool calls
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			toolCalls := []LangChainToolCall{}
			for _, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
				tc := LangChainToolCall{
					Name: *toolCall.Function.Name,
					Args: stringToArgsMap(toolCall.Function.Arguments),
					ID:   toolCall.ID,
				}
				toolCalls = append(toolCalls, tc)
			}
			response.ToolCalls = &toolCalls
		}

		// Add metadata
		response.Metadata["model"] = bifrostResp.Model
		if choice.FinishReason != nil {
			response.Metadata["finish_reason"] = *choice.FinishReason
		}
	}

	return response
}

// DeriveLangChainInvokeFromBifrostResponse converts a Bifrost response to LangChain invoke format
func DeriveLangChainInvokeFromBifrostResponse(bifrostResp *schemas.BifrostResponse, requestType string) *LangChainInvokeResponse {
	if bifrostResp == nil {
		return nil
	}

	response := &LangChainInvokeResponse{
		Metadata: make(map[string]interface{}),
	}

	// Convert usage information
	if bifrostResp.Usage != (schemas.LLMUsage{}) {
		response.Usage = &LangChainUsage{
			InputTokens:  bifrostResp.Usage.PromptTokens,
			OutputTokens: bifrostResp.Usage.CompletionTokens,
			TotalTokens:  bifrostResp.Usage.TotalTokens,
		}
	}

	// Set output based on request type
	if requestType == "chat" {
		if len(bifrostResp.Choices) > 0 {
			choice := bifrostResp.Choices[0]
			chatMsg := map[string]interface{}{
				"type": "ai",
			}
			if choice.Message.Content != nil {
				chatMsg["content"] = *choice.Message.Content
			}
			response.Output = chatMsg
		}
	} else {
		// Text completion
		if len(bifrostResp.Choices) > 0 && bifrostResp.Choices[0].Message.Content != nil {
			response.Output = *bifrostResp.Choices[0].Message.Content
		}
	}

	// Add metadata
	response.Metadata["model"] = bifrostResp.Model
	if len(bifrostResp.Choices) > 0 && bifrostResp.Choices[0].FinishReason != nil {
		response.Metadata["finish_reason"] = *bifrostResp.Choices[0].FinishReason
	}

	return response
}

// Helper functions

func stringPtr(s string) *string {
	return &s
}

func mapToJSONString(m map[string]interface{}) string {
	jsonBytes, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

func stringToArgsMap(s string) map[string]interface{} {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return make(map[string]interface{})
	}
	return result
}
