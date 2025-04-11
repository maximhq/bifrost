package providers

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/maxim-go"
)

type AzureTextResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"` // text.completion or chat.completion
	Choices []struct {
		FinishReason *string                          `json:"finish_reason,omitempty"`
		Index        int                              `json:"index"`
		Text         string                           `json:"text"`
		LogProbs     interfaces.TextCompletionLogProb `json:"logprobs"`
	} `json:"choices"`
	Model             string              `json:"model"`
	Created           int                 `json:"created"` // The Unix timestamp (in seconds).
	SystemFingerprint *string             `json:"system_fingerprint"`
	Usage             interfaces.LLMUsage `json:"usage"`
}

type AzureChatResponse struct {
	ID                string                             `json:"id"`
	Object            string                             `json:"object"` // text.completion or chat.completion
	Choices           []interfaces.BifrostResponseChoice `json:"choices"`
	Model             string                             `json:"model"`
	Created           int                                `json:"created"` // The Unix timestamp (in seconds).
	SystemFingerprint *string                            `json:"system_fingerprint"`
	Usage             interfaces.LLMUsage                `json:"usage"`
}

type AzureError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// AzureProvider implements the Provider interface for Azure API
type AzureProvider struct {
	logger interfaces.Logger
	client *fasthttp.Client
	meta   interfaces.MetaConfig
}

// NewAzureProvider creates a new AzureProvider instance
func NewAzureProvider(config *interfaces.ProviderConfig, logger interfaces.Logger) *AzureProvider {
	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	return &AzureProvider{
		logger: logger,
		client: client,
		meta:   config.MetaConfig,
	}
}

func (provider *AzureProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.Azure
}

func (provider *AzureProvider) PrepareToolChoices(params map[string]interface{}) map[string]interface{} {
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

func (provider *AzureProvider) CompleteRequest(requestBody map[string]interface{}, path string, key string, model string) ([]byte, *interfaces.BifrostError) {
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

	if provider.meta.GetEndpoint() == nil {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: "endpoint not set",
			},
		}
	}

	url := *provider.meta.GetEndpoint()

	if provider.meta.GetDeployments() != nil {
		deployment := provider.meta.GetDeployments()[model]
		if deployment == "" {
			return nil, &interfaces.BifrostError{
				IsBifrostError: false,
				Error: interfaces.ErrorField{
					Message: fmt.Sprintf("deployment if not found for model %s", model),
				},
			}
		}

		apiVersion := provider.meta.GetAPIVersion()
		if apiVersion == nil {
			apiVersion = maxim.StrPtr("2024-02-01")
		}

		url = fmt.Sprintf("%s/openai/deployments/%s/%s?api-version=%s", url, deployment, path, *apiVersion)
	} else {
		return nil, &interfaces.BifrostError{
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: "deployments not set",
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
	req.Header.Set("api-key", key)
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
		var errorResp AzureError
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
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: interfaces.ErrorField{
				Type:    &errorResp.Error.Code,
				Message: errorResp.Error.Message,
			},
		}
	}

	// Read the response body
	body := resp.Body()

	return body, nil
}

// TextCompletion implements text completion using Anthropic's API
func (provider *AzureProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	preparedParams := PrepareParams(params)

	// Merge additional parameters
	requestBody := MergeConfig(map[string]interface{}{
		"model":  model,
		"prompt": text,
	}, preparedParams)

	body, err := provider.CompleteRequest(requestBody, "completions", key, model)
	if err != nil {
		return nil, err
	}

	// Parse the response
	var response AzureTextResponse
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

	choices := []interfaces.BifrostResponseChoice{}

	// Create the completion result
	if len(response.Choices) > 0 {
		choices = append(choices, interfaces.BifrostResponseChoice{
			Index: 0,
			Message: interfaces.BifrostResponseChoiceMessage{
				Role:    interfaces.RoleAssistant,
				Content: &response.Choices[0].Text,
			},
			FinishReason: response.Choices[0].FinishReason,
			LogProbs: &interfaces.LogProbs{
				Text: response.Choices[0].LogProbs,
			},
		})
	}

	completionResult := &interfaces.BifrostResponse{
		ID:      response.ID,
		Choices: choices,
		Usage:   response.Usage,
		Model:   response.Model,
		ExtraFields: interfaces.BifrostResponseExtraFields{
			Provider:    interfaces.Azure,
			RawResponse: rawResponse,
		},
	}

	return completionResult, nil
}

// ChatCompletion implements chat completion using Azure's API
func (provider *AzureProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	// Format messages for Azure API
	var formattedMessages []map[string]interface{}
	for _, msg := range messages {
		if msg.ImageContent != nil {
			var content []map[string]interface{}

			imageContent := map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]interface{}{"url": msg.ImageContent.URL},
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

	// Merge additional parameters
	requestBody := MergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	body, err := provider.CompleteRequest(requestBody, "chat/completions", key, model)
	if err != nil {
		return nil, err
	}

	// Decode response
	var response AzureChatResponse
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

	// Create the completion result
	result := &interfaces.BifrostResponse{
		ID:                response.ID,
		Object:            response.Object,
		Choices:           response.Choices,
		Model:             response.Model,
		Created:           response.Created,
		SystemFingerprint: response.SystemFingerprint,
		Usage:             response.Usage,
		ExtraFields: interfaces.BifrostResponseExtraFields{
			Provider:    interfaces.Azure,
			RawResponse: rawResponse,
		},
	}

	return result, nil
}
