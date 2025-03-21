package providers

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/valyala/fasthttp"
)

// CohereParameterDefinition represents a parameter definition for a Cohere tool
type CohereParameterDefinition struct {
	Type        string  `json:"type"`
	Description *string `json:"description,omitempty"`
	Required    bool    `json:"required"`
}

// CohereTool represents a tool definition for Cohere API
type CohereTool struct {
	Name                 string                               `json:"name"`
	Description          string                               `json:"description"`
	ParameterDefinitions map[string]CohereParameterDefinition `json:"parameter_definitions"`
}

type CohereToolCall struct {
	Name       string      `json:"name"`
	Parameters interface{} `json:"parameters"`
}

// CohereChatResponse represents the response from Cohere's chat API
type CohereChatResponse struct {
	ResponseID   string `json:"response_id"`
	Text         string `json:"text"`
	GenerationID string `json:"generation_id"`
	ChatHistory  []struct {
		Role      interfaces.ModelChatMessageRole `json:"role"`
		Message   string                          `json:"message"`
		ToolCalls []CohereToolCall                `json:"tool_calls"`
	} `json:"chat_history"`
	FinishReason string `json:"finish_reason"`
	Meta         struct {
		APIVersion struct {
			Version string `json:"version"`
		} `json:"api_version"`
		BilledUnits struct {
			InputTokens  float64 `json:"input_tokens"`
			OutputTokens float64 `json:"output_tokens"`
		} `json:"billed_units"`
		Tokens struct {
			InputTokens  float64 `json:"input_tokens"`
			OutputTokens float64 `json:"output_tokens"`
		} `json:"tokens"`
	} `json:"meta"`
	ToolCalls []CohereToolCall `json:"tool_calls"`
}

// OpenAIProvider implements the Provider interface for OpenAI
type CohereProvider struct {
	client *fasthttp.Client
}

// NewOpenAIProvider creates a new OpenAI provider instance
func NewCohereProvider(config *interfaces.ProviderConfig) *CohereProvider {
	return &CohereProvider{
		client: &fasthttp.Client{
			ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
			WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
			MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
		},
	}
}

func (provider *CohereProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.Cohere
}

func (provider *CohereProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
	return nil, fmt.Errorf("text completion is not supported by Cohere")
}

func (provider *CohereProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
	// Get the last message and chat history
	lastMessage := messages[len(messages)-1]
	chatHistory := messages[:len(messages)-1]

	// Transform chat history
	var cohereHistory []map[string]interface{}
	for _, msg := range chatHistory {
		cohereHistory = append(cohereHistory, map[string]interface{}{
			"role":    msg.Role,
			"message": msg.Content,
		})
	}

	preparedParams := PrepareParams(params)

	// Prepare request body
	requestBody := MergeConfig(map[string]interface{}{
		"message":      lastMessage.Content,
		"chat_history": cohereHistory,
		"model":        model,
	}, preparedParams)

	// Add tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		var tools []CohereTool
		for _, tool := range *params.Tools {
			parameterDefinitions := make(map[string]CohereParameterDefinition)
			params := tool.Function.Parameters
			for name, prop := range tool.Function.Parameters.Properties {
				propMap, ok := prop.(map[string]interface{})
				if ok {
					paramDef := CohereParameterDefinition{
						Required: slices.Contains(params.Required, name),
					}

					if typeStr, ok := propMap["type"].(string); ok {
						paramDef.Type = typeStr
					}

					if desc, ok := propMap["description"].(string); ok {
						paramDef.Description = &desc
					}

					parameterDefinitions[name] = paramDef
				}
			}

			tools = append(tools, CohereTool{
				Name:                 tool.Function.Name,
				Description:          tool.Function.Description,
				ParameterDefinitions: parameterDefinitions,
			})
		}
		requestBody["tools"] = tools
	}

	// Marshal request body
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %v", err)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("https://api.cohere.ai/v1/chat")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.SetBody(jsonBody)

	// Make request
	if err := provider.client.Do(req, resp); err != nil {
		return nil, fmt.Errorf("error making request: %v", err)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, fmt.Errorf("cohere error: %s", resp.Body())
	}

	// Read response body
	body := resp.Body()

	// Decode response
	var response CohereChatResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse Bedrock response: %v", err)
	}

	// Parse raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, fmt.Errorf("failed to parse raw response: %v", err)
	}

	// Transform tool calls if present
	var toolCalls []interfaces.ToolCall
	if response.ToolCalls != nil {
		for _, tool := range response.ToolCalls {
			function := interfaces.FunctionCall{
				Name: &tool.Name,
			}

			args, err := json.Marshal(tool.Parameters)
			if err != nil {
				function.Arguments = fmt.Sprintf("%v", tool.Parameters)
			} else {
				function.Arguments = string(args)
			}

			toolCalls = append(toolCalls, interfaces.ToolCall{
				Function: function,
			})
		}
	}

	// Get role and content from the last message in chat history
	var role interfaces.ModelChatMessageRole
	var content string
	if len(response.ChatHistory) > 0 {
		lastMsg := response.ChatHistory[len(response.ChatHistory)-1]
		role = lastMsg.Role
		content = lastMsg.Message
	} else {
		role = interfaces.RoleChatbot
		content = response.Text
	}

	// Create completion result
	result := &interfaces.BifrostResponse{
		ID: response.ResponseID,
		Choices: []interfaces.BifrostResponseChoice{
			{
				Index: 0,
				Message: interfaces.BifrostResponseChoiceMessage{
					Role:      role,
					Content:   &content,
					ToolCalls: &toolCalls,
				},
				FinishReason: &response.FinishReason,
			},
		},
		Usage: interfaces.LLMUsage{
			PromptTokens:     int(response.Meta.Tokens.InputTokens),
			CompletionTokens: int(response.Meta.Tokens.OutputTokens),
			TotalTokens:      int(response.Meta.Tokens.InputTokens + response.Meta.Tokens.OutputTokens),
		},

		ExtraFields: interfaces.BifrostResponseExtraFields{
			Provider: interfaces.Cohere,
			BilledUsage: &interfaces.BilledLLMUsage{
				PromptTokens:     float64Ptr(response.Meta.BilledUnits.InputTokens),
				CompletionTokens: float64Ptr(response.Meta.BilledUnits.OutputTokens),
			},
			ChatHistory: convertChatHistory(response.ChatHistory),
			RawResponse: rawResponse,
		},
		Model: model,
	}

	return result, nil
}

// Helper function to convert chat history to the correct type
func convertChatHistory(history []struct {
	Role      interfaces.ModelChatMessageRole `json:"role"`
	Message   string                          `json:"message"`
	ToolCalls []CohereToolCall                `json:"tool_calls"`
}) *[]interfaces.BifrostResponseChoiceMessage {
	converted := make([]interfaces.BifrostResponseChoiceMessage, len(history))
	for i, msg := range history {
		var toolCalls []interfaces.ToolCall
		if msg.ToolCalls != nil {
			for _, tool := range msg.ToolCalls {
				function := interfaces.FunctionCall{
					Name: &tool.Name,
				}

				args, err := json.Marshal(tool.Parameters)
				if err != nil {
					function.Arguments = fmt.Sprintf("%v", tool.Parameters)
				} else {
					function.Arguments = string(args)
				}

				toolCalls = append(toolCalls, interfaces.ToolCall{
					Function: function,
				})
			}
		}
		converted[i] = interfaces.BifrostResponseChoiceMessage{
			Role:      msg.Role,
			Content:   &msg.Message,
			ToolCalls: &toolCalls,
		}
	}
	return &converted
}

// Helper function to create a pointer to a float64
func float64Ptr(f float64) *float64 {
	return &f
}
