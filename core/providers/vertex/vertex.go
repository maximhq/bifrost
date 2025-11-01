package vertex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

type VertexError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// vertexClientPool provides a pool/cache for authenticated Vertex HTTP clients.
// This avoids creating and authenticating clients for every request.
// Uses sync.Map for atomic operations without explicit locking.
var vertexClientPool sync.Map

// getClientKey generates a unique key for caching authenticated clients.
// It uses a hash of the auth credentials for security.
func getClientKey(authCredentials string) string {
	hash := sha256.Sum256([]byte(authCredentials))
	return hex.EncodeToString(hash[:])
}

// removeVertexClient removes a specific client from the pool.
// This should be called when:
// - API returns authentication/authorization errors (401, 403)
// - Auth client creation fails
// - Network errors that might indicate credential issues
// This ensures we don't keep using potentially invalid clients.
func removeVertexClient(authCredentials string) {
	clientKey := getClientKey(authCredentials)
	vertexClientPool.Delete(clientKey)
}

// VertexProvider implements the Provider interface for Google's Vertex AI API.
type VertexProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewVertexProvider creates a new Vertex provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewVertexProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*VertexProvider, error) {
	config.CheckAndSetDefaults()
	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     1024,
		MaxIdleConnDuration: 60 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	return &VertexProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// getAuthTokenSource returns an authenticated token source for Vertex AI API requests.
// It uses the default credentials if no auth credentials are provided.
// It uses the JWT config if auth credentials are provided.
// It returns an error if the token source creation fails.
func getAuthTokenSource(key schemas.Key) (oauth2.TokenSource, error) {
	if key.VertexKeyConfig == nil {
		return nil, fmt.Errorf("vertex key config is not set")
	}
	authCredentials := key.VertexKeyConfig.AuthCredentials
	var tokenSource oauth2.TokenSource
	if authCredentials == "" {
		creds, err := google.FindDefaultCredentials(context.Background(), cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("failed to find default credentials: %w", err)
		}
		tokenSource = creds.TokenSource
	} else {
		conf, err := google.JWTConfigFromJSON([]byte(authCredentials), cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("failed to create JWT config: %w", err)
		}
		tokenSource = conf.TokenSource(context.Background())
	}
	return tokenSource, nil
}

// GetProviderKey returns the provider identifier for Vertex.
func (provider *VertexProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Vertex
}

// ListModels performs a list models request to Vertex's API.
func (provider *VertexProvider) ListModels(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}

	projectID := key.VertexKeyConfig.ProjectID
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
	}

	region := key.VertexKeyConfig.Region
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
	}

	// Build URL using centralized URL construction
	requestURL := ToVertexListModelsURL(request, fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/models", region, projectID, region))

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(requestURL)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)
	tokenSource, err := getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, providerName)
	}
	// Getting oauth2 token
	token, err := tokenSource.Token()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error getting token", err, providerName)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	// Make the request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
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
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}
	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		}
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, resp.String()))
		var errorResp VertexError
		if err := sonic.Unmarshal(resp.Body(), &errorResp); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     schemas.Ptr(resp.StatusCode()),
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderResponseUnmarshal,
					Error:   err,
				},
			}
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Message: errorResp.Error.Message,
			},
		}
	}
	// Parse Vertex's response
	var vertexResponse VertexListModelsResponse
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &vertexResponse, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	// Create final response
	response := vertexResponse.ToBifrostListModelsResponse()
	// Set ExtraFields
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.RequestType = schemas.ListModelsRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TextCompletion is not supported by the Vertex provider.
// Returns an error indicating that text completion is not available.
func (provider *VertexProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("text completion", "vertex")
}

// TextCompletionStream performs a streaming text completion request to Vertex's API.
// It formats the request, sends it to Vertex, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *VertexProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("text completion stream", "vertex")
}

// ChatCompletion performs a chat completion request to the Vertex API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *VertexProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", schemas.Vertex)
	}

	// Format messages for Vertex API
	var requestBody map[string]interface{}

	if strings.Contains(request.Model, "claude") {
		// Use centralized Anthropic converter
		reqBody := anthropic.ToAnthropicChatCompletionRequest(request)
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("chat completion input is not provided", nil, schemas.Vertex)
		}

		// Convert struct to map for Vertex API
		reqBytes, _ := sonic.Marshal(reqBody)
		sonic.Unmarshal(reqBytes, &requestBody)
	} else {
		// Use centralized OpenAI converter for non-Claude models
		reqBody := openai.ToOpenAIChatRequest(request)
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("chat completion input is not provided", nil, schemas.Vertex)
		}

		// Convert struct to map for Vertex API
		reqBytes, _ := sonic.Marshal(reqBody)
		sonic.Unmarshal(reqBytes, &requestBody)
	}

	if strings.Contains(request.Model, "claude") {
		if _, exists := requestBody["anthropic_version"]; !exists {
			requestBody["anthropic_version"] = "vertex-2023-10-16"
		}

		delete(requestBody, "model")
	}

	delete(requestBody, "region")

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Vertex)
	}

	projectID := key.VertexKeyConfig.ProjectID
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", schemas.Vertex)
	}

	region := key.VertexKeyConfig.Region
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config", schemas.Vertex)
	}

	var url string
	if strings.Contains(request.Model, "claude") {
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/%s:rawPredict", projectID, request.Model)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict", region, projectID, region, request.Model)
		}
	} else {
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/openapi/chat/completions", projectID)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/openapi/chat/completions", region, projectID, region)
		}
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	// Getting oauth2 token
	tokenSource, err := getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, schemas.Vertex)
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error getting token", err, schemas.Vertex)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.SetBody(jsonBody)

	// Create request

	// Make the request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
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
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, provider.GetProviderKey())
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, provider.GetProviderKey())
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		}

		var openAIErr schemas.BifrostError

		var vertexErr []VertexError
		if err := sonic.Unmarshal(resp.Body(), &openAIErr); err != nil {
			// Try Vertex error format if OpenAI format fails
			if err := sonic.Unmarshal(resp.Body(), &vertexErr); err != nil {

				//try with single Vertex error format
				var vertexErr VertexError
				if err := sonic.Unmarshal(resp.Body(), &vertexErr); err != nil {
					return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.Vertex)
				}

				return nil, providerUtils.NewProviderAPIError(vertexErr.Error.Message, nil, resp.StatusCode(), schemas.Vertex, nil, nil)
			}

			if len(vertexErr) > 0 {
				return nil, providerUtils.NewProviderAPIError(vertexErr[0].Error.Message, nil, resp.StatusCode(), schemas.Vertex, nil, nil)
			}
		}

		return nil, providerUtils.NewProviderAPIError(openAIErr.Error.Message, nil, resp.StatusCode(), schemas.Vertex, nil, nil)
	}

	if strings.Contains(request.Model, "claude") {
		// Create response object from pool
		anthropicChatResponse := anthropic.AcquireAnthropicChatResponse()
		defer anthropic.ReleaseAnthropicChatResponse(anthropicChatResponse)

		rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), anthropicChatResponse, provider.sendBackRawResponse)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		// Create final response
		response := anthropicChatResponse.ToBifrostChatResponse()

		response.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ChatCompletionRequest,
			Provider:       schemas.Vertex,
			ModelRequested: request.Model,
			Latency:        latency.Milliseconds(),
		}

		if provider.sendBackRawResponse {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	} else {
		response := &schemas.BifrostChatResponse{}

		// Use enhanced response handler with pre-allocated response
		rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), response, provider.sendBackRawResponse)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		response.ExtraFields.RequestType = schemas.ChatCompletionRequest
		response.ExtraFields.Provider = schemas.Vertex
		response.ExtraFields.ModelRequested = request.Model
		response.ExtraFields.Latency = latency.Milliseconds()

		if provider.sendBackRawResponse {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	}
}

// ChatCompletionStream performs a streaming chat completion request to the Vertex API.
// It supports both OpenAI-style streaming (for non-Claude models) and Anthropic-style streaming (for Claude models).
// Returns a channel of BifrostResponse objects for streaming results or an error if the request fails.
func (provider *VertexProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", schemas.Vertex)
	}

	projectID := key.VertexKeyConfig.ProjectID
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", schemas.Vertex)
	}

	region := key.VertexKeyConfig.Region
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config", schemas.Vertex)
	}

	if strings.Contains(request.Model, "claude") {
		// Use Anthropic-style streaming for Claude models
		reqBody := anthropic.ToAnthropicChatCompletionRequest(request)
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("chat completion input is not provided", nil, schemas.Vertex)
		}

		reqBody.Stream = schemas.Ptr(true)

		// Convert struct to map for Vertex API
		reqBytes, _ := sonic.Marshal(reqBody)
		var requestBody map[string]interface{}
		sonic.Unmarshal(reqBytes, &requestBody)

		if _, exists := requestBody["anthropic_version"]; !exists {
			requestBody["anthropic_version"] = "vertex-2023-10-16"
		}

		delete(requestBody, "model")
		delete(requestBody, "region")

		var url string
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/%s:streamRawPredict", projectID, request.Model)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:streamRawPredict", region, projectID, region, request.Model)
		}

		// Prepare headers for Vertex Anthropic
		headers := map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "text/event-stream",
			"Cache-Control": "no-cache",
		}

		// Adding authorization header
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, schemas.Vertex)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err, schemas.Vertex)
		}
		headers["Authorization"] = "Bearer " + token.AccessToken

		// Use shared Anthropic streaming logic
		return anthropic.HandleAnthropicChatCompletionStreaming(
			ctx,
			provider.client,
			url,
			requestBody,
			headers,
			provider.networkConfig.ExtraHeaders,
			provider.sendBackRawResponse,
			schemas.Vertex,
			postHookRunner,
			provider.logger,
		)
	} else {
		var url string
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/openapi/chat/completions", projectID)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/openapi/chat/completions", region, projectID, region)
		}
		authHeader := map[string]string{}
		if key.Value != "" {
			authHeader["Authorization"] = "Bearer " + key.Value
		}
		// Use shared OpenAI streaming logic
		return openai.HandleOpenAIChatCompletionStreaming(
			ctx,
			provider.client,
			url,
			request,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			provider.sendBackRawResponse,
			schemas.Vertex,
			postHookRunner,
			provider.logger,
		)
	}
}

// Responses performs a responses request to the Vertex API.
func (provider *VertexProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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

// ResponsesStream performs a streaming responses request to the Vertex API.
func (provider *VertexProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return provider.ChatCompletionStream(
		ctx,
		providerUtils.GetResponsesChunkConverterCombinedPostHookRunner(postHookRunner),
		key,
		request.ToChatRequest(),
	)
}

// Embedding generates embeddings for the given input text(s) using Vertex AI.
// All Vertex AI embedding models use the same response format regardless of the model type.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *VertexProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", schemas.Vertex)
	}

	projectID := key.VertexKeyConfig.ProjectID
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", schemas.Vertex)
	}

	region := key.VertexKeyConfig.Region
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config", schemas.Vertex)
	}

	// Use centralized Vertex converter
	reqBody := ToVertexEmbeddingRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewConfigurationError("embedding input texts are empty", schemas.Vertex)
	}

	// All Vertex AI embedding models use the same native Vertex embedding API
	return provider.handleVertexEmbedding(ctx, request.Model, key, reqBody)
}

// handleVertexEmbedding handles embedding requests using Vertex's native embedding API
// This is used for all Vertex AI embedding models as they all use the same response format
func (provider *VertexProvider) handleVertexEmbedding(ctx context.Context, model string, key schemas.Key, vertexReq *VertexEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	// Use the typed request directly
	jsonBody, err := sonic.Marshal(vertexReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Vertex)
	}

	// Build the native Vertex embedding API endpoint
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		key.VertexKeyConfig.Region, key.VertexKeyConfig.ProjectID, key.VertexKeyConfig.Region, model)

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	// Getting oauth2 token
	tokenSource, err := getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, schemas.Vertex)
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error getting token", err, schemas.Vertex)
	}
	req.Header.Set("Authorization", "Bearer " + token.AccessToken)

	req.SetBody(jsonBody)

	
	// Set any extra headers from network config
	

	// Make the request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
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
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, provider.GetProviderKey())
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, provider.GetProviderKey())
	}

	
	if resp.StatusCode() != fasthttp.StatusOK {
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		}

		// Try to parse Vertex's error format
		var vertexError map[string]interface{}
		if err := sonic.Unmarshal(resp.Body(), &vertexError); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.Vertex)
		}

		// Extract error message from Vertex's error format
		errorMessage := "Unknown error"
		if errorObj, exists := vertexError["error"]; exists {
			if errorMap, ok := errorObj.(map[string]interface{}); ok {
				if message, exists := errorMap["message"]; exists {
					if msgStr, ok := message.(string); ok {
						errorMessage = msgStr
					}
				}
			}
		}

		return nil, providerUtils.NewProviderAPIError(errorMessage, nil, resp.StatusCode(), schemas.Vertex, nil, nil)
	}

	// Parse Vertex's native embedding response using typed response
	var vertexResponse VertexEmbeddingResponse
	if err := sonic.Unmarshal(resp.Body(), &vertexResponse); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.Vertex)
	}

	// Use centralized Vertex converter
	bifrostResponse := vertexResponse.ToBifrostEmbeddingResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = schemas.Vertex
	bifrostResponse.ExtraFields.ModelRequested = model
	bifrostResponse.ExtraFields.RequestType = schemas.EmbeddingRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		// Convert back to map for raw response
		rawResponseBytes, _ := sonic.Marshal(&vertexResponse)
		var rawResponseMap map[string]interface{}
		sonic.Unmarshal(rawResponseBytes, &rawResponseMap)
		bifrostResponse.ExtraFields.RawResponse = rawResponseMap
	}

	return bifrostResponse, nil
}

// Speech is not supported by the Vertex provider.
func (provider *VertexProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("speech", "vertex")
}

// SpeechStream is not supported by the Vertex provider.
func (provider *VertexProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("speech stream", "vertex")
}

// Transcription is not supported by the Vertex provider.
func (provider *VertexProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("transcription", "vertex")
}

// TranscriptionStream is not supported by the Vertex provider.
func (provider *VertexProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("transcription stream", "vertex")
}
