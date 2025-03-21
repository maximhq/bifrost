package providers

import (
	"bifrost/interfaces"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type OpenAIResponse struct {
	ID      string                              `json:"id"`
	Choices []interfaces.CompletionResultChoice `json:"choices"`
	Usage   interfaces.LLMUsage                 `json:"usage"`
	Model   string                              `json:"model"`
	Created interface{}                         `json:"created"`
}

// OpenAIProvider implements the Provider interface for OpenAI
type OpenAIProvider struct {
	client *http.Client
}

// NewOpenAIProvider creates a new OpenAI provider instance
func NewOpenAIProvider() *OpenAIProvider {
	return &OpenAIProvider{
		client: &http.Client{Timeout: time.Second * 30},
	}
}

func (provider *OpenAIProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.OpenAI
}

// TextCompletion performs text completion
func (provider *OpenAIProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.CompletionResult, error) {
	return nil, fmt.Errorf("text completion is not supported by OpenAI")
}

// sanitizeParameters cleans up the parameters for OpenAI
func (provider *OpenAIProvider) sanitizeParameters(params *interfaces.ModelParameters) *interfaces.ModelParameters {
	sanitized := params
	if sanitized == nil {
		return nil
	}

	if params.ExtraParams != nil {
		// For logprobs, if it's disabled, we remove top_logprobs
		if _, exists := params.ExtraParams["logprobs"]; !exists {
			delete(sanitized.ExtraParams, "top_logprobs")
		}
	}

	return sanitized
}

// ChatCompletion implements chat completion using OpenAI's API
func (provider *OpenAIProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.CompletionResult, error) {
	startTime := time.Now()

	// Format messages for OpenAI API
	var openAIMessages []map[string]interface{}
	for _, msg := range messages {

		var content any
		if msg.Content != nil {
			content = msg.Content
		} else {
			content = msg.ImageContent
		}

		openAIMessages = append(openAIMessages, map[string]interface{}{
			"role":    msg.Role,
			"content": content,
		})
	}

	// Sanitize parameters
	params = provider.sanitizeParameters(params)
	preparedParams := PrepareParams(params)

	requestBody := MergeConfig(map[string]interface{}{
		"model":    model,
		"messages": openAIMessages,
	}, preparedParams)

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %v", err)
	}

	// Create request
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	// Add headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	// Make request
	resp, err := provider.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	latency := time.Since(startTime).Seconds()

	// Handle error response
	if resp.StatusCode != http.StatusOK {
		var errorResp struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Param   any    `json:"param"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errorResp); err != nil {
			return nil, fmt.Errorf("error decoding error response: %v", err)
		}
		return nil, fmt.Errorf("OpenAI error: %s", errorResp.Error.Message)
	}

	// Decode response
	var response OpenAIResponse

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("error decoding response: %v", err)
	}

	// Convert the raw result to CompletionResult
	result := &interfaces.CompletionResult{
		ID:      response.ID,
		Choices: response.Choices,
		Usage:   response.Usage,
		Model:   response.Model,
	}

	// Handle the created field conversion
	if response.Created != nil {
		switch v := response.Created.(type) {
		case float64:
			// Convert Unix timestamp to string
			result.Created = fmt.Sprintf("%d", int64(v))
		case string:
			result.Created = v
		}
	}

	// Add provider-specific information
	result.Provider = interfaces.OpenAI
	result.Usage.Latency = &latency

	return result, nil
}
