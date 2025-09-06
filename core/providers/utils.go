// Package providers implements various LLM providers and their utility functions.
// This file contains common utility functions used across different provider implementations.
package providers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"

	"maps"
)

// dataURIRegex is a precompiled regex for matching data URI format patterns.
// It matches patterns like: data:image/png;base64,iVBORw0KGgo...
var dataURIRegex = regexp.MustCompile(`^data:([^;]+)(;base64)?,(.+)$`)

// base64Regex is a precompiled regex for matching base64 strings.
// It matches strings containing only valid base64 characters with optional padding.
var base64Regex = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)

// fileExtensionToMediaType maps common image file extensions to their corresponding media types.
// This map is used to infer media types from file extensions in URLs.
var fileExtensionToMediaType = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".bmp":  "image/bmp",
}

// ImageContentType represents the type of image content
type ImageContentType string

const (
	ImageContentTypeBase64 ImageContentType = "base64"
	ImageContentTypeURL    ImageContentType = "url"
)

// URLTypeInfo contains extracted information about a URL
type URLTypeInfo struct {
	Type                 ImageContentType
	MediaType            *string
	DataURLWithoutPrefix *string // URL without the prefix (eg data:image/png;base64,iVBORw0KGgo...)
}

// ContextKey is a custom type for context keys to prevent key collisions in the context.
// It provides type safety for context values and ensures that context keys are unique
// across different packages.
type ContextKey string

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
				Type:    Ptr(schemas.RequestCancelled),
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

// setExtraHeaders sets additional headers from NetworkConfig to the fasthttp request.
// This allows users to configure custom headers for their provider requests.
// Header keys are canonicalized using textproto.CanonicalMIMEHeaderKey to avoid duplicates.
// The Authorization header is excluded for security reasons.
// It accepts a list of headers (all canonicalized) to skip for security reasons.
// Headers are only set if they don't already exist on the request to avoid overwriting important headers.
func setExtraHeaders(req *fasthttp.Request, extraHeaders map[string]string, skipHeaders *[]string) {
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
			if slices.Contains(*skipHeaders, key) {
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
func setExtraHeadersHTTP(req *http.Request, extraHeaders map[string]string, skipHeaders *[]string) {
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
			if slices.Contains(*skipHeaders, key) {
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
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderDecodeStructured,
				Error:   structuredErr,
			},
		}
	}

	if sendBackRawResponse {
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

	return nil, nil
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

// Ptr creates a pointer to any value.
// This is a helper function for creating pointers to values.
func Ptr[T any](v T) *T {
	return &v
}

//* IMAGE UTILS *//

// SanitizeImageURL sanitizes and validates an image URL.
// It handles both data URLs and regular HTTP/HTTPS URLs.
// It also detects raw base64 image data and adds proper data URL headers.
func SanitizeImageURL(rawURL string) (string, error) {
	if rawURL == "" {
		return rawURL, fmt.Errorf("URL cannot be empty")
	}

	// Trim whitespace
	rawURL = strings.TrimSpace(rawURL)

	// Check if it's already a proper data URL
	if strings.HasPrefix(rawURL, "data:") {
		// Validate data URL format
		if !dataURIRegex.MatchString(rawURL) {
			return rawURL, fmt.Errorf("invalid data URL format")
		}
		return rawURL, nil
	}

	// Check if it looks like raw base64 image data
	if isLikelyBase64(rawURL) {
		// Detect the image type from the base64 data
		mediaType := detectImageTypeFromBase64(rawURL)

		// Remove any whitespace/newlines from base64 data
		cleanBase64 := strings.ReplaceAll(strings.ReplaceAll(rawURL, "\n", ""), " ", "")

		// Create proper data URL
		return fmt.Sprintf("data:%s;base64,%s", mediaType, cleanBase64), nil
	}

	// Parse as regular URL
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, fmt.Errorf("invalid URL format: %w", err)
	}

	// Validate scheme
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return rawURL, fmt.Errorf("URL must use http or https scheme")
	}

	// Validate host
	if parsedURL.Host == "" {
		return rawURL, fmt.Errorf("URL must have a valid host")
	}

	return parsedURL.String(), nil
}

// ExtractURLTypeInfo extracts type and media type information from a sanitized URL.
// For data URLs, it parses the media type and encoding.
// For regular URLs, it attempts to infer the media type from the file extension.
func ExtractURLTypeInfo(sanitizedURL string) URLTypeInfo {
	if strings.HasPrefix(sanitizedURL, "data:") {
		return extractDataURLInfo(sanitizedURL)
	}
	return extractRegularURLInfo(sanitizedURL)
}

// extractDataURLInfo extracts information from a data URL
func extractDataURLInfo(dataURL string) URLTypeInfo {
	// Parse data URL: data:[<mediatype>][;base64],<data>
	matches := dataURIRegex.FindStringSubmatch(dataURL)

	if len(matches) != 4 {
		return URLTypeInfo{Type: ImageContentTypeBase64}
	}

	mediaType := matches[1]
	isBase64 := matches[2] == ";base64"

	dataURLWithoutPrefix := dataURL
	if isBase64 {
		dataURLWithoutPrefix = dataURL[len("data:")+len(mediaType)+len(";base64,"):]
	}

	info := URLTypeInfo{
		MediaType:            &mediaType,
		DataURLWithoutPrefix: &dataURLWithoutPrefix,
	}

	if isBase64 {
		info.Type = ImageContentTypeBase64
	} else {
		info.Type = ImageContentTypeURL // Non-base64 data URL
	}

	return info
}

// extractRegularURLInfo extracts information from a regular HTTP/HTTPS URL
func extractRegularURLInfo(regularURL string) URLTypeInfo {
	info := URLTypeInfo{
		Type: ImageContentTypeURL,
	}

	// Try to infer media type from file extension
	parsedURL, err := url.Parse(regularURL)
	if err != nil {
		return info
	}

	path := strings.ToLower(parsedURL.Path)

	// Check for known file extensions using the map
	for ext, mediaType := range fileExtensionToMediaType {
		if strings.HasSuffix(path, ext) {
			info.MediaType = &mediaType
			break
		}
	}
	// For URLs without recognizable extensions, MediaType remains nil

	return info
}

// detectImageTypeFromBase64 detects the image type from base64 data by examining the header bytes
func detectImageTypeFromBase64(base64Data string) string {
	// Remove any whitespace or newlines
	cleanData := strings.ReplaceAll(strings.ReplaceAll(base64Data, "\n", ""), " ", "")

	// Check common image format signatures in base64
	switch {
	case strings.HasPrefix(cleanData, "/9j/") || strings.HasPrefix(cleanData, "/9k/"):
		// JPEG images typically start with /9j/ or /9k/ in base64 (FFD8 in hex)
		return "image/jpeg"
	case strings.HasPrefix(cleanData, "iVBORw0KGgo"):
		// PNG images start with iVBORw0KGgo in base64 (89504E470D0A1A0A in hex)
		return "image/png"
	case strings.HasPrefix(cleanData, "R0lGOD"):
		// GIF images start with R0lGOD in base64 (474946 in hex)
		return "image/gif"
	case strings.HasPrefix(cleanData, "Qk"):
		// BMP images start with Qk in base64 (424D in hex)
		return "image/bmp"
	case strings.HasPrefix(cleanData, "UklGR") && len(cleanData) >= 16 && cleanData[12:16] == "V0VC":
		// WebP images start with RIFF header (UklGR in base64) and have WEBP signature at offset 8-11 (V0VC in base64)
		return "image/webp"
	case strings.HasPrefix(cleanData, "PHN2Zy") || strings.HasPrefix(cleanData, "PD94bW"):
		// SVG images often start with <svg or <?xml in base64
		return "image/svg+xml"
	default:
		// Default to JPEG for unknown formats
		return "image/jpeg"
	}
}

// isLikelyBase64 checks if a string looks like base64 data
func isLikelyBase64(s string) bool {
	// Remove whitespace for checking
	cleanData := strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), " ", "")

	// Check if it contains only base64 characters using pre-compiled regex
	return base64Regex.MatchString(cleanData)
}

var (
	riff = []byte("RIFF")
	wave = []byte("WAVE")
	id3  = []byte("ID3")
	form = []byte("FORM")
	aiff = []byte("AIFF")
	aifc = []byte("AIFC")
	flac = []byte("fLaC")
	oggs = []byte("OggS")
	adif = []byte("ADIF")
)

// detectAudioMimeType attempts to detect the MIME type from audio file headers
// Gemini supports: WAV, MP3, AIFF, AAC, OGG Vorbis, FLAC
func detectAudioMimeType(audioData []byte) string {
	if len(audioData) < 4 {
		return "audio/mp3"
	}
	// WAV (RIFF/WAVE)
	if len(audioData) >= 12 &&
		bytes.Equal(audioData[:4], riff) &&
		bytes.Equal(audioData[8:12], wave) {
		return "audio/wav"
	}
	// MP3: ID3v2 tag (keep this check for MP3)
	if len(audioData) >= 3 && bytes.Equal(audioData[:3], id3) {
		return "audio/mp3"
	}
	// AAC: ADIF or ADTS (0xFFF sync) - check before MP3 frame sync to avoid misclassification
	if bytes.HasPrefix(audioData, adif) {
		return "audio/aac"
	}
	if len(audioData) >= 2 && audioData[0] == 0xFF && (audioData[1]&0xF6) == 0xF0 {
		return "audio/aac"
	}
	// AIFF / AIFC (map both to audio/aiff)
	if len(audioData) >= 12 && bytes.Equal(audioData[:4], form) &&
		(bytes.Equal(audioData[8:12], aiff) || bytes.Equal(audioData[8:12], aifc)) {
		return "audio/aiff"
	}
	// FLAC
	if bytes.HasPrefix(audioData, flac) {
		return "audio/flac"
	}
	// OGG container
	if bytes.HasPrefix(audioData, oggs) {
		return "audio/ogg"
	}
	// MP3: MPEG frame sync (cover common variants) - check after AAC to avoid misclassification
	if len(audioData) >= 2 && audioData[0] == 0xFF &&
		(audioData[1] == 0xFB || audioData[1] == 0xF3 || audioData[1] == 0xF2 || audioData[1] == 0xFA) {
		return "audio/mp3"
	}
	// Fallback within supported set
	return "audio/mp3"
}

// newUnsupportedOperationError creates a standardized error for unsupported operations.
// This helper reduces code duplication across providers that don't support certain operations.
func newUnsupportedOperationError(operation string, providerName string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Provider:       schemas.ModelProvider(providerName),
		Error: schemas.ErrorField{
			Message: fmt.Sprintf("%s is not supported by %s provider", operation, providerName),
		},
	}
}

// checkOperationAllowed enforces per-op gating using schemas.Operation.
// Behavior:
// - If no gating is configured (config == nil or AllowedRequests == nil), the operation is allowed.
// - If gating is configured, returns an error when the operation is not explicitly allowed.
func checkOperationAllowed(defaultProvider schemas.ModelProvider, config *schemas.CustomProviderConfig, operation schemas.Operation) *schemas.BifrostError {
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
		Provider:       providerType,
		Error: schemas.ErrorField{
			Message: message,
		},
	}
}

// newBifrostOperationError creates a standardized error for bifrost operation errors.
// This helper reduces code duplication across providers that have bifrost operation errors.
func newBifrostOperationError(message string, err error, providerType schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: true,
		Provider:       providerType,
		Error: schemas.ErrorField{
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
		Provider:       providerType,
		StatusCode:     &statusCode,
		Type:           errorType,
		EventID:        eventID,
		Error: schemas.ErrorField{
			Message: message,
			Error:   err,
			Type:    errorType,
		},
	}
}

// approximateTokenCount provides a rough approximation of token count for text.
// WARNING: This is a best-effort approximation using 1 token per 4 characters.
// This heuristic is particularly inaccurate for:
// - Non-ASCII text (multi-byte characters)
// - Short texts
// - Different languages and tokenization methods
// - Various model-specific tokenizers
//
// The actual token count may vary significantly based on tokenization method,
// language, and text structure. Consider omitting token metrics when precise
// counts are unavailable to avoid misleading usage information.
//
// For precise token usage tracking, implement a proper tokenizer that matches
// the model's tokenization method.
func approximateTokenCount(texts []string) int {
	totalInputTokens := 0
	for _, text := range texts {
		// Rough approximation: 1 token per 4 characters
		totalInputTokens += len(text) / 4
	}
	return totalInputTokens
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
	logger schemas.Logger,
) {
	// Send scanner error through channel
	bifrostError :=
		&schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: fmt.Sprintf("Error reading stream: %v", err),
				Error:   err,
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
