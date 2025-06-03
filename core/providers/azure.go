// Package providers implements various LLM providers and their utility functions.
// This file contains the Azure OpenAI provider implementation.
package providers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/goccy/go-json"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// AzureTextResponse represents the response structure from Azure's text completion API.
// It includes completion choices, model information, and usage statistics.
type AzureTextResponse struct {
	ID      string `json:"id"`     // Unique identifier for the completion
	Object  string `json:"object"` // Type of completion (always "text.completion")
	Choices []struct {
		FinishReason *string                       `json:"finish_reason,omitempty"` // Reason for completion termination
		Index        int                           `json:"index"`                   // Index of the choice
		Text         string                        `json:"text"`                    // Generated text
		LogProbs     schemas.TextCompletionLogProb `json:"logprobs"`                // Log probabilities
	} `json:"choices"`
	Model             string           `json:"model"`              // Model used for the completion
	Created           int              `json:"created"`            // Unix timestamp of completion creation
	SystemFingerprint *string          `json:"system_fingerprint"` // System fingerprint for the request
	Usage             schemas.LLMUsage `json:"usage"`              // Token usage statistics
}

// AzureChatResponse represents the response structure from Azure's chat completion API.
// It includes completion choices, model information, and usage statistics.
type AzureChatResponse struct {
	ID                string                          `json:"id"`                 // Unique identifier for the completion
	Object            string                          `json:"object"`             // Type of completion (always "chat.completion")
	Choices           []schemas.BifrostResponseChoice `json:"choices"`            // Array of completion choices
	Model             string                          `json:"model"`              // Model used for the completion
	Created           int                             `json:"created"`            // Unix timestamp of completion creation
	SystemFingerprint *string                         `json:"system_fingerprint"` // System fingerprint for the request
	Usage             schemas.LLMUsage                `json:"usage"`              // Token usage statistics
}

// AzureError represents the error response structure from Azure's API.
// It includes error code and message information.
type AzureError struct {
	Error struct {
		Code    string `json:"code"`    // Error code
		Message string `json:"message"` // Error message
	} `json:"error"`
}

// azureTextCompletionResponsePool provides a pool for Azure text completion response objects.
var azureTextCompletionResponsePool = sync.Pool{
	New: func() interface{} {
		return &AzureTextResponse{}
	},
}

// azureChatResponsePool provides a pool for Azure chat response objects.
var azureChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &AzureChatResponse{}
	},
}

// acquireAzureChatResponse gets an Azure chat response from the pool and resets it.
func acquireAzureChatResponse() *AzureChatResponse {
	resp := azureChatResponsePool.Get().(*AzureChatResponse)
	*resp = AzureChatResponse{} // Reset the struct
	return resp
}

// releaseAzureChatResponse returns an Azure chat response to the pool.
func releaseAzureChatResponse(resp *AzureChatResponse) {
	if resp != nil {
		azureChatResponsePool.Put(resp)
	}
}

// acquireAzureTextResponse gets an Azure text completion response from the pool and resets it.
func acquireAzureTextResponse() *AzureTextResponse {
	resp := azureTextCompletionResponsePool.Get().(*AzureTextResponse)
	*resp = AzureTextResponse{} // Reset the struct
	return resp
}

// releaseAzureTextResponse returns an Azure text completion response to the pool.
func releaseAzureTextResponse(resp *AzureTextResponse) {
	if resp != nil {
		azureTextCompletionResponsePool.Put(resp)
	}
}

// AzureProvider implements the Provider interface for Azure's OpenAI API.
type AzureProvider struct {
	logger schemas.Logger     // Logger for provider operations
	client *fasthttp.Client   // HTTP client for API requests
	meta   schemas.MetaConfig // Azure-specific configuration
}

// NewAzureProvider creates a new Azure provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewAzureProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*AzureProvider, error) {
	config.CheckAndSetDefaults()

	if config.MetaConfig == nil {
		return nil, fmt.Errorf("meta config is not set")
	}

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		azureChatResponsePool.Put(&AzureChatResponse{})
		azureTextCompletionResponsePool.Put(&AzureTextResponse{})
		bifrostResponsePool.Put(&schemas.BifrostResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	return &AzureProvider{
		logger: logger,
		client: client,
		meta:   config.MetaConfig,
	}, nil
}

// GetProviderKey returns the provider identifier for Azure.
func (provider *AzureProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Azure
}

// completeRequest sends a request to Azure's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AzureProvider) completeRequest(ctx context.Context, requestBody map[string]interface{}, path string, key string, model string) ([]byte, *schemas.BifrostError) {
	// Marshal the request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
	}

	if provider.meta.GetEndpoint() == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
				Message: "endpoint not set",
			},
		}
	}

	url := *provider.meta.GetEndpoint()

	if provider.meta.GetDeployments() != nil {
		deployment := provider.meta.GetDeployments()[model]
		if deployment == "" {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: schemas.ErrorField{
					Message: fmt.Sprintf("deployment if not found for model %s", model),
				},
			}
		}

		apiVersion := provider.meta.GetAPIVersion()
		if apiVersion == nil {
			apiVersion = StrPtr("2024-02-01")
		}

		url = fmt.Sprintf("%s/openai/deployments/%s/%s?api-version=%s", url, deployment, path, *apiVersion)
	} else {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: schemas.ErrorField{
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
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from azure provider: %s", string(resp.Body())))

		var errorResp AzureError

		bifrostErr := handleProviderAPIError(resp, &errorResp)
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
func (provider *AzureProvider) TextCompletion(ctx context.Context, model, key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	preparedParams := prepareParams(params)

	// Merge additional parameters
	requestBody := mergeConfig(map[string]interface{}{
		"model":  model,
		"prompt": text,
	}, preparedParams)

	responseBody, err := provider.completeRequest(ctx, requestBody, "completions", key, model)
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

	choices := []schemas.BifrostResponseChoice{}

	// Create the completion result
	if len(response.Choices) > 0 {
		// Create a copy of the text to avoid dangling pointer to pooled object
		textCopy := response.Choices[0].Text

		choices = append(choices, schemas.BifrostResponseChoice{
			Index: 0,
			Message: schemas.BifrostMessage{
				Role:    schemas.ModelChatMessageRoleAssistant,
				Content: &textCopy,
			},
			FinishReason: response.Choices[0].FinishReason,
			LogProbs: &schemas.LogProbs{
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
	bifrostResponse.ExtraFields = schemas.BifrostResponseExtraFields{
		Provider:    schemas.Azure,
		RawResponse: rawResponse,
	}

	return bifrostResponse, nil
}

// ChatCompletion performs a chat completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) ChatCompletion(ctx context.Context, model, key string, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	preparedParams := prepareParams(params)

	// Format messages for Azure API
	var formattedMessages []map[string]interface{}
	for _, msg := range messages {
		message := map[string]interface{}{
			"role": msg.Role,
		}

		// Only add content if it's not nil
		if msg.Content != nil {
			message["content"] = *msg.Content
		}

		formattedMessages = append(formattedMessages, message)
	}

	// Merge additional parameters
	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	responseBody, err := provider.completeRequest(ctx, requestBody, "chat/completions", key, model)
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
	bifrostResponse.ExtraFields = schemas.BifrostResponseExtraFields{
		Provider:    schemas.Azure,
		RawResponse: rawResponse,
	}

	return bifrostResponse, nil
}
