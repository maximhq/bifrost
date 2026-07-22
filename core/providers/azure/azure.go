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
	"strings"
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

// DefaultAzureSorageScope is the default scope for Azure storage.
const DefaultAzureStorageScope = "https://storage.azure.com/.default"

// AzureProvider implements the Provider interface for Azure's API.
type AzureProvider struct {
	logger          schemas.Logger        // Logger for provider operations
	client          *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig   schemas.NetworkConfig // Network configuration including extra headers

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

// getOrCreateDefaultAzureCredential returns a DefaultAzureCredential, creating and caching it if needed.
// It automatically detects the auth environment: managed identity on Azure VMs/containers,
// workload identity in AKS, environment variables, Azure CLI, and more.
func (p *AzureProvider) getOrCreateDefaultAzureCredential() (azcore.TokenCredential, error) {
	const cacheKey = "default_azure_credential"

	if val, ok := p.credentials.Load(cacheKey); ok {
		return val.(azcore.TokenCredential), nil
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, err
	}

	actual, _ := p.credentials.LoadOrStore(cacheKey, cred)
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
			return nil, providerUtils.NewBifrostOperationError("failed to get or create Azure authentication", err)
		}

		scopes := getAzureScopes(key.AzureKeyConfig.Scopes)

		token, err := cred.GetToken(ctx, policy.TokenRequestOptions{
			Scopes: scopes,
		})
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to get Azure access token", err)
		}

		if token.Token == "" {
			return nil, providerUtils.NewBifrostOperationError("Azure access token is empty", errors.New("token is empty"))
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
		// No explicit credentials provided - attempt DefaultAzureCredential auto-detection.
		// This covers managed identity on Azure VMs/containers, workload identity in AKS,
		// environment variables, Azure CLI, and more - with no config required.
		scopes := getAzureScopes(nil)
		if key.AzureKeyConfig != nil {
			scopes = getAzureScopes(key.AzureKeyConfig.Scopes)
		}

		cred, err := provider.getOrCreateDefaultAzureCredential()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("no credentials provided and DefaultAzureCredential unavailable", err)
		}

		token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: scopes})
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("no credentials provided and DefaultAzureCredential failed to get token", err)
		}

		if token.Token == "" {
			return nil, providerUtils.NewBifrostOperationError("no credentials provided and DefaultAzureCredential returned empty token", errors.New("token is empty"))
		}

		authHeader["Authorization"] = fmt.Sprintf("Bearer %s", token.Token)
		return authHeader, nil
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

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	// Configure proxy and retry policy
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)
	return &AzureProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Azure.
func (provider *AzureProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Azure
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *AzureProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	// Create the request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(endpoint + providerUtils.GetPathFromContext(ctx, "/openai/v1/models"))
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
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Store provider response headers in context before status check so error responses also forward them
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(openai.ParseOpenAIError(resp), latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
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
	response := azureResponse.ToBifrostListModelsResponse(key.Models, key.BlacklistedModels, key.Aliases, request.Unfiltered)
	if response == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert Azure model list response", nil)
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
	)
}

// TextCompletion performs a text completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAITextCompletionRequest(
		ctx,
		provider.client,
		fmt.Sprintf("%s/openai/v1/completions", endpoint),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		nil,
		nil,
		provider.logger,
	)
}

// TextCompletionStream performs a streaming text completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *AzureProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	url := fmt.Sprintf("%s/openai/v1/completions", endpoint)

	// Get Azure authentication headers
	authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
	if err != nil {
		return nil, err
	}

	return openai.HandleOpenAITextCompletionStreaming(
		ctx,
		provider.streamingClient,
		url,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		postHookRunner,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// ChatCompletion performs a chat completion request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		// Anthropic-family models use the native Anthropic Messages endpoint via the shared handler.
		authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, true)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		authHeader["anthropic-version"] = resolveAnthropicVersion(ctx)
		return anthropic.HandleAnthropicChatCompletionRequest(
			ctx,
			provider.client,
			fmt.Sprintf("%s/anthropic/v1/messages", endpoint),
			request,
			anthropic.AnthropicRequestBuildConfig{
				Provider:                  schemas.Azure,
				Model:                     request.Model,
				IsStreaming:               false,
				BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
				ShouldSendBackRawRequest:  provider.sendBackRawRequest,
				ShouldSendBackRawResponse: provider.sendBackRawResponse,
			},
			authHeader,
			provider.networkConfig.ExtraHeaders,
			nil,
			provider.logger,
		)
	}

	// OpenAI-family models use the OpenAI-compatible Azure endpoint via the shared handler.
	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		fmt.Sprintf("%s/openai/v1/chat/completions", endpoint),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to Azure's API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Azure-specific URL construction with deployments and supports both api-key and Bearer token authentication.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *AzureProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	var url string
	if schemas.ResolveFamily(ctx, request.Model) == schemas.ModelFamilyAnthropic {
		authHeader, err := provider.getAzureAuthHeaders(ctx, key, true)
		if err != nil {
			return nil, err
		}
		authHeader["anthropic-version"] = resolveAnthropicVersion(ctx)
		url = fmt.Sprintf("%s/anthropic/v1/messages", endpoint)

		jsonData, err := anthropic.BuildAnthropicChatRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
			Provider:                  schemas.Azure,
			Model:                     request.Model,
			IsStreaming:               true,
			ShouldSendBackRawRequest:  provider.sendBackRawRequest,
			ShouldSendBackRawResponse: provider.sendBackRawResponse,
		})
		if err != nil {
			return nil, err
		}

		// Use shared streaming logic from Anthropic
		return anthropic.HandleAnthropicChatCompletionStreaming(
			ctx,
			provider.streamingClient,
			url,
			jsonData,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			provider.networkConfig.BetaHeaderOverrides,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			nil,
			nil,
			provider.logger,
			postHookSpanFinalizer,
		)
	} else {
		authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
		if err != nil {
			return nil, err
		}
		url = fmt.Sprintf("%s/openai/v1/chat/completions", endpoint)

		// Use shared streaming logic from OpenAI
		return openai.HandleOpenAIChatCompletionStreaming(
			ctx,
			provider.streamingClient,
			url,
			request,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			provider.logger,
			postHookSpanFinalizer,
		)
	}
}

// Responses performs a responses request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AzureProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		// Anthropic-family models use the native Anthropic Messages endpoint via the shared handler.
		authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, true)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		authHeader["anthropic-version"] = resolveAnthropicVersion(ctx)
		return anthropic.HandleAnthropicResponsesRequest(
			ctx,
			provider.client,
			fmt.Sprintf("%s/anthropic/v1/messages", endpoint),
			request,
			anthropic.AnthropicRequestBuildConfig{
				Provider:                  schemas.Azure,
				Model:                     request.Model,
				IsStreaming:               false,
				ValidateTools:             true,
				BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
				ShouldSendBackRawRequest:  provider.sendBackRawRequest,
				ShouldSendBackRawResponse: provider.sendBackRawResponse,
			},
			authHeader,
			provider.networkConfig.ExtraHeaders,
			nil,
			provider.logger,
		)
	}

	// OpenAI-family models use the OpenAI-compatible Azure endpoint via the shared handler.
	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	path := "openai/v1/responses"
	if v := resolveAPIVersion(ctx, ""); v != "" {
		path += "?api-version=" + v
	}
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.client,
		fmt.Sprintf("%s/%s", endpoint, path),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// ResponsesStream performs a streaming responses request to Azure's API.
func (provider *AzureProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	var url string
	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		authHeader, err := provider.getAzureAuthHeaders(ctx, key, true)
		if err != nil {
			return nil, err
		}
		authHeader["anthropic-version"] = resolveAnthropicVersion(ctx)
		url = fmt.Sprintf("%s/anthropic/v1/messages", endpoint)

		jsonData, bifrostErr := anthropic.BuildAnthropicResponsesRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
			Provider:                  schemas.Azure,
			Model:                     request.Model,
			IsStreaming:               true,
			ValidateTools:             true,
			ShouldSendBackRawRequest:  provider.sendBackRawRequest,
			ShouldSendBackRawResponse: provider.sendBackRawResponse,
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		// Use shared streaming logic from Anthropic
		return anthropic.HandleAnthropicResponsesStream(
			ctx,
			provider.streamingClient,
			url,
			jsonData,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			provider.networkConfig.BetaHeaderOverrides,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			nil,
			nil,
			provider.logger,
			postHookSpanFinalizer,
		)
	} else {
		authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
		if err != nil {
			return nil, err
		}
		path := "openai/v1/responses"
		if v := resolveAPIVersion(ctx, ""); v != "" {
			path += "?api-version=" + v
		}
		url = fmt.Sprintf("%s/%s", endpoint, path)

		// Use shared streaming logic from OpenAI
		return openai.HandleOpenAIResponsesStreaming(
			ctx,
			provider.streamingClient,
			url,
			request,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			nil,
			nil,
			nil,
			nil,
			nil,
			provider.logger,
			postHookSpanFinalizer,
		)
	}
}

// Embedding generates embeddings for the given input text(s) using Azure.
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *AzureProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		fmt.Sprintf("%s/openai/v1/embeddings", endpoint),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		nil,
		provider.logger,
	)
}

// Speech is not supported by the Azure provider.
func (provider *AzureProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	url := fmt.Sprintf("%s/openai/v1/audio/speech", endpoint)

	response, err := openai.HandleOpenAISpeechRequest(
		ctx,
		provider.client,
		url,
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		authHeader,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		nil,
		provider.logger,
	)

	if err != nil {
		return nil, err
	}

	return response, err
}

// Rerank is not supported by the Azure provider.
func (provider *AzureProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// OCR is not supported by the Azure provider.
func (provider *AzureProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// SpeechStream handles streaming for speech synthesis with Azure.
// Azure sends raw binary audio bytes in SSE format, unlike OpenAI which sends JSON.
func (provider *AzureProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	// Get Azure authentication headers
	authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/openai/v1/audio/speech", endpoint)

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
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			reqBody := openai.ToOpenAISpeechRequest(request)
			if reqBody != nil {
				reqBody.StreamFormat = schemas.Ptr("sse")
			}
			return reqBody, nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.OpenAI) {
		req.SetBody(jsonBody)
	}

	startTime := time.Now()
	// Make the request
	requestErr := provider.client.Do(req, resp)
	latency := time.Since(startTime)
	if requestErr != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(requestErr, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   requestErr,
				},
			}, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		if errors.Is(requestErr, fasthttp.ErrTimeout) || errors.Is(requestErr, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, requestErr), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		// Request failed before the first response byte (server closed an idle/pooled connection,
		// broken pipe, connection refused, DNS failure, etc.). Surface as a retriable upstream
		// connection error (502) so executeRequestWithRetries honors max_retries, matching the
		// non-streaming path - see https://github.com/maximhq/bifrost/issues/4496.
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, requestErr), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Extract provider response headers before status check so error responses also forward them
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, openai.ParseOpenAIError(resp), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	// Start streaming in a goroutine
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonBody)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		// Always release response on exit; bodyStream close should prevent indefinite blocking.
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)

		reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()

		// Wrap reader with idle timeout to detect stalled streams (e.g., Azure TPM throttling
		// that stops sending data but keeps the TCP connection open).
		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		// Setup cancellation handler to close the raw network stream on ctx cancellation,
		// which immediately unblocks any in-progress read (including reads blocked inside a gzip decompression layer).
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()

		chunkIndex := -1
		lastChunkTime := startTime

		// Read SSE events manually to handle binary data with embedded newlines
		// SSE format: "data: <content>\n\n" - events are separated by double newlines
		// We can't use bufio.Scanner because MP3 data contains 0x0a bytes which get interpreted as newlines
		readBuffer := make([]byte, 64*1024) // 64KB read chunks
		var accumulated []byte
		doneReceived := false

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			// Read from stream
			n, readErr := reader.Read(readBuffer)
			if n > 0 {
				accumulated = append(accumulated, readBuffer[:n]...)

				// Process complete SSE events (separated by \r\n\r\n or \n\n)
				for {
					// Find the next double-newline separator (try CRLF first, then LF)
					var idx int
					var separatorLen int
					idx = bytes.Index(accumulated, []byte("\r\n\r\n"))
					if idx != -1 {
						separatorLen = 4 // \r\n\r\n
					} else {
						idx = bytes.Index(accumulated, []byte("\n\n"))
						if idx != -1 {
							separatorLen = 2 // \n\n
						}
					}
					if idx == -1 {
						// No complete event yet, need more data
						break
					}

					// Extract the event (everything up to the separator)
					event := accumulated[:idx]
					accumulated = accumulated[idx+separatorLen:] // Skip the separator

					// Skip empty events and comments
					if len(event) == 0 || bytes.HasPrefix(event, []byte(":")) {
						continue
					}

					// Parse the SSE event
					var audioData []byte

					// Check if this has "data: " prefix (standard SSE format)
					if bytes.HasPrefix(event, []byte("data: ")) {
						audioData = event[6:] // Skip "data: " prefix
						// Check for [DONE] marker - break out of loops to send final response
						if bytes.Equal(audioData, []byte("[DONE]")) {
							doneReceived = true
							break
						}
					} else {
						// Raw data without prefix (shouldn't happen with Azure, but handle it)
						audioData = event
					}

					// Skip empty data
					if len(audioData) == 0 {
						continue
					}

					// Azure sends JSON-wrapped responses for speech streaming
					// Parse the JSON to extract the response type and audio data
					var response schemas.BifrostSpeechStreamResponse
					if err := sonic.Unmarshal(audioData, &response); err != nil {
						// If JSON parsing fails, check if this might be an error response
						// Quick check for error field (allocation-free using sonic.Get)
						if errorNode, _ := sonic.Get(audioData, "error"); errorNode.Exists() {
							// Only unmarshal when we know there's an error
							var bifrostErr schemas.BifrostError
							if errParseErr := sonic.Unmarshal(audioData, &bifrostErr); errParseErr == nil {
								if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
									ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
									providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, provider.logger, postHookSpanFinalizer)
									return
								}
							}
						}
						// If it's not valid JSON, log and skip
						provider.logger.Warn("failed to parse speech stream response: %v", err)
						continue
					}

					// Check for completion event - skip if no audio data
					if response.Type == schemas.SpeechStreamResponseTypeDone || len(response.Audio) == 0 {
						// This is a control event or empty response - skip
						continue
					}

					chunkIndex++

					// Set extra fields for the response
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						ChunkIndex: chunkIndex,
						Latency:    time.Since(lastChunkTime).Milliseconds(),
					}
					lastChunkTime = time.Now()

					if sendBackRawResponse {
						response.ExtraFields.RawResponse = audioData
					}

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, &response, nil, nil), responseChan, postHookSpanFinalizer)
				}

				// Check if we received [DONE] marker - break outer loop to send final response
				if doneReceived {
					break
				}
			}

			// Handle read errors
			if readErr != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				if readErr != io.EOF {
					// Non-EOF errors (e.g., connection reset by peer due to TPM throttling)
					// must be reported to the client instead of falling through to send
					// a fake "done" response with truncated audio.
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					provider.logger.Warn("Error reading stream: %v", readErr)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, provider.logger, postHookSpanFinalizer)
					return
				}
				break
			}
		}

		// Send final "done" response only if we received the [DONE] marker from the provider.
		// Without [DONE], the stream ended abnormally (e.g., clean EOF without proper SSE termination).
		if chunkIndex >= 0 && doneReceived {
			finalResponse := schemas.BifrostSpeechStreamResponse{
				Type: schemas.SpeechStreamResponseTypeDone,
				ExtraFields: schemas.BifrostResponseExtraFields{
					ChunkIndex: chunkIndex + 1,
					Latency:    time.Since(startTime).Milliseconds(),
				},
			}

			if sendBackRawRequest {
				providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, jsonBody)
			}

			finalResponse.BackfillParams(request)
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, &finalResponse, nil, nil), responseChan, postHookSpanFinalizer)
		} else if chunkIndex >= 0 && !doneReceived {
			provider.logger.Warn("Stream ended without receiving [DONE] marker after %d chunks", chunkIndex+1)
		}

		// Response is released via deferred ReleaseStreamingResponse(resp) above.
	}()

	return responseChan, nil
}

// Transcription is not supported by the Azure provider.
func (provider *AzureProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	url := fmt.Sprintf("%s/openai/deployments/%s/audio/transcriptions?api-version=%s", endpoint, request.Model, resolveAPIVersion(ctx, DefaultAzureAPIVersion))

	response, err := openai.HandleOpenAITranscriptionRequest(
		ctx,
		provider.client,
		url,
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		authHeader,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		nil,
		provider.logger,
	)

	if err != nil {
		return nil, err
	}

	return response, err
}

// TranscriptionStream is not supported by the Azure provider.
func (provider *AzureProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration performs an Image Generation request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a BifrostResponse containing the bifrost response or an error if the request fails.
func (provider *AzureProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key,
	request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response, err := openai.HandleOpenAIImageGenerationRequest(
		ctx,
		provider.client,
		fmt.Sprintf("%s/openai/v1/images/generations", endpoint),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		authHeader,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
	if err != nil {
		return nil, err
	}

	return response, err
}

// ImageGenerationStream performs a streaming image generation request to Azure's API.
// It formats the request, sends it to Azure, and processes the response.
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *AzureProvider) ImageGenerationStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostImageGenerationRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	url := fmt.Sprintf("%s/openai/v1/images/generations", endpoint)

	authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
	if err != nil {
		return nil, err
	}

	// Azure is OpenAI-compatible
	return openai.HandleOpenAIImageGenerationStreaming(
		ctx,
		provider.streamingClient,
		url,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)

}

// ImageEdit performs an image edit request to Azure's API.
func (provider *AzureProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	url := fmt.Sprintf("%s/openai/v1/images/edits", endpoint)
	response, err := openai.HandleOpenAIImageEditRequest(
		ctx,
		provider.client,
		url,
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		authHeader,
		false,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		provider.logger,
	)
	if err != nil {
		return nil, err
	}

	return response, err
}

// ImageEditStream performs a streaming image edit request to Azure's API.
func (provider *AzureProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	url := fmt.Sprintf("%s/openai/v1/images/edits", endpoint)

	authHeader, err := provider.getAzureAuthHeaders(ctx, key, false)
	if err != nil {
		return nil, err
	}

	// Azure is OpenAI-compatible
	return openai.HandleOpenAIImageEditStreamRequest(
		ctx,
		provider.streamingClient,
		url,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		false,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)

}

// ImageVariation is not supported by the Azure provider.
func (provider *AzureProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoGeneration creates a video using Azure's OpenAI-compatible Sora API.
// This delegates to the OpenAI handler with Azure-specific URL and authentication.
func (provider *AzureProvider) VideoGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Build Azure URL for OpenAI-compatible video generation endpoint
	url := fmt.Sprintf("%s/openai/v1/videos", endpoint)

	response, bifrostErr := openai.HandleOpenAIVideoGenerationRequest(
		ctx,
		provider.client,
		url,
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		authHeader,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return response, nil
}

// VideoRetrieve retrieves the status of a video from Azure's OpenAI-compatible API.
func (provider *AzureProvider) VideoRetrieve(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	if request.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}
	videoID := providerUtils.StripVideoIDProviderSuffix(request.ID, providerName)

	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openai.HandleOpenAIVideoRetrieveRequest(
		ctx,
		provider.client,
		fmt.Sprintf("%s/openai/v1/videos/%s", endpoint, videoID),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		authHeaders,
		providerName,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.VideoDownload,
		provider.logger,
	)
}

// VideoDownload downloads video content from Azure's OpenAI-compatible API.
func (provider *AzureProvider) VideoDownload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}
	videoID := providerUtils.StripVideoIDProviderSuffix(request.ID, providerName)

	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Build Azure URL
	url := fmt.Sprintf("%s/openai/v1/videos/%s/content", endpoint, videoID)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)

	// Get authentication headers
	authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	for k, v := range authHeaders {
		req.Header.Set(k, v)
	}

	// Make request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(openai.ParseOpenAIError(resp), latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	// Get content type from response
	contentType := string(resp.Header.ContentType())
	if contentType == "" {
		// Default to video/mp4 if not specified
		contentType = "video/mp4"
	}

	// Create response
	response := &schemas.BifrostVideoDownloadResponse{
		VideoID:     request.ID,
		Content:     append([]byte(nil), body...),
		ContentType: contentType,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}

	return response, nil
}

// VideoDelete deletes a video from Azure's OpenAI-compatible API.
func (provider *AzureProvider) VideoDelete(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}
	videoID := providerUtils.StripVideoIDProviderSuffix(request.ID, providerName)

	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Build Azure URL
	url := fmt.Sprintf("%s/openai/v1/videos/%s", endpoint, videoID)

	response, bifrostErr := openai.HandleOpenAIVideoDeleteRequest(
		ctx,
		provider.client,
		url,
		videoID,
		key,
		provider.networkConfig.ExtraHeaders,
		authHeader,
		providerName,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return response, nil
}

// VideoList lists videos from Azure's OpenAI-compatible API.
func (provider *AzureProvider) VideoList(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Build Azure URL
	baseURL := fmt.Sprintf("%s/openai/v1/videos", endpoint)

	response, bifrostErr := openai.HandleOpenAIVideoListRequest(
		ctx,
		provider.client,
		baseURL,
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		authHeader,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return response, nil
}

// VideoRemix is not supported by Azure provider.
func (provider *AzureProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// FileUpload uploads a file to Azure OpenAI.
func (provider *AzureProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil)
	}

	if request.Purpose == "" {
		return nil, providerUtils.NewBifrostOperationError("purpose is required", nil)
	}

	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add purpose field
	if err := writer.WriteField("purpose", string(request.Purpose)); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write purpose field", err)
	}

	// Add file field
	filename := request.Filename
	if filename == "" {
		filename = "file.jsonl"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create form file", err)
	}
	if _, err := part.Write(request.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file content", err)
	}

	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to close multipart writer", err)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL
	requestURL := fmt.Sprintf("%s/openai/v1/files", endpoint)

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
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		return nil, providerUtils.SetErrorLatency(openai.ParseOpenAIError(resp), latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	var openAIResp openai.OpenAIFileResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostFileUploadResponse(latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// FileList lists files from all provided Azure keys and aggregates results.
// FileList lists files using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *AzureProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for file list operation")
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Initialize serial pagination helper
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger, true)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostFileListResponse{
			Object:  "list",
			Data:    []schemas.FileObject{},
			HasMore: false,
		}, nil
	}

	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	requestURL := fmt.Sprintf("%s/openai/v1/files", endpoint)
	values := url.Values{}
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
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(openai.ParseOpenAIError(resp), latency)
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr)
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
			Latency: latency.Milliseconds(),
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
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		endpoint := resolveAzureEndpoint(ctx, key)
		if endpoint == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL
		requestURL := fmt.Sprintf("%s/openai/v1/files/%s", endpoint, url.PathEscape(request.FileID))

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
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			lastErr = openai.ParseOpenAIError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		var openAIResp openai.OpenAIFileResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		return openAIResp.ToBifrostFileRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
	}

	return nil, lastErr
}

// FileDelete deletes a file from Azure OpenAI by trying each key until successful.
func (provider *AzureProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil)
	}

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for file delete operation")
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		endpoint := resolveAzureEndpoint(ctx, key)
		if endpoint == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL
		requestURL := fmt.Sprintf("%s/openai/v1/files/%s", endpoint, url.PathEscape(request.FileID))

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
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusNoContent {
			lastErr = openai.ParseOpenAIError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		if resp.StatusCode() == fasthttp.StatusNoContent {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return &schemas.BifrostFileDeleteResponse{
				ID:      request.FileID,
				Object:  "file",
				Deleted: true,
				ExtraFields: schemas.BifrostResponseExtraFields{
					Latency: latency.Milliseconds(),
				},
			}, nil
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		var openAIResp openai.OpenAIFileDeleteResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := &schemas.BifrostFileDeleteResponse{
			ID:      openAIResp.ID,
			Object:  openAIResp.Object,
			Deleted: openAIResp.Deleted,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
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
	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil)
	}

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for file content operation")
	}

	var lastErr *schemas.BifrostError

	for _, key := range keys {
		endpoint := resolveAzureEndpoint(ctx, key)
		if endpoint == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL
		requestURL := fmt.Sprintf("%s/openai/v1/files/%s/content", endpoint, url.PathEscape(request.FileID))

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
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			lastErr = openai.ParseOpenAIError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		// Get content type from response
		contentType := string(resp.Header.ContentType())
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		content := append([]byte(nil), body...)

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		return &schemas.BifrostFileContentResponse{
			FileID:      request.FileID,
			Content:     content,
			ContentType: contentType,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// BatchCreate creates a new batch job on Azure OpenAI.
// Azure Batch API uses the same format as OpenAI but with Azure-specific URL patterns.
func (provider *AzureProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	inputFileID := request.InputFileID

	// If no file_id provided but inline requests are available, upload them first
	if inputFileID == "" && len(request.Requests) > 0 {
		// Convert inline requests to JSONL format
		jsonlData, err := openai.ConvertRequestsToJSONL(request.Requests)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to convert requests to JSONL", err)
		}

		// Upload the file with purpose "batch"
		uploadResp, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
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
	if inputFileID == "" && request.InputBlob == nil {
		return nil, providerUtils.NewBifrostOperationError("either input_file_id, input_blob, or requests array is required for Azure batch API", nil)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL
	requestURL := fmt.Sprintf("%s/openai/v1/batches", endpoint)

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
		Endpoint:         string(request.Endpoint),
		CompletionWindow: request.CompletionWindow,
		Metadata:         request.Metadata,
	}

	// Azure requires either input_file_id OR (input_blob + output_folder), not both.
	if inputFileID != "" {
		openAIReq.InputFileID = schemas.Ptr(inputFileID)
	} else {
		if request.InputBlob != nil {
			openAIReq.InputBlob = request.InputBlob
		}
		if request.OutputFolder != nil {
			openAIReq.OutputFolder = request.OutputFolder
		}
	}

	// Set default completion window if not provided
	if openAIReq.CompletionWindow == "" {
		openAIReq.CompletionWindow = "24h"
	}

	jsonData, err := providerUtils.MarshalSorted(openAIReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		return nil, providerUtils.EnrichError(ctx, openai.ParseOpenAIError(resp), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	var openAIResp openai.OpenAIBatchResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	return openAIResp.ToBifrostBatchCreateResponse(latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// BatchList lists batch jobs from all provided Azure keys and aggregates results.
// BatchList lists batch jobs using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *AzureProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for batch list operation")
	}

	// Initialize serial pagination helper
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger, true)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostBatchListResponse{
			Object:  "list",
			Data:    []schemas.BifrostBatchRetrieveResponse{},
			HasMore: false,
		}, nil
	}

	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := fmt.Sprintf("%s/openai/v1/batches", endpoint)
	values := url.Values{}
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
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(openai.ParseOpenAIError(resp), latency)
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr)
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
		batches = append(batches, *batch.ToBifrostBatchRetrieveResponse(latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse))
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
			Latency: latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.NextCursor = &nextCursor
	}

	return bifrostResp, nil
}

// BatchRetrieve retrieves a specific batch job from Azure OpenAI by trying each key until found.
func (provider *AzureProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil)
	}

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for batch retrieve operation")
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		endpoint := resolveAzureEndpoint(ctx, key)
		if endpoint == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL
		requestURL := fmt.Sprintf("%s/openai/v1/batches/%s", endpoint, url.PathEscape(request.BatchID))

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
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			lastErr = openai.ParseOpenAIError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		var openAIResp openai.OpenAIBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := openAIResp.ToBifrostBatchRetrieveResponse(latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse)
		return result, nil
	}

	return nil, lastErr
}

// BatchCancel cancels a batch job on Azure OpenAI by trying each key until successful.
func (provider *AzureProvider) BatchCancel(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil)
	}

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no Azure keys available for batch cancel operation")
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		endpoint := resolveAzureEndpoint(ctx, key)
		if endpoint == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Build URL
		requestURL := fmt.Sprintf("%s/openai/v1/batches/%s/cancel", endpoint, url.PathEscape(request.BatchID))

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
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			lastErr = openai.ParseOpenAIError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		var openAIResp openai.OpenAIBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := &schemas.BifrostBatchCancelResponse{
			ID:           openAIResp.ID,
			Object:       openAIResp.Object,
			Status:       openai.ToBifrostBatchStatus(openAIResp.Status),
			CancellingAt: openAIResp.CancellingAt,
			CancelledAt:  openAIResp.CancelledAt,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
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

// BatchDelete is not supported by the Azure provider.
func (provider *AzureProvider) BatchDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, schemas.Azure)
}

// getBlobStorageTokenForKey returns a Bearer token scoped to Azure Blob Storage for a single key.
func (provider *AzureProvider) getBlobStorageTokenForKey(ctx *schemas.BifrostContext, key schemas.Key) (string, *schemas.BifrostError) {
	if key.AzureKeyConfig == nil {
		return "", nil
	}
	cfg := key.AzureKeyConfig

	if cfg.ClientID != nil && cfg.ClientSecret != nil && cfg.TenantID != nil &&
		cfg.ClientID.GetValue() != "" && cfg.ClientSecret.GetValue() != "" && cfg.TenantID.GetValue() != "" {
		cred, err := provider.getOrCreateAuth(cfg.TenantID.GetValue(), cfg.ClientID.GetValue(), cfg.ClientSecret.GetValue())
		if err != nil {
			return "", providerUtils.NewProviderAPIError("failed to acquire Azure SP credentials for blob storage", err, http.StatusUnauthorized, nil, nil)
		}
		token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{DefaultAzureStorageScope}})
		if err != nil {
			return "", providerUtils.NewProviderAPIError("failed to get Azure SP token for blob storage", err, http.StatusUnauthorized, nil, nil)
		}
		if token.Token == "" {
			return "", providerUtils.NewProviderAPIError("Azure SP token for blob storage is empty", nil, http.StatusUnauthorized, nil, nil)
		}
		return token.Token, nil
	}

	// No SP credentials: try DefaultAzureCredential (managed identity, workload identity, env vars, etc.).
	// Failure is silent — ambient auth simply not available for this key.
	cred, err := provider.getOrCreateDefaultAzureCredential()
	if err != nil {
		return "", nil
	}
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{DefaultAzureStorageScope}})
	if err != nil || token.Token == "" {
		return "", nil
	}
	return token.Token, nil
}

// isTrustedAzureBlobHost returns true if the host is a recognized Azure Blob Storage domain.
func isTrustedAzureBlobHost(host string) bool {
	return strings.HasSuffix(host, ".blob.core.windows.net") ||
		strings.HasSuffix(host, ".dfs.core.windows.net")
}

// downloadBlobURL fetches the content of an Azure Blob Storage URL, trying each key's
// credentials in sequence until a download succeeds — mirroring how FileContent loops keys.
// SAS URLs (containing "sig=") are fetched in a single unauthenticated attempt since the
// token in the URL already grants access.
func (provider *AzureProvider) downloadBlobURL(ctx *schemas.BifrostContext, blobURL string, keys []schemas.Key) ([]byte, int64, *schemas.BifrostError) {
	// Validate host for all blob URLs before any outbound request
	parsed, parseErr := url.Parse(blobURL)
	if parseErr != nil || parsed.Scheme != "https" || !isTrustedAzureBlobHost(parsed.Hostname()) {
		return nil, 0, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("blob URL is not a trusted Azure Blob Storage endpoint: %s", blobURL), nil,
		)
	}

	// SAS URL: credentials are embedded
	if strings.Contains(blobURL, "sig=") {
		return provider.doGetBlob(ctx, blobURL, "")
	}

	// Plain URL: try each key's storage credentials until one succeeds.
	var lastErr *schemas.BifrostError
	for _, key := range keys {
		token, tokenErr := provider.getBlobStorageTokenForKey(ctx, key)
		if tokenErr != nil {
			lastErr = tokenErr
			continue
		}
		if token == "" {
			continue
		}
		content, latency, err := provider.doGetBlob(ctx, blobURL, token)
		if err == nil {
			return content, latency, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return nil, 0, lastErr
	}
	return nil, 0, providerUtils.NewBifrostOperationError("no Azure keys available for blob download", nil)
}

// doGetBlob performs a single GET request to a blob URL, optionally adding a Bearer token.
func (provider *AzureProvider) doGetBlob(ctx *schemas.BifrostContext, blobURL string, bearerToken string) ([]byte, int64, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(blobURL)
	req.Header.SetMethod(http.MethodGet)
	if bearerToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", bearerToken))
		req.Header.Set("x-ms-version", "2020-04-08")
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, 0, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, 0, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("blob download failed with status %d", resp.StatusCode()), nil,
		)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, 0, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	return append([]byte(nil), body...), latency.Milliseconds(), nil
}

// BatchResults retrieves batch results from Azure OpenAI.
// For file-based batches it downloads via output_file_id using the Files API.
// For blob-based batches it fetches the output_blob URL directly using Azure Storage credentials.
func (provider *AzureProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	batchResp, bifrostErr := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  request.BatchID,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var content []byte
	var latencyMs int64

	switch {
	case batchResp.OutputFileID != nil && *batchResp.OutputFileID != "":
		fileContentResp, err := provider.FileContent(ctx, keys, &schemas.BifrostFileContentRequest{
			Provider: request.Provider,
			FileID:   *batchResp.OutputFileID,
		})
		if err != nil {
			return nil, err
		}
		content = fileContentResp.Content
		latencyMs = fileContentResp.ExtraFields.Latency

	case batchResp.OutputBlob != nil && *batchResp.OutputBlob != "":
		blobContent, blobLatency, err := provider.downloadBlobURL(ctx, *batchResp.OutputBlob, keys)
		if err != nil {
			return nil, err
		}
		content = blobContent
		latencyMs = blobLatency

	default:
		return nil, providerUtils.NewBifrostOperationError("batch results not available: neither output_file_id nor output_blob is set (batch may not be completed yet)", nil)
	}

	var results []schemas.BatchResultItem
	parseResult := providerUtils.ParseJSONL(content, func(line []byte) error {
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
			Latency: latencyMs,
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

// Compaction compacts a conversation context window using Azure OpenAI's /openai/v1/responses/compact endpoint.
func (provider *AzureProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}
	authHeader, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	path := "openai/v1/responses/compact"
	if v := resolveAPIVersion(ctx, ""); v != "" {
		path += "?api-version=" + v
	}
	return openai.HandleOpenAICompactionRequest(
		ctx,
		provider.client,
		fmt.Sprintf("%s/%s", endpoint, path),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		provider.logger,
	)
}

// buildContainerURL constructs the Azure container API URL.
// Container endpoints are not per-deployment, so they use the openai/v1 prefix directly.
// ctx carries the resolved alias so per-alias Endpoint overrides are honored.
func (provider *AzureProvider) buildContainerURL(ctx *schemas.BifrostContext, key schemas.Key, path string) string {
	endpoint := strings.TrimRight(resolveAzureEndpoint(ctx, key), "/")
	return fmt.Sprintf("%s/openai/v1%s", endpoint, path)
}

// ContainerCreate creates a new container via Azure's OpenAI API.
func (provider *AzureProvider) ContainerCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil)
	}
	if request.Name == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: name is required", nil)
	}
	if resolveAzureEndpoint(ctx, key) == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	reqBody := map[string]interface{}{"name": request.Name}
	if request.ExpiresAfter != nil {
		reqBody["expires_after"] = map[string]interface{}{
			"anchor":  request.ExpiresAfter.Anchor,
			"minutes": request.ExpiresAfter.Minutes,
		}
	}
	if len(request.FileIDs) > 0 {
		reqBody["file_ids"] = request.FileIDs
	}
	if request.MemoryLimit != "" {
		reqBody["memory_limit"] = request.MemoryLimit
	}
	if len(request.Metadata) > 0 {
		reqBody["metadata"] = request.Metadata
	}
	for k, v := range request.ExtraParams {
		if _, exists := reqBody[k]; !exists {
			reqBody[k] = v
		}
	}

	jsonBody, err := providerUtils.MarshalSorted(reqBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildContainerURL(ctx, key, "/containers"))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.SetBody(jsonBody)

	authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	for k, v := range authHeaders {
		req.Header.Set(k, v)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		return nil, providerUtils.SetErrorLatency(openai.ParseOpenAIError(resp), latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}
	responseBody := append([]byte(nil), body...)
	var containerResp struct {
		ID           string                         `json:"id"`
		Object       string                         `json:"object"`
		Name         string                         `json:"name"`
		CreatedAt    int64                          `json:"created_at"`
		Status       schemas.ContainerStatus        `json:"status"`
		ExpiresAfter *schemas.ContainerExpiresAfter `json:"expires_after"`
		LastActiveAt *int64                         `json:"last_active_at"`
		MemoryLimit  string                         `json:"memory_limit"`
		Metadata     map[string]string              `json:"metadata"`
	}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &containerResp, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostContainerCreateResponse{
		ID:           containerResp.ID,
		Object:       containerResp.Object,
		Name:         containerResp.Name,
		CreatedAt:    containerResp.CreatedAt,
		Status:       containerResp.Status,
		ExpiresAfter: containerResp.ExpiresAfter,
		LastActiveAt: containerResp.LastActiveAt,
		MemoryLimit:  containerResp.MemoryLimit,
		Metadata:     containerResp.Metadata,
		ExtraFields:  schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()},
	}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ContainerList lists containers via Azure's OpenAI API.
func (provider *AzureProvider) ContainerList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve retrieves a specific container via Azure's OpenAI API.
func (provider *AzureProvider) ContainerRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil)
	}
	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("container_id is required", nil)
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("provider config not found", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if resolveAzureEndpoint(ctx, key) == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.buildContainerURL(ctx, key, "/containers/"+url.PathEscape(request.ContainerID)))
		req.Header.SetMethod(http.MethodGet)

		authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		for k, v := range authHeaders {
			req.Header.Set(k, v)
		}

		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		wait()
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		if resp.StatusCode() >= 400 {
			lastErr = openai.ParseOpenAIError(resp)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
		if decodeErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr)
			continue
		}
		responseBody := append([]byte(nil), body...)
		var containerResp struct {
			ID           string                         `json:"id"`
			Object       string                         `json:"object"`
			Name         string                         `json:"name"`
			CreatedAt    int64                          `json:"created_at"`
			Status       schemas.ContainerStatus        `json:"status"`
			ExpiresAfter *schemas.ContainerExpiresAfter `json:"expires_after"`
			LastActiveAt *int64                         `json:"last_active_at"`
			MemoryLimit  string                         `json:"memory_limit"`
			Metadata     map[string]string              `json:"metadata"`
		}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &containerResp, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		response := &schemas.BifrostContainerRetrieveResponse{
			ID:           containerResp.ID,
			Object:       containerResp.Object,
			Name:         containerResp.Name,
			CreatedAt:    containerResp.CreatedAt,
			Status:       containerResp.Status,
			ExpiresAfter: containerResp.ExpiresAfter,
			LastActiveAt: containerResp.LastActiveAt,
			MemoryLimit:  containerResp.MemoryLimit,
			Metadata:     containerResp.Metadata,
			ExtraFields:  schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()},
		}
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return response, nil
	}
	return nil, lastErr
}

// ContainerDelete deletes a container via Azure's OpenAI API.
func (provider *AzureProvider) ContainerDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil)
	}
	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("container_id is required", nil)
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("provider config not found", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if resolveAzureEndpoint(ctx, key) == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.buildContainerURL(ctx, key, "/containers/"+url.PathEscape(request.ContainerID)))
		req.Header.SetMethod(http.MethodDelete)
		req.Header.SetContentType("application/json")

		authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		for k, v := range authHeaders {
			req.Header.Set(k, v)
		}

		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		wait()
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		if resp.StatusCode() >= 400 {
			lastErr = openai.ParseOpenAIError(resp)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
		if decodeErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr)
			continue
		}
		responseBody := append([]byte(nil), body...)
		var deleteResp struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Deleted bool   `json:"deleted"`
		}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &deleteResp, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		response := &schemas.BifrostContainerDeleteResponse{
			ID:          deleteResp.ID,
			Object:      deleteResp.Object,
			Deleted:     deleteResp.Deleted,
			ExtraFields: schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()},
		}
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return response, nil
	}
	return nil, lastErr
}

// ContainerFileCreate uploads a file to a container via Azure's OpenAI API.
func (provider *AzureProvider) ContainerFileCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil)
	}
	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil)
	}
	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("invalid request: file is required", nil)
	}
	if resolveAzureEndpoint(ctx, key) == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "file")
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create multipart form", err)
	}
	if _, err = part.Write(request.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file to multipart form", err)
	}
	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to close multipart form", err)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildContainerURL(ctx, key, fmt.Sprintf("/containers/%s/files", url.PathEscape(request.ContainerID))))
	req.Header.SetMethod(http.MethodPost)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetBody(body.Bytes())

	authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	for k, v := range authHeaders {
		req.Header.Set(k, v)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() >= 400 {
		return nil, providerUtils.SetErrorLatency(openai.ParseOpenAIError(resp), latency)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	var fileResp struct {
		ID          string `json:"id"`
		Object      string `json:"object"`
		Bytes       int64  `json:"bytes"`
		CreatedAt   int64  `json:"created_at"`
		ContainerID string `json:"container_id"`
		Path        string `json:"path"`
		Source      string `json:"source"`
	}

	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &fileResp, nil, false, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostContainerFileCreateResponse{
		ID:          fileResp.ID,
		Object:      fileResp.Object,
		Bytes:       fileResp.Bytes,
		CreatedAt:   fileResp.CreatedAt,
		ContainerID: fileResp.ContainerID,
		Path:        fileResp.Path,
		Source:      fileResp.Source,
		ExtraFields: schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()},
	}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = "<REDACTED>"
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ContainerFileList lists files in a container via Azure's OpenAI API.
func (provider *AzureProvider) ContainerFileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil)
	}
	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil)
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("provider config not found", nil)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	helper, herr := providerUtils.NewSerialListHelper(keys, request.After, provider.logger, true)
	if herr != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", herr)
	}

	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		return &schemas.BifrostContainerFileListResponse{Object: "list", Data: []schemas.ContainerFileObject{}, HasMore: false}, nil
	}
	if resolveAzureEndpoint(ctx, key) == "" {
		return nil, providerUtils.NewConfigurationError("endpoint not set")
	}

	requestURL := provider.buildContainerURL(ctx, key, fmt.Sprintf("/containers/%s/files", url.PathEscape(request.ContainerID)))
	queryParams := url.Values{}
	if request.Limit > 0 {
		queryParams.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	if nativeCursor != "" {
		queryParams.Set("after", nativeCursor)
	}
	if request.Order != nil {
		queryParams.Set("order", *request.Order)
	}
	if len(queryParams) > 0 {
		requestURL += "?" + queryParams.Encode()
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)

	authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	for k, v := range authHeaders {
		req.Header.Set(k, v)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() >= 400 {
		return nil, providerUtils.SetErrorLatency(openai.ParseOpenAIError(resp), latency)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	var listResp struct {
		Object  string                        `json:"object"`
		Data    []schemas.ContainerFileObject `json:"data"`
		FirstID *string                       `json:"first_id"`
		LastID  *string                       `json:"last_id"`
		HasMore bool                          `json:"has_more"`
	}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &listResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	cursorID := ""
	if listResp.LastID != nil {
		cursorID = *listResp.LastID
	}
	nextCursor, hasMore := helper.BuildNextCursor(listResp.HasMore, cursorID)

	response := &schemas.BifrostContainerFileListResponse{
		Object:      listResp.Object,
		Data:        listResp.Data,
		FirstID:     listResp.FirstID,
		LastID:      listResp.LastID,
		HasMore:     hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()},
	}
	if nextCursor != "" {
		response.After = &nextCursor
	}
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ContainerFileRetrieve retrieves file metadata from a container via Azure's OpenAI API.
func (provider *AzureProvider) ContainerFileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil)
	}
	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil)
	}
	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: file_id is required", nil)
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("provider config not found", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if resolveAzureEndpoint(ctx, key) == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.buildContainerURL(ctx, key, fmt.Sprintf("/containers/%s/files/%s", url.PathEscape(request.ContainerID), url.PathEscape(request.FileID))))
		req.Header.SetMethod(http.MethodGet)

		authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		for k, v := range authHeaders {
			req.Header.Set(k, v)
		}

		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		wait()
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		if resp.StatusCode() >= 400 {
			lastErr = openai.ParseOpenAIError(resp)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		responseBody, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		var fileResp struct {
			ID          string `json:"id"`
			Object      string `json:"object"`
			Bytes       int64  `json:"bytes"`
			CreatedAt   int64  `json:"created_at"`
			ContainerID string `json:"container_id"`
			Path        string `json:"path"`
			Source      string `json:"source"`
		}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &fileResp, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		response := &schemas.BifrostContainerFileRetrieveResponse{
			ID:          fileResp.ID,
			Object:      fileResp.Object,
			Bytes:       fileResp.Bytes,
			CreatedAt:   fileResp.CreatedAt,
			ContainerID: fileResp.ContainerID,
			Path:        fileResp.Path,
			Source:      fileResp.Source,
			ExtraFields: schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()},
		}
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return response, nil
	}
	return nil, lastErr
}

// ContainerFileContent retrieves the binary content of a file from a container via Azure's OpenAI API.
func (provider *AzureProvider) ContainerFileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil)
	}
	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil)
	}
	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: file_id is required", nil)
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("provider config not found", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if resolveAzureEndpoint(ctx, key) == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.buildContainerURL(ctx, key, fmt.Sprintf("/containers/%s/files/%s/content", url.PathEscape(request.ContainerID), url.PathEscape(request.FileID))))
		req.Header.SetMethod(http.MethodGet)

		authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		for k, v := range authHeaders {
			req.Header.Set(k, v)
		}

		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		wait()
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		if resp.StatusCode() >= 400 {
			lastErr = openai.ParseOpenAIError(resp)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		contentType := string(resp.Header.ContentType())
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		response := &schemas.BifrostContainerFileContentResponse{
			Content:     append([]byte(nil), body...),
			ContentType: contentType,
			ExtraFields: schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()},
		}
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = map[string]string{
				"container_id": request.ContainerID,
				"file_id":      request.FileID,
			}
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = "<REDACTED>"
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return response, nil
	}
	return nil, lastErr
}

// ContainerFileDelete deletes a file from a container via Azure's OpenAI API.
func (provider *AzureProvider) ContainerFileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil)
	}
	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil)
	}
	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: file_id is required", nil)
	}
	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError("provider config not found", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		if resolveAzureEndpoint(ctx, key) == "" {
			lastErr = providerUtils.NewConfigurationError("endpoint not set")
			continue
		}

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.buildContainerURL(ctx, key, fmt.Sprintf("/containers/%s/files/%s", url.PathEscape(request.ContainerID), url.PathEscape(request.FileID))))
		req.Header.SetMethod(http.MethodDelete)

		authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, false)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		for k, v := range authHeaders {
			req.Header.Set(k, v)
		}

		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		wait()
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}
		if resp.StatusCode() >= 400 {
			lastErr = openai.ParseOpenAIError(resp)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		responseBody, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		var deleteResp struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Deleted bool   `json:"deleted"`
		}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &deleteResp, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		response := &schemas.BifrostContainerFileDeleteResponse{
			ID:          deleteResp.ID,
			Object:      deleteResp.Object,
			Deleted:     deleteResp.Deleted,
			ExtraFields: schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds()},
		}
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return response, nil
	}
	return nil, lastErr
}

// Passthrough forwards a raw request to Azure's API without any transformation.
func (provider *AzureProvider) Passthrough(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	url, err := provider.buildPassthroughURL(ctx, key, req.Path, req.RawQuery)
	if err != nil {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("failed to build passthrough URL: %s", err.Error()))
	}

	fasthttpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	defer fasthttp.ReleaseRequest(fasthttpReq)

	fasthttpReq.Header.SetMethod(req.Method)
	fasthttpReq.SetRequestURI(url)

	providerUtils.SetExtraHeaders(ctx, fasthttpReq, provider.networkConfig.ExtraHeaders, nil)

	for k, v := range req.SafeHeaders {
		fasthttpReq.Header.Set(k, v)
	}

	authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, schemas.IsAnthropicModelFamily(ctx, req.Model))
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	for k, v := range authHeaders {
		fasthttpReq.Header.Set(k, v)
	}

	fasthttpReq.SetBody(req.Body)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, fasthttpReq, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers := providerUtils.ExtractPassthroughProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to decode response body", err)
	}

	var passthroughUsage *schemas.BifrostPassthroughUsage
	if resp.StatusCode() >= 200 && resp.StatusCode() < 300 {
		passthroughUsage = extractAzurePassthroughUsage(req.Method, req.Path, req.Body, body, req.Model)
	}

	bifrostResponse := &schemas.BifrostPassthroughResponse{
		StatusCode: resp.StatusCode(),
		Headers:    headers,
		Body:       body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: headers,
			PassthroughPath:         req.Path,
		},
		PassthroughUsage: passthroughUsage,
	}

	return bifrostResponse, nil
}

// PassthroughStream forwards a raw streaming request to Azure's API without any transformation.
// Chunks are piped back as raw bytes, preserving the upstream SSE or binary stream format.
func (provider *AzureProvider) PassthroughStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	url, err := provider.buildPassthroughURL(ctx, key, req.Path, req.RawQuery)
	if err != nil {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("failed to build passthrough URL: %s", err.Error()))
	}

	fasthttpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(fasthttpReq)

	fasthttpReq.Header.SetMethod(req.Method)
	fasthttpReq.SetRequestURI(url)

	providerUtils.SetExtraHeaders(ctx, fasthttpReq, provider.networkConfig.ExtraHeaders, nil)

	for k, v := range req.SafeHeaders {
		fasthttpReq.Header.Set(k, v)
	}

	fasthttpReq.Header.Set("Connection", "close")

	authHeaders, bifrostErr := provider.getAzureAuthHeaders(ctx, key, schemas.IsAnthropicModelFamily(ctx, req.Model))
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	for k, v := range authHeaders {
		fasthttpReq.Header.Set(k, v)
	}

	fasthttpReq.SetBody(req.Body)

	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.streamingClient, resp)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	startTime := time.Now()

	err = activeClient.Do(fasthttpReq, resp)
	latency := time.Since(startTime)
	if err != nil {
		providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		// Request failed before the first response byte (server closed an idle/pooled connection,
		// broken pipe, connection refused, DNS failure, etc.). Surface as a retriable upstream
		// connection error (502) so executeRequestWithRetries honors max_retries, matching the
		// non-streaming path - see https://github.com/maximhq/bifrost/issues/4496.
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), latency)
	}

	headers := providerUtils.ExtractPassthroughProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	rawBodyStream := resp.BodyStream()
	if rawBodyStream == nil {
		providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.NewBifrostOperationError("provider returned an empty stream body", fmt.Errorf("provider returned an empty stream body"))
	}

	var anthropicUsage *anthropic.AnthropicPassthroughStreamUsage
	if schemas.IsAnthropicModelFamily(ctx, req.Model) {
		anthropicUsage = &anthropic.AnthropicPassthroughStreamUsage{}
	}
	return providerUtils.StreamPassthrough(
		ctx, postHookRunner, postHookSpanFinalizer, resp, rawBodyStream,
		providerUtils.PassthroughStreamParams{
			StatusCode:       resp.StatusCode(),
			Headers:          headers,
			Path:             req.Path,
			RawRequest:       req.Body,
			CancellationBody: providerUtils.PassthroughJSONBody(fasthttpReq, req.Body),
			StartTime:        startTime,
			Logger:           provider.logger,
			HasUsage: func(event []byte) bool {
				if anthropicUsage != nil {
					return anthropic.HasAnthropicPassthroughUsage(event)
				}
				return openai.HasOpenAIPassthroughUsage(event)
			},
			Observe: func(event []byte) *schemas.BifrostPassthroughUsage {
				if anthropicUsage != nil {
					return anthropicUsage.ObserveEvent(event)
				}
				return openai.ExtractOpenAIPassthroughUsage(req.Method, req.Path, req.Body, event)
			},
		},
	), nil
}

// buildPassthroughURL constructs the full Azure URL for a passthrough request.
// ctx carries the resolved alias used to pick a per-alias api-version override
// when the caller did not supply one in rawQuery.
func (provider *AzureProvider) buildPassthroughURL(ctx *schemas.BifrostContext, key schemas.Key, path, rawQuery string) (string, error) {
	endpoint := resolveAzureEndpoint(ctx, key)
	if endpoint == "" {
		return "", fmt.Errorf("endpoint not set")
	}

	// Normalise paths emitted by the Azure SDK.
	path = strings.Replace(path, "/openai/responses", "/openai/v1/responses", 1)
	path = strings.Replace(path, "/openai/videos", "/openai/v1/videos", 1)

	switch {
	case strings.HasPrefix(path, "/anthropic/") || strings.HasPrefix(path, "/openai/v1/videos"):
		// Anthropic and video routes do not accept api-version — strip it if present.
		if values, err := url.ParseQuery(rawQuery); err == nil {
			values.Del("api-version")
			rawQuery = values.Encode()
		}
	case strings.Contains(path, "/openai/v1/responses"):
		// The versionless v1 API omits api-version by default — Azure OpenAI v1
		// GA and Azure AI Foundry project endpoints reject it outright on /v1
		// paths. Only attach it if the caller didn't already supply one and the
		// user explicitly configured an override.
		if values, err := url.ParseQuery(rawQuery); err == nil {
			if values.Get("api-version") == "" {
				if v := resolveAPIVersion(ctx, ""); v != "" {
					values.Set("api-version", v)
					rawQuery = values.Encode()
				}
			}
		}
	case strings.Contains(path, "/openai/deployments/"):
		// Classic /deployments/ routes require api-version. Inject a default if absent.
		values, _ := url.ParseQuery(rawQuery)
		if values.Get("api-version") == "" {
			values.Set("api-version", resolveAPIVersion(ctx, DefaultAzureAPIVersion))
			rawQuery = values.Encode()
		}
	}

	fullURL := endpoint + path
	if rawQuery != "" {
		fullURL += "?" + rawQuery
	}
	return fullURL, nil
}

// extractAzurePassthroughUsage dispatches usage extraction by the upstream API the
// passthrough request targets. Azure serves both OpenAI and Azure-hosted Anthropic models,
// so Anthropic routes (e.g. /messages) must use the Anthropic extractor — otherwise their
// usage is dropped and budgets/logging stay wrong.
func extractAzurePassthroughUsage(method, path string, reqBody, body []byte, model string) *schemas.BifrostPassthroughUsage {
	if schemas.IsAnthropicModel(model) {
		return anthropic.ExtractAnthropicPassthroughUsage(path, reqBody, body)
	}
	return openai.ExtractOpenAIPassthroughUsage(method, path, reqBody, body)
}
