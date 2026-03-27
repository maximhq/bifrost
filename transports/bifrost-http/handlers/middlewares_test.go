package handlers

import (
	"bytes"
	"compress/zlib"
	"compress/gzip"
	cryptoRand "crypto/rand"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// mockLogger is a mock implementation of schemas.Logger for testing
type mockLogger struct{}

func (m *mockLogger) Debug(format string, args ...any)                  {}
func (m *mockLogger) Info(format string, args ...any)                   {}
func (m *mockLogger) Warn(format string, args ...any)                   {}
func (m *mockLogger) Error(format string, args ...any)                  {}
func (m *mockLogger) Fatal(format string, args ...any)                  {}
func (m *mockLogger) SetLevel(level schemas.LogLevel)                   {}
func (m *mockLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (m *mockLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// TestCorsMiddleware_LocalhostOrigins tests that localhost origins are always allowed
func TestCorsMiddleware_LocalhostOrigins(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{},
		},
	}

	SetLogger(&mockLogger{})

	localhostOrigins := []string{
		"http://localhost:3000",
		"https://localhost:3000",
		"http://127.0.0.1:8080",
		"http://0.0.0.0:5000",
		"https://127.0.0.1:3000",
	}

	for _, origin := range localhostOrigins {
		t.Run(origin, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.Set("Origin", origin)

			nextCalled := false
			next := func(ctx *fasthttp.RequestCtx) {
				nextCalled = true
			}

			middleware := CorsMiddleware(config)
			handler := middleware(next)
			handler(ctx)

			// Check CORS headers are set
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) != origin {
				t.Errorf("Expected Access-Control-Allow-Origin to be %s, got %s", origin, string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")))
			}
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Methods")) != "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD" {
				t.Errorf("Access-Control-Allow-Methods header not set correctly")
			}
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Headers")) != "Content-Type, Authorization, X-Requested-With, X-Stainless-Timeout, X-Api-Key" {
				t.Errorf("Access-Control-Allow-Headers header not set correctly")
			}
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Credentials")) != "true" {
				t.Errorf("Access-Control-Allow-Credentials header not set correctly")
			}
			if string(ctx.Response.Header.Peek("Access-Control-Max-Age")) != "86400" {
				t.Errorf("Access-Control-Max-Age header not set correctly")
			}

			// Check next handler was called
			if !nextCalled {
				t.Error("Next handler was not called")
			}
		})
	}
}

// TestCorsMiddleware_ConfiguredOrigins tests that configured allowed origins work
func TestCorsMiddleware_ConfiguredOrigins(t *testing.T) {
	allowedOrigin := "https://example.com"
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{allowedOrigin},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", allowedOrigin)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check CORS headers are set
	if string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) != allowedOrigin {
		t.Errorf("Expected Access-Control-Allow-Origin to be %s, got %s", allowedOrigin, string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")))
	}

	// Check next handler was called
	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_NonAllowedOrigins tests that non-allowed origins don't get CORS headers
func TestCorsMiddleware_NonAllowedOrigins(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{"https://allowed.com"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://malicious.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check CORS headers are NOT set
	if len(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) != 0 {
		t.Error("Access-Control-Allow-Origin header should not be set for non-allowed origin")
	}

	// Check next handler was still called for non-OPTIONS requests
	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_PreflightAllowedOrigin tests OPTIONS preflight requests for allowed origins
func TestCorsMiddleware_PreflightAllowedOrigin(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{"https://example.com"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("OPTIONS")
	ctx.Request.Header.Set("Origin", "https://example.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check status code is 200 OK
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status code %d for allowed origin preflight, got %d", fasthttp.StatusOK, ctx.Response.StatusCode())
	}

	// Check CORS headers are set
	if string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) != "https://example.com" {
		t.Error("Access-Control-Allow-Origin header not set correctly for allowed origin preflight")
	}

	// Check next handler was NOT called for OPTIONS requests
	if nextCalled {
		t.Error("Next handler should not be called for OPTIONS preflight requests")
	}
}

// TestCorsMiddleware_PreflightNonAllowedOrigin tests OPTIONS preflight requests for non-allowed origins
func TestCorsMiddleware_PreflightNonAllowedOrigin(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{"https://allowed.com"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("OPTIONS")
	ctx.Request.Header.Set("Origin", "https://malicious.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check status code is 403 Forbidden
	if ctx.Response.StatusCode() != fasthttp.StatusForbidden {
		t.Errorf("Expected status code %d for non-allowed origin preflight, got %d", fasthttp.StatusForbidden, ctx.Response.StatusCode())
	}

	// Check CORS headers are NOT set
	if len(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) != 0 {
		t.Error("Access-Control-Allow-Origin header should not be set for non-allowed origin preflight")
	}

	// Check next handler was NOT called for OPTIONS requests
	if nextCalled {
		t.Error("Next handler should not be called for OPTIONS preflight requests")
	}
}

// TestCorsMiddleware_PreflightLocalhost tests OPTIONS preflight requests for localhost
func TestCorsMiddleware_PreflightLocalhost(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("OPTIONS")
	ctx.Request.Header.Set("Origin", "http://localhost:3000")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check status code is 200 OK
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status code %d for localhost preflight, got %d", fasthttp.StatusOK, ctx.Response.StatusCode())
	}

	// Check CORS headers are set
	if string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) != "http://localhost:3000" {
		t.Error("Access-Control-Allow-Origin header not set correctly for localhost preflight")
	}

	// Check next handler was NOT called for OPTIONS requests
	if nextCalled {
		t.Error("Next handler should not be called for OPTIONS preflight requests")
	}
}

// TestCorsMiddleware_NoOriginHeader tests behavior when no Origin header is present
func TestCorsMiddleware_NoOriginHeader(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	// No Origin header set

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check CORS headers are NOT set when no origin is present
	if len(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) != 0 {
		t.Error("Access-Control-Allow-Origin header should not be set when no Origin header is present")
	}

	// Check next handler was called
	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// Testlib.ChainMiddlewares_NoMiddlewares tests chaining with no middlewares
func TestChainMiddlewares_NoMiddlewares(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	handlerCalled := false

	handler := func(ctx *fasthttp.RequestCtx) {
		handlerCalled = true
	}

	chained := lib.ChainMiddlewares(handler)
	chained(ctx)

	if !handlerCalled {
		t.Error("Handler was not called when no middlewares are present")
	}
}

// Testlib.ChainMiddlewares_SingleMiddleware tests chaining with a single middleware
func TestChainMiddlewares_SingleMiddleware(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	middlewareCalled := false
	handlerCalled := false

	middleware := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			middlewareCalled = true
			next(ctx)
		}
	})

	handler := func(ctx *fasthttp.RequestCtx) {
		handlerCalled = true
	}

	chained := lib.ChainMiddlewares(handler, middleware)
	chained(ctx)

	if !middlewareCalled {
		t.Error("Middleware was not called")
	}
	if !handlerCalled {
		t.Error("Handler was not called")
	}
}

// Testlib.ChainMiddlewares_MultipleMiddlewares tests chaining with multiple middlewares
func TestChainMiddlewares_MultipleMiddlewares(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	executionOrder := []int{}

	middleware1 := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 1)
			next(ctx)
		}
	})

	middleware2 := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 2)
			next(ctx)
		}
	})

	middleware3 := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 3)
			next(ctx)
		}
	})

	handler := func(ctx *fasthttp.RequestCtx) {
		executionOrder = append(executionOrder, 4)
	}

	chained := lib.ChainMiddlewares(handler, middleware1, middleware2, middleware3)
	chained(ctx)

	// Check execution order: middlewares should execute in order, then handler
	expectedOrder := []int{1, 2, 3, 4}
	if len(executionOrder) != len(expectedOrder) {
		t.Errorf("Expected %d function calls, got %d", len(expectedOrder), len(executionOrder))
	}

	for i, expected := range expectedOrder {
		if i >= len(executionOrder) || executionOrder[i] != expected {
			t.Errorf("Expected execution order %v, got %v", expectedOrder, executionOrder)
			break
		}
	}
}

// Testlib.ChainMiddlewares_MiddlewareCanModifyContext tests that middlewares can modify the context
func TestChainMiddlewares_MiddlewareCanModifyContext(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	middleware := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			ctx.SetUserValue("test-key", "test-value")
			next(ctx)
		}
	})

	handler := func(ctx *fasthttp.RequestCtx) {
		value := ctx.UserValue("test-key")
		if value == nil {
			t.Error("Handler did not receive modified context from middleware")
		} else if value.(string) != "test-value" {
			t.Errorf("Expected user value to be 'test-value', got '%s'", value.(string))
		}
	}

	chained := lib.ChainMiddlewares(handler, middleware)
	chained(ctx)
}

// Testlib.ChainMiddlewares_ShortCircuit tests that when a middleware writes a response
// and does not call next, subsequent middlewares and handler do not execute.
func TestChainMiddlewares_ShortCircuit(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	executionOrder := []int{}

	// First middleware - writes response and short-circuits by not calling next
	middleware1 := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 1)
			ctx.SetStatusCode(fasthttp.StatusUnauthorized)
			ctx.SetBodyString("Unauthorized")
			// Not calling next(ctx) to short-circuit
		}
	})

	// Second middleware - should NOT execute when middleware1 short-circuits
	middleware2 := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 2)
			next(ctx)
		}
	})

	// Third middleware - should NOT execute when middleware1 short-circuits
	middleware3 := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 3)
			next(ctx)
		}
	})

	// Handler - should NOT execute when middleware1 short-circuits
	handler := func(ctx *fasthttp.RequestCtx) {
		executionOrder = append(executionOrder, 4)
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBodyString("Success")
	}

	chained := lib.ChainMiddlewares(handler, middleware1, middleware2, middleware3)
	chained(ctx)

	// Verify only middleware1 executed
	expectedOrder := []int{1}
	if len(executionOrder) != len(expectedOrder) {
		t.Errorf("Expected %d function calls, got %d", len(expectedOrder), len(executionOrder))
	}

	for i, expected := range expectedOrder {
		if i >= len(executionOrder) || executionOrder[i] != expected {
			t.Errorf("Expected execution order %v, got %v", expectedOrder, executionOrder)
			break
		}
	}

	// The middleware's response should be preserved (not overwritten)
	if ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
		t.Errorf("Expected status code %d, got %d", fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
	}
	if string(ctx.Response.Body()) != "Unauthorized" {
		t.Errorf("Expected body 'Unauthorized', got '%s'", string(ctx.Response.Body()))
	}
}

// Testlib.ChainMiddlewares_ShortCircuitMiddlePosition tests that middleware in the middle
// can short-circuit, preventing later middlewares and handler from executing.
func TestChainMiddlewares_ShortCircuitMiddlePosition(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	executionOrder := []int{}

	// First middleware - executes and calls next
	middleware1 := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 1)
			next(ctx)
		}
	})

	// Second middleware - writes response and short-circuits
	middleware2 := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 2)
			ctx.SetStatusCode(fasthttp.StatusUnauthorized)
			ctx.SetBodyString("Unauthorized")
			// Not calling next(ctx) to short-circuit
		}
	})

	// Third middleware - should NOT execute
	middleware3 := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 3)
			next(ctx)
		}
	})

	// Handler - should NOT execute
	handler := func(ctx *fasthttp.RequestCtx) {
		executionOrder = append(executionOrder, 4)
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBodyString("Success")
	}

	chained := lib.ChainMiddlewares(handler, middleware1, middleware2, middleware3)
	chained(ctx)

	// Verify only middleware1 and middleware2 executed
	expectedOrder := []int{1, 2}
	if len(executionOrder) != len(expectedOrder) {
		t.Errorf("Expected %d function calls, got %d", len(expectedOrder), len(executionOrder))
	}

	for i, expected := range expectedOrder {
		if i >= len(executionOrder) || executionOrder[i] != expected {
			t.Errorf("Expected execution order %v, got %v", expectedOrder, executionOrder)
			break
		}
	}

	// The middleware2's response should be preserved
	if ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
		t.Errorf("Expected status code %d, got %d", fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
	}
	if string(ctx.Response.Body()) != "Unauthorized" {
		t.Errorf("Expected body 'Unauthorized', got '%s'", string(ctx.Response.Body()))
	}
}

// TestAuthMiddleware_NilAuthConfig tests that auth middleware allows requests when auth config is nil
func TestAuthMiddleware_NilAuthConfig(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	// authConfig is nil by default (simulates app start with no auth config)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/some-endpoint")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := am.APIMiddleware()
	handler := middleware(next)
	handler(ctx)

	// When auth config is nil, requests should be allowed through
	if !nextCalled {
		t.Error("Next handler should be called when auth config is nil")
	}
}

// TestAuthMiddleware_DisabledAuthConfig tests that auth middleware allows requests when auth is disabled
func TestAuthMiddleware_DisabledAuthConfig(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewEnvVar("admin"),
		AdminPassword: schemas.NewEnvVar("password"),
		IsEnabled:     false,
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/some-endpoint")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := am.APIMiddleware()
	handler := middleware(next)
	handler(ctx)

	// When auth is disabled, requests should be allowed through
	if !nextCalled {
		t.Error("Next handler should be called when auth is disabled")
	}
}

// TestAuthMiddleware_EnabledAuthConfig_NoAuth tests that auth middleware blocks unauthenticated requests
func TestAuthMiddleware_EnabledAuthConfig_NoAuth(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewEnvVar("admin"),
		AdminPassword: schemas.NewEnvVar("hashedpassword"),
		IsEnabled:     true,
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/some-endpoint")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := am.APIMiddleware()
	handler := middleware(next)
	handler(ctx)

	// When auth is enabled and no auth header is provided, request should be blocked
	if nextCalled {
		t.Error("Next handler should NOT be called when auth is enabled and no credentials provided")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
		t.Errorf("Expected status code %d, got %d", fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
	}
}

// TestAuthMiddleware_WhitelistedRoutes tests that whitelisted routes bypass auth
func TestAuthMiddleware_WhitelistedRoutes(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewEnvVar("admin"),
		AdminPassword: schemas.NewEnvVar("hashedpassword"),
		IsEnabled:     true,
	})

	whitelistedRoutes := []string{
		"/api/session/is-auth-enabled",
		"/api/session/login",
		"/api/oauth/callback",
		"/health",
	}

	for _, route := range whitelistedRoutes {
		t.Run(route, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(route)

			nextCalled := false
			next := func(ctx *fasthttp.RequestCtx) {
				nextCalled = true
			}

			middleware := am.APIMiddleware()
			handler := middleware(next)
			handler(ctx)

			if !nextCalled {
				t.Errorf("Next handler should be called for whitelisted route %s", route)
			}
		})
	}
}

// TestAuthMiddleware_UpdateAuthConfig_NilToEnabled tests updating auth config from nil to enabled
func TestAuthMiddleware_UpdateAuthConfig_NilToEnabled(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	// Initially auth config is nil

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/some-endpoint")

	// First request should pass (nil config)
	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := am.APIMiddleware()
	handler := middleware(next)
	handler(ctx)

	if !nextCalled {
		t.Error("First request should pass when auth config is nil")
	}

	// Now enable auth
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewEnvVar("admin"),
		AdminPassword: schemas.NewEnvVar("hashedpassword"),
		IsEnabled:     true,
	})

	// Second request should be blocked (auth enabled, no credentials)
	ctx2 := &fasthttp.RequestCtx{}
	ctx2.Request.SetRequestURI("/api/some-endpoint")

	nextCalled = false
	handler(ctx2)

	if nextCalled {
		t.Error("Second request should be blocked after auth is enabled")
	}
	if ctx2.Response.StatusCode() != fasthttp.StatusUnauthorized {
		t.Errorf("Expected status code %d, got %d", fasthttp.StatusUnauthorized, ctx2.Response.StatusCode())
	}
}

// TestAuthMiddleware_UpdateAuthConfig_EnabledToDisabled tests disabling auth after it was enabled
func TestAuthMiddleware_UpdateAuthConfig_EnabledToDisabled(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	// Start with auth enabled
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewEnvVar("admin"),
		AdminPassword: schemas.NewEnvVar("hashedpassword"),
		IsEnabled:     true,
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/some-endpoint")

	// First request should be blocked (auth enabled, no credentials)
	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := am.APIMiddleware()
	handler := middleware(next)
	handler(ctx)

	if nextCalled {
		t.Error("First request should be blocked when auth is enabled")
	}

	// Now disable auth
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewEnvVar("admin"),
		AdminPassword: schemas.NewEnvVar("hashedpassword"),
		IsEnabled:     false,
	})

	// Second request should pass (auth disabled)
	ctx2 := &fasthttp.RequestCtx{}
	ctx2.Request.SetRequestURI("/api/some-endpoint")

	nextCalled = false
	handler(ctx2)

	if !nextCalled {
		t.Error("Second request should pass after auth is disabled")
	}
}

// TestFasthttpToHTTPRequest tests the conversion from fasthttp context to HTTPRequest
func TestFasthttpToHTTPRequest(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	// Set up test data
	ctx.Request.Header.SetMethod("POST")
	// Query params include: integers, floats, booleans, timestamps, and strings with special chars
	ctx.Request.SetRequestURI("/api/v1/test?limit=100&offset=50&min_cost=12.50&max_latency=1500.75&missing_cost_only=true&start_time=2023-01-15T10:30:00Z&content_search=test+query&special=%2B%26%3D%3F")
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("Authorization", "Bearer token123")
	ctx.Request.Header.Set("X-Request-Id", "12345")
	ctx.Request.Header.Set("X-Custom-Header", "value-with-dashes")
	ctx.Request.SetBodyString(`{"key": "value", "number": 42, "nested": {"bool": true}}`)

	// Acquire HTTPRequest from pool
	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)

	// Call the function
	fasthttpToHTTPRequest(ctx, req)

	// Verify Method
	if req.Method != "POST" {
		t.Errorf("Expected Method to be 'POST', got '%s'", req.Method)
	}

	// Verify Path (without query params)
	if req.Path != "/api/v1/test" {
		t.Errorf("Expected Path to be '/api/v1/test', got '%s'", req.Path)
	}

	// Verify Headers
	expectedHeaders := map[string]string{
		"Content-Type":    "application/json",
		"Authorization":   "Bearer token123",
		"X-Request-Id":    "12345",
		"X-Custom-Header": "value-with-dashes",
	}
	for key, expectedValue := range expectedHeaders {
		if actualValue, exists := req.Headers[key]; !exists {
			t.Errorf("Expected header '%s' to exist", key)
		} else if actualValue != expectedValue {
			t.Errorf("Expected header '%s' to be '%s', got '%s'", key, expectedValue, actualValue)
		}
	}

	// Verify Query params
	expectedQuery := map[string]string{
		"limit":             "100",                  // integer
		"offset":            "50",                   // integer
		"min_cost":          "12.50",                // float
		"max_latency":       "1500.75",              // float
		"missing_cost_only": "true",                 // boolean
		"start_time":        "2023-01-15T10:30:00Z", // timestamp
		"content_search":    "test query",           // string with space (decoded)
		"special":           "+&=?",                 // special characters (decoded)
	}
	for key, expectedValue := range expectedQuery {
		if actualValue, exists := req.Query[key]; !exists {
			t.Errorf("Expected query param '%s' to exist", key)
		} else if actualValue != expectedValue {
			t.Errorf("Expected query param '%s' to be '%s', got '%s'", key, expectedValue, actualValue)
		}
	}

	// Verify Body (JSON with various types)
	expectedBody := `{"key": "value", "number": 42, "nested": {"bool": true}}`
	if string(req.Body) != expectedBody {
		t.Errorf("Expected Body to be '%s', got '%s'", expectedBody, string(req.Body))
	}

	// Verify body is a copy, not a reference
	originalBody := ctx.Request.Body()
	if len(req.Body) > 0 && len(originalBody) > 0 {
		// Modify the HTTPRequest body
		req.Body[0] = 'X'
		// Original should remain unchanged
		if originalBody[0] == 'X' {
			t.Error("Body should be a copy, not a reference to the original")
		}
	}
}

// TestCorsMiddleware_DefaultHeaders tests that default CORS headers are set
func TestCorsMiddleware_DefaultHeaders(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{"https://example.com"},
			AllowedHeaders: []string{}, // No custom headers
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://example.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check default headers are set
	expectedHeaders := "Content-Type, Authorization, X-Requested-With, X-Stainless-Timeout, X-Api-Key"
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))
	if actualHeaders != expectedHeaders {
		t.Errorf("Expected Access-Control-Allow-Headers to be %s, got %s", expectedHeaders, actualHeaders)
	}

	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_WildcardHeaders tests that wildcard allowed headers sets Access-Control-Allow-Headers to *
func TestCorsMiddleware_WildcardHeaders(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{"https://example.com"},
			AllowedHeaders: []string{"*"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://example.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check that wildcard is set
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))
	if actualHeaders != "*" {
		t.Errorf("Expected Access-Control-Allow-Headers to be *, got %s", actualHeaders)
	}

	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_CustomHeaders tests that custom allowed headers are appended to defaults
func TestCorsMiddleware_CustomHeaders(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{"https://example.com"},
			AllowedHeaders: []string{"X-Custom-Header", "X-Another-Header"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://example.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check that custom headers are included along with defaults
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))
	expectedHeaders := []string{
		"Content-Type",
		"Authorization",
		"X-Requested-With",
		"X-Stainless-Timeout",
		"X-Custom-Header",
		"X-Another-Header",
	}

	for _, header := range expectedHeaders {
		if !containsHeader(actualHeaders, header) {
			t.Errorf("Expected Access-Control-Allow-Headers to contain %s, got %s", header, actualHeaders)
		}
	}

	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_DuplicateHeaders tests that duplicate headers are not added twice
func TestCorsMiddleware_DuplicateHeaders(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{"https://example.com"},
			// Include a header that's already in defaults
			AllowedHeaders: []string{"Content-Type", "X-Custom-Header"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://example.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check headers - Content-Type should not be duplicated
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))

	// Count occurrences of "Content-Type"
	count := countHeaderOccurrences(actualHeaders, "Content-Type")
	if count != 1 {
		t.Errorf("Expected Content-Type to appear once, but appeared %d times in: %s", count, actualHeaders)
	}

	// Custom header should be present
	if !containsHeader(actualHeaders, "X-Custom-Header") {
		t.Errorf("Expected Access-Control-Allow-Headers to contain X-Custom-Header, got %s", actualHeaders)
	}

	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_CustomHeadersWithLocalhost tests custom headers work with localhost origins
func TestCorsMiddleware_CustomHeadersWithLocalhost(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{},
			AllowedHeaders: []string{"X-Development-Header"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "http://localhost:3000")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check that custom header is included for localhost
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))
	if !containsHeader(actualHeaders, "X-Development-Header") {
		t.Errorf("Expected Access-Control-Allow-Headers to contain X-Development-Header for localhost, got %s", actualHeaders)
	}

	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_CustomHeadersNotSetForNonAllowedOrigin tests that CORS headers (including custom) are not set for non-allowed origins
func TestCorsMiddleware_CustomHeadersNotSetForNonAllowedOrigin(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{"https://allowed.com"},
			AllowedHeaders: []string{"X-Custom-Header"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://malicious.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := CorsMiddleware(config)
	handler := middleware(next)
	handler(ctx)

	// Check CORS headers are NOT set (including Allow-Headers)
	if len(ctx.Response.Header.Peek("Access-Control-Allow-Headers")) != 0 {
		t.Error("Access-Control-Allow-Headers header should not be set for non-allowed origin")
	}

	// Check next handler was still called for non-OPTIONS requests
	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// Helper function to check if a header is present in the comma-separated list
func containsHeader(headerList, header string) bool {
	headers := splitHeaders(headerList)
	for _, h := range headers {
		if h == header {
			return true
		}
	}
	return false
}

// Helper function to split and trim headers
func splitHeaders(headerList string) []string {
	// Simple split by comma and trim spaces
	var headers []string
	start := 0
	for i := 0; i < len(headerList); i++ {
		if headerList[i] == ',' {
			header := headerList[start:i]
			// Trim spaces
			for len(header) > 0 && header[0] == ' ' {
				header = header[1:]
			}
			for len(header) > 0 && header[len(header)-1] == ' ' {
				header = header[:len(header)-1]
			}
			if header != "" {
				headers = append(headers, header)
			}
			start = i + 1
		}
	}
	// Add last header
	if start < len(headerList) {
		header := headerList[start:]
		// Trim spaces
		for len(header) > 0 && header[0] == ' ' {
			header = header[1:]
		}
		for len(header) > 0 && header[len(header)-1] == ' ' {
			header = header[:len(header)-1]
		}
		if header != "" {
			headers = append(headers, header)
		}
	}
	return headers
}

// Helper function to count occurrences of a header
func countHeaderOccurrences(headerList, header string) int {
	headers := splitHeaders(headerList)
	count := 0
	for _, h := range headers {
		if h == header {
			count++
		}
	}
	return count
}

// TestFasthttpToHTTPRequest_PathParams tests that path parameters are extracted correctly
func TestFasthttpToHTTPRequest_PathParams(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	// Set up test data
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/v1beta/files/file-abc123")

	// Simulate what the fasthttp router does - set path params as user values
	ctx.SetUserValue("file_id", "file-abc123")
	ctx.SetUserValue("model", "gemini-pro")

	// Set some system values that should be ignored
	ctx.SetUserValue("BifrostContextKeyRequestID", "req-123")
	ctx.SetUserValue("trace_id", "trace-456")
	ctx.SetUserValue("span_id", "span-789")

	// Acquire HTTPRequest from pool
	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)

	// Call the function
	fasthttpToHTTPRequest(ctx, req)

	// Verify path parameters are extracted
	expectedPathParams := map[string]string{
		"file_id": "file-abc123",
		"model":   "gemini-pro",
	}

	if len(req.PathParams) != len(expectedPathParams) {
		t.Errorf("Expected %d path params, got %d", len(expectedPathParams), len(req.PathParams))
	}

	for key, expectedValue := range expectedPathParams {
		if actualValue, exists := req.PathParams[key]; !exists {
			t.Errorf("Expected path param '%s' to exist", key)
		} else if actualValue != expectedValue {
			t.Errorf("Expected path param '%s' to be '%s', got '%s'", key, expectedValue, actualValue)
		}
	}

	// Verify system keys are NOT in path params
	systemKeys := []string{"BifrostContextKeyRequestID", "trace_id", "span_id"}
	for _, key := range systemKeys {
		if _, exists := req.PathParams[key]; exists {
			t.Errorf("System key '%s' should not be in path params", key)
		}
	}

	// Test the helper method
	if fileID := req.CaseInsensitivePathParamLookup("file_id"); fileID != "file-abc123" {
		t.Errorf("CaseInsensitivePathParamLookup failed: expected 'file-abc123', got '%s'", fileID)
	}
	if fileID := req.CaseInsensitivePathParamLookup("FILE_ID"); fileID != "file-abc123" {
		t.Errorf("CaseInsensitivePathParamLookup should be case-insensitive: expected 'file-abc123', got '%s'", fileID)
	}
}

func TestRequestDecompressionMiddleware_SupportedEncodings(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 10,
		},
	}

	plainBody := []byte(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`)
	testCases := []struct {
		name     string
		encoding string
		encode   func([]byte) ([]byte, error)
	}{
		{name: "gzip", encoding: "gzip", encode: gzipCompress},
		{name: "deflate", encoding: "deflate", encode: deflateCompress},
		{name: "brotli", encoding: "br", encode: brotliCompress},
		{name: "zstd", encoding: "zstd", encode: zstdCompress},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			compressedBody, err := tc.encode(plainBody)
			if err != nil {
				t.Fatalf("failed to encode body: %v", err)
			}

			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod("POST")
			ctx.Request.Header.SetContentType("application/json")
			ctx.Request.Header.Set("Content-Encoding", tc.encoding)
			ctx.Request.SetBodyRaw(compressedBody)

			nextCalled := false
			next := func(ctx *fasthttp.RequestCtx) {
				nextCalled = true
				if string(ctx.Request.Body()) != string(plainBody) {
					t.Fatalf("expected decompressed body, got %q", string(ctx.Request.Body()))
				}
			}

			handler := RequestDecompressionMiddleware(config)(next)
			handler(ctx)

			if !nextCalled {
				t.Fatal("next handler was not called")
			}
			if got := string(ctx.Request.Header.Peek("Content-Encoding")); got != "" {
				t.Fatalf("expected content-encoding to be cleared, got %q", got)
			}
			if got := string(ctx.Request.Header.Peek("Content-Length")); got != "" {
				t.Fatalf("expected content-length to be cleared, got %q", got)
			}
		})
	}
}

func TestRequestDecompressionMiddleware_InvalidCompressedBody(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 10,
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "gzip")
	ctx.Request.SetBodyRaw([]byte("not-a-valid-gzip-payload"))

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if nextCalled {
		t.Fatal("next handler should not be called for invalid compressed payload")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", ctx.Response.StatusCode())
	}

	var bifrostErr schemas.BifrostError
	if err := json.Unmarshal(ctx.Response.Body(), &bifrostErr); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "invalid compressed request body") {
		t.Fatalf("unexpected error message: %#v", bifrostErr.Error)
	}
}

func TestRequestDecompressionMiddleware_UnsupportedEncoding(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 10,
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "snappy")
	ctx.Request.SetBodyRaw([]byte("whatever"))

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if nextCalled {
		t.Fatal("next handler should not be called for unsupported content-encoding")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", ctx.Response.StatusCode())
	}

	var bifrostErr schemas.BifrostError
	if err := json.Unmarshal(ctx.Response.Body(), &bifrostErr); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "unsupported Content-Encoding") {
		t.Fatalf("unexpected error message: %#v", bifrostErr.Error)
	}
}

func TestRequestDecompressionMiddleware_DecompressedSizeLimit(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 1,
		},
	}

	plainBody := bytes.Repeat([]byte("a"), (1024*1024)+10)
	compressedBody, err := gzipCompress(plainBody)
	if err != nil {
		t.Fatalf("failed to gzip test payload: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "gzip")
	ctx.Request.SetBodyRaw(compressedBody)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if nextCalled {
		t.Fatal("next handler should not be called when decompressed body exceeds limit")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusRequestEntityTooLarge {
		t.Fatalf("expected status 413, got %d", ctx.Response.StatusCode())
	}

	var bifrostErr schemas.BifrostError
	if err := json.Unmarshal(ctx.Response.Body(), &bifrostErr); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "decompressed request body exceeds max allowed size") {
		t.Fatalf("unexpected error message: %#v", bifrostErr.Error)
	}
}

func TestRequestDecompressionMiddleware_EmptyBodyWithContentEncoding(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 10,
		},
	}

	encodings := []string{"gzip", "deflate", "br", "zstd"}
	for _, enc := range encodings {
		t.Run(enc, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod("POST")
			ctx.Request.Header.Set("Content-Encoding", enc)
			ctx.Request.SetBodyRaw([]byte{})

			nextCalled := false
			next := func(ctx *fasthttp.RequestCtx) {
				nextCalled = true
			}

			handler := RequestDecompressionMiddleware(config)(next)
			handler(ctx)

			// Empty body with Content-Encoding should return 400 (decoders fail on empty input)
			if nextCalled {
				// Some decoders may produce empty output — that's acceptable too
				return
			}
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
				t.Fatalf("expected status 400, got %d", ctx.Response.StatusCode())
			}
		})
	}
}

func TestRequestDecompressionMiddleware_NoContentEncoding(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 10,
		},
	}

	originalBody := []byte(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBodyRaw(originalBody)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
		if string(ctx.Request.Body()) != string(originalBody) {
			t.Fatalf("expected body to be unchanged, got %q", string(ctx.Request.Body()))
		}
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if !nextCalled {
		t.Fatal("next handler was not called")
	}
}

func TestRequestDecompressionMiddleware_ExactSizeLimit(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 1,
		},
	}

	plainBody := bytes.Repeat([]byte("a"), 1024*1024) // exactly 1 MB
	compressedBody, err := gzipCompress(plainBody)
	if err != nil {
		t.Fatalf("failed to gzip test payload: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "gzip")
	ctx.Request.SetBodyRaw(compressedBody)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
		if len(ctx.Request.Body()) != 1024*1024 {
			t.Fatalf("expected body length %d, got %d", 1024*1024, len(ctx.Request.Body()))
		}
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if !nextCalled {
		t.Fatal("next handler was not called — body exactly at limit should pass")
	}
}

// --- Streaming decompression path tests ---

func TestShouldStreamDecompress(t *testing.T) {
	defaultThreshold := int(schemas.DefaultLargePayloadRequestThresholdBytes)
	tests := []struct {
		name            string
		contentLength   int
		customThreshold int64 // 0 means no custom threshold (use default)
		want            bool
	}{
		{"chunked (CL=-1)", -1, 0, true},
		{"empty body (CL=0)", 0, 0, false},
		{"small body", 100, 0, false},
		{"at default threshold", defaultThreshold, 0, false},
		{"above default threshold", defaultThreshold + 1, 0, true},
		// Custom enterprise threshold (1MB) — body at 2MB should stream.
		{"above custom threshold", 2 * 1024 * 1024, 1 * 1024 * 1024, true},
		// Custom enterprise threshold (20MB) — body at default 10MB+1 should NOT stream.
		{"below custom threshold", defaultThreshold + 1, 20 * 1024 * 1024, false},
		// Chunked always streams regardless of custom threshold.
		{"chunked with custom threshold", -1, 50 * 1024 * 1024, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &lib.Config{}
			if tt.customThreshold > 0 {
				cfg.StreamingDecompressThreshold = tt.customThreshold
			}
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.Set("Content-Encoding", "gzip")
			if tt.contentLength >= 0 {
				ctx.Request.Header.SetContentLength(tt.contentLength)
			} else {
				// Simulate chunked: set body stream with unknown size
				ctx.Request.SetBodyStream(bytes.NewReader(nil), -1)
			}
			if got := shouldStreamDecompress(cfg, ctx); got != tt.want {
				t.Errorf("shouldStreamDecompress() = %v, want %v (CL=%d, threshold=%d)", got, tt.want, tt.contentLength, tt.customThreshold)
			}
		})
	}
}

func TestRequestDecompressionMiddleware_StreamingPath_ChunkedGzip(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 100,
		},
	}

	plainBody := []byte(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`)
	compressedBody, err := gzipCompress(plainBody)
	if err != nil {
		t.Fatalf("failed to gzip: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "gzip")
	// Chunked: SetBodyStream with size -1 triggers the streaming path
	ctx.Request.SetBodyStream(bytes.NewReader(compressedBody), -1)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
		// Content-Encoding should be cleared
		if ce := string(ctx.Request.Header.Peek("Content-Encoding")); ce != "" {
			t.Errorf("expected Content-Encoding to be cleared, got %q", ce)
		}
		// Body should be correctly decompressed
		body := ctx.Request.Body()
		if string(body) != string(plainBody) {
			t.Errorf("decompressed body mismatch: got %d bytes, want %d bytes", len(body), len(plainBody))
		}
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if !nextCalled {
		t.Fatal("next handler was not called")
	}
}

func TestRequestDecompressionMiddleware_StreamingPath_AllEncodings(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 100,
		},
	}

	plainBody := []byte(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`)
	testCases := []struct {
		name     string
		encoding string
		encode   func([]byte) ([]byte, error)
	}{
		{name: "gzip", encoding: "gzip", encode: gzipCompress},
		{name: "deflate", encoding: "deflate", encode: deflateCompress},
		{name: "brotli", encoding: "br", encode: brotliCompress},
		{name: "zstd", encoding: "zstd", encode: zstdCompress},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			compressedBody, err := tc.encode(plainBody)
			if err != nil {
				t.Fatalf("failed to encode body: %v", err)
			}

			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod("POST")
			ctx.Request.Header.Set("Content-Encoding", tc.encoding)
			// Use chunked (-1) to trigger streaming path regardless of compressed size
			ctx.Request.SetBodyStream(bytes.NewReader(compressedBody), -1)

			nextCalled := false
			next := func(ctx *fasthttp.RequestCtx) {
				nextCalled = true
				body := ctx.Request.Body()
				if string(body) != string(plainBody) {
					t.Fatalf("expected decompressed body, got %q", string(body))
				}
			}

			handler := RequestDecompressionMiddleware(config)(next)
			handler(ctx)

			if !nextCalled {
				t.Fatal("next handler was not called")
			}
			if got := string(ctx.Request.Header.Peek("Content-Encoding")); got != "" {
				t.Fatalf("expected content-encoding to be cleared, got %q", got)
			}
		})
	}
}

func TestRequestDecompressionMiddleware_StreamingPath_InvalidBody(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 100,
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "gzip")
	// Chunked with invalid gzip data → streaming path → error
	ctx.Request.SetBodyStream(bytes.NewReader([]byte("not-a-valid-gzip-payload")), -1)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if nextCalled {
		t.Fatal("next handler should not be called for invalid compressed payload")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", ctx.Response.StatusCode())
	}
}

func TestRequestDecompressionMiddleware_StreamingPath_UnsupportedEncoding(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 100,
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "snappy")
	ctx.Request.SetBodyStream(bytes.NewReader([]byte("whatever")), -1)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if nextCalled {
		t.Fatal("next handler should not be called for unsupported encoding")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", ctx.Response.StatusCode())
	}
}

func TestRequestDecompressionMiddleware_BufferedPath_SmallGzip(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 10,
		},
	}

	plainBody := []byte(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`)
	compressedBody, err := gzipCompress(plainBody)
	if err != nil {
		t.Fatalf("failed to gzip: %v", err)
	}

	// Verify compressed body is below threshold (should use buffered path)
	if int64(len(compressedBody)) > schemas.DefaultLargePayloadRequestThresholdBytes {
		t.Skip("compressed body unexpectedly exceeds threshold")
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "gzip")
	// SetBodyRaw with known small Content-Length → buffered path
	ctx.Request.SetBodyRaw(compressedBody)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
		if string(ctx.Request.Body()) != string(plainBody) {
			t.Fatalf("expected decompressed body, got %q", string(ctx.Request.Body()))
		}
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if !nextCalled {
		t.Fatal("next handler was not called")
	}
}

func TestRequestDecompressionMiddleware_StreamingPath_LargeGzip(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			MaxRequestBodySizeMB: 100,
		},
	}

	// Random bytes are incompressible — compressed size ≈ input size + gzip overhead.
	bodySize := int(schemas.DefaultLargePayloadRequestThresholdBytes) + 1024*1024
	plainBody := make([]byte, bodySize)
	if _, err := cryptoRand.Read(plainBody); err != nil {
		t.Fatalf("failed to generate random data: %v", err)
	}
	compressedBody, err := gzipCompress(plainBody)
	if err != nil {
		t.Fatalf("failed to gzip: %v", err)
	}

	if int64(len(compressedBody)) <= schemas.DefaultLargePayloadRequestThresholdBytes {
		t.Skipf("compressed body %d bytes is below threshold %d",
			len(compressedBody), schemas.DefaultLargePayloadRequestThresholdBytes)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "gzip")
	ctx.Request.Header.SetContentLength(len(compressedBody))
	ctx.Request.SetBodyStream(bytes.NewReader(compressedBody), len(compressedBody))

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
		if ce := string(ctx.Request.Header.Peek("Content-Encoding")); ce != "" {
			t.Errorf("expected Content-Encoding to be cleared, got %q", ce)
		}
		body := ctx.Request.Body()
		if len(body) != len(plainBody) {
			t.Errorf("decompressed body length: got %d, want %d", len(body), len(plainBody))
		}
		if !bytes.Equal(body, plainBody) {
			t.Error("decompressed body content does not match original")
		}
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if !nextCalled {
		t.Fatal("next handler was not called")
	}
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// deflateCompress produces zlib-wrapped DEFLATE (RFC 1950) — the correct
// format for HTTP Content-Encoding "deflate" per RFC 9110 §8.4.1.2.
func deflateCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func brotliCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func zstdCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(enc, bytes.NewReader(data)); err != nil {
		enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
