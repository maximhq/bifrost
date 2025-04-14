// Package providers implements various LLM providers and their utility functions.
// This file contains the Azure OpenAI provider implementation.
package providers

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/interfaces"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/maxim-go"
)

// AzureTextResponse represents the response structure from Azure's text completion API.
// It includes completion choices, model information, and usage statistics.
type AzureTextResponse struct {
	ID      string `json:"id"`     // Unique identifier for the completion
	Object  string `json:"object"` // Type of completion (always "text.completion")
	Choices []struct {
		FinishReason *string                          `json:"finish_reason,omitempty"` // Reason for completion termination
		Index        int                              `json:"index"`                   // Index of the choice
		Text         string                           `json:"text"`                    // Generated text
		LogProbs     interfaces.TextCompletionLogProb `json:"logprobs"`                // Log probabilities
	} `json:"choices"`
	Model             string              `json:"model"`              // Model used for the completion
	Created           int                 `json:"created"`            // Unix timestamp of completion creation
	SystemFingerprint *string             `json:"system_fingerprint"` // System fingerprint for the request
	Usage             interfaces.LLMUsage `json:"usage"`              // Token usage statistics
}

// AzureChatResponse represents the response structure from Azure's chat completion API.
// It includes completion choices, model information, and usage statistics.
type AzureChatResponse struct {
	ID                string                             `json:"id"`                 // Unique identifier for the completion
	Object            string                             `json:"object"`             // Type of completion (always "chat.completion")
	Choices           []interfaces.BifrostResponseChoice `json:"choices"`            // Array of completion choices
	Model             string                             `json:"model"`              // Model used for the completion
	Created           int                                `json:"created"`            // Unix timestamp of completion creation
	SystemFingerprint *string                            `json:"system_fingerprint"` // System fingerprint for the request
	Usage             interfaces.LLMUsage                `json:"usage"`              // Token usage statistics
}

// AzureError represents the error response structure from Azure's API.
// It includes error code and message information.
type AzureError struct {
	Error struct {
		Code    string `json:"code"`    // Error code
		Message string `json:"message"` // Error message
	} `json:"error"`
}

// AzureProvider implements the Provider interface for Azure's OpenAI API.
type AzureProvider struct {
	logger interfaces.Logger     // Logger for provider operations
	client *fasthttp.Client      // HTTP client for API requests
	meta   interfaces.MetaConfig // Azure-specific configuration
}

// NewAzureProvider creates a new Azure provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewAzureProvider(config *interfaces.ProviderConfig, logger interfaces.Logger) *AzureProvider {
	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		azureChatResponsePool.Put(&AzureChatResponse{})
		azureTextCompletionResponsePool.Put(&AzureTextResponse{})
		bifrostResponsePool.Put(&interfaces.BifrostResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	return &AzureProvider{
		logger: logger,
		client: client,
		meta:   config.MetaConfig,
	}
}

// GetProviderKey returns the provider identifier for Azure.
func (provider *AzureProvider) GetProviderKey() interfaces.SupportedModelProvider {
	return interfaces.Azure
}

// PrepareToolChoices prepares tool choice parameters for Azure's API.
// It handles conversion of tool choice parameters to the format expected by Azure.
// Returns the modified parameters map.
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

// CompleteRequest sends a request to Azure's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AzureProvider) CompleteRequest(requestBody map[string]interface{}, path string, key string, model string) ([]byte, *interfaces.BifrostError) {
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
			IsBifrostError: false,
			Error: interfaces.ErrorField{
				Message: interfaces.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		var errorResp AzureError

		bifrostErr := handleProviderAPIError(resp, errorResp)
		bifrostErr.Error.Type = &errorResp.Error.Code
		bifrostErr.Error.Message = errorResp.Error.Message

		return nil, bifrostErr
	}

	// Read the response body
	body := resp.Body()

	return body, nil
}

// TextCompletion performs a text completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) TextCompletion(model, key, text string, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	preparedParams := prepareParams(params)

	// Merge additional parameters
	requestBody := mergeConfig(map[string]interface{}{
		"model":  model,
		"prompt": text,
	}, preparedParams)

	responseBody, err := provider.CompleteRequest(requestBody, "completions", key, model)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireAzureTextResponse()
	defer releaseAzureTextResponse(response)

	// Create Bifrost response from pool
	bifrostResponse := acquireBifrostResponse()
	defer releaseBifrostResponse(bifrostResponse)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
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

	bifrostResponse.ID = response.ID
	bifrostResponse.Choices = choices
	bifrostResponse.Model = response.Model
	bifrostResponse.Created = response.Created
	bifrostResponse.SystemFingerprint = response.SystemFingerprint
	bifrostResponse.Usage = response.Usage
	bifrostResponse.ExtraFields = interfaces.BifrostResponseExtraFields{
		Provider:    interfaces.Azure,
		RawResponse: rawResponse,
	}

	return bifrostResponse, nil
}

// ChatCompletion performs a chat completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) ChatCompletion(model, key string, messages []interfaces.Message, params *interfaces.ModelParameters) (*interfaces.BifrostResponse, *interfaces.BifrostError) {
	preparedParams := prepareParams(params)

	// Format messages for Azure API
	var formattedMessages []map[string]interface{}
	for _, msg := range messages {
		formattedMessages = append(formattedMessages, map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// Merge additional parameters
	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	responseBody, err := provider.CompleteRequest(requestBody, "chat/completions", key, model)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireAzureChatResponse()
	defer releaseAzureChatResponse(response)

	// Create Bifrost response from pool
	bifrostResponse := acquireBifrostResponse()
	defer releaseBifrostResponse(bifrostResponse)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse.ID = response.ID
	bifrostResponse.Choices = response.Choices
	bifrostResponse.Model = response.Model
	bifrostResponse.Created = response.Created
	bifrostResponse.SystemFingerprint = response.SystemFingerprint
	bifrostResponse.Usage = response.Usage
	bifrostResponse.ExtraFields = interfaces.BifrostResponseExtraFields{
		Provider:    interfaces.Azure,
		RawResponse: rawResponse,
	}

	return bifrostResponse, nil
}
