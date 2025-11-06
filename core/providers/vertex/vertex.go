package vertex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
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

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
// Handles pagination automatically by following nextPageToken until all models are retrieved.
func (provider *VertexProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
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

	var host string
	if region == "global" {
		host = "aiplatform.googleapis.com"
	} else {
		host = fmt.Sprintf("%s-aiplatform.googleapis.com", region)
	}

	// Accumulate all models from paginated requests
	var allModels []VertexModel
	var totalLatency time.Duration
	var rawResponses []interface{}
	pageToken := ""

	// Getting oauth2 token
	tokenSource, err := getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, schemas.Vertex)
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error getting token", err, schemas.Vertex)
	}

	// Loop through all pages until no nextPageToken is returned
	for {
		// Build URL with pagination parameters
		requestURL := fmt.Sprintf("https://%s/v1/projects/%s/locations/%s/models?pageSize=%d", host, projectID, region, MaxPageSize)
		if pageToken != "" {
			requestURL = fmt.Sprintf("%s&pageToken=%s", requestURL, url.QueryEscape(pageToken))
		}

		// Create HTTP request for listing models
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.Header.SetMethod(http.MethodGet)
		req.SetRequestURI(requestURL)
		req.Header.SetContentType("application/json")
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)

		_, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
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
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, provider.GetProviderKey())
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
				removeVertexClient(key.VertexKeyConfig.AuthCredentials)
			}

			var errorResp VertexError
			if err := sonic.Unmarshal(resp.Body(), &errorResp); err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.Vertex)
			}
			return nil, providerUtils.NewProviderAPIError(errorResp.Error.Message, nil, resp.StatusCode(), schemas.Vertex, nil, nil)
		}

		// Parse Vertex's response
		var vertexResponse VertexListModelsResponse
		rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &vertexResponse, provider.sendBackRawResponse)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			rawResponses = append(rawResponses, rawResponse)
		}

		// Accumulate models from this page
		allModels = append(allModels, vertexResponse.Models...)

		// Check if there are more pages
		if vertexResponse.NextPageToken == "" {
			break
		}
		pageToken = vertexResponse.NextPageToken
	}

	// Create aggregated response from all pages
	aggregatedResponse := &VertexListModelsResponse{
		Models: allModels,
	}
	response := aggregatedResponse.ToBifrostListModelsResponse()
	response.ExtraFields.Latency = totalLatency.Milliseconds()

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponses
	}

	return response, nil
}

// ListModels performs a list models request to Vertex's API.
// Requests are made concurrently for improved performance.
func (provider *VertexProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
		provider.logger,
	)
}

// TextCompletion is not supported by the Vertex provider.
// Returns an error indicating that text completion is not available.
func (provider *VertexProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream performs a streaming text completion request to Vertex's API.
// It formats the request, sends it to Vertex, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *VertexProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to the Vertex API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *VertexProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			//TODO: optimize this double Marshal
			// Format messages for Vertex API
			var requestBody map[string]interface{}

			if strings.Contains(request.Model, "claude") {
				// Use centralized Anthropic converter
				reqBody := anthropic.ToAnthropicChatCompletionRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}

				// Convert struct to map for Vertex API
				reqBytes, err := sonic.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
				if err := sonic.Unmarshal(reqBytes, &requestBody); err != nil {
					return nil, fmt.Errorf("failed to unmarshal request body: %w", err)
				}
			} else {
				// Use centralized OpenAI converter for non-Claude models
				reqBody := openai.ToOpenAIChatRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}

				// Convert struct to map for Vertex API
				reqBytes, err := sonic.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
				if err := sonic.Unmarshal(reqBytes, &requestBody); err != nil {
					return nil, fmt.Errorf("failed to unmarshal request body: %w", err)
				}
			}

			if strings.Contains(request.Model, "claude") {
				if _, exists := requestBody["anthropic_version"]; !exists {
					requestBody["anthropic_version"] = DefaultVertexAnthropicVersion
				}

				delete(requestBody, "model")
			}

			delete(requestBody, "region")

			return requestBody, nil

		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	projectID := key.VertexKeyConfig.ProjectID
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
	}

	region := key.VertexKeyConfig.Region
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
	}

	// Determine the URL based on model type
	var url string
	if strings.Contains(request.Model, "claude") {
		// Claude models use Anthropic publisher
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/%s:rawPredict", projectID, request.Model)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict", region, projectID, region, request.Model)
		}
	} else if providerUtils.IsVertexMistralModel(request.Model) {
		// Mistral models use mistralai publisher with rawPredict
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/mistralai/models/%s:rawPredict", projectID, request.Model)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/mistralai/models/%s:rawPredict", region, projectID, region, request.Model)
		}
	} else {
		// Other models use OpenAPI endpoint
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
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

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
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, provider.GetProviderKey())
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials)
		}

		var openAIErr schemas.BifrostError

		var vertexErr []VertexError
		if err := sonic.Unmarshal(resp.Body(), &openAIErr); err != nil || openAIErr.Error == nil {
			// Try Vertex error format if OpenAI format fails or is incomplete
			if err := sonic.Unmarshal(resp.Body(), &vertexErr); err != nil {

				//try with single Vertex error format
				var vertexErr VertexError
				if err := sonic.Unmarshal(resp.Body(), &vertexErr); err != nil {
					// Try VertexValidationError format (validation errors from Mistral endpoint)
					var validationErr VertexValidationError
					if err := sonic.Unmarshal(resp.Body(), &validationErr); err != nil {
						return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.Vertex)
					}
					if len(validationErr.Detail) > 0 {
						return nil, providerUtils.NewProviderAPIError(validationErr.Detail[0].Msg, nil, resp.StatusCode(), schemas.Vertex, nil, nil)
					}
					return nil, providerUtils.NewProviderAPIError("Unknown error", nil, resp.StatusCode(), schemas.Vertex, nil, nil)
				}

				return nil, providerUtils.NewProviderAPIError(vertexErr.Error.Message, nil, resp.StatusCode(), schemas.Vertex, nil, nil)
			}

			if len(vertexErr) > 0 {
				return nil, providerUtils.NewProviderAPIError(vertexErr[0].Error.Message, nil, resp.StatusCode(), schemas.Vertex, nil, nil)
			}

			return nil, providerUtils.NewProviderAPIError("Unknown error", nil, resp.StatusCode(), schemas.Vertex, nil, nil)
		} else {
			// OpenAI error format succeeded with valid Error field
			return nil, providerUtils.NewProviderAPIError(openAIErr.Error.Message, nil, resp.StatusCode(), schemas.Vertex, nil, nil)
		}
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
			Provider:       providerName,
			ModelRequested: request.Model,
			Latency:        latency.Milliseconds(),
		}

		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
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
		response.ExtraFields.Provider = providerName
		response.ExtraFields.ModelRequested = request.Model
		response.ExtraFields.Latency = latency.Milliseconds()

		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	}
}

// ChatCompletionStream performs a streaming chat completion request to the Vertex API.
// It supports both OpenAI-style streaming (for non-Claude models) and Anthropic-style streaming (for Claude models).
// Returns a channel of BifrostResponse objects for streaming results or an error if the request fails.
func (provider *VertexProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
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

	if strings.Contains(request.Model, "claude") {
		// Use Anthropic-style streaming for Claude models
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) {
				reqBody := anthropic.ToAnthropicChatCompletionRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}

				reqBody.Stream = schemas.Ptr(true)

				// Convert struct to map for Vertex API
				reqBytes, err := sonic.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
				var requestBody map[string]interface{}
				if err := sonic.Unmarshal(reqBytes, &requestBody); err != nil {
					return nil, fmt.Errorf("failed to unmarshal request body: %w", err)
				}

				if _, exists := requestBody["anthropic_version"]; !exists {
					requestBody["anthropic_version"] = DefaultVertexAnthropicVersion
				}

				delete(requestBody, "model")
				delete(requestBody, "region")
				return requestBody, nil
			},
			provider.GetProviderKey())
		if bifrostErr != nil {
			return nil, bifrostErr
		}

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
			jsonData,
			headers,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			providerName,
			postHookRunner,
			provider.logger,
		)
	} else {
		// Use OpenAI-compatible streaming for Mistral and other models
		var url string
		if providerUtils.IsVertexMistralModel(request.Model) {
			// Mistral models use mistralai publisher with streamRawPredict
			if region == "global" {
				url = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/mistralai/models/%s:streamRawPredict", projectID, request.Model)
			} else {
				url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/mistralai/models/%s:streamRawPredict", region, projectID, region, request.Model)
			}
		} else {
			// Other models use OpenAPI endpoint
			if region == "global" {
				url = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/openapi/chat/completions", projectID)
			} else {
				url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/openapi/chat/completions", region, projectID, region)
			}
		}

		// Getting oauth2 token
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, schemas.Vertex)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err, schemas.Vertex)
		}

		authHeader := map[string]string{
			"Authorization": "Bearer " + token.AccessToken,
		}

		// Use shared OpenAI streaming logic
		return openai.HandleOpenAIChatCompletionStreaming(
			ctx,
			provider.client,
			url,
			request,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			providerName,
			postHookRunner,
			nil,
			nil,
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

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToVertexEmbeddingRequest(request), nil },
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Build the native Vertex embedding API endpoint
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		key.VertexKeyConfig.Region, key.VertexKeyConfig.ProjectID, key.VertexKeyConfig.Region, request.Model)

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

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
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, provider.GetProviderKey())
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
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.EmbeddingRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponseMap map[string]interface{}
		if err := sonic.Unmarshal(resp.Body(), &rawResponseMap); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRawResponseUnmarshal, err, providerName)
		}
		bifrostResponse.ExtraFields.RawResponse = rawResponseMap
	}

	return bifrostResponse, nil
}

// Speech is not supported by the Vertex provider.
func (provider *VertexProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Vertex provider.
func (provider *VertexProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Vertex provider.
func (provider *VertexProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Vertex provider.
func (provider *VertexProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}
