package elevenlabs

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type ElevenlabsProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewElevenlabsProvider creates a new Gemini provider instance.
// It initializes the HTTP client with the provided configuration.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewElevenlabsProvider(config *schemas.ProviderConfig, logger schemas.Logger) *ElevenlabsProvider {
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

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.elevenlabs.io"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &ElevenlabsProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Gemini.
func (provider *ElevenlabsProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Elevenlabs, provider.customProviderConfig)
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *ElevenlabsProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Build URL using centralized URL construction
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/v1/models"))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("xi-api-key", key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := parseElevenlabsError(providerName, resp)
		return nil, bifrostErr
	}

	var elevenlabsResponse ElevenlabsListModelsResponse
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &elevenlabsResponse, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := elevenlabsResponse.ToBifrostListModelsResponse(providerName)

	response.ExtraFields.Latency = latency.Milliseconds()

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to ElevenLabs' API.
// Requests are made concurrently for improved performance.
func (provider *ElevenlabsProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
		provider.logger,
	)
}

// buildRequestURL constructs the full request URL using the provider's configuration.
func (provider *ElevenlabsProvider) buildRequestURL(ctx context.Context, defaultPath string, requestType schemas.RequestType, request *schemas.BifrostSpeechRequest) string {
	baseURL := provider.networkConfig.BaseURL
	requestPath := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)

	u, parseErr := url.Parse(baseURL)
	if parseErr != nil {
		return baseURL + requestPath
	}

	u.Path = path.Join(u.Path, requestPath)
	q := u.Query()

	if request.Params != nil {
		if request.Params.EnableLogging != nil {
			q.Set("enable_logging", strconv.FormatBool(*request.Params.EnableLogging))
		}

		if request.Params.OutputFormat != nil {
			q.Set("output_format", string(*request.Params.OutputFormat))
		}

		if request.Params.OptimizeStreamingLatency != nil {
			q.Set("optimize_streaming_latency", strconv.FormatBool(*request.Params.OptimizeStreamingLatency))
		}
	}

	u.RawQuery = q.Encode()
	return u.String()
}

func parseElevenlabsError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	body := resp.Body()

	// Try to parse as Elevenlabs validation error first
	var errorResp ElevenlabsValidationError
	if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Detail != nil && len(errorResp.Detail) > 0 {
		var messages []string
		for _, detail := range errorResp.Detail {
			location := "unknown"
			if len(detail.Loc) > 0 {
				location = strings.Join(detail.Loc, ".")
			}
			messages = append(messages, fmt.Sprintf("[%s] %s (%s)", location, detail.Msg, detail.Type))
		}

		errorMessage := strings.Join(messages, "; ")

		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr("validation_error"),
				Message: fmt.Sprintf("Elevenlabs validation error: %s", errorMessage),
			},
		}
		return bifrostErr
	}

	// Try to parse as generic Elevenlabs error
	var genericError ElevenlabsGenericError
	if err := sonic.Unmarshal(body, &genericError); err == nil && genericError.Detail.Message != "" {
		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr(genericError.Detail.Status),
				Message: genericError.Detail.Message,
			},
		}
		return bifrostErr
	}

	// Fallback to raw response parsing
	var rawResponse map[string]interface{}
	if err := sonic.Unmarshal(body, &rawResponse); err != nil {
		return providerUtils.NewBifrostOperationError("failed to parse error response", err, providerName)
	}

	return providerUtils.NewBifrostOperationError(fmt.Sprintf("Elevenlabs error: %v", rawResponse), fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
}

func (provider *ElevenlabsProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	voiceId := "21m00Tcm4TlvDq8ikWAM" // default voice (Rachel)
	if request.Params != nil &&
		request.Params.VoiceConfig != nil &&
		request.Params.VoiceConfig.Voice != nil &&
		*request.Params.VoiceConfig.Voice != "" {
		voiceId = *request.Params.VoiceConfig.Voice
	}

	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/text-to-speech/"+voiceId, schemas.SpeechRequest, request))

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("xi-api-key", key.Value)
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToElevenlabsSpeechRequest(request), nil },
		providerName)

	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, parseElevenlabsError(providerName, resp)
	}

	// Get the binary audio data from the response body
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Create final response with the audio data
	// Note: For speech synthesis, we return the binary audio data in the raw response
	// The audio data is typically in MP3, WAV, or other audio formats as specified by response_format
	bifrostResponse := &schemas.BifrostSpeechResponse{
		Audio: body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.SpeechRequest,
			Provider:       providerName,
			ModelRequested: request.Model,
			Latency:        latency.Milliseconds(),
		},
	}

	return bifrostResponse, nil

}

// --- TODO ---
func (provider *ElevenlabsProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

func (provider *ElevenlabsProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

func (provider *ElevenlabsProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// --- UNSUPPORTED METHODS ---
// TextCompletion is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
}

// ChatCompletionStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
}

// Responses is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesRequest, provider.GetProviderKey())
}

// ResponsesStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, provider.GetProviderKey())
}

// Embedding is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) Embedding(ctx context.Context, key schemas.Key, input *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}