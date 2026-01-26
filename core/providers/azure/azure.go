// Package azure implements the Azure provider.
package azure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/valyala/fasthttp"
)

// AzureAuthorizationTokenKey is the context key for the Azure authentication token.
const AzureAuthorizationTokenKey schemas.BifrostContextKey = "azure-authorization-token"

// DefaultAzureScope is the default scope for Azure authentication.
const DefaultAzureScope = "https://cognitiveservices.azure.com/.default"

// AzureProvider implements the Provider interface for Azure's API.
type AzureProvider struct {
	logger        schemas.Logger        // Logger for provider operations
	client        *fasthttp.Client      // HTTP client for API requests
	networkConfig schemas.NetworkConfig // Network configuration including extra headers

	credentials         sync.Map // map of tenant ID:client ID to azcore.TokenCredential
	sendBackRawRequest  bool     // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool     // Whether to include raw response in BifrostResponse
}

func (p *AzureProvider) getOrCreateAuth(
	tenantID, clientID, clientSecret string,
) (azcore.TokenCredential, error) {
	key := tenantID + ":" + clientID

	// Fast path
	if val, ok := p.credentials.Load(key); ok {
		return val.(azcore.TokenCredential), nil
	}

	// Slow path - create new credential
	cred, err := azidentity.NewClientSecretCredential(
		tenantID,
		clientID,
		clientSecret,
		nil,
	)
	if err != nil {
		return nil, err
	}

	actual, _ := p.credentials.LoadOrStore(key, cred)
	return actual.(azcore.TokenCredential), nil
}

// getAzureAuthHeaders returns authentication headers based on priority:
// 1. Service Principal (client ID/secret/tenant ID) - Bearer token
// 2. Context token - Bearer token
// 3. API key - api-key or x-api-key header
func (provider *AzureProvider) getAzureAuthHeaders(ctx *schemas.BifrostContext, key schemas.Key, isAnthropicModel bool) (map[string]string, *schemas.BifrostError) {
	authHeader := make(map[string]string)

	// Service Principal authentication
	if key.AzureKeyConfig != nil && key.AzureKeyConfig.ClientID != nil &&
		key.AzureKeyConfig.ClientSecret != nil && key.AzureKeyConfig.TenantID != nil && key.AzureKeyConfig.ClientID.GetValue() != "" && key.AzureKeyConfig.ClientSecret.GetValue() != "" && key.AzureKeyConfig.TenantID.GetValue() != "" {
		cred, err := provider.getOrCreateAuth(key.AzureKeyConfig.TenantID.GetValue(), key.AzureKeyConfig.ClientID.GetValue(), key.AzureKeyConfig.ClientSecret.GetValue())
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to get or create Azure authentication", err, schemas.Azure)
		}

		token, err := cred.GetToken(ctx, policy.TokenRequestOptions{
			Scopes: []string{DefaultAzureScope},
		})
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to get Azure access token", err, schemas.Azure)
		}

		if token.Token == "" {
			return nil, providerUtils.NewBifrostOperationError("Azure access token is empty", errors.New("token is empty"), schemas.Azure)
		}

		authHeader["Authorization"] = fmt.Sprintf("Bearer %s", token.Token)
		return authHeader, nil
	}

	// Context token authentication
	if authToken, ok := ctx.Value(AzureAuthorizationTokenKey).(string); ok && authToken != "" {
		authHeader["Authorization"] = fmt.Sprintf("Bearer %s", authToken)
		return authHeader, nil
	}

	value := key.Value.GetValue()
	if value == "" {
		return nil, providerUtils.NewBifrostOperationError("API key is empty", errors.New("API key is empty"), schemas.Azure)
	}

	// API key authentication
	if isAnthropicModel {
		authHeader["x-api-key"] = value
	} else {
		authHeader["api-key"] = value
	}
	return authHeader, nil
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
	ctx *schemas.BifrostContext,
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
	isAnthropicModel := schemas.IsAnthropicModel(deployment)

	// Get authentication headers
	authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, isAnthropicModel)
	if bifrostErr != nil {
		return nil, deployment, 0, bifrostErr
	}

	// Apply headers to request
	for k, v := range authHeaders {
		req.Header.Set(k, v)
	}

	endpoint := key.AzureKeyConfig.Endpoint.GetValue()
	if endpoint == "" {
		return nil, deployment, 0, providerUtils.NewConfigurationError("endpoint not set", provider.GetProviderKey())
	}

	if isAnthropicModel {
		req.Header.Set("anthropic-version", AzureAnthropicAPIVersionDefault)
		url = fmt.Sprintf("%s/%s", endpoint, path)
	} else {
		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
		}
		if path == "openai/v1/responses" {
			url = fmt.Sprintf("%s/%s?api-version=preview", endpoint, path)
		} else {
			url = fmt.Sprintf("%s/%s?api-version=%s", endpoint, path, apiVersion.GetValue())
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
func (provider *AzureProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	// Validate Azure key configuration
	if key.AzureKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("azure key config not set", schemas.Azure)
	}

	if key.AzureKeyConfig.Endpoint.GetValue() == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set", schemas.Azure)
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}

	// Create the request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(key.AzureKeyConfig.Endpoint.GetValue() + providerUtils.GetPathFromContext(ctx, fmt.Sprintf("/openai/models?api-version=%s", apiVersion.GetValue())))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	for k, v := range authHeaders {
		req.Header.Set(k, v)
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
func (provider *AzureProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
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
func (provider *AzureProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
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
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	response := &schemas.BifrostTextCompletionResponse{}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
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
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *AzureProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment := key.AzureKeyConfig.Deployments[request.Model]
	if deployment == "" {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), provider.GetProviderKey())
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/completions?api-version=%s", key.AzureKeyConfig.Endpoint.GetValue(), deployment, apiVersion.GetValue())

	// Get Azure authentication headers
	authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
	if err != nil {
		return nil, err
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
		nil,
		postHookRunner,
		customPostResponseConverter,
		provider.logger,
	)
}

// ChatCompletion performs a chat completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
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
				reqBody, err := anthropic.ToAnthropicChatRequest(ctx, request)
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
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	response := &schemas.BifrostChatResponse{}
	var rawRequest interface{}
	var rawResponse interface{}

	if schemas.IsAnthropicModel(deployment) {
		anthropicResponse := anthropic.AcquireAnthropicMessageResponse()
		defer anthropic.ReleaseAnthropicMessageResponse(anthropicResponse)
		rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, anthropicResponse, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		response = anthropicResponse.ToBifrostChatResponse(ctx)
	} else {
		rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
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
func (provider *AzureProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
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

	var url string
	if schemas.IsAnthropicModel(deployment) {
		authHeader, err := provider.getAzureAuthHeaders(ctx, key, true)
		if err != nil {
			return nil, err
		}
		authHeader["anthropic-version"] = AzureAnthropicAPIVersionDefault
		url = fmt.Sprintf("%s/anthropic/v1/messages", key.AzureKeyConfig.Endpoint.GetValue())

		jsonData, err := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) {
				reqBody, err := anthropic.ToAnthropicChatRequest(ctx, request)
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
		authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
		if err != nil {
			return nil, err
		}
		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
		}
		url = fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", key.AzureKeyConfig.Endpoint.GetValue(), deployment, apiVersion.GetValue())

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
			nil,
			postResponseConverter,
			provider.logger,
		)
	}
}

// Responses performs a responses request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	response := &schemas.BifrostResponsesResponse{}
	var rawRequest interface{}
	var rawResponse interface{}

	if schemas.IsAnthropicModel(deployment) {
		anthropicResponse := anthropic.AcquireAnthropicMessageResponse()
		defer anthropic.ReleaseAnthropicMessageResponse(anthropicResponse)
		rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, anthropicResponse, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		response = anthropicResponse.ToBifrostResponsesResponse(ctx)
	} else {
		rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
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
func (provider *AzureProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
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

	var url string
	if schemas.IsAnthropicModel(deployment) {
		authHeader, err := provider.getAzureAuthHeaders(ctx, key, true)
		if err != nil {
			return nil, err
		}
		authHeader["anthropic-version"] = AzureAnthropicAPIVersionDefault
		url = fmt.Sprintf("%s/anthropic/v1/messages", key.AzureKeyConfig.Endpoint.GetValue())

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
		authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
		if err != nil {
			return nil, err
		}
		url = fmt.Sprintf("%s/openai/v1/responses?api-version=preview", key.AzureKeyConfig.Endpoint.GetValue())

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
			nil,
			postRequestConverter,
			postResponseConverter,
			provider.logger,
		)
	}
}

// Embedding generates embeddings for the given input text(s) using Azure.
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *AzureProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
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
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	response := &schemas.BifrostEmbeddingResponse{}

	// Use enhanced response handler with pre-allocated response
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
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
func (provider *AzureProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment, err := provider.getModelDeployment(key, request.Model)
	if err != nil {
		return nil, err
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}

	endpoint := key.AzureKeyConfig.Endpoint.GetValue()
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set", provider.GetProviderKey())
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/audio/speech?api-version=%s", endpoint, deployment, apiVersion.GetValue())

	response, err := openai.HandleOpenAISpeechRequest(
		ctx,
		provider.client,
		url,
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)

	if err != nil {
		return nil, err
	}

	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.ModelDeployment = deployment

	return response, err
}

// SpeechStream handles streaming for speech synthesis with Azure.
// Azure sends raw binary audio bytes in SSE format, unlike OpenAI which sends JSON.
func (provider *AzureProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment, err := provider.getModelDeployment(key, request.Model)
	if err != nil {
		return nil, err
	}

	// Get Azure authentication headers
	authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
	if err != nil {
		return nil, err
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}
	url := fmt.Sprintf("%s/openai/deployments/%s/audio/speech?api-version=%s", key.AzureKeyConfig.Endpoint.GetValue(), deployment, apiVersion.GetValue())

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	// Prepare headers
	headers := map[string]string{
		"Content-Type":    "application/json",
		"Accept":          "text/event-stream",
		"Cache-Control":   "no-cache",
		"Accept-Encoding": "identity",
	}

	maps.Copy(headers, authHeader)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Set headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Build request body
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := openai.ToOpenAISpeechRequest(request)
			if reqBody != nil {
				reqBody.StreamFormat = schemas.Ptr("sse")
				reqBody.Model = deployment // Replace model with deployment
			}
			return reqBody, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonBody)

	// Make the request
	requestErr := provider.client.Do(req, resp)
	if requestErr != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(requestErr, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   requestErr,
				},
			}, jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
		}
		if errors.Is(requestErr, fasthttp.ErrTimeout) || errors.Is(requestErr, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, requestErr, provider.GetProviderKey()), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, requestErr, provider.GetProviderKey()), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, providerUtils.EnrichError(ctx, openai.ParseOpenAIError(resp, schemas.SpeechStreamRequest, provider.GetProviderKey(), request.Model), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.GetProviderKey(), request.Model, schemas.SpeechStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.GetProviderKey(), request.Model, schemas.SpeechStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		// Always release response on exit; bodyStream close should prevent indefinite blocking.
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()

		// Check if response is compressed
		bodyStream := resp.BodyStream()
		chunkIndex := -1
		startTime := time.Now()
		lastChunkTime := startTime

		// Read SSE events manually to handle binary data with embedded newlines
		// SSE format: "data: <content>\n\n" - events are separated by double newlines
		// We can't use bufio.Scanner because MP3 data contains 0x0a bytes which get interpreted as newlines
		readBuffer := make([]byte, 64*1024) // 64KB read chunks
		var accumulated []byte

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			// Read from stream
			n, readErr := bodyStream.Read(readBuffer)
			if n > 0 {
				accumulated = append(accumulated, readBuffer[:n]...)

				// Process complete SSE events (separated by \n\n)
				for {
					// Find the next double-newline separator
					idx := bytes.Index(accumulated, []byte("\n\n"))
					if idx == -1 {
						// No complete event yet, need more data
						break
					}

					// Extract the event (everything up to \n\n)
					event := accumulated[:idx]
					accumulated = accumulated[idx+2:] // Skip the \n\n

					// Skip empty events and comments
					if len(event) == 0 || bytes.HasPrefix(event, []byte(":")) {
						continue
					}

					// Parse the SSE event
					var audioData []byte

					// Check if this has "data: " prefix (standard SSE format)
					if bytes.HasPrefix(event, []byte("data: ")) {
						audioData = event[6:] // Skip "data: " prefix
						// Check for [DONE] marker
						if bytes.Equal(audioData, []byte("[DONE]")) {
							return
						}
					} else {
						// Raw data without prefix (shouldn't happen with Azure, but handle it)
						audioData = event
					}

					// First, try to parse as JSON error response (these would be valid JSON text)
					var bifrostErr schemas.BifrostError
					if err := sonic.Unmarshal(audioData, &bifrostErr); err == nil {
						if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
							bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
								Provider:       provider.GetProviderKey(),
								ModelRequested: request.Model,
								RequestType:    schemas.SpeechStreamRequest,
							}
							ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
							providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, provider.logger)
							return
						}
					}

					// Skip empty audio data
					if len(audioData) == 0 {
						continue
					}

					chunkIndex++

					// Create response with raw audio data
					// Azure sends raw binary MP3 frames (starting with 0xff 0xf3 or 0xff 0xfb)
					response := schemas.BifrostSpeechStreamResponse{
						Type:  schemas.SpeechStreamResponseTypeDelta,
						Audio: audioData,
						ExtraFields: schemas.BifrostResponseExtraFields{
							RequestType:     schemas.SpeechStreamRequest,
							Provider:        provider.GetProviderKey(),
							ModelRequested:  request.Model,
							ModelDeployment: deployment,
							ChunkIndex:      chunkIndex,
							Latency:         time.Since(lastChunkTime).Milliseconds(),
						},
					}
					lastChunkTime = time.Now()

					if sendBackRawResponse {
						response.ExtraFields.RawResponse = audioData
					}

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, &response, nil, nil), responseChan)
				}
			}

			// Handle read errors
			if readErr != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				if readErr != io.EOF {
					provider.logger.Warn("Error reading stream: %v", readErr)
				}
				break
			}
		}

		// Send final "done" response if we received audio chunks
		if chunkIndex >= 0 {
			finalResponse := schemas.BifrostSpeechStreamResponse{
				Type: schemas.SpeechStreamResponseTypeDone,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:     schemas.SpeechStreamRequest,
					Provider:        provider.GetProviderKey(),
					ModelRequested:  request.Model,
					ModelDeployment: deployment,
					ChunkIndex:      chunkIndex + 1,
					Latency:         time.Since(startTime).Milliseconds(),
				},
			}

			if sendBackRawRequest {
				providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, jsonBody)
			}

			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, &finalResponse, nil, nil), responseChan)
		}

		// Note: We don't call ReleaseStreamingResponse here because the scanner has already
		// consumed the body stream. Calling it would block indefinitely waiting for more data.
		// The response will be released when the request context is cleaned up.
	}()

	return responseChan, nil
}

// Transcription is not supported by the Azure provider.
func (provider *AzureProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment, err := provider.getModelDeployment(key, request.Model)
	if err != nil {
		return nil, err
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/audio/transcriptions?api-version=%s", key.AzureKeyConfig.Endpoint.GetValue(), deployment, apiVersion.GetValue())

	response, err := openai.HandleOpenAITranscriptionRequest(
		ctx,
		provider.client,
		url,
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)

	if err != nil {
		return nil, err
	}

	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.ModelDeployment = deployment

	return response, err
}

// TranscriptionStream is not supported by the Azure provider.
func (provider *AzureProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration performs an Image Generation request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the bifrost response or an error if the request fails.
func (provider *AzureProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key,
	request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	// Validate api key configs
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	deployment := key.AzureKeyConfig.Deployments[request.Model]
	if deployment == "" {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), provider.GetProviderKey())
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil || apiVersion.GetValue() == "" {
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}

	endpoint := key.AzureKeyConfig.Endpoint.GetValue()
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set", provider.GetProviderKey())
	}

	response, err := openai.HandleOpenAIImageGenerationRequest(
		ctx,
		provider.client,
		fmt.Sprintf("%s/openai/deployments/%s/images/generations?api-version=%s", endpoint, deployment, apiVersion.GetValue()),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
	if err != nil {
		return nil, err
	}

	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.ModelDeployment = deployment

	return response, err
}

// ImageGenerationStream performs a streaming image generation request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *AzureProvider) ImageGenerationStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	key schemas.Key,
	request *schemas.BifrostImageGenerationRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {

	// Validate api key configs
	if err := provider.validateKeyConfig(key); err != nil {
		return nil, err
	}

	//
	deployment := key.AzureKeyConfig.Deployments[request.Model]
	if deployment == "" {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("deployment not found for model %s", request.Model), provider.GetProviderKey())
	}

	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil || apiVersion.GetValue() == "" {
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}

	endpoint := key.AzureKeyConfig.Endpoint.GetValue()
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set", provider.GetProviderKey())
	}

	postResponseConverter := func(resp *schemas.BifrostImageGenerationStreamResponse) *schemas.BifrostImageGenerationStreamResponse {
		if resp != nil {
			resp.ExtraFields.ModelDeployment = deployment
		}
		return resp
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/images/generations?api-version=%s", endpoint, deployment, apiVersion.GetValue())

	authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
	if err != nil {
		return nil, err
	}

	// Azure is OpenAI-compatible
	return openai.HandleOpenAIImageGenerationStreaming(
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

// validateKeyConfig validates the key configuration.
// It checks if the key config is set, the endpoint is set, and the deployments are set.
// Returns an error if any of the checks fail.
func (provider *AzureProvider) validateKeyConfig(key schemas.Key) *schemas.BifrostError {
	if key.AzureKeyConfig == nil {
		return providerUtils.NewConfigurationError("azure key config not set", provider.GetProviderKey())
	}

	if key.AzureKeyConfig.Endpoint.GetValue() == "" {
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
	if key.AzureKeyConfig.Endpoint.GetValue() == "" {
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
func (provider *AzureProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
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
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
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
	baseURL := fmt.Sprintf("%s/openai/files", key.AzureKeyConfig.Endpoint.GetValue())
	values := url.Values{}
	values.Set("api-version", apiVersion.GetValue())
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())

	// Set Azure authentication
	if err := provider.setAzureAuth(ctx, req, key); err != nil {
		return nil, err
	}

	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
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

// FileList lists files from all provided Azure keys and aggregates results.
// FileList lists files using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *AzureProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for file list operation", providerName)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Initialize serial pagination helper
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err, providerName)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostFileListResponse{
			Object:  "list",
			Data:    []schemas.FileObject{},
			HasMore: false,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileListRequest,
				Provider:    providerName,
			},
		}, nil
	}

	// Validate key config
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	requestURL := fmt.Sprintf("%s/openai/files", key.AzureKeyConfig.Endpoint.GetValue())
	values := url.Values{}
	values.Set("api-version", apiVersion.GetValue())
	if request.Purpose != "" {
		values.Set("purpose", string(request.Purpose))
	}
	// Use native cursor from serial helper
	if nativeCursor != "" {
		values.Set("after", nativeCursor)
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
	if err := provider.setAzureAuth(ctx, req, key); err != nil {
		return nil, err
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, openai.ParseOpenAIError(resp, schemas.FileListRequest, providerName, "")
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr, providerName)
	}

	var openAIResp openai.OpenAIFileListResponse
	_, _, bifrostErr = providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert files to Bifrost format
	files := make([]schemas.FileObject, 0, len(openAIResp.Data))
	var lastFileID string
	for _, file := range openAIResp.Data {
		files = append(files, schemas.FileObject{
			ID:            file.ID,
			Object:        file.Object,
			Bytes:         file.Bytes,
			CreatedAt:     file.CreatedAt,
			Filename:      file.Filename,
			Purpose:       schemas.FilePurpose(file.Purpose),
			Status:        openai.ToBifrostFileStatus(file.Status),
			StatusDetails: file.StatusDetails,
		})
		lastFileID = file.ID
	}

	// Build cursor for next request
	nextCursor, hasMore := helper.BuildNextCursor(openAIResp.HasMore, lastFileID)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		Data:    files,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.After = &nextCursor
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from Azure OpenAI by trying each key until found.
func (provider *AzureProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if err := provider.validateKeyConfigForFiles(key); err != nil {
			lastErr = err
			continue
		}

		// Get API version
		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
		}

		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL with query params
		baseURL := fmt.Sprintf("%s/openai/files/%s", key.AzureKeyConfig.Endpoint.GetValue(), url.PathEscape(request.FileID))
		values := url.Values{}
		values.Set("api-version", apiVersion.GetValue())
		requestURL := baseURL + "?" + values.Encode()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(requestURL)
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		// Set Azure authentication
		if authErr := provider.setAzureAuth(ctx, req, key); authErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = authErr
			continue
		}

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = openai.ParseOpenAIError(resp, schemas.FileRetrieveRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		var openAIResp openai.OpenAIFileResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		return openAIResp.ToBifrostFileRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
	}

	return nil, lastErr
}

// FileDelete deletes a file from Azure OpenAI by trying each key until successful.
func (provider *AzureProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for file delete operation", providerName)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if err := provider.validateKeyConfigForFiles(key); err != nil {
			lastErr = err
			continue
		}

		// Get API version
		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
		}

		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL with query params
		baseURL := fmt.Sprintf("%s/openai/files/%s", key.AzureKeyConfig.Endpoint.GetValue(), url.PathEscape(request.FileID))
		values := url.Values{}
		values.Set("api-version", apiVersion.GetValue())
		requestURL := baseURL + "?" + values.Encode()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(requestURL)
		req.Header.SetMethod(http.MethodDelete)
		req.Header.SetContentType("application/json")

		// Set Azure authentication
		if authErr := provider.setAzureAuth(ctx, req, key); authErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = authErr
			continue
		}

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusNoContent {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = openai.ParseOpenAIError(resp, schemas.FileDeleteRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		if resp.StatusCode() == fasthttp.StatusNoContent {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
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
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		var openAIResp openai.OpenAIFileDeleteResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

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

	return nil, lastErr
}

// FileContent downloads file content from Azure OpenAI by trying each key until found.
func (provider *AzureProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for file content operation", providerName)
	}

	var lastErr *schemas.BifrostError

	for _, key := range keys {
		if err := provider.validateKeyConfigForFiles(key); err != nil {
			lastErr = err
			continue
		}

		// Get API version
		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
		}

		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL with query params
		baseURL := fmt.Sprintf("%s/openai/files/%s/content", key.AzureKeyConfig.Endpoint.GetValue(), url.PathEscape(request.FileID))
		values := url.Values{}
		values.Set("api-version", apiVersion.GetValue())
		requestURL := baseURL + "?" + values.Encode()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(requestURL)
		req.Header.SetMethod(http.MethodGet)

		// Set Azure authentication
		if authErr := provider.setAzureAuth(ctx, req, key); authErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = authErr
			continue
		}

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = openai.ParseOpenAIError(resp, schemas.FileContentRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		// Get content type from response
		contentType := string(resp.Header.ContentType())
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		content := append([]byte(nil), body...)

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

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

	return nil, lastErr
}

// BatchCreate creates a new batch job on Azure OpenAI.
// Azure Batch API uses the same format as OpenAI but with Azure-specific URL patterns.
func (provider *AzureProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
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
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/batches", key.AzureKeyConfig.Endpoint.GetValue())
	values := url.Values{}
	values.Set("api-version", apiVersion.GetValue())
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	if err := provider.setAzureAuth(ctx, req, key); err != nil {
		return nil, err
	}

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
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, openai.ParseOpenAIError(resp, schemas.BatchCreateRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	var openAIResp openai.OpenAIBatchResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	return openAIResp.ToBifrostBatchCreateResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// BatchList lists batch jobs from all provided Azure keys and aggregates results.
// BatchList lists batch jobs using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *AzureProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for batch list operation", providerName)
	}

	// Initialize serial pagination helper
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err, providerName)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostBatchListResponse{
			Object:  "list",
			Data:    []schemas.BifrostBatchRetrieveResponse{},
			HasMore: false,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchListRequest,
				Provider:    providerName,
			},
		}, nil
	}

	// Validate key config
	if err := provider.validateKeyConfigForFiles(key); err != nil {
		return nil, err
	}

	// Get API version
	apiVersion := key.AzureKeyConfig.APIVersion
	if apiVersion == nil {
		apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/batches", key.AzureKeyConfig.Endpoint.GetValue())
	values := url.Values{}
	values.Set("api-version", apiVersion.GetValue())
	if request.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper
	if nativeCursor != "" {
		values.Set("after", nativeCursor)
	}
	requestURL := baseURL + "?" + values.Encode()

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	// Set Azure authentication
	if err := provider.setAzureAuth(ctx, req, key); err != nil {
		return nil, err
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, openai.ParseOpenAIError(resp, schemas.BatchListRequest, providerName, "")
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr, providerName)
	}

	var openAIResp openai.OpenAIBatchListResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert batches to Bifrost format
	batches := make([]schemas.BifrostBatchRetrieveResponse, 0, len(openAIResp.Data))
	var lastBatchID string
	for _, batch := range openAIResp.Data {
		batches = append(batches, *batch.ToBifrostBatchRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse))
		lastBatchID = batch.ID
	}

	// Build cursor for next request
	nextCursor, hasMore := helper.BuildNextCursor(openAIResp.HasMore, lastBatchID)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostBatchListResponse{
		Object:  "list",
		Data:    batches,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.NextCursor = &nextCursor
	}

	return bifrostResp, nil
}

// BatchRetrieve retrieves a specific batch job from Azure OpenAI by trying each key until found.
func (provider *AzureProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for batch retrieve operation", providerName)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if err := provider.validateKeyConfigForFiles(key); err != nil {
			lastErr = err
			continue
		}

		// Get API version
		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
		}

		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL with query params
		baseURL := fmt.Sprintf("%s/openai/batches/%s", key.AzureKeyConfig.Endpoint.GetValue(), url.PathEscape(request.BatchID))
		values := url.Values{}
		values.Set("api-version", apiVersion.GetValue())
		requestURL := baseURL + "?" + values.Encode()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(requestURL)
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		// Set Azure authentication
		if authErr := provider.setAzureAuth(ctx, req, key); authErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = authErr
			continue
		}

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = openai.ParseOpenAIError(resp, schemas.BatchRetrieveRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		var openAIResp openai.OpenAIBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := openAIResp.ToBifrostBatchRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse)
		result.ExtraFields.RequestType = schemas.BatchRetrieveRequest
		return result, nil
	}

	return nil, lastErr
}

// BatchCancel cancels a batch job on Azure OpenAI by trying each key until successful.
func (provider *AzureProvider) BatchCancel(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for batch cancel operation", providerName)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if err := provider.validateKeyConfigForFiles(key); err != nil {
			lastErr = err
			continue
		}

		// Get API version
		apiVersion := key.AzureKeyConfig.APIVersion
		if apiVersion == nil {
			apiVersion = schemas.NewEnvVar(AzureAPIVersionDefault)
		}

		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL with query params
		baseURL := fmt.Sprintf("%s/openai/batches/%s/cancel", key.AzureKeyConfig.Endpoint.GetValue(), url.PathEscape(request.BatchID))
		values := url.Values{}
		values.Set("api-version", apiVersion.GetValue())
		requestURL := baseURL + "?" + values.Encode()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(requestURL)
		req.Header.SetMethod(http.MethodPost)
		req.Header.SetContentType("application/json")

		// Set Azure authentication
		if authErr := provider.setAzureAuth(ctx, req, key); authErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = authErr
			continue
		}

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = openai.ParseOpenAIError(resp, schemas.BatchCancelRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		var openAIResp openai.OpenAIBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

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

	return nil, lastErr
}

// BatchResults retrieves batch results from Azure OpenAI by trying each key until successful.
// For Azure (like OpenAI), batch results are obtained by downloading the output_file_id.
func (provider *AzureProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// First, retrieve the batch to get the output_file_id (using all keys)
	batchResp, bifrostErr := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  request.BatchID,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if batchResp.OutputFileID == nil || *batchResp.OutputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch results not available: output_file_id is empty (batch may not be completed)", nil, providerName)
	}

	// Download the output file content (using all keys)
	fileContentResp, bifrostErr := provider.FileContent(ctx, keys, &schemas.BifrostFileContentRequest{
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
			provider.logger.Warn("failed to parse batch result line: %v", err)
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

// CountTokens is not supported by the Azure provider.
func (provider *AzureProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Azure provider.
func (provider *AzureProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Azure provider.
func (provider *AzureProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Azure provider.
func (provider *AzureProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Azure provider.
func (provider *AzureProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Azure provider.
func (provider *AzureProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Azure provider.
func (provider *AzureProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Azure provider.
func (provider *AzureProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Azure provider.
func (provider *AzureProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Azure provider.
func (provider *AzureProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}
