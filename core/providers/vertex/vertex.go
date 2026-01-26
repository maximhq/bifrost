package vertex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/maximhq/bifrost/core/providers/gemini"
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
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
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
		sendBackRawRequest:  config.SendBackRawRequest,
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
	if authCredentials.GetValue() == "" {
		creds, err := google.FindDefaultCredentials(context.Background(), cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("failed to find default credentials in environment: %w", err)
		}
		tokenSource = creds.TokenSource
	} else {
		conf, err := google.JWTConfigFromJSON([]byte(authCredentials.GetValue()), cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("failed to create JWT config from auth credentials: %w", err)
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
func (provider *VertexProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}

	projectID := key.VertexKeyConfig.ProjectID
	if projectID.GetValue() == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
	}

	region := key.VertexKeyConfig.Region.GetValue()
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
	var rawRequests []interface{}
	var rawResponses []interface{}
	pageToken := ""

	// Getting oauth2 token
	tokenSource, err := getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source (api key auth not supported for list models)", err, schemas.Vertex)
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error getting token (api key auth not supported for list models)", err, schemas.Vertex)
	}

	// Loop through all pages until no nextPageToken is returned
	for {
		// Build URL with pagination parameters
		requestURL := fmt.Sprintf("https://%s/v1/projects/%s/locations/%s/models?pageSize=%d", host, projectID.GetValue(), region, MaxPageSize)
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
			return nil, providerUtils.EnrichError(ctx, bifrostErr, nil, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
				removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
			}

			var errorResp VertexError
			if err := sonic.Unmarshal(resp.Body(), &errorResp); err != nil {
				return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.Vertex), nil, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
			}
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewProviderAPIError(errorResp.Error.Message, nil, resp.StatusCode(), schemas.Vertex, nil, nil), nil, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		// Parse Vertex's response
		var vertexResponse VertexListModelsResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &vertexResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, nil, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			rawRequests = append(rawRequests, rawRequest)
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
	response := aggregatedResponse.ToBifrostListModelsResponse(key.Models, key.VertexKeyConfig.Deployments)

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequests
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponses
	}

	return response, nil
}

// ListModels performs a list models request to Vertex's API.
// Requests are made concurrently for improved performance.
func (provider *VertexProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	finalResponse, bifrostErr := providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
		provider.logger,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return finalResponse, nil
}

// TextCompletion is not supported by the Vertex provider.
// Returns an error indicating that text completion is not available.
func (provider *VertexProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream performs a streaming text completion request to Vertex's API.
// It formats the request, sends it to Vertex, and processes the response.
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *VertexProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to the Vertex API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *VertexProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}

	deployment := provider.getModelDeployment(key, request.Model)
	// strip google/ prefix if present
	if after, ok := strings.CutPrefix(deployment, "google/"); ok {
		deployment = after
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			//TODO: optimize this double Marshal
			// Format messages for Vertex API
			var requestBody map[string]interface{}

			if schemas.IsAnthropicModel(deployment) {
				// Use centralized Anthropic converter
				reqBody, err := anthropic.ToAnthropicChatRequest(ctx, request)
				if err != nil {
					return nil, err
				}
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}
				reqBody.Model = deployment
				// Convert struct to map for Vertex API
				reqBytes, err := sonic.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
				if err := sonic.Unmarshal(reqBytes, &requestBody); err != nil {
					return nil, fmt.Errorf("failed to unmarshal request body: %w", err)
				}
			} else if schemas.IsGeminiModel(deployment) || schemas.IsAllDigitsASCII(deployment) {
				reqBody := gemini.ToGeminiChatCompletionRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}
				reqBody.Model = deployment
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
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
				reqBody.Model = deployment
				// Convert struct to map for Vertex API
				reqBytes, err := sonic.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
				if err := sonic.Unmarshal(reqBytes, &requestBody); err != nil {
					return nil, fmt.Errorf("failed to unmarshal request body: %w", err)
				}
			}

			if schemas.IsAnthropicModel(deployment) {
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

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
	}

	// Auth query is used for fine-tuned models to pass the API key in the query string
	authQuery := ""
	// Determine the URL based on model type
	var completeURL string
	if schemas.IsAllDigitsASCII(deployment) {
		// Custom Fine-tuned models use OpenAPI endpoint
		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models", providerName)
		}
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/%s:generateContent", projectNumber, deployment)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/%s:generateContent", region, projectNumber, region, deployment)
		}
	} else if schemas.IsAnthropicModel(deployment) {
		// Claude models use Anthropic publisher
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/%s:rawPredict", projectID, deployment)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict", region, projectID, region, deployment)
		}
	} else if schemas.IsMistralModel(deployment) {
		// Mistral models use mistralai publisher with rawPredict
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/mistralai/models/%s:rawPredict", projectID, deployment)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/mistralai/models/%s:rawPredict", region, projectID, region, deployment)
		}
	} else if schemas.IsGeminiModel(deployment) {
		// Gemini models support api key
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s:generateContent", projectID, deployment)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent", region, projectID, region, deployment)
		}
	} else {
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/openapi/chat/completions", projectID)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/openapi/chat/completions", region, projectID, region)
		}
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// If auth query is set, add it to the URL
	// Otherwise, get the oauth2 token and set the Authorization header
	if authQuery != "" {
		completeURL = fmt.Sprintf("%s?%s", completeURL, authQuery)
	} else {
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
	}

	req.SetRequestURI(completeURL)
	req.SetBody(jsonBody)

	// Make the request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}
		return nil, providerUtils.EnrichError(ctx, parseVertexError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.ChatCompletionRequest,
		}), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	if schemas.IsAnthropicModel(deployment) {
		// Create response object from pool
		anthropicResponse := anthropic.AcquireAnthropicMessageResponse()
		defer anthropic.ReleaseAnthropicMessageResponse(anthropicResponse)

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), anthropicResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		// Create final response
		response := anthropicResponse.ToBifrostChatResponse(ctx)

		response.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ChatCompletionRequest,
			Provider:       providerName,
			ModelRequested: request.Model,
			Latency:        latency.Milliseconds(),
		}

		response.ExtraFields.ModelRequested = request.Model
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}

		// Set raw request if enabled
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}

		// Set raw response if enabled
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	} else if schemas.IsGeminiModel(deployment) || schemas.IsAllDigitsASCII(deployment) {
		geminiResponse := gemini.GenerateContentResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &geminiResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response := geminiResponse.ToBifrostChatResponse()
		response.ExtraFields.RequestType = schemas.ChatCompletionRequest
		response.ExtraFields.Provider = providerName
		response.ExtraFields.ModelRequested = request.Model
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}
		response.ExtraFields.Latency = latency.Milliseconds()

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}

		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	} else {
		response := &schemas.BifrostChatResponse{}

		// Use enhanced response handler with pre-allocated response
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), response, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response.ExtraFields.RequestType = schemas.ChatCompletionRequest
		response.ExtraFields.Provider = providerName
		response.ExtraFields.ModelRequested = request.Model
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
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
}

// ChatCompletionStream performs a streaming chat completion request to the Vertex API.
// It supports both OpenAI-style streaming (for non-Claude models) and Anthropic-style streaming (for Claude models).
// Returns a channel of BifrostStreamChunk objects for streaming results or an error if the request fails.
func (provider *VertexProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
	}

	deployment := provider.getModelDeployment(key, request.Model)
	// strip google/ prefix if present
	if after, ok := strings.CutPrefix(deployment, "google/"); ok {
		deployment = after
	}

	postResponseConverter := func(response *schemas.BifrostChatResponse) *schemas.BifrostChatResponse {
		response.ExtraFields.ModelRequested = request.Model
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}
		return response
	}

	if schemas.IsAnthropicModel(deployment) {
		// Use Anthropic-style streaming for Claude models
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
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

		var completeURL string
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/%s:streamRawPredict", projectID, deployment)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:streamRawPredict", region, projectID, region, deployment)
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
			completeURL,
			jsonData,
			headers,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			providerName,
			postHookRunner,
			postResponseConverter,
			provider.logger,
			&providerUtils.RequestMetadata{
				Provider:    providerName,
				Model:       request.Model,
				RequestType: schemas.ChatCompletionStreamRequest,
			},
		)
	} else if schemas.IsGeminiModel(deployment) || schemas.IsAllDigitsASCII(deployment) {
		// Use Gemini-style streaming for Gemini models
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) {
				reqBody := gemini.ToGeminiChatCompletionRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}
				reqBody.Model = deployment
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				return reqBody, nil
			},
			provider.GetProviderKey())
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		// Auth query is used to pass the API key in the query string
		authQuery := ""
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}

		// For custom/fine-tuned models, validate projectNumber is set
		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if schemas.IsAllDigitsASCII(deployment) && projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models", providerName)
		}

		// Construct the URL for Gemini streaming
		completeURL := getCompleteURLForGeminiEndpoint(deployment, region, projectID, projectNumber, ":streamGenerateContent")

		// Add alt=sse parameter
		if authQuery != "" {
			completeURL = fmt.Sprintf("%s?alt=sse&%s", completeURL, authQuery)
		} else {
			completeURL = fmt.Sprintf("%s?alt=sse", completeURL)
		}

		// Prepare headers for Vertex Gemini
		headers := map[string]string{
			"Accept":        "text/event-stream",
			"Cache-Control": "no-cache",
		}

		// If no auth query, use OAuth2 token
		if authQuery == "" {
			tokenSource, err := getAuthTokenSource(key)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, schemas.Vertex)
			}
			token, err := tokenSource.Token()
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error getting token", err, schemas.Vertex)
			}
			headers["Authorization"] = "Bearer " + token.AccessToken
		}

		// Use shared streaming logic from Gemini
		return gemini.HandleGeminiChatCompletionStream(
			ctx,
			provider.client,
			completeURL,
			jsonData,
			headers,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			request.Model,
			postHookRunner,
			postResponseConverter,
			provider.logger,
		)
	} else {
		var authHeader map[string]string
		// Auth query is used for fine-tuned models to pass the API key in the query string
		authQuery := ""
		// Determine the URL based on model type
		var completeURL string
		if schemas.IsMistralModel(deployment) {
			// Mistral models use mistralai publisher with streamRawPredict
			if region == "global" {
				completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/mistralai/models/%s:streamRawPredict", projectID, deployment)
			} else {
				completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/mistralai/models/%s:streamRawPredict", region, projectID, region, deployment)
			}
		} else {
			// Other models use OpenAPI endpoint for gemini models
			if key.Value.GetValue() != "" {
				authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
			}
			if region == "global" {
				completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/openapi/chat/completions", projectID)
			} else {
				completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/openapi/chat/completions", region, projectID, region)
			}
		}

		if authQuery != "" {
			completeURL = fmt.Sprintf("%s?%s", completeURL, authQuery)
		} else {
			// Getting oauth2 token
			tokenSource, err := getAuthTokenSource(key)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, schemas.Vertex)
			}
			token, err := tokenSource.Token()
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error getting token", err, schemas.Vertex)
			}
			authHeader = map[string]string{
				"Authorization": "Bearer " + token.AccessToken,
			}
		}

		postRequestConverter := func(reqBody *openai.OpenAIChatRequest) *openai.OpenAIChatRequest {
			reqBody.Model = deployment
			return reqBody
		}

		// Use shared OpenAI streaming logic
		return openai.HandleOpenAIChatCompletionStreaming(
			ctx,
			provider.client,
			completeURL,
			request,
			authHeader,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			providerName,
			postHookRunner,
			nil,
			nil,
			postRequestConverter,
			postResponseConverter,
			provider.logger,
		)
	}
}

// Responses performs a responses request to the Vertex API.
func (provider *VertexProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}

	deployment := provider.getModelDeployment(key, request.Model)
	// strip google/ prefix if present
	if after, ok := strings.CutPrefix(deployment, "google/"); ok {
		deployment = after
	}

	if schemas.IsAnthropicModel(deployment) {
		jsonBody, bifrostErr := getRequestBodyForAnthropicResponses(ctx, request, deployment, providerName, false)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		projectID := key.VertexKeyConfig.ProjectID.GetValue()
		if projectID == "" {
			return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
		}

		region := key.VertexKeyConfig.Region.GetValue()
		if region == "" {
			return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
		}

		// Claude models use Anthropic publisher
		var url string
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/publishers/anthropic/models/%s:rawPredict", projectID, deployment)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict", region, projectID, region, deployment)
		}

		// Create HTTP request for streaming
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.Header.SetMethod(http.MethodPost)
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

		req.SetRequestURI(url)
		req.SetBody(jsonBody)

		// Make the request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		if resp.StatusCode() != fasthttp.StatusOK {
			// Remove client from pool for authentication/authorization errors
			if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
				removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
			}
			return nil, providerUtils.EnrichError(ctx, parseVertexError(resp, &providerUtils.RequestMetadata{
				Provider:    providerName,
				Model:       request.Model,
				RequestType: schemas.ResponsesRequest,
			}), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		// Create response object from pool
		anthropicResponse := anthropic.AcquireAnthropicMessageResponse()
		defer anthropic.ReleaseAnthropicMessageResponse(anthropicResponse)

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), anthropicResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		// Create final response
		response := anthropicResponse.ToBifrostResponsesResponse(ctx)

		response.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesRequest,
			Provider:       providerName,
			ModelRequested: request.Model,
			Latency:        latency.Milliseconds(),
		}

		response.ExtraFields.ModelRequested = request.Model

		// Set raw request if enabled
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}

		// Set raw response if enabled
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}

		return response, nil
	} else if schemas.IsGeminiModel(deployment) || schemas.IsAllDigitsASCII(deployment) {
		jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) {
				reqBody := gemini.ToGeminiResponsesRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("responses input is not provided")
				}
				reqBody.Model = deployment
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				return reqBody, nil
			},
			provider.GetProviderKey())
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		projectID := key.VertexKeyConfig.ProjectID.GetValue()
		if projectID == "" {
			return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
		}

		region := key.VertexKeyConfig.Region.GetValue()
		if region == "" {
			return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
		}

		authQuery := ""
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}

		// For custom/fine-tuned models, validate projectNumber is set
		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if schemas.IsAllDigitsASCII(deployment) && projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models", providerName)
		}

		url := getCompleteURLForGeminiEndpoint(deployment, region, projectID, projectNumber, ":generateContent")

		// Create HTTP request for streaming
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.Header.SetMethod(http.MethodPost)
		req.Header.SetContentType("application/json")
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

		// If auth query is set, add it to the URL
		// Otherwise, get the oauth2 token and set the Authorization header
		if authQuery != "" {
			url = fmt.Sprintf("%s?%s", url, authQuery)
		} else {
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
		}

		req.SetRequestURI(url)
		req.SetBody(jsonBody)

		// Make the request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		if resp.StatusCode() != fasthttp.StatusOK {
			// Remove client from pool for authentication/authorization errors
			if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
				removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
			}
			return nil, providerUtils.EnrichError(ctx, parseVertexError(resp, &providerUtils.RequestMetadata{
				Provider:    providerName,
				Model:       request.Model,
				RequestType: schemas.ResponsesRequest,
			}), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		geminiResponse := &gemini.GenerateContentResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), geminiResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response := geminiResponse.ToResponsesBifrostResponsesResponse()
		response.ExtraFields.RequestType = schemas.ResponsesRequest
		response.ExtraFields.Provider = providerName
		response.ExtraFields.ModelRequested = request.Model
		response.ExtraFields.Latency = latency.Milliseconds()

		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}

		// Set raw response if enabled
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}

		return response, nil
	} else {
		chatResponse, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
		if err != nil {
			return nil, err
		}

		response := chatResponse.ToBifrostResponsesResponse()
		response.ExtraFields.RequestType = schemas.ResponsesRequest
		response.ExtraFields.Provider = provider.GetProviderKey()
		response.ExtraFields.ModelRequested = request.Model

		response.ExtraFields.ModelRequested = request.Model
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}

		return response, nil
	}
}

// ResponsesStream performs a streaming responses request to the Vertex API.
func (provider *VertexProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}

	deployment := provider.getModelDeployment(key, request.Model)
	// strip google/ prefix if present
	if after, ok := strings.CutPrefix(deployment, "google/"); ok {
		deployment = after
	}

	if schemas.IsAnthropicModel(deployment) {
		region := key.VertexKeyConfig.Region.GetValue()
		if region == "" {
			return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
		}

		projectID := key.VertexKeyConfig.ProjectID.GetValue()
		if projectID == "" {
			return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
		}

		jsonBody, bifrostErr := getRequestBodyForAnthropicResponses(ctx, request, deployment, providerName, true)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		var url string
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/%s:streamRawPredict", projectID, deployment)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:streamRawPredict", region, projectID, region, deployment)
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

		postResponseConverter := func(response *schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse {
			response.ExtraFields.ModelRequested = request.Model
			if request.Model != deployment {
				response.ExtraFields.ModelDeployment = deployment
			}
			return response
		}

		// Use shared streaming logic from Anthropic
		return anthropic.HandleAnthropicResponsesStream(
			ctx,
			provider.client,
			url,
			jsonBody,
			headers,
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
	} else if schemas.IsGeminiModel(deployment) || schemas.IsAllDigitsASCII(deployment) {
		region := key.VertexKeyConfig.Region.GetValue()
		if region == "" {
			return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
		}

		projectID := key.VertexKeyConfig.ProjectID.GetValue()
		if projectID == "" {
			return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
		}

		// Use Gemini-style streaming for Gemini models
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) {
				reqBody := gemini.ToGeminiResponsesRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("responses input is not provided")
				}
				reqBody.Model = deployment
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				return reqBody, nil
			},
			provider.GetProviderKey())
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		// Auth query is used to pass the API key in the query string
		authQuery := ""
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}

		// For custom/fine-tuned models, validate projectNumber is set
		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if schemas.IsAllDigitsASCII(deployment) && projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models", providerName)
		}

		// Construct the URL for Gemini streaming
		completeURL := getCompleteURLForGeminiEndpoint(deployment, region, projectID, projectNumber, ":streamGenerateContent")
		// Add alt=sse parameter
		if authQuery != "" {
			completeURL = fmt.Sprintf("%s?alt=sse&%s", completeURL, authQuery)
		} else {
			completeURL = fmt.Sprintf("%s?alt=sse", completeURL)
		}

		// Prepare headers for Vertex Gemini
		headers := map[string]string{
			"Accept":        "text/event-stream",
			"Cache-Control": "no-cache",
		}

		// If no auth query, use OAuth2 token
		if authQuery == "" {
			tokenSource, err := getAuthTokenSource(key)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, schemas.Vertex)
			}
			token, err := tokenSource.Token()
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error getting token", err, schemas.Vertex)
			}
			headers["Authorization"] = "Bearer " + token.AccessToken
		}

		postResponseConverter := func(response *schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse {
			response.ExtraFields.ModelRequested = request.Model
			if request.Model != deployment {
				response.ExtraFields.ModelDeployment = deployment
			}
			return response
		}

		// Use shared streaming logic from Gemini
		return gemini.HandleGeminiResponsesStream(
			ctx,
			provider.client,
			completeURL,
			jsonData,
			headers,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			request.Model,
			postHookRunner,
			postResponseConverter,
			provider.logger,
		)
	} else {
		ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
		return provider.ChatCompletionStream(
			ctx,
			postHookRunner,
			key,
			request.ToChatRequest(),
		)
	}
}

// Embedding generates embeddings for the given input text(s) using Vertex AI.
// All Vertex AI embedding models use the same response format regardless of the model type.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *VertexProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}
	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
	}

	region := key.VertexKeyConfig.Region.GetValue()
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

	deployment := provider.getModelDeployment(key, request.Model)

	// Remove google/ prefix from deployment
	deployment = strings.TrimPrefix(deployment, "google/")

	// Build the native Vertex embedding API endpoint
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		region, projectID, region, deployment)

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
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}

		responseBody := resp.Body()

		// Extract error message from Vertex's error format
		errorMessage := "Unknown error"
		if len(responseBody) > 0 {
			// Try to parse Vertex's error format
			var vertexError map[string]interface{}
			if err := sonic.Unmarshal(resp.Body(), &vertexError); err != nil {
				return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.Vertex), jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
			}

			if errorObj, exists := vertexError["error"]; exists {
				if errorMap, ok := errorObj.(map[string]interface{}); ok {
					if message, exists := errorMap["message"]; exists {
						if msgStr, ok := message.(string); ok {
							errorMessage = msgStr
						}
					}
				}
			}
		}

		return nil, providerUtils.EnrichError(ctx, providerUtils.NewProviderAPIError(errorMessage, nil, resp.StatusCode(), schemas.Vertex, nil, nil), jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Parse Vertex's native embedding response using typed response
	var vertexResponse VertexEmbeddingResponse
	if err := sonic.Unmarshal(resp.Body(), &vertexResponse); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, schemas.Vertex), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Use centralized Vertex converter
	bifrostResponse := vertexResponse.ToBifrostEmbeddingResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.EmbeddingRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	if bifrostResponse.ExtraFields.ModelRequested != deployment {
		bifrostResponse.ExtraFields.ModelDeployment = deployment
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponseMap map[string]interface{}
		if err := sonic.Unmarshal(resp.Body(), &rawResponseMap); err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderRawResponseUnmarshal, err, providerName), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		bifrostResponse.ExtraFields.RawResponse = rawResponseMap
	}

	return bifrostResponse, nil
}

// Speech is not supported by the Vertex provider.
func (provider *VertexProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Vertex provider.
func (provider *VertexProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Vertex provider.
func (provider *VertexProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Vertex provider.
func (provider *VertexProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

func (provider *VertexProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}

	deployment := provider.getModelDeployment(key, request.Model)
	// strip google/ prefix if present
	if after, ok := strings.CutPrefix(deployment, "google/"); ok {
		deployment = after
	}

	// Validate model type before processing
	if !schemas.IsGeminiModel(deployment) && !schemas.IsAllDigitsASCII(deployment) && !schemas.IsImagenModel(deployment) {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("image generation is only supported for Gemini and Imagen models, got: %s", deployment), providerName)
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			var requestBody map[string]interface{}

			if schemas.IsGeminiModel(deployment) || schemas.IsAllDigitsASCII(deployment) {
				reqBody := gemini.ToGeminiImageGenerationRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("image generation input is not provided")
				}
				reqBody.Model = deployment
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				// Convert struct to map for Vertex API
				reqBytes, err := sonic.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
				if err := sonic.Unmarshal(reqBytes, &requestBody); err != nil {
					return nil, fmt.Errorf("failed to unmarshal request body: %w", err)
				}
			} else if schemas.IsImagenModel(deployment) {
				reqBody := gemini.ToImagenImageGenerationRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("image generation input is not provided")
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

			delete(requestBody, "region")
			return requestBody, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
	}

	// Auth query is used for fine-tuned models to pass the API key in the query string
	authQuery := ""
	// Determine the URL based on model type
	var completeURL string
	if schemas.IsAllDigitsASCII(deployment) {
		// Custom Fine-tuned models use OpenAPI endpoint
		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models", providerName)
		}
		if value := key.Value.GetValue(); value != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(value))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/%s:generateContent", projectNumber, deployment)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/%s:generateContent", region, projectNumber, region, deployment)
		}

	} else if schemas.IsImagenModel(deployment) {
		// Imagen models are published models, use publishers/google/models path
		if value := key.Value.GetValue(); value != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(value))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s:predict", projectID, deployment)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict", region, projectID, region, deployment)
		}
	} else if schemas.IsGeminiModel(deployment) {
		if value := key.Value.GetValue(); value != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(value))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s:generateContent", projectID, deployment)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent", region, projectID, region, deployment)
		}
	}

	// Create HTTP request for image generation
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// If auth query is set, add it to the URL
	// Otherwise, get the oauth2 token and set the Authorization header
	if authQuery != "" {
		completeURL = fmt.Sprintf("%s?%s", completeURL, authQuery)
	} else {
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
	}

	req.SetRequestURI(completeURL)
	req.SetBody(jsonBody)

	// Make the request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}
		return nil, providerUtils.EnrichError(ctx, parseVertexError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.ImageGenerationRequest,
		}), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	if schemas.IsGeminiModel(deployment) || schemas.IsAllDigitsASCII(deployment) {
		geminiResponse := gemini.GenerateContentResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &geminiResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response, err := geminiResponse.ToBifrostImageGenerationResponse()
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, err, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response.ExtraFields.RequestType = schemas.ImageGenerationRequest
		response.ExtraFields.Provider = providerName
		response.ExtraFields.ModelRequested = request.Model
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}
		response.ExtraFields.Latency = latency.Milliseconds()

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}

		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	} else {
		// Handle Imagen responses
		imagenResponse := gemini.GeminiImagenResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &imagenResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response := imagenResponse.ToBifrostImageGenerationResponse()
		response.ExtraFields.RequestType = schemas.ImageGenerationRequest
		response.ExtraFields.Provider = providerName
		response.ExtraFields.ModelRequested = request.Model
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}
		response.ExtraFields.Latency = latency.Milliseconds()

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}

		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	}
}

// ImageGenerationStream is not supported by the Vertex provider.
func (provider *VertexProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// stripVertexGeminiUnsupportedFields removes fields that are not supported by Vertex AI's Gemini API.
// Specifically, it removes the "id" field from function_call and function_response objects in contents.
func stripVertexGeminiUnsupportedFields(requestBody *gemini.GeminiGenerationRequest) {
	for _, content := range requestBody.Contents {
		for _, part := range content.Parts {
			// Remove id from function_call
			if part.FunctionCall != nil {
				part.FunctionCall.ID = ""
			}
			// Remove id from function_response
			if part.FunctionResponse != nil {
				part.FunctionResponse.ID = ""
			}
		}
	}
}

func (provider *VertexProvider) getModelDeployment(key schemas.Key, model string) string {
	if key.VertexKeyConfig == nil {
		return model
	}

	if key.VertexKeyConfig.Deployments != nil {
		if deployment, ok := key.VertexKeyConfig.Deployments[model]; ok {
			return deployment
		}
	}
	return model
}

// BatchCreate is not supported by Vertex AI provider.
func (provider *VertexProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by Vertex AI provider.
func (provider *VertexProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by Vertex AI provider.
func (provider *VertexProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by Vertex AI provider.
func (provider *VertexProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchResults is not supported by Vertex AI provider.
func (provider *VertexProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not yet implemented for Vertex AI provider.
// Vertex AI uses Google Cloud Storage (GCS) for batch input/output files.
func (provider *VertexProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not yet implemented for Vertex AI provider.
func (provider *VertexProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not yet implemented for Vertex AI provider.
func (provider *VertexProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not yet implemented for Vertex AI provider.
func (provider *VertexProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not yet implemented for Vertex AI provider.
func (provider *VertexProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// CountTokens counts the number of tokens in the provided content using Vertex AI's countTokens endpoint.
// Supports Gemini models with both text and image content.
func (provider *VertexProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is not set", providerName)
	}

	deployment := provider.getModelDeployment(key, request.Model)
	// strip google/ prefix if present
	if after, ok := strings.CutPrefix(deployment, "google/"); ok {
		deployment = after
	}

	var (
		jsonBody   []byte
		bifrostErr *schemas.BifrostError
	)

	if schemas.IsAnthropicModel(deployment) {
		jsonBody, bifrostErr = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) {
				return anthropic.ToAnthropicResponsesRequest(ctx, request)
			},
			providerName,
		)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		var payload map[string]any
		if err := sonic.Unmarshal(jsonBody, &payload); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerName)
		}

		payload["model"] = deployment
		if _, exists := payload["anthropic_version"]; !exists {
			payload["anthropic_version"] = DefaultVertexAnthropicVersion
		}

		delete(payload, "region")
		delete(payload, "max_tokens")
		delete(payload, "temperature")

		newBody, err := sonic.Marshal(payload)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
		}
		jsonBody = newBody
	} else {
		jsonBody, bifrostErr = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) { return gemini.ToGeminiResponsesRequest(request), nil },
			providerName,
		)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		var payload map[string]any
		if err := sonic.Unmarshal(jsonBody, &payload); err == nil {
			delete(payload, "toolConfig")
			delete(payload, "generationConfig")
			delete(payload, "systemInstruction")
			newBody, err := sonic.Marshal(payload)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
			}
			jsonBody = newBody
		}
	}

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set", providerName)
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config", providerName)
	}

	authQuery := ""
	var completeURL string

	if schemas.IsAnthropicModel(deployment) {
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/count-tokens:rawPredict", projectID)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/count-tokens:rawPredict", region, projectID, region)
		}
	} else if schemas.IsGeminiModel(deployment) || schemas.IsAllDigitsASCII(deployment) {
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}

		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if schemas.IsAllDigitsASCII(deployment) && projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models", providerName)
		}

		completeURL = getCompleteURLForGeminiEndpoint(deployment, region, projectID, projectNumber, ":countTokens")
	}

	if completeURL == "" {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("count tokens is not supported for model/deployment: %s", deployment), providerName)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	if authQuery != "" {
		completeURL = fmt.Sprintf("%s?%s", completeURL, authQuery)
	} else {
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err, schemas.Vertex)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err, schemas.Vertex)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	req.SetRequestURI(completeURL)
	req.SetBody(jsonBody)

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}
		return nil, providerUtils.EnrichError(ctx, parseVertexError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.CountTokensRequest,
		}), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	if schemas.IsAnthropicModel(deployment) {
		anthropicResponse := &anthropic.AnthropicCountTokensResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), anthropicResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response := anthropicResponse.ToBifrostCountTokensResponse(request.Model)
		response.ExtraFields.RequestType = schemas.CountTokensRequest
		response.ExtraFields.Provider = providerName
		response.ExtraFields.ModelRequested = request.Model
		if request.Model != deployment {
			response.ExtraFields.ModelDeployment = deployment
		}
		response.ExtraFields.Latency = latency.Milliseconds()

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}

		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	}

	vertexResponse := VertexCountTokensResponse{}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &vertexResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	response := vertexResponse.ToBifrostCountTokensResponse(request.Model)
	response.ExtraFields.RequestType = schemas.CountTokensRequest
	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	if request.Model != deployment {
		response.ExtraFields.ModelDeployment = deployment
	}
	response.ExtraFields.Latency = latency.Milliseconds()

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ContainerCreate is not supported by the Vertex provider.
func (provider *VertexProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Vertex provider.
func (provider *VertexProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Vertex provider.
func (provider *VertexProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Vertex provider.
func (provider *VertexProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Vertex provider.
func (provider *VertexProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Vertex provider.
func (provider *VertexProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Vertex provider.
func (provider *VertexProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Vertex provider.
func (provider *VertexProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Vertex provider.
func (provider *VertexProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}
