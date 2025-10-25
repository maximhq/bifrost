// Package providers implements various LLM providers and their utility functions.
// This file contains common utility functions used across different provider implementations.
package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
)

// IMPORTANT: This function does NOT truly cancel the underlying fasthttp network request if the
// context is done. The fasthttp client call will continue in its goroutine until it completes
// or times out based on its own settings. This function merely stops *waiting* for the
// fasthttp call and returns an error related to the context.
// Returns the request latency and any error that occurred.
func makeRequestWithContext(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) (time.Duration, *schemas.BifrostError) {
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
				return latency, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, "")
			}
			// The HTTP request itself failed (e.g., connection error, fasthttp timeout).
			return latency, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderRequest,
					Error:   err,
				},
			}
		}
		// HTTP request was successful from fasthttp's perspective (err is nil).
		// The caller should check resp.StatusCode() for HTTP-level errors (4xx, 5xx).
		return latency, nil
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
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: providerType,
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
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: providerType,
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
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: providerType,
		},
	}
}

func sendCreatedEventResponsesChunk(ctx context.Context, postHookRunner schemas.PostHookRunner, provider schemas.ModelProvider, model string, startTime time.Time, responseChan chan *schemas.BifrostStream) {
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
	processAndSendResponse(ctx, postHookRunner, bifrostResponse, responseChan)
}

// sendInProgressResponsesChunk sends a ResponsesStreamResponseTypeInProgress event
func sendInProgressEventResponsesChunk(ctx context.Context, postHookRunner schemas.PostHookRunner, provider schemas.ModelProvider, model string, startTime time.Time, responseChan chan *schemas.BifrostStream) {
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
	processAndSendResponse(ctx, postHookRunner, bifrostResponse, responseChan)
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
) {
	// Run post hooks on the response
	processedResponse, processedError := postHookRunner(&ctx, response, nil)

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

func createBifrostTextCompletionChunkResponse(
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

func createBifrostChatCompletionChunkResponse(
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

func handleStreamEndWithSuccess(
	ctx context.Context,
	response *schemas.BifrostResponse,
	postHookRunner schemas.PostHookRunner,
	responseChan chan *schemas.BifrostStream,
) {
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
	processAndSendResponse(ctx, postHookRunner, response, responseChan)
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

func getResponsesChunkConverterCombinedPostHookRunner(postHookRunner schemas.PostHookRunner) schemas.PostHookRunner {
	responsesChunkConverter := func(_ *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if result != nil {
			if result.ChatResponse != nil {
				result.ResponsesStreamResponse = result.ChatResponse.ToBifrostResponsesStreamResponse()
				if result.ResponsesResponse == nil {
					result.ResponsesResponse = &schemas.BifrostResponsesResponse{}
				}
				result.ResponsesResponse.ExtraFields = result.ResponsesStreamResponse.ExtraFields
				result.ResponsesResponse.ExtraFields.RequestType = schemas.ResponsesRequest
			}
		} else if err != nil {
			// Ensure downstream knows this is a Responses stream even on errors
			err.ExtraFields.RequestType = schemas.ResponsesStreamRequest
		}
		return result, err
	}

	// Create a combined post hook runner that first converts to responses stream, then runs the original post hooks
	combinedPostHookRunner := func(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		// First run the responses chunk converter
		result, err = responsesChunkConverter(ctx, result, err)
		// Then run the original post hook runner
		return postHookRunner(ctx, result, err)
	}

	return combinedPostHookRunner
}

func getBifrostResponseForStreamResponse(
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
