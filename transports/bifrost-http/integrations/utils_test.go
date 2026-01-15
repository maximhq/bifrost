package integrations

import (
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestNewBifrostError_WithError tests newBifrostError with an error
func TestNewBifrostError_WithError(t *testing.T) {
	err := errors.New("test error")
	bifrostErr := newBifrostError(err, "custom message")

	if bifrostErr == nil {
		t.Fatal("Expected non-nil BifrostError")
	}
	if bifrostErr.IsBifrostError {
		t.Error("Expected IsBifrostError to be false")
	}
	if bifrostErr.Error == nil {
		t.Fatal("Expected non-nil Error field")
	}
	if bifrostErr.Error.Message != "custom message" {
		t.Errorf("Expected message 'custom message', got '%s'", bifrostErr.Error.Message)
	}
	if bifrostErr.Error.Error != err {
		t.Error("Expected original error to be preserved")
	}
}

// TestNewBifrostError_NilError tests newBifrostError with nil error
func TestNewBifrostError_NilError(t *testing.T) {
	bifrostErr := newBifrostError(nil, "message only")

	if bifrostErr == nil {
		t.Fatal("Expected non-nil BifrostError")
	}
	if bifrostErr.IsBifrostError {
		t.Error("Expected IsBifrostError to be false")
	}
	if bifrostErr.Error == nil {
		t.Fatal("Expected non-nil Error field")
	}
	if bifrostErr.Error.Message != "message only" {
		t.Errorf("Expected message 'message only', got '%s'", bifrostErr.Error.Message)
	}
	if bifrostErr.Error.Error != nil {
		t.Error("Expected nil Error when input error is nil")
	}
}

// TestSafeGetRequestType_NilChunk tests safeGetRequestType with nil chunk
func TestSafeGetRequestType_NilChunk(t *testing.T) {
	result := safeGetRequestType(nil)
	if result != "unknown" {
		t.Errorf("Expected 'unknown' for nil chunk, got '%s'", result)
	}
}

// TestSafeGetRequestType_EmptyChunk tests safeGetRequestType with empty chunk
func TestSafeGetRequestType_EmptyChunk(t *testing.T) {
	chunk := &schemas.BifrostStream{}
	result := safeGetRequestType(chunk)
	if result != "unknown" {
		t.Errorf("Expected 'unknown' for empty chunk, got '%s'", result)
	}
}

// TestSafeGetRequestType_WithChatResponse tests safeGetRequestType with chat response
func TestSafeGetRequestType_WithChatResponse(t *testing.T) {
	chunk := &schemas.BifrostStream{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
			},
		},
	}
	result := safeGetRequestType(chunk)
	if result != string(schemas.ChatCompletionRequest) {
		t.Errorf("Expected '%s', got '%s'", schemas.ChatCompletionRequest, result)
	}
}

// TestSafeGetRequestType_WithTextResponse tests safeGetRequestType with text completion response
func TestSafeGetRequestType_WithTextResponse(t *testing.T) {
	chunk := &schemas.BifrostStream{
		BifrostTextCompletionResponse: &schemas.BifrostTextCompletionResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.TextCompletionRequest,
			},
		},
	}
	result := safeGetRequestType(chunk)
	if result != string(schemas.TextCompletionRequest) {
		t.Errorf("Expected '%s', got '%s'", schemas.TextCompletionRequest, result)
	}
}

// TestSafeGetRequestType_WithError tests safeGetRequestType with error response
func TestSafeGetRequestType_WithError(t *testing.T) {
	chunk := &schemas.BifrostStream{
		BifrostError: &schemas.BifrostError{
			ExtraFields: schemas.BifrostErrorExtraFields{
				RequestType: schemas.ChatCompletionRequest,
			},
		},
	}
	result := safeGetRequestType(chunk)
	if result != string(schemas.ChatCompletionRequest) {
		t.Errorf("Expected '%s', got '%s'", schemas.ChatCompletionRequest, result)
	}
}

// TestExtractHeadersFromRequest tests extractHeadersFromRequest
func TestExtractHeadersFromRequest(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("Authorization", "Bearer token")
	ctx.Request.Header.Set("X-Custom-Header", "value")

	headers := extractHeadersFromRequest(ctx)

	if headers["Content-Type"] == nil || headers["Content-Type"][0] != "application/json" {
		t.Error("Expected Content-Type header to be extracted")
	}
	if headers["Authorization"] == nil || headers["Authorization"][0] != "Bearer token" {
		t.Error("Expected Authorization header to be extracted")
	}
	if headers["X-Custom-Header"] == nil || headers["X-Custom-Header"][0] != "value" {
		t.Error("Expected X-Custom-Header header to be extracted")
	}
}

// TestExtractHeadersFromRequest_EmptyHeaders tests extractHeadersFromRequest with no headers
func TestExtractHeadersFromRequest_EmptyHeaders(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	headers := extractHeadersFromRequest(ctx)

	// fasthttp always has some headers like Host, so just check it returns a map
	if headers == nil {
		t.Error("Expected non-nil headers map")
	}
}

// TestExtractExactPath_WithIntegrationPrefix tests extractExactPath with integration prefix
func TestExtractExactPath_WithIntegrationPrefix(t *testing.T) {
	testCases := []struct {
		path     string
		expected string
	}{
		{"/openai/v1/chat/completions", "v1/chat/completions"},
		{"/anthropic/v1/messages", "v1/messages"},
		{"/genai/v1/models", "v1/models"},
		{"/litellm/v1/chat/completions", "v1/chat/completions"},
		{"/langchain/v1/run", "v1/run"},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(tc.path)

			result := extractExactPath(ctx)

			if result != tc.expected {
				t.Errorf("Expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

// TestExtractExactPath_WithQueryString tests extractExactPath with query string
func TestExtractExactPath_WithQueryString(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/openai/v1/chat/completions?model=gpt-4&stream=true")

	result := extractExactPath(ctx)

	if result != "v1/chat/completions?model=gpt-4&stream=true" {
		t.Errorf("Expected 'v1/chat/completions?model=gpt-4&stream=true', got '%s'", result)
	}
}

// TestExtractExactPath_NoIntegrationPrefix tests extractExactPath without integration prefix
func TestExtractExactPath_NoIntegrationPrefix(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/custom/path")

	result := extractExactPath(ctx)

	// Should return path as-is (without leading slash from internal processing)
	if result != "/custom/path" {
		t.Errorf("Expected '/custom/path', got '%s'", result)
	}
}

// TestExtractExactPath_RootPath tests extractExactPath with root path
func TestExtractExactPath_RootPath(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/")

	result := extractExactPath(ctx)

	if result != "/" {
		t.Errorf("Expected '/', got '%s'", result)
	}
}

// TestIsAnthropicAPIKeyAuth_WithAPIKey tests isAnthropicAPIKeyAuth with x-api-key header
func TestIsAnthropicAPIKeyAuth_WithAPIKey(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-api-key", "sk-ant-api-key")

	result := isAnthropicAPIKeyAuth(ctx)

	if !result {
		t.Error("Expected true when x-api-key header is present")
	}
}

// TestIsAnthropicAPIKeyAuth_WithOAuthToken tests isAnthropicAPIKeyAuth with OAuth token
func TestIsAnthropicAPIKeyAuth_WithOAuthToken(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer sk-ant-oat12345")

	result := isAnthropicAPIKeyAuth(ctx)

	if result {
		t.Error("Expected false when OAuth token (sk-ant-oat) is present")
	}
}

// TestIsAnthropicAPIKeyAuth_WithRegularBearerToken tests isAnthropicAPIKeyAuth with regular bearer
func TestIsAnthropicAPIKeyAuth_WithRegularBearerToken(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer sk-regular-key")

	result := isAnthropicAPIKeyAuth(ctx)

	if !result {
		t.Error("Expected true for regular bearer token (not OAuth)")
	}
}

// TestIsAnthropicAPIKeyAuth_NoHeaders tests isAnthropicAPIKeyAuth with no auth headers
func TestIsAnthropicAPIKeyAuth_NoHeaders(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	result := isAnthropicAPIKeyAuth(ctx)

	if !result {
		t.Error("Expected true (API mode) when no auth headers are present")
	}
}

// TestIsAnthropicAPIKeyAuth_CaseInsensitive tests isAnthropicAPIKeyAuth case handling
func TestIsAnthropicAPIKeyAuth_CaseInsensitive(t *testing.T) {
	testCases := []struct {
		authHeader string
		expected   bool
	}{
		{"Bearer SK-ANT-OAT123", false},       // Uppercase OAuth prefix
		{"BEARER sk-ant-oat123", false},       // Uppercase Bearer
		{"bearer sk-ant-oat123", false},       // Lowercase bearer
		{"Bearer sk-ant-api-key", true},       // Not OAuth (sk-ant-api, not sk-ant-oat)
	}

	for _, tc := range testCases {
		t.Run(tc.authHeader, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.Set("Authorization", tc.authHeader)

			result := isAnthropicAPIKeyAuth(ctx)

			if result != tc.expected {
				t.Errorf("For '%s', expected %v, got %v", tc.authHeader, tc.expected, result)
			}
		})
	}
}

// TestAvailableIntegrations documents available integrations
func TestAvailableIntegrations(t *testing.T) {
	expected := []string{"openai", "anthropic", "genai", "litellm", "langchain"}

	if len(availableIntegrations) != len(expected) {
		t.Errorf("Expected %d integrations, got %d", len(expected), len(availableIntegrations))
	}

	for i, integration := range expected {
		if i >= len(availableIntegrations) || availableIntegrations[i] != integration {
			t.Errorf("Expected integration '%s' at index %d", integration, i)
		}
	}

	t.Log("Available integrations: openai, anthropic, genai, litellm, langchain")
}

// TestBifrostContextKeyProvider documents the provider context key
func TestBifrostContextKeyProvider(t *testing.T) {
	expected := schemas.BifrostContextKey("provider")

	if bifrostContextKeyProvider != expected {
		t.Errorf("Expected provider context key '%s', got '%s'", expected, bifrostContextKeyProvider)
	}

	t.Log("Provider context key is 'provider'")
}

// TestExtractFallbacksFromRequest_ReflectionBased documents fallback extraction
func TestExtractFallbacksFromRequest_ReflectionBased(t *testing.T) {
	// The extractFallbacksFromRequest function uses reflection to find a 'fallbacks' field
	// It handles:
	// - []string: returns as-is
	// - string: returns as single-element slice
	// - nil/missing: returns nil

	// Test struct with fallbacks
	type RequestWithFallbacks struct {
		fallbacks []string
	}

	// The function is on GenericRouter, so we document behavior here
	t.Log("extractFallbacksFromRequest uses reflection to find 'fallbacks' field in request structs")
}

// TestSafeGetRequestType_Priority documents request type extraction priority
func TestSafeGetRequestType_Priority(t *testing.T) {
	// Priority order:
	// 1. BifrostTextCompletionResponse.ExtraFields.RequestType
	// 2. BifrostChatResponse.ExtraFields.RequestType
	// 3. BifrostResponsesStreamResponse.ExtraFields.RequestType
	// 4. BifrostSpeechStreamResponse.ExtraFields.RequestType
	// 5. BifrostTranscriptionStreamResponse.ExtraFields.RequestType
	// 6. BifrostError.ExtraFields.RequestType (fallback)
	// 7. "unknown" (final fallback)

	t.Log("Request type is extracted from response ExtraFields, with BifrostError as fallback")
}
