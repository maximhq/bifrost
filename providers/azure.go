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

	for range config.ConcurrencyAndBufferSize.Concurrency {
		// Create and put new objects directly into pools
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

// TextCompletion implements text completion using Anthropic's API
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
	bifrostResponse.Usage = response.Usage
	bifrostResponse.Model = response.Model
	bifrostResponse.Created = response.Created
	bifrostResponse.SystemFingerprint = response.SystemFingerprint
	bifrostResponse.ExtraFields = interfaces.BifrostResponseExtraFields{
		Provider:    interfaces.Azure,
		RawResponse: rawResponse,
	}

	return bifrostResponse, nil
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

	preparedParams := prepareParams(params)

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

	// Use enhanced response handler
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Set response fields
	bifrostResponse.ID = response.ID
	bifrostResponse.Choices = response.Choices
	bifrostResponse.Object = response.Object
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
