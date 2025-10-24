// Package providers implements various LLM providers and their utility functions.
// This file contains the Azure OpenAI provider implementation.
package providers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/azure"
	"github.com/maximhq/bifrost/core/schemas/providers/openai"
	"github.com/valyala/fasthttp"
)

// AzureAuthorizationTokenKey is the context key for the Azure authentication token.
const AzureAuthorizationTokenKey schemas.BifrostContextKey = "azure-authorization-token"

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
// Returns the response body, request latency, or an error if the request fails.
// Parameters:
//   - method: HTTP method (GET, POST, etc.)
//   - requestBody: request body (can be nil for GET requests)
//   - path: API path (e.g., "chat/completions", "models")
//   - key: Azure authentication key
//   - model: model name (can be empty for non-deployment endpoints)
//   - useDeployment: whether to use deployment-based URL construction
func (provider *AzureProvider) completeRequest(ctx context.Context, method string, requestBody interface{}, path string, key schemas.Key, model string, useDeployment bool) ([]byte, time.Duration, *schemas.BifrostError) {
	if key.AzureKeyConfig == nil {
		return nil, 0, newConfigurationError("azure key config not set", schemas.Azure)
	}

	if key.AzureKeyConfig.Endpoint == "" {
		return nil, 0, newConfigurationError("endpoint not set", schemas.Azure)
	}

	var jsonData []byte
	var err error

	// Marshal the request body if provided
	if requestBody != nil {
		jsonData, err = sonic.Marshal(requestBody)
		if err != nil {
			return nil, 0, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Azure)
		}
	}

	url := key.AzureKeyConfig.Endpoint
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(azure.DefaultAzureAPIVersion)
	}

	// Construct URL based on whether deployment is required
	if useDeployment {
		if key.AzureKeyConfig.Deployments == nil {
			return nil, 0, newConfigurationError("deployments not set", schemas.Azure)
		}

		deployment := key.AzureKeyConfig.Deployments[model]
		if deployment == "" {
			return nil, 0, newConfigurationError(fmt.Sprintf("deployment not found for model %s", model), schemas.Azure)
		}

		url = fmt.Sprintf("%s/openai/deployments/%s/%s?api-version=%s", url, deployment, path, *apiVersion)
	} else {
		// For non-deployment endpoints (like list models)
		url = fmt.Sprintf("%s/openai/%s?api-version=%s", url, path, *apiVersion)
	}

	// Create the request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(method)

	// Set authentication
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
		// Ensure api-key is not accidentally present (from extra headers, etc.)
		req.Header.Del("api-key")
	} else {
		req.Header.Set("api-key", key.Value)
	}

	// Set body and content type for non-GET requests
	if method != "GET" && requestBody != nil {
		req.Header.SetContentType("application/json")
		req.SetBody(jsonData)
	}

	// Send the request and measure latency
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from azure provider: %s", string(resp.Body())))

		var errorResp map[string]interface{}

		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("%s error: %v", schemas.Azure, errorResp)

		return nil, latency, bifrostErr
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since resp.Body() references fasthttp's internal buffer
	bodyCopy := append([]byte(nil), resp.Body()...)

	return bodyCopy, latency, nil
}

// TextCompletion performs a text completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	// Use centralized OpenAI text converter (Azure is OpenAI-compatible)
	reqBody := openai.ToOpenAITextCompletionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("text completion input is not provided", nil, schemas.Azure)
	}

	responseBody, latency, err := provider.completeRequest(ctx, "POST", reqBody, "completions", key, request.Model, true)
	if err != nil {
		return nil, err
	}

	response := &schemas.BifrostTextCompletionResponse{}

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = schemas.Azure
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.TextCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TextCompletionStream performs a streaming text completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *AzureProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if key.AzureKeyConfig == nil {
		return nil, newConfigurationError("azure key config not set", schemas.Azure)
	}

	// Construct Azure-specific URL with deployment
	if key.AzureKeyConfig.Endpoint == "" {
		return nil, newConfigurationError("endpoint not set", schemas.Azure)
	}

	baseURL := key.AzureKeyConfig.Endpoint
	var fullURL string

	if key.AzureKeyConfig.Deployments != nil {
		deployment := key.AzureKeyConfig.Deployments[request.Model]
		if deployment == "" {
			return nil, newConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), schemas.Azure)
		}

		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.Ptr(azure.DefaultAzureAPIVersion)
		}

		fullURL = fmt.Sprintf("%s/openai/deployments/%s/completions?api-version=%s", baseURL, deployment, *apiVersion)
	} else {
		return nil, newConfigurationError("deployments not set", schemas.Azure)
	}

	// Prepare Azure-specific headers
	authHeader := make(map[string]string)

	// Set Azure authentication - either Bearer token or api-key
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		authHeader["Authorization"] = fmt.Sprintf("Bearer %s", authToken)
	} else {
		authHeader["api-key"] = key.Value
	}

	return handleOpenAITextCompletionStreaming(
		ctx,
		provider.streamClient,
		fullURL,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		postHookRunner,
		provider.logger,
	)
}

// ChatCompletion performs a chat completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Use centralized OpenAI converter since Azure is OpenAI-compatible
	reqBody := openai.ToOpenAIChatRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("chat completion input is not provided", nil, schemas.Azure)
	}

	responseBody, latency, err := provider.completeRequest(ctx, "POST", reqBody, "chat/completions", key, request.Model, true)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	// response := acquireAzureChatResponse()
	// defer releaseAzureChatResponse(response)

	response := &schemas.BifrostChatResponse{}

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = schemas.Azure
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ChatCompletionRequest

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ChatCompletionStream performs a streaming chat completion request to Azure's OpenAI API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Azure-specific URL construction with deployments and supports both api-key and Bearer token authentication.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *AzureProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if key.AzureKeyConfig == nil {
		return nil, newConfigurationError("azure key config not set", schemas.Azure)
	}

	// Construct Azure-specific URL with deployment
	if key.AzureKeyConfig.Endpoint == "" {
		return nil, newConfigurationError("endpoint not set", schemas.Azure)
	}

	baseURL := key.AzureKeyConfig.Endpoint
	var fullURL string

	if key.AzureKeyConfig.Deployments != nil {
		deployment := key.AzureKeyConfig.Deployments[request.Model]
		if deployment == "" {
			return nil, newConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), schemas.Azure)
		}

		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.Ptr(azure.DefaultAzureAPIVersion)
		}

		fullURL = fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", baseURL, deployment, *apiVersion)
	} else {
		return nil, newConfigurationError("deployments not set", schemas.Azure)
	}

	// Prepare Azure-specific headers
	authHeader := make(map[string]string)

	// Set Azure authentication - either Bearer token or api-key
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		authHeader["Authorization"] = fmt.Sprintf("Bearer %s", authToken)
	} else {
		authHeader["api-key"] = key.Value
	}

	// Use shared streaming logic from OpenAI
	return handleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamClient,
		fullURL,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		schemas.Azure,
		postHookRunner,
		provider.logger,
	)
}

// Responses performs a responses request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	chatResponse, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}

	response := chatResponse.ToBifrostResponsesResponse()
	response.ExtraFields.RequestType = schemas.ResponsesRequest
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model

	return response, nil
}

// ResponsesStream performs a streaming responses request to Azure's API.
func (provider *AzureProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return provider.ChatCompletionStream(
		ctx,
		getResponsesChunkConverterCombinedPostHookRunner(postHookRunner),
		key,
		request.ToChatRequest(),
	)
}

// Embedding generates embeddings for the given input text(s) using Azure OpenAI.
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *AzureProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	// Use centralized converter
	reqBody := openai.ToOpenAIEmbeddingRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("embedding input is not provided", nil, schemas.Azure)
	}

	responseBody, latency, err := provider.completeRequest(ctx, "POST", reqBody, "embeddings", key, request.Model, true)
	if err != nil {
		return nil, err
	}

	response := &schemas.BifrostEmbeddingResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = schemas.Azure
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.EmbeddingRequest

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// Speech is not supported by the Azure provider.
func (provider *AzureProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "azure")
}

// SpeechStream is not supported by the Azure provider.
func (provider *AzureProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "azure")
}

// Transcription is not supported by the Azure provider.
func (provider *AzureProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "azure")
}

// TranscriptionStream is not supported by the Azure provider.
func (provider *AzureProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "azure")
}

// ListModels performs a list models request to Azure's API.
// It retrieves all models accessible by the Azure OpenAI resource
func (provider *AzureProvider) ListModels(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	// List models endpoint doesn't require a deployment, it's a resource-level operation
	responseBody, latency, err := provider.completeRequest(ctx, "GET", nil, "models", key, "", false)
	if err != nil {
		return nil, err
	}

	// Parse Azure-specific response
	azureResponse := &azure.AzureListModelsResponse{}
	rawResponse, bifrostErr := handleProviderResponse(responseBody, azureResponse, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	response := azureResponse.ToBifrostListModelsResponse()
	if response == nil {
		return nil, newBifrostOperationError("failed to convert Azure model list response", nil, schemas.Azure)
	}

	response = schemas.ApplyPagination(response, request.PageSize, request.PageToken)

	response.ExtraFields.Provider = schemas.Azure
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.RequestType = schemas.ListModelsRequest

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}
