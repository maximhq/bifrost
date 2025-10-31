package vertex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

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
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewVertexProvider creates a new Vertex provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewVertexProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*VertexProvider, error) {
	config.CheckAndSetDefaults()
	return &VertexProvider{
		logger:              logger,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// getAuthClient returns an authenticated HTTP client for Vertex AI API requests.
// This function implements client pooling to avoid creating and authenticating
// clients for every request, which significantly improves performance by:
// - Avoiding repeated JWT config creation
// - Reusing OAuth2 token refresh logic
// - Reducing authentication overhead
func getAuthClient(key schemas.Key) (*http.Client, error) {
	if key.VertexKeyConfig == nil {
		return nil, fmt.Errorf("vertex key config is not set")
	}

	authCredentials := key.VertexKeyConfig.AuthCredentials
	var client *http.Client
	// Generate cache key from credentials
	clientKey := getClientKey(authCredentials)

	// Try to get existing client from pool
	if value, exists := vertexClientPool.Load(clientKey); exists {
		return value.(*http.Client), nil
	}

	if authCredentials == "" {
		// When auth credentials are not explicitly set, use default credentials
		// This will automatically detect credentials from the environment/server
		var err error
		client, err = google.DefaultClient(context.Background(), cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("failed to create default client: %w", err)
		}
	} else {
		conf, err := google.JWTConfigFromJSON([]byte(authCredentials), cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("failed to create JWT config: %w", err)
		}
		client = conf.Client(context.Background())
	}

	// Store the client using LoadOrStore to handle race conditions
	// If another goroutine stored a client while we were creating ours, use theirs
	actual, _ := vertexClientPool.LoadOrStore(clientKey, client)
	return actual.(*http.Client), nil
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

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
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
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Set any extra headers from network config
	providerUtils.SetExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	req.Header.Set("Content-Type", "application/json")

	client, err := getAuthClient(key)
	if err != nil {
		// Remove client from pool if auth client creation fails
		removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		return nil, providerUtils.NewBifrostOperationError("error creating auth client", err, providerName)
	}

	// Make request and measure latency
	startTime := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(startTime)
	if err != nil {
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
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error reading response",
				Error:   err,
			},
		}
	}

	// Handle error response
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		}
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(body)))

		var errorResp VertexError

		if err := sonic.Unmarshal(body, &errorResp); err != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &resp.StatusCode,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderResponseUnmarshal,
					Error:   err,
				},
			}
		}

		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &resp.StatusCode,
			Error: &schemas.ErrorField{
				Message: errorResp.Error.Message,
			},
		}
	}

	// Parse Vertex's response
	var vertexResponse VertexListModelsResponse
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &vertexResponse, provider.sendBackRawResponse)
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

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
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
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, schemas.Vertex)
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Set any extra headers from network config
	providerUtils.SetExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	req.Header.Set("Content-Type", "application/json")

	client, err := getAuthClient(key)
	if err != nil {
		// Remove client from pool if auth client creation fails
		removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		return nil, providerUtils.NewBifrostOperationError("error creating auth client", err, schemas.Vertex)
	}

	startTime := time.Now()

	// Make request
	resp, err := client.Do(req)
	if err != nil {
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
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, schemas.Vertex)
		}
		// Remove client from pool for non-context errors (could be auth/network issues)
		removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, schemas.Vertex)
	}
	defer resp.Body.Close()

	latency := time.Since(startTime)

	// Handle error response
	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err, schemas.Vertex)
	}

	if resp.StatusCode != http.StatusOK {
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		}

		var openAIErr schemas.BifrostError

		var vertexErr []VertexError
		if err := sonic.Unmarshal(body, &openAIErr); err != nil {
			// Try Vertex error format if OpenAI format fails
			if err := sonic.Unmarshal(body, &vertexErr); err != nil {

				//try with single Vertex error format
				var vertexErr VertexError
				if err := sonic.Unmarshal(body, &vertexErr); err != nil {
					return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.Vertex)
				}

				return nil, providerUtils.NewProviderAPIError(vertexErr.Error.Message, nil, resp.StatusCode, schemas.Vertex, nil, nil)
			}

			if len(vertexErr) > 0 {
				return nil, providerUtils.NewProviderAPIError(vertexErr[0].Error.Message, nil, resp.StatusCode, schemas.Vertex, nil, nil)
			}
		}

		return nil, providerUtils.NewProviderAPIError(openAIErr.Error.Message, nil, resp.StatusCode, schemas.Vertex, nil, nil)
	}

	if strings.Contains(request.Model, "claude") {
		// Create response object from pool
		anthropicChatResponse := anthropic.AcquireAnthropicChatResponse()
		defer anthropic.ReleaseAnthropicChatResponse(anthropicChatResponse)

		rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, anthropicChatResponse, provider.sendBackRawResponse)
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
		rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, provider.sendBackRawResponse)
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

	client, err := getAuthClient(key)
	if err != nil {
		// Remove client from pool if auth client creation fails
		removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		return nil, providerUtils.NewBifrostOperationError("error creating auth client", err, schemas.Vertex)
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

		// Use shared Anthropic streaming logic
		return anthropic.HandleAnthropicChatCompletionStreaming(
			ctx,
			client,
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
			client,
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

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
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
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, schemas.Vertex)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, schemas.Vertex)
	}

	// Set any extra headers from network config
	providerUtils.SetExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	req.Header.Set("Content-Type", "application/json")

	client, err := getAuthClient(key)
	if err != nil {
		// Remove client from pool if auth client creation fails
		removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		return nil, providerUtils.NewBifrostOperationError("error creating auth client", err, schemas.Vertex)
	}

	startTime := time.Now()

	// Make request
	resp, err := client.Do(req)
	if err != nil {
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
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, schemas.Vertex)
		}
		// Remove client from pool for non-context errors (could be auth/network issues)
		removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, schemas.Vertex)
	}
	defer resp.Body.Close()

	latency := time.Since(startTime)

	// Handle error response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err, schemas.Vertex)
	}

	if resp.StatusCode != http.StatusOK {
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		}

		// Try to parse Vertex's error format
		var vertexError map[string]interface{}
		if err := sonic.Unmarshal(body, &vertexError); err != nil {
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

		return nil, providerUtils.NewProviderAPIError(errorMessage, nil, resp.StatusCode, schemas.Vertex, nil, nil)
	}

	// Parse Vertex's native embedding response using typed response
	var vertexResponse VertexEmbeddingResponse
	if err := sonic.Unmarshal(body, &vertexResponse); err != nil {
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
