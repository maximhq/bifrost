// Package providers implements various LLM providers and their utility functions.
// This file contains common utility functions used across different provider implementations.
package utils

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
)

var logger schemas.Logger

func SetLogger(l schemas.Logger) {
	logger = l
}

var UnsupportedSpeechStreamModels = []string{"tts-1", "tts-1-hd"}

// MakeRequestWithContext makes a request with a context and returns the latency and error.
// IMPORTANT: This function does NOT truly cancel the underlying fasthttp network request if the
// context is done. The fasthttp client call will continue in its goroutine until it completes
// or times out based on its own settings. This function merely stops *waiting* for the
// fasthttp call and returns an error related to the context.
// Returns the request latency and any error that occurred.
func MakeRequestWithContext(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) (time.Duration, *schemas.BifrostError) {
	startTime := time.Now()
	errChan := make(chan error, 1)

	go func() {
		// client.Do is a blocking call.
		// It will send an error (or nil for success) to errChan when it completes.
		errChan <- client.Do(req, resp)
	}()

	select {
	case <-ctx.Done():
		// Context was cancelled (e.g., deadline exceeded or manual cancellation).
		// Calculate latency even for cancelled requests
		latency := time.Since(startTime)
		return latency, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Type:    schemas.Ptr(schemas.RequestCancelled),
				Message: fmt.Sprintf("Request cancelled or timed out by context: %v", ctx.Err()),
				Error:   ctx.Err(),
			},
		}
	case err := <-errChan:
		// The fasthttp.Do call completed.
		// Calculate latency for both successful and failed requests
		latency := time.Since(startTime)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return latency, &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(schemas.RequestCancelled),
						Message: schemas.ErrRequestCancelled,
						Error:   err,
					},
				}
			}
			if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
				return latency, NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, "")
			}
			// The HTTP request itself failed (e.g., connection error, fasthttp timeout).
			return latency, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderDoRequest,
					Error:   err,
				},
			}
		}
		// HTTP request was successful from fasthttp's perspective (err is nil).
		// The caller should check resp.StatusCode() for HTTP-level errors (4xx, 5xx).
		return latency, nil
	}
}

// ConfigureProxy sets up a proxy for the fasthttp client based on the provided configuration.
// It supports HTTP, SOCKS5, and environment-based proxy configurations.
// Returns the configured client or the original client if proxy configuration is invalid.
func ConfigureProxy(client *fasthttp.Client, proxyConfig *schemas.ProxyConfig, logger schemas.Logger) *fasthttp.Client {
	if proxyConfig == nil {
		return client
	}

	var dialFunc fasthttp.DialFunc

	// Create the appropriate proxy based on type
	switch proxyConfig.Type {
	case schemas.NoProxy:
		return client
	case schemas.HTTPProxy:
		if proxyConfig.URL == "" {
			logger.Warn("Warning: HTTP proxy URL is required for setting up proxy")
			return client
		}
		dialFunc = fasthttpproxy.FasthttpHTTPDialer(proxyConfig.URL)
	case schemas.Socks5Proxy:
		if proxyConfig.URL == "" {
			logger.Warn("Warning: SOCKS5 proxy URL is required for setting up proxy")
			return client
		}
		proxyURL := proxyConfig.URL
		// Add authentication if provided
		if proxyConfig.Username != "" && proxyConfig.Password != "" {
			parsedURL, err := url.Parse(proxyConfig.URL)
			if err != nil {
				logger.Warn("Invalid proxy configuration: invalid SOCKS5 proxy URL")
				return client
			}
			// Set user and password in the parsed URL
			parsedURL.User = url.UserPassword(proxyConfig.Username, proxyConfig.Password)
			proxyURL = parsedURL.String()
		}
		dialFunc = fasthttpproxy.FasthttpSocksDialer(proxyURL)
	case schemas.EnvProxy:
		// Use environment variables for proxy configuration
		dialFunc = fasthttpproxy.FasthttpProxyHTTPDialer()
	default:
		logger.Warn(fmt.Sprintf("Invalid proxy configuration: unsupported proxy type: %s", proxyConfig.Type))
		return client
	}

	if dialFunc != nil {
		client.Dial = dialFunc
	}

	return client
}

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
func filterHeaders(headers map[string][]string) map[string][]string {
	filtered := make(map[string][]string, len(headers))
	for k, v := range headers {
		if !hopByHopHeaders[strings.ToLower(k)] {
			filtered[k] = v
		}
	}
	return filtered
}

// SetExtraHeaders sets additional headers from NetworkConfig to the fasthttp request.
// This allows users to configure custom headers for their provider requests.
// Header keys are canonicalized using textproto.CanonicalMIMEHeaderKey to avoid duplicates.
// The Authorization header is excluded for security reasons.
// It accepts a list of headers (all canonicalized) to skip for security reasons.
// Headers are only set if they don't already exist on the request to avoid overwriting important headers.
func SetExtraHeaders(ctx context.Context, req *fasthttp.Request, extraHeaders map[string]string, skipHeaders []string) {
	for key, value := range extraHeaders {
		canonicalKey := textproto.CanonicalMIMEHeaderKey(key)
		// Skip Authorization header for security reasons
		if key == "Authorization" {
			continue
		}
		if skipHeaders != nil {
			if slices.Contains(skipHeaders, key) {
				continue
			}
		}
		// Only set the header if it doesn't already exist to avoid overwriting important headers
		if len(req.Header.Peek(canonicalKey)) == 0 {
			req.Header.Set(canonicalKey, value)
		}
	}

	// Give priority to extra headers in the context
	if extraHeaders, ok := (ctx).Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
		for k, values := range filterHeaders(extraHeaders) {
			for i, v := range values {
				if i == 0 {
					req.Header.Set(k, v)
				} else {
					req.Header.Add(k, v)
				}
			}
		}
	}
}

// GetPathFromContext gets the path from the context, if it exists, otherwise returns the default path.
func GetPathFromContext(ctx context.Context, defaultPath string) string {
	if pathInContext, ok := ctx.Value(schemas.BifrostContextKeyURLPath).(string); ok {
		return pathInContext
	}
	return defaultPath
}

// GetRequestPath gets the request path from the context, if it exists, checking for path overrides in the custom provider config.
func GetRequestPath(ctx context.Context, defaultPath string, customProviderConfig *schemas.CustomProviderConfig, requestType schemas.RequestType) string {
	// If path set in context, return it
	if pathInContext, ok := ctx.Value(schemas.BifrostContextKeyURLPath).(string); ok {
		return pathInContext
	}
	// If path override set in custom provider config, return it
	if customProviderConfig != nil && customProviderConfig.RequestPathOverrides != nil {
		if raw, ok := customProviderConfig.RequestPathOverrides[requestType]; ok {
			pathOverride := strings.TrimSpace(raw)
			if pathOverride == "" {
				return defaultPath
			}
			if !strings.HasPrefix(pathOverride, "/") {
				pathOverride = "/" + pathOverride
			}
			return pathOverride
		}
	}
	// Return default path
	return defaultPath
}

type RequestBodyGetter interface {
	GetRawRequestBody() []byte
}

// CheckAndGetRawRequestBody checks if the raw request body should be used, and returns it if it exists.
func CheckAndGetRawRequestBody(ctx context.Context, request RequestBodyGetter) ([]byte, bool) {
	if rawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && rawBody {
		return request.GetRawRequestBody(), true
	}
	return nil, false
}

type RequestBodyConverter func() (any, error)

// CheckContextAndGetRequestBody checks if the raw request body should be used, and returns it if it exists.
func CheckContextAndGetRequestBody(ctx context.Context, request RequestBodyGetter, requestConverter RequestBodyConverter, providerType schemas.ModelProvider) ([]byte, *schemas.BifrostError) {
	rawBody, ok := CheckAndGetRawRequestBody(ctx, request)
	if !ok {
		convertedBody, err := requestConverter()
		if err != nil {
			return nil, NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerType)
		}
		if convertedBody == nil {
			return nil, NewBifrostOperationError("request body is not provided", nil, providerType)
		}
		jsonBody, err := sonic.Marshal(convertedBody)
		if err != nil {
			return nil, NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerType)
		}
		return jsonBody, nil
	} else {
		return rawBody, nil
	}
}

// SetExtraHeadersHTTP sets additional headers from NetworkConfig to the standard HTTP request.
// This allows users to configure custom headers for their provider requests.
// Header keys are canonicalized using textproto.CanonicalMIMEHeaderKey to avoid duplicates.
// It accepts a list of headers (all canonicalized) to skip for security reasons.
// Headers are only set if they don't already exist on the request to avoid overwriting important headers.
func SetExtraHeadersHTTP(ctx context.Context, req *http.Request, extraHeaders map[string]string, skipHeaders []string) {
	for key, value := range extraHeaders {
		canonicalKey := textproto.CanonicalMIMEHeaderKey(key)
		// Skip Authorization header for security reasons
		if key == "Authorization" {
			continue
		}
		if skipHeaders != nil {
			if slices.Contains(skipHeaders, key) {
				continue
			}
		}
		// Only set the header if it doesn't already exist to avoid overwriting important headers
		if req.Header.Get(canonicalKey) == "" {
			req.Header.Set(canonicalKey, value)
		}
	}

	// Give priority to extra headers in the context
	if extraHeaders, ok := (ctx).Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
		for k, values := range filterHeaders(extraHeaders) {
			for i, v := range values {
				if i == 0 {
					req.Header.Set(k, v)
				} else {
					req.Header.Add(k, v)
				}
			}
		}
	}
}

// HandleProviderAPIError processes error responses from provider APIs.
// It attempts to unmarshal the error response and returns a BifrostError
// with the appropriate status code and error information.
// errorResp must be a pointer to the target struct for unmarshaling.
func HandleProviderAPIError(resp *fasthttp.Response, errorResp any) *schemas.BifrostError {
	statusCode := resp.StatusCode()

	if err := sonic.Unmarshal(resp.Body(), errorResp); err != nil {
		rawResponse := resp.Body()
		message := fmt.Sprintf("provider API error: %s", string(rawResponse))
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: message,
			},
		}
	}

	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{},
	}
}

// HandleProviderResponse handles common response parsing logic for provider responses.
// It attempts to parse the response body into the provided response type
// and returns either the parsed response or a BifrostError if parsing fails.
// If sendBackRawResponse is true, it returns the raw response interface, otherwise nil.
func HandleProviderResponse[T any](responseBody []byte, response *T, sendBackRawResponse bool) (interface{}, *schemas.BifrostError) {
	var rawResponse interface{}

	var wg sync.WaitGroup
	var structuredErr, rawErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		structuredErr = sonic.Unmarshal(responseBody, response)
	}()
	go func() {
		defer wg.Done()
		if sendBackRawResponse {
			rawErr = sonic.Unmarshal(responseBody, &rawResponse)
		}
	}()
	wg.Wait()

	if structuredErr != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseUnmarshal,
				Error:   structuredErr,
			},
		}
	}

	if sendBackRawResponse {
		if rawErr != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderRawResponseUnmarshal,
					Error:   rawErr,
				},
			}
		}

		return rawResponse, nil
	}

	return nil, nil
}

// NewUnsupportedOperationError creates a standardized error for unsupported operations.
// This helper reduces code duplication across providers that don't support certain operations.
func NewUnsupportedOperationError(requestType schemas.RequestType, providerName schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: fmt.Sprintf("%s is not supported by %s provider", requestType, providerName),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:    providerName,
			RequestType: requestType,
		},
	}
}

// CheckOperationAllowed enforces per-op gating using schemas.Operation.
// Behavior:
// - If no gating is configured (config == nil or AllowedRequests == nil), the operation is allowed.
// - If gating is configured, returns an error when the operation is not explicitly allowed.
func CheckOperationAllowed(defaultProvider schemas.ModelProvider, config *schemas.CustomProviderConfig, operation schemas.RequestType) *schemas.BifrostError {
	// No gating configured => allowed
	if config == nil || config.AllowedRequests == nil {
		return nil
	}
	// Explicitly allowed?
	if config.IsOperationAllowed(operation) {
		return nil
	}
	// Gated and not allowed
	resolved := GetProviderName(defaultProvider, config)
	return NewUnsupportedOperationError(operation, resolved)
}

// CheckAndDecodeBody checks the content encoding and decodes the body accordingly.
func CheckAndDecodeBody(resp *fasthttp.Response) ([]byte, error) {
	contentEncoding := strings.ToLower(strings.TrimSpace(string(resp.Header.Peek("Content-Encoding"))))
	switch contentEncoding {
	case "gzip":
		return resp.BodyGunzip()
	default:
		return resp.Body(), nil
	}
}

// NewConfigurationError creates a standardized error for configuration errors.
// This helper reduces code duplication across providers that have configuration errors.
func NewConfigurationError(message string, providerType schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: message,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: providerType,
		},
	}
}

// NewBifrostOperationError creates a standardized error for bifrost operation errors.
// This helper reduces code duplication across providers that have bifrost operation errors.
func NewBifrostOperationError(message string, err error, providerType schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: message,
			Error:   err,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: providerType,
		},
	}
}

// NewProviderAPIError creates a standardized error for provider API errors.
// This helper reduces code duplication across providers that have provider API errors.
func NewProviderAPIError(message string, err error, statusCode int, providerType schemas.ModelProvider, errorType *string, eventID *string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Type:           errorType,
		EventID:        eventID,
		Error: &schemas.ErrorField{
			Message: message,
			Error:   err,
			Type:    errorType,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: providerType,
		},
	}
}

// ShouldSendBackRawResponse checks if the raw response should be sent back, and returns it if it exists.
func ShouldSendBackRawResponse(ctx context.Context, defaultSendBackRawResponse bool) bool {
	if sendBackRawResponse, ok := ctx.Value(schemas.BifrostContextKeySendBackRawResponse).(bool); ok && sendBackRawResponse {
		return sendBackRawResponse
	}
	return defaultSendBackRawResponse
}

// SendCreatedEventResponsesChunk sends a ResponsesStreamResponseTypeCreated event.
func SendCreatedEventResponsesChunk(ctx context.Context, postHookRunner schemas.PostHookRunner, provider schemas.ModelProvider, model string, startTime time.Time, responseChan chan *schemas.BifrostStream) {
	firstChunk := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCreated,
		SequenceNumber: 0,
		Response:       &schemas.BifrostResponsesResponse{},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesStreamRequest,
			Provider:       provider,
			ModelRequested: model,
			ChunkIndex:     0,
			Latency:        time.Since(startTime).Milliseconds(),
		},
	}
	//TODO add bifrost response pooling here
	bifrostResponse := &schemas.BifrostResponse{
		ResponsesStreamResponse: firstChunk,
	}
	ProcessAndSendResponse(ctx, postHookRunner, bifrostResponse, responseChan)
}

// SendInProgressEventResponsesChunk sends a ResponsesStreamResponseTypeInProgress event
func SendInProgressEventResponsesChunk(ctx context.Context, postHookRunner schemas.PostHookRunner, provider schemas.ModelProvider, model string, startTime time.Time, responseChan chan *schemas.BifrostStream) {
	chunk := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeInProgress,
		SequenceNumber: 1,
		Response:       &schemas.BifrostResponsesResponse{},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesStreamRequest,
			Provider:       provider,
			ModelRequested: model,
			ChunkIndex:     1,
			Latency:        time.Since(startTime).Milliseconds(),
		},
	}
	//TODO add bifrost response pooling here
	bifrostResponse := &schemas.BifrostResponse{
		ResponsesStreamResponse: chunk,
	}
	ProcessAndSendResponse(ctx, postHookRunner, bifrostResponse, responseChan)
}

// ProcessAndSendResponse handles post-hook processing and sends the response to the channel.
// This utility reduces code duplication across streaming implementations by encapsulating
// the common pattern of running post hooks, handling errors, and sending responses with
// proper context cancellation handling.
func ProcessAndSendResponse(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	response *schemas.BifrostResponse,
	responseChan chan *schemas.BifrostStream,
) {
	// Run post hooks on the response
	processedResponse, processedError := postHookRunner(&ctx, response, nil)

	if HandleStreamControlSkip(processedError) {
		return
	}

	streamResponse := &schemas.BifrostStream{}
	if processedResponse != nil {
		streamResponse.BifrostTextCompletionResponse = processedResponse.TextCompletionResponse
		streamResponse.BifrostChatResponse = processedResponse.ChatResponse
		streamResponse.BifrostResponsesStreamResponse = processedResponse.ResponsesStreamResponse
		streamResponse.BifrostSpeechStreamResponse = processedResponse.SpeechStreamResponse
		streamResponse.BifrostTranscriptionStreamResponse = processedResponse.TranscriptionStreamResponse
	}
	if processedError != nil {
		streamResponse.BifrostError = processedError
	}

	select {
	case responseChan <- streamResponse:
	case <-ctx.Done():
		return
	}
}

// ProcessAndSendBifrostError handles post-hook processing and sends the bifrost error to the channel.
// This utility reduces code duplication across streaming implementations by encapsulating
// the common pattern of running post hooks, handling errors, and sending responses with
// proper context cancellation handling.
func ProcessAndSendBifrostError(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	bifrostErr *schemas.BifrostError,
	responseChan chan *schemas.BifrostStream,
	logger schemas.Logger,
) {
	// Send scanner error through channel
	processedResponse, processedError := postHookRunner(&ctx, nil, bifrostErr)

	if HandleStreamControlSkip(processedError) {
		return
	}

	streamResponse := &schemas.BifrostStream{}
	if processedResponse != nil {
		streamResponse.BifrostTextCompletionResponse = processedResponse.TextCompletionResponse
		streamResponse.BifrostChatResponse = processedResponse.ChatResponse
		streamResponse.BifrostResponsesStreamResponse = processedResponse.ResponsesStreamResponse
		streamResponse.BifrostSpeechStreamResponse = processedResponse.SpeechStreamResponse
		streamResponse.BifrostTranscriptionStreamResponse = processedResponse.TranscriptionStreamResponse
	}
	if processedError != nil {
		streamResponse.BifrostError = processedError
	}

	select {
	case responseChan <- streamResponse:
	case <-ctx.Done():
	}
}

// ProcessAndSendError handles post-hook processing and sends the error to the channel.
// This utility reduces code duplication across streaming implementations by encapsulating
// the common pattern of running post hooks, handling errors, and sending responses with
// proper context cancellation handling.
func ProcessAndSendError(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	err error,
	responseChan chan *schemas.BifrostStream,
	requestType schemas.RequestType,
	providerName schemas.ModelProvider,
	model string,
	logger schemas.Logger,
) {
	// Send scanner error through channel
	bifrostError :=
		&schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("Error reading stream: %v", err),
				Error:   err,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RequestType:    requestType,
				Provider:       providerName,
				ModelRequested: model,
			},
		}
	processedResponse, processedError := postHookRunner(&ctx, nil, bifrostError)

	if HandleStreamControlSkip(processedError) {
		return
	}

	streamResponse := &schemas.BifrostStream{}
	if processedResponse != nil {
		streamResponse.BifrostTextCompletionResponse = processedResponse.TextCompletionResponse
		streamResponse.BifrostChatResponse = processedResponse.ChatResponse
		streamResponse.BifrostResponsesStreamResponse = processedResponse.ResponsesStreamResponse
		streamResponse.BifrostSpeechStreamResponse = processedResponse.SpeechStreamResponse
		streamResponse.BifrostTranscriptionStreamResponse = processedResponse.TranscriptionStreamResponse
	}
	if processedError != nil {
		streamResponse.BifrostError = processedError
	}

	select {
	case responseChan <- streamResponse:
	case <-ctx.Done():
	}
}

// CreateBifrostTextCompletionChunkResponse creates a bifrost text completion chunk response.
func CreateBifrostTextCompletionChunkResponse(
	id string,
	usage *schemas.BifrostLLMUsage,
	finishReason *string,
	currentChunkIndex int,
	requestType schemas.RequestType,
	providerName schemas.ModelProvider,
	model string,
) *schemas.BifrostTextCompletionResponse {
	response := &schemas.BifrostTextCompletionResponse{
		ID:     id,
		Object: "text_completion",
		Usage:  usage,
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason:                 finishReason,
				TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{}, // empty delta
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    requestType,
			Provider:       providerName,
			ModelRequested: model,
			ChunkIndex:     currentChunkIndex + 1,
		},
	}
	return response
}

// CreateBifrostChatCompletionChunkResponse creates a bifrost chat completion chunk response.
func CreateBifrostChatCompletionChunkResponse(
	id string,
	usage *schemas.BifrostLLMUsage,
	finishReason *string,
	currentChunkIndex int,
	requestType schemas.RequestType,
	providerName schemas.ModelProvider,
	model string,
) *schemas.BifrostChatResponse {
	response := &schemas.BifrostChatResponse{
		ID:     id,
		Object: "chat.completion.chunk",
		Usage:  usage,
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: finishReason,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{}, // empty delta
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    requestType,
			Provider:       providerName,
			ModelRequested: model,
			ChunkIndex:     currentChunkIndex + 1,
		},
	}
	return response
}

// HandleStreamControlSkip checks if the stream control should be skipped.
func HandleStreamControlSkip(bifrostErr *schemas.BifrostError) bool {
	if bifrostErr == nil || bifrostErr.StreamControl == nil {
		return false
	}
	if bifrostErr.StreamControl.SkipStream != nil && *bifrostErr.StreamControl.SkipStream {
		if bifrostErr.StreamControl.LogError != nil && *bifrostErr.StreamControl.LogError {
			logger.Warn("Error in stream: " + bifrostErr.Error.Message)
		}
		return true
	}
	return false
}

// GetProviderName extracts the provider name from custom provider configuration.
// If a custom provider key is specified, it returns that; otherwise, it returns the default provider.
// Note: CustomProviderKey is internally set by Bifrost and should always match the provider name.
func GetProviderName(defaultProvider schemas.ModelProvider, customConfig *schemas.CustomProviderConfig) schemas.ModelProvider {
	if customConfig != nil {
		if key := strings.TrimSpace(customConfig.CustomProviderKey); key != "" {
			return schemas.ModelProvider(key)
		}
	}
	return defaultProvider
}

// ProviderSendsDoneMarker returns true if the provider sends the [DONE] marker in streaming responses.
// Some OpenAI-compatible providers (like Cerebras) don't send [DONE] and instead end the stream
// after sending the finish_reason. This function helps determine the correct stream termination logic.
func ProviderSendsDoneMarker(providerName schemas.ModelProvider) bool {
	switch providerName {
	case schemas.Cerebras, schemas.Perplexity:
		// Cerebras and Perplexity don't send [DONE] marker, ends stream after finish_reason
		return false
	default:
		// Default to expecting [DONE] marker for safety
		return true
	}
}

func ProviderIsResponsesAPINative(providerName schemas.ModelProvider) bool {
	switch providerName {
	case schemas.OpenAI, schemas.OpenRouter, schemas.Azure:
		return true
	default:
		return false
	}
}

// ReleaseStreamingResponse releases a streaming response by draining the body stream and releasing the response.
func ReleaseStreamingResponse(resp *fasthttp.Response) {
	// Drain any remaining data from the body stream before releasing
	// This prevents "whitespace in header" errors when the response is reused
	if resp.BodyStream() != nil {
		io.Copy(io.Discard, resp.BodyStream())
	}
	fasthttp.ReleaseResponse(resp)
}

// GetBifrostResponseForStreamResponse converts the provided responses to a bifrost response.
func GetBifrostResponseForStreamResponse(
	textCompletionResponse *schemas.BifrostTextCompletionResponse,
	chatResponse *schemas.BifrostChatResponse,
	responsesStreamResponse *schemas.BifrostResponsesStreamResponse,
	speechStreamResponse *schemas.BifrostSpeechStreamResponse,
	transcriptionStreamResponse *schemas.BifrostTranscriptionStreamResponse,
) *schemas.BifrostResponse {
	//TODO add bifrost response pooling here
	bifrostResponse := &schemas.BifrostResponse{}

	switch {
	case textCompletionResponse != nil:
		bifrostResponse.TextCompletionResponse = textCompletionResponse
		return bifrostResponse
	case chatResponse != nil:
		bifrostResponse.ChatResponse = chatResponse
		return bifrostResponse
	case responsesStreamResponse != nil:
		bifrostResponse.ResponsesStreamResponse = responsesStreamResponse
		return bifrostResponse
	case speechStreamResponse != nil:
		bifrostResponse.SpeechStreamResponse = speechStreamResponse
		return bifrostResponse
	case transcriptionStreamResponse != nil:
		bifrostResponse.TranscriptionStreamResponse = transcriptionStreamResponse
		return bifrostResponse
	}
	return nil
}

// aggregateListModelsResponses merges multiple BifrostListModelsResponse objects into a single response.
// It concatenates all model arrays, deduplicates based on model ID, sums up latencies across all responses,
// and concatenates raw responses into an array.
// When duplicate IDs are found, the first occurrence is kept to maintain the original ordering.
func aggregateListModelsResponses(responses []*schemas.BifrostListModelsResponse) *schemas.BifrostListModelsResponse {
	if len(responses) == 0 {
		return &schemas.BifrostListModelsResponse{
			Data: []schemas.Model{},
		}
	}

	if len(responses) == 1 {
		return responses[0]
	}

	// Use a map to track unique model IDs for efficient deduplication
	seenIDs := make(map[string]struct{})
	aggregated := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0),
	}

	// Aggregate all models with deduplication, and collect raw responses
	var rawResponses []interface{}

	for _, response := range responses {
		if response == nil {
			continue
		}

		// Add models, skipping duplicates based on ID
		for _, model := range response.Data {
			if _, exists := seenIDs[model.ID]; !exists {
				seenIDs[model.ID] = struct{}{}
				aggregated.Data = append(aggregated.Data, model)
			}
		}

		// Collect raw response if present
		if response.ExtraFields.RawResponse != nil {
			rawResponses = append(rawResponses, response.ExtraFields.RawResponse)
		}
	}

	// Sort models alphabetically by ID
	sort.Slice(aggregated.Data, func(i, j int) bool {
		return aggregated.Data[i].ID < aggregated.Data[j].ID
	})

	if len(rawResponses) > 0 {
		aggregated.ExtraFields.RawResponse = rawResponses
	}

	return aggregated
}

// extractSuccessfulListModelsResponses extracts successful responses from a results channel
// and tracks the last error encountered. This utility reduces code duplication across providers
// for handling multi-key ListModels requests.
func extractSuccessfulListModelsResponses(
	results chan schemas.ListModelsByKeyResult,
	providerName schemas.ModelProvider,
	logger schemas.Logger,
) ([]*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	var successfulResponses []*schemas.BifrostListModelsResponse
	var lastError *schemas.BifrostError

	for result := range results {
		if result.Err != nil {
			logger.Debug(fmt.Sprintf("failed to list models with key %s: %s", result.KeyID, result.Err.Error.Message))
			lastError = result.Err
			continue
		}

		successfulResponses = append(successfulResponses, result.Response)
	}

	if len(successfulResponses) == 0 {
		if lastError != nil {
			return nil, lastError
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "all keys failed to list models",
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider:    providerName,
				RequestType: schemas.ListModelsRequest,
			},
		}
	}

	return successfulResponses, nil
}

// HandleMultipleListModelsRequests handles multiple list models requests concurrently for different keys.
// It launches concurrent requests for all keys and waits for all goroutines to complete.
// It returns the aggregated response or an error if the request fails.
func HandleMultipleListModelsRequests(
	ctx context.Context,
	keys []schemas.Key,
	request *schemas.BifrostListModelsRequest,
	listModelsByKey func(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError),
	logger schemas.Logger,
) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	startTime := time.Now()

	results := make(chan schemas.ListModelsByKeyResult, len(keys))
	var wg sync.WaitGroup

	// Launch concurrent requests for all keys
	for _, key := range keys {
		wg.Add(1)
		go func(k schemas.Key) {
			defer wg.Done()
			resp, bifrostErr := listModelsByKey(ctx, k, request)
			results <- schemas.ListModelsByKeyResult{Response: resp, Err: bifrostErr, KeyID: k.ID}
		}(key)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(results)

	successfulResponses, err := extractSuccessfulListModelsResponses(results, request.Provider, logger)
	if err != nil {
		return nil, err
	}

	// Aggregate all successful responses
	response := aggregateListModelsResponses(successfulResponses)
	response = response.ApplyPagination(request.PageSize, request.PageToken)

	// Set ExtraFields
	latency := time.Since(startTime)
	response.ExtraFields.Provider = request.Provider
	response.ExtraFields.RequestType = schemas.ListModelsRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}
