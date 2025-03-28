package providers

import (
	"bifrost/interfaces"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/maximhq/maxim-go"
)

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

// AnthropicProvider implements the Provider interface for Anthropic's Claude API
type AnthropicProvider struct {
	client *http.Client
}

// NewAnthropicProvider creates a new AnthropicProvider instance
func NewAnthropicProvider(config *interfaces.ProviderConfig) *AnthropicProvider {
	return &AnthropicProvider{
		client: &http.Client{Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)},
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

// TextCompletion implements text completion using Anthropic's API
func (provider *AnthropicProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
	startTime := time.Now()

	preparedParams := provider.PrepareTextCompletionParams(PrepareParams(params))

	// Merge additional parameters
	requestBody := MergeConfig(map[string]interface{}{
		"model":  model,
		"prompt": fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", text),
	}, preparedParams)

	// Marshal the request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %v", err)
	}

	// Create the request with the JSON body
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/complete", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	// Send the request
	resp, err := provider.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	// Calculate latency
	latency := time.Since(startTime).Seconds()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}

	// Check for error response
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic error: %s", string(body))
	}

	// Parse the response
	var response AnthropicTextResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("error parsing response: %v", err)
	}

	// Parse raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, fmt.Errorf("error parsing raw response: %v", err)
	}

	// Create the completion result
	completionResult := &interfaces.BifrostResponse{
		ID: response.ID,
		Choices: []interfaces.BifrostResponseChoice{
			{
				Index: 0,
				Message: interfaces.BifrostResponseChoiceMessage{
					Role:    interfaces.RoleAssistant,
					Content: response.Completion,
				},
			},
		},
		Usage: interfaces.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
			Latency:          &latency,
		},
		Model:       response.Model,
		Provider:    interfaces.Anthropic,
		RawResponse: rawResponse,
	}

	return completionResult, nil
}

// ChatCompletion implements chat completion using Anthropic's API
func (provider *AnthropicProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
	// Format messages for Anthropic API
	var formattedMessages []map[string]interface{}
	for _, msg := range messages {
		formattedMessages = append(formattedMessages, map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		})
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

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %v", err)
	}

	// Create request
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	// Send request
	resp, err := provider.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}

	// Check for non-200 status codes
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %s", string(body))
	}

	// Decode response
	var anthropicResponse AnthropicChatResponse
	if err := json.Unmarshal(body, &anthropicResponse); err != nil {
		return nil, fmt.Errorf("error decoding response: %v", err)
	}

	// Parse raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, fmt.Errorf("error parsing raw response: %v", err)
	}

	// Process the response into our BifrostResponse format
	var choices []interfaces.BifrostResponseChoice

	// Process content and tool calls
	for i, c := range anthropicResponse.Content {
		var content string
		var toolCalls []interfaces.ToolCall

		switch c.Type {
		case "thinking":
			content = c.Thinking
		case "text":
			content = c.Text
		case "tool_use":
			toolCalls = append(toolCalls, interfaces.ToolCall{
				Type:      maxim.StrPtr("function"),
				ID:        &c.ID,
				Name:      &c.Name,
				Arguments: json.RawMessage(must(json.Marshal(c.Input))),
			})
		}

		choices = append(choices, interfaces.BifrostResponseChoice{
			Index: i,
			Message: interfaces.BifrostResponseChoiceMessage{
				Role:      interfaces.RoleAssistant,
				Content:   content,
				ToolCalls: &toolCalls,
			},
			StopReason: &anthropicResponse.StopReason,
			Stop:       anthropicResponse.StopSequence,
		})
	}

	// Create the completion result
	result := &interfaces.BifrostResponse{
		ID:      anthropicResponse.ID,
		Choices: choices,
		Usage: interfaces.LLMUsage{
			PromptTokens:     anthropicResponse.Usage.InputTokens,
			CompletionTokens: anthropicResponse.Usage.OutputTokens,
			TotalTokens:      anthropicResponse.Usage.InputTokens + anthropicResponse.Usage.OutputTokens,
		},
		Model:       anthropicResponse.Model,
		Provider:    interfaces.Anthropic,
		RawResponse: rawResponse,
	}

	return result, nil
}

// Helper function to handle JSON marshaling errors
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
