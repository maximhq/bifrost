// Package providers implements various LLM providers and their utility functions.
// This file contains the Anthropic Passthrough provider implementation for OAuth mode.
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// AnthropicPassthroughProvider implements OAuth passthrough mode for Anthropic's Claude API.
// This provider is used when the API key starts with "sk-ant-oat" (OAuth Access Token).
// It passes through the original request body and headers from Claude Code without modification.
type AnthropicPassthroughProvider struct {
	logger               schemas.Logger
	client               *fasthttp.Client
	streamClient         *http.Client
	apiVersion           string
	networkConfig        schemas.NetworkConfig
	sendBackRawResponse  bool
	customProviderConfig *schemas.CustomProviderConfig
}

// NewAnthropicPassthroughProvider creates a new Anthropic passthrough provider instance.
// It initializes the HTTP client with the provided configuration for OAuth passthrough mode.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewAnthropicPassthroughProvider(config *schemas.ProviderConfig, logger schemas.Logger) *AnthropicPassthroughProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.Concurrency,
	}

	streamClient := &http.Client{
		Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.anthropic.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &AnthropicPassthroughProvider{
		logger:               logger,
		client:               client,
		streamClient:         streamClient,
		apiVersion:           "2023-06-01",
		networkConfig:        config.NetworkConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider identifier for Anthropic passthrough mode.
func (provider *AnthropicPassthroughProvider) GetProviderKey() schemas.ModelProvider {
	return getProviderName(schemas.AnthropicPassthrough, provider.customProviderConfig)
}

// extractRawBodyFromContext extracts the original request body from context.
// In passthrough mode, it retrieves the unmodified request body from Claude Code.
// Returns the raw body bytes or an error if extraction fails.
func (provider *AnthropicPassthroughProvider) extractRawBodyFromContext(ctx context.Context) ([]byte, *schemas.BifrostError) {
	originalBody := ctx.Value(schemas.BifrostContextKeyOriginalRequest)
	if originalBody == nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, fmt.Errorf("passthrough mode requires original request body in context"), provider.GetProviderKey())
	}

	rawBody, ok := originalBody.(json.RawMessage)
	if !ok {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, fmt.Errorf("original request body has invalid type: %T, expected json.RawMessage", originalBody), provider.GetProviderKey())
	}

	if len(rawBody) == 0 {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, fmt.Errorf("original request body is empty"), provider.GetProviderKey())
	}

	return rawBody, nil
}

// sendRequest sends a request to Anthropic's API in passthrough mode and handles the response.
// It passes through the original headers and body from Claude Code without modification.
// Returns the response body or an error if the request fails.
func (provider *AnthropicPassthroughProvider) sendRequest(ctx context.Context, rawBody []byte, url string, key string) ([]byte, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(url)
	req.Header.SetMethod("POST")

	// Pass through all original headers from context (OAuth passthrough mode)
	if headers, ok := ctx.Value(schemas.BifrostContextKeyOriginalHeaders).(map[string]string); ok {
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}

	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetBody(rawBody)

	if bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp); bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", provider.GetProviderKey(), string(resp.Body())))
		var errorResp AnthropicError
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Message = errorResp.Error.Message
		return nil, bifrostErr
	}

	body, err := resp.BodyGunzip()
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, fmt.Errorf("failed to read response body: %w", err), provider.GetProviderKey())
	}

	return body, nil
}

// TextCompletion is not supported by the Anthropic passthrough provider.
// OAuth passthrough mode only supports chat completion endpoints.
func (provider *AnthropicPassthroughProvider) TextCompletion(context.Context, string, schemas.Key, string, *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion in passthrough mode", "anthropic")
}

// ChatCompletion performs a chat completion request to Anthropic's API in passthrough mode.
// It forwards the original request from Claude Code without modification, preserving all headers.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicPassthroughProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.AnthropicPassthrough, provider.customProviderConfig, schemas.OperationChatCompletion); err != nil {
		return nil, err
	}

	rawBody, err := provider.extractRawBodyFromContext(ctx)
	if err != nil {
		return nil, err
	}

	url := provider.networkConfig.BaseURL + "/v1/messages"
	responseBody, bifrostErr := provider.sendRequest(ctx, rawBody, url, key.Value)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := acquireAnthropicChatResponse()
	defer releaseAnthropicChatResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse := &schemas.BifrostResponse{}
	bifrostResponse, parseErr := parseAnthropicResponse(response, bifrostResponse)
	if parseErr != nil {
		return nil, parseErr
	}

	bifrostResponse.ExtraFields = schemas.BifrostResponseExtraFields{
		Provider: provider.GetProviderKey(),
	}

	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// Embedding is not supported by the Anthropic passthrough provider.
func (provider *AnthropicPassthroughProvider) Embedding(context.Context, string, schemas.Key, *schemas.EmbeddingInput, *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("embedding", "anthropic")
}

// ChatCompletionStream performs a streaming chat completion request to the Anthropic API in passthrough mode.
// It supports real-time streaming of responses using Server-Sent Events (SSE) while preserving original headers.
// Returns a channel containing BifrostStream objects representing the stream or an error if the request fails.
func (provider *AnthropicPassthroughProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.AnthropicPassthrough, provider.customProviderConfig, schemas.OperationChatCompletionStream); err != nil {
		return nil, err
	}

	rawBody, err := provider.extractRawBodyFromContext(ctx)
	if err != nil {
		return nil, err
	}

	var requestBody map[string]interface{}
	if unmarshalErr := sonic.Unmarshal(rawBody, &requestBody); unmarshalErr != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, unmarshalErr, provider.GetProviderKey())
	}

	headers, ok := ctx.Value(schemas.BifrostContextKeyOriginalHeaders).(map[string]string)
	if !ok {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, fmt.Errorf("passthrough mode requires original headers in context"), provider.GetProviderKey())
	}

	return handleAnthropicStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/messages",
		requestBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		params,
		postHookRunner,
		provider.logger,
	)
}

// Speech is not supported by the Anthropic passthrough provider.
func (provider *AnthropicPassthroughProvider) Speech(context.Context, string, schemas.Key, *schemas.SpeechInput, *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "anthropic_passthrough")
}

// SpeechStream is not supported by the Anthropic passthrough provider.
func (provider *AnthropicPassthroughProvider) SpeechStream(context.Context, schemas.PostHookRunner, string, schemas.Key, *schemas.SpeechInput, *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "anthropic_passthrough")
}

// Transcription is not supported by the Anthropic passthrough provider.
func (provider *AnthropicPassthroughProvider) Transcription(context.Context, string, schemas.Key, *schemas.TranscriptionInput, *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "anthropic_passthrough")
}

// TranscriptionStream is not supported by the Anthropic passthrough provider.
func (provider *AnthropicPassthroughProvider) TranscriptionStream(context.Context, schemas.PostHookRunner, string, schemas.Key, *schemas.TranscriptionInput, *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "anthropic_passthrough")
}
