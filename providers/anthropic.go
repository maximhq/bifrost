package providers

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/maxim-go"
)

type AnthropicToolChoice struct {
	Type                   interfaces.ToolChoiceType `json:"type"`
	Name                   *string                   `json:"name"`
	DisableParallelToolUse *bool                     `json:"disable_parallel_tool_use"`
}

type AnthropicTextResponse struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Completion string `json:"completion"`
	Model      string `json:"model"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type AnthropicChatResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type     string                 `json:"type"`
		Text     string                 `json:"text,omitempty"`
		Thinking string                 `json:"thinking,omitempty"`
		ID       string                 `json:"id"`
		Name     string                 `json:"name"`
		Input    map[string]interface{} `json:"input"`
	} `json:"content"`
	Model        string  `json:"model"`
	StopReason   string  `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type AnthropicError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// AnthropicProvider implements the Provider interface for Anthropic's Claude API
type AnthropicProvider struct {
	logger interfaces.Logger
	client *fasthttp.Client
}

// NewAnthropicProvider creates a new AnthropicProvider instance
func NewAnthropicProvider(config *interfaces.ProviderConfig, logger interfaces.Logger) *AnthropicProvider {
	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	return &AnthropicProvider{
		logger: logger,
		client: client,
	}
}

func (provider *AnthropicProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.Anthropic
}

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

func (provider *AnthropicProvider) CompleteRequest(requestBody map[string]interface{}, url string, key string) ([]byte, *interfaces.BifrostError) {
	// Marshal the request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "error marshaling request",
				Error:   err,
			},
		}
	}

	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("https://api.anthropic.com/v1/complete")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.SetBody(jsonData)

	// Send the request
	if err := provider.client.Do(req, resp); err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "error sending request",
				Error:   err,
			},
		}
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		var errorResp AnthropicError
		if err := json.Unmarshal(resp.Body(), &errorResp); err != nil {
			return nil, &interfaces.BifrostError{
				IsBifrostError: true,
				Error: interfaces.ErrorField{
					Message: "error parsing error response",
					Error:   err,
				},
			}
		}

		statusCode := resp.StatusCode()

		return nil, &interfaces.BifrostError{
			Type:           &errorResp.Type,
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: interfaces.ErrorField{
				Type:    &errorResp.Error.Type,
				Message: errorResp.Error.Message,
			},
		}
	}

	// Read the response body
	body := resp.Body()

	return body, nil
}

// TextCompletion implements text completion using Anthropic's API
func (provider *AnthropicProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	preparedParams := provider.PrepareTextCompletionParams(PrepareParams(params))

	// Merge additional parameters
	requestBody := MergeConfig(map[string]interface{}{
		"model":  model,
		"prompt": fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", text),
	}, preparedParams)

	body, err := provider.CompleteRequest(requestBody, "https://api.anthropic.com/v1/complete", key)
	if err != nil {
		return nil, err
	}

	// Parse the response
	var response AnthropicTextResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "error parsing response",
				Error:   err,
			},
		}
	}

	// Parse raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "error parsing raw response",
				Error:   err,
			},
		}
	}

	// Create the completion result
	completionResult := &interfaces.BifrostResponse{
		ID: response.ID,
		Choices: []interfaces.BifrostResponseChoice{
			{
				Index: 0,
				Message: interfaces.BifrostResponseChoiceMessage{
					Role:    interfaces.RoleAssistant,
					Content: &response.Completion,
				},
			},
		},
		Usage: interfaces.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
		Model: response.Model,
		ExtraFields: interfaces.BifrostResponseExtraFields{
			Provider:    interfaces.Anthropic,
			RawResponse: rawResponse,
		},
	}

	return completionResult, nil
}

// ChatCompletion implements chat completion using Anthropic's API
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

	preparedParams := PrepareParams(params)

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
	requestBody := MergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	body, err := provider.CompleteRequest(requestBody, "https://api.anthropic.com/v1/messages", key)
	if err != nil {
		return nil, err
	}

	// Decode response
	var response AnthropicChatResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "error decoding response",
				Error:   err,
			},
		}
	}

	// Decode raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: true,
			Error: interfaces.ErrorField{
				Message: "error parsing raw response",
				Error:   err,
			},
		}
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

	// Create the completion result
	result := &interfaces.BifrostResponse{
		ID:      response.ID,
		Choices: choices,
		Usage: interfaces.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
		Model: response.Model,
		ExtraFields: interfaces.BifrostResponseExtraFields{
			Provider:    interfaces.Anthropic,
			RawResponse: rawResponse,
		},
	}

	return result, nil
}
