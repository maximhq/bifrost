package vertex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
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

// vertexLocationsPathRe matches /locations/{region} in Vertex API paths for region replacement.
var vertexLocationsPathRe = regexp.MustCompile(`/locations/[^/]+`)

var vertexProjectsPathRe = regexp.MustCompile(`/projects/[^/]+`)

const maxStreamPassthroughCaptureBytes = 1024 * 1024

// vertexBodyProjectsRe matches projects/{project} in body JSON values,
// where the path may appear as "projects/X (after a JSON quote) or /projects/X (mid-path).
var vertexBodyProjectsRe = regexp.MustCompile(`(["/])projects/[^/"]+`)

// vertexShortModelRe matches short-form model names like "models/X" in JSON bodies
// that need expanding to the full Vertex resource path.
var vertexShortModelRe = regexp.MustCompile(`"(models/[^/"]+)"`)

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
	client              *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient     *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewVertexProvider creates a new Vertex provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewVertexProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*VertexProvider, error) {
	config.CheckAndSetDefaults()
	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)
	return &VertexProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
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
	authCredentials := key.VertexKeyConfig.AuthCredentials
	var tokenSource oauth2.TokenSource
	if authCredentials.GetValue() == "" {
		creds, err := google.FindDefaultCredentials(context.Background(), cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("failed to find default credentials in environment: %w", err)
		}
		tokenSource = creds.TokenSource
	} else {
		jsonData := []byte(authCredentials.GetValue())

		// Peek at the JSON to detect the "type" field
		var meta struct {
			Type string `json:"type"`
		}
		if err := sonic.Unmarshal(jsonData, &meta); err != nil {
			return nil, fmt.Errorf("failed to parse auth credentials JSON: %w", err)
		}

		// Map string to google.CredentialsType with a security whitelist
		var credType google.CredentialsType
		switch meta.Type {
		case string(google.ServiceAccount):
			credType = google.ServiceAccount
		case string(google.ImpersonatedServiceAccount):
			credType = google.ImpersonatedServiceAccount
		case string(google.AuthorizedUser):
			credType = google.AuthorizedUser
		case string(google.ExternalAccount):
			credType = google.ExternalAccount
		case string(google.ExternalAccountAuthorizedUser):
			credType = google.ExternalAccountAuthorizedUser
		case "":
			return nil, fmt.Errorf("invalid google auth credentials: missing 'type'")
		default:
			return nil, fmt.Errorf("unsupported or restricted credential type: %s", meta.Type)
		}

		conf, err := google.CredentialsFromJSONWithType(context.Background(), jsonData, credType, cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("failed to create credentials from auth credentials JSON: %w", err)
		}
		tokenSource = conf.TokenSource
	}
	return tokenSource, nil
}

// GetProviderKey returns the provider identifier for Vertex.
func (provider *VertexProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Vertex
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
//
// The logic is:
// 1. If deployments or allowedModels are configured, return those (no API call needed)
// 2. Otherwise, fetch from the publishers.models.list API endpoint (Model Garden)
func (provider *VertexProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config")
	}

	deployments := key.Aliases
	allowedModels := key.Models

	if !request.Unfiltered && (allowedModels.IsEmpty() && len(deployments) == 0 || key.BlacklistedModels.IsBlockAll()) {
		return &schemas.BifrostListModelsResponse{Data: make([]schemas.Model, 0)}, nil
	}

	// If deployments or allowedModels are configured, return those directly without API call
	// Skip this fast path when Unfiltered is set so the full Vertex catalog can be retrieved
	if !request.Unfiltered && (len(deployments) > 0 || allowedModels.IsRestricted()) {
		return buildResponseFromConfig(deployments, allowedModels, key.BlacklistedModels), nil
	}

	// No deployments configured - fetch from Model Garden API
	var host string
	if region == "global" {
		host = "aiplatform.googleapis.com"
	} else {
		host = fmt.Sprintf("%s-aiplatform.googleapis.com", region)
	}

	// Accumulate all publisher models from paginated requests
	var allPublisherModels []VertexPublisherModel
	var rawRequests []interface{}
	var rawResponses []interface{}
	pageToken := ""

	// Getting oauth2 token
	tokenSource, err := getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source (api key auth not supported for list models)", err)
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error getting token (api key auth not supported for list models)", err)
	}

	// Iterate over all supported Vertex publishers to include Google, Anthropic, and Mistral models
	publishers := []string{"google", "anthropic", "mistralai"}
	for _, publisher := range publishers {
		pageToken = ""
		// Loop through all pages until no nextPageToken is returned
		for {
			// Build URL for publishers.models.list endpoint (Model Garden)
			// Format: https://{region}-aiplatform.googleapis.com/v1beta1/publishers/{publisher}/models
			requestURL := fmt.Sprintf("https://%s/v1beta1/publishers/%s/models?pageSize=%d", host, publisher, MaxPageSize)
			if pageToken != "" {
				requestURL = fmt.Sprintf("%s&pageToken=%s", requestURL, url.QueryEscape(pageToken))
			}

			// Create HTTP request for listing models
			req := fasthttp.AcquireRequest()
			resp := fasthttp.AcquireResponse()

			req.Header.SetMethod(http.MethodGet)
			req.SetRequestURI(requestURL)
			req.Header.SetContentType("application/json")
			providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
			req.Header.Set("Authorization", "Bearer "+token.AccessToken)

			_, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
			if bifrostErr != nil {
				wait()
				respBody := append([]byte(nil), resp.Body()...)
				fasthttp.ReleaseRequest(req)
				fasthttp.ReleaseResponse(resp)
				// Non-Google publishers may not be available in all regions; skip on error
				if publisher != "google" {
					break
				}
				return nil, providerUtils.EnrichError(ctx, bifrostErr, nil, respBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
			}
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

			// Handle error response
			if resp.StatusCode() != fasthttp.StatusOK {
				if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
					removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
				}

				// Non-Google publishers may not be available in all regions;
				// skip only on 403/404 which indicate regional unavailability.
				// Surface other errors (401, 429, 5xx) so they aren't silently swallowed.
				if publisher != "google" && (resp.StatusCode() == fasthttp.StatusForbidden || resp.StatusCode() == fasthttp.StatusNotFound) {
					wait()
					fasthttp.ReleaseRequest(req)
					fasthttp.ReleaseResponse(resp)
					break
				}

				respBody := append([]byte(nil), resp.Body()...)
				statusCode := resp.StatusCode()
				wait()
				fasthttp.ReleaseRequest(req)
				fasthttp.ReleaseResponse(resp)

				var errorResp VertexError
				if err := sonic.Unmarshal(respBody, &errorResp); err != nil {
					return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err), nil, respBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
				}
				return nil, providerUtils.EnrichError(ctx, providerUtils.NewProviderAPIError(errorResp.Error.Message, nil, statusCode, nil, nil), nil, respBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
			}

			// Parse Vertex's publisher models response
			var vertexResponse VertexListPublisherModelsResponse
			rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &vertexResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
			if bifrostErr != nil {
				respBody := append([]byte(nil), resp.Body()...)
				wait()
				fasthttp.ReleaseRequest(req)
				fasthttp.ReleaseResponse(resp)
				return nil, providerUtils.EnrichError(ctx, bifrostErr, nil, respBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
			}
			if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
				rawRequests = append(rawRequests, rawRequest)
			}
			if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
				rawResponses = append(rawResponses, rawResponse)
			}

			// Accumulate models from this page
			allPublisherModels = append(allPublisherModels, vertexResponse.PublisherModels...)

			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)

			// Check if there are more pages
			if vertexResponse.NextPageToken == "" {
				break
			}
			pageToken = vertexResponse.NextPageToken
		}
	}

	// Create aggregated response from all pages
	aggregatedResponse := &VertexListPublisherModelsResponse{
		PublisherModels: allPublisherModels,
	}

	response := aggregatedResponse.ToBifrostListModelsResponse(key.Models, key.BlacklistedModels, key.Aliases, request.Unfiltered)

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
func (provider *VertexProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to the Vertex API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *VertexProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			// Format messages for Vertex API, preserving key order for prompt caching
			var rawBody []byte
			var extraParams map[string]interface{}
			var err error

			if schemas.IsAnthropicModel(request.Model) {
				// Use centralized Anthropic converter
				reqBody, convErr := anthropic.ToAnthropicChatRequest(ctx, request)
				if convErr != nil {
					return nil, convErr
				}
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}
				extraParams = reqBody.GetExtraParams()
				// Add provider-aware beta headers for Vertex
				anthropic.AddMissingBetaHeadersToContext(ctx, reqBody, schemas.Vertex)
				// Marshal to JSON bytes, preserving struct field order
				rawBody, err = providerUtils.MarshalSorted(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
				// Add anthropic_version if not present (using sjson to preserve order)
				if !providerUtils.JSONFieldExists(rawBody, "anthropic_version") {
					rawBody, err = providerUtils.SetJSONField(rawBody, "anthropic_version", DefaultVertexAnthropicVersion)
					if err != nil {
						return nil, fmt.Errorf("failed to set anthropic_version: %w", err)
					}
				}
				// Inject beta headers into body as anthropic_beta (Vertex uses body field, not HTTP header)
				if betaHeaders := anthropic.FilterBetaHeadersForProvider(anthropic.MergeBetaHeaders(provider.networkConfig.ExtraHeaders, ctx), schemas.Vertex, provider.networkConfig.BetaHeaderOverrides); len(betaHeaders) > 0 {
					rawBody, err = providerUtils.SetJSONField(rawBody, "anthropic_beta", betaHeaders)
					if err != nil {
						return nil, fmt.Errorf("failed to set anthropic_beta: %w", err)
					}
				}
				// Remove model field (it's in URL for Vertex)
				rawBody, err = providerUtils.DeleteJSONField(rawBody, "model")
				if err != nil {
					return nil, fmt.Errorf("failed to delete model field: %w", err)
				}
			} else if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
				reqBody, err := gemini.ToGeminiChatCompletionRequest(request)
				if err != nil {
					return nil, err
				}
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}
				extraParams = reqBody.GetExtraParams()
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				// Marshal to JSON bytes
				rawBody, err = providerUtils.MarshalSorted(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
			} else {
				// Use centralized OpenAI converter for non-Claude models
				reqBody := openai.ToOpenAIChatRequest(ctx, request)
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}
				extraParams = reqBody.GetExtraParams()
				// Marshal to JSON bytes
				rawBody, err = providerUtils.MarshalSorted(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
			}

			// Remove region field if present
			rawBody, err = providerUtils.DeleteJSONField(rawBody, "region")
			if err != nil {
				return nil, fmt.Errorf("failed to delete region field: %w", err)
			}
			return &VertexRawRequestBody{RawBody: rawBody, ExtraParams: extraParams}, nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config")
	}

	// Remap unsupported tool versions for Vertex (handles raw passthrough bodies)
	if schemas.IsAnthropicModel(request.Model) && jsonBody != nil {
		remappedBody, remapErr := anthropic.RemapRawToolVersionsForProvider(jsonBody, schemas.Vertex)
		if remapErr != nil {
			return nil, providerUtils.NewBifrostOperationError(remapErr.Error(), nil)
		}
		jsonBody = remappedBody
	}

	// Auth query is used for fine-tuned models to pass the API key in the query string
	authQuery := ""
	// Determine the URL based on model type
	var completeURL string
	if schemas.IsAllDigitsASCII(request.Model) {
		// Custom Fine-tuned models use OpenAPI endpoint
		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models")
		}
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/%s:generateContent", projectNumber, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/%s:generateContent", region, projectNumber, region, request.Model)
		}
	} else if schemas.IsAnthropicModel(request.Model) {
		// Claude models use Anthropic publisher
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/%s:rawPredict", projectID, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict", region, projectID, region, request.Model)
		}
	} else if schemas.IsMistralModel(request.Model) {
		// Mistral models use mistralai publisher with rawPredict
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/mistralai/models/%s:rawPredict", projectID, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/mistralai/models/%s:rawPredict", region, projectID, region, request.Model)
		}
	} else if schemas.IsGeminiModel(request.Model) {
		// Gemini models support api key
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s:generateContent", projectID, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent", region, projectID, region, request.Model)
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
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	// Skip anthropic-beta from context headers — Anthropic models on Vertex use the
	// anthropic_beta body field instead, and other model families don't use it.
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, []string{anthropic.AnthropicBetaHeader})

	// If auth query is set, add it to the URL
	// Otherwise, get the oauth2 token and set the Authorization header
	if authQuery != "" {
		completeURL = fmt.Sprintf("%s?%s", completeURL, authQuery)
	} else {
		// Getting oauth2 token
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	req.SetRequestURI(completeURL)
	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBody(ctx, req)
	if !usedLargePayloadBody {
		req.SetBody(jsonBody)
	}

	// Make the request with optional large response streaming
	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}
		return nil, providerUtils.EnrichError(ctx, parseVertexError(resp), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	responseBody, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
	if decodeErr != nil {
		return nil, providerUtils.EnrichError(ctx, decodeErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if isLargeResp {
		respOwned = false
		return &schemas.BifrostChatResponse{
			Model: request.Model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
			},
		}, nil
	}

	if schemas.IsAnthropicModel(request.Model) {
		// Create response object from pool
		anthropicResponse := anthropic.AcquireAnthropicMessageResponse()
		defer anthropic.ReleaseAnthropicMessageResponse(anthropicResponse)

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, anthropicResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		// Create final response
		response := anthropicResponse.ToBifrostChatResponse(ctx)

		response.ExtraFields = schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
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
	} else if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
		geminiResponse := gemini.GenerateContentResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &geminiResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response := geminiResponse.ToBifrostChatResponse()
		response.ExtraFields.Latency = latency.Milliseconds()
		response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

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
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response.ExtraFields.Latency = latency.Milliseconds()
		response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

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
func (provider *VertexProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config")
	}

	if schemas.IsAnthropicModel(request.Model) {
		// Use Anthropic-style streaming for Claude models
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (providerUtils.RequestBodyWithExtraParams, error) {
				var extraParams map[string]interface{}
				reqBody, convErr := anthropic.ToAnthropicChatRequest(ctx, request)
				if convErr != nil {
					return nil, convErr
				}
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}
				extraParams = reqBody.GetExtraParams()
				reqBody.Stream = new(true)
				// Add provider-aware beta headers for Vertex
				anthropic.AddMissingBetaHeadersToContext(ctx, reqBody, schemas.Vertex)

				// Marshal to JSON bytes, preserving struct field order for prompt caching
				rawBody, err := providerUtils.MarshalSorted(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}

				// Add anthropic_version if not present (using sjson to preserve order)
				if !providerUtils.JSONFieldExists(rawBody, "anthropic_version") {
					rawBody, err = providerUtils.SetJSONField(rawBody, "anthropic_version", DefaultVertexAnthropicVersion)
					if err != nil {
						return nil, fmt.Errorf("failed to set anthropic_version: %w", err)
					}
				}
				// Inject beta headers into body as anthropic_beta (Vertex uses body field, not HTTP header)
				if betaHeaders := anthropic.FilterBetaHeadersForProvider(anthropic.MergeBetaHeaders(provider.networkConfig.ExtraHeaders, ctx), schemas.Vertex, provider.networkConfig.BetaHeaderOverrides); len(betaHeaders) > 0 {
					rawBody, err = providerUtils.SetJSONField(rawBody, "anthropic_beta", betaHeaders)
					if err != nil {
						return nil, fmt.Errorf("failed to set anthropic_beta: %w", err)
					}
				}

				// Remove model and region fields (using sjson to preserve order)
				rawBody, err = providerUtils.DeleteJSONField(rawBody, "model")
				if err != nil {
					return nil, fmt.Errorf("failed to delete model field: %w", err)
				}
				rawBody, err = providerUtils.DeleteJSONField(rawBody, "region")
				if err != nil {
					return nil, fmt.Errorf("failed to delete region field: %w", err)
				}
				return &VertexRawRequestBody{RawBody: rawBody, ExtraParams: extraParams}, nil
			},
		)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		// Remap unsupported tool versions for Vertex streaming (handles raw passthrough bodies)
		if jsonData != nil {
			var remapErr error
			jsonData, remapErr = anthropic.RemapRawToolVersionsForProvider(jsonData, schemas.Vertex)
			if remapErr != nil {
				return nil, providerUtils.NewBifrostOperationError(remapErr.Error(), nil)
			}
		}

		var completeURL string
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/%s:streamRawPredict", projectID, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:streamRawPredict", region, projectID, region, request.Model)
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
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		headers["Authorization"] = "Bearer " + token.AccessToken

		// Use shared Anthropic streaming logic
		return anthropic.HandleAnthropicChatCompletionStreaming(
			ctx,
			provider.streamingClient,
			completeURL,
			jsonData,
			headers,
			provider.networkConfig.ExtraHeaders,
			provider.networkConfig.BetaHeaderOverrides,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			providerName,
			postHookRunner,
			nil,
			provider.logger,
			postHookSpanFinalizer,
		)
	} else if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
		// Use Gemini-style streaming for Gemini models
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (providerUtils.RequestBodyWithExtraParams, error) {
				reqBody, err := gemini.ToGeminiChatCompletionRequest(request)
				if err != nil {
					return nil, err
				}
				if reqBody == nil {
					return nil, fmt.Errorf("chat completion input is not provided")
				}
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				return reqBody, nil
			},
		)
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
		if schemas.IsAllDigitsASCII(request.Model) && projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models")
		}

		// Construct the URL for Gemini streaming
		completeURL := getCompleteURLForGeminiEndpoint(request.Model, region, projectID, projectNumber, ":streamGenerateContent")

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
				return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
			}
			token, err := tokenSource.Token()
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error getting token", err)
			}
			headers["Authorization"] = "Bearer " + token.AccessToken
		}

		// Use shared streaming logic from Gemini
		return gemini.HandleGeminiChatCompletionStream(
			ctx,
			provider.streamingClient,
			completeURL,
			jsonData,
			headers,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			request.Model,
			postHookRunner,
			nil,
			provider.logger,
			postHookSpanFinalizer,
		)
	} else {
		var authHeader map[string]string
		// Auth query is used for fine-tuned models to pass the API key in the query string
		authQuery := ""
		// Determine the URL based on model type
		var completeURL string
		if schemas.IsMistralModel(request.Model) {
			// Mistral models use mistralai publisher with streamRawPredict
			if region == "global" {
				completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/mistralai/models/%s:streamRawPredict", projectID, request.Model)
			} else {
				completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/mistralai/models/%s:streamRawPredict", region, projectID, region, request.Model)
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
				return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
			}
			token, err := tokenSource.Token()
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error getting token", err)
			}
			authHeader = map[string]string{
				"Authorization": "Bearer " + token.AccessToken,
			}
		}

		// Use shared OpenAI streaming logic
		return openai.HandleOpenAIChatCompletionStreaming(
			ctx,
			provider.streamingClient,
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
			nil,
			nil,
			nil,
			provider.logger,
			postHookSpanFinalizer,
		)
	}
}

// Responses performs a responses request to the Vertex API.
func (provider *VertexProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if schemas.IsAnthropicModel(request.Model) {
		jsonBody, bifrostErr := getRequestBodyForAnthropicResponses(ctx, request, request.Model, false, false, provider.networkConfig.BetaHeaderOverrides, provider.networkConfig.ExtraHeaders, provider.sendBackRawRequest, provider.sendBackRawResponse)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		projectID := key.VertexKeyConfig.ProjectID.GetValue()
		if projectID == "" {
			return nil, providerUtils.NewConfigurationError("project ID is not set")
		}

		region := key.VertexKeyConfig.Region.GetValue()
		if region == "" {
			return nil, providerUtils.NewConfigurationError("region is not set in key config")
		}

		// Claude models use Anthropic publisher
		var url string
		if region == "global" {
			url = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/publishers/anthropic/models/%s:rawPredict", projectID, request.Model)
		} else {
			url = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict", region, projectID, region, request.Model)
		}

		// Create HTTP request for streaming
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		respOwned := true
		defer func() {
			if respOwned {
				fasthttp.ReleaseResponse(resp)
			}
		}()

		req.Header.SetMethod(http.MethodPost)
		req.Header.SetContentType("application/json")
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, []string{anthropic.AnthropicBetaHeader})

		// Getting oauth2 token
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)

		req.SetRequestURI(url)
		usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBody(ctx, req)
		if !usedLargePayloadBody {
			req.SetBody(jsonBody)
		}

		// Make the request with optional large response streaming
		activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
		defer wait()
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		if usedLargePayloadBody {
			providerUtils.DrainLargePayloadRemainder(ctx)
		}
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

		if resp.StatusCode() != fasthttp.StatusOK {
			providerUtils.MaterializeStreamErrorBody(ctx, resp)
			// Remove client from pool for authentication/authorization errors
			if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
				removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
			}
			return nil, providerUtils.EnrichError(ctx, parseVertexError(resp), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		responseBody, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
		if decodeErr != nil {
			return nil, providerUtils.EnrichError(ctx, decodeErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		if isLargeResp {
			respOwned = false
			return &schemas.BifrostResponsesResponse{
				ExtraFields: schemas.BifrostResponseExtraFields{
					Latency:                 latency.Milliseconds(),
					ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
				},
			}, nil
		}

		// Create response object from pool
		anthropicResponse := anthropic.AcquireAnthropicMessageResponse()
		defer anthropic.ReleaseAnthropicMessageResponse(anthropicResponse)

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, anthropicResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		// Create final response
		response := anthropicResponse.ToBifrostResponsesResponse(ctx)

		response.ExtraFields = schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		}

		response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)
		// Set raw request if enabled
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}

		// Set raw response if enabled
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	} else if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
		jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (providerUtils.RequestBodyWithExtraParams, error) {
				reqBody, err := gemini.ToGeminiResponsesRequest(request)
				if err != nil {
					return nil, err
				}
				if reqBody == nil {
					return nil, fmt.Errorf("responses input is not provided")
				}
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				return reqBody, nil
			},
		)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		projectID := key.VertexKeyConfig.ProjectID.GetValue()
		if projectID == "" {
			return nil, providerUtils.NewConfigurationError("project ID is not set")
		}

		region := key.VertexKeyConfig.Region.GetValue()
		if region == "" {
			return nil, providerUtils.NewConfigurationError("region is not set in key config")
		}

		authQuery := ""
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}

		// For custom/fine-tuned models, validate projectNumber is set
		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if schemas.IsAllDigitsASCII(request.Model) && projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models")
		}

		url := getCompleteURLForGeminiEndpoint(request.Model, region, projectID, projectNumber, ":generateContent")

		// Create HTTP request for streaming
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		respOwned := true
		defer func() {
			if respOwned {
				fasthttp.ReleaseResponse(resp)
			}
		}()

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
				return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
			}
			token, err := tokenSource.Token()
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error getting token", err)
			}
			req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		}

		req.SetRequestURI(url)
		usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBody(ctx, req)
		if !usedLargePayloadBody {
			req.SetBody(jsonBody)
		}

		// Make the request with optional large response streaming
		activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
		defer wait()
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		if usedLargePayloadBody {
			providerUtils.DrainLargePayloadRemainder(ctx)
		}
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

		if resp.StatusCode() != fasthttp.StatusOK {
			providerUtils.MaterializeStreamErrorBody(ctx, resp)
			// Remove client from pool for authentication/authorization errors
			if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
				removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
			}
			return nil, providerUtils.EnrichError(ctx, parseVertexError(resp), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		responseBody, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
		if decodeErr != nil {
			return nil, providerUtils.EnrichError(ctx, decodeErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		if isLargeResp {
			respOwned = false
			return &schemas.BifrostResponsesResponse{
				ExtraFields: schemas.BifrostResponseExtraFields{
					Latency:                 latency.Milliseconds(),
					ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
				},
			}, nil
		}

		geminiResponse := &gemini.GenerateContentResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, geminiResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response := geminiResponse.ToResponsesBifrostResponsesResponse()
		response.ExtraFields.Latency = latency.Milliseconds()
		response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

		// Set raw response if enabled
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
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
		return response, nil
	}
}

// ResponsesStream performs a streaming responses request to the Vertex API.
func (provider *VertexProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if schemas.IsAnthropicModel(request.Model) {
		region := key.VertexKeyConfig.Region.GetValue()
		if region == "" {
			return nil, providerUtils.NewConfigurationError("region is not set in key config")
		}

		projectID := key.VertexKeyConfig.ProjectID.GetValue()
		if projectID == "" {
			return nil, providerUtils.NewConfigurationError("project ID is not set")
		}

		jsonBody, bifrostErr := getRequestBodyForAnthropicResponses(ctx, request, request.Model, true, false, provider.networkConfig.BetaHeaderOverrides, provider.networkConfig.ExtraHeaders, provider.sendBackRawRequest, provider.sendBackRawResponse)
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
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		headers["Authorization"] = "Bearer " + token.AccessToken

		// Use shared streaming logic from Anthropic
		return anthropic.HandleAnthropicResponsesStream(
			ctx,
			provider.streamingClient,
			url,
			jsonBody,
			headers,
			provider.networkConfig.ExtraHeaders,
			provider.networkConfig.BetaHeaderOverrides,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			nil,
			provider.logger,
			postHookSpanFinalizer,
		)
	} else if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
		region := key.VertexKeyConfig.Region.GetValue()
		if region == "" {
			return nil, providerUtils.NewConfigurationError("region is not set in key config")
		}

		projectID := key.VertexKeyConfig.ProjectID.GetValue()
		if projectID == "" {
			return nil, providerUtils.NewConfigurationError("project ID is not set")
		}

		// Use Gemini-style streaming for Gemini models
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (providerUtils.RequestBodyWithExtraParams, error) {
				reqBody, err := gemini.ToGeminiResponsesRequest(request)
				if err != nil {
					return nil, err
				}
				if reqBody == nil {
					return nil, fmt.Errorf("responses input is not provided")
				}
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				return reqBody, nil
			},
		)
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
		if schemas.IsAllDigitsASCII(request.Model) && projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models")
		}

		// Construct the URL for Gemini streaming
		completeURL := getCompleteURLForGeminiEndpoint(request.Model, region, projectID, projectNumber, ":streamGenerateContent")
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
				return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
			}
			token, err := tokenSource.Token()
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error getting token", err)
			}
			headers["Authorization"] = "Bearer " + token.AccessToken
		}

		// Use shared streaming logic from Gemini
		return gemini.HandleGeminiResponsesStream(
			ctx,
			provider.streamingClient,
			completeURL,
			jsonData,
			headers,
			provider.networkConfig.ExtraHeaders,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			request.Model,
			postHookRunner,
			nil,
			provider.logger,
			postHookSpanFinalizer,
		)
	} else {
		ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
		return provider.ChatCompletionStream(
			ctx,
			postHookRunner,
			postHookSpanFinalizer,
			key,
			request.ToChatRequest(),
		)
	}
}

// Embedding generates embeddings for the given input text(s) using Vertex AI.
// All Vertex AI embedding models use the same response format regardless of the model type.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *VertexProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config")
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToVertexEmbeddingRequest(request), nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// For custom/fine-tuned models, validate projectNumber is set
	projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
	if schemas.IsAllDigitsASCII(request.Model) && projectNumber == "" {
		return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models")
	}

	// Build the native Vertex embedding API endpoint
	url := getCompleteURLForGeminiEndpoint(request.Model, region, projectID, projectNumber, ":predict")

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Getting oauth2 token
	tokenSource, err := getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error getting token", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBody(ctx, req)
	if !usedLargePayloadBody {
		req.SetBody(jsonBody)
	}

	// Make the request with optional large response streaming
	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}

		errBody := resp.Body()

		// Extract error message from Vertex's error format
		errorMessage := "Unknown error"
		if len(errBody) > 0 {
			// Try to parse Vertex's error format
			var vertexError map[string]interface{}
			if err := sonic.Unmarshal(errBody, &vertexError); err != nil {
				return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err), jsonBody, errBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
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

		return nil, providerUtils.EnrichError(ctx, providerUtils.NewProviderAPIError(errorMessage, nil, resp.StatusCode(), nil, nil), jsonBody, errBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	responseBody, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
	if decodeErr != nil {
		return nil, providerUtils.EnrichError(ctx, decodeErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if isLargeResp {
		respOwned = false
		return &schemas.BifrostEmbeddingResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
			},
		}, nil
	}

	// Parse Vertex's native embedding response using typed response
	var vertexResponse VertexEmbeddingResponse
	if err := sonic.Unmarshal(responseBody, &vertexResponse); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err), jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Use centralized Vertex converter
	bifrostResponse := vertexResponse.ToBifrostEmbeddingResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponseMap map[string]interface{}
		if err := sonic.Unmarshal(resp.Body(), &rawResponseMap); err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderRawResponseUnmarshal, err), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		bifrostResponse.ExtraFields.RawResponse = rawResponseMap
	}

	return bifrostResponse, nil
}

// Speech is not supported by the Vertex provider.
func (provider *VertexProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// Rerank performs a rerank request using Vertex Discovery Engine ranking API.
func (provider *VertexProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	projectID := strings.TrimSpace(key.VertexKeyConfig.ProjectID.GetValue())
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	options, err := getVertexRerankOptions(projectID, request.Params)
	if err != nil {
		return nil, providerUtils.NewConfigurationError(err.Error())
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToVertexRankRequest(request, options)
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	completeURL := fmt.Sprintf("https://discoveryengine.googleapis.com/v1/%s:rank", options.RankingConfig)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(completeURL)
	req.Header.SetContentType("application/json")
	req.Header.Set("X-Goog-User-Project", projectID)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	tokenSource, err := getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error getting token", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBody(ctx, req)
	if !usedLargePayloadBody {
		req.SetBody(jsonBody)
	}

	// Make the request with optional large response streaming
	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}

		errorMessage := parseDiscoveryEngineErrorMessage(resp.Body())
		parsedError := parseVertexError(resp)

		if strings.TrimSpace(errorMessage) != "" {
			shouldOverride := parsedError == nil ||
				parsedError.Error == nil ||
				strings.TrimSpace(parsedError.Error.Message) == "" ||
				parsedError.Error.Message == "Unknown error" ||
				parsedError.Error.Message == schemas.ErrProviderResponseUnmarshal

			if shouldOverride {
				parsedError = providerUtils.NewProviderAPIError(errorMessage, nil, resp.StatusCode(), nil, nil)
			}
		}

		return nil, providerUtils.EnrichError(ctx, parsedError, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	responseBody, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
	if decodeErr != nil {
		return nil, providerUtils.EnrichError(ctx, decodeErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if isLargeResp {
		respOwned = false
		return &schemas.BifrostRerankResponse{
			Model: request.Model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
			},
		}, nil
	}

	vertexResponse := &VertexRankResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, vertexResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	returnDocuments := request.Params != nil && request.Params.ReturnDocuments != nil && *request.Params.ReturnDocuments
	bifrostResponse, err := vertexResponse.ToBifrostRerankResponse(request.Documents, returnDocuments)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error converting rerank response", err), jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// OCR is not supported by the Vertex provider.
func (provider *VertexProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Vertex provider.
func (provider *VertexProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Vertex provider.
func (provider *VertexProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Vertex provider.
func (provider *VertexProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

func (provider *VertexProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	// Validate model type before processing
	if !schemas.IsGeminiModel(request.Model) && !schemas.IsAllDigitsASCII(request.Model) && !schemas.IsImagenModel(request.Model) {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("image generation is only supported for Gemini and Imagen models, got: %s", request.Model))
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			var rawBody []byte
			var extraParams map[string]interface{}
			var err error

			if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
				reqBody := gemini.ToGeminiImageGenerationRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("image generation input is not provided")
				}
				extraParams = reqBody.GetExtraParams()
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				// Marshal to JSON bytes, preserving key order
				rawBody, err = providerUtils.MarshalSorted(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
			} else if schemas.IsImagenModel(request.Model) {
				reqBody := gemini.ToImagenImageGenerationRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("image generation input is not provided")
				}
				extraParams = reqBody.GetExtraParams()
				// Marshal to JSON bytes, preserving key order
				rawBody, err = providerUtils.MarshalSorted(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
			}

			// Remove region field if present
			rawBody, err = providerUtils.DeleteJSONField(rawBody, "region")
			if err != nil {
				return nil, fmt.Errorf("failed to delete region field: %w", err)
			}
			return &VertexRawRequestBody{RawBody: rawBody, ExtraParams: extraParams}, nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config")
	}

	// Auth query is used for fine-tuned models to pass the API key in the query string
	authQuery := ""
	// Determine the URL based on model type
	var completeURL string
	if schemas.IsAllDigitsASCII(request.Model) {
		// Custom Fine-tuned models use OpenAPI endpoint
		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models")
		}
		if value := key.Value.GetValue(); value != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(value))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/%s:generateContent", projectNumber, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/%s:generateContent", region, projectNumber, region, request.Model)
		}

	} else if schemas.IsImagenModel(request.Model) {
		// Imagen models are published models, use publishers/google/models path
		if value := key.Value.GetValue(); value != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(value))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s:predict", projectID, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict", region, projectID, region, request.Model)
		}
	} else if schemas.IsGeminiModel(request.Model) {
		if value := key.Value.GetValue(); value != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(value))
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s:generateContent", projectID, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent", region, projectID, region, request.Model)
		}
	}

	// Create HTTP request for image generation
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

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
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	req.SetRequestURI(completeURL)
	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBody(ctx, req)
	if !usedLargePayloadBody {
		req.SetBody(jsonBody)
	}

	// Make the request with optional large response streaming
	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		// Remove client from pool for authentication/authorization errors
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}
		return nil, providerUtils.EnrichError(ctx, parseVertexError(resp), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	responseBody, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
	if decodeErr != nil {
		return nil, providerUtils.EnrichError(ctx, decodeErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if isLargeResp {
		respOwned = false
		return &schemas.BifrostImageGenerationResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
			},
		}, nil
	}

	if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
		geminiResponse := gemini.GenerateContentResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &geminiResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response, err := geminiResponse.ToBifrostImageGenerationResponse()
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, err, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response.ExtraFields.Latency = latency.Milliseconds()
		response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

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

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &imagenResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response := imagenResponse.ToBifrostImageGenerationResponse()
		response.ExtraFields.Latency = latency.Milliseconds()
		response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

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
func (provider *VertexProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit edits images for the given input text(s) using Vertex AI.
// Returns a BifrostResponse containing the images and any error that occurred.
func (provider *VertexProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	// Validate model type before processing
	if !schemas.IsGeminiModel(request.Model) && !schemas.IsAllDigitsASCII(request.Model) && !schemas.IsImagenModel(request.Model) {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("image edit is only supported for Gemini and Imagen models, got: %s", request.Model))
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			var rawBody []byte
			var extraParams map[string]interface{}
			var err error

			if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
				reqBody := gemini.ToGeminiImageEditRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("image edit input is not provided")
				}
				extraParams = reqBody.GetExtraParams()
				// Strip unsupported fields for Vertex Gemini
				stripVertexGeminiUnsupportedFields(reqBody)
				// Marshal to JSON bytes, preserving key order
				rawBody, err = providerUtils.MarshalSorted(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
			} else if schemas.IsImagenModel(request.Model) {
				reqBody := gemini.ToImagenImageEditRequest(request)
				if reqBody == nil {
					return nil, fmt.Errorf("image edit input is not provided")
				}
				extraParams = reqBody.GetExtraParams()
				// Marshal to JSON bytes, preserving key order
				rawBody, err = providerUtils.MarshalSorted(reqBody)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal request body: %w", err)
				}
			}

			// Remove region field if present
			rawBody, err = providerUtils.DeleteJSONField(rawBody, "region")
			if err != nil {
				return nil, fmt.Errorf("failed to delete region field: %w", err)
			}
			return &VertexRawRequestBody{RawBody: rawBody, ExtraParams: extraParams}, nil
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config")
	}

	authQuery := ""
	if value := key.Value.GetValue(); value != "" {
		authQuery = fmt.Sprintf("key=%s", url.QueryEscape(value))
	}

	var completeURL string
	if schemas.IsAllDigitsASCII(request.Model) {
		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models")
		}
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1beta1/projects/%s/locations/global/endpoints/%s:generateContent", projectNumber, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/%s:generateContent", region, projectNumber, region, request.Model)
		}
	} else if schemas.IsImagenModel(request.Model) {
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s:predict", projectID, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict", region, projectID, region, request.Model)
		}
	} else if schemas.IsGeminiModel(request.Model) {
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/google/models/%s:generateContent", projectID, request.Model)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent", region, projectID, region, request.Model)
		}
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

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
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	req.SetRequestURI(completeURL)
	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBody(ctx, req)
	if !usedLargePayloadBody {
		req.SetBody(jsonBody)
	}

	// Make the request with optional large response streaming
	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}
		return nil, providerUtils.EnrichError(ctx, parseVertexError(resp), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	responseBody, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
	if decodeErr != nil {
		return nil, providerUtils.EnrichError(ctx, decodeErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if isLargeResp {
		respOwned = false
		return &schemas.BifrostImageGenerationResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
			},
		}, nil
	}

	if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
		geminiResponse := gemini.GenerateContentResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &geminiResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response, err := geminiResponse.ToBifrostImageGenerationResponse()
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, err, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response.ExtraFields.Latency = latency.Milliseconds()
		response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

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

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &imagenResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response := imagenResponse.ToBifrostImageGenerationResponse()
		response.ExtraFields.Latency = latency.Milliseconds()
		response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	}
}

// ImageEditStream is not supported by the Vertex provider.
func (provider *VertexProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Vertex provider.
func (provider *VertexProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoGeneration generates a video using Vertex AI's Gemini models.
// Only Gemini models support video generation in Vertex AI.
// Uses the predictLongRunning endpoint for async video generation.
func (provider *VertexProvider) VideoGeneration(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Only Gemini models support video generation in Vertex
	if !schemas.IsVeoModel(bifrostReq.Model) && !schemas.IsAllDigitsASCII(bifrostReq.Model) {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("video generation is only supported for Veo models in Vertex, got: %s", bifrostReq.Model))
	}

	// Convert Bifrost request to Gemini format (reusing Gemini converters)
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return gemini.ToGeminiVideoGenerationRequest(bifrostReq)
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config")
	}

	// Auth query is used to pass the API key in the query string
	authQuery := ""
	if key.Value.GetValue() != "" {
		authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
	}

	// For custom/fine-tuned models, validate projectNumber is set
	projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
	if schemas.IsAllDigitsASCII(bifrostReq.Model) && projectNumber == "" {
		return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models")
	}

	// Construct the URL for Gemini video generation using predictLongRunning
	completeURL := getCompleteURLForGeminiEndpoint(bifrostReq.Model, region, projectID, projectNumber, ":predictLongRunning")

	// Create HTTP request
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
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	req.SetRequestURI(completeURL)
	req.SetBody(jsonData)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}
		return nil, providerUtils.EnrichError(ctx, parseVertexError(resp), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Parse response
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	var operation gemini.GenerateVideosOperation
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &operation, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	// Convert to Bifrost response using Gemini converter
	bifrostResp, bifrostErr := gemini.ToBifrostVideoGenerationResponse(&operation, bifrostReq.Model)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	bifrostResp.ID = providerUtils.AddVideoIDProviderSuffix(bifrostResp.ID, providerName)

	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// VideoRetrieve retrieves the status of a video generation operation.
// Uses the fetchPredictOperation endpoint for Vertex AI.
func (provider *VertexProvider) VideoRetrieve(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config")
	}

	// Construct base URL based on region
	var baseURL string
	if region == "global" {
		baseURL = "https://aiplatform.googleapis.com/v1"
	} else {
		baseURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", region)
	}

	// Construct the URL for fetching the operation status
	// The operation name (bifrostReq.ID) already contains the full path:
	// projects/PROJECT_ID/locations/REGION/publishers/google/models/MODEL_ID/operations/OPERATION_ID
	// We need to extract the model path from it to construct the fetchPredictOperation endpoint
	// Extract: projects/.../models/MODEL_ID from the operation name
	taskID := providerUtils.StripVideoIDProviderSuffix(bifrostReq.ID, provider.GetProviderKey())
	var modelPath string
	if idx := strings.Index(taskID, "/operations/"); idx != -1 {
		modelPath = taskID[:idx]
	} else {
		return nil, providerUtils.NewBifrostOperationError("invalid operation ID format", nil)
	}

	// Construct the URL: https://REGION-aiplatform.googleapis.com/v1/{modelPath}:fetchPredictOperation
	completeURL := fmt.Sprintf("%s/%s:fetchPredictOperation", baseURL, modelPath)

	// Auth query is used to pass the API key in the query string
	authQuery := ""
	if key.Value.GetValue() != "" {
		authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
	}

	// Create request body with operation name (using sjson to avoid map marshaling)
	jsonBody, err := providerUtils.SetJSONField([]byte(`{}`), "operationName", taskID)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to marshal request", err)
	}

	// Create HTTP request
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
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	req.SetRequestURI(completeURL)
	req.SetBody(jsonBody)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}
		return nil, providerUtils.EnrichError(ctx, parseVertexError(resp), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Parse response
	var operation gemini.GenerateVideosOperation
	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &operation, jsonBody, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResp, bifrostErr := gemini.ToBifrostVideoGenerationResponse(&operation, "")
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	bifrostResp.ID = providerUtils.AddVideoIDProviderSuffix(bifrostResp.ID, provider.GetProviderKey())
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// VideoDownload downloads the generated video content.
// First retrieves the video status to get the URL, then downloads the content.
// Handles both regular URLs and data URLs (base64-encoded videos).
func (provider *VertexProvider) VideoDownload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	if request == nil || request.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}
	// Retrieve operation first to get the video URL
	bifrostVideoRetrieveRequest := &schemas.BifrostVideoRetrieveRequest{
		Provider: request.Provider,
		ID:       request.ID,
	}
	videoResp, bifrostErr := provider.VideoRetrieve(ctx, key, bifrostVideoRetrieveRequest)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if videoResp.Status != schemas.VideoStatusCompleted {
		return nil, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("video not ready, current status: %s", videoResp.Status),
			nil)
	}
	if len(videoResp.Videos) == 0 {
		return nil, providerUtils.NewBifrostOperationError("video URL not available", nil)
	}
	var content []byte
	var latency time.Duration
	var providerResponseHeaders map[string]string
	contentType := "video/mp4"
	// Check if it's a data URL (base64-encoded video)
	if videoResp.Videos[0].Type == schemas.VideoOutputTypeBase64 && videoResp.Videos[0].Base64Data != nil {
		// Decode base64 content
		startTime := time.Now()
		decoded, err := base64.StdEncoding.DecodeString(*videoResp.Videos[0].Base64Data)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to decode base64 video data", err)
		}
		content = decoded
		contentType = videoResp.Videos[0].ContentType
		latency = time.Since(startTime)
	} else if videoResp.Videos[0].Type == schemas.VideoOutputTypeURL && videoResp.Videos[0].URL != nil {
		// Regular URL - fetch from HTTP endpoint
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(*videoResp.Videos[0].URL)
		req.Header.SetMethod(http.MethodGet)
		// Add authentication for Vertex video downloads
		authQuery := ""
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}
		if authQuery != "" {
			uri := *videoResp.Videos[0].URL
			if strings.Contains(uri, "?") {
				uri += "&" + authQuery
			} else {
				uri += "?" + authQuery
			}
			req.SetRequestURI(uri)
		} else {
			tokenSource, err := getAuthTokenSource(key)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
			}
			token, err := tokenSource.Token()
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError("error getting token", err)
			}
			req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		}
		var bifrostErr *schemas.BifrostError
		var wait func()
		latency, bifrostErr, wait = providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		defer wait()
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
		if resp.StatusCode() != fasthttp.StatusOK {
			return nil, providerUtils.NewBifrostOperationError(
				fmt.Sprintf("failed to download video: HTTP %d", resp.StatusCode()),
				nil)
		}
		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		contentType = string(resp.Header.ContentType())
		content = append([]byte(nil), body...)
		providerResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)
	} else {
		return nil, providerUtils.NewBifrostOperationError("invalid video output type", nil)
	}

	bifrostResp := &schemas.BifrostVideoDownloadResponse{
		VideoID:     request.ID,
		Content:     content,
		ContentType: contentType,
	}

	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	return bifrostResp, nil
}

// VideoDelete is not supported by the Vertex provider.
func (provider *VertexProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the Vertex provider.
func (provider *VertexProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the Vertex provider.
func (provider *VertexProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
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

// BatchDelete is not supported by Vertex AI provider.
func (provider *VertexProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
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
	var (
		jsonBody   []byte
		bifrostErr *schemas.BifrostError
	)

	if schemas.IsAnthropicModel(request.Model) {
		jsonBody, bifrostErr = getRequestBodyForAnthropicResponses(ctx, request, request.Model, false, true, provider.networkConfig.BetaHeaderOverrides, provider.networkConfig.ExtraHeaders, provider.sendBackRawRequest, provider.sendBackRawResponse)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
	} else {
		jsonBody, bifrostErr = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (providerUtils.RequestBodyWithExtraParams, error) {
				return gemini.ToGeminiResponsesRequest(request)
			},
		)
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		// Skip field-stripping when large payload mode is active — jsonBody is nil
		// and the raw body will stream directly from the ingress reader.
		if jsonBody != nil {
			// Use sjson to delete fields directly from JSON bytes, preserving key ordering
			jsonBody, _ = providerUtils.DeleteJSONField(jsonBody, "toolConfig")
			jsonBody, _ = providerUtils.DeleteJSONField(jsonBody, "generationConfig")
			jsonBody, _ = providerUtils.DeleteJSONField(jsonBody, "systemInstruction")
		}
	}

	projectID := key.VertexKeyConfig.ProjectID.GetValue()
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return nil, providerUtils.NewConfigurationError("region is not set in key config")
	}

	authQuery := ""
	var completeURL string

	if schemas.IsAnthropicModel(request.Model) {
		if region == "global" {
			completeURL = fmt.Sprintf("https://aiplatform.googleapis.com/v1/projects/%s/locations/global/publishers/anthropic/models/count-tokens:rawPredict", projectID)
		} else {
			completeURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/count-tokens:rawPredict", region, projectID, region)
		}
	} else if schemas.IsGeminiModel(request.Model) || schemas.IsAllDigitsASCII(request.Model) {
		if key.Value.GetValue() != "" {
			authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
		}

		projectNumber := key.VertexKeyConfig.ProjectNumber.GetValue()
		if schemas.IsAllDigitsASCII(request.Model) && projectNumber == "" {
			return nil, providerUtils.NewConfigurationError("project number is not set for fine-tuned models")
		}

		completeURL = getCompleteURLForGeminiEndpoint(request.Model, region, projectID, projectNumber, ":countTokens")
	}

	if completeURL == "" {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("count tokens is not supported for model: %s", request.Model))
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, []string{anthropic.AnthropicBetaHeader})

	if authQuery != "" {
		completeURL = fmt.Sprintf("%s?%s", completeURL, authQuery)
	} else {
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	req.SetRequestURI(completeURL)
	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBody(ctx, req)
	if !usedLargePayloadBody {
		req.SetBody(jsonBody)
	}

	// Make the request with optional large response streaming
	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
		}
		return nil, providerUtils.EnrichError(ctx, parseVertexError(resp), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	responseBody, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
	if decodeErr != nil {
		return nil, providerUtils.EnrichError(ctx, decodeErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	if isLargeResp {
		respOwned = false
		return &schemas.BifrostCountTokensResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
			},
		}, nil
	}

	if schemas.IsAnthropicModel(request.Model) {
		anthropicResponse := &anthropic.AnthropicCountTokensResponse{}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, anthropicResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}

		response := anthropicResponse.ToBifrostCountTokensResponse(request.Model)
		response.ExtraFields.Latency = latency.Milliseconds()
		response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}

		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		return response, nil
	}

	vertexResponse := VertexCountTokensResponse{}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &vertexResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	response := vertexResponse.ToBifrostCountTokensResponse(request.Model)
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)

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

func (provider *VertexProvider) Passthrough(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	projectID := strings.TrimSpace(key.VertexKeyConfig.ProjectID.GetValue())
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	keyRegion := key.VertexKeyConfig.Region.GetValue()
	if keyRegion == "" {
		keyRegion = "global"
	}

	var baseURL string
	if keyRegion == "global" {
		baseURL = "https://aiplatform.googleapis.com/v1"
	} else {
		baseURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", keyRegion)
	}

	// Normalize path: remove leading /v1 or /v1/ to avoid duplicate version segments (e.g. /v1/v1/...)
	path := req.Path
	for strings.HasPrefix(path, "/v1/") || path == "/v1" {
		path = strings.TrimPrefix(path, "/v1/")
		path = strings.TrimPrefix(path, "/v1")
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
	}

	// Replace region in path with key's configured region (client path may have different region)
	if strings.Contains(path, "/locations/") {
		path = vertexLocationsPathRe.ReplaceAllString(path, "/locations/"+keyRegion)
		if strings.Contains(path, "/projects/") {
			path = vertexProjectsPathRe.ReplaceAllString(path, "/projects/"+projectID)
		}
	} else {
		// add projects/%s/locations/%s/publishers/google to path
		path = fmt.Sprintf("/projects/%s/locations/%s%s", projectID, keyRegion, path)
	}

	requestURL := baseURL + path
	if req.RawQuery != "" {
		requestURL += "?" + req.RawQuery
	}

	// Only use API key for Google publisher endpoints; Anthropic/Mistral/OpenAPI-style paths require OAuth.
	authQuery := ""
	if key.Value.GetValue() != "" && strings.Contains(path, "publishers/google") {
		authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
	}

	// Prepare fasthttp request
	fasthttpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	defer fasthttp.ReleaseRequest(fasthttpReq)

	fasthttpReq.Header.SetMethod(req.Method)

	// If auth query is set, add it to the URL; otherwise use OAuth2
	if authQuery != "" {
		if strings.Contains(requestURL, "?") {
			requestURL += "&" + authQuery
		} else {
			requestURL += "?" + authQuery
		}
	} else {
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		fasthttpReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	fasthttpReq.SetRequestURI(requestURL)

	// Set extra headers from provider network config
	providerUtils.SetExtraHeaders(ctx, fasthttpReq, provider.networkConfig.ExtraHeaders, nil)

	// Set safe headers from client request
	for k, v := range req.SafeHeaders {
		if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "proxy-authorization") {
			continue
		}
		fasthttpReq.Header.Set(k, v)
	}

	if len(req.Body) > 0 && strings.Contains(strings.ToLower(string(fasthttpReq.Header.ContentType())), "application/json") {
		region := keyRegion
		// Replace fully-qualified model paths that have placeholder project/location
		// e.g. "projects/None/locations/None/publishers/..." -> "projects/real-id/locations/real-region/..."
		body := req.Body
		bodyStr := vertexBodyProjectsRe.ReplaceAllString(string(body), "${1}projects/"+projectID)
		bodyStr = vertexLocationsPathRe.ReplaceAllString(bodyStr, "/locations/"+region)
		// Expand short-form model names: "models/X" -> "projects/P/locations/L/publishers/google/models/X"
		bodyStr = vertexShortModelRe.ReplaceAllString(bodyStr,
			fmt.Sprintf(`"projects/%s/locations/%s/publishers/google/$1"`, projectID, keyRegion))
		fasthttpReq.SetBodyString(bodyStr)
	} else if len(req.Body) > 0 {
		fasthttpReq.SetBody(req.Body)
	}

	// Execute request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, fasthttpReq, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Remove client from pool for authentication/authorization errors
	if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
		removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
	}

	headers := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to decode response body", err)
	}
	originalHeaders := make(map[string]string, len(headers))
	maps.Copy(originalHeaders, headers)
	for k := range headers {
		if strings.EqualFold(k, "Content-Encoding") || strings.EqualFold(k, "Content-Length") {
			delete(headers, k)
		}
	}
	bifrostResponse := &schemas.BifrostPassthroughResponse{
		StatusCode: resp.StatusCode(),
		Headers:    headers,
		Body:       body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: originalHeaders,
		},
	}

	return bifrostResponse, nil
}

func (provider *VertexProvider) PassthroughStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	projectID := strings.TrimSpace(key.VertexKeyConfig.ProjectID.GetValue())
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}

	keyRegion := key.VertexKeyConfig.Region.GetValue()
	if keyRegion == "" {
		keyRegion = "global"
	}

	var baseURL string
	if keyRegion == "global" {
		baseURL = "https://aiplatform.googleapis.com/v1"
	} else {
		baseURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1", keyRegion)
	}

	// Normalize path: remove leading /v1 or /v1/ to avoid duplicate version segments.
	path := req.Path
	for strings.HasPrefix(path, "/v1/") || path == "/v1" {
		path = strings.TrimPrefix(path, "/v1/")
		path = strings.TrimPrefix(path, "/v1")
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
	}

	// Replace region and project in path with key's configured values.
	if strings.Contains(path, "/locations/") {
		path = vertexLocationsPathRe.ReplaceAllString(path, "/locations/"+keyRegion)
		if strings.Contains(path, "/projects/") {
			path = vertexProjectsPathRe.ReplaceAllString(path, "/projects/"+projectID)
		}
	} else {
		path = fmt.Sprintf("/projects/%s/locations/%s%s", projectID, keyRegion, path)
	}

	requestURL := baseURL + path
	if req.RawQuery != "" {
		requestURL += "?" + req.RawQuery
	}

	fasthttpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(fasthttpReq)

	fasthttpReq.Header.SetMethod(req.Method)

	// Only use API key for Google publisher endpoints; Anthropic/Mistral/OpenAPI-style paths require OAuth.
	authQuery := ""
	if key.Value.GetValue() != "" && strings.Contains(path, "publishers/google") {
		authQuery = fmt.Sprintf("key=%s", url.QueryEscape(key.Value.GetValue()))
	}

	if authQuery != "" {
		if strings.Contains(requestURL, "?") {
			requestURL += "&" + authQuery
		} else {
			requestURL += "?" + authQuery
		}
	} else {
		tokenSource, err := getAuthTokenSource(key)
		if err != nil {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
			providerUtils.ReleaseStreamingResponse(resp)
			return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
		}
		token, err := tokenSource.Token()
		if err != nil {
			removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
			providerUtils.ReleaseStreamingResponse(resp)
			return nil, providerUtils.NewBifrostOperationError("error getting token", err)
		}
		fasthttpReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	fasthttpReq.SetRequestURI(requestURL)

	providerUtils.SetExtraHeaders(ctx, fasthttpReq, provider.networkConfig.ExtraHeaders, nil)

	for k, v := range req.SafeHeaders {
		if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "proxy-authorization") {
			continue
		}
		fasthttpReq.Header.Set(k, v)
	}

	if len(req.Body) > 0 && strings.Contains(strings.ToLower(string(fasthttpReq.Header.ContentType())), "application/json") {
		bodyStr := vertexBodyProjectsRe.ReplaceAllString(string(req.Body), "${1}projects/"+projectID)
		bodyStr = vertexLocationsPathRe.ReplaceAllString(bodyStr, "/locations/"+keyRegion)
		bodyStr = vertexShortModelRe.ReplaceAllString(bodyStr,
			fmt.Sprintf(`"projects/%s/locations/%s/publishers/google/$1"`, projectID, keyRegion))
		fasthttpReq.SetBodyString(bodyStr)
	} else if len(req.Body) > 0 {
		fasthttpReq.SetBody(req.Body)
	}

	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.streamingClient, resp)
	if err := activeClient.Do(fasthttpReq, resp); err != nil {
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
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
	}

	if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
		removeVertexClient(key.VertexKeyConfig.AuthCredentials.GetValue())
	}

	headers := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	bodyStream := resp.BodyStream()
	if bodyStream == nil {
		providerUtils.ReleaseStreamingResponse(resp)
		return nil, providerUtils.NewBifrostOperationError(
			"provider returned an empty stream body",
			fmt.Errorf("provider returned an empty stream body"))
	}

	// Set stream idle timeout from provider config.
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	// Wrap body with idle timeout to detect stalled streams.
	rawBodyStream := bodyStream
	bodyStream, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(bodyStream, rawBodyStream, providerUtils.GetStreamIdleTimeout(ctx))

	// Cancellation must close the raw stream to unblock reads.
	stopCancellation := providerUtils.SetupStreamCancellation(ctx, rawBodyStream, provider.logger)

	extraFields := schemas.BifrostResponseExtraFields{}
	statusCode := resp.StatusCode()

	ch := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, ch, provider.logger, postHookSpanFinalizer)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, ch, provider.logger, postHookSpanFinalizer)
			}
			close(ch)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		defer stopIdleTimeout()
		defer stopCancellation()
		streamStart := time.Now()

		fullResponseBody := make([]byte, 0, maxStreamPassthroughCaptureBytes)
		fullResponseBodyTruncated := false
		terminalDetector := &providerUtils.StreamTerminalDetector{}
		buf := make([]byte, 4096)
		for {
			n, readErr := bodyStream.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				if !fullResponseBodyTruncated {
					remaining := maxStreamPassthroughCaptureBytes - len(fullResponseBody)
					if remaining > 0 {
						if n <= remaining {
							fullResponseBody = append(fullResponseBody, chunk...)
						} else {
							fullResponseBody = append(fullResponseBody, chunk[:remaining]...)
							fullResponseBodyTruncated = true
						}
					} else {
						fullResponseBodyTruncated = true
					}
				}
				select {
				case ch <- &schemas.BifrostStreamChunk{
					BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{
						StatusCode:  statusCode,
						Headers:     headers,
						Body:        chunk,
						ExtraFields: extraFields,
					},
				}:
				case <-ctx.Done():
					return
				}

				// Vertex streamGenerateContent passthrough can emit terminal markers
				// (finishReason) before the underlying HTTP body is closed.
				// Finalize as success once this appears to avoid hanging clients.
				if terminalDetector.ObserveChunk(chunk) {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					extraFields.Latency = time.Since(streamStart).Milliseconds()
					var capturedBody []byte
					if !fullResponseBodyTruncated {
						capturedBody = append([]byte(nil), fullResponseBody...)
					}
					finalResp := &schemas.BifrostResponse{
						PassthroughResponse: &schemas.BifrostPassthroughResponse{
							StatusCode:  statusCode,
							Headers:     headers,
							Body:        capturedBody,
							ExtraFields: extraFields,
						},
					}
					postHookRunner(ctx, finalResp, nil)
					return
				}
			}
			if readErr == io.EOF {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				extraFields.Latency = time.Since(streamStart).Milliseconds()
				var capturedBody []byte
				if !fullResponseBodyTruncated {
					capturedBody = append([]byte(nil), fullResponseBody...)
				}
				finalResp := &schemas.BifrostResponse{
					PassthroughResponse: &schemas.BifrostPassthroughResponse{
						StatusCode:  statusCode,
						Headers:     headers,
						Body:        capturedBody,
						ExtraFields: extraFields,
					},
				}
				postHookRunner(ctx, finalResp, nil)
				return
			}
			if readErr != nil {
				if ctx.Err() != nil {
					return // let defer handle cancel/timeout
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				extraFields.Latency = time.Since(streamStart).Milliseconds()
				providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, ch, provider.logger, postHookSpanFinalizer)
				return
			}
		}
	}()
	return ch, nil
}
