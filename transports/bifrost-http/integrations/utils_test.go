package integrations

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// testLogger implements schemas.Logger for testing (all no-ops)
type testLogger struct{}

func (t *testLogger) Debug(msg string, args ...any) {}
func (t *testLogger) Info(msg string, args ...any)  {}
func (t *testLogger) Warn(msg string, args ...any)  {}
func (t *testLogger) Error(msg string, args ...any) {}
func (t *testLogger) Fatal(msg string, args ...any) {}
func (t *testLogger) SetLevel(level schemas.LogLevel) {}
func (t *testLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (t *testLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

var _ schemas.Logger = (*testLogger)(nil)

func ptr(i int) *int {
	return &i
}

func strPtr(s string) *string {
	return &s
}

func newTestGenericRouter() *GenericRouter {
	return NewGenericRouter(nil, &mockHandlerStore{}, nil, &testLogger{})
}

func newTestBifrostContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// TestSendStreamError_PropagatesProviderStatusCode verifies that sendStreamError
// sets the HTTP status code from the provider's BifrostError.StatusCode field.
// All three providers (OpenAI, Anthropic, Bedrock) return actual HTTP error codes
// for pre-stream errors, so Bifrost must propagate them faithfully.
func TestSendStreamError_PropagatesProviderStatusCode(t *testing.T) {
	tests := []struct {
		name               string
		statusCode         *int
		expectedStatusCode int
	}{
		{
			name:               "provider 400 - Bedrock ValidationException / OpenAI invalid_request_error",
			statusCode:         ptr(400),
			expectedStatusCode: 400,
		},
		{
			name:               "provider 429 - rate limiting (all providers)",
			statusCode:         ptr(429),
			expectedStatusCode: 429,
		},
		{
			name:               "provider 503 - Bedrock ServiceUnavailableException",
			statusCode:         ptr(503),
			expectedStatusCode: 503,
		},
		{
			name:               "provider 529 - Anthropic overloaded_error",
			statusCode:         ptr(529),
			expectedStatusCode: 529,
		},
		{
			name:               "nil StatusCode defaults to 500",
			statusCode:         nil,
			expectedStatusCode: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newTestGenericRouter()
			ctx := &fasthttp.RequestCtx{}
			bifrostCtx := newTestBifrostContext()

			bifrostErr := &schemas.BifrostError{
				StatusCode: tt.statusCode,
				Error: &schemas.ErrorField{
					Message: "test error",
				},
			}

			config := RouteConfig{
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			}

			router.sendStreamError(ctx, bifrostCtx, config, bifrostErr)

			assert.Equal(t, tt.expectedStatusCode, ctx.Response.StatusCode())
			assert.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))

			body := string(ctx.Response.Body())
			assert.True(t, sonic.Valid(ctx.Response.Body()), "response body should be valid JSON, got: %s", body)
			assert.False(t, strings.HasPrefix(body, "data: "), "response should not be SSE format")
		})
	}
}

// TestSendStreamError_OpenAIErrorFormat verifies the response body matches the
// OpenAI error format. OpenAI's ErrorConverter returns *schemas.BifrostError directly,
// which serializes to {"is_bifrost_error":false,"status_code":400,"error":{...}}.
func TestSendStreamError_OpenAIErrorFormat(t *testing.T) {
	router := newTestGenericRouter()
	ctx := &fasthttp.RequestCtx{}
	bifrostCtx := newTestBifrostContext()

	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     ptr(400),
		Error: &schemas.ErrorField{
			Type:    strPtr("invalid_request_error"),
			Message: "content is empty",
		},
	}

	config := RouteConfig{
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	}

	router.sendStreamError(ctx, bifrostCtx, config, bifrostErr)

	assert.Equal(t, 400, ctx.Response.StatusCode())

	// Unmarshal and verify the structure
	var result map[string]interface{}
	err := sonic.Unmarshal(ctx.Response.Body(), &result)
	require.NoError(t, err)

	assert.Contains(t, result, "is_bifrost_error")
	assert.Contains(t, result, "status_code")
	assert.Contains(t, result, "error")
	assert.Equal(t, false, result["is_bifrost_error"])

	errorObj, ok := result["error"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "invalid_request_error", errorObj["type"])
	assert.Equal(t, "content is empty", errorObj["message"])
}

// TestSendStreamError_AnthropicErrorFormat verifies the response body matches the
// Anthropic error format: {"type":"error","error":{"type":"...","message":"..."}}.
// Critically, it also verifies that the StreamConfig.ErrorConverter (which returns
// raw SSE strings) is NOT used — sendStreamError must use the route-level ErrorConverter.
func TestSendStreamError_AnthropicErrorFormat(t *testing.T) {
	router := newTestGenericRouter()
	ctx := &fasthttp.RequestCtx{}
	bifrostCtx := newTestBifrostContext()

	bifrostErr := &schemas.BifrostError{
		StatusCode: ptr(429),
		Error: &schemas.ErrorField{
			Type:    strPtr("rate_limit_error"),
			Message: "rate limited",
		},
	}

	config := RouteConfig{
		// Route-level: returns JSON-marshallable *AnthropicMessageError
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		// Stream-level: returns raw SSE string — should NOT be used by sendStreamError
		StreamConfig: &StreamConfig{
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicResponsesStreamError(err)
			},
		},
	}

	router.sendStreamError(ctx, bifrostCtx, config, bifrostErr)

	assert.Equal(t, 429, ctx.Response.StatusCode())
	assert.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))

	body := string(ctx.Response.Body())

	// Must NOT contain SSE markers — that would mean StreamConfig.ErrorConverter was used
	assert.NotContains(t, body, "event: error", "response should not contain SSE event markers")

	// Unmarshal and verify Anthropic error structure
	var result anthropic.AnthropicMessageError
	err := sonic.Unmarshal(ctx.Response.Body(), &result)
	require.NoError(t, err)

	assert.Equal(t, "error", result.Type)
	assert.Equal(t, "rate_limit_error", result.Error.Type)
	assert.Equal(t, "rate limited", result.Error.Message)
}

// TestSendStreamError_BedrockErrorFormat verifies the response body matches the
// Bedrock error format: {"__type":"ValidationException","message":"..."}.
func TestSendStreamError_BedrockErrorFormat(t *testing.T) {
	router := newTestGenericRouter()
	ctx := &fasthttp.RequestCtx{}
	bifrostCtx := newTestBifrostContext()

	bifrostErr := &schemas.BifrostError{
		StatusCode: ptr(400),
		Error: &schemas.ErrorField{
			Code:    strPtr("ValidationException"),
			Message: "validation error",
		},
	}

	config := RouteConfig{
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
	}

	router.sendStreamError(ctx, bifrostCtx, config, bifrostErr)

	assert.Equal(t, 400, ctx.Response.StatusCode())

	// Unmarshal and verify Bedrock error structure
	var result bedrock.BedrockError
	err := sonic.Unmarshal(ctx.Response.Body(), &result)
	require.NoError(t, err)

	assert.Equal(t, "ValidationException", result.Type)
	assert.Equal(t, "validation error", result.Message)
}

// TestSendStreamError_ForwardsProviderHeaders verifies that provider response headers
// stored in the BifrostContext are forwarded to the HTTP response. This ensures
// clients receive provider-specific headers (e.g., x-amzn-requestid for Bedrock,
// x-request-id for Anthropic) even in error scenarios.
func TestSendStreamError_ForwardsProviderHeaders(t *testing.T) {
	router := newTestGenericRouter()
	ctx := &fasthttp.RequestCtx{}
	bifrostCtx := newTestBifrostContext()

	// Set provider response headers on the context
	bifrostCtx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{
		"x-amzn-requestid": "req-123",
		"x-amzn-errortype": "ValidationException",
	})

	bifrostErr := &schemas.BifrostError{
		StatusCode: ptr(400),
		Error: &schemas.ErrorField{
			Message: "validation error",
		},
	}

	config := RouteConfig{
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	}

	router.sendStreamError(ctx, bifrostCtx, config, bifrostErr)

	assert.Equal(t, 400, ctx.Response.StatusCode())
	assert.Equal(t, "req-123", string(ctx.Response.Header.Peek("x-amzn-requestid")))
	assert.Equal(t, "ValidationException", string(ctx.Response.Header.Peek("x-amzn-errortype")))
}
