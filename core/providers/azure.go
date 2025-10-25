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
func (provider *AzureProvider) completeRequest(ctx context.Context, requestBody interface{}, path string, key schemas.Key, model string, requestType schemas.RequestType) ([]byte, time.Duration, *schemas.BifrostError) {
	// Marshal the request body
	jsonData, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, 0, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, provider.GetProviderKey())
	}

	deployment := key.AzureKeyConfig.Deployments[model]
	if deployment == "" {
		return nil, 0, newConfigurationError(fmt.Sprintf("deployment not found for model %s", model), provider.GetProviderKey())
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(azure.DefaultAzureAPIVersion)
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/%s?api-version=%s", key.AzureKeyConfig.Endpoint, deployment, path, *apiVersion)

	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
		// Ensure api-key is not accidentally present (from extra headers, etc.)
		req.Header.Del("api-key")
	} else {
		req.Header.Set("api-key", key.Value)
	}

	req.SetBody(jsonData)

	// Send the request and measure latency
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, latency, parseOpenAIError(resp, requestType, provider.GetProviderKey(), model)
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since resp.Body() references fasthttp's internal buffer
	bodyCopy := append([]byte(nil), resp.Body()...)

	return bodyCopy, latency, nil
}

// ListModels performs a list models request to Azure's API.
// It retrieves all models accessible by the Azure OpenAI resource
func (provider *AzureProvider) ListModels(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	// Validate Azure key configuration
	if key.AzureKeyConfig == nil {
		return nil, newConfigurationError("azure key config not set", schemas.Azure)
	}

	if key.AzureKeyConfig.Endpoint == "" {
		return nil, newConfigurationError("endpoint not set", schemas.Azure)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(azure.DefaultAzureAPIVersion)
	}

	// Construct URL - list models is a resource-level operation, doesn't require deployment
	url := fmt.Sprintf("%s/openai/models?api-version=%s", key.AzureKeyConfig.Endpoint, *apiVersion)

	// Create the request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Set Azure authentication - either Bearer token or api-key
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
		// Ensure api-key is not accidentally present (from extra headers, etc.)
		req.Header.Del("api-key")
	} else {
		req.Header.Set("api-key", key.Value)
	}

	// Send the request and measure latency
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from azure provider: %s", string(resp.Body())))
		return nil, parseOpenAIError(resp, schemas.ListModelsRequest, provider.GetProviderKey(), "")
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since resp.Body() references fasthttp's internal buffer
	responseBody := append([]byte(nil), resp.Body()...)

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

	response = response.ApplyPagination(request.PageSize, request.PageToken)

	response.ExtraFields.Provider = schemas.Azure
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.RequestType = schemas.ListModelsRequest

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TextCompletion performs a text completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	// Use centralized OpenAI text converter (Azure is OpenAI-compatible)
	reqBody := openai.ToOpenAITextCompletionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("text completion input is not provided", nil, provider.GetProviderKey())
	}

	responseBody, latency, err := provider.completeRequest(ctx, reqBody, "completions", key, request.Model, schemas.TextCompletionRequest)
	if err != nil {
		return nil, err
	}

	response := &schemas.BifrostTextCompletionResponse{}

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = provider.GetProviderKey()
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
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment := key.AzureKeyConfig.Deployments[request.Model]
	if deployment == "" {
		return nil, newConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), provider.GetProviderKey())
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(azure.DefaultAzureAPIVersion)
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/completions?api-version=%s", key.AzureKeyConfig.Endpoint, deployment, *apiVersion)

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
		url,
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
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	// Use centralized OpenAI converter since Azure is OpenAI-compatible
	reqBody := openai.ToOpenAIChatRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("chat completion input is not provided", nil, provider.GetProviderKey())
	}

	responseBody, latency, err := provider.completeRequest(ctx, reqBody, "chat/completions", key, request.Model, schemas.ChatCompletionRequest)
	if err != nil {
		return nil, err
	}

	response := &schemas.BifrostChatResponse{}

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = provider.GetProviderKey()
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
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment := key.AzureKeyConfig.Deployments[request.Model]
	if deployment == "" {
		return nil, newConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), provider.GetProviderKey())
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(azure.DefaultAzureAPIVersion)
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", key.AzureKeyConfig.Endpoint, deployment, *apiVersion)

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
		url,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		postHookRunner,
		provider.logger,
	)
}

// Responses performs a responses request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment := key.AzureKeyConfig.Deployments[request.Model]
	if deployment == "" {
		return nil, newConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), provider.GetProviderKey())
	}

	url := fmt.Sprintf("%s/openai/v1/responses?api-version=preview", key.AzureKeyConfig.Endpoint)

	// Use centralized OpenAI converter since Azure is OpenAI-compatible
	reqBody := openai.ToOpenAIResponsesRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("responses input is not provided", nil, provider.GetProviderKey())
	}

	reqBody.Model = deployment

	// Marshal the request body
	jsonData, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, provider.GetProviderKey())
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

	// Send the request and measure latency
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseOpenAIError(resp, schemas.ResponsesRequest, provider.GetProviderKey(), request.Model)
	}

	response := &schemas.BifrostResponsesResponse{}

	rawResponse, bifrostErr := handleProviderResponse(resp.Body(), response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ResponsesRequest

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ResponsesStream performs a streaming responses request to Azure's API.
func (provider *AzureProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment := key.AzureKeyConfig.Deployments[request.Model]
	if deployment == "" {
		return nil, newConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), provider.GetProviderKey())
	}

	url := fmt.Sprintf("%s/openai/v1/responses?api-version=preview", key.AzureKeyConfig.Endpoint)

	// Prepare Azure-specific headers
	authHeader := make(map[string]string)

	// Set Azure authentication - either Bearer token or api-key
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
		authHeader["Authorization"] = fmt.Sprintf("Bearer %s", authToken)
	} else {
		authHeader["api-key"] = key.Value
	}

	postRequestConverter := func(req *openai.OpenAIResponsesRequest) *openai.OpenAIResponsesRequest {
		req.Model = deployment
		return req
	}

	// Use shared streaming logic from OpenAI
	return handleOpenAIResponsesStreaming(
		ctx,
		provider.streamClient,
		url,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		postHookRunner,
		postRequestConverter,
		provider.logger,
	)
}

// Embedding generates embeddings for the given input text(s) using Azure OpenAI.
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *AzureProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	// Use centralized converter
	reqBody := openai.ToOpenAIEmbeddingRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("embedding input is not provided", nil, provider.GetProviderKey())
	}

	responseBody, latency, err := provider.completeRequest(ctx, reqBody, "embeddings", key, request.Model, schemas.EmbeddingRequest)
	if err != nil {
		return nil, err
	}

	response := &schemas.BifrostEmbeddingResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = provider.GetProviderKey()
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

// validateKeyConfig validates the key configuration.
// It checks if the key config is set, the endpoint is set, and the deployments are set.
// Returns an error if any of the checks fail.
func (provider *AzureProvider) validateKeyConfig(key schemas.Key) *schemas.BifrostError {
	if key.AzureKeyConfig == nil {
		return newConfigurationError("azure key config not set", provider.GetProviderKey())
	}

	if key.AzureKeyConfig.Endpoint == "" {
		return newConfigurationError("endpoint not set", provider.GetProviderKey())
	}

	if key.AzureKeyConfig.Deployments == nil {
		return newConfigurationError("deployments not set", provider.GetProviderKey())
	}

	return nil
}
