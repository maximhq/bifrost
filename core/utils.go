package bifrost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Define a set of retryable status codes
var retryableStatusCodes = map[int]bool{
	500: true, // Internal Server Error
	502: true, // Bad Gateway
	503: true, // Service Unavailable
	504: true, // Gateway Timeout
	429: true, // Too Many Requests
}

// Define rate limit error message patterns (case-insensitive)
var rateLimitPatterns = []string{
	"rate limit",
	"rate_limit",
	"ratelimit",
	"too many requests",
	"quota exceeded",
	"quota_exceeded",
	"request limit",
	"throttled",
	"throttling",
	"rate exceeded",
	"limit exceeded",
	"requests per",
	"rpm exceeded",
	"tpm exceeded",
	"tokens per minute",
	"requests per minute",
	"requests per second",
	"api rate limit",
	"usage limit",
	"concurrent requests limit",
}

// dynamicallyConfigurableProviders is the list of providers that can be dynamically configured.
// Excluding providers that require extra configuration. (like Ollama and SGL)
var dynamicallyConfigurableProviders = []schemas.ModelProvider{
	schemas.Anthropic,
	schemas.Azure,
	schemas.Bedrock,
	schemas.Cerebras,
	schemas.Cohere,
	schemas.Elevenlabs,
	schemas.Gemini,
	schemas.Groq,
	schemas.HuggingFace,
	schemas.Mistral,
	schemas.Nebius,
	schemas.OpenAI,
	schemas.OpenRouter,
	schemas.Parasail,
	schemas.Perplexity,
	schemas.Vertex,
	schemas.XAI,
}

// isModelRequired returns true if the request type requires a model
func isModelRequired(reqType schemas.RequestType) bool {
	return reqType == schemas.TextCompletionRequest || reqType == schemas.TextCompletionStreamRequest || reqType == schemas.ChatCompletionRequest || reqType == schemas.ChatCompletionStreamRequest || reqType == schemas.ResponsesRequest || reqType == schemas.ResponsesStreamRequest || reqType == schemas.SpeechRequest || reqType == schemas.SpeechStreamRequest || reqType == schemas.TranscriptionRequest || reqType == schemas.TranscriptionStreamRequest || reqType == schemas.EmbeddingRequest || reqType == schemas.ImageGenerationRequest || reqType == schemas.ImageGenerationStreamRequest
}

// Ptr returns a pointer to the given value.
func Ptr[T any](v T) *T {
	return &v
}

// providerRequiresKey returns true if the given provider requires an API key for authentication.
// Some providers like Ollama and SGL are keyless and don't require API keys.
func providerRequiresKey(providerKey schemas.ModelProvider, customConfig *schemas.CustomProviderConfig) bool {
	// Keyless custom providers are not allowed for Bedrock.
	if customConfig != nil && customConfig.IsKeyLess && customConfig.BaseProviderType != schemas.Bedrock {
		return false
	}
	return providerKey != schemas.Ollama && providerKey != schemas.SGL
}

// canProviderKeyValueBeEmpty returns true if the given provider allows the API key to be empty.
// Some providers like Vertex and Bedrock have their credentials in additional key configs..
func canProviderKeyValueBeEmpty(providerKey schemas.ModelProvider) bool {
	return providerKey == schemas.Vertex || providerKey == schemas.Bedrock
}

func isKeySkippingAllowed(providerKey schemas.ModelProvider) bool {
	return providerKey != schemas.Azure && providerKey != schemas.Bedrock && providerKey != schemas.Vertex
}

// calculateBackoff implements exponential backoff with jitter for retry attempts.
func calculateBackoff(attempt int, config *schemas.ProviderConfig) time.Duration {
	// Calculate an exponential backoff: initial * 2^attempt
	backoff := min(config.NetworkConfig.RetryBackoffInitial*time.Duration(1<<uint(attempt)), config.NetworkConfig.RetryBackoffMax)
	// Add jitter (20%)
	jitter := float64(backoff) * (0.8 + 0.4*rand.Float64())
	result := time.Duration(jitter)
	// Ensure we never exceed the configured maximum
	return min(result, config.NetworkConfig.RetryBackoffMax)
}

// validateRequest validates the given request.
func validateRequest(req *schemas.BifrostRequest) *schemas.BifrostError {
	if req == nil {
		return newBifrostErrorFromMsg("bifrost request cannot be nil")
	}
	provider, model, _ := req.GetRequestFields()
	if provider == "" {
		return newBifrostErrorFromMsg("provider is required")
	}
	if isModelRequired(req.RequestType) && model == "" {
		return newBifrostErrorFromMsg("model is required")
	}
	return nil
}

// IsRateLimitErrorMessage checks if an error message indicates a rate limit issue
func IsRateLimitErrorMessage(errorMessage string) bool {
	if errorMessage == "" {
		return false
	}

	// Convert to lowercase for case-insensitive matching
	lowerMessage := strings.ToLower(errorMessage)

	// Check if any rate limit pattern is found in the error message
	for _, pattern := range rateLimitPatterns {
		if strings.Contains(lowerMessage, pattern) {
			return true
		}
	}

	return false
}

// newBifrostError wraps a standard error into a BifrostError with IsBifrostError set to false.
// This helper function reduces code duplication when handling non-Bifrost errors.
func newBifrostError(err error) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: err.Error(),
			Error:   err,
		},
	}
}

// newBifrostErrorFromMsg creates a BifrostError with a custom message.
// This helper function is used for static error messages.
func newBifrostErrorFromMsg(message string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: message,
		},
	}
}

// newBifrostMessageChan creates a channel that sends a bifrost response.
// It is used to send a bifrost response to the client.
func newBifrostMessageChan(message *schemas.BifrostResponse) chan *schemas.BifrostStreamChunk {
	ch := make(chan *schemas.BifrostStreamChunk)

	go func() {
		defer close(ch)
		ch <- &schemas.BifrostStreamChunk{
			BifrostTextCompletionResponse:      message.TextCompletionResponse,
			BifrostChatResponse:                message.ChatResponse,
			BifrostResponsesStreamResponse:     message.ResponsesStreamResponse,
			BifrostSpeechStreamResponse:        message.SpeechStreamResponse,
			BifrostTranscriptionStreamResponse: message.TranscriptionStreamResponse,
		}
	}()

	return ch
}

var supportedBaseProvidersSet = func() map[schemas.ModelProvider]struct{} {
	m := make(map[schemas.ModelProvider]struct{}, len(schemas.SupportedBaseProviders))
	for _, p := range schemas.SupportedBaseProviders {
		m[p] = struct{}{}
	}
	return m
}()

// IsSupportedBaseProvider reports whether providerKey is allowed as a base provider
// for custom providers.
func IsSupportedBaseProvider(providerKey schemas.ModelProvider) bool {
	_, ok := supportedBaseProvidersSet[providerKey]
	return ok
}

var standardProvidersSet = func() map[schemas.ModelProvider]struct{} {
	m := make(map[schemas.ModelProvider]struct{}, len(schemas.StandardProviders))
	for _, p := range schemas.StandardProviders {
		m[p] = struct{}{}
	}
	return m
}()

// IsStandardProvider reports whether providerKey is a built-in (non-custom) provider.
func IsStandardProvider(providerKey schemas.ModelProvider) bool {
	_, ok := standardProvidersSet[providerKey]
	return ok
}

// IsStreamRequestType returns true if the given request type is a stream request.
func IsStreamRequestType(reqType schemas.RequestType) bool {
	return reqType == schemas.TextCompletionStreamRequest || reqType == schemas.ChatCompletionStreamRequest || reqType == schemas.ResponsesStreamRequest || reqType == schemas.SpeechStreamRequest || reqType == schemas.TranscriptionStreamRequest || reqType == schemas.ImageGenerationStreamRequest
}

func GetTracerFromContext(ctx *schemas.BifrostContext) (schemas.Tracer, string, error) {
	tracer, ok := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	if !ok || tracer == nil {
		return nil, "", fmt.Errorf("tracer not found in context")
	}
	traceID, ok := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if !ok || traceID == "" {
		return nil, "", fmt.Errorf("traceID not found in context")
	}
	return tracer, traceID, nil
}

// isBatchRequestType returns true if the given request type is a batch API operation.
func isBatchRequestType(reqType schemas.RequestType) bool {
	return reqType == schemas.BatchCreateRequest || reqType == schemas.BatchListRequest || reqType == schemas.BatchRetrieveRequest || reqType == schemas.BatchCancelRequest || reqType == schemas.BatchResultsRequest
}

// isFileRequestType returns true if the given request type is a file API operation.
func isFileRequestType(reqType schemas.RequestType) bool {
	return reqType == schemas.FileUploadRequest || reqType == schemas.FileListRequest || reqType == schemas.FileRetrieveRequest || reqType == schemas.FileDeleteRequest || reqType == schemas.FileContentRequest
}

// isContainerRequestType returns true if the given request type is a container API operation.
func isContainerRequestType(reqType schemas.RequestType) bool {
	return reqType == schemas.ContainerCreateRequest || reqType == schemas.ContainerListRequest ||
		reqType == schemas.ContainerRetrieveRequest || reqType == schemas.ContainerDeleteRequest ||
		reqType == schemas.ContainerFileCreateRequest || reqType == schemas.ContainerFileListRequest ||
		reqType == schemas.ContainerFileRetrieveRequest || reqType == schemas.ContainerFileContentRequest ||
		reqType == schemas.ContainerFileDeleteRequest
}

// IsFinalChunk returns true if the given context is a final chunk.
func IsFinalChunk(ctx *schemas.BifrostContext) bool {
	if ctx == nil {
		return false
	}

	isStreamEndIndicator := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator)
	if isStreamEndIndicator == nil {
		return false
	}

	if f, ok := isStreamEndIndicator.(bool); ok {
		return f
	}

	return false
}

// GetResponseFields extracts the request type, provider, and model from the result or error
func GetResponseFields(result *schemas.BifrostResponse, err *schemas.BifrostError) (requestType schemas.RequestType, provider schemas.ModelProvider, model string) {
	if result != nil {
		extraFields := result.GetExtraFields()
		return extraFields.RequestType, extraFields.Provider, extraFields.ModelRequested
	}
	return err.ExtraFields.RequestType, err.ExtraFields.Provider, err.ExtraFields.ModelRequested
}

// MarshalUnsafe marshals the given value to a JSON string without escaping HTML characters.
// Returns empty string if marshaling fails.
func MarshalUnsafe(v any) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(v)
	if err != nil {
		return ""
	}
	// Encode adds a trailing newline, trim it
	return strings.TrimSpace(buf.String())
}

func GetErrorMessage(err *schemas.BifrostError) string {
	if err == nil {
		return ""
	}
	if err.StatusCode != nil {
		switch *err.StatusCode {
		case 401:
			return "unauthorized"
		case 403:
			return "forbidden"
		case 404:
			return "endpoint not found"
		case 405:
			return "method not allowed"
		case 429:
			return "rate limit exceeded"
		case 500:
			return "internal server error"
		case 502:
			return "bad gateway"
		case 503:
			return "service unavailable"
		case 504:
			return "gateway timeout"
		default:
			if err.Error != nil && err.Error.Message != "" {
				return err.Error.Message
			}
			return fmt.Sprintf("HTTP %d error", *err.StatusCode)
		}
	} else if err.Error != nil && err.Error.Message != "" {
		return err.Error.Message
	} else if err.Type != nil {
		return *err.Type
	} else {
		return "unknown error"
	}
}

// GetStringFromContext safely extracts a string value from context
func GetStringFromContext(ctx context.Context, key any) string {
	if value := ctx.Value(key); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// GetIntFromContext safely extracts an int value from context
func GetIntFromContext(ctx context.Context, key any) int {
	if value := ctx.Value(key); value != nil {
		if intValue, ok := value.(int); ok {
			return intValue
		}
	}
	return 0
}

// RedactSensitiveString redacts sensitive information in a string
func RedactSensitiveString(s string) string {
	if s == "" {
		return ""
	}
	// Show first 4 and last 4 characters for identification, rest is [REDACTED]
	if len(s) <= 8 {
		return "[REDACTED]"
	}
	return s[:4] + "[REDACTED]" + s[len(s)-4:]
}

// ValidateExternalURL validates a URL for security concerns (SSRF protection)
func ValidateExternalURL(urlStr string) error {
	if urlStr == "" {
		return fmt.Errorf("URL cannot be empty")
	}
	// Parse the URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}
	// Only allow HTTPS scheme (or HTTP for localhost in development)
	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		return fmt.Errorf("only https and http schemes are allowed, got: %s", parsedURL.Scheme)
	}
	// Extract hostname
	hostname := parsedURL.Hostname()
	if hostname == "" {
		return fmt.Errorf("URL must have a hostname")
	}
	// Block localhost and loopback addresses
	if isLocalhost(hostname) {
		return fmt.Errorf("localhost and loopback addresses are not allowed")
	}
	// Resolve hostname to IP addresses
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Errorf("failed to resolve hostname: %w", err)
	}
	// Check if any resolved IP is private
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("private IP addresses are not allowed")
		}
	}
	return nil
}

// isLocalhost checks if a hostname is localhost or a loopback address
func isLocalhost(hostname string) bool {
	return hostname == "localhost" ||
		hostname == "127.0.0.1" ||
		hostname == "::1" ||
		hostname == "0.0.0.0" ||
		hostname == "::"
}

// isPrivateIP checks if an IP address is in a private range
func isPrivateIP(ip net.IP) bool {
	// Private IPv4 ranges
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16", // Link-local
		"127.0.0.0/8",    // Loopback
	}
	for _, cidr := range privateRanges {
		_, subnet, _ := net.ParseCIDR(cidr)
		if subnet.Contains(ip) {
			return true
		}
	}
	// Check for private IPv6
	if ip.To4() == nil {
		// Check for IPv6 loopback and link-local
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return true
		}
		// Check for IPv6 unique local addresses (fc00::/7)
		if len(ip) == 16 && (ip[0]&0xfe) == 0xfc {
			return true
		}
	}
	return false
}

// sanitizeSpanName sanitizes a span name to remove capital letters and spaces to make it a valid span name
func sanitizeSpanName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, " ", "-"))
}
