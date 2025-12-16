// Package azure implements the Azure provider.
package azure

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/valyala/fasthttp"
)

// AzureAuthorizationTokenKey is the context key for the Azure authentication token.
const AzureAuthorizationTokenKey schemas.BifrostContextKey = "azure-authorization-token"

// AzureProvider implements the Provider interface for Azure's API.
type AzureProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewAzureProvider creates a new Azure provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewAzureProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*AzureProvider, error) {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 60 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	// Configure proxy if provided
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)

	return &AzureProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
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
func (provider *AzureProvider) completeRequest(
	ctx context.Context,
	jsonData []byte,
	path string,
	key schemas.Key,
	deployment string,
	model string,
	requestType schemas.RequestType,
) ([]byte, string, time.Duration, *schemas.BifrostError) {
	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	var url string
	if schemas.IsAnthropicModel(deployment) {
		req.Header.Set("x-api-key", key.Value)
		req.Header.Set("anthropic-version", AzureAnthropicAPIVersionDefault)
		url = fmt.Sprintf("%s/%s", key.AzureKeyConfig.Endpoint, path)
	} else {
		if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
			// TODO: Shift this to key.Value like in bedrock and vertex
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
			// Ensure api-key is not accidentally present (from extra headers, etc.)
			req.Header.Del("api-key")
		} else {
			req.Header.Set("api-key", key.Value)
		}
		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.Ptr(AzureAPIVersionDefault)
		}
		if path == "openai/v1/responses" {
			url = fmt.Sprintf("%s/%s?api-version=preview", key.AzureKeyConfig.Endpoint, path)
		} else {
			url = fmt.Sprintf("%s/%s?api-version=%s", key.AzureKeyConfig.Endpoint, path, *apiVersion)
		}
	}

	req.SetRequestURI(url)
	req.SetBody(jsonData)

	// Send the request and measure latency
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, deployment, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, deployment, latency, openai.ParseOpenAIError(resp, requestType, provider.GetProviderKey(), model)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, deployment, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since body references fasthttp's internal buffer
	bodyCopy := append([]byte(nil), body...)

	return bodyCopy, deployment, latency, nil
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *AzureProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	// Validate Azure key configuration
	if key.AzureKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("azure key config not set", schemas.Azure)
	}

	if key.AzureKeyConfig.Endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set", schemas.Azure)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create the request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(key.AzureKeyConfig.Endpoint + providerUtils.GetPathFromContext(ctx, fmt.Sprintf("/openai/models?api-version=%s", *apiVersion)))
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
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, openai.ParseOpenAIError(resp, schemas.ListModelsRequest, provider.GetProviderKey(), "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since resp.Body() references fasthttp's internal buffer
	responseBody := append([]byte(nil), body...)

	// Parse Azure-specific response
	azureResponse := &AzureListModelsResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, azureResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	response := azureResponse.ToBifrostListModelsResponse(key.Models, key.AzureKeyConfig.Deployments)
	if response == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert Azure model list response", nil, schemas.Azure)
	}

	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Azure's API.
// It retrieves all models accessible by the Azure resource
// Requests are made concurrently for improved performance.
func (provider *AzureProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
		provider.logger,
	)
}

// TextCompletion performs a text completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment, err := provider.getModelDeployment(key, request.Model)
	if err != nil {
		return nil, err
	}

	// Use centralized OpenAI text converter (Azure is OpenAI-compatible)
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return openai.ToOpenAITextCompletionRequest(request), nil },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	responseBody, deployment, latency, err := provider.completeRequest(
		ctx,
		jsonData,
		fmt.Sprintf("openai/deployments/%s/completions", deployment),
		key,
		deployment,
		request.Model,
		schemas.TextCompletionRequest,
	)
	if err != nil {
		return nil, err
	}

	response := &schemas.BifrostTextCompletionResponse{}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.ModelDeployment = deployment
	response.ExtraFields.RequestType = schemas.TextCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
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
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), provider.GetProviderKey())
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
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

	customPostResponseConverter := func(response *schemas.BifrostTextCompletionResponse) *schemas.BifrostTextCompletionResponse {
		response.ExtraFields.ModelDeployment = deployment
		return response
	}

	return openai.HandleOpenAITextCompletionStreaming(
		ctx,
		provider.client,
		url,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		customPostResponseConverter,
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

	deployment, err := provider.getModelDeployment(key, request.Model)
	if err != nil {
		return nil, err
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			if schemas.IsAnthropicModel(deployment) {
				reqBody, err := anthropic.ToAnthropicChatRequest(request)
				if err != nil {
					return nil, err
				}
				if reqBody != nil {
					reqBody.Model = deployment
				}
				return reqBody, nil
			} else {
				return openai.ToOpenAIChatRequest(request), nil
			}
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var path string
	if schemas.IsAnthropicModel(deployment) {
		path = "anthropic/v1/messages"
	} else {
		path = fmt.Sprintf("openai/deployments/%s/chat/completions", deployment)
	}

	responseBody, deployment, latency, err := provider.completeRequest(
		ctx,
		jsonData,
		path,
		key,
		deployment,
		request.Model,
		schemas.ChatCompletionRequest,
	)
	if err != nil {
		return nil, err
	}

	response := &schemas.BifrostChatResponse{}
	var rawRequest interface{}
	var rawResponse interface{}

	if schemas.IsAnthropicModel(deployment) {
		anthropicResponse := anthropic.AcquireAnthropicMessageResponse()
		defer anthropic.ReleaseAnthropicMessageResponse(anthropicResponse)
		rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, anthropicResponse, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		response = anthropicResponse.ToBifrostChatResponse()
	} else {
		rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, bifrostErr
		}
	}

	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.ModelDeployment = deployment
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.RequestType = schemas.ChatCompletionRequest

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ChatCompletionStream performs a streaming chat completion request to Azure's API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Azure-specific URL construction with deployments and supports both api-key and Bearer token authentication.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *AzureProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment, err := provider.getModelDeployment(key, request.Model)
	if err != nil {
		return nil, err
	}

	postResponseConverter := func(response *schemas.BifrostChatResponse) *schemas.BifrostChatResponse {
		response.ExtraFields.ModelDeployment = deployment
		return response
	}

	authHeader := make(map[string]string)
	var url string
	if schemas.IsAnthropicModel(deployment) {
		authHeader["x-api-key"] = key.Value
		authHeader["anthropic-version"] = AzureAnthropicAPIVersionDefault
		url = fmt.Sprintf("%s/anthropic/v1/messages", key.AzureKeyConfig.Endpoint)

		jsonData, err := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) {
				reqBody, err := anthropic.ToAnthropicChatRequest(request)
				if err != nil {
					return nil, err
				}
				if reqBody != nil {
					reqBody.Model = deployment
					reqBody.Stream = schemas.Ptr(true)
				}
				return reqBody, nil
			},
			provider.GetProviderKey())
		if err != nil {
			return nil, err
		}

		// Use shared streaming logic from Anthropic
		return anthropic.HandleAnthropicChatCompletionStreaming(
			ctx,
			provider.client,
			url,
			jsonData,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			postResponseConverter,
			provider.logger,
			&providerUtils.RequestMetadata{
				Provider:    provider.GetProviderKey(),
				Model:       request.Model,
				RequestType: schemas.ChatCompletionStreamRequest,
			},
		)
	} else {
		// Set Azure authentication - either Bearer token or api-key
		if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
			authHeader["Authorization"] = fmt.Sprintf("Bearer %s", authToken)
		} else {
			authHeader["api-key"] = key.Value
		}
		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.Ptr(AzureAPIVersionDefault)
		}
		url = fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", key.AzureKeyConfig.Endpoint, deployment, *apiVersion)

		// Use shared streaming logic from OpenAI
		return openai.HandleOpenAIChatCompletionStreaming(
			ctx,
			provider.client,
			url,
			request,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			nil,
			nil,
			postResponseConverter,
			provider.logger,
		)
	}
}

// Responses performs a responses request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment, err := provider.getModelDeployment(key, request.Model)
	if err != nil {
		return nil, err
	}

	var jsonData []byte
	var bifrostErr *schemas.BifrostError
	if schemas.IsAnthropicModel(deployment) {
		jsonData, bifrostErr = getRequestBodyForAnthropicResponses(ctx, request, deployment, provider.GetProviderKey(), false)
	} else {
		jsonData, bifrostErr = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) {
				reqBody := openai.ToOpenAIResponsesRequest(request)
				if reqBody != nil {
					reqBody.Model = deployment
				}
				return reqBody, nil
			},
			provider.GetProviderKey())
	}
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var path string
	if schemas.IsAnthropicModel(deployment) {
		path = "anthropic/v1/messages"
	} else {
		path = "openai/v1/responses"
	}

	responseBody, deployment, latency, err := provider.completeRequest(
		ctx,
		jsonData,
		path,
		key,
		deployment,
		request.Model,
		schemas.ResponsesRequest,
	)
	if err != nil {
		return nil, err
	}

	response := &schemas.BifrostResponsesResponse{}
	var rawRequest interface{}
	var rawResponse interface{}

	if schemas.IsAnthropicModel(deployment) {
		anthropicResponse := anthropic.AcquireAnthropicMessageResponse()
		defer anthropic.ReleaseAnthropicMessageResponse(anthropicResponse)
		rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, anthropicResponse, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		response = anthropicResponse.ToBifrostResponsesResponse()
	} else {
		rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, bifrostErr
		}
	}

	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.ModelDeployment = deployment
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.RequestType = schemas.ResponsesRequest

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ResponsesStream performs a streaming responses request to Azure's API.
func (provider *AzureProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment, err := provider.getModelDeployment(key, request.Model)
	if err != nil {
		return nil, err
	}

	postResponseConverter := func(response *schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse {
		response.ExtraFields.ModelDeployment = deployment
		return response
	}

	authHeader := make(map[string]string)
	var url string
	if schemas.IsAnthropicModel(deployment) {
		authHeader["x-api-key"] = key.Value
		authHeader["anthropic-version"] = AzureAnthropicAPIVersionDefault
		url = fmt.Sprintf("%s/anthropic/v1/messages", key.AzureKeyConfig.Endpoint)

		jsonData, bifrostErr := getRequestBodyForAnthropicResponses(ctx, request, deployment, provider.GetProviderKey(), true)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		// Use shared streaming logic from Anthropic
		return anthropic.HandleAnthropicResponsesStream(
			ctx,
			provider.client,
			url,
			jsonData,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			postResponseConverter,
			provider.logger,
			&providerUtils.RequestMetadata{
				Provider:    provider.GetProviderKey(),
				Model:       request.Model,
				RequestType: schemas.ResponsesStreamRequest,
			},
		)
	} else {
		// Set Azure authentication - either Bearer token or api-key
		if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok {
			authHeader["Authorization"] = fmt.Sprintf("Bearer %s", authToken)
		} else {
			authHeader["api-key"] = key.Value
		}
		url = fmt.Sprintf("%s/openai/v1/responses?api-version=preview", key.AzureKeyConfig.Endpoint)

		postRequestConverter := func(req *openai.OpenAIResponsesRequest) *openai.OpenAIResponsesRequest {
			req.Model = deployment
			return req
		}

		// Use shared streaming logic from OpenAI
		return openai.HandleOpenAIResponsesStreaming(
			ctx,
			provider.client,
			url,
			request,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			postRequestConverter,
			postResponseConverter,
			provider.logger,
		)
	}
}

// Embedding generates embeddings for the given input text(s) using Azure.
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *AzureProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment, err := provider.getModelDeployment(key, request.Model)
	if err != nil {
		return nil, err
	}

	// Use centralized converter
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return openai.ToOpenAIEmbeddingRequest(request), nil },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	responseBody, deployment, latency, err := provider.completeRequest(
		ctx,
		jsonData,
		fmt.Sprintf("openai/deployments/%s/embeddings", deployment),
		key,
		deployment,
		request.Model,
		schemas.EmbeddingRequest,
	)
	if err != nil {
		return nil, err
	}

	response := &schemas.BifrostEmbeddingResponse{}

	// Use enhanced response handler with pre-allocated response
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.ModelDeployment = deployment
	response.ExtraFields.RequestType = schemas.EmbeddingRequest

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// Speech is not supported by the Azure provider.
func (provider *AzureProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Azure provider.
func (provider *AzureProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Azure provider.
func (provider *AzureProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Azure provider.
func (provider *AzureProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// validateKeyConfig validates the key configuration.
// It checks if the key config is set, the endpoint is set, and the deployments are set.
// Returns an error if any of the checks fail.
func (provider *AzureProvider) validateKeyConfig(key schemas.Key) *schemas.BifrostError {
	if key.AzureKeyConfig == nil {
		return providerUtils.NewConfigurationError("azure key config not set", provider.GetProviderKey())
	}

	if key.AzureKeyConfig.Endpoint == "" {
		return providerUtils.NewConfigurationError("endpoint not set", provider.GetProviderKey())
	}

	if key.AzureKeyConfig.Deployments == nil {
		return providerUtils.NewConfigurationError("deployments not set", provider.GetProviderKey())
	}

	return nil
}

// validateKeyConfigForFiles validates key config for file/batch APIs, which only
// require a configured Azure endpoint (no per-model deployments needed).
func (provider *AzureProvider) validateKeyConfigForFiles(key schemas.Key) *schemas.BifrostError {
	if key.AzureKeyConfig == nil {
		return providerUtils.NewConfigurationError("azure key config not set", provider.GetProviderKey())
	}
	if key.AzureKeyConfig.Endpoint == "" {
		return providerUtils.NewConfigurationError("endpoint not set", provider.GetProviderKey())
	}
	return nil
}

func (provider *AzureProvider) getModelDeployment(key schemas.Key, model string) (string, *schemas.BifrostError) {
	if key.AzureKeyConfig == nil {
		return "", providerUtils.NewConfigurationError("azure key config not set", provider.GetProviderKey())
	}

	if key.AzureKeyConfig.Deployments != nil {
		if deployment, ok := key.AzureKeyConfig.Deployments[model]; ok {
			return deployment, nil
		}
	}
	return "", providerUtils.NewConfigurationError(fmt.Sprintf("deployment not found for model %s", model), provider.GetProviderKey())
}

// FileUpload uploads a file to Azure OpenAI.
func (provider *AzureProvider) FileUpload(ctx context.Context, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil, providerName)
	}

	if request.Purpose == "" {
		return nil, providerUtils.NewBifrostOperationError("purpose is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add purpose field
	if err := writer.WriteField("purpose", string(request.Purpose)); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write purpose field", err, providerName)
	}

	// Add file field
	filename := request.Filename
	if filename == "" {
		filename = "file.jsonl"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create form file", err, providerName)
	}
	if _, err := part.Write(request.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file content", err, providerName)
	}

	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to close multipart writer", err, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/files", key.AzureKeyConfig.Endpoint)
	values := url.Values{}
	values.Set("api-version", *apiVersion)
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileUploadRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIFileResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostFileUploadResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// FileList lists files from Azure OpenAI.
func (provider *AzureProvider) FileList(ctx context.Context, key schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	requestURL := fmt.Sprintf("%s/openai/files", key.AzureKeyConfig.Endpoint)
	values := url.Values{}
	values.Set("api-version", *apiVersion)
	if request.Purpose != "" {
		values.Set("purpose", string(request.Purpose))
	}
	if encodedValues := values.Encode(); encodedValues != "" {
		requestURL += "?" + encodedValues
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileListRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIFileListResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  openAIResp.Object,
		HasMore: openAIResp.HasMore,
		Data:    make([]schemas.FileObject, len(openAIResp.Data)),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	for i, file := range openAIResp.Data {
		bifrostResp.Data[i] = schemas.FileObject{
			ID:            file.ID,
			Object:        file.Object,
			Bytes:         file.Bytes,
			CreatedAt:     file.CreatedAt,
			Filename:      file.Filename,
			Purpose:       schemas.FilePurpose(file.Purpose),
			Status:        openai.ToBifrostFileStatus(file.Status),
			StatusDetails: file.StatusDetails,
		}
	}

	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from Azure OpenAI.
func (provider *AzureProvider) FileRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/files/%s", key.AzureKeyConfig.Endpoint, url.PathEscape(request.FileID))
	values := url.Values{}
	values.Set("api-version", *apiVersion)
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileRetrieveRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIFileResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostFileRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// FileDelete deletes a file from Azure OpenAI.
func (provider *AzureProvider) FileDelete(ctx context.Context, key schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/files/%s", key.AzureKeyConfig.Endpoint, url.PathEscape(request.FileID))
	values := url.Values{}
	values.Set("api-version", *apiVersion)
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodDelete)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusNoContent {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileDeleteRequest, providerName, "")
	}

	if resp.StatusCode() == fasthttp.StatusNoContent {
		return &schemas.BifrostFileDeleteResponse{
			ID:      request.FileID,
			Object:  "file",
			Deleted: true,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileDeleteRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIFileDeleteResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result := &schemas.BifrostFileDeleteResponse{
		ID:      openAIResp.ID,
		Object:  openAIResp.Object,
		Deleted: openAIResp.Deleted,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileDeleteRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if sendBackRawRequest {
		result.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		result.ExtraFields.RawResponse = rawResponse
	}

	return result, nil
}

// FileContent downloads file content from Azure OpenAI.
func (provider *AzureProvider) FileContent(ctx context.Context, key schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/files/%s/content", key.AzureKeyConfig.Endpoint, url.PathEscape(request.FileID))
	values := url.Values{}
	values.Set("api-version", *apiVersion)
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.FileContentRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Get content type from response
	contentType := string(resp.Header.ContentType())
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	content := append([]byte(nil), body...)
	return &schemas.BifrostFileContentResponse{
		FileID:      request.FileID,
		Content:     content,
		ContentType: contentType,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileContentRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// BatchCreate creates a new batch job on Azure OpenAI.
// Azure Batch API uses the same format as OpenAI but with Azure-specific URL patterns.
func (provider *AzureProvider) BatchCreate(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	inputFileID := request.InputFileID

	// If no file_id provided but inline requests are available, upload them first
	if inputFileID == "" && len(request.Requests) > 0 {
		// Convert inline requests to JSONL format
		jsonlData, err := openai.ConvertRequestsToJSONL(request.Requests)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to convert requests to JSONL", err, providerName)
		}

		// Upload the file with purpose "batch"
		uploadResp, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
			Provider: schemas.Azure,
			File:     jsonlData,
			Filename: "batch_requests.jsonl",
			Purpose:  "batch",
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		inputFileID = uploadResp.ID
	}

	// Validate that we have a file ID (either provided or uploaded)
	if inputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("either input_file_id or requests array is required for Azure batch API", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/batches", key.AzureKeyConfig.Endpoint)
	values := url.Values{}
	values.Set("api-version", *apiVersion)
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Build request body
	openAIReq := &openai.OpenAIBatchRequest{
		InputFileID:      inputFileID,
		Endpoint:         string(request.Endpoint),
		CompletionWindow: request.CompletionWindow,
		Metadata:         request.Metadata,
	}

	// Set default completion window if not provided
	if openAIReq.CompletionWindow == "" {
		openAIReq.CompletionWindow = "24h"
	}

	jsonData, err := sonic.Marshal(openAIReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}
	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.BatchCreateRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIBatchResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostBatchCreateResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// BatchList lists batch jobs from Azure OpenAI.
func (provider *AzureProvider) BatchList(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/batches", key.AzureKeyConfig.Endpoint)
	values := url.Values{}
	values.Set("api-version", *apiVersion)
	if request.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	if request.After != nil && *request.After != "" {
		values.Set("after", *request.After)
	}
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.BatchListRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIBatchListResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostBatchListResponse{
		Object:  openAIResp.Object,
		FirstID: openAIResp.FirstID,
		LastID:  openAIResp.LastID,
		HasMore: openAIResp.HasMore,
		Data:    make([]schemas.BifrostBatchRetrieveResponse, len(openAIResp.Data)),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	for i, batch := range openAIResp.Data {
		bifrostResp.Data[i] = *batch.ToBifrostBatchRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse)
	}

	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// BatchRetrieve retrieves a specific batch job from Azure OpenAI.
func (provider *AzureProvider) BatchRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/batches/%s", key.AzureKeyConfig.Endpoint, url.PathEscape(request.BatchID))
	values := url.Values{}
	values.Set("api-version", *apiVersion)
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.BatchRetrieveRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIBatchResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result := openAIResp.ToBifrostBatchRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse)
	result.ExtraFields.RequestType = schemas.BatchRetrieveRequest
	return result, nil
}

// BatchCancel cancels a batch job on Azure OpenAI.
func (provider *AzureProvider) BatchCancel(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.Ptr(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/batches/%s/cancel", key.AzureKeyConfig.Endpoint, url.PathEscape(request.BatchID))
	values := url.Values{}
	values.Set("api-version", *apiVersion)
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	provider.setAzureAuth(ctx, req, key)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.BatchCancelRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp openai.OpenAIBatchResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result := &schemas.BifrostBatchCancelResponse{
		ID:           openAIResp.ID,
		Object:       openAIResp.Object,
		Status:       openai.ToBifrostBatchStatus(openAIResp.Status),
		CancellingAt: openAIResp.CancellingAt,
		CancelledAt:  openAIResp.CancelledAt,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCancelRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if openAIResp.RequestCounts != nil {
		result.RequestCounts = schemas.BatchRequestCounts{
			Total:     openAIResp.RequestCounts.Total,
			Completed: openAIResp.RequestCounts.Completed,
			Failed:    openAIResp.RequestCounts.Failed,
		}
	}

	if sendBackRawRequest {
		result.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		result.ExtraFields.RawResponse = rawResponse
	}

	return result, nil
}

// BatchResults retrieves batch results from Azure OpenAI.
// For Azure (like OpenAI), batch results are obtained by downloading the output_file_id.
func (provider *AzureProvider) BatchResults(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// First, retrieve the batch to get the output_file_id
	batchResp, bifrostErr := provider.BatchRetrieve(ctx, key, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  request.BatchID,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if batchResp.OutputFileID == nil || *batchResp.OutputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch results not available: output_file_id is empty (batch may not be completed)", nil, providerName)
	}

	// Download the output file content
	fileContentResp, bifrostErr := provider.FileContent(ctx, key, &schemas.BifrostFileContentRequest{
		Provider: request.Provider,
		FileID:   *batchResp.OutputFileID,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Parse JSONL content - each line is a separate result
	var results []schemas.BatchResultItem

	parseResult := providerUtils.ParseJSONL(fileContentResp.Content, func(line []byte) error {
		var resultItem schemas.BatchResultItem
		if err := sonic.Unmarshal(line, &resultItem); err != nil {
			provider.logger.Warn(fmt.Sprintf("failed to parse batch result line: %v", err))
			return err
		}
		results = append(results, resultItem)
		return nil
	})

	batchResultsResp := &schemas.BifrostBatchResultsResponse{
		BatchID: request.BatchID,
		Results: results,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchResultsRequest,
			Provider:    providerName,
			Latency:     fileContentResp.ExtraFields.Latency,
		},
	}

	if len(parseResult.Errors) > 0 {
		batchResultsResp.ExtraFields.ParseErrors = parseResult.Errors
	}

	return batchResultsResp, nil
}
