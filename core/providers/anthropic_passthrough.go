// Package providers implements various LLM providers and their utility functions.
// This file contains the Anthropic Passthrough provider implementation for OAuth mode.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// hopByHopHeaders are HTTP/1.1 headers that must not be forwarded by proxies.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"proxy-connection":    true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// filterHeaders filters out hop-by-hop headers and returns only the allowed headers.
func filterHeaders(headers map[string]string) map[string]string {
	filtered := make(map[string]string, len(headers))
	for k, v := range headers {
		if !hopByHopHeaders[strings.ToLower(k)] {
			filtered[k] = v
		}
	}
	return filtered
}

// AnthropicPassthroughProvider implements OAuth passthrough mode for Anthropic's Claude API.
// This provider is used when the API key starts with "sk-ant-oat" (OAuth Access Token).
// It passes through the original request body and headers from Claude Code without modification.
type AnthropicPassthroughProvider struct {
	logger               schemas.Logger
	client               *fasthttp.Client // For non-streaming requests
	streamClient         *http.Client     // For streaming requests
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
		Transport: &http.Transport{
			ResponseHeaderTimeout: time.Second * time.Duration(
				max(config.NetworkConfig.DefaultRequestTimeoutInSeconds, 60),
			),
		},
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

// extractOriginalPathFromContext extracts the original request path from context.
// In passthrough mode, it retrieves the original path (e.g., /v1/messages/count_tokens?beta=true).
// Returns the path string or default path if not found in context.
func (provider *AnthropicPassthroughProvider) extractOriginalPathFromContext(ctx context.Context) string {
	defaultPath := "/v1/messages?beta=true"

	if originalPath := ctx.Value(schemas.BifrostContextKeyOriginalPath); originalPath != nil {
		if pathStr, ok := originalPath.(string); ok && pathStr != "" {
			return pathStr
		}
	}

	return defaultPath
}

// sendRequest sends a request to Anthropic's API in passthrough mode and handles the response using fasthttp.
// It passes through the original headers and body from Claude Code without modification.
// Returns: rawBytes (original response), decodedBytes (), headers, error
func (provider *AnthropicPassthroughProvider) sendRequest(ctx context.Context, rawBody []byte, url string, key string) ([]byte, []byte, http.Header, *schemas.BifrostError) {

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(url)
	req.Header.SetMethod("POST")
	req.SetBody(rawBody)

	if originalHeaders, ok := ctx.Value(schemas.BifrostContextKeyOriginalHeaders).(map[string]string); ok {
		for k, v := range filterHeaders(originalHeaders) {
			req.Header.Set(k, v)
		}
	}

	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	// Send the request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, nil, nil, bifrostErr
	}

	rawBytes := resp.Body()

	if resp.StatusCode() != fasthttp.StatusOK {

		var errorResp AnthropicError

		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Message = errorResp.Error.Message

		return nil, nil, nil, bifrostErr
	}

	decodedBody := rawBytes
	contentEncoding := string(resp.Header.Peek("Content-Encoding"))

	if contentEncoding == "gzip" {
		var err error
		decodedBody, err = resp.BodyGunzip()
		if err != nil {
			return nil, nil, nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, fmt.Errorf("failed to decompress gzip response: %w", err), provider.GetProviderKey())
		}
	}

	httpHeaders := make(http.Header)
	for k, v := range resp.Header.All() {
		httpHeaders.Add(string(k), string(v))
	}

	return rawBytes, decodedBody, httpHeaders, nil
}

// TextCompletion is not supported by the Anthropic passthrough provider.
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

	path := provider.extractOriginalPathFromContext(ctx)
	url := provider.networkConfig.BaseURL + path

	rawBytes, decodedBody, respHeaders, bifrostErr := provider.sendRequest(ctx, rawBody, url, key.Value)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := acquireAnthropicChatResponse()
	defer releaseAnthropicChatResponse(response)

	_, bifrostErr = handleProviderResponse(decodedBody, response, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse := &schemas.BifrostResponse{}
	bifrostResponse, parseErr := parseAnthropicResponse(response, bifrostResponse)
	if parseErr != nil {
		return nil, parseErr
	}

	bifrostResponse.ExtraFields = schemas.BifrostResponseExtraFields{
		Provider:    provider.GetProviderKey(),
		RawResponse: rawBytes,
		RawHeaders:  respHeaders,
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

	headers, ok := ctx.Value(schemas.BifrostContextKeyOriginalHeaders).(map[string]string)
	if !ok {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, fmt.Errorf("passthrough mode requires original headers in context"), provider.GetProviderKey())
	}

	path := provider.extractOriginalPathFromContext(ctx)
	url := provider.networkConfig.BaseURL + path

	return provider.handleAnthropicStreamingPassthrough(
		ctx,
		postHookRunner,
		params,
		url,
		rawBody,
		headers,
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

// handleAnthropicStreamingPassthrough implements passthrough streaming.
// It reads raw SSE events from Anthropic and forwards them as-is without parsing/reconstruction.
// This preserves the exact stream format that Claude Code expects.
// Additionally, it parses SSE events to extract usage information for telemetry.
func (provider *AnthropicPassthroughProvider) handleAnthropicStreamingPassthrough(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	params *schemas.ModelParameters,
	url string,
	rawBody []byte,
	headers map[string]string,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {

	// Create HTTP request for streaming - use rawBody as-is (TRUE passthrough)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(rawBody))
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, provider.GetProviderKey())
	}

	for k, v := range filterHeaders(headers) {
		req.Header.Set(k, v)
	}

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Make the request using streaming client
	resp, err := provider.streamClient.Do(req)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, provider.GetProviderKey())
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from Anthropic: %d", resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, provider.GetProviderKey(), nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine - forward raw SSE events and parse for telemetry
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		reader := bufio.NewReader(resp.Body)
		// Track metadata for telemetry
		var messageID string
		var usage *schemas.LLMUsage
		var finishReason *string
		var eventType string
		var eventData string

		for {
			line, err := reader.ReadBytes('\n')

			// Send data if we have any
			if len(line) > 0 {
				// Forward raw SSE event as-is
				select {
				case responseChan <- &schemas.BifrostStream{
					RawSSEEvent: line,
				}:
					// Successfully sent
				case <-ctx.Done():
					return
				}

				// Parse SSE event for telemetry (parallel to forwarding)
				lineStr := strings.TrimSpace(string(line))

				// Skip empty lines and comments
				if lineStr == "" || strings.HasPrefix(lineStr, ":") {
					continue
				}

				// Parse SSE event type
				if strings.HasPrefix(lineStr, "event: ") {
					eventType = strings.TrimSpace(strings.TrimPrefix(lineStr, "event: "))
					continue
				}

				// Parse SSE event data
				if strings.HasPrefix(lineStr, "data: ") {
					eventData = strings.TrimSpace(strings.TrimPrefix(lineStr, "data: "))

					// Only parse if we have both event type and data
					if eventType != "" && eventData != "" {
						var event AnthropicStreamEvent
						if err := sonic.Unmarshal([]byte(eventData), &event); err == nil {
							// Extract usage information
							if event.Usage != nil {
								usage = &schemas.LLMUsage{
									PromptTokens:     event.Usage.InputTokens,
									CompletionTokens: event.Usage.OutputTokens,
									TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
								}
							}

							// Extract finish reason
							if event.Delta != nil && event.Delta.StopReason != nil {
								mapped := MapAnthropicFinishReason(*event.Delta.StopReason)
								finishReason = &mapped
							}

							// Extract message ID from message_start event
							if eventType == "message_start" && event.Message != nil {
								messageID = event.Message.ID
							}
						}

						// Reset event parsing state
						eventType = ""
						eventData = ""
					}
				}
			}

			// Stop on any error (including EOF)
			if err != nil {
				if err == io.EOF {
					// Send final chunk with usage for telemetry
					response := createBifrostChatCompletionChunkResponse(messageID, usage, finishReason, -1, params, provider.GetProviderKey())
					handleStreamEndForPassthrough(ctx, response, postHookRunner, provider.logger)
				} else {
					// Stream error - log and send to client
					provider.logger.Warn(fmt.Sprintf("Error reading Anthropic passthrough stream: %v", err))
					processAndSendError(ctx, postHookRunner, err, responseChan, provider.logger)
				}
				return
			}
		}
	}()

	return responseChan, nil
}
