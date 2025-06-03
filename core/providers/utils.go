// Package providers implements various LLM providers and their utility functions.
// This file contains common utility functions used across different provider implementations.
package providers

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/goccy/go-json"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"

	"maps"
)

// bifrostResponsePool provides a pool for Bifrost response objects.
var bifrostResponsePool = sync.Pool{
	New: func() interface{} {
		return &schemas.BifrostResponse{}
	},
}

// acquireBifrostResponse gets a Bifrost response from the pool and resets it.
func acquireBifrostResponse() *schemas.BifrostResponse {
	resp := bifrostResponsePool.Get().(*schemas.BifrostResponse)
	*resp = schemas.BifrostResponse{} // Reset the struct
	return resp
}

// releaseBifrostResponse returns a Bifrost response to the pool.
func releaseBifrostResponse(resp *schemas.BifrostResponse) {
	if resp != nil {
		bifrostResponsePool.Put(resp)
	}
}

// mergeConfig merges a default configuration map with custom parameters.
// It creates a new map containing all default values, then overrides them with any custom values.
// Returns a new map containing the merged configuration.
func mergeConfig(defaultConfig map[string]interface{}, customParams map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{})

	// Copy default config
	for k, v := range defaultConfig {
		merged[k] = v
	}

	// Override with custom parameters
	for k, v := range customParams {
		merged[k] = v
	}

	return merged
}

// prepareParams converts ModelParameters into a flat map of parameters.
// It handles both standard fields and extra parameters, using reflection to process
// the struct fields and their JSON tags.
// Returns a map containing all parameters ready for use in API requests.
func prepareParams(params *schemas.ModelParameters) map[string]interface{} {
	flatParams := make(map[string]interface{})

	// Return empty map if params is nil
	if params == nil {
		return flatParams
	}

	// Use reflection to get the type and value of params
	val := reflect.ValueOf(params).Elem()
	typ := val.Type()

	// Iterate through all fields
	for i := range val.NumField() {
		field := val.Field(i)
		fieldType := typ.Field(i)

		// Skip the ExtraParams field as it's handled separately
		if fieldType.Name == "ExtraParams" {
			continue
		}

		// Get the JSON tag name
		jsonTag := fieldType.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Strip out ,omitempty and others from the tag
		jsonTag = strings.Split(jsonTag, ",")[0]

		// Handle pointer fields
		if field.Kind() == reflect.Ptr && !field.IsNil() {
			flatParams[jsonTag] = field.Elem().Interface()
		}
	}

	// Handle ExtraParams
	maps.Copy(flatParams, params.ExtraParams)

	return flatParams
}

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
			Error: schemas.ErrorField{
				Type:    StrPtr(schemas.RequestCancelled),
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
				Error: schemas.ErrorField{
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
	case schemas.HttpProxy:
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
		proxyUrl := proxyConfig.URL
		// Add authentication if provided
		if proxyConfig.Username != "" && proxyConfig.Password != "" {
			parsedURL, err := url.Parse(proxyConfig.URL)
			if err != nil {
				logger.Warn("Invalid proxy configuration: invalid SOCKS5 proxy URL")
				return client
			}
			// Set user and password in the parsed URL
			parsedURL.User = url.UserPassword(proxyConfig.Username, proxyConfig.Password)
			proxyUrl = parsedURL.String()
		}
		dialFunc = fasthttpproxy.FasthttpSocksDialer(proxyUrl)
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

// handleProviderAPIError processes error responses from provider APIs.
// It attempts to unmarshal the error response and returns a BifrostError
// with the appropriate status code and error information.
func handleProviderAPIError(resp *fasthttp.Response, errorResp any) *schemas.BifrostError {
	statusCode := resp.StatusCode()

	if err := json.Unmarshal(resp.Body(), &errorResp); err != nil {
		return &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     &statusCode,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderResponseUnmarshal,
				Error:   err,
			},
		}
	}

	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error:          schemas.ErrorField{},
	}
}

// handleProviderResponse handles common response parsing logic for provider responses.
// It attempts to parse the response body into the provided response type
// and returns either the parsed response or a BifrostError if parsing fails.
func handleProviderResponse[T any](responseBody []byte, response *T) (interface{}, *schemas.BifrostError) {
	var rawResponse interface{}

	var wg sync.WaitGroup
	var structuredErr, rawErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		structuredErr = json.Unmarshal(responseBody, response)
	}()
	go func() {
		defer wg.Done()
		rawErr = json.Unmarshal(responseBody, &rawResponse)
	}()
	wg.Wait()

	if structuredErr != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderDecodeStructured,
				Error:   structuredErr,
			},
		}
	}

	if rawErr != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderDecodeRaw,
				Error:   rawErr,
			},
		}
	}

	return rawResponse, nil
}

// getRoleFromMessage extracts and validates the role from a message map.
func getRoleFromMessage(msg map[string]interface{}) (schemas.ModelChatMessageRole, bool) {
	roleVal, exists := msg["role"]
	if !exists {
		return "", false // Role key doesn't exist
	}

	// Try direct assertion to ModelChatMessageRole
	roleAsModelType, ok := roleVal.(schemas.ModelChatMessageRole)
	if ok {
		return roleAsModelType, true
	}

	// Try assertion to string and then convert
	roleAsString, okStr := roleVal.(string)
	if okStr {
		return schemas.ModelChatMessageRole(roleAsString), true
	}

	return "", false // Role is of an unexpected or invalid type
}

// float64Ptr creates a pointer to a float64 value.
// This is a helper function for creating pointers to float64 values.
func float64Ptr(f float64) *float64 {
	return &f
}

func StrPtr(s string) *string {
	return &s
}
