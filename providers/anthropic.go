package providers

import (
	"bifrost/interfaces"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
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
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Thinking string `json:"thinking,omitempty"`
		ToolUse  *struct {
			ID    string                 `json:"id"`
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		} `json:"tool_use,omitempty"`
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
func NewAnthropicProvider() *AnthropicProvider {
	return &AnthropicProvider{
		// @comment let us have this be controllable
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (provider *AnthropicProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.Anthropic
}

// TextCompletion implements text completion using Anthropic's API
func (provider *AnthropicProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.CompletionResult, error) {
	preparedParams := PrepareParams(params)

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

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}

	// Check for error response
	if resp.StatusCode != http.StatusOK {
		var errorResp struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &errorResp); err != nil {
			return nil, fmt.Errorf("error response: %s", string(body))
		}
		return nil, fmt.Errorf("anthropic error: %s", errorResp.Error.Message)
	}

	// Parse the response
	var response AnthropicTextResponse

	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("error parsing response: %v", err)
	}

	// Create the completion result
	completionResult := &interfaces.CompletionResult{
		ID: response.ID,
		Choices: []interfaces.CompletionResultChoice{
			{
				Index: 0,
				Message: interfaces.CompletionResponseChoice{
					Role:    interfaces.RoleAssistant,
					Content: response.Completion,
				},
			},
		},
		Usage: interfaces.LLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
		Model:    response.Model,
		Provider: interfaces.Anthropic,
	}

	return completionResult, nil
}

// ChatCompletion implements chat completion using Anthropic's API
func (provider *AnthropicProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.CompletionResult, error) {
	startTime := time.Now()

	// Format messages for Anthropic API
	var formattedMessages []map[string]interface{}
	for _, msg := range messages {
		formattedMessages = append(formattedMessages, map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	preparedParams := PrepareParams(params)

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

	// Calculate latency
	latency := time.Since(startTime).Seconds()

	// Decode response
	var anthropicResponse AnthropicChatResponse

	if err := json.Unmarshal(body, &anthropicResponse); err != nil {
		return nil, fmt.Errorf("error decoding response: %v", err)
	}

	// Process the response into our CompletionResult format
	var content string
	var toolCalls []interfaces.ToolCall
	var finishReason string

	// Process content and tool calls
	for _, c := range anthropicResponse.Content {
		switch c.Type {
		case "thinking":
			if content == "" {
				content = fmt.Sprintf("<think>\n%s\n</think>\n\n", c.Thinking)
			}
		case "text":
			content += c.Text
		case "tool_use":
			if c.ToolUse != nil {
				toolCalls = append(toolCalls, interfaces.ToolCall{
					Type: "function",
					ID:   c.ToolUse.ID,
					Function: interfaces.FunctionCall{
						Name:      c.ToolUse.Name,
						Arguments: string(must(json.Marshal(c.ToolUse.Input))),
					},
				})
				finishReason = "tool_calls"
			}
		}
	}

	// Create the completion result
	result := &interfaces.CompletionResult{
		ID: anthropicResponse.ID,
		Choices: []interfaces.CompletionResultChoice{
			{
				Index: 0,
				Message: interfaces.CompletionResponseChoice{
					Role:      interfaces.RoleAssistant,
					Content:   content,
					ToolCalls: &toolCalls,
				},
				FinishReason: &finishReason,
			},
		},
		Usage: interfaces.LLMUsage{
			PromptTokens:     anthropicResponse.Usage.InputTokens,
			CompletionTokens: anthropicResponse.Usage.OutputTokens,
			TotalTokens:      anthropicResponse.Usage.InputTokens + anthropicResponse.Usage.OutputTokens,
			Latency:          &latency,
		},
		Model:    anthropicResponse.Model,
		Provider: interfaces.Anthropic,
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
