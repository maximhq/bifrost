// Package providers implements various LLM providers and their utility functions.
// This file contains the Azure OpenAI provider implementation.
package providers

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bytedance/sonic"
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

// AzureError represents the error response structure from Azure's API.
// It includes error code and message information.
type AzureError struct {
	Error struct {
		Code    string `json:"code"`    // Error code
		Message string `json:"message"` // Error message
	} `json:"error"`
}

// AzureAuthorizationTokenKey is the context key for the Azure authentication token.
const AzureAuthorizationTokenKey ContextKey = "azure-authorization-token"

// azureTextCompletionResponsePool provides a pool for Azure text completion response objects.
var azureTextCompletionResponsePool = sync.Pool{
	New: func() interface{} {
		return &AzureTextResponse{}
	},
}

// // azureChatResponsePool provides a pool for Azure chat response objects.
// var azureChatResponsePool = sync.Pool{
// 	New: func() interface{} {
// 		return &schemas.BifrostResponse{}
// 	},
// }

// // acquireAzureChatResponse gets an Azure chat response from the pool and resets it.
// func acquireAzureChatResponse() *schemas.BifrostResponse {
// 	resp := azureChatResponsePool.Get().(*schemas.BifrostResponse)
// 	*resp = schemas.BifrostResponse{} // Reset the struct
// 	return resp
// }

// // releaseAzureChatResponse returns an Azure chat response to the pool.
// func releaseAzureChatResponse(resp *schemas.BifrostResponse) {
// 	if resp != nil {
// 		azureChatResponsePool.Put(resp)
// 	}
// }

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
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	streamClient        *http.Client          // HTTP client for streaming requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewAzureProvider creates a new Azure provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewAzureProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*AzureProvider, error) {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.Concurrency,
	}

	// Initialize streaming HTTP client
	streamClient := &http.Client{
		Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
	}

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		// azureChatResponsePool.Put(&schemas.BifrostResponse{})
		azureTextCompletionResponsePool.Put(&AzureTextResponse{})

	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	return &AzureProvider{
		logger:              logger,
		client:              client,
		streamClient:        streamClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Azure.
func (provider *AzureProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Azure
}

// completeRequest sends a request to Azure's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AzureProvider) completeRequest(ctx context.Context, requestBody map[string]interface{}, path string, key schemas.Key, model string) ([]byte, *schemas.BifrostError) {
	if key.AzureKeyConfig == nil {
		return nil, newConfigurationError("azure key config not set", schemas.Azure)
	}

	// Marshal the request body
	jsonData, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Azure)
	}

	if key.AzureKeyConfig.Endpoint == "" {
		return nil, newConfigurationError("endpoint not set", schemas.Azure)
	}

	url := key.AzureKeyConfig.Endpoint

	if key.AzureKeyConfig.Deployments != nil {
		deployment := key.AzureKeyConfig.Deployments[model]
		if deployment == "" {
			return nil, newConfigurationError(fmt.Sprintf("deployment not found for model %s", model), schemas.Azure)
		}

		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = Ptr("2024-02-01")
		}

		url = fmt.Sprintf("%s/openai/deployments/%s/%s?api-version=%s", url, deployment, path, *apiVersion)
	} else {
		return nil, newConfigurationError("deployments not set", schemas.Azure)
	}

	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
		// Ensure api-key is not accidentally present (from extra headers, etc.)
		req.Header.Del("api-key")
	} else {
		req.Header.Set("api-key", key.Value)
	}

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
func (provider *AzureProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
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

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	choices := []schemas.BifrostResponseChoice{}

	// Create the completion result
	if len(response.Choices) > 0 {
		choices = append(choices, schemas.BifrostResponseChoice{
			Index: 0,
			BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
				Message: schemas.BifrostMessage{
					Role: schemas.ModelChatMessageRoleAssistant,
					Content: schemas.MessageContent{
						ContentStr: &response.Choices[0].Text,
					},
				},
				LogProbs: &schemas.LogProbs{
					Text: response.Choices[0].LogProbs,
				},
			},
			FinishReason: response.Choices[0].FinishReason,
		})
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{
		ID:                response.ID,
		Choices:           choices,
		Model:             response.Model,
		Created:           response.Created,
		SystemFingerprint: response.SystemFingerprint,
		Usage:             &response.Usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Azure,
		},
	}

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// ChatCompletion performs a chat completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

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
	// response := acquireAzureChatResponse()
	// defer releaseAzureChatResponse(response)

	response := &schemas.BifrostResponse{}

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = schemas.Azure

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		response.ExtraFields.Params = *params
	}

	return response, nil
}

// Embedding generates embeddings for the given input text(s) using Azure OpenAI.
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *AzureProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Prepare request body - Azure uses deployment-scoped URLs, so model is not needed in body
	requestBody := prepareOpenAIEmbeddingRequest(input, params)

	responseBody, err := provider.completeRequest(ctx, requestBody, "embeddings", key, model)
	if err != nil {
		return nil, err
	}

	// Pre-allocate response structs from pools
	// response := acquireAzureChatResponse()
	// defer releaseAzureChatResponse(response)

	response := &schemas.BifrostResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = schemas.Azure

	if params != nil {
		response.ExtraFields.Params = *params
	}

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ChatCompletionStream performs a streaming chat completion request to Azure's OpenAI API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Azure-specific URL construction with deployments and supports both api-key and Bearer token authentication.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *AzureProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	if key.AzureKeyConfig == nil {
		return nil, newConfigurationError("azure key config not set", schemas.Azure)
	}

	// Merge additional parameters and set stream to true
	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
		"stream":   true,
	}, preparedParams)

	// Construct Azure-specific URL with deployment
	if key.AzureKeyConfig.Endpoint == "" {
		return nil, newConfigurationError("endpoint not set", schemas.Azure)
	}

	baseURL := key.AzureKeyConfig.Endpoint
	var fullURL string

	if key.AzureKeyConfig.Deployments != nil {
		deployment := key.AzureKeyConfig.Deployments[model]
		if deployment == "" {
			return nil, newConfigurationError(fmt.Sprintf("deployment not found for model %s", model), schemas.Azure)
		}

		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = Ptr("2024-02-01")
		}

		fullURL = fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", baseURL, deployment, *apiVersion)
	} else {
		return nil, newConfigurationError("deployments not set", schemas.Azure)
	}

	// Prepare Azure-specific headers
	headers := make(map[string]string)
	headers["Content-Type"] = "application/json"
	headers["Accept"] = "text/event-stream"
	headers["Cache-Control"] = "no-cache"

	// Set Azure authentication - either Bearer token or api-key
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		headers["Authorization"] = fmt.Sprintf("Bearer %s", authToken)
	} else {
		headers["api-key"] = key.Value
	}

	// Use shared streaming logic from OpenAI
	return handleOpenAIStreaming(
		ctx,
		provider.streamClient,
		fullURL,
		requestBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		schemas.Azure, // Provider type
		params,
		postHookRunner,
		provider.logger,
	)
}

func (provider *AzureProvider) Speech(ctx context.Context, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "azure")
}

func (provider *AzureProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "azure")
}

func (provider *AzureProvider) Transcription(ctx context.Context, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "azure")
}

func (provider *AzureProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "azure")
}
