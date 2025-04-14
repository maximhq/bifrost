// Package providers implements various LLM providers and their utility functions.
// This file contains the Cohere provider implementation.
package providers

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/valyala/fasthttp"
)

// CohereParameterDefinition represents a parameter definition for a Cohere tool.
// It defines the type, description, and whether the parameter is required.
type CohereParameterDefinition struct {
	Type        string  `json:"type"`                  // Type of the parameter
	Description *string `json:"description,omitempty"` // Optional description of the parameter
	Required    bool    `json:"required"`              // Whether the parameter is required
}

// CohereTool represents a tool definition for the Cohere API.
// It includes the tool's name, description, and parameter definitions.
type CohereTool struct {
	Name                 string                               `json:"name"`                  // Name of the tool
	Description          string                               `json:"description"`           // Description of the tool
	ParameterDefinitions map[string]CohereParameterDefinition `json:"parameter_definitions"` // Definitions of the tool's parameters
}

// CohereToolCall represents a tool call made by the Cohere API.
// It includes the name of the tool and its parameters.
type CohereToolCall struct {
	Name       string      `json:"name"`       // Name of the tool being called
	Parameters interface{} `json:"parameters"` // Parameters passed to the tool
}

// CohereChatResponse represents the response from Cohere's chat API.
// It includes the response ID, generated text, chat history, and usage statistics.
type CohereChatResponse struct {
	ResponseID   string `json:"response_id"`   // Unique identifier for the response
	Text         string `json:"text"`          // Generated text response
	GenerationID string `json:"generation_id"` // ID of the generation
	ChatHistory  []struct {
		Role      interfaces.ModelChatMessageRole `json:"role"`       // Role of the message sender
		Message   string                          `json:"message"`    // Content of the message
		ToolCalls []CohereToolCall                `json:"tool_calls"` // Tool calls made in the message
	} `json:"chat_history"` // History of the chat conversation
	FinishReason string `json:"finish_reason"` // Reason for completion termination
	Meta         struct {
		APIVersion struct {
			Version string `json:"version"` // Version of the API used
		} `json:"api_version"` // API version information
		BilledUnits struct {
			InputTokens  float64 `json:"input_tokens"`  // Number of input tokens billed
			OutputTokens float64 `json:"output_tokens"` // Number of output tokens billed
		} `json:"billed_units"` // Token usage billing information
		Tokens struct {
			InputTokens  float64 `json:"input_tokens"`  // Number of input tokens used
			OutputTokens float64 `json:"output_tokens"` // Number of output tokens generated
		} `json:"tokens"` // Token usage statistics
	} `json:"meta"` // Metadata about the response
	ToolCalls []CohereToolCall `json:"tool_calls"` // Tool calls made in the response
}

// CohereError represents an error response from the Cohere API.
type CohereError struct {
	Message string `json:"message"` // Error message
}

// CohereProvider implements the Provider interface for Cohere.
type CohereProvider struct {
	logger interfaces.Logger // Logger for provider operations
	client *fasthttp.Client  // HTTP client for API requests
}

// NewCohereProvider creates a new Cohere provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts and connection limits.
func NewCohereProvider(config *interfaces.ProviderConfig, logger interfaces.Logger) *CohereProvider {
	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		cohereResponsePool.Put(&CohereChatResponse{})
		bifrostResponsePool.Put(&interfaces.BifrostResponse{})
	}

	return &CohereProvider{
		logger: logger,
		client: client,
	}
}

// GetProviderKey returns the provider identifier for Cohere.
func (provider *CohereProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.Cohere
}

// TextCompletion is not supported by the Cohere provider.
// Returns an error indicating that text completion is not supported.
func (provider *CohereProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	return nil, &interfaces.BifrostError{
		IsBifrostError: false,
		Error: interfaces.ErrorField{
			Message: "text completion is not supported by cohere provider",
		},
	}
}

// ChatCompletion performs a chat completion request to the Cohere API.
// It formats the request, sends it to Cohere, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *CohereProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
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

	preparedParams := prepareParams(params)

	// Prepare request body
	requestBody := mergeConfig(map[string]interface{}{
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
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: interfaces.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
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
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: interfaces.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		var errorResp CohereError

		bifrostErr := handleProviderAPIError(resp, errorResp)
		bifrostErr.Error.Message = errorResp.Message

		return nil, bifrostErr
	}

	// Read response body
	responseBody := resp.Body()

	// Create response object from pool
	response := acquireCohereResponse()
	defer releaseCohereResponse(response)

	// Create Bifrost response from pool
	bifrostResponse := acquireBifrostResponse()
	defer releaseBifrostResponse(bifrostResponse)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
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

	bifrostResponse.ID = response.ResponseID
	bifrostResponse.Choices = []interfaces.BifrostResponseChoice{
		{
			Index: 0,
			Message: interfaces.BifrostResponseChoiceMessage{
				Role:      role,
				Content:   &content,
				ToolCalls: &toolCalls,
			},
			FinishReason: &response.FinishReason,
		},
	}
	bifrostResponse.Usage = interfaces.LLMUsage{
		PromptTokens:     int(response.Meta.Tokens.InputTokens),
		CompletionTokens: int(response.Meta.Tokens.OutputTokens),
		TotalTokens:      int(response.Meta.Tokens.InputTokens + response.Meta.Tokens.OutputTokens),
	}
	bifrostResponse.Model = model
	bifrostResponse.ExtraFields = interfaces.BifrostResponseExtraFields{
		Provider: interfaces.Cohere,
		BilledUsage: &interfaces.BilledLLMUsage{
			PromptTokens:     float64Ptr(response.Meta.BilledUnits.InputTokens),
			CompletionTokens: float64Ptr(response.Meta.BilledUnits.OutputTokens),
		},
		ChatHistory: convertChatHistory(response.ChatHistory),
		RawResponse: rawResponse,
	}

	return bifrostResponse, nil
}

// convertChatHistory converts Cohere's chat history format to Bifrost's format for standardization.
// It transforms the chat history messages and their tool calls.
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
