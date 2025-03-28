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

type OpenAIToolCall struct {
	Type     *string `json:"type"`
	ID       *string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type OpenAIMessage struct {
	Role      interfaces.ModelChatMessageRole `json:"role"`
	Content   string                          `json:"content"`
	ToolCalls *[]OpenAIToolCall               `json:"tool_calls,omitempty"`
}

type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason *string       `json:"finish_reason"`
	LogProbs     *interface{}  `json:"logprobs"`
}

type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   struct {
		PromptTokens       int `json:"prompt_tokens"`
		CompletionTokens   int `json:"completion_tokens"`
		TotalTokens        int `json:"total_tokens"`
		PromptTokenDetails struct {
			CachedToken int `json:"cached_tokens"`
			AudioToken  int `json:"audio_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokenDetails struct {
			ReasoningTokens          int `json:"reasoning_tokens"`
			AudioTokens              int `json:"audio_tokens"`
			AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
			RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
		} `json:"completion_tokens_details"`
		Latency float64 `json:"latency"`
	} `json:"usage"`
	Model             string      `json:"model"`
	Created           interface{} `json:"created"`
	ServiceTier       string      `json:"service_tier"`
	SystemFingerprint string      `json:"system_fingerprint"`
}

// OpenAIProvider implements the Provider interface for OpenAI
type OpenAIProvider struct {
	client *http.Client
}

// NewOpenAIProvider creates a new OpenAI provider instance
func NewOpenAIProvider(config *interfaces.ProviderConfig) *OpenAIProvider {
	return &OpenAIProvider{
		client: &http.Client{Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)},
	}
}

func (provider *OpenAIProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.OpenAI
}

// TextCompletion performs text completion
func (provider *OpenAIProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
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

func (provider *OpenAIProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, error) {
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

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}

	// Handle error response
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI error: %s", string(body))
	}

	// Decode structured response
	var response OpenAIResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("error decoding structured response: %v", err)
	}

	// Decode raw response
	var rawResponse interface{}
	if err := json.Unmarshal(body, &rawResponse); err != nil {
		return nil, fmt.Errorf("error decoding raw response: %v", err)
	}

	// Transform choices to include tool calls
	var choices []interfaces.BifrostResponseChoice
	for i, c := range response.Choices {
		// Transform tool calls if present
		var toolCalls []interfaces.ToolCall
		if c.Message.ToolCalls != nil {
			for _, tool := range *c.Message.ToolCalls {
				toolCalls = append(toolCalls, interfaces.ToolCall{
					ID:        tool.ID,
					Type:      tool.Type,
					Name:      &tool.Function.Name,
					Arguments: json.RawMessage(tool.Function.Arguments),
				})
			}
		}

		choices = append(choices, interfaces.BifrostResponseChoice{
			Index: i,
			Message: interfaces.BifrostResponseChoiceMessage{
				Role:      c.Message.Role,
				Content:   c.Message.Content,
				ToolCalls: &toolCalls,
			},
			StopReason: c.FinishReason,
			LogProbs:   c.LogProbs,
		})
	}

	result := &interfaces.BifrostResponse{
		ID:      response.ID,
		Choices: choices,
		Usage: interfaces.LLMUsage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
			Latency:          &latency,
		},
		Model:       response.Model,
		Provider:    interfaces.OpenAI,
		RawResponse: rawResponse,
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

	return result, nil
}
