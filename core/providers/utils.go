// Package providers implements various LLM providers and their utility functions.
// This file contains common utility functions used across different provider implementations.
package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
)

// IMPORTANT: This function does NOT truly cancel the underlying fasthttp network request if the
// context is done. The fasthttp client call will continue in its goroutine until it completes
// or times out based on its own settings. This function merely stops *waiting* for the
// fasthttp call and returns an error related to the context.
func makeRequestWithContext(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) *schemas.BifrostError {
	errChan := make(chan error, 1)

	go func() {
		// client.Do is a blocking call.
		// It will send an error (or nil for success) to errChan when it completes.
		errChan <- client.Do(req, resp)
	}()

	select {
	case <-ctx.Done():
		// Context was cancelled (e.g., deadline exceeded or manual cancellation).
		// Return a BifrostError indicating this.
		return &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Type:    schemas.Ptr(schemas.RequestCancelled),
				Message: fmt.Sprintf("Request cancelled or timed out by context: %v", ctx.Err()),
				Error:   ctx.Err(),
			},
		}
	case err := <-errChan:
		// The fasthttp.Do call completed.
		if err != nil {
			// The HTTP request itself failed (e.g., connection error, fasthttp timeout).
			return &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderRequest,
					Error:   err,
				},
			}
		}
		// HTTP request was successful from fasthttp's perspective (err is nil).
		// The caller should check resp.StatusCode() for HTTP-level errors (4xx, 5xx).
		return nil
	}
}

// configureProxy sets up a proxy for the fasthttp client based on the provided configuration.
// It supports HTTP, SOCKS5, and environment-based proxy configurations.
// Returns the configured client or the original client if proxy configuration is invalid.
func configureProxy(client *fasthttp.Client, proxyConfig *schemas.ProxyConfig, logger schemas.Logger) *fasthttp.Client {
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

// setExtraHeaders sets additional headers from NetworkConfig to the fasthttp request.
// This allows users to configure custom headers for their provider requests.
// Header keys are canonicalized using textproto.CanonicalMIMEHeaderKey to avoid duplicates.
// The Authorization header is excluded for security reasons.
// It accepts a list of headers (all canonicalized) to skip for security reasons.
// Headers are only set if they don't already exist on the request to avoid overwriting important headers.
func setExtraHeaders(req *fasthttp.Request, extraHeaders map[string]string, skipHeaders []string) {
	if extraHeaders == nil {
		return
	}

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
}

// setExtraHeadersHTTP sets additional headers from NetworkConfig to the standard HTTP request.
// This allows users to configure custom headers for their provider requests.
// Header keys are canonicalized using textproto.CanonicalMIMEHeaderKey to avoid duplicates.
// It accepts a list of headers (all canonicalized) to skip for security reasons.
// Headers are only set if they don't already exist on the request to avoid overwriting important headers.
func setExtraHeadersHTTP(req *http.Request, extraHeaders map[string]string, skipHeaders []string) {
	if extraHeaders == nil {
		return
	}

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
}

// handleProviderAPIError processes error responses from provider APIs.
// It attempts to unmarshal the error response and returns a BifrostError
// with the appropriate status code and error information.
func handleProviderAPIError(resp *fasthttp.Response, errorResp any) *schemas.BifrostError {
	statusCode := resp.StatusCode()

	if err := sonic.Unmarshal(resp.Body(), &errorResp); err != nil {
		return &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseUnmarshal,
				Error:   err,
			},
		}
	}

	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{},
	}
}

// handleProviderResponse handles common response parsing logic for provider responses.
// It attempts to parse the response body into the provided response type
// and returns either the parsed response or a BifrostError if parsing fails.
// If sendBackRawResponse is true, it returns the raw response interface, otherwise nil.
func handleProviderResponse[T any](responseBody []byte, response *T, sendBackRawResponse bool) (interface{}, *schemas.BifrostError) {
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
				Message: schemas.ErrProviderDecodeStructured,
				Error:   structuredErr,
			},
		}
	}

	if sendBackRawResponse {
		if rawErr != nil {
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderDecodeRaw,
					Error:   rawErr,
				},
			}
		}

		return rawResponse, nil
	}

	return nil, nil
}

// newUnsupportedOperationError creates a standardized error for unsupported operations.
// This helper reduces code duplication across providers that don't support certain operations.
func newUnsupportedOperationError(operation string, providerName string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: fmt.Sprintf("%s is not supported by %s provider", operation, providerName),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:    schemas.ModelProvider(providerName),
			RequestType: schemas.RequestType(operation),
		},
	}
}

// checkOperationAllowed enforces per-op gating using schemas.Operation.
// Behavior:
// - If no gating is configured (config == nil or AllowedRequests == nil), the operation is allowed.
// - If gating is configured, returns an error when the operation is not explicitly allowed.
func checkOperationAllowed(defaultProvider schemas.ModelProvider, config *schemas.CustomProviderConfig, operation schemas.RequestType) *schemas.BifrostError {
	// No gating configured => allowed
	if config == nil || config.AllowedRequests == nil {
		return nil
	}
	// Explicitly allowed?
	if config.IsOperationAllowed(operation) {
		return nil
	}
	// Gated and not allowed
	resolved := getProviderName(defaultProvider, config)
	return newUnsupportedOperationError(string(operation), string(resolved))
}

// newConfigurationError creates a standardized error for configuration errors.
// This helper reduces code duplication across providers that have configuration errors.
func newConfigurationError(message string, providerType schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: message,
		},
	}
}

// newBifrostOperationError creates a standardized error for bifrost operation errors.
// This helper reduces code duplication across providers that have bifrost operation errors.
func newBifrostOperationError(message string, err error, providerType schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: message,
			Error:   err,
		},
	}
}

// newProviderAPIError creates a standardized error for provider API errors.
// This helper reduces code duplication across providers that have provider API errors.
func newProviderAPIError(message string, err error, statusCode int, providerType schemas.ModelProvider, errorType *string, eventID *string) *schemas.BifrostError {
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
	}
}

// processAndSendResponse handles post-hook processing and sends the response to the channel.
// This utility reduces code duplication across streaming implementations by encapsulating
// the common pattern of running post hooks, handling errors, and sending responses with
// proper context cancellation handling.
func processAndSendResponse(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	response *schemas.BifrostResponse,
	responseChan chan *schemas.BifrostStream,
	logger schemas.Logger,
) {
	// Run post hooks on the response
	processedResponse, bifrostErr := postHookRunner(&ctx, response, nil)
	if bifrostErr != nil {
		// check if it is a stream error
		if handleStreamControlSkip(logger, bifrostErr) {
			return
		}

		// Send error response and close channel
		errorResponse := &schemas.BifrostStream{
			BifrostError: bifrostErr,
		}

		// Try to send error response before closing
		select {
		case responseChan <- errorResponse:
		case <-ctx.Done():
		}
		return
	}

	// Send the response
	select {
	case responseChan <- &schemas.BifrostStream{
		BifrostResponse: processedResponse,
		BifrostError:    bifrostErr,
	}:
	case <-ctx.Done():
		return
	}
}

// processAndSendBifrostError handles post-hook processing and sends the bifrost error to the channel.
// This utility reduces code duplication across streaming implementations by encapsulating
// the common pattern of running post hooks, handling errors, and sending responses with
// proper context cancellation handling.
func processAndSendBifrostError(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	bifrostErr *schemas.BifrostError,
	responseChan chan *schemas.BifrostStream,
	logger schemas.Logger,
) {
	// Send scanner error through channel
	processedResponse, processedError := postHookRunner(&ctx, nil, bifrostErr)

	if handleStreamControlSkip(logger, processedError) {
		return
	}

	errorResponse := &schemas.BifrostStream{
		BifrostResponse: processedResponse,
		BifrostError:    processedError,
	}
	select {
	case responseChan <- errorResponse:
	case <-ctx.Done():
	}
}

// processAndSendError handles post-hook processing and sends the error to the channel.
// This utility reduces code duplication across streaming implementations by encapsulating
// the common pattern of running post hooks, handling errors, and sending responses with
// proper context cancellation handling.
func processAndSendError(
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

	if handleStreamControlSkip(logger, processedError) {
		return
	}

	errorResponse := &schemas.BifrostStream{
		BifrostResponse: processedResponse,
		BifrostError:    processedError,
	}
	select {
	case responseChan <- errorResponse:
	case <-ctx.Done():
	}
}

func createBifrostChatCompletionChunkResponse(
	id string,
	usage *schemas.LLMUsage,
	finishReason *string,
	currentChunkIndex int,
	requestType schemas.RequestType,
	providerName schemas.ModelProvider,
	model string,
) *schemas.BifrostResponse {
	response := &schemas.BifrostResponse{
		ID:     id,
		Object: "chat.completion.chunk",
		Usage:  usage,
		Choices: []schemas.BifrostChatResponseChoice{
			{
				FinishReason: finishReason,
				BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
					Delta: schemas.BifrostStreamDelta{}, // empty delta
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

func handleStreamEndWithSuccess(
	ctx context.Context,
	response *schemas.BifrostResponse,
	postHookRunner schemas.PostHookRunner,
	responseChan chan *schemas.BifrostStream,
	logger schemas.Logger,
) {
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
	processAndSendResponse(ctx, postHookRunner, response, responseChan, logger)
}

func handleStreamControlSkip(logger schemas.Logger, bifrostErr *schemas.BifrostError) bool {
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

// getProviderName extracts the provider name from custom provider configuration.
// If a custom provider key is specified, it returns that; otherwise, it returns the default provider.
// Note: CustomProviderKey is internally set by Bifrost and should always match the provider name.
func getProviderName(defaultProvider schemas.ModelProvider, customConfig *schemas.CustomProviderConfig) schemas.ModelProvider {
	if customConfig != nil {
		if key := strings.TrimSpace(customConfig.CustomProviderKey); key != "" {
			return schemas.ModelProvider(key)
		}
	}
	return defaultProvider
}
