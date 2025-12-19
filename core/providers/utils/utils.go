// Package providers implements various LLM providers and their utility functions.
// This file contains common utility functions used across different provider implementations.
package utils

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
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
		proxyURL := proxyConfig.URL
		if proxyConfig.Username != "" && proxyConfig.Password != "" {
			parsedURL, err := url.Parse(proxyConfig.URL)
			if err != nil {
				logger.Warn("Invalid proxy configuration: invalid HTTP proxy URL")
				return client
			}
			// Set user and password in the parsed URL
			parsedURL.User = url.UserPassword(proxyConfig.Username, proxyConfig.Password)
			proxyURL = parsedURL.String()
		}
		dialFunc = fasthttpproxy.FasthttpHTTPDialer(proxyURL)
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

	// Configure custom CA certificate if provided
	if proxyConfig.CACertPEM != "" {
		tlsConfig, err := createTLSConfigWithCA(proxyConfig.CACertPEM)
		if err != nil {
			logger.Warn(fmt.Sprintf("Failed to configure custom CA certificate: %v", err))
		} else {
			client.TLSConfig = tlsConfig
		}
	}

	return client
}

// createTLSConfigWithCA creates a TLS configuration with a custom CA certificate
// appended to the system root CA pool.
func createTLSConfigWithCA(caCertPEM string) (*tls.Config, error) {
	// Get the system root CA pool
	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		// If we can't get system certs, create a new pool
		rootCAs = x509.NewCertPool()
	}

	// Append the custom CA certificate
	if !rootCAs.AppendCertsFromPEM([]byte(caCertPEM)) {
		return nil, fmt.Errorf("failed to parse CA certificate PEM")
	}

	return &tls.Config{
		RootCAs:    rootCAs,
		MinVersion: tls.VersionTLS12,
	}, nil
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
		jsonBody, err := sonic.MarshalIndent(convertedBody, "", "  ")
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
// HTML detection only runs if JSON parsing fails to avoid expensive regex operations
// on responses that are almost certainly valid JSON. errorResp must be a pointer to
// the target struct for unmarshaling.
func HandleProviderAPIError(resp *fasthttp.Response, errorResp any) *schemas.BifrostError {
	statusCode := resp.StatusCode()

	// Decode body
	decodedBody, err := CheckAndDecodeBody(resp)
	if err != nil {
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: err.Error(),
			},
		}
	}

	// Check for empty response
	trimmed := strings.TrimSpace(string(decodedBody))
	if len(trimmed) == 0 {
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseEmpty,
			},
		}
	}

	// Try JSON parsing first
	if err := sonic.Unmarshal(decodedBody, errorResp); err == nil {
		// JSON parsing succeeded, return success
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error:          &schemas.ErrorField{},
		}
	}

	// JSON parsing failed - now check if it's an HTML response (expensive operation)
	if IsHTMLResponse(resp, decodedBody) {
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseHTML,
				Error:   errors.New(string(decodedBody)),
			},
		}
	}

	// Not HTML either - return raw response as error message
	message := fmt.Sprintf("provider API error: %s", string(decodedBody))
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: message,
		},
	}
}

// HandleProviderResponse handles common response parsing logic for provider responses.
// It attempts to parse the response body into the provided response type
// and returns either the parsed response or a BifrostError if parsing fails.
// If sendBackRawResponse is true, it returns the raw response interface, otherwise nil.
// HTML detection only runs if JSON parsing fails to avoid expensive regex operations
// on responses that are almost certainly valid JSON.
func HandleProviderResponse[T any](responseBody []byte, response *T, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (rawRequest interface{}, rawResponse interface{}, bifrostErr *schemas.BifrostError) {
	// Check for empty response
	trimmed := strings.TrimSpace(string(responseBody))
	if len(trimmed) == 0 {
		return nil, nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseEmpty,
			},
		}
	}

	var wg sync.WaitGroup
	var structuredErr, rawRequestErr, rawResponseErr error

	// Skip raw request capture if requestBody is nil (e.g., for GET requests)
	shouldCaptureRawRequest := sendBackRawRequest && requestBody != nil

	// Count goroutines to spawn
	numGoroutines := 1 // Always unmarshal structured response
	if shouldCaptureRawRequest {
		numGoroutines++
	}
	if sendBackRawResponse {
		numGoroutines++
	}

	wg.Add(numGoroutines)
	go func() {
		defer wg.Done()
		structuredErr = sonic.Unmarshal(responseBody, response)
	}()

	if shouldCaptureRawRequest {
		go func() {
			defer wg.Done()
			rawRequestErr = sonic.Unmarshal(requestBody, &rawRequest)
		}()
	}

	if sendBackRawResponse {
		go func() {
			defer wg.Done()
			rawResponseErr = sonic.Unmarshal(responseBody, &rawResponse)
		}()
	}
	wg.Wait()

	if structuredErr != nil {
		// JSON parsing failed - check if it's an HTML response (expensive operation)
		if IsHTMLResponse(nil, responseBody) {
			return nil, nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderResponseHTML,
					Error:   errors.New(string(responseBody)),
				},
			}
		}

		return nil, nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseUnmarshal,
				Error:   structuredErr,
			},
		}
	}

	if shouldCaptureRawRequest {
		if rawRequestErr != nil {
			return nil, nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderRawRequestUnmarshal,
					Error:   rawRequestErr,
				},
			}
		}
		if sendBackRawResponse && rawResponseErr != nil {
			return nil, nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderRawResponseUnmarshal,
					Error:   rawResponseErr,
				},
			}
		}
		return rawRequest, rawResponse, nil
	}

	if sendBackRawResponse {
		if rawResponseErr != nil {
			return nil, nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderRawResponseUnmarshal,
					Error:   rawResponseErr,
				},
			}
		}
		return rawRequest, rawResponse, nil
	}

	return nil, nil, nil
}

// ParseAndSetRawRequest parses the raw request body and sets it in the extra fields.
func ParseAndSetRawRequest(extraFields *schemas.BifrostResponseExtraFields, jsonBody []byte) {
	var rawRequest interface{}
	if err := sonic.Unmarshal(jsonBody, &rawRequest); err != nil {
		logger.Warn(fmt.Sprintf("Failed to parse raw request: %v", err))
	} else {
		extraFields.RawRequest = rawRequest
	}
}

// NewUnsupportedOperationError creates a standardized error for unsupported operations.
// This helper reduces code duplication across providers that don't support certain operations.
func NewUnsupportedOperationError(requestType schemas.RequestType, providerName schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: fmt.Sprintf("%s is not supported by %s provider", requestType, providerName),
			Code:    schemas.Ptr("unsupported_operation"),
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

// IsHTMLResponse checks if the response is HTML by examining the Content-Type header
// and/or the response body for HTML indicators.
func IsHTMLResponse(resp *fasthttp.Response, body []byte) bool {
	// Check Content-Type header first (most reliable indicator)
	if resp != nil {
		contentType := strings.ToLower(string(resp.Header.Peek("Content-Type")))
		if strings.Contains(contentType, "text/html") {
			return true
		}
	}

	// If body is small, it's unlikely to be HTML
	if len(body) < 20 {
		return false
	}

	// Check for HTML indicators in body
	bodyLower := strings.ToLower(string(body))

	// Look for common HTML tags or DOCTYPE
	htmlIndicators := []string{
		"<!doctype html",
		"<html",
		"<head",
		"<body",
		"<title>",
		"<h1>",
		"<h2>",
		"<h3>",
		"<p>",
		"<div",
	}

	for _, indicator := range htmlIndicators {
		if strings.Contains(bodyLower, indicator) {
			return true
		}
	}

	return false
}

// Limit body size to prevent ReDoS on very large malicious responses
const maxBodySize = 32 * 1024 // 32KB

// ExtractHTMLErrorMessage extracts meaningful error information from an HTML response.
// It attempts to find error messages from title tags, headers, and visible text.
// UNUSED for now but could be useful in the future
func ExtractHTMLErrorMessage(body []byte) string {
	if len(body) > maxBodySize {
		body = body[:maxBodySize]
	}

	bodyStr := string(body)
	bodyLower := strings.ToLower(bodyStr)

	// Try to extract title first
	if idx := strings.Index(bodyLower, "<title>"); idx != -1 {
		endIdx := strings.Index(bodyLower[idx:], "</title>")
		if endIdx != -1 {
			title := strings.TrimSpace(bodyStr[idx+7 : idx+endIdx])
			if title != "" && title != "Error" {
				return title
			}
		}
	}

	// Try to extract from h1, h2, h3 tags (common for error pages)
	for _, tag := range []string{"h1", "h2", "h3"} {
		pattern := fmt.Sprintf("<%s[^>]*>([^<]+)</%s>", tag, tag)
		re := regexp.MustCompile("(?i)" + pattern)
		if matches := re.FindStringSubmatch(bodyStr); len(matches) > 1 {
			msg := strings.TrimSpace(matches[1])
			if msg != "" {
				return msg
			}
		}
	}

	// Try to extract from meta description
	pattern := `<meta\s+name="description"\s+content="([^"]+)"`
	re := regexp.MustCompile("(?i)" + pattern)
	if matches := re.FindStringSubmatch(bodyStr); len(matches) > 1 {
		msg := strings.TrimSpace(matches[1])
		if msg != "" {
			return msg
		}
	}

	// Extract visible text: remove script and style tags, then extract text
	// Remove script and style tags and their content
	re = regexp.MustCompile(`(?i)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	cleaned := re.ReplaceAllString(bodyStr, "")

	// Remove HTML tags
	re = regexp.MustCompile(`<[^>]+>`)
	cleaned = re.ReplaceAllString(cleaned, " ")

	// Clean up whitespace and get first meaningful sentence
	sentences := strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == '\n' || r == '\r'
	})

	for _, sentence := range sentences {
		trimmed := strings.TrimSpace(sentence)
		if len(trimmed) > 10 && len(trimmed) < 500 {
			// Limit to first 200 chars to avoid very long messages
			if len(trimmed) > 200 {
				trimmed = trimmed[:200] + "..."
			}
			return trimmed
		}
	}

	// If all else fails, return a generic message with status code context
	return "HTML error response received from provider"
}

// JSONLParseResult holds parsed items and any line-level errors encountered during parsing.
type JSONLParseResult struct {
	Errors []schemas.BatchError
}

// ParseJSONL parses JSONL data line by line, calling the provided callback for each line.
// It collects parse errors with line numbers rather than silently skipping failed lines.
// The callback receives the line bytes and returns an error if parsing fails.
// This function operates directly on byte slices to avoid unnecessary string conversions.
func ParseJSONL(data []byte, parseLine func(line []byte) error) JSONLParseResult {
	result := JSONLParseResult{}

	lineNum := 0
	start := 0

	for i := 0; i <= len(data); i++ {
		// Check for newline or end of data
		if i == len(data) || data[i] == '\n' {
			lineNum++

			// Extract the line (excluding the newline character)
			end := i
			if end > start {
				line := data[start:end]

				// Trim trailing carriage return for Windows-style line endings
				if len(line) > 0 && line[len(line)-1] == '\r' {
					line = line[:len(line)-1]
				}

				// Skip empty lines
				if len(line) > 0 {
					if err := parseLine(line); err != nil {
						lineNumCopy := lineNum
						result.Errors = append(result.Errors, schemas.BatchError{
							Code:    "parse_error",
							Message: err.Error(),
							Line:    &lineNumCopy,
						})
					}
				}
			}

			start = i + 1
		}
	}

	return result
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

// RequestMetadata contains metadata about a request for error reporting.
// This struct is used to pass request context to parseError functions.
type RequestMetadata struct {
	Provider    schemas.ModelProvider
	Model       string
	RequestType schemas.RequestType
}

// ShouldSendBackRawRequest checks if the raw request should be sent back.
// Context overrides are intentionally restricted to asymmetric behavior: a context value can only
// promote false→true and will not override a true config to false, avoiding accidental suppression.
func ShouldSendBackRawRequest(ctx context.Context, defaultSendBackRawRequest bool) bool {
	if sendBackRawRequest, ok := ctx.Value(schemas.BifrostContextKeySendBackRawRequest).(bool); ok && sendBackRawRequest {
		return sendBackRawRequest
	}
	return defaultSendBackRawRequest
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
	case schemas.Cerebras, schemas.Perplexity, schemas.HuggingFace:
		// Cerebras, Perplexity, and HuggingFace don't send [DONE] marker, ends stream after finish_reason
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

	// Always apply deduplication, even for single responses

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

// GetRandomString generates a random alphanumeric string of the given length.
func GetRandomString(length int) string {
	if length <= 0 {
		return ""
	}
	randomSource := rand.New(rand.NewSource(time.Now().UnixNano()))
	letters := []rune("abcdefghijklmnopqrstuvwxyz0123456789")
	b := make([]rune, length)
	for i := range b {
		b[i] = letters[randomSource.Intn(len(letters))]
	}
	return string(b)
}

// GetReasoningEffortFromBudgetTokens maps a reasoning token budget to OpenAI reasoning effort.
// Valid values: none, low, medium, high
func GetReasoningEffortFromBudgetTokens(
	budgetTokens int,
	minBudgetTokens int,
	maxTokens int,
) string {
	if budgetTokens <= 0 {
		return "none"
	}

	// Defensive defaults
	if maxTokens <= 0 {
		return "medium"
	}

	// Normalize budget
	if budgetTokens < minBudgetTokens {
		budgetTokens = minBudgetTokens
	}
	if budgetTokens > maxTokens {
		budgetTokens = maxTokens
	}

	// Avoid division by zero
	if maxTokens <= minBudgetTokens {
		return "high"
	}

	ratio := float64(budgetTokens-minBudgetTokens) / float64(maxTokens-minBudgetTokens)

	switch {
	case ratio <= 0.25:
		return "low"
	case ratio <= 0.60:
		return "medium"
	default:
		return "high"
	}
}

// GetBudgetTokensFromReasoningEffort converts OpenAI reasoning effort
// into a reasoning token budget.
// effort ∈ {"none", "minimal", "low", "medium", "high"}
func GetBudgetTokensFromReasoningEffort(
	effort string,
	minBudgetTokens int,
	maxTokens int,
) (int, error) {
	if effort == "none" {
		return 0, nil
	}

	if minBudgetTokens > maxTokens {
		return 0, fmt.Errorf("max_tokens must be greater than %d for reasoning", minBudgetTokens)
	}

	// Defensive defaults
	if maxTokens <= minBudgetTokens {
		return minBudgetTokens, nil
	}

	var ratio float64

	switch effort {
	case "minimal":
		ratio = 0.025
	case "low":
		ratio = 0.15
	case "medium":
		ratio = 0.425
	case "high":
		ratio = 0.80
	default:
		// Unknown effort → safe default
		ratio = 0.425
	}

	budget := minBudgetTokens + int(ratio*float64(maxTokens-minBudgetTokens))

	return budget, nil
}
