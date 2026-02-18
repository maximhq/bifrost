// Package sapaicore implements the SAP AI Core provider for Bifrost.
// SAP AI Core is a gateway that provides OAuth2-authenticated access to multiple AI backends
// (OpenAI, Anthropic via Bedrock, Gemini via Vertex) through a unified API.
package sapaicore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// SAPAICoreAuthorizationTokenKey is the context key for passing a pre-fetched SAP AI Core token.
const SAPAICoreAuthorizationTokenKey schemas.BifrostContextKey = "sapaicore-authorization-token"

// SAPAICoreProvider implements the Provider interface for SAP AI Core.
type SAPAICoreProvider struct {
	logger              schemas.Logger
	client              *fasthttp.Client
	networkConfig       schemas.NetworkConfig
	sendBackRawRequest  bool
	sendBackRawResponse bool
	tokenCache          *TokenCache
	deploymentCache     *DeploymentCache
}

// NewSAPAICoreProvider creates a new SAP AI Core provider instance.
func NewSAPAICoreProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*SAPAICoreProvider, error) {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	// Configure proxy and dialer
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client)

	tokenCache := NewTokenCache(client)
	deploymentCache := NewDeploymentCache(client, tokenCache)

	return &SAPAICoreProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
		tokenCache:          tokenCache,
		deploymentCache:     deploymentCache,
	}, nil
}

// GetProviderKey returns the provider identifier for SAP AI Core.
func (provider *SAPAICoreProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.SAPAICore
}

// Shutdown cleans up provider resources including cached tokens and deployments.
// This should be called when the provider is no longer needed.
func (provider *SAPAICoreProvider) Shutdown() {
	if provider.tokenCache != nil {
		provider.tokenCache.Cleanup()
	}
	if provider.deploymentCache != nil {
		provider.deploymentCache.ClearCache("", "")
	}
}

// getKeyConfig extracts and validates the SAP AI Core key configuration
func getKeyConfig(key schemas.Key) (*schemas.SAPAICoreKeyConfig, *schemas.BifrostError) {
	if key.SAPAICoreKeyConfig == nil {
		return nil, providerUtils.NewBifrostOperationError(
			"SAP AI Core key configuration is missing",
			fmt.Errorf("sapaicore_key_config is required"),
			schemas.SAPAICore,
		)
	}

	config := key.SAPAICoreKeyConfig

	// Validate required fields
	if config.ClientID.GetValue() == "" {
		return nil, providerUtils.NewBifrostOperationError(
			"SAP AI Core ClientID is required",
			fmt.Errorf("client_id is missing or empty"),
			schemas.SAPAICore,
		)
	}
	if config.ClientSecret.GetValue() == "" {
		return nil, providerUtils.NewBifrostOperationError(
			"SAP AI Core ClientSecret is required",
			fmt.Errorf("client_secret is missing or empty"),
			schemas.SAPAICore,
		)
	}
	if config.AuthURL.GetValue() == "" {
		return nil, providerUtils.NewBifrostOperationError(
			"SAP AI Core AuthURL is required",
			fmt.Errorf("auth_url is missing or empty"),
			schemas.SAPAICore,
		)
	}
	if config.BaseURL.GetValue() == "" {
		return nil, providerUtils.NewBifrostOperationError(
			"SAP AI Core BaseURL is required",
			fmt.Errorf("base_url is missing or empty"),
			schemas.SAPAICore,
		)
	}
	if config.ResourceGroup.GetValue() == "" {
		return nil, providerUtils.NewBifrostOperationError(
			"SAP AI Core ResourceGroup is required",
			fmt.Errorf("resource_group is missing or empty"),
			schemas.SAPAICore,
		)
	}

	return config, nil
}

// getAuthToken retrieves an auth token from context or fetches a new one
func (provider *SAPAICoreProvider) getAuthToken(ctx *schemas.BifrostContext, config *schemas.SAPAICoreKeyConfig) (string, *schemas.BifrostError) {
	// Check for context-provided token first
	if authToken, ok := ctx.Value(SAPAICoreAuthorizationTokenKey).(string); ok && authToken != "" {
		return authToken, nil
	}

	// Fetch token using OAuth2 client credentials
	return provider.tokenCache.GetToken(
		config.ClientID.GetValue(),
		config.ClientSecret.GetValue(),
		config.AuthURL.GetValue(),
	)
}

// resolveDeployment resolves the deployment ID for a model
func (provider *SAPAICoreProvider) resolveDeployment(
	modelName string,
	config *schemas.SAPAICoreKeyConfig,
) (string, BackendType, *schemas.BifrostError) {
	return provider.deploymentCache.GetDeploymentID(
		modelName,
		config.Deployments,
		config.ClientID.GetValue(),
		config.ClientSecret.GetValue(),
		config.AuthURL.GetValue(),
		config.BaseURL.GetValue(),
		config.ResourceGroup.GetValue(),
	)
}

// buildRequestURL constructs the URL for a SAP AI Core API request
func buildRequestURL(baseURL, deploymentID, path string) string {
	normalizedURL := normalizeBaseURL(baseURL)
	return fmt.Sprintf("%s/inference/deployments/%s%s", normalizedURL, deploymentID, path)
}

// ListModels returns the list of available models from running deployments.
func (provider *SAPAICoreProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if len(keys) == 0 {
		return nil, providerUtils.NewBifrostOperationError(
			"no API keys provided for SAP AI Core",
			fmt.Errorf("keys required"),
			providerName,
		)
	}

	// Use first key for model listing
	key := keys[0]
	config, err := getKeyConfig(key)
	if err != nil {
		return nil, err
	}

	startTime := time.Now()

	models, listErr := provider.deploymentCache.ListModels(
		config.ClientID.GetValue(),
		config.ClientSecret.GetValue(),
		config.AuthURL.GetValue(),
		config.BaseURL.GetValue(),
		config.ResourceGroup.GetValue(),
	)
	if listErr != nil {
		return nil, listErr
	}

	latency := time.Since(startTime)

	// Convert to Bifrost format
	bifrostModels := make([]schemas.Model, 0, len(models))
	for _, m := range models {
		ownedBy := "sapaicore"
		bifrostModels = append(bifrostModels, schemas.Model{
			ID:      m.ID,
			OwnedBy: &ownedBy,
		})
	}

	response := &schemas.BifrostListModelsResponse{
		Data: bifrostModels,
	}
	response.ExtraFields.Provider = providerName
	response.ExtraFields.RequestType = schemas.ListModelsRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

// ChatCompletion performs a chat completion request to SAP AI Core.
// It routes the request to the appropriate backend (OpenAI, Bedrock, or Vertex) based on the model.
func (provider *SAPAICoreProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	config, err := getKeyConfig(key)
	if err != nil {
		return nil, err
	}

	// Get auth token
	token, tokenErr := provider.getAuthToken(ctx, config)
	if tokenErr != nil {
		return nil, tokenErr
	}

	// Resolve deployment
	deploymentID, backend, deployErr := provider.resolveDeployment(request.Model, config)
	if deployErr != nil {
		return nil, deployErr
	}

	// Route based on backend
	switch backend {
	case BackendBedrock:
		return provider.handleBedrockChatCompletion(ctx, token, config, deploymentID, request)
	case BackendVertex:
		return provider.handleVertexChatCompletion(ctx, token, config, deploymentID, request)
	default:
		return provider.handleOpenAIChatCompletion(ctx, token, config, deploymentID, request)
	}
}

// handleOpenAIChatCompletion handles chat completion for OpenAI-compatible backends
func (provider *SAPAICoreProvider) handleOpenAIChatCompletion(
	ctx *schemas.BifrostContext,
	token string,
	config *schemas.SAPAICoreKeyConfig,
	deploymentID string,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Build URL with api-version query parameter
	baseRequestURL := buildRequestURL(config.BaseURL.GetValue(), deploymentID, "/chat/completions")
	requestURL := baseRequestURL + "?api-version=" + SAPAICoreAPIVersion

	// Create a mock key with Bearer token for OpenAI handler
	mockKey := schemas.Key{
		Value: *schemas.NewEnvVar(token),
	}

	// Use extra headers for SAP AI Core specific headers
	extraHeaders := maps.Clone(provider.networkConfig.ExtraHeaders)
	if extraHeaders == nil {
		extraHeaders = make(map[string]string)
	}
	extraHeaders["AI-Resource-Group"] = config.ResourceGroup.GetValue()

	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		requestURL,
		request,
		mockKey,
		extraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		providerName,
		ParseSAPAICoreError,
		provider.logger,
	)
}

// handleBedrockChatCompletion handles chat completion for Bedrock backends (Anthropic, Amazon)
func (provider *SAPAICoreProvider) handleBedrockChatCompletion(
	ctx *schemas.BifrostContext,
	token string,
	config *schemas.SAPAICoreKeyConfig,
	deploymentID string,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Build Bedrock-style URL
	requestURL := buildRequestURL(config.BaseURL.GetValue(), deploymentID, "/invoke")

	// Convert request to Bedrock format
	bedrockRequest := convertToBedrock(request)

	jsonData, marshalErr := sonic.Marshal(bedrockRequest)
	if marshalErr != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to marshal Bedrock request",
			marshalErr,
			providerName,
		)
	}

	// Make request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(requestURL)
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", config.ResourceGroup.GetValue())
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetBody(jsonData)

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, providerName, request.Model)
	}

	// Parse Bedrock response
	responseBody := append([]byte(nil), resp.Body()...)
	response, parseErr := parseBedrockResponse(responseBody, request.Model)
	if parseErr != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to parse Bedrock response",
			parseErr,
			providerName,
		)
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ChatCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

// handleVertexChatCompletion handles chat completion for Vertex backends (Gemini)
func (provider *SAPAICoreProvider) handleVertexChatCompletion(
	ctx *schemas.BifrostContext,
	token string,
	config *schemas.SAPAICoreKeyConfig,
	deploymentID string,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Build Vertex-style URL
	requestURL := buildRequestURL(config.BaseURL.GetValue(), deploymentID, fmt.Sprintf("/models/%s:generateContent", request.Model))

	// Convert request to Vertex format
	vertexRequest := convertToVertex(request)

	jsonData, marshalErr := sonic.Marshal(vertexRequest)
	if marshalErr != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to marshal Vertex request",
			marshalErr,
			providerName,
		)
	}

	// Make request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(requestURL)
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", config.ResourceGroup.GetValue())
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetBody(jsonData)

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, ParseSAPAICoreError(resp, schemas.ChatCompletionRequest, providerName, request.Model)
	}

	// Parse Vertex response
	responseBody := append([]byte(nil), resp.Body()...)
	response, parseErr := parseVertexResponse(responseBody, request.Model)
	if parseErr != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to parse Vertex response",
			parseErr,
			providerName,
		)
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ChatCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

// ChatCompletionStream performs a streaming chat completion request to SAP AI Core.
func (provider *SAPAICoreProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	config, err := getKeyConfig(key)
	if err != nil {
		return nil, err
	}

	// Get auth token
	token, tokenErr := provider.getAuthToken(ctx, config)
	if tokenErr != nil {
		return nil, tokenErr
	}

	// Resolve deployment
	deploymentID, backend, deployErr := provider.resolveDeployment(request.Model, config)
	if deployErr != nil {
		return nil, deployErr
	}

	// Route based on backend
	switch backend {
	case BackendBedrock:
		return provider.handleBedrockChatCompletionStream(ctx, postHookRunner, token, config, deploymentID, request)
	case BackendVertex:
		return provider.handleVertexChatCompletionStream(ctx, postHookRunner, token, config, deploymentID, request)
	default:
		return provider.handleOpenAIChatCompletionStream(ctx, postHookRunner, token, config, deploymentID, request)
	}
}

// handleOpenAIChatCompletionStream handles streaming chat completion for OpenAI-compatible backends
func (provider *SAPAICoreProvider) handleOpenAIChatCompletionStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	token string,
	config *schemas.SAPAICoreKeyConfig,
	deploymentID string,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Build URL with api-version query parameter
	baseRequestURL := buildRequestURL(config.BaseURL.GetValue(), deploymentID, "/chat/completions")
	requestURL := baseRequestURL + "?api-version=" + SAPAICoreAPIVersion

	// Set up auth headers
	authHeader := map[string]string{
		"Authorization":     "Bearer " + token,
		"AI-Resource-Group": config.ResourceGroup.GetValue(),
	}

	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.client,
		requestURL,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		providerName,
		postHookRunner,
		nil,
		ParseSAPAICoreError,
		nil,
		nil,
		provider.logger,
	)
}

// handleBedrockChatCompletionStream handles streaming chat completion for Bedrock backends
func (provider *SAPAICoreProvider) handleBedrockChatCompletionStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	token string,
	config *schemas.SAPAICoreKeyConfig,
	deploymentID string,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Build Bedrock streaming URL
	requestURL := buildRequestURL(config.BaseURL.GetValue(), deploymentID, "/invoke-with-response-stream")

	// Convert request to Bedrock format
	bedrockRequest := convertToBedrock(request)

	jsonData, marshalErr := sonic.Marshal(bedrockRequest)
	if marshalErr != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to marshal Bedrock streaming request",
			marshalErr,
			providerName,
		)
	}

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.SetRequestURI(requestURL)
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", config.ResourceGroup.GetValue())
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetBody(jsonData)

	// Make the request
	if err := provider.client.Do(req, resp); err != nil {
		providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, ParseSAPAICoreError(resp, schemas.ChatCompletionStreamRequest, providerName, request.Model)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ChatCompletionStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ChatCompletionStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()

		// Process Bedrock event stream
		processBedrockEventStream(ctx, resp.BodyStream(), responseChan, postHookRunner, providerName, request.Model, provider.logger)
	}()

	return responseChan, nil
}

// handleVertexChatCompletionStream handles streaming chat completion for Vertex backends
func (provider *SAPAICoreProvider) handleVertexChatCompletionStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	token string,
	config *schemas.SAPAICoreKeyConfig,
	deploymentID string,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Build Vertex streaming URL
	requestURL := buildRequestURL(config.BaseURL.GetValue(), deploymentID, fmt.Sprintf("/models/%s:streamGenerateContent?alt=sse", request.Model))

	// Convert request to Vertex format
	vertexRequest := convertToVertex(request)

	jsonData, marshalErr := sonic.Marshal(vertexRequest)
	if marshalErr != nil {
		return nil, providerUtils.NewBifrostOperationError(
			"failed to marshal Vertex streaming request",
			marshalErr,
			providerName,
		)
	}

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.SetRequestURI(requestURL)
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", config.ResourceGroup.GetValue())
	req.Header.Set("Accept", "text/event-stream")
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetBody(jsonData)

	// Make the request
	if err := provider.client.Do(req, resp); err != nil {
		providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, ParseSAPAICoreError(resp, schemas.ChatCompletionStreamRequest, providerName, request.Model)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ChatCompletionStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ChatCompletionStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()

		// Process Vertex SSE stream
		processVertexSSEStream(ctx, resp.BodyStream(), responseChan, postHookRunner, providerName, request.Model, provider.logger)
	}()

	return responseChan, nil
}

// TextCompletion is not directly supported - returns an error
func (provider *SAPAICoreProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"TextCompletion is not supported by SAP AI Core provider - use ChatCompletion instead",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// TextCompletionStream is not directly supported - returns an error
func (provider *SAPAICoreProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"TextCompletionStream is not supported by SAP AI Core provider - use ChatCompletionStream instead",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// Embedding performs an embedding request to SAP AI Core.
func (provider *SAPAICoreProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	config, err := getKeyConfig(key)
	if err != nil {
		return nil, err
	}

	// Get auth token
	token, tokenErr := provider.getAuthToken(ctx, config)
	if tokenErr != nil {
		return nil, tokenErr
	}

	// Resolve deployment
	deploymentID, _, deployErr := provider.resolveDeployment(request.Model, config)
	if deployErr != nil {
		return nil, deployErr
	}

	// Build URL - embeddings use OpenAI-compatible endpoint
	baseRequestURL := buildRequestURL(config.BaseURL.GetValue(), deploymentID, "/embeddings")
	requestURL := baseRequestURL + "?api-version=" + SAPAICoreAPIVersion

	// Create a mock key with Bearer token
	mockKey := schemas.Key{
		Value: *schemas.NewEnvVar(token),
	}

	// Use extra headers for SAP AI Core specific headers
	extraHeaders := maps.Clone(provider.networkConfig.ExtraHeaders)
	if extraHeaders == nil {
		extraHeaders = make(map[string]string)
	}
	extraHeaders["AI-Resource-Group"] = config.ResourceGroup.GetValue()

	return openai.HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		requestURL,
		request,
		mockKey,
		extraHeaders,
		providerName,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// Responses is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"Responses is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ResponsesStream is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ResponsesStream is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// CountTokens is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"CountTokens is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// Speech is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"Speech is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// SpeechStream is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"SpeechStream is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// Transcription is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"Transcription is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// TranscriptionStream is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"TranscriptionStream is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ImageGeneration is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ImageGeneration is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ImageGenerationStream is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ImageGenerationStream is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ImageEdit is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ImageEdit is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ImageEditStream is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ImageEditStream is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ImageVariation is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ImageVariation is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// BatchCreate is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"BatchCreate is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// BatchList is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"BatchList is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// BatchRetrieve is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"BatchRetrieve is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// BatchCancel is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) BatchCancel(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"BatchCancel is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// BatchResults is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"BatchResults is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// FileUpload is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"FileUpload is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// FileList is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"FileList is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// FileRetrieve is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"FileRetrieve is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// FileDelete is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"FileDelete is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// FileContent is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"FileContent is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ContainerCreate is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ContainerCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ContainerCreate is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ContainerList is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ContainerList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ContainerList is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ContainerRetrieve is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ContainerRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ContainerRetrieve is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ContainerDelete is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ContainerDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ContainerDelete is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ContainerFileCreate is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ContainerFileCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ContainerFileCreate is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ContainerFileList is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ContainerFileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ContainerFileList is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ContainerFileRetrieve is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ContainerFileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ContainerFileRetrieve is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ContainerFileContent is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ContainerFileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ContainerFileContent is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// ContainerFileDelete is not supported by SAP AI Core provider
func (provider *SAPAICoreProvider) ContainerFileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewBifrostOperationError(
		"ContainerFileDelete is not supported by SAP AI Core provider",
		fmt.Errorf("unsupported operation"),
		schemas.SAPAICore,
	)
}

// processBedrockEventStream processes Bedrock event stream and sends chunks to the channel
func processBedrockEventStream(
	ctx *schemas.BifrostContext,
	bodyStream io.Reader,
	responseChan chan *schemas.BifrostStreamChunk,
	postHookRunner schemas.PostHookRunner,
	providerName schemas.ModelProvider,
	model string,
	logger schemas.Logger,
) {
	scanner := bufio.NewScanner(bodyStream)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	chunkIndex := -1
	usage := &schemas.BifrostLLMUsage{}
	var finishReason *string
	startTime := time.Now()

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Parse Bedrock event stream format
		// This is simplified - full implementation would parse AWS event stream format
		if strings.HasPrefix(line, "data: ") {
			jsonData := strings.TrimPrefix(line, "data: ")

			var bedrockEvent BedrockStreamEvent
			if err := sonic.Unmarshal([]byte(jsonData), &bedrockEvent); err != nil {
				logger.Warn("Failed to parse Bedrock stream event: %v", err)
				continue
			}

			// Convert to Bifrost response
			if bedrockEvent.Delta != nil && bedrockEvent.Delta.Text != nil {
				chunkIndex++

				response := &schemas.BifrostChatResponse{
					ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
					Object:  "chat.completion.chunk",
					Created: int(time.Now().Unix()),
					Model:   model,
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: 0,
							ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
								Delta: &schemas.ChatStreamResponseChoiceDelta{
									Content: bedrockEvent.Delta.Text,
								},
							},
						},
					},
				}

				response.ExtraFields.Provider = providerName
				response.ExtraFields.ModelRequested = model
				response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
				response.ExtraFields.ChunkIndex = chunkIndex

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
			}

			// Handle stop reason
			if bedrockEvent.StopReason != nil {
				finishReason = bedrockEvent.StopReason
			}

			// Handle usage
			if bedrockEvent.Usage != nil {
				usage.PromptTokens = bedrockEvent.Usage.InputTokens
				usage.CompletionTokens = bedrockEvent.Usage.OutputTokens
				usage.TotalTokens = bedrockEvent.Usage.TotalTokens
			}
		}
	}

	// Send final chunk with usage
	if finishReason != nil || usage.TotalTokens > 0 {
		finalResponse := providerUtils.CreateBifrostChatCompletionChunkResponse("", usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, model)
		finalResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, finalResponse, nil, nil, nil, nil), responseChan)
	}
}

// processVertexSSEStream processes Vertex SSE stream and sends chunks to the channel
func processVertexSSEStream(
	ctx *schemas.BifrostContext,
	bodyStream io.Reader,
	responseChan chan *schemas.BifrostStreamChunk,
	postHookRunner schemas.PostHookRunner,
	providerName schemas.ModelProvider,
	model string,
	logger schemas.Logger,
) {
	scanner := bufio.NewScanner(bodyStream)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	chunkIndex := -1
	usage := &schemas.BifrostLLMUsage{}
	var finishReason *string
	startTime := time.Now()

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			jsonData := strings.TrimPrefix(line, "data: ")

			var vertexResp VertexGenerateContentResponse
			if err := sonic.Unmarshal([]byte(jsonData), &vertexResp); err != nil {
				logger.Warn("Failed to parse Vertex stream event: %v", err)
				continue
			}

			// Convert to Bifrost response
			if len(vertexResp.Candidates) > 0 && len(vertexResp.Candidates[0].Content.Parts) > 0 {
				chunkIndex++

				text := vertexResp.Candidates[0].Content.Parts[0].Text

				response := &schemas.BifrostChatResponse{
					ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
					Object:  "chat.completion.chunk",
					Created: int(time.Now().Unix()),
					Model:   model,
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: 0,
							ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
								Delta: &schemas.ChatStreamResponseChoiceDelta{
									Content: &text,
								},
							},
						},
					},
				}

				response.ExtraFields.Provider = providerName
				response.ExtraFields.ModelRequested = model
				response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
				response.ExtraFields.ChunkIndex = chunkIndex

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)

				// Handle finish reason
				if vertexResp.Candidates[0].FinishReason != "" {
					fr := vertexResp.Candidates[0].FinishReason
					finishReason = &fr
				}
			}

			// Handle usage metadata
			if vertexResp.UsageMetadata != nil {
				usage.PromptTokens = vertexResp.UsageMetadata.PromptTokenCount
				usage.CompletionTokens = vertexResp.UsageMetadata.CandidatesTokenCount
				usage.TotalTokens = vertexResp.UsageMetadata.TotalTokenCount
			}
		}
	}

	// Send final chunk with usage
	if finishReason != nil || usage.TotalTokens > 0 {
		finalResponse := providerUtils.CreateBifrostChatCompletionChunkResponse("", usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, model)
		finalResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, finalResponse, nil, nil, nil, nil), responseChan)
	}
}
