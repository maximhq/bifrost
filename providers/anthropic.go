// Package providers implements various LLM providers and their utility functions.
// This file contains the Anthropic provider implementation.
package providers

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/maxim-go"
)

// AnthropicToolChoice represents the tool choice configuration for Anthropic's API.
// It specifies how tools should be used in the completion request.
type AnthropicToolChoice struct {
	Type                   interfaces.ToolChoiceType `json:"type"`                      // Type of tool choice
	Name                   *string                   `json:"name"`                      // Name of the tool to use
	DisableParallelToolUse *bool                     `json:"disable_parallel_tool_use"` // Whether to disable parallel tool use
}

// AnthropicTextResponse represents the response structure from Anthropic's text completion API.
// It includes the completion text, model information, and token usage statistics.
type AnthropicTextResponse struct {
	ID         string `json:"id"`         // Unique identifier for the completion
	Type       string `json:"type"`       // Type of completion
	Completion string `json:"completion"` // Generated completion text
	Model      string `json:"model"`      // Model used for the completion
	Usage      struct {
		InputTokens  int `json:"input_tokens"`  // Number of input tokens used
		OutputTokens int `json:"output_tokens"` // Number of output tokens generated
	} `json:"usage"` // Token usage statistics
}

// AnthropicChatResponse represents the response structure from Anthropic's chat completion API.
// It includes message content, model information, and token usage statistics.
type AnthropicChatResponse struct {
	ID      string `json:"id"`   // Unique identifier for the completion
	Type    string `json:"type"` // Type of completion
	Role    string `json:"role"` // Role of the message sender
	Content []struct {
		Type     string                 `json:"type"`               // Type of content
		Text     string                 `json:"text,omitempty"`     // Text content
		Thinking string                 `json:"thinking,omitempty"` // Thinking process
		ID       string                 `json:"id"`                 // Content identifier
		Name     string                 `json:"name"`               // Name of the content
		Input    map[string]interface{} `json:"input"`              // Input parameters
	} `json:"content"` // Array of content items
	Model        string  `json:"model"`                   // Model used for the completion
	StopReason   string  `json:"stop_reason,omitempty"`   // Reason for completion termination
	StopSequence *string `json:"stop_sequence,omitempty"` // Sequence that caused completion to stop
	Usage        struct {
		InputTokens  int `json:"input_tokens"`  // Number of input tokens used
		OutputTokens int `json:"output_tokens"` // Number of output tokens generated
	} `json:"usage"` // Token usage statistics
}

// AnthropicError represents the error response structure from Anthropic's API.
// It includes error type and message information.
type AnthropicError struct {
	Type  string `json:"type"` // Type of error
	Error struct {
		Type    string `json:"type"`    // Error type
		Message string `json:"message"` // Error message
	} `json:"error"` // Error details
}

// AnthropicProvider implements the Provider interface for Anthropic's Claude API.
type AnthropicProvider struct {
	logger interfaces.Logger // Logger for provider operations
	client *fasthttp.Client  // HTTP client for API requests
}

// NewAnthropicProvider creates a new Anthropic provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewAnthropicProvider(config *interfaces.ProviderConfig, logger interfaces.Logger) *AnthropicProvider {
	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		anthropicTextResponsePool.Put(&AnthropicTextResponse{})
		anthropicChatResponsePool.Put(&AnthropicChatResponse{})
		bifrostResponsePool.Put(&interfaces.BifrostResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	return &AnthropicProvider{
		logger: logger,
		client: client,
	}
}

// GetProviderKey returns the provider identifier for Anthropic.
func (provider *AnthropicProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.Anthropic
}

// PrepareTextCompletionParams prepares text completion parameters for Anthropic's API.
// It handles parameter mapping and conversion to the format expected by Anthropic.
// Returns the modified parameters map.
func (provider *AnthropicProvider) PrepareTextCompletionParams(params map[string]interface{}) map[string]interface{} {
	// Check if there is a key entry for max_tokens
	if maxTokens, exists := params["max_tokens"]; exists {
		// Check if max_tokens_to_sample is already present
		if _, exists := params["max_tokens_to_sample"]; !exists {
			// If max_tokens_to_sample is not present, rename max_tokens to max_tokens_to_sample
			params["max_tokens_to_sample"] = maxTokens
		}
		delete(params, "max_tokens")
	}
	return params
}

// PrepareToolChoices prepares tool choice parameters for Anthropic's API.
// It handles conversion of tool choice parameters to the format expected by Anthropic.
// Returns the modified parameters map.
func (provider *AnthropicProvider) PrepareToolChoices(params map[string]interface{}) map[string]interface{} {
	toolChoice, exists := params["tool_choice"]
	if !exists {
		return params
	}

	switch tc := toolChoice.(type) {
	case interfaces.ToolChoice:
		anthropicToolChoice := AnthropicToolChoice{
			Type: tc.Type,
			Name: &tc.Function.Name,
		}

		parallelToolCalls, exists := params["parallel_tool_calls"]
		if !exists {
			return params
		}

		switch parallelTC := parallelToolCalls.(type) {
		case bool:
			disableParallel := !parallelTC
			anthropicToolChoice.DisableParallelToolUse = &disableParallel

			delete(params, "parallel_tool_calls")
		}

		params["tool_choice"] = anthropicToolChoice
	}

	return params
}

// CompleteRequest sends a request to Anthropic's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AnthropicProvider) CompleteRequest(requestBody map[string]interface{}, url string, key string) ([]byte, *interfaces.BifrostError) {
	// Marshal the request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: interfaces.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
	}

	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(url)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.SetBody(jsonData)

	// Send the request
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
		var errorResp AnthropicError

		bifrostErr := handleProviderAPIError(resp, errorResp)
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Message = errorResp.Error.Message

		return nil, bifrostErr
	}

	// Read the response body
	body := resp.Body()

	return body, nil
}

// TextCompletion performs a text completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	preparedParams := provider.PrepareTextCompletionParams(prepareParams(params))

	// Merge additional parameters
	requestBody := mergeConfig(map[string]interface{}{
		"model":  model,
		"prompt": fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", text),
	}, preparedParams)

	responseBody, err := provider.CompleteRequest(requestBody, "https://api.anthropic.com/v1/complete", key)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireAnthropicTextResponse()
	defer releaseAnthropicTextResponse(response)

	// Create Bifrost response from pool
	bifrostResponse := acquireBifrostResponse()
	defer releaseBifrostResponse(bifrostResponse)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse.ID = response.ID
	bifrostResponse.Choices = []interfaces.BifrostResponseChoice{
		{
			Index: 0,
			Message: interfaces.BifrostResponseChoiceMessage{
				Role:    interfaces.RoleAssistant,
				Content: &response.Completion,
			},
		},
	}
	bifrostResponse.Usage = interfaces.LLMUsage{
		PromptTokens:     response.Usage.InputTokens,
		CompletionTokens: response.Usage.OutputTokens,
		TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
	}
	bifrostResponse.Model = response.Model
	bifrostResponse.ExtraFields = interfaces.BifrostResponseExtraFields{
		Provider:    interfaces.Anthropic,
		RawResponse: rawResponse,
	}

	return bifrostResponse, nil
}

// ChatCompletion performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	// Format messages for Anthropic API
	var formattedMessages []map[string]interface{}
	for _, msg := range messages {
		if msg.ImageContent != nil {
			var content []map[string]interface{}

			imageContent := map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type": msg.ImageContent.Type,
				},
			}

			// Handle different image source types
			if *msg.ImageContent.Type == "url" {
				imageContent["source"].(map[string]interface{})["url"] = msg.ImageContent.URL
			} else {
				imageContent["source"].(map[string]interface{})["media_type"] = msg.ImageContent.MediaType
				imageContent["source"].(map[string]interface{})["data"] = msg.ImageContent.URL
			}

			content = append(content, imageContent)

			// Add text content if present
			if msg.Content != nil {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": msg.Content,
				})
			}

			formattedMessages = append(formattedMessages, map[string]interface{}{
				"role":    msg.Role,
				"content": content,
			})
		} else {
			formattedMessages = append(formattedMessages, map[string]interface{}{
				"role":    msg.Role,
				"content": msg.Content,
			})
		}
	}

	preparedParams := prepareParams(params)

	// Transform tools if present
	if params != nil && params.Tools != nil && len(*params.Tools) > 0 {
		var tools []map[string]interface{}
		for _, tool := range *params.Tools {
			tools = append(tools, map[string]interface{}{
				"name":         tool.Function.Name,
				"description":  tool.Function.Description,
				"input_schema": tool.Function.Parameters,
			})
		}

		preparedParams["tools"] = tools
	}

	// Merge additional parameters
	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	responseBody, err := provider.CompleteRequest(requestBody, "https://api.anthropic.com/v1/messages", key)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireAnthropicChatResponse()
	defer releaseAnthropicChatResponse(response)

	// Create Bifrost response from pool
	bifrostResponse := acquireBifrostResponse()
	defer releaseBifrostResponse(bifrostResponse)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Process the response into our BifrostResponse format
	var choices []interfaces.BifrostResponseChoice

	// Process content and tool calls
	for i, c := range response.Content {
		var content string
		var toolCalls []interfaces.ToolCall

		switch c.Type {
		case "thinking":
			content = c.Thinking
		case "text":
			content = c.Text
		case "tool_use":
			function := interfaces.FunctionCall{
				Name: &c.Name,
			}

			args, err := json.Marshal(c.Input)
			if err != nil {
				function.Arguments = fmt.Sprintf("%v", c.Input)
			} else {
				function.Arguments = string(args)
			}

			toolCalls = append(toolCalls, interfaces.ToolCall{
				Type:     maxim.StrPtr("function"),
				ID:       &c.ID,
				Function: function,
			})
		}

		choices = append(choices, interfaces.BifrostResponseChoice{
			Index: i,
			Message: interfaces.BifrostResponseChoiceMessage{
				Role:      interfaces.RoleAssistant,
				Content:   &content,
				ToolCalls: &toolCalls,
			},
			FinishReason: &response.StopReason,
			StopString:   response.StopSequence,
		})
	}

	bifrostResponse.ID = response.ID
	bifrostResponse.Choices = choices
	bifrostResponse.Usage = interfaces.LLMUsage{
		PromptTokens:     response.Usage.InputTokens,
		CompletionTokens: response.Usage.OutputTokens,
		TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
	}
	bifrostResponse.Model = response.Model
	bifrostResponse.ExtraFields = interfaces.BifrostResponseExtraFields{
		Provider:    interfaces.Anthropic,
		RawResponse: rawResponse,
	}

	return bifrostResponse, nil
}
