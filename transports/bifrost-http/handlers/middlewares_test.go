package handlers

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	cryptoRand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/fasthttp/router"
	"github.com/klauspost/compress/zstd"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/temptoken"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
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
		ClientConfig: &configstore.ClientConfig{
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

			middleware := NewCorsMiddleware(config).Middleware()
			handler := middleware(next)
			handler(ctx)

			// Check CORS headers are set
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")) != origin {
				t.Errorf("Expected Access-Control-Allow-Origin to be %s, got %s", origin, string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")))
			}
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Methods")) != "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD" {
				t.Errorf("Access-Control-Allow-Methods header not set correctly")
			}
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Headers")) != "Content-Type, Authorization, X-Requested-With, X-Stainless-Timeout, X-Api-Key, X-OpenAI-Agents-SDK, X-Operation-ID, X-Bifrost-View-As, X-Bifrost-Setup-Token, MCP-Protocol-Version, Mcp-Session-Id, Mcp-Method, Mcp-Name, Last-Event-ID" {
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

// TestCorsMiddleware_ExposesBifrostResponseHeaders pins that a cross-origin browser client can
// read the gateway's x-bifrost-* response headers. With client.extra_fields_placement =
// "headers" they are the only place extra_fields reaches the client, and a browser hides any
// response header that Access-Control-Expose-Headers does not list.
func TestCorsMiddleware_ExposesBifrostResponseHeaders(t *testing.T) {
	SetLogger(&mockLogger{})
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{AllowedOrigins: []string{"https://app.example.com"}}}
	handler := NewCorsMiddleware(config).Middleware()(func(ctx *fasthttp.RequestCtx) {})

	for _, origin := range []string{"https://app.example.com", "http://localhost:3000"} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Origin", origin)
		handler(ctx)
		exposed := strings.Split(string(ctx.Response.Header.Peek("Access-Control-Expose-Headers")), ", ")
		for _, name := range []string{
			"x-bifrost-request-id", "x-bifrost-trace-id", "x-bifrost-provider", "x-bifrost-request-type",
			"x-bifrost-routing-info-provider", "x-bifrost-routing-info-model", "x-bifrost-cache-hit",
			"x-bifrost-latency-ms", "x-bifrost-converted-request-type", "x-bifrost-error-type", "x-bifrost-retry-after-ms",
		} {
			if !slices.Contains(exposed, name) {
				t.Errorf("origin %s: Access-Control-Expose-Headers %q does not list %s", origin, exposed, name)
			}
		}
	}

	blocked := &fasthttp.RequestCtx{}
	blocked.Request.Header.Set("Origin", "https://malicious.com")
	handler(blocked)
	if got := blocked.Response.Header.Peek("Access-Control-Expose-Headers"); len(got) != 0 {
		t.Errorf("a non-allowed origin must not get Access-Control-Expose-Headers, got %q", got)
	}
}

// TestCorsMiddleware_ConfiguredOrigins tests that configured allowed origins work
func TestCorsMiddleware_ConfiguredOrigins(t *testing.T) {
	allowedOrigin := "https://example.com"
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			AllowedOrigins: []string{allowedOrigin},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", allowedOrigin)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := NewCorsMiddleware(config).Middleware()
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
		ClientConfig: &configstore.ClientConfig{
			AllowedOrigins: []string{"https://allowed.com"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://malicious.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := NewCorsMiddleware(config).Middleware()
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
// TestCorsMiddleware_PreflightAllowsSetupTokenHeader pins that the dashboard's
// X-Bifrost-Setup-Token header passes a cross-origin preflight with no allowed_headers
// configured. allowed_origins "*" only governs the origin check; without the header in the
// default list, every locked-dashboard call from a separate UI origin fails CORS.
func TestCorsMiddleware_PreflightAllowsSetupTokenHeader(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, origins := range [][]string{{"*"}, {"https://dash.example.com"}} {
		t.Run(strings.Join(origins, ","), func(t *testing.T) {
			config := &lib.Config{ClientConfig: &configstore.ClientConfig{AllowedOrigins: origins}}
			for _, origin := range []string{"http://localhost:3000", "https://dash.example.com"} {
				ctx := &fasthttp.RequestCtx{}
				ctx.Request.Header.SetMethod("OPTIONS")
				ctx.Request.Header.Set("Origin", origin)
				ctx.Request.Header.Set("Access-Control-Request-Headers", "content-type, x-bifrost-setup-token")
				NewCorsMiddleware(config).Middleware()(func(ctx *fasthttp.RequestCtx) {})(ctx)
				if ctx.Response.StatusCode() != fasthttp.StatusOK {
					t.Fatalf("%s: preflight status = %d", origin, ctx.Response.StatusCode())
				}
				allowHeaders := strings.ToLower(string(ctx.Response.Header.Peek("Access-Control-Allow-Headers")))
				if !strings.Contains(allowHeaders, "x-bifrost-setup-token") {
					t.Errorf("%s: Access-Control-Allow-Headers %q must include X-Bifrost-Setup-Token", origin, allowHeaders)
				}
			}
		})
	}
}

// Browser MCP clients send the Streamable HTTP headers on every request after initialize, and on
// every 2026-07-28 request; a preflight that leaves any out blocks the request in the browser.
func TestCorsMiddleware_PreflightAllowsMCPTransportHeaders(t *testing.T) {
	SetLogger(&mockLogger{})
	requested := []string{"content-type", "mcp-protocol-version", "mcp-session-id", "mcp-method", "mcp-name", "last-event-id", "mcp-param-region"}
	for _, origins := range [][]string{{"*"}, {"https://inspector.example.com"}} {
		t.Run(strings.Join(origins, ","), func(t *testing.T) {
			config := &lib.Config{ClientConfig: &configstore.ClientConfig{AllowedOrigins: origins}}
			for _, origin := range []string{"http://localhost:6274", "https://inspector.example.com"} {
				ctx := &fasthttp.RequestCtx{}
				ctx.Request.Header.SetMethod("OPTIONS")
				ctx.Request.Header.Set("Origin", origin)
				ctx.Request.Header.Set("Access-Control-Request-Headers", strings.Join(requested, ", "))
				NewCorsMiddleware(config).Middleware()(func(ctx *fasthttp.RequestCtx) {})(ctx)
				assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), origin)
				allowed := strings.Split(strings.ToLower(string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))), ", ")
				for _, header := range requested {
					assert.Contains(t, allowed, header, "%s: Access-Control-Allow-Headers", origin)
				}
				exposed := strings.Split(strings.ToLower(string(ctx.Response.Header.Peek("Access-Control-Expose-Headers"))), ", ")
				assert.Contains(t, exposed, "mcp-session-id", "%s: initialize's session id must be readable", origin)
			}
		})
	}
}

func TestCorsMiddleware_PreflightAllowedOrigin(t *testing.T) {
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
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

	middleware := NewCorsMiddleware(config).Middleware()
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
		ClientConfig: &configstore.ClientConfig{
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

	middleware := NewCorsMiddleware(config).Middleware()
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
		ClientConfig: &configstore.ClientConfig{
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

	middleware := NewCorsMiddleware(config).Middleware()
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
		ClientConfig: &configstore.ClientConfig{
			AllowedOrigins: []string{},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	// No Origin header set

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := NewCorsMiddleware(config).Middleware()
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

// TestRecoveryMiddleware_RecoversFromPanic proves a panicking handler no longer
// takes down the process: the request is answered with a 500 instead of
// unwinding past RecoveryMiddleware.
func TestRecoveryMiddleware_RecoversFromPanic(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := &fasthttp.RequestCtx{}

	handler := func(ctx *fasthttp.RequestCtx) {
		values := []int{1, 2, 3}
		_ = values[10] // out-of-bounds access, mirrors an unguarded slice index in request-derived parsing
	}

	wrapped := RecoveryMiddleware(newRecoveryTestCors())(handler)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped RecoveryMiddleware: %v", r)
			}
		}()
		wrapped(ctx)
	}()

	if ctx.Response.StatusCode() != fasthttp.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
	}
}

// TestRecoveryMiddleware_DoesNotLogPanicValue asserts the recovery log never echoes
// an arbitrary panic value, which can wrap request content or secrets, while still
// keeping runtime error messages, which are runtime-generated and carry no request data.
func TestRecoveryMiddleware_DoesNotLogPanicValue(t *testing.T) {
	capLogger := &captureLogger{}
	SetLogger(capLogger)
	defer SetLogger(&mockLogger{})

	secretPanic := func(*fasthttp.RequestCtx) { panic(errors.New("upstream rejected key sk-secret-123")) }
	RecoveryMiddleware(newRecoveryTestCors())(secretPanic)(&fasthttp.RequestCtx{})

	if len(capLogger.errors) != 1 {
		t.Fatalf("error logs = %d, want 1", len(capLogger.errors))
	}
	if strings.Contains(capLogger.errors[0], "sk-secret-123") {
		t.Errorf("recovery log leaked the panic value: %q", capLogger.errors[0])
	}
	if !strings.Contains(capLogger.errors[0], "*errors.errorString") {
		t.Errorf("recovery log = %q, want the panic value type *errors.errorString", capLogger.errors[0])
	}

	runtimePanic := func(*fasthttp.RequestCtx) {
		values := []int{1, 2, 3}
		idx := 10
		_ = values[idx]
	}
	RecoveryMiddleware(newRecoveryTestCors())(runtimePanic)(&fasthttp.RequestCtx{})

	if len(capLogger.errors) != 2 {
		t.Fatalf("error logs = %d, want 2", len(capLogger.errors))
	}
	if !strings.Contains(capLogger.errors[1], "index out of range [10] with length 3") {
		t.Errorf("recovery log = %q, want the runtime error message kept", capLogger.errors[1])
	}
}

// TestRecoverWebSocketSession_RecoversAndRedactsPanic pins the helper every WebSocket
// upgrade callback defers: a panic on the hijacked session goroutine is swallowed (so the
// process keeps serving), logged once, and never logged with the panic value itself.
func TestRecoverWebSocketSession_RecoversAndRedactsPanic(t *testing.T) {
	capLogger := &captureLogger{}
	SetLogger(capLogger)
	defer SetLogger(&mockLogger{})

	session := func() {
		defer recoverWebSocketSession(nil)
		panic(errors.New("upstream rejected key sk-secret-456"))
	}
	session() // must return normally

	if len(capLogger.errors) != 1 {
		t.Fatalf("error logs = %d, want 1", len(capLogger.errors))
	}
	if strings.Contains(capLogger.errors[0], "sk-secret-456") {
		t.Errorf("websocket recovery log leaked the panic value: %q", capLogger.errors[0])
	}
	if !strings.Contains(capLogger.errors[0], "websocket session") || !strings.Contains(capLogger.errors[0], "*errors.errorString") {
		t.Errorf("websocket recovery log = %q, want the session marker and the panic value type", capLogger.errors[0])
	}

	quiet := func() {
		defer recoverWebSocketSession(nil)
	}
	quiet()
	if len(capLogger.errors) != 1 {
		t.Fatalf("error logs after a non-panicking session = %d, want 1", len(capLogger.errors))
	}

	// A runtime.Error is summarized by type alone on the WebSocket paths too: its text can carry
	// whatever a plugin put in it.
	runtimeSession := func() {
		defer recoverWebSocketSession(nil)
		panic(secretRuntimeError{})
	}
	runtimeSession()
	if len(capLogger.errors) != 2 {
		t.Fatalf("error logs = %d, want 2", len(capLogger.errors))
	}
	if strings.Contains(capLogger.errors[1], "sk-secret-789") {
		t.Errorf("websocket recovery log leaked a runtime.Error's text: %q", capLogger.errors[1])
	}
	err := runWebSocketRelay(nil, func() error { panic(secretRuntimeError{}) })
	if err == nil || strings.Contains(err.Error(), "sk-secret-789") {
		t.Errorf("relay error leaked a runtime.Error's text: %v", err)
	}
}

// secretRuntimeError is a runtime.Error whose text carries something that must not be echoed.
type secretRuntimeError struct{}

func (secretRuntimeError) Error() string { return "index out of range for key sk-secret-789" }
func (secretRuntimeError) RuntimeError() {}

// TestRecoveryMiddleware_PassesThroughNormalRequests confirms the middleware is
// a no-op for handlers that don't panic.
func TestRecoveryMiddleware_PassesThroughNormalRequests(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := &fasthttp.RequestCtx{}
	handlerCalled := false

	handler := func(ctx *fasthttp.RequestCtx) {
		handlerCalled = true
		ctx.SetStatusCode(fasthttp.StatusOK)
	}

	wrapped := RecoveryMiddleware(newRecoveryTestCors())(handler)
	wrapped(ctx)

	if !handlerCalled {
		t.Error("handler was not called")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("expected status %d, got %d", fasthttp.StatusOK, ctx.Response.StatusCode())
	}
}

// newRecoveryTestCors returns a CorsMiddleware with default config (localhost
// origins allowed) for RecoveryMiddleware tests.
func newRecoveryTestCors() *CorsMiddleware {
	return NewCorsMiddleware(&lib.Config{ClientConfig: &configstore.ClientConfig{}})
}

// TestRecoveryMiddleware_KeepsOuterHeadersAndLogsStatus runs a panic through the
// production server-level chain (ServerRootHandler). The 500 must still
// carry the security and CORS headers the outer middlewares set before the
// panic, and the CORS access log must record 500, not the default 200.
func TestRecoveryMiddleware_KeepsOuterHeadersAndLogsStatus(t *testing.T) {
	capLogger := &captureLogger{}
	SetLogger(capLogger)
	defer SetLogger(&mockLogger{})

	cors := newRecoveryTestCors()
	panicking := func(ctx *fasthttp.RequestCtx) {
		ctx.Response.Header.Set("X-Handler-Partial", "stale")
		panic("boom")
	}
	handler := ServerRootHandler(cors, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, panicking)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/chat/completions")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Origin", "http://localhost:3000")
	handler(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", ctx.Response.StatusCode(), fasthttp.StatusInternalServerError)
	}
	if got := string(ctx.Response.Header.Peek("X-Frame-Options")); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := string(ctx.Response.Header.Peek("Access-Control-Allow-Origin")); got != "http://localhost:3000" {
		t.Errorf("Access-Control-Allow-Origin = %q, want http://localhost:3000", got)
	}
	if got := string(ctx.Response.Header.Peek("X-Handler-Partial")); got != "" {
		t.Errorf("X-Handler-Partial = %q, want partial handler headers dropped", got)
	}
	if got := string(ctx.Response.Header.Peek("x-bifrost-trace-id")); got != "" {
		t.Errorf("x-bifrost-trace-id = %q, want none when Tracing did not run", got)
	}
	if len(capLogger.events) != 1 {
		t.Fatalf("access log events = %d, want 1", len(capLogger.events))
	}
	if got := capLogger.events[0].intFields["http.status_code"]; got != fasthttp.StatusInternalServerError {
		t.Errorf("access log http.status_code = %d, want %d", got, fasthttp.StatusInternalServerError)
	}
}

// TestRecoveryMiddleware_TracingRecordsPanicAsError runs a panic through the
// production stack: ServerRootHandler around the inference-route outer chain
// (InferenceOuterMiddlewares). The root span must end as an
// error with http.status_code 500, not be exported as a successful request.
func TestRecoveryMiddleware_TracingRecordsPanicAsError(t *testing.T) {
	SetLogger(&mockLogger{})

	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()
	plugin := &captureTracePlugin{done: make(chan struct{})}
	tm := NewTracingMiddleware(tracer)
	tm.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, nil)

	panicking := func(*fasthttp.RequestCtx) { panic("boom") }
	cors := newRecoveryTestCors()
	route := lib.ChainMiddlewares(panicking, InferenceOuterMiddlewares(tm, cors)...)
	handler := ServerRootHandler(cors, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, route)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/chat/completions")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("x-request-id", "req-panic-1")
	handler(ctx)

	// The 500 must still carry the correlation IDs so the caller can find the trace.
	if got := string(ctx.Response.Header.Peek("x-request-id")); got != "req-panic-1" {
		t.Errorf("x-request-id = %q, want req-panic-1", got)
	}
	if got := string(ctx.Response.Header.Peek("x-bifrost-request-id")); got != "req-panic-1" {
		t.Errorf("x-bifrost-request-id = %q, want req-panic-1", got)
	}
	if got := string(ctx.Response.Header.Peek("x-bifrost-trace-id")); got == "" {
		t.Error("expected x-bifrost-trace-id on the recovered 500")
	}

	select {
	case <-plugin.done:
		if plugin.rootStatus != schemas.SpanStatusError {
			t.Errorf("root span status = %v, want %v", plugin.rootStatus, schemas.SpanStatusError)
		}
		if plugin.rootStatusCode != fasthttp.StatusInternalServerError {
			t.Errorf("root span http.status_code = %v, want %d", plugin.rootStatusCode, fasthttp.StatusInternalServerError)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trace was not flushed to the observability plugin")
	}
}

func TestIsInferenceWSEndpoint(t *testing.T) {
	paths := []string{
		"/v1/responses",
		"/v1/realtime",
		"/responses",
		"/realtime",
		"/openai/v1/responses",
		"/openai/responses",
		"/openai/openai/responses",
		"/openai/v1/realtime",
		"/openai/realtime",
		"/openai/openai/realtime",
	}

	for _, path := range paths {
		if !isInferenceWSEndpoint(path) {
			t.Fatalf("expected inference websocket path %s to be recognized", path)
		}
	}

	if isInferenceWSEndpoint("/api/ws") {
		t.Fatal("dashboard websocket path should not be treated as inference websocket")
	}
	if isInferenceWSEndpoint("/openai/chat/completions") {
		t.Fatal("non-websocket OpenAI path should not be treated as inference websocket")
	}
}

func TestIsRealtimeTransportEndpoint(t *testing.T) {
	paths := []string{
		"/v1/realtime",
		"/realtime",
		"/openai/realtime",
		"/openai/v1/realtime",
		"/openai/openai/realtime",
		"/v1/realtime/calls",
		"/realtime/calls",
		"/openai/realtime/calls",
		"/openai/v1/realtime/calls",
		"/openai/openai/realtime/calls",
	}

	for _, path := range paths {
		if !isRealtimeTransportEndpoint(path) {
			t.Fatalf("expected realtime transport path %s to be recognized", path)
		}
	}

	nonTransportPaths := []string{
		"/v1/realtime/client_secrets",
		"/openai/v1/realtime/client_secrets",
		"/v1/chat/completions",
	}

	for _, path := range nonTransportPaths {
		if isRealtimeTransportEndpoint(path) {
			t.Fatalf("did not expect non-transport path %s to be recognized", path)
		}
	}
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
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("password"),
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

// TestAuthMiddleware_DisabledAuthConfig_CrossOrigin: while dashboard auth is disabled, a browser
// request from an origin the operator did not name is refused on management routes. Localhost
// origins, explicitly listed origins and domain patterns pass; the bare "*" default does not count.
// Requests without an Origin, whitelisted routes and inference routes are unaffected.
func TestAuthMiddleware_DisabledAuthConfig_CrossOrigin(t *testing.T) {
	SetLogger(&mockLogger{})

	run := func(am *AuthMiddleware, middleware schemas.BifrostHTTPMiddleware, path, origin string) (nextCalled bool, status int) {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(path)
		if origin != "" {
			ctx.Request.Header.Set("Origin", origin)
		}
		middleware(func(ctx *fasthttp.RequestCtx) { nextCalled = true })(ctx)
		return nextCalled, ctx.Response.StatusCode()
	}

	t.Run("auth config nil, default origins", func(t *testing.T) {
		am := &AuthMiddleware{}
		api := am.APIMiddleware()

		called, _ := run(am, api, "/api/providers", "")
		assert.True(t, called, "no Origin header: CLI/SDK callers are unaffected")
		called, _ = run(am, api, "/api/providers", "http://localhost:3000")
		assert.True(t, called, "localhost origins are always allowed")
		called, _ = run(am, api, "/api/providers", "http://[::1]:3000")
		assert.True(t, called)
		called, status := run(am, api, "/api/providers", "https://evil.example")
		assert.False(t, called, "an unlisted origin must not reach the open management API")
		assert.Equal(t, fasthttp.StatusForbidden, status)
		called, _ = run(am, api, "/health", "https://evil.example")
		assert.True(t, called, "whitelisted routes keep today's behaviour")
		called, _ = run(am, am.InferenceMiddleware(), "/v1/chat/completions", "https://evil.example")
		assert.True(t, called, "inference routes are governed by the governance plugin, not by this check")
	})

	t.Run("auth disabled, configured origins", func(t *testing.T) {
		am := &AuthMiddleware{}
		am.UpdateAuthConfig(&configstore.AuthConfig{
			AdminUserName: schemas.NewSecretVar("admin"),
			AdminPassword: schemas.NewSecretVar("password"),
			IsEnabled:     false,
		})
		api := am.APIMiddleware()

		am.UpdateAllowedOrigins([]string{"*"})
		called, status := run(am, api, "/api/providers", "https://evil.example")
		assert.False(t, called, "the bare wildcard does not name an origin")
		assert.Equal(t, fasthttp.StatusForbidden, status)

		am.UpdateAllowedOrigins([]string{"https://app.example.com", "https://*.corp.example"})
		called, _ = run(am, api, "/api/providers", "https://app.example.com")
		assert.True(t, called, "an explicitly listed origin passes")
		called, _ = run(am, api, "/api/providers", "https://tools.corp.example")
		assert.True(t, called, "a domain pattern names the origin")
		called, status = run(am, api, "/api/providers", "https://app.example.com.evil.example")
		assert.False(t, called)
		assert.Equal(t, fasthttp.StatusForbidden, status)

		// The dashboard served from a non-localhost address sends its own Origin on writes;
		// a same-origin request (Origin host:port equals Host) is not cross-origin.
		sameOrigin := &fasthttp.RequestCtx{}
		sameOrigin.Request.SetRequestURI("/api/config")
		sameOrigin.Request.Header.SetMethod(fasthttp.MethodPut)
		sameOrigin.Request.Header.SetHost("10.0.0.5:8080")
		sameOrigin.Request.Header.Set("Origin", "http://10.0.0.5:8080")
		sameOriginCalled := false
		api(func(*fasthttp.RequestCtx) { sameOriginCalled = true })(sameOrigin)
		assert.True(t, sameOriginCalled, "the dashboard's own same-origin write must not be refused (status %d)", sameOrigin.Response.StatusCode())

		defaultPort := &fasthttp.RequestCtx{}
		defaultPort.Request.SetRequestURI("/api/config")
		defaultPort.Request.Header.SetHost("bifrost.internal:443")
		defaultPort.Request.Header.Set("Origin", "https://bifrost.internal")
		defaultPortCalled := false
		api(func(*fasthttp.RequestCtx) { defaultPortCalled = true })(defaultPort)
		assert.True(t, defaultPortCalled, "an explicit default port in Host is the same origin")

		crossHost := &fasthttp.RequestCtx{}
		crossHost.Request.SetRequestURI("/api/config")
		crossHost.Request.Header.SetHost("10.0.0.5:8080")
		crossHost.Request.Header.Set("Origin", "http://10.0.0.5:9090")
		crossHostCalled := false
		api(func(*fasthttp.RequestCtx) { crossHostCalled = true })(crossHost)
		assert.False(t, crossHostCalled, "a different port is a different origin")
		assert.Equal(t, fasthttp.StatusForbidden, crossHost.Response.StatusCode())

		// Operator-configured whitelisted routes are exempt like the system ones.
		am.UpdateWhitelistedRoutes([]string{"/api/public/*"})
		called, _ = run(am, api, "/api/public/status", "https://evil.example")
		assert.True(t, called)
	})

	// Without a config store there is no auth middleware at all; the bypass middleware the server
	// installs instead applies the same rule, since the API is just as open there.
	t.Run("no config store, auth bypassed", func(t *testing.T) {
		am := &AuthMiddleware{}
		bypassed := AuthBypassedMiddleware([]string{"*"}, nil)

		called, _ := run(am, bypassed, "/api/logs", "")
		assert.True(t, called, "no Origin header: CLI/SDK callers are unaffected")
		called, _ = run(am, bypassed, "/api/logs", "http://localhost:3000")
		assert.True(t, called, "localhost origins are always allowed")
		called, status := run(am, bypassed, "/api/logs", "https://evil.example")
		assert.False(t, called, "an unlisted origin must not reach the open management API without a config store either")
		assert.Equal(t, fasthttp.StatusForbidden, status)
		called, _ = run(am, bypassed, "/health", "https://evil.example")
		assert.True(t, called, "whitelisted routes keep today's behaviour")

		listed := AuthBypassedMiddleware([]string{"https://app.example.com", "https://*.corp.example"}, []string{"/api/public/*"})
		called, _ = run(am, listed, "/api/logs", "https://app.example.com")
		assert.True(t, called, "an explicitly listed origin passes")
		called, _ = run(am, listed, "/api/logs", "https://tools.corp.example")
		assert.True(t, called, "a domain pattern names the origin")
		called, status = run(am, listed, "/api/logs", "https://app.example.com.evil.example")
		assert.False(t, called)
		assert.Equal(t, fasthttp.StatusForbidden, status)
		called, _ = run(am, listed, "/api/public/status", "https://evil.example")
		assert.True(t, called, "operator-configured whitelisted routes are exempt")

		sameOrigin := &fasthttp.RequestCtx{}
		sameOrigin.Request.SetRequestURI("/api/config")
		sameOrigin.Request.Header.SetMethod(fasthttp.MethodPut)
		sameOrigin.Request.Header.SetHost("10.0.0.5:8080")
		sameOrigin.Request.Header.Set("Origin", "http://10.0.0.5:8080")
		sameOriginCalled := false
		bypassed(func(*fasthttp.RequestCtx) { sameOriginCalled = true })(sameOrigin)
		assert.True(t, sameOriginCalled, "the dashboard's own same-origin write must not be refused (status %d)", sameOrigin.Response.StatusCode())

		// A refused request is never marked as the local admin.
		refused := &fasthttp.RequestCtx{}
		refused.Request.SetRequestURI("/api/logs")
		refused.Request.Header.Set("Origin", "https://evil.example")
		bypassed(func(*fasthttp.RequestCtx) {})(refused)
		assert.Nil(t, refused.UserValue(schemas.IsLocalAdminContextKey))
		assert.Nil(t, refused.UserValue(schemas.BifrostContextKeyAuthBypassed))
	})
}

// TestAuthMiddleware_EnabledAuthConfig_NoAuth tests that auth middleware blocks unauthenticated requests
func TestAuthMiddleware_EnabledAuthConfig_NoAuth(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
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

func TestAuthMiddleware_SkillsPublicServeManagementSplit(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})

	t.Run("serve routes bypass auth", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/api/skills/serve/my-skill.git/info/refs?service=git-upload-pack")

		nextCalled := false
		handler := am.APIMiddleware()(func(ctx *fasthttp.RequestCtx) {
			nextCalled = true
		})
		handler(ctx)

		if !nextCalled {
			t.Fatal("expected public skills serving route to bypass auth")
		}
	})

	t.Run("management routes require auth", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/api/skills")

		nextCalled := false
		handler := am.APIMiddleware()(func(ctx *fasthttp.RequestCtx) {
			nextCalled = true
		})
		handler(ctx)

		if nextCalled {
			t.Fatal("expected skills management route to require auth")
		}
		if ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
			t.Fatalf("expected %d, got %d", fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
		}
	})
}

// TestAuthMiddleware_EncodedTraversalDoesNotBypassAuth exercises the same raw-path
// router dispatch as production. Authorization must never use the decoded path
// to whitelist a request dispatched to a protected parameterized handler.
func TestAuthMiddleware_EncodedTraversalDoesNotBypassAuth(t *testing.T) {
	am := newTraversalAuthMiddleware()
	cases := []struct {
		name, method, route, uri string
		status                   int
	}{
		{"provider update", "PUT", "/api/providers/{provider}", "/api/providers/..%2Fskills%2Fserve%2Fmalicious", 401},
		{"provider key creation", "POST", "/api/providers/{provider}/keys", "/api/providers/..%2Fskills%2Fserve%2Fmalicious/keys", 401},
		{"provider deletion", "DELETE", "/api/providers/{provider}", "/api/providers/..%2Fskills%2Fserve%2Fmalicious", 401},
		{"plugin dev prefix", "PUT", "/api/plugins/{name}", "/api/plugins/..%2Fdev%2Fmalicious", 401},
		{"lowercase slash", "PUT", "/api/providers/{provider}", "/api/providers/..%2fskills%2fserve%2fmalicious", 401},
		{"encoded dots", "PUT", "/api/providers/{provider}", "/api/providers/%2e%2e%2Fskills%2Fserve%2Fmalicious", 401},
		{"double encoding", "PUT", "/api/providers/{provider}", "/api/providers/%252e%252e%252Fskills%252Fserve%252Fmalicious", 401},
		{"query string", "PUT", "/api/providers/{provider}", "/api/providers/..%2Fskills%2Fserve%2Fmalicious?source=test", 401},
		{"public skills", "GET", "/api/skills/serve/{path:*}", "/api/skills/serve/my-skill.git/info/refs?service=git-upload-pack", 204},
		{"public dev", "GET", "/api/dev/pprof/{profile}", "/api/dev/pprof/goroutine", 204},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTraversalAuthRoute(t, am, tc.method, tc.route, tc.uri, "", tc.status)
		})
	}
}

// traversalTokenStore only implements the lookup used by the real token service.
// An embedded interface makes any unexpected store access fail loudly.
type traversalTokenStore struct {
	configstore.ConfigStore
	row tables.TempToken
}

func (s *traversalTokenStore) GetTempTokenByHash(_ context.Context, hash string) (*tables.TempToken, error) {
	if hash != s.row.TokenHash {
		return nil, nil
	}
	row := s.row
	return &row, nil
}

func newTraversalAuthMiddleware() *AuthMiddleware {
	SetLogger(&mockLogger{})
	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})
	return am
}

// Record router selection separately from the protected handler: a 404 caused
// by a malformed fixture must not be mistaken for successful auth enforcement.
func assertTraversalAuthRoute(t *testing.T, am *AuthMiddleware, method, route, uri, token string, status int) {
	t.Helper()
	matched, reached := false, false
	r := router.New()
	protected := am.APIMiddleware()(func(ctx *fasthttp.RequestCtx) {
		reached = true
		ctx.SetStatusCode(fasthttp.StatusNoContent)
	})
	r.Handle(method, route, func(ctx *fasthttp.RequestCtx) {
		matched = true
		protected(ctx)
	})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(uri)
	if token != "" {
		ctx.Request.Header.Set("X-Bifrost-Temp-Token", token)
	}
	r.Handler(ctx)
	if !matched {
		t.Fatalf("fixture did not match route %s %s: %q", method, route, uri)
	}
	if got := ctx.Response.StatusCode(); got != status {
		t.Fatalf("%s %s: expected status %d, got %d (handler reached=%v)", method, uri, status, got, reached)
	}
	if wantReached := status == fasthttp.StatusNoContent; reached != wantReached {
		t.Fatalf("%s %s: handler reached=%v, want %v", method, uri, reached, wantReached)
	}
	if reached && token != "" {
		if ctx.UserValue(schemas.BifrostContextKeyTempTokenScope) == nil || ctx.UserValue(schemas.BifrostContextKeyTempTokenResourceID) == nil {
			t.Fatal("successful token auth must attach the validated scope and resource ID")
		}
	}
}

func TestAuthMiddleware_TempTokenEncodedTraversal(t *testing.T) {
	const token = "test-scoped-token"
	const flowID = "flow-123"
	for _, scope := range []temptoken.Scope{mcpAuthScope, mcpHeadersAuthScope, oauth2ConsentScope} {
		t.Run(scope.Name, func(t *testing.T) {
			store := &traversalTokenStore{row: tables.TempToken{
				ID: "token-123", TokenHash: encrypt.HashSHA256(token),
				Scope: scope.Name, ResourceID: flowID, ExpiresAt: time.Now().Add(time.Hour),
			}}
			am := newTraversalAuthMiddleware()
			am.tempTokensService = temptoken.NewService(store, temptoken.NewRegistry())
			if err := RegisterTempTokenScopes(am.tempTokensService); err != nil {
				t.Fatal(err)
			}
			am.UpdateTempTokenAuthEnabled(true)
			for _, allowed := range scope.AllowedRoutes {
				path := strings.ReplaceAll(allowed.Path, scope.ResourceIDInPath, flowID)
				t.Run(allowed.Method+" "+allowed.Path, func(t *testing.T) {
					t.Run("legitimate", func(t *testing.T) {
						assertTraversalAuthRoute(t, am, allowed.Method, allowed.Path, path+"?source=test", token, 204)
					})
					for _, target := range []string{"/api/providers/{provider}", "/api/plugins/{name}"} {
						for _, slash := range []string{"%2F", "%2f"} {
							for _, dots := range []string{"..", "%2e%2e"} {
								uri := target[:strings.Index(target, "{")] + dots + slash + strings.ReplaceAll(strings.TrimPrefix(path, "/api/"), "/", slash)
								t.Run(uri, func(t *testing.T) {
									assertTraversalAuthRoute(t, am, allowed.Method, target, uri, token, 401)
								})
							}
						}
					}
					t.Run("wrong resource", func(t *testing.T) {
						assertTraversalAuthRoute(t, am, allowed.Method, allowed.Path, strings.ReplaceAll(path, flowID, "other-flow"), token, 401)
					})
					t.Run("wrong method", func(t *testing.T) {
						assertTraversalAuthRoute(t, am, "POST", allowed.Path, path, token, 401)
					})
					t.Run("unknown token", func(t *testing.T) {
						assertTraversalAuthRoute(t, am, allowed.Method, allowed.Path, path, "unknown-token", 401)
					})
					t.Run("expired", func(t *testing.T) {
						expiresAt := store.row.ExpiresAt
						store.row.ExpiresAt = time.Now().Add(-time.Hour)
						defer func() { store.row.ExpiresAt = expiresAt }()
						assertTraversalAuthRoute(t, am, allowed.Method, allowed.Path, path, token, 401)
					})
					t.Run("disabled", func(t *testing.T) {
						am.UpdateTempTokenAuthEnabled(false)
						defer am.UpdateTempTokenAuthEnabled(true)
						assertTraversalAuthRoute(t, am, allowed.Method, allowed.Path, path, token, 401)
					})
				})
			}
		})
	}
}

// The two failure modes must stay distinguishable, and a caller that presented
// no token must learn nothing about whether the feature is on.
func TestAuthMiddleware_TempTokenUnauthorizedCodes(t *testing.T) {
	const token = "test-scoped-token"
	const flowID = "flow-123"
	route := oauth2ConsentScope.AllowedRoutes[0]
	path := strings.ReplaceAll(route.Path, oauth2ConsentScope.ResourceIDInPath, flowID)

	for _, tc := range []struct {
		name     string
		enabled  bool
		token    string
		wantCode string
	}{
		{"feature disabled", false, token, TempTokenAuthDisabledCode},
		{"token rejected", true, "unknown-token", TempTokenRejectedCode},
		{"no token stays opaque", false, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &traversalTokenStore{row: tables.TempToken{
				ID: "token-123", TokenHash: encrypt.HashSHA256(token),
				Scope: oauth2ConsentScope.Name, ResourceID: flowID, ExpiresAt: time.Now().Add(time.Hour),
			}}
			am := newTraversalAuthMiddleware()
			am.tempTokensService = temptoken.NewService(store, temptoken.NewRegistry())
			if err := RegisterTempTokenScopes(am.tempTokensService); err != nil {
				t.Fatal(err)
			}
			am.UpdateTempTokenAuthEnabled(tc.enabled)

			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod(route.Method)
			ctx.Request.SetRequestURI(path)
			if tc.token != "" {
				ctx.Request.Header.Set("X-Bifrost-Temp-Token", tc.token)
			}
			am.APIMiddleware()(func(*fasthttp.RequestCtx) {
				t.Fatal("protected handler must not run")
			})(ctx)

			if got := ctx.Response.StatusCode(); got != fasthttp.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", got)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(ctx.Response.Body(), &body); err != nil {
				t.Fatalf("decode body %q: %v", ctx.Response.Body(), err)
			}
			if body.Error.Code != tc.wantCode {
				t.Fatalf("expected code %q, got %q", tc.wantCode, body.Error.Code)
			}
		})
	}
}

// TestAuthMiddleware_WhitelistedRoutes tests that whitelisted routes bypass auth
func TestAuthMiddleware_WhitelistedRoutes(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})

	whitelistedRoutes := []string{
		"/api/session/is-auth-enabled",
		"/api/session/login",
		// Logout is idempotent (clears the cookie, revokes the session if a
		// token is present) and must never 401, or a repeat logout cascades
		// into a redirect loop in the dashboard.
		"/api/session/logout",
		"/api/oauth/callback",
		// Client ID Metadata Document
		"/api/oauth/client-metadata.json",
		"/health",
	}

	for _, route := range whitelistedRoutes {
		t.Run(route, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(route)

			nextCalled := false
			bypassMarked := false
			next := func(ctx *fasthttp.RequestCtx) {
				nextCalled = true
				bypassMarked, _ = ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool)
			}

			middleware := am.APIMiddleware()
			handler := middleware(next)
			handler(ctx)

			if !nextCalled {
				t.Errorf("Next handler should be called for whitelisted route %s", route)
			}
			// A whitelisted request reaches its handler with no credential checked, so
			// handlers that gate on genuine auth (proxy config, dial targets) must see
			// it as bypassed rather than as an authenticated admin.
			if !bypassMarked {
				t.Errorf("whitelisted route %s must be marked auth-bypassed", route)
			}
		})
	}
}

// TestAuthMiddleware_APIMiddleware_DevPrefixDoesNotMatchDevices guards against the
// prefix-matching bug where the "/api/dev" whitelist prefix (intended for the dev pprof
// routes under "/api/dev/pprof") also matched "/api/devices", silently bypassing auth on
// the edge-control devices route. With the trailing-slash fix, "/api/dev/pprof" must still
// bypass auth while "/api/devices" must NOT.
func TestAuthMiddleware_APIMiddleware_DevPrefixDoesNotMatchDevices(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})

	cases := []struct {
		name           string
		uri            string
		wantNextCalled bool // true => route is whitelisted (auth bypassed)
	}{
		{name: "dev pprof is whitelisted", uri: "/api/dev/pprof", wantNextCalled: true},
		{name: "dev pprof subpath is whitelisted", uri: "/api/dev/pprof/goroutines", wantNextCalled: true},
		{name: "devices is NOT whitelisted", uri: "/api/devices?limit=25&offset=0", wantNextCalled: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(tc.uri)

			nextCalled := false
			next := func(ctx *fasthttp.RequestCtx) { nextCalled = true }

			am.APIMiddleware()(next)(ctx)

			if nextCalled != tc.wantNextCalled {
				t.Fatalf("route %q: nextCalled = %v, want %v (status %d)", tc.uri, nextCalled, tc.wantNextCalled, ctx.Response.StatusCode())
			}
			// A non-whitelisted route with no credentials must be rejected, not passed through.
			if !tc.wantNextCalled && ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
				t.Fatalf("route %q: expected 401 for unauthenticated non-whitelisted route, got %d", tc.uri, ctx.Response.StatusCode())
			}
		})
	}
}

func TestAuthMiddleware_InferenceMiddleware_RealtimeTransportBypassesAuth(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})
	routes := []string{
		"/v1/realtime",
		"/openai/v1/realtime",
		"/v1/realtime/calls?model=gpt-realtime",
		"/openai/v1/realtime/calls?model=gpt-realtime",
	}

	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(route)

			nextCalled := false
			next := func(ctx *fasthttp.RequestCtx) {
				nextCalled = true
			}

			handler := am.InferenceMiddleware()(next)
			handler(ctx)

			if !nextCalled {
				t.Fatalf("expected realtime transport route %s to bypass auth", route)
			}
		})
	}
}

// TestAuthMiddleware_InferenceMiddleware_DelegatesAuthToGovernance verifies that the
// inference middleware passes every inference request through — including realtime minting
// endpoints and credential-less requests — even with dashboard auth enabled. Inference
// authentication is owned by the governance plugin downstream (the authoritative VK
// validator), not by this dashboard-auth middleware. Re-introducing a credential check
// here would reject virtual-key callers and break inference auth, so the middleware must
// never short-circuit an inference request.
func TestAuthMiddleware_InferenceMiddleware_DelegatesAuthToGovernance(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})

	cases := []struct {
		name      string
		uri       string
		headerKey string
		headerVal string
	}{
		{name: "chat completion with virtual key", uri: "/v1/chat/completions", headerKey: "x-bf-vk", headerVal: "sk-bf-abc123"},
		{name: "chat completion without credentials", uri: "/v1/chat/completions"},
		{name: "realtime minting (client_secrets)", uri: "/v1/realtime/client_secrets"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(tc.uri)
			if tc.headerKey != "" {
				ctx.Request.Header.Set(tc.headerKey, tc.headerVal)
			}

			nextCalled := false
			next := func(ctx *fasthttp.RequestCtx) { nextCalled = true }

			am.InferenceMiddleware()(next)(ctx)

			if !nextCalled {
				t.Fatalf("expected inference request %q to pass the middleware (governance enforces auth downstream), got %d", tc.name, ctx.Response.StatusCode())
			}
		})
	}
}

// TestAuthMiddleware_APIMiddleware_VirtualKeyDoesNotBypass guards against the privilege-
// escalation loophole: a virtual key must never grant access to admin/dashboard routes.
func TestAuthMiddleware_APIMiddleware_VirtualKeyDoesNotBypass(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/config")
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-abc123")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) { nextCalled = true }

	am.APIMiddleware()(next)(ctx)

	if nextCalled {
		t.Fatal("virtual key must not bypass auth on admin/dashboard routes")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
		t.Fatalf("expected %d for admin route with VK, got %d", fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
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
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
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

// TestAuthMiddleware_BootstrapToken_NoAdminNoToken tests that CheckBootstrapToken fails
// closed when no admin account exists yet and no setup token was configured at boot (e.g.
// the operator hasn't set setup_token / BIFROST_SETUP_TOKEN) — the very first admin account
// cannot be created until the operator configures one.
func TestAuthMiddleware_BootstrapToken_NoAdminNoToken(t *testing.T) {
	am := &AuthMiddleware{}

	if am.CheckBootstrapToken("") {
		t.Error("CheckBootstrapToken should reject when no admin exists and no setup token is configured")
	}
	if am.CheckBootstrapToken("anything") {
		t.Error("CheckBootstrapToken should reject when no admin exists and no setup token is configured")
	}
}

// TestAuthMiddleware_BootstrapToken_AdminExists tests that CheckBootstrapToken allows any
// value through once an admin account already exists, regardless of bootstrapToken state.
func TestAuthMiddleware_BootstrapToken_AdminExists(t *testing.T) {
	am := &AuthMiddleware{}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("password"),
		IsEnabled:     true,
	})

	if !am.CheckBootstrapToken("") {
		t.Error("CheckBootstrapToken should allow through once an admin account exists")
	}
	if !am.CheckBootstrapToken("anything") {
		t.Error("CheckBootstrapToken should allow through once an admin account exists")
	}
}

// TestAuthMiddleware_BootstrapToken_ValidatesAndClears tests that once a bootstrap token is
// set (simulating InitAuthMiddleware booting with a configured setup token and no admin
// account yet), only the exact token validates, and creating the admin account — which sets
// authConfig, mirroring BifrostHTTPServer.UpdateAuthConfig — permanently reopens the gate.
func TestAuthMiddleware_BootstrapToken_ValidatesAndClears(t *testing.T) {
	am := &AuthMiddleware{}
	token := "test-bootstrap-token"
	am.bootstrapToken.Store(&token)

	if am.CheckBootstrapToken("") {
		t.Error("CheckBootstrapToken should reject an empty token once a bootstrap token is set")
	}
	if am.CheckBootstrapToken("wrong-token") {
		t.Error("CheckBootstrapToken should reject a mismatched token")
	}
	if !am.CheckBootstrapToken(token) {
		t.Error("CheckBootstrapToken should accept the exact bootstrap token")
	}

	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("password"),
		IsEnabled:     true,
	})
	am.ClearBootstrapToken()

	if !am.CheckBootstrapToken("wrong-token") {
		t.Error("CheckBootstrapToken should allow anything through once an admin account exists")
	}
}

// runOSSAPIChain runs a request through the OSS /api chain (SetupLockMiddleware then
// APIMiddleware, as wired in server.go) and reports whether the handler ran.
func runOSSAPIChain(am *AuthMiddleware, ctx *fasthttp.RequestCtx) bool {
	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) { nextCalled = true }
	am.SetupLockMiddleware()(am.APIMiddleware()(next))(ctx)
	return nextCalled
}

func newSetupLockedAuthMiddleware(setupToken string, authConfig *configstore.AuthConfig) *AuthMiddleware {
	am := &AuthMiddleware{}
	if setupToken != "" {
		am.setupToken.Store(&setupToken)
	}
	am.UpdateAuthConfig(authConfig)
	return am
}

// TestSetupLockMiddleware_LocksAPIWhileAuthInactive pins the OSS lockout: with no admin
// account, or an admin account with auth disabled, every non-public /api call needs the
// setup token (401 when missing, 403 when wrong or when none is configured).
func TestSetupLockMiddleware_LocksAPIWhileAuthInactive(t *testing.T) {
	SetLogger(&mockLogger{})
	disabled := &configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("password"),
		IsEnabled:     false,
	}
	states := map[string]*configstore.AuthConfig{"no admin": nil, "auth disabled": disabled}
	for name, authConfig := range states {
		t.Run(name, func(t *testing.T) {
			cases := []struct {
				name       string
				setupToken string
				header     string
				wantNext   bool
				wantStatus int
			}{
				{name: "missing header", setupToken: "s3cret", wantStatus: fasthttp.StatusUnauthorized},
				{name: "wrong header", setupToken: "s3cret", header: "nope", wantStatus: fasthttp.StatusForbidden},
				{name: "no token configured", header: "s3cret", wantStatus: fasthttp.StatusForbidden},
				{name: "correct header", setupToken: "s3cret", header: "s3cret", wantNext: true, wantStatus: fasthttp.StatusOK},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					am := newSetupLockedAuthMiddleware(tc.setupToken, authConfig)
					ctx := &fasthttp.RequestCtx{}
					ctx.Request.SetRequestURI("/api/providers")
					if tc.header != "" {
						ctx.Request.Header.Set(SetupTokenHeader, tc.header)
					}
					if got := runOSSAPIChain(am, ctx); got != tc.wantNext {
						t.Fatalf("next called = %v, want %v (status %d, body %s)", got, tc.wantNext, ctx.Response.StatusCode(), ctx.Response.Body())
					}
					if ctx.Response.StatusCode() != tc.wantStatus {
						t.Errorf("status = %d, want %d (body %s)", ctx.Response.StatusCode(), tc.wantStatus, ctx.Response.Body())
					}
					if tc.wantNext {
						if authed, _ := ctx.UserValue(schemas.BifrostContextKeySetupTokenAuthenticated).(bool); !authed {
							t.Error("setup-token request should be marked BifrostContextKeySetupTokenAuthenticated")
						}
						if admin, _ := ctx.UserValue(schemas.IsLocalAdminContextKey).(bool); !admin {
							t.Error("setup-token request should run as the local admin")
						}
						if bypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); bypassed {
							t.Error("setup-token request passed a credential check and must not be marked as an auth bypass")
						}
					}
				})
			}
		})
	}
}

// TestSetupLockMiddleware_PublicRoutesStayOpen pins that the setup screen can bootstrap:
// health, version and is-auth-enabled answer without the setup token while locked.
func TestSetupLockMiddleware_PublicRoutesStayOpen(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, path := range []string{"/health", "/api/version", "/api/session/is-auth-enabled"} {
		am := newSetupLockedAuthMiddleware("s3cret", nil)
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(path)
		if !runOSSAPIChain(am, ctx) {
			t.Errorf("%s should stay public while locked, got %d", path, ctx.Response.StatusCode())
		}
	}
}

// TestSetupLockMiddleware_IgnoredOnceAuthEnabled pins that the setup token stops working
// the moment dashboard auth is enabled; normal session auth applies.
func TestSetupLockMiddleware_IgnoredOnceAuthEnabled(t *testing.T) {
	SetLogger(&mockLogger{})
	am := newSetupLockedAuthMiddleware("s3cret", &configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/providers")
	ctx.Request.Header.Set(SetupTokenHeader, "s3cret")
	if runOSSAPIChain(am, ctx) {
		t.Fatal("setup token must not authenticate once dashboard auth is enabled")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
		t.Errorf("status = %d, want 401", ctx.Response.StatusCode())
	}
}

// TestSetupLockMiddleware_LiftsWhenAdminEnabled pins that saving an enabled admin unlocks
// the API without a restart (UpdateAuthConfig swaps the pointer the gate reads).
func TestSetupLockMiddleware_LiftsWhenAdminEnabled(t *testing.T) {
	SetLogger(&mockLogger{})
	am := newSetupLockedAuthMiddleware("s3cret", nil)
	handler := am.SetupLockMiddleware()(func(ctx *fasthttp.RequestCtx) {})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/providers")
	handler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
		t.Fatalf("locked status = %d, want 401", ctx.Response.StatusCode())
	}
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})
	nextCalled := false
	ctx2 := &fasthttp.RequestCtx{}
	ctx2.Request.SetRequestURI("/api/providers")
	am.SetupLockMiddleware()(func(ctx *fasthttp.RequestCtx) { nextCalled = true })(ctx2)
	if !nextCalled {
		t.Error("gate should hand off to APIMiddleware once dashboard auth is enabled")
	}
}

// TestSetupLockMiddleware_SetupSessionCookie pins the dashboard path: the HttpOnly cookie
// from POST /api/session/setup stands in for the header (WebSocket upgrades included), and a
// tampered, expired, or other-token cookie is refused.
func TestSetupLockMiddleware_SetupSessionCookie(t *testing.T) {
	SetLogger(&mockLogger{})
	am := newSetupLockedAuthMiddleware("s3cret", nil)
	now := time.Now()
	valid, expires, err := am.IssueSetupSession(now)
	if err != nil {
		t.Fatal(err)
	}
	if got := expires.Sub(now); got != SetupSessionTTL {
		t.Errorf("expiry = %v, want %v", got, SetupSessionTTL)
	}
	if strings.Contains(valid, "s3cret") {
		t.Fatal("setup session cookie must not carry the setup token")
	}
	other := newSetupLockedAuthMiddleware("another-token", nil)
	otherCookie, _, err := other.IssueSetupSession(now)
	if err != nil {
		t.Fatal(err)
	}
	expired, _, err := am.IssueSetupSession(now.Add(-SetupSessionTTL - time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tampered := valid[:len(valid)-2] + "xx"
	for _, tc := range []struct {
		name, cookie, header string
		websocket            bool
		wantNext             bool
		wantStatus           int
	}{
		{name: "valid cookie", cookie: valid, wantNext: true, wantStatus: fasthttp.StatusOK},
		{name: "valid cookie on websocket upgrade", cookie: valid, websocket: true, wantNext: true, wantStatus: fasthttp.StatusOK},
		{name: "tampered cookie", cookie: tampered, wantStatus: fasthttp.StatusUnauthorized},
		{name: "expired cookie", cookie: expired, wantStatus: fasthttp.StatusUnauthorized},
		{name: "cookie signed under another token", cookie: otherCookie, wantStatus: fasthttp.StatusUnauthorized},
		{name: "garbage cookie", cookie: "v1.notanumber.zz.zz", wantStatus: fasthttp.StatusUnauthorized},
		{name: "wrong header beats a valid cookie", cookie: valid, header: "nope", wantStatus: fasthttp.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/api/providers")
			ctx.Request.Header.SetCookie(SetupSessionCookie, tc.cookie)
			if tc.header != "" {
				ctx.Request.Header.Set(SetupTokenHeader, tc.header)
			}
			if tc.websocket {
				ctx.Request.Header.Set("Upgrade", "websocket")
			}
			if got := runOSSAPIChain(am, ctx); got != tc.wantNext {
				t.Fatalf("next called = %v, want %v (status %d, body %s)", got, tc.wantNext, ctx.Response.StatusCode(), ctx.Response.Body())
			}
			if ctx.Response.StatusCode() != tc.wantStatus {
				t.Errorf("status = %d, want %d", ctx.Response.StatusCode(), tc.wantStatus)
			}
			if tc.wantNext {
				if bypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); bypassed {
					t.Error("cookie-authenticated request must not be marked as an auth bypass")
				}
			}
		})
	}

	// Once dashboard auth is on, the cookie is no credential: APIMiddleware wants a session.
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
		IsEnabled:     true,
	})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/providers")
	ctx.Request.Header.SetCookie(SetupSessionCookie, valid)
	if runOSSAPIChain(am, ctx) || ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
		t.Errorf("setup session cookie must not authenticate once dashboard auth is enabled, got %d", ctx.Response.StatusCode())
	}
}

// TestSetupLockMiddleware_InferenceUnaffected pins that the gate is API-only: inference
// keeps delegating to governance (enforce_auth_on_inference) while dashboard auth is off.
func TestSetupLockMiddleware_InferenceUnaffected(t *testing.T) {
	SetLogger(&mockLogger{})
	am := newSetupLockedAuthMiddleware("s3cret", nil)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/chat/completions")
	nextCalled := false
	am.InferenceMiddleware()(func(ctx *fasthttp.RequestCtx) { nextCalled = true })(ctx)
	if !nextCalled {
		t.Error("InferenceMiddleware must not require the setup token")
	}
}

// TestAuthMiddleware_UpdateAuthConfig_EnabledToDisabled tests disabling auth after it was enabled
func TestAuthMiddleware_UpdateAuthConfig_EnabledToDisabled(t *testing.T) {
	SetLogger(&mockLogger{})

	am := &AuthMiddleware{}
	// Start with auth enabled
	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
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
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("hashedpassword"),
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
		ClientConfig: &configstore.ClientConfig{
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

	middleware := NewCorsMiddleware(config).Middleware()
	handler := middleware(next)
	handler(ctx)

	// Check default headers are set
	expectedHeaders := "Content-Type, Authorization, X-Requested-With, X-Stainless-Timeout, X-Api-Key, X-OpenAI-Agents-SDK, X-Operation-ID, X-Bifrost-View-As, X-Bifrost-Setup-Token, MCP-Protocol-Version, Mcp-Session-Id, Mcp-Method, Mcp-Name, Last-Event-ID"
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))
	if actualHeaders != expectedHeaders {
		t.Errorf("Expected Access-Control-Allow-Headers to be %s, got %s", expectedHeaders, actualHeaders)
	}

	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_WildcardHeaders_NonCredentialed tests that wildcard allowed headers
// sets Access-Control-Allow-Headers to * for non-credentialed requests (wildcard origins).
func TestCorsMiddleware_WildcardHeaders_NonCredentialed(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			AllowedOrigins: []string{"*"},
			AllowedHeaders: []string{"*"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Origin", "https://example.com")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := NewCorsMiddleware(config).Middleware()
	handler := middleware(next)
	handler(ctx)

	// Non-credentialed: the wildcard is valid per spec, but never covers Authorization, so the
	// defaults stay listed beside it.
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))
	if actualHeaders != corsAllowHeaders(nil)+", *" {
		t.Errorf("Expected Access-Control-Allow-Headers to be the defaults plus *, got %s", actualHeaders)
	}

	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_WildcardHeaders_NonCredentialedPreflightAllowsAuthorization pins that a browser
// client on a wildcard-allowed origin can send a bearer token: the Fetch standard never lets "*"
// authorize the Authorization request header, so it must be listed explicitly.
func TestCorsMiddleware_WildcardHeaders_NonCredentialedPreflightAllowsAuthorization(t *testing.T) {
	SetLogger(&mockLogger{})
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			AllowedOrigins: []string{"*"},
			AllowedHeaders: []string{"*"},
		},
	}
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodOptions)
	ctx.Request.Header.Set("Origin", "https://inspector.example.com")
	ctx.Request.Header.Set("Access-Control-Request-Method", fasthttp.MethodPost)
	ctx.Request.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	NewCorsMiddleware(config).Middleware()(func(*fasthttp.RequestCtx) {})(ctx)

	allowed := strings.Split(string(ctx.Response.Header.Peek("Access-Control-Allow-Headers")), ", ")
	if !slices.Contains(allowed, "Authorization") {
		t.Errorf("Expected Authorization to be listed explicitly, got %v", allowed)
	}
	if !slices.Contains(allowed, "*") {
		t.Errorf("Expected the wildcard to still allow other headers, got %v", allowed)
	}
	if len(ctx.Response.Header.Peek("Access-Control-Allow-Credentials")) != 0 {
		t.Error("A wildcard-matched origin must not get credentialed CORS")
	}
}

// TestCorsMiddleware_WildcardHeaders_CredentialedPreflight tests that wildcard allowed headers
// reflects Access-Control-Request-Headers for credentialed preflight requests instead of sending
// the literal *, which browsers don't treat as a wildcard when credentials are present.
func TestCorsMiddleware_WildcardHeaders_CredentialedPreflight(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			AllowedOrigins: []string{"https://example.com"},
			AllowedHeaders: []string{"*"},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("OPTIONS")
	ctx.Request.Header.Set("Origin", "https://example.com")
	ctx.Request.Header.Set("Access-Control-Request-Headers", "Authorization, X-Custom-Header")

	next := func(ctx *fasthttp.RequestCtx) {
		t.Error("Next handler should not be called for preflight")
	}

	middleware := NewCorsMiddleware(config).Middleware()
	handler := middleware(next)
	handler(ctx)

	// Credentialed preflight: should reflect requested headers, not *
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))
	if actualHeaders != "Authorization, X-Custom-Header" {
		t.Errorf("Expected Access-Control-Allow-Headers to reflect requested headers, got %s", actualHeaders)
	}

	// Should also have credentials
	creds := string(ctx.Response.Header.Peek("Access-Control-Allow-Credentials"))
	if creds != "true" {
		t.Errorf("Expected Access-Control-Allow-Credentials to be true, got %s", creds)
	}
}

// TestCorsMiddleware_WildcardHeaders_CredentialedNonPreflight tests that wildcard allowed headers
// uses defaults for credentialed non-preflight requests (no Access-Control-Request-Headers).
func TestCorsMiddleware_WildcardHeaders_CredentialedNonPreflight(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
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

	middleware := NewCorsMiddleware(config).Middleware()
	handler := middleware(next)
	handler(ctx)

	// Credentialed non-preflight: should use defaults (not *)
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))
	defaultHeaders := []string{"Content-Type", "Authorization", "X-Requested-With", "X-Stainless-Timeout", "X-Api-Key"}
	for _, header := range defaultHeaders {
		if !containsHeader(actualHeaders, header) {
			t.Errorf("Expected Access-Control-Allow-Headers to contain %s, got %s", header, actualHeaders)
		}
	}
	if actualHeaders == "*" {
		t.Error("Expected Access-Control-Allow-Headers to NOT be * for credentialed requests")
	}

	if !nextCalled {
		t.Error("Next handler was not called")
	}
}

// TestCorsMiddleware_CustomHeaders tests that custom allowed headers are appended to defaults
func TestCorsMiddleware_CustomHeaders(t *testing.T) {
	SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
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

	middleware := NewCorsMiddleware(config).Middleware()
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
		ClientConfig: &configstore.ClientConfig{
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

	middleware := NewCorsMiddleware(config).Middleware()
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
		ClientConfig: &configstore.ClientConfig{
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

	middleware := NewCorsMiddleware(config).Middleware()
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
		ClientConfig: &configstore.ClientConfig{
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

	middleware := NewCorsMiddleware(config).Middleware()
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
		ClientConfig: &configstore.ClientConfig{
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
		ClientConfig: &configstore.ClientConfig{
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
		ClientConfig: &configstore.ClientConfig{
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
	// The wording is fasthttp's, wrapped by the middleware with %v, so match it
	// case-insensitively rather than pinning an upstream string. fasthttp changed
	// it from "unsupported Content-Encoding: snappy" to
	// `unsupported content-encoding: "snappy"`; what this test cares about is that
	// the unsupported encoding is reported, not how upstream capitalises it.
	if bifrostErr.Error == nil ||
		!strings.Contains(strings.ToLower(bifrostErr.Error.Message), "unsupported content-encoding") {
		t.Fatalf("unexpected error message: %#v", bifrostErr.Error)
	}
}

func TestRequestDecompressionMiddleware_DecompressedSizeLimit(t *testing.T) {
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
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

// TestRequestDecompressionMiddleware_ZstdOversizedWindowRejected reproduces
// an oversized-window frame end to end through the actual middleware: a
// ~9-byte zstd frame whose header declares a 512 MiB window, decoding to zero
// bytes. RequestDecompressionMiddleware runs before routing and auth, so
// without a bound on the decoder, this pre-allocates ~512 MiB per request
// regardless of MaxRequestBodySizeMB - that limit only bounds decompressed
// OUTPUT via io.LimitedReader, which never sees a byte here. The request must
// be rejected, not merely produce a small/empty body.
func TestRequestDecompressionMiddleware_ZstdOversizedWindowRejected(t *testing.T) {
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			MaxRequestBodySizeMB: 100,
		},
	}

	// zstd magic (28 b5 2f fd) + Frame_Header_Descriptor (00) +
	// Window_Descriptor (98 -> 512 MiB window) + empty last raw block (01 00 00).
	oversizedFrame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x98, 0x01, 0x00, 0x00}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "zstd")
	ctx.Request.SetBodyRaw(oversizedFrame)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	handler := RequestDecompressionMiddleware(config)(next)
	handler(ctx)

	if nextCalled {
		t.Fatal("next handler should not be called for an oversized-window zstd frame")
	}
	// A declared window beyond the decoder bound is a payload-too-large condition, not a
	// malformed body, so it is reported as 413 (see IsDecompressionSizeLimitError).
	if ctx.Response.StatusCode() != fasthttp.StatusRequestEntityTooLarge {
		t.Fatalf("expected status 413, got %d", ctx.Response.StatusCode())
	}
}

func TestRequestDecompressionMiddleware_EmptyBodyWithContentEncoding(t *testing.T) {
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
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
		ClientConfig: &configstore.ClientConfig{
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
		ClientConfig: &configstore.ClientConfig{
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

// compressedCodecs are the request encodings the middleware accepts, with a test encoder each.
var compressedCodecs = []struct {
	name     string
	encoding string
	encode   func([]byte) ([]byte, error)
}{
	{name: "gzip", encoding: "gzip", encode: gzipCompress},
	{name: "deflate", encoding: "deflate", encode: deflateCompress},
	{name: "brotli", encoding: "br", encode: brotliCompress},
	{name: "zstd", encoding: "zstd", encode: zstdCompress},
}

// incompressibleBytes returns n bytes no codec shrinks, from a fixed xorshift sequence.
func incompressibleBytes(n int) []byte {
	out := make([]byte, n)
	x := uint64(0x9E3779B97F4A7C15)
	for i := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[i] = byte(x)
	}
	return out
}

// runDecompression sends one compressed body through the middleware and reports whether the
// next handler ran.
func runDecompression(t *testing.T, config *lib.Config, encoding string, compressed []byte) (*fasthttp.RequestCtx, bool) {
	t.Helper()
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", encoding)
	ctx.Request.SetBodyRaw(compressed)
	nextCalled := false
	RequestDecompressionMiddleware(config)(func(*fasthttp.RequestCtx) { nextCalled = true })(ctx)
	return ctx, nextCalled
}

// TestRequestDecompressionMiddleware_ExpansionBound pins that what a compressed body may expand
// to is bounded by what was sent, not only by the absolute cap: the middleware runs before
// routing and auth, so the cap alone lets a few hundred unauthenticated bytes become 100 MiB of
// heap. A body may grow to at most 100 times its compressed size, with a 1 MiB floor so small
// bodies are never refused, and never past the cap. Bodies within those bounds are untouched.
func TestRequestDecompressionMiddleware_ExpansionBound(t *testing.T) {
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{MaxRequestBodySizeMB: 100}}

	// A compressed length whose expansion would overflow int is still allowed the cap, not wrapped
	// to a negative product and clamped to the floor. On a 32-bit build that is a 22 MiB body.
	const capBytes = 100 << 20
	if got := decompressionAllowance(math.MaxInt/requestDecompressionMaxRatio+1, capBytes); got != capBytes {
		t.Fatalf("a length whose expansion overflows int must be allowed the cap %d, got %d", capBytes, got)
	}
	if got := decompressionAllowance(math.MaxInt, capBytes); got != capBytes {
		t.Fatalf("the largest length must be allowed the cap %d, got %d", capBytes, got)
	}

	bomb := make([]byte, 2<<20) // 2 MiB of zeros: a few hundred bytes on the wire
	for _, codec := range compressedCodecs {
		t.Run(codec.name+" bomb", func(t *testing.T) {
			compressed, err := codec.encode(bomb)
			if err != nil {
				t.Fatal(err)
			}
			if len(compressed) > 8<<10 {
				t.Fatalf("test bomb is %d bytes compressed, expected a few hundred", len(compressed))
			}
			ctx, nextCalled := runDecompression(t, config, codec.encoding, compressed)
			if nextCalled {
				t.Fatalf("a %d-byte body expanding to %d bytes must be refused before the router", len(compressed), len(bomb))
			}
			if ctx.Response.StatusCode() != fasthttp.StatusRequestEntityTooLarge {
				t.Fatalf("expected 413, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
			if !strings.Contains(string(ctx.Response.Body()), "expansion") {
				t.Fatalf("the refusal must name the expansion bound, got %s", ctx.Response.Body())
			}
		})
	}

	// Under the floor, compressibility does not matter.
	small := make([]byte, 512<<10)
	compressed, err := gzipCompress(small)
	if err != nil {
		t.Fatal(err)
	}
	if ctx, nextCalled := runDecompression(t, config, "gzip", compressed); !nextCalled {
		t.Fatalf("a body under the floor must pass however well it compresses: %d %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	// Above the floor, a body within the ratio passes.
	within := incompressibleBytes(2 << 20)
	compressed, err = gzipCompress(within)
	if err != nil {
		t.Fatal(err)
	}
	if ctx, nextCalled := runDecompression(t, config, "gzip", compressed); !nextCalled {
		t.Fatalf("a body within the ratio must pass: %d %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

// TestRequestDecompressionMiddleware_InFlightBudget pins that buffered decompressions cannot
// stack up without bound: the bytes each one inflates are charged to a budget of four times the
// cap for the life of the request, and a request that cannot be charged is answered 503 rather
// than given another buffer.
func TestRequestDecompressionMiddleware_InFlightBudget(t *testing.T) {
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{MaxRequestBodySizeMB: 1}}
	compressed, err := gzipCompress(make([]byte, 1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	handler := RequestDecompressionMiddleware(config)(func(*fasthttp.RequestCtx) {
		entered <- struct{}{}
		<-release
	})
	send := func() *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod("POST")
		ctx.Request.Header.Set("Content-Encoding", "gzip")
		ctx.Request.SetBodyRaw(compressed)
		handler(ctx)
		return ctx
	}

	// Each body inflates to the whole 1 MiB cap, so four fill the 4 MiB budget.
	done := make(chan struct{})
	for range 4 {
		go func() {
			send()
			done <- struct{}{}
		}()
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the first four requests must be admitted")
		}
	}
	fifth := make(chan *fasthttp.RequestCtx, 1)
	go func() { fifth <- send() }()
	select {
	case ctx := <-fifth:
		if ctx.Response.StatusCode() != fasthttp.StatusServiceUnavailable {
			t.Fatalf("the fifth request must be refused with 503 while the budget is held, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
		}
	case <-entered:
		close(release)
		t.Fatal("the fifth request was admitted while four held the whole budget")
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the fifth request neither returned nor entered the handler")
	}
	close(release)
	for range 4 {
		<-done
	}
	// The reservations end with their requests.
	release = make(chan struct{})
	close(release)
	if ctx := send(); ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("a request after the others returned must be admitted, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

// sdkLikeJSONBody builds a chat request of about inflatedLen bytes whose content mixes prose with
// base64 noise, so it gzips at a ratio typical of real SDK payloads (single digits) rather than
// the hundreds that runs of one byte reach.
func sdkLikeJSONBody(t testing.TB, inflatedLen int) []byte {
	t.Helper()
	noise := make([]byte, 150)
	prose := strings.Repeat("The quarterly report shows revenue growth across every region. ", 13)
	var content strings.Builder
	for content.Len() < inflatedLen {
		if _, err := cryptoRand.Read(noise); err != nil {
			t.Fatal(err)
		}
		content.WriteString(base64.StdEncoding.EncodeToString(noise))
		content.WriteString(prose)
	}
	return []byte(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"` + content.String() + `"}]}`)
}

// TestRequestDecompressionMiddleware_ConcurrentMultiMBGzipNotRefused pins that ordinary gzipped
// SDK bodies are not refused just because several are in flight at once. Each body is past
// cap/100 compressed, so its expansion allowance is the whole cap; the in-flight budget must be
// charged for what the bodies really inflate to, not for that allowance, or the fifth concurrent
// request is answered 503 while the process holds a fraction of the budget.
func TestRequestDecompressionMiddleware_ConcurrentMultiMBGzipNotRefused(t *testing.T) {
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{MaxRequestBodySizeMB: 10}}
	plain := sdkLikeJSONBody(t, 2*1024*1024)
	compressed, err := gzipCompress(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) <= requestBodyCap(config)/requestDecompressionMaxRatio {
		t.Fatalf("fixture must be past cap/%d compressed to exercise a full-cap allowance, got %d bytes", requestDecompressionMaxRatio, len(compressed))
	}

	const concurrent = 8
	entered := make(chan struct{}, concurrent)
	release := make(chan struct{})
	handler := RequestDecompressionMiddleware(config)(func(ctx *fasthttp.RequestCtx) {
		if !bytes.Equal(ctx.Request.Body(), plain) {
			t.Errorf("decompressed body differs from what was sent: got %d bytes, want %d", len(ctx.Request.Body()), len(plain))
		}
		entered <- struct{}{}
		<-release
	})
	results := make(chan *fasthttp.RequestCtx, concurrent)
	for range concurrent {
		go func() {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod("POST")
			ctx.Request.Header.Set("Content-Encoding", "gzip")
			ctx.Request.SetBodyRaw(compressed)
			handler(ctx)
			results <- ctx
		}()
	}
	for i := range concurrent {
		select {
		case <-entered:
		case ctx := <-results:
			close(release)
			t.Fatalf("after %d of %d concurrent %d-byte gzip bodies (%d bytes inflated each, %d MiB cap) were admitted, one was refused with %d: %s",
				i, concurrent, len(compressed), len(plain), config.ClientConfig.MaxRequestBodySizeMB, ctx.Response.StatusCode(), ctx.Response.Body())
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatalf("only %d of %d concurrent requests reached the handler", i, concurrent)
		}
	}
	close(release)
	for range concurrent {
		if ctx := <-results; ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
		}
	}
}

// TestRequestDecompressionMiddleware_BudgetReleasedOnFailedDecode pins that a decompression that
// fails part way, past the expansion bound or on a corrupt stream, gives back whatever budget it
// had charged: afterwards requests that need the whole budget at once are all admitted.
func TestRequestDecompressionMiddleware_BudgetReleasedOnFailedDecode(t *testing.T) {
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{MaxRequestBodySizeMB: 1}}
	full, err := gzipCompress(make([]byte, 1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	bomb, err := gzipCompress(make([]byte, 2*1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	truncated := full[:len(full)/2]

	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	hold := false
	handler := RequestDecompressionMiddleware(config)(func(*fasthttp.RequestCtx) {
		if hold {
			entered <- struct{}{}
			<-release
		}
	})
	send := func(body []byte) *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod("POST")
		ctx.Request.Header.Set("Content-Encoding", "gzip")
		ctx.Request.SetBodyRaw(body)
		handler(ctx)
		return ctx
	}
	for range 4 {
		if ctx := send(bomb); ctx.Response.StatusCode() != fasthttp.StatusRequestEntityTooLarge {
			t.Fatalf("bomb: expected 413, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
		}
		if ctx := send(truncated); ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
			t.Fatalf("truncated gzip: expected 400, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
		}
	}

	// Four 1 MiB bodies need the whole 4 MiB budget; any leak above would refuse one of them.
	hold = true
	results := make(chan *fasthttp.RequestCtx, 4)
	for range 4 {
		go func() { results <- send(full) }()
	}
	for range 4 {
		select {
		case <-entered:
		case ctx := <-results:
			close(release)
			t.Fatalf("budget leaked by failed decodes: a 1 MiB body was refused with %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("the four full-size requests did not reach the handler")
		}
	}
	close(release)
	for range 4 {
		<-results
	}
}

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
		ClientConfig: &configstore.ClientConfig{
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
		ClientConfig: &configstore.ClientConfig{
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

// TestRequestDecompressionMiddleware_StreamingPath_ReleasedOnPanic asserts the pooled
// streaming decompressor is released even when the handler chain panics and
// RecoveryMiddleware recovers it. zstd is used because ReleaseZstdDecoder calls
// Reset(nil), which detaches the source: a released decoder can no longer yield
// the body, while an unreleased one still can.
func TestRequestDecompressionMiddleware_StreamingPath_ReleasedOnPanic(t *testing.T) {
	SetLogger(&mockLogger{})
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			MaxRequestBodySizeMB: 100,
		},
	}

	plainBody := []byte(`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`)
	compressedBody, err := zstdCompress(plainBody)
	if err != nil {
		t.Fatalf("failed to encode body: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "zstd")
	// Chunked (-1) triggers the streaming path regardless of compressed size.
	ctx.Request.SetBodyStream(bytes.NewReader(compressedBody), -1)

	var decoder io.Reader
	panicking := func(ctx *fasthttp.RequestCtx) {
		decoder = ctx.RequestBodyStream()
		panic("boom")
	}
	RecoveryMiddleware(newRecoveryTestCors())(RequestDecompressionMiddleware(config)(panicking))(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", ctx.Response.StatusCode(), fasthttp.StatusInternalServerError)
	}
	if decoder == nil {
		t.Fatal("handler did not observe the streaming decompressor")
	}
	if got, _ := io.ReadAll(decoder); bytes.Equal(got, plainBody) {
		t.Error("streaming decompressor was not released after a recovered panic")
	}
}

func TestRequestDecompressionMiddleware_StreamingPath_InvalidBody(t *testing.T) {
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
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
		ClientConfig: &configstore.ClientConfig{
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
		ClientConfig: &configstore.ClientConfig{
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
		ClientConfig: &configstore.ClientConfig{
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

// captureTracePlugin is an ObservabilityPlugin that captures the root span status and
// the root and llm.call span timestamps from a flushed trace. Values are copied
// synchronously inside Inject because the tracer releases the pooled trace once
// Inject returns.
type captureTracePlugin struct {
	done           chan struct{}
	rootStart      time.Time
	rootEnd        time.Time
	rootStatus     schemas.SpanStatus
	rootStatusCode any
	llmEnd         time.Time
	foundLLM       bool
}

func (p *captureTracePlugin) GetName() string { return "capture-trace" }
func (p *captureTracePlugin) Cleanup() error  { return nil }
func (p *captureTracePlugin) Inject(_ context.Context, trace *schemas.Trace) error {
	defer close(p.done)
	if trace == nil || trace.RootSpan == nil {
		return nil
	}
	p.rootStart = trace.RootSpan.StartTime
	p.rootEnd = trace.RootSpan.EndTime
	p.rootStatus = trace.RootSpan.Status
	p.rootStatusCode = trace.RootSpan.Attributes["http.status_code"]
	for _, span := range trace.Spans {
		if span != nil && span.Kind == schemas.SpanKindLLMCall {
			p.llmEnd = span.EndTime
			p.foundLLM = true
		}
	}
	return nil
}

// TestTracingMiddleware_StreamingRootSpanEndsAfterLLMSpan asserts that for a deferred
// (streaming) request the root HTTP span is ended by the trace completer — after the
// stream drains — rather than at handler return. Before the fix the middleware ended
// the root span in its defer, so the root closed before the deferred llm.call span,
// making the child appear longer than its parent in trace viewers.
func TestTracingMiddleware_StreamingRootSpanEndsAfterLLMSpan(t *testing.T) {
	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()

	plugin := &captureTracePlugin{done: make(chan struct{})}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, nil)

	tm := NewTracingMiddleware(tracer)

	var traceID, rootSpanID string
	var completer func([]schemas.PluginLogEntry)
	next := func(ctx *fasthttp.RequestCtx) {
		traceID, _ = ctx.UserValue(schemas.BifrostContextKeyTraceID).(string)
		rootSpanID, _ = ctx.UserValue(schemas.BifrostContextKeySpanID).(string)
		completer, _ = ctx.UserValue(schemas.BifrostContextKeyTraceCompleter).(func([]schemas.PluginLogEntry))
		// Mark this as a deferred (streaming) request so the middleware leaves the
		// root span open for the completer to end.
		ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/openai/v1/chat/completions")
	ctx.Request.Header.SetMethod("POST")
	// Runs setup, next, and the middleware defer (which must NOT end the root span
	// because the request is deferred).
	tm.Middleware()(next)(ctx)

	if traceID == "" {
		t.Fatal("middleware did not set a trace ID")
	}
	if completer == nil {
		t.Fatal("middleware did not set a trace completer")
	}

	// Simulate the provider goroutine: the deferred llm.call span ends mid-stream...
	goCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	goCtx = context.WithValue(goCtx, schemas.BifrostContextKeySpanID, rootSpanID)
	_, llmHandle := tracer.StartSpan(goCtx, "chat test-model", schemas.SpanKindLLMCall)
	time.Sleep(10 * time.Millisecond)
	tracer.EndSpan(llmHandle, schemas.SpanStatusOk, "")

	// ...and the trace completer fires after the stream fully drains.
	completer(nil)

	select {
	case <-plugin.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for trace injection")
	}

	if !plugin.foundLLM {
		t.Fatal("llm.call span not found in flushed trace")
	}
	if plugin.rootEnd.IsZero() {
		t.Fatal("root span EndTime is zero — it was never ended")
	}
	if plugin.rootEnd.Before(plugin.llmEnd) {
		t.Fatalf("root span ended before llm.call span: root.EndTime=%v, llm.EndTime=%v", plugin.rootEnd, plugin.llmEnd)
	}
	if !plugin.rootEnd.After(plugin.rootStart) {
		t.Fatalf("root span has non-positive duration: start=%v, end=%v", plugin.rootStart, plugin.rootEnd)
	}
}

func TestCollectDimensionHeaders(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("X-BF-Dim-Environment", "prod")
	ctx.Request.Header.Set("x-bf-dim-team", "ml")
	ctx.Request.Header.Set("x-bf-dim-", "ignored")  // empty dimension name
	ctx.Request.Header.Set("x-request-id", "req-1") // non-dimension header

	dims := collectDimensionHeaders(ctx)
	if len(dims) != 2 {
		t.Fatalf("collectDimensionHeaders() = %v, want 2 entries", dims)
	}
	if dims["environment"] != "prod" || dims["team"] != "ml" {
		t.Errorf("collectDimensionHeaders() = %v, want environment=prod team=ml", dims)
	}

	if got := collectDimensionHeaders(&fasthttp.RequestCtx{}); got != nil {
		t.Errorf("collectDimensionHeaders(no dims) = %v, want nil", got)
	}
	if got := collectDimensionHeaders(nil); got != nil {
		t.Errorf("collectDimensionHeaders(nil) = %v, want nil", got)
	}
}

// TestTracingMiddleware_SetsCorrelationHeaders asserts that every traced response
// carries x-bifrost-request-id, x-bifrost-trace-id and the deprecated x-request-id so
// callers can pivot a request into its logs and trace in Grafana/Tempo/Loki (BF-1041).
func TestTracingMiddleware_SetsCorrelationHeaders(t *testing.T) {
	SetLogger(&mockLogger{})

	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()
	mw := NewTracingMiddleware(tracer).Middleware()

	newCtx := func() *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/openai/v1/chat/completions")
		ctx.Request.Header.SetMethod("POST")
		return ctx
	}

	t.Run("generates request id when absent", func(t *testing.T) {
		ctx := newCtx()
		mw(func(*fasthttp.RequestCtx) {})(ctx)

		if got := string(ctx.Response.Header.Peek("x-bifrost-trace-id")); got == "" {
			t.Error("expected x-bifrost-trace-id response header to be set")
		}
		got := string(ctx.Response.Header.Peek("x-bifrost-request-id"))
		if got == "" {
			t.Error("expected x-bifrost-request-id response header to be set")
		}
		if legacy := string(ctx.Response.Header.Peek("x-request-id")); legacy != got {
			t.Errorf("x-request-id = %q, want the generated request id %q", legacy, got)
		}
	})

	t.Run("echoes caller-supplied request id", func(t *testing.T) {
		ctx := newCtx()
		ctx.Request.Header.Set("x-request-id", "req-abc-123")
		mw(func(*fasthttp.RequestCtx) {})(ctx)

		if got := string(ctx.Response.Header.Peek("x-bifrost-request-id")); got != "req-abc-123" {
			t.Errorf("x-bifrost-request-id = %q, want req-abc-123", got)
		}
		if got := string(ctx.Response.Header.Peek("x-request-id")); got != "req-abc-123" {
			t.Errorf("x-request-id = %q, want req-abc-123", got)
		}
		if got := string(ctx.Response.Header.Peek("x-bifrost-trace-id")); got == "" {
			t.Error("expected x-bifrost-trace-id response header to be set")
		}
	})

	// An upstream that answers with its own x-request-id (OpenAI does) takes over the deprecated
	// header, as it always has. x-bifrost-request-id and x-bifrost-trace-id stay this gateway's,
	// even against a chained upstream Bifrost that sends its own.
	t.Run("provider headers never replace the gateway's correlation headers", func(t *testing.T) {
		ctx := newCtx()
		ctx.Request.Header.Set("x-request-id", "req-abc-123")
		mw(func(c *fasthttp.RequestCtx) {
			lib.ForwardProviderResponseHeaders(c, map[string]string{
				"X-Request-Id":         "req_upstream",
				"X-Bifrost-Request-Id": "upstream-hop",
				"X-Bifrost-Trace-Id":   "upstream-trace",
			})
		})(ctx)

		if got := string(ctx.Response.Header.Peek("x-bifrost-request-id")); got != "req-abc-123" {
			t.Errorf("x-bifrost-request-id = %q, want req-abc-123", got)
		}
		if got := string(ctx.Response.Header.Peek("x-bifrost-trace-id")); got == "" || got == "upstream-trace" {
			t.Errorf("x-bifrost-trace-id = %q, want this gateway's trace id", got)
		}
		if got := string(ctx.Response.Header.Peek("x-request-id")); got != "req_upstream" {
			t.Errorf("x-request-id = %q, want the provider's req_upstream", got)
		}
	})

	t.Run("headers survive the error path", func(t *testing.T) {
		ctx := newCtx()
		mw(func(c *fasthttp.RequestCtx) {
			SendError(c, fasthttp.StatusBadGateway, "boom")
		})(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusBadGateway {
			t.Fatalf("status = %d, want %d", ctx.Response.StatusCode(), fasthttp.StatusBadGateway)
		}
		if got := string(ctx.Response.Header.Peek("x-bifrost-trace-id")); got == "" {
			t.Error("expected x-bifrost-trace-id to survive the error path")
		}
		if got := string(ctx.Response.Header.Peek("x-request-id")); got == "" {
			t.Error("expected x-request-id to survive the error path")
		}
		if got := string(ctx.Response.Header.Peek("x-bifrost-request-id")); got == "" {
			t.Error("expected x-bifrost-request-id to survive the error path")
		}
	})
}

// captureLogEvent records the structured string and int fields emitted on the access
// log so a test can assert which correlation keys and status were written.
type captureLogEvent struct {
	strFields map[string]string
	intFields map[string]int
}

func (c *captureLogEvent) Str(key, val string) schemas.LogEventBuilder {
	c.strFields[key] = val
	return c
}
func (c *captureLogEvent) Int(key string, val int) schemas.LogEventBuilder {
	c.intFields[key] = val
	return c
}
func (c *captureLogEvent) Int64(string, int64) schemas.LogEventBuilder { return c }
func (c *captureLogEvent) Send()                                       {}

type captureLogger struct {
	mockLogger
	events []*captureLogEvent
	errors []string
}

func (l *captureLogger) Error(format string, args ...any) {
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}

func (l *captureLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	e := &captureLogEvent{strFields: map[string]string{}, intFields: map[string]int{}}
	l.events = append(l.events, e)
	return e
}

// TestTracingMiddleware_AccessLogIncludesRequestID asserts the stdout access log
// carries both trace_id and request_id, so Loki can index on either (BF-1041).
func TestTracingMiddleware_AccessLogIncludesRequestID(t *testing.T) {
	logger := &captureLogger{}
	SetLogger(logger)
	defer SetLogger(&mockLogger{})

	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			AllowedOrigins: []string{},
		},
	}
	cors := NewCorsMiddleware(config).Middleware()

	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()
	tm := NewTracingMiddleware(tracer).Middleware()

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/openai/v1/chat/completions")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("x-request-id", "req-xyz")

	// CORS owns the access-log defer and wraps TracingMiddleware, so the trace_id
	// UserValue and x-request-id header set by tracing are visible when it runs.
	cors(tm(func(*fasthttp.RequestCtx) {}))(ctx)

	if len(logger.events) != 1 {
		t.Fatalf("access log events = %d, want 1", len(logger.events))
	}
	fields := logger.events[0].strFields
	if got := fields["request_id"]; got != "req-xyz" {
		t.Errorf("access log request_id = %q, want req-xyz", got)
	}
	if got := fields["trace_id"]; got == "" {
		t.Error("expected access log to include a non-empty trace_id")
	}
}

// fakePreAuthPlugin is a minimal HTTPTransportPlugin whose pre-auth hook is supplied per test;
// the remaining transport hooks are inert.
type fakePreAuthPlugin struct {
	name string
	hook func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)
}

func (p *fakePreAuthPlugin) GetName() string { return p.name }

func (p *fakePreAuthPlugin) Cleanup() error { return nil }

func (p *fakePreAuthPlugin) HTTPTransportPreAuthHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return p.hook(ctx, req)
}

func (p *fakePreAuthPlugin) HTTPTransportPreHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

func (p *fakePreAuthPlugin) HTTPTransportPostHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, _ *schemas.HTTPResponse) error {
	return nil
}

func (p *fakePreAuthPlugin) HTTPTransportResponseHeadersHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, _ *schemas.HTTPResponseMetadata) error {
	return nil
}

func (p *fakePreAuthPlugin) HTTPTransportStreamChunkHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// preAuthTestConfig builds a Config whose transport plugin cache holds the given plugins.
func preAuthTestConfig(plugins ...schemas.HTTPTransportPlugin) *lib.Config {
	config := &lib.Config{}
	config.HTTPTransportPlugins.Store(&plugins)
	return config
}

// preAuthTestCtx builds a request context carrying a body, so tests can assert the body is
// neither delivered to the hook nor disturbed by the phase.
func preAuthTestCtx() *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod("POST")
	req.SetRequestURI("/v1/chat/completions?stream=false")
	req.SetBodyString(`{"model":"gpt-4"}`)
	// Init rather than a zero value: the middleware derives a BifrostContext from the
	// request context, and Done() panics on an uninitialized RequestCtx.
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, nil, nil)
	return ctx
}

// TestTransportPreAuthInterceptorMiddleware_HeaderVisibleToNext asserts the whole point of the
// phase: a credential a plugin writes is on the request before the next middleware runs.
func TestTransportPreAuthInterceptorMiddleware_HeaderVisibleToNext(t *testing.T) {
	config := preAuthTestConfig(&fakePreAuthPlugin{
		name: "vk-injector",
		hook: func(_ *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
			req.Headers["x-bf-vk"] = "sk-bf-injected"
			req.Query["injected"] = "yes"
			return nil, nil
		},
	})

	var seenVK, seenQuery string
	handler := TransportPreAuthInterceptorMiddleware(config)(func(ctx *fasthttp.RequestCtx) {
		seenVK = string(ctx.Request.Header.Peek("x-bf-vk"))
		seenQuery = string(ctx.Request.URI().QueryArgs().Peek("injected"))
	})

	ctx := preAuthTestCtx()
	handler(ctx)

	if seenVK != "sk-bf-injected" {
		t.Errorf("expected next middleware to see the injected virtual key, got %q", seenVK)
	}
	if seenQuery != "yes" {
		t.Errorf("expected next middleware to see the injected query param, got %q", seenQuery)
	}
}

// TestTransportPreAuthInterceptorMiddleware_BodyIsModifiable asserts the pre-auth phase carries
// the same request shape as the post-auth phase: the hook reads the body and its rewrite lands on
// the request, so a hook can move between the two phases by renaming.
func TestTransportPreAuthInterceptorMiddleware_BodyIsModifiable(t *testing.T) {
	var hookSawBody string
	config := preAuthTestConfig(&fakePreAuthPlugin{
		name: "body-rewriter",
		hook: func(_ *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
			hookSawBody = string(req.Body)
			req.Body = []byte(`{"model":"rewritten"}`)
			return nil, nil
		},
	})

	var handlerBody string
	handler := TransportPreAuthInterceptorMiddleware(config)(func(ctx *fasthttp.RequestCtx) {
		handlerBody = string(ctx.Request.Body())
	})

	ctx := preAuthTestCtx()
	handler(ctx)

	if hookSawBody != `{"model":"gpt-4"}` {
		t.Errorf("expected the pre-auth hook to receive the request body, got %q", hookSawBody)
	}
	if handlerBody != `{"model":"rewritten"}` {
		t.Errorf("expected the rewritten body to reach the handler, got %q", handlerBody)
	}
}

// TestTransportPreAuthInterceptorMiddleware_ShortCircuitResponse asserts a plugin can answer the
// request itself, and that authentication and the handler never run.
func TestTransportPreAuthInterceptorMiddleware_ShortCircuitResponse(t *testing.T) {
	config := preAuthTestConfig(&fakePreAuthPlugin{
		name: "denier",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
			return &schemas.HTTPResponse{
				StatusCode: fasthttp.StatusUnauthorized,
				Headers:    map[string]string{"WWW-Authenticate": "Bearer"},
				Body:       []byte("denied"),
			}, nil
		},
	})

	nextCalled := false
	handler := TransportPreAuthInterceptorMiddleware(config)(func(_ *fasthttp.RequestCtx) {
		nextCalled = true
	})

	ctx := preAuthTestCtx()
	handler(ctx)

	if nextCalled {
		t.Error("expected the chain to stop at the short-circuiting plugin")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
	}
	if got := string(ctx.Response.Body()); got != "denied" {
		t.Errorf("expected the plugin's body, got %q", got)
	}
}

// TestTransportPreAuthInterceptorMiddleware_PluginError asserts a hook error fails the request
// rather than letting it continue unauthenticated.
func TestTransportPreAuthInterceptorMiddleware_PluginError(t *testing.T) {
	config := preAuthTestConfig(&fakePreAuthPlugin{
		name: "broken",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
			return nil, context.DeadlineExceeded
		},
	})

	nextCalled := false
	handler := TransportPreAuthInterceptorMiddleware(config)(func(_ *fasthttp.RequestCtx) {
		nextCalled = true
	})

	ctx := preAuthTestCtx()
	handler(ctx)

	if nextCalled {
		t.Error("expected the chain to stop after a plugin error")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
	}
}

// TestTransportPreAuthInterceptorMiddleware_PathMutationRejected asserts routing cannot be
// rewritten from the pre-auth phase — the request is failed rather than served on a path the
// router never matched.
func TestTransportPreAuthInterceptorMiddleware_PathMutationRejected(t *testing.T) {
	SetLogger(&mockLogger{})
	config := preAuthTestConfig(&fakePreAuthPlugin{
		name: "rerouter",
		hook: func(_ *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
			req.Path = "/v1/embeddings"
			return nil, nil
		},
	})

	nextCalled := false
	handler := TransportPreAuthInterceptorMiddleware(config)(func(_ *fasthttp.RequestCtx) {
		nextCalled = true
	})

	ctx := preAuthTestCtx()
	handler(ctx)

	if nextCalled {
		t.Error("expected the chain to stop after a rejected path mutation")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusConflict {
		t.Errorf("expected status %d, got %d", fasthttp.StatusConflict, ctx.Response.StatusCode())
	}
}

// TestTransportPreAuthInterceptorMiddleware_NoPlugins asserts the phase is a pass-through when
// nothing implements the hook, which is the common case.
func TestTransportPreAuthInterceptorMiddleware_NoPlugins(t *testing.T) {
	nextCalled := false
	handler := TransportPreAuthInterceptorMiddleware(preAuthTestConfig())(func(_ *fasthttp.RequestCtx) {
		nextCalled = true
	})

	ctx := preAuthTestCtx()
	handler(ctx)

	if !nextCalled {
		t.Error("expected the request to pass straight through when no plugin implements the hook")
	}
}

type fakeResponseHeadersPlugin struct {
	name     string
	hook     func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error
	preHook  func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)    // optional
	postHook func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error // optional
}

func (p *fakeResponseHeadersPlugin) GetName() string { return p.name }

func (p *fakeResponseHeadersPlugin) Cleanup() error { return nil }

func (p *fakeResponseHeadersPlugin) HTTPTransportResponseHeadersHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
	return p.hook(ctx, req, resp)
}

func (p *fakeResponseHeadersPlugin) HTTPTransportPreAuthHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

func (p *fakeResponseHeadersPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if p.preHook == nil {
		return nil, nil
	}
	return p.preHook(ctx, req)
}

func (p *fakeResponseHeadersPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	if p.postHook == nil {
		return nil
	}
	return p.postHook(ctx, req, resp)
}

func (p *fakeResponseHeadersPlugin) HTTPTransportStreamChunkHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

func responseHeadersTestConfig(t *testing.T, plugins ...schemas.BasePlugin) *lib.Config {
	t.Helper()
	config := &lib.Config{}
	for _, plugin := range plugins {
		if err := config.ReloadPlugin(plugin); err != nil {
			t.Fatalf("register response-headers plugin: %v", err)
		}
	}
	return config
}

// TestTransportRealtimeTurnMetadata verifies the actual HTTP plugin middleware
// exposes opt-in metadata to the session snapshot while excluding ordinary values.
func TestTransportRealtimeTurnMetadata(t *testing.T) {
	type metadataKey uint8
	const userKey metadataKey = 0
	const requestOnlyKey metadataKey = 1
	const preAuthKey metadataKey = 2
	preAuthPlugin := &fakePreAuthPlugin{
		name: "realtime-pre-auth-metadata",
		hook: func(ctx *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
			ctx.SetValueForRealtimeTurns(preAuthKey, "pre-auth")
			return nil, nil
		},
	}
	plugin := &fakeResponseHeadersPlugin{
		name: "realtime-metadata",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, _ *schemas.HTTPResponseMetadata) error {
			return nil
		},
		preHook: func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
			ctx.SetValueForRealtimeTurns(userKey, req.CaseInsensitiveHeaderLookup("X-Example-User-ID"))
			ctx.SetValue(requestOnlyKey, "request-only")
			return nil, nil
		},
	}
	var snapshot map[any]any
	config := responseHeadersTestConfig(t, preAuthPlugin, plugin)
	handler := TransportPreAuthInterceptorMiddleware(config)(TransportInterceptorMiddleware(config)(func(ctx *fasthttp.RequestCtx) {
		snapshot = snapshotRealtimeMiddlewareValues(ctx)
		requestCtx, requestCancel := lib.ConvertToBifrostContext(ctx, testWSHandlerStore{})
		defer requestCancel()
		relay, cancel := newRealtimeRelayContext(requestCtx)
		defer cancel()
		if relay.Value(userKey) != "test-alice" || relay.Value(preAuthKey) != "pre-auth" || relay.Value(requestOnlyKey) != nil {
			t.Fatal("WebRTC relay lost opt-in metadata or inherited request-only values")
		}
	}))
	ctx := preAuthTestCtx()
	ctx.Request.SetRequestURI("/v1/realtime?model=openai/test-model")
	ctx.Request.Header.Set("X-Example-User-ID", "test-alice")
	handler(ctx)
	if snapshot[userKey] != "test-alice" || snapshot[preAuthKey] != "pre-auth" || snapshot[requestOnlyKey] != nil {
		t.Fatalf("session snapshot = %v", snapshot)
	}
}

// TestTransportRealtimeTurnMetadataLaterWrites reconciles pre-auth opt-ins with later writes and clears.
func TestTransportRealtimeTurnMetadataLaterWrites(t *testing.T) {
	type metadataKey uint8
	const userKey metadataKey = 0
	const requestOnlyKey metadataKey = 1
	for _, mode := range []string{"overwrite", "nil", "clear", "untouched"} {
		t.Run(mode, func(t *testing.T) {
			preAuth := &fakePreAuthPlugin{name: "mark", hook: func(ctx *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
				ctx.SetValueForRealtimeTurns(userKey, "pre-auth")
				return nil, nil
			}}
			plugin := &fakeResponseHeadersPlugin{name: "update", hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, _ *schemas.HTTPResponseMetadata) error {
				return nil
			}, preHook: func(ctx *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
				switch mode {
				case "overwrite":
					ctx.SetValue(userKey, "transport")
				case "nil":
					ctx.SetValue(userKey, nil)
				case "clear":
					ctx.ClearValue(userKey)
				}
				ctx.SetValue(requestOnlyKey, "request-only")
				return nil, nil
			}}
			config := responseHeadersTestConfig(t, preAuth, plugin)
			called := false
			handler := TransportPreAuthInterceptorMiddleware(config)(TransportInterceptorMiddleware(config)(func(ctx *fasthttp.RequestCtx) {
				called = true
				var want any
				if mode == "overwrite" {
					want = "transport"
				} else if mode == "untouched" {
					want = "pre-auth"
				}
				snapshot := snapshotRealtimeMiddlewareValues(ctx)
				value, exists := snapshot[userKey]
				assert.True(t, exists)
				assert.Equal(t, want, value)
				_, exists = snapshot[requestOnlyKey]
				assert.False(t, exists, "ordinary new keys must stay request-local")
				requestCtx, cancel := lib.ConvertToBifrostContext(ctx, testWSHandlerStore{})
				defer cancel()
				relay, relayCancel := newRealtimeRelayContext(requestCtx)
				defer relayCancel()
				assert.Equal(t, want, relay.Value(userKey))
			}))
			ctx := preAuthTestCtx()
			ctx.Request.SetRequestURI("/v1/realtime?model=openai/test-model")
			handler(ctx)
			assert.True(t, called)
		})
	}
}

func TestTransportResponseHeadersHook_NonStreaming(t *testing.T) {
	var gotStatus int
	var gotPath string
	plugin := &fakeResponseHeadersPlugin{
		name: "response-headers",
		hook: func(_ *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
			gotStatus = resp.StatusCode
			gotPath = req.Path
			resp.DeleteHeader("X-Remove-Me")
			resp.SetHeader("X-Custom-Header", "custom-value")
			resp.SetHeader("Connection", "close")
			resp.SetHeader("Content-Length", "999")
			resp.SetHeader("Transfer-Encoding", "chunked")
			resp.StatusCode = fasthttp.StatusTeapot // Status is intentionally read-only.
			return nil
		},
	}

	handler := TransportInterceptorMiddleware(responseHeadersTestConfig(t, plugin))(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(fasthttp.StatusCreated)
		ctx.Response.Header.Set("X-Remove-Me", "old")
		ctx.Response.Header.Set("Connection", "keep-alive")
		ctx.SetBodyString("ok")
	})

	ctx := preAuthTestCtx()
	handler(ctx)

	if gotStatus != fasthttp.StatusCreated {
		t.Fatalf("hook status = %d, want %d", gotStatus, fasthttp.StatusCreated)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("hook path = %q, want /v1/chat/completions", gotPath)
	}
	if got := string(ctx.Response.Header.Peek("X-Custom-Header")); got != "custom-value" {
		t.Fatalf("X-Custom-Header = %q, want custom-value", got)
	}
	if got := string(ctx.Response.Header.Peek("X-Remove-Me")); got != "" {
		t.Fatalf("X-Remove-Me = %q, want deleted", got)
	}
	if got := string(ctx.Response.Header.Peek("Connection")); got != "keep-alive" {
		t.Fatalf("Connection = %q, want transport-owned keep-alive", got)
	}
	if got := ctx.Response.Header.ContentLength(); got == 999 {
		t.Fatalf("Content-Length = %d, want transport-owned length", got)
	}
	if got := string(ctx.Response.Header.Peek("Transfer-Encoding")); got != "" {
		t.Fatalf("Transfer-Encoding = %q, want transport-owned (unset)", got)
	}
	if got := ctx.Response.StatusCode(); got != fasthttp.StatusCreated {
		t.Fatalf("status = %d, want read-only status %d", got, fasthttp.StatusCreated)
	}
}

// TestTransportResponseHeadersHook_PreHookShortCircuits asserts responses produced by
// HTTPTransportPreHook (error, short-circuit, rejected rewrite) still run the headers hook.
func TestTransportResponseHeadersHook_PreHookShortCircuits(t *testing.T) {
	SetLogger(&mockLogger{})
	tests := []struct {
		name       string
		preHook    func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)
		wantStatus int
	}{
		{
			name: "hook error",
			preHook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
				return nil, context.DeadlineExceeded
			},
			wantStatus: fasthttp.StatusInternalServerError,
		},
		{
			name: "short-circuit response",
			preHook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
				return &schemas.HTTPResponse{StatusCode: fasthttp.StatusTooManyRequests, Headers: map[string]string{}, Body: []byte("slow down")}, nil
			},
			wantStatus: fasthttp.StatusTooManyRequests,
		},
		{
			name: "rejected path rewrite",
			preHook: func(_ *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
				req.Path = "/v1/embeddings"
				return nil, nil
			},
			wantStatus: fasthttp.StatusConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &fakeResponseHeadersPlugin{
				name:    "pre-hook-short-circuit",
				preHook: tt.preHook,
				hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
					resp.SetHeader("X-Request-Trace", "abc123")
					return nil
				},
			}
			handler := TransportInterceptorMiddleware(responseHeadersTestConfig(t, plugin))(func(_ *fasthttp.RequestCtx) {
				t.Fatal("expected the chain to stop at the pre-hook")
			})
			ctx := preAuthTestCtx()
			handler(ctx)

			if got := ctx.Response.StatusCode(); got != tt.wantStatus {
				t.Fatalf("status = %d, want %d", got, tt.wantStatus)
			}
			if got := string(ctx.Response.Header.Peek("X-Request-Trace")); got != "abc123" {
				t.Fatalf("X-Request-Trace = %q, want abc123", got)
			}
		})
	}
}

// TestTransportResponseHeadersHook_ErrorStopsRemainingHooks asserts a hook error keeps the
// mutations made so far (including the failing plugin's) and skips the remaining plugins.
func TestTransportResponseHeadersHook_ErrorStopsRemainingHooks(t *testing.T) {
	SetLogger(&mockLogger{})
	firstCalled := false
	first := &fakeResponseHeadersPlugin{
		name: "first",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
			firstCalled = true
			resp.SetHeader("X-First", "1")
			return nil
		},
	}
	second := &fakeResponseHeadersPlugin{
		name: "second",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
			resp.SetHeader("X-Second", "1")
			return context.DeadlineExceeded
		},
	}
	third := &fakeResponseHeadersPlugin{
		name: "third",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
			resp.SetHeader("X-Third", "1")
			return nil
		},
	}

	handler := TransportInterceptorMiddleware(responseHeadersTestConfig(t, first, second, third))(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(fasthttp.StatusOK)
	})
	ctx := preAuthTestCtx()
	handler(ctx)

	if firstCalled {
		t.Fatal("expected hooks after the failing plugin to be skipped")
	}
	if got := string(ctx.Response.Header.Peek("X-Third")); got != "1" {
		t.Fatalf("X-Third = %q, want mutation from a hook before the error", got)
	}
	if got := string(ctx.Response.Header.Peek("X-Second")); got != "1" {
		t.Fatalf("X-Second = %q, want the failing hook's mutation kept", got)
	}
	if got := string(ctx.Response.Header.Peek("X-First")); got != "" {
		t.Fatalf("X-First = %q, want unset", got)
	}
	if got := ctx.Response.StatusCode(); got != fasthttp.StatusOK {
		t.Fatalf("status = %d, want the handler's status", got)
	}
}

// TestTransportResponseHeadersHook_OrderRelativeToPostHook asserts the buffered path runs
// HTTPTransportPostHook before the headers hook, while the streaming path defers it.
func TestTransportResponseHeadersHook_OrderRelativeToPostHook(t *testing.T) {
	tests := []struct {
		name          string
		streaming     bool
		wantPostFirst bool
	}{
		{name: "buffered runs the post-hook first", streaming: false, wantPostFirst: true},
		{name: "streaming defers the post-hook", streaming: true, wantPostFirst: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			var sawPostHeader string
			plugin := &fakeResponseHeadersPlugin{
				name: "ordering",
				postHook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
					calls = append(calls, "post")
					resp.Headers["X-Post"] = "1"
					return nil
				},
				hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
					calls = append(calls, "headers")
					sawPostHeader = resp.Header("X-Post")
					return nil
				},
			}
			handler := TransportInterceptorMiddleware(responseHeadersTestConfig(t, plugin))(func(ctx *fasthttp.RequestCtx) {
				if tt.streaming {
					ctx.SetContentType("text/event-stream")
					ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)
					ctx.Response.SetBodyStream(bytes.NewReader([]byte("data: ok\n\n")), -1)
					return
				}
				ctx.SetBodyString("ok")
			})
			ctx := preAuthTestCtx()
			handler(ctx)
			defer ctx.Response.CloseBodyStream()

			if tt.wantPostFirst {
				if got := strings.Join(calls, ","); got != "post,headers" {
					t.Fatalf("hook order = %q, want post,headers", got)
				}
				if sawPostHeader != "1" {
					t.Fatalf("headers hook saw X-Post = %q, want the post-hook's header", sawPostHeader)
				}
				return
			}
			if got := strings.Join(calls, ","); got != "headers" {
				t.Fatalf("hook order = %q, want only headers before the stream ends", got)
			}
		})
	}
}

func TestTransportResponseHeadersHook_Streaming(t *testing.T) {
	plugin := &fakeResponseHeadersPlugin{
		name: "stream-response-headers",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
			if got := resp.Header("Content-Type"); got != "text/event-stream" {
				t.Fatalf("Content-Type in hook = %q, want text/event-stream", got)
			}
			resp.SetHeader("X-Streaming-Header", "present-before-first-chunk")
			return nil
		},
	}

	handler := TransportInterceptorMiddleware(responseHeadersTestConfig(t, plugin))(func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("text/event-stream")
		ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)
		ctx.Response.SetBodyStream(bytes.NewReader([]byte("data: ok\n\n")), -1)
	})

	ctx := preAuthTestCtx()
	handler(ctx)
	defer ctx.Response.CloseBodyStream()

	if got := string(ctx.Response.Header.Peek("X-Streaming-Header")); got != "present-before-first-chunk" {
		t.Fatalf("X-Streaming-Header = %q, want present-before-first-chunk", got)
	}
}

func TestTransportResponseHeadersHook_ReverseOrder(t *testing.T) {
	var calls []string
	first := &fakeResponseHeadersPlugin{
		name: "first",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
			calls = append(calls, "first")
			resp.SetHeader("X-Order", "first")
			return nil
		},
	}
	second := &fakeResponseHeadersPlugin{
		name: "second",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
			calls = append(calls, "second")
			resp.SetHeader("X-Order", "second")
			return nil
		},
	}

	handler := TransportInterceptorMiddleware(responseHeadersTestConfig(t, first, second))(func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(fasthttp.StatusOK)
	})
	ctx := preAuthTestCtx()
	handler(ctx)

	if got := strings.Join(calls, ","); got != "second,first" {
		t.Fatalf("hook order = %q, want second,first", got)
	}
	if got := string(ctx.Response.Header.Peek("X-Order")); got != "first" {
		t.Fatalf("X-Order = %q, want first", got)
	}
}

// responseHeaderValues returns every value of a live response header, including Set-Cookie.
func responseHeaderValues(ctx *fasthttp.RequestCtx, name string) []string {
	var values []string
	for key, value := range ctx.Response.Header.All() {
		if strings.EqualFold(string(key), name) {
			values = append(values, string(value))
		}
	}
	return values
}

func TestTransportResponseHeadersHook_RepeatedHeaders(t *testing.T) {
	tests := []struct {
		name       string
		hook       func(resp *schemas.HTTPResponseMetadata)
		wantLink   []string
		wantCookie []string
		wantExtra  string
	}{
		{
			name:       "no-op hook preserves repeated headers",
			hook:       func(*schemas.HTTPResponseMetadata) {},
			wantLink:   []string{"<a>; rel=preload", "<b>; rel=preload"},
			wantCookie: []string{"a=1", "b=2"},
		},
		{
			name:       "unrelated header change leaves repeated headers untouched",
			hook:       func(resp *schemas.HTTPResponseMetadata) { resp.SetHeader("X-Extra", "1") },
			wantLink:   []string{"<a>; rel=preload", "<b>; rel=preload"},
			wantCookie: []string{"a=1", "b=2"},
			wantExtra:  "1",
		},
		{
			name:       "deleting a repeated header removes every value",
			hook:       func(resp *schemas.HTTPResponseMetadata) { resp.DeleteHeader("Link") },
			wantLink:   nil,
			wantCookie: []string{"a=1", "b=2"},
		},
		{
			name:       "changing a repeated header replaces every value",
			hook:       func(resp *schemas.HTTPResponseMetadata) { resp.SetHeader("Link", "<c>; rel=preload") },
			wantLink:   []string{"<c>; rel=preload"},
			wantCookie: []string{"a=1", "b=2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &fakeResponseHeadersPlugin{
				name: "repeated-headers",
				hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
					tt.hook(resp)
					return nil
				},
			}
			// Streaming path: the buffered path also runs the post-hook write-back, which is covered separately.
			handler := TransportInterceptorMiddleware(responseHeadersTestConfig(t, plugin))(func(ctx *fasthttp.RequestCtx) {
				ctx.SetContentType("text/event-stream")
				ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)
				ctx.Response.SetBodyStream(bytes.NewReader([]byte("data: ok\n\n")), -1)
				ctx.Response.Header.Add("Link", "<a>; rel=preload")
				ctx.Response.Header.Add("Link", "<b>; rel=preload")
				ctx.Response.Header.Add("Set-Cookie", "a=1")
				ctx.Response.Header.Add("Set-Cookie", "b=2")
			})
			ctx := preAuthTestCtx()
			handler(ctx)
			defer ctx.Response.CloseBodyStream()

			if got := responseHeaderValues(ctx, "Link"); !reflect.DeepEqual(got, tt.wantLink) {
				t.Fatalf("Link = %q, want %q", got, tt.wantLink)
			}
			if got := responseHeaderValues(ctx, "Set-Cookie"); !reflect.DeepEqual(got, tt.wantCookie) {
				t.Fatalf("Set-Cookie = %q, want %q", got, tt.wantCookie)
			}
			if got := string(ctx.Response.Header.Peek("X-Extra")); got != tt.wantExtra {
				t.Fatalf("X-Extra = %q, want %q", got, tt.wantExtra)
			}
		})
	}
}

// TestTransportResponseHeadersHook_PreAuthShortCircuits asserts plugin-produced responses from the
// pre-auth phase still run the response-headers hook, like the post-auth short-circuits do.
func TestTransportResponseHeadersHook_PreAuthShortCircuits(t *testing.T) {
	SetLogger(&mockLogger{})
	tests := []struct {
		name       string
		hook       func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)
		wantStatus int
	}{
		{
			name: "hook error",
			hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
				return nil, context.DeadlineExceeded
			},
			wantStatus: fasthttp.StatusInternalServerError,
		},
		{
			name: "short-circuit response",
			hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
				return &schemas.HTTPResponse{StatusCode: fasthttp.StatusForbidden, Headers: map[string]string{}, Body: []byte("denied")}, nil
			},
			wantStatus: fasthttp.StatusForbidden,
		},
		{
			name: "rejected path rewrite",
			hook: func(_ *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
				req.Path = "/v1/embeddings"
				return nil, nil
			},
			wantStatus: fasthttp.StatusConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := &fakeResponseHeadersPlugin{
				name: "response-headers",
				hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
					resp.SetHeader("X-Request-Trace", "abc123")
					return nil
				},
			}
			config := preAuthTestConfig(&fakePreAuthPlugin{name: "pre-auth", hook: tt.hook}, headers)
			handler := TransportPreAuthInterceptorMiddleware(config)(func(_ *fasthttp.RequestCtx) {
				t.Fatal("expected the chain to stop in the pre-auth phase")
			})
			ctx := preAuthTestCtx()
			handler(ctx)

			if got := ctx.Response.StatusCode(); got != tt.wantStatus {
				t.Fatalf("status = %d, want %d", got, tt.wantStatus)
			}
			if got := string(ctx.Response.Header.Peek("X-Request-Trace")); got != "abc123" {
				t.Fatalf("X-Request-Trace = %q, want abc123", got)
			}
		})
	}
}

// TestSecurityHeadersMiddleware_APINoStore verifies that /api/ responses carry
// Cache-Control: no-store unless the handler sets its own policy, so a CDN never serves
// one user's session or config data to another. Non-API paths are left alone.
func TestSecurityHeadersMiddleware_APINoStore(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		handlerSets string
		want        string
	}{
		{name: "api path gets no-store", path: "/api/session/is-auth-enabled", want: "no-store"},
		{name: "api path keeps handler policy", path: "/api/branding/logo", handlerSets: "private, max-age=86400", want: "private, max-age=86400"},
		{name: "non-api path untouched", path: "/ui/assets/app.js", want: ""},
		{name: "non-api path keeps handler policy", path: "/ui/assets/app.js", handlerSets: "public, max-age=3600", want: "public, max-age=3600"},
		{name: "prefix must match a segment", path: "/apiary", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := SecurityHeadersMiddleware()(func(ctx *fasthttp.RequestCtx) {
				if tt.handlerSets != "" {
					ctx.Response.Header.Set("Cache-Control", tt.handlerSets)
				}
				ctx.SetStatusCode(fasthttp.StatusOK)
			})
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(tt.path)
			handler(ctx)
			if got := string(ctx.Response.Header.Peek("Cache-Control")); got != tt.want {
				t.Fatalf("Cache-Control for %s = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestAuthBypassedMiddleware_MarksRequest pins the marker the server installs when there is
// no config store and so no auth middleware. Handlers that require genuine auth for dangerous
// changes key off BifrostContextKeyAuthBypassed; an unmarked request reads as authenticated,
// so without the marker every such guard fails open in exactly the no-auth deployment.
func TestAuthBypassedMiddleware_MarksRequest(t *testing.T) {
	var sawBypassed, sawLocalAdmin bool
	handler := lib.ChainMiddlewares(func(ctx *fasthttp.RequestCtx) {
		sawBypassed, _ = ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool)
		sawLocalAdmin, _ = ctx.UserValue(schemas.IsLocalAdminContextKey).(bool)
	}, AuthBypassedMiddleware([]string{"*"}, nil))

	handler(&fasthttp.RequestCtx{})

	if !sawBypassed {
		t.Fatalf("expected request to be marked as auth-bypassed")
	}
	// Same posture as the auth-disabled branch of the real middleware: the request
	// is the local admin for ordinary handlers (notifications check this marker
	// directly), while the bypass marker keeps the sensitive-change guards closed.
	if !sawLocalAdmin {
		t.Fatalf("expected request to be marked local admin as well")
	}
}

// Copies the root span's attributes out of a flushed trace, synchronously because
// the tracer pools the trace.
type captureRootAttrsPlugin struct {
	done  chan struct{}
	name  string
	attrs map[string]any
}

func (p *captureRootAttrsPlugin) GetName() string { return "capture-root-attrs" }
func (p *captureRootAttrsPlugin) Cleanup() error  { return nil }
func (p *captureRootAttrsPlugin) Inject(_ context.Context, trace *schemas.Trace) error {
	defer close(p.done)
	if trace == nil || trace.RootSpan == nil {
		return nil
	}
	p.name = trace.RootSpan.Name
	p.attrs = make(map[string]any, len(trace.RootSpan.Attributes))
	for k, v := range trace.RootSpan.Attributes {
		p.attrs[k] = v
	}
	return nil
}

// The root span must carry the stable OTel HTTP attributes, with http.route as the
// matched route template rather than the raw path (#7438).
func TestTracingMiddleware_RootSpanCarriesStableHTTPSemconv(t *testing.T) {
	SetLogger(&mockLogger{})

	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()

	plugin := &captureRootAttrsPlugin{done: make(chan struct{})}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, nil)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/openai/v1/chat/completions?stream=true")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("User-Agent", "bifrost-test/1.0")

	NewTracingMiddleware(tracer).Middleware()(func(c *fasthttp.RequestCtx) {
		// The router resolves the route template only once it dispatches, so this lands
		// after the span opens, mirroring the real chain.
		c.SetUserValue(string(schemas.BifrostContextKeyHTTPRoute), "/openai/v1/chat/completions")
		c.Response.SetStatusCode(fasthttp.StatusOK)
	})(ctx)

	select {
	case <-plugin.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for trace injection")
	}

	want := map[string]any{
		"http.route":                "/openai/v1/chat/completions",
		"http.request.method":       "POST",
		"http.response.status_code": 200,
		"url.path":                  "/openai/v1/chat/completions",
		"url.scheme":                "http",
		"user_agent.original":       "bifrost-test/1.0",
	}
	for k, v := range want {
		if got := plugin.attrs[k]; got != v {
			t.Errorf("stable attribute %s = %#v, want %#v", k, got, v)
		}
	}

	// Deprecated keys stay so existing dashboards keep working.
	legacy := map[string]any{
		"http.method":      "POST",
		"http.url":         "/openai/v1/chat/completions",
		"http.user_agent":  "bifrost-test/1.0",
		"http.status_code": 200,
	}
	for k, v := range legacy {
		if got := plugin.attrs[k]; got != v {
			t.Errorf("legacy attribute %s = %#v, want %#v (must stay)", k, got, v)
		}
	}
}

// The query string is dropped rather than redacted, and an unknown HTTP method
// collapses to _OTHER so it cannot inflate cardinality.
func TestTracingMiddleware_RootSpanDropsQueryAndNormalizes(t *testing.T) {
	SetLogger(&mockLogger{})

	run := func(t *testing.T, uri, method string) (string, map[string]any) {
		t.Helper()
		store := tracing.NewTraceStore(5*time.Minute, nil)
		defer store.Stop()
		tracer := tracing.NewTracer(store, nil, nil)
		defer tracer.Stop()
		plugin := &captureRootAttrsPlugin{done: make(chan struct{})}
		tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, nil)

		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(uri)
		ctx.Request.Header.SetMethod(method)
		NewTracingMiddleware(tracer).Middleware()(func(*fasthttp.RequestCtx) {})(ctx)

		select {
		case <-plugin.done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for trace injection")
		}
		return plugin.name, plugin.attrs
	}

	// Dropping rather than redacting means no denylist can go stale. The query must
	// reach neither an attribute nor the span name.
	t.Run("the query string reaches nothing on the span", func(t *testing.T) {
		name, attrs := run(t, "/v1/realtime?ticket=SUPERSECRET&%74oken=ALSOSECRET&model=gpt-4o", "GET")

		if _, ok := attrs["url.query"]; ok {
			t.Error("url.query must not be set at all")
		}
		if got := attrs["url.path"]; got != "/v1/realtime" {
			t.Errorf("url.path = %#v, want %q", got, "/v1/realtime")
		}
		if got := attrs["http.url"]; got != "/v1/realtime" {
			t.Errorf("http.url = %#v, want the path with no query", got)
		}
		if name != "/v1/realtime" {
			t.Errorf("span name = %q, want the path with no query", name)
		}
		for k, v := range attrs {
			if str, ok := v.(string); ok && (strings.Contains(str, "SECRET") || strings.Contains(str, "gpt-4o")) {
				t.Errorf("attribute %s carried query content: %q", k, str)
			}
		}
	})

	t.Run("unknown method collapses to _OTHER", func(t *testing.T) {
		_, attrs := run(t, "/v1/chat/completions", "FOOBAR")
		if got := attrs["http.request.method"]; got != "_OTHER" {
			t.Errorf("http.request.method = %#v, want %q", got, "_OTHER")
		}
		if got := attrs["http.request.method_original"]; got != "FOOBAR" {
			t.Errorf("http.request.method_original = %#v, want %q", got, "FOOBAR")
		}
		// The deprecated key keeps the raw value it has always carried.
		if got := attrs["http.method"]; got != "FOOBAR" {
			t.Errorf("http.method = %#v, want %q", got, "FOOBAR")
		}
	})

	t.Run("known method is passed through without an original", func(t *testing.T) {
		_, attrs := run(t, "/v1/chat/completions", "POST")
		if got := attrs["http.request.method"]; got != "POST" {
			t.Errorf("http.request.method = %#v, want %q", got, "POST")
		}
		if _, ok := attrs["http.request.method_original"]; ok {
			t.Error("http.request.method_original must be absent for a known method")
		}
	})
}

// TestAuthMiddleware_ConfiguredSetupToken_OutlivesFirstAdmin pins the second use of the
// operator-configured setup token: proving control when auth_config is changed while
// dashboard auth is disabled. Unlike the first-admin bootstrap gate it must keep matching
// only the exact configured value after an admin account exists and after
// ClearBootstrapToken, and it must never match when no token is configured.
func TestAuthMiddleware_ConfiguredSetupToken_OutlivesFirstAdmin(t *testing.T) {
	SetLogger(&mockLogger{})
	const token = "test-setup-token"
	am, err := InitAuthMiddleware(newRealOAuth2Store(t), nil, nil, token)
	if err != nil {
		t.Fatalf("InitAuthMiddleware: %v", err)
	}

	if !am.CheckConfiguredSetupToken(token) {
		t.Error("the configured token must match before any admin exists")
	}
	if am.CheckConfiguredSetupToken("wrong") || am.CheckConfiguredSetupToken("") {
		t.Error("a wrong or empty token must not match")
	}

	am.UpdateAuthConfig(&configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar("password"),
		IsEnabled:     true,
	})
	am.ClearBootstrapToken()

	if !am.CheckBootstrapToken("anything") {
		t.Error("the first-admin gate must still open once an admin account exists")
	}
	if !am.CheckConfiguredSetupToken(token) {
		t.Error("the configured token must keep matching after the first admin is created")
	}
	if am.CheckConfiguredSetupToken("anything") {
		t.Error("an admin account must not make every token match the configured one")
	}

	none, err := InitAuthMiddleware(newRealOAuth2Store(t), nil, nil, "")
	if err != nil {
		t.Fatalf("InitAuthMiddleware: %v", err)
	}
	if none.CheckConfiguredSetupToken("") || none.CheckConfiguredSetupToken("anything") {
		t.Error("with no token configured nothing may match")
	}
}

// zstdWindow512MiBFrame is a complete zstd frame (magic, frame header with a
// Window_Descriptor only, one empty raw last block) whose header declares a
// 512 MiB back-reference window: nine bytes on the wire, no decoded output.
var zstdWindow512MiBFrame = []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x98, 0x01, 0x00, 0x00}

func TestRequestDecompressionMiddleware_ZstdDeclaredWindowBeyondLimitRejected(t *testing.T) {
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			MaxRequestBodySizeMB: 100,
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/chat/completions")
	ctx.Request.Header.Set("Content-Encoding", "zstd")
	ctx.Request.SetBodyRaw(zstdWindow512MiBFrame)

	nextCalled := false
	handler := RequestDecompressionMiddleware(config)(func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	})

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	handler(ctx)
	runtime.ReadMemStats(&after)

	if nextCalled {
		t.Fatal("next handler must not run for a frame declaring a window beyond the limit")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusRequestEntityTooLarge {
		t.Fatalf("expected status 413, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 32<<20 {
		t.Fatalf("middleware allocated %d bytes for a 9-byte frame; the declared window must be refused before it is allocated", delta)
	}
	var bifrostErr schemas.BifrostError
	if err := json.Unmarshal(ctx.Response.Body(), &bifrostErr); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "window size exceeded") {
		t.Fatalf("unexpected error message: %#v", bifrostErr.Error)
	}
}

func TestRequestDecompressionMiddleware_StreamingPath_DecompressedSizeLimit(t *testing.T) {
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			MaxRequestBodySizeMB: 1,
		},
	}

	// 4 MiB of zeros gzips to a few KiB: a small wire body that expands well past the 1 MB cap.
	plainBody := make([]byte, 4<<20)
	compressedBody, err := gzipCompress(plainBody)
	if err != nil {
		t.Fatalf("failed to gzip test payload: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "gzip")
	ctx.Request.SetBodyStream(bytes.NewReader(compressedBody), -1)

	var readErr error
	var readBytes int64
	handler := RequestDecompressionMiddleware(config)(func(ctx *fasthttp.RequestCtx) {
		readBytes, readErr = io.Copy(io.Discard, ctx.RequestBodyStream())
	})
	handler(ctx)

	if !errors.Is(readErr, errRequestBodyTooLarge) {
		t.Fatalf("expected the body stream to fail with errRequestBodyTooLarge, got %v after %d bytes", readErr, readBytes)
	}
	if readBytes > int64(1<<20)+1 {
		t.Fatalf("stream yielded %d decompressed bytes, more than the 1 MB cap", readBytes)
	}
}

func TestRequestDecompressionMiddleware_StreamingPath_ZstdDeclaredWindowBounded(t *testing.T) {
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{
			MaxRequestBodySizeMB: 100,
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("Content-Encoding", "zstd")
	// Chunked (size -1) takes the streaming path, where the decoder error surfaces
	// lazily when the handler reads the body rather than at middleware time. The
	// point this pins is that the bounded decoder refuses the declared window
	// instead of allocating it.
	ctx.Request.SetBodyStream(bytes.NewReader(zstdWindow512MiBFrame), -1)

	var readErr error
	handler := RequestDecompressionMiddleware(config)(func(ctx *fasthttp.RequestCtx) {
		_, readErr = io.Copy(io.Discard, ctx.RequestBodyStream())
	})

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	handler(ctx)
	runtime.ReadMemStats(&after)

	if readErr == nil {
		t.Fatal("expected reading the body to fail on the declared oversized window")
	}
	if !strings.Contains(readErr.Error(), "window size exceeded") {
		t.Fatalf("unexpected read error: %v", readErr)
	}
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 32<<20 {
		t.Fatalf("streaming decode allocated %d bytes for a 9-byte frame; the declared window must be refused, not allocated", delta)
	}
}

func TestLimitedBodyReader_ExactLimitPassesOneByteOverFails(t *testing.T) {
	exact := bytes.Repeat([]byte("x"), 16)
	got, err := io.ReadAll(newLimitedBodyReader(bytes.NewReader(exact), 16))
	if err != nil || len(got) != 16 {
		t.Fatalf("exact-limit body: got %d bytes, err %v", len(got), err)
	}

	over := bytes.Repeat([]byte("x"), 17)
	_, err = io.ReadAll(newLimitedBodyReader(bytes.NewReader(over), 16))
	if !errors.Is(err, errRequestBodyTooLarge) {
		t.Fatalf("one-byte-over body: expected errRequestBodyTooLarge, got %v", err)
	}
}

func TestRecoveryMiddleware_PanicBecomesInternalServerErrorAndServerKeepsServing(t *testing.T) {
	SetLogger(&mockLogger{})
	handler := RecoveryMiddleware(newRecoveryTestCors())(func(ctx *fasthttp.RequestCtx) {
		if string(ctx.Path()) == "/panic" {
			var pc *[]byte
			_ = (*pc)[:1] // nil dereference: the kind of runtime panic a parser raises
		}
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBodyString("ok")
	})

	server := &fasthttp.Server{Handler: handler}
	ln := fasthttputil.NewInmemoryListener()
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()

	client := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			return ln.Dial()
		},
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	status, body, err := client.Get(nil, "http://bifrost/panic")
	if err != nil {
		t.Fatalf("panicking request should still get a response, got transport error: %v", err)
	}
	if status != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500 for the panicking request, got %d: %s", status, body)
	}
	var bifrostErr schemas.BifrostError
	if err := json.Unmarshal(body, &bifrostErr); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "internal server error" {
		t.Fatalf("unexpected error payload: %s", body)
	}

	status, body, err = client.Get(nil, "http://bifrost/ok")
	if err != nil {
		t.Fatalf("server must keep serving after a recovered panic, got %v", err)
	}
	if status != fasthttp.StatusOK || string(body) != "ok" {
		t.Fatalf("follow-up request got %d %q, want 200 ok", status, body)
	}
}

// A disconnected caller leaves its worker still writing, so flushing at handler return
// would send every connector a trace with no cost or model.
func TestTracingMiddleware_DeferredCompletionWaitsForWorker(t *testing.T) {
	SetLogger(&mockLogger{})

	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()

	plugin := &captureRootAttrsPlugin{done: make(chan struct{})}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, nil)

	var traceID string
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/chat/completions")
	ctx.Request.Header.SetMethod("POST")

	NewTracingMiddleware(tracer).Middleware()(func(c *fasthttp.RequestCtx) {
		traceID, _ = c.UserValue(schemas.BifrostContextKeyTraceID).(string)
		// The caller's context ended and the worker won the handoff.
		tracer.DeferTraceCompletion(traceID)
		c.Response.SetStatusCode(499)
	})(ctx)

	// Handler returned and the defer ran; nothing may be exported yet.
	select {
	case <-plugin.done:
		t.Fatal("trace was flushed at handler return despite a worker still owning it")
	case <-time.After(150 * time.Millisecond):
	}

	// The worker now finishes: it writes the late attributes, then completes the trace.
	if h := tracer.GetSpanHandleByID(traceID, nil); h != nil {
		tracer.SetAttribute(h, "gen_ai.usage.cost", 0.0421)
	}
	tracer.ClearTraceCompletionDeferral(traceID)
	tracer.CompleteAndFlushTrace(traceID)

	select {
	case <-plugin.done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker completion did not flush the trace")
	}

	// Exported exactly once, and carrying the worker's late write.
	if got := plugin.attrs["gen_ai.usage.cost"]; got != 0.0421 {
		t.Errorf("exported cost = %#v, want 0.0421 (the worker's late write must be in the snapshot)", got)
	}
	if tracer.IsTraceCompletionDeferred(traceID) {
		t.Error("deferral marker outlived completion")
	}
}

// Only the transport defer sees the transport plugin logs, so without the handoff the
// worker can flush before they are attached.
func TestTracingMiddleware_TransportLogsSurviveWorkerCompletion(t *testing.T) {
	SetLogger(&mockLogger{})

	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()

	plugin := &capturePluginLogsPlugin{done: make(chan struct{})}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, nil)

	var traceID string
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/chat/completions")
	ctx.Request.Header.SetMethod("POST")

	NewTracingMiddleware(tracer).Middleware()(func(c *fasthttp.RequestCtx) {
		traceID, _ = c.UserValue(schemas.BifrostContextKeyTraceID).(string)
		tracer.DeferTraceCompletion(traceID)
		c.SetUserValue(schemas.BifrostContextKeyTransportPluginLogs, []schemas.PluginLogEntry{
			{PluginName: "transport-probe", Message: "transport-log-marker"},
		})
		c.Response.SetStatusCode(499)
	})(ctx)

	// The worker completes only after the handoff.
	tracer.AwaitTransportHandoff(traceID)
	tracer.ClearTraceCompletionDeferral(traceID)
	tracer.CompleteAndFlushTrace(traceID)

	select {
	case <-plugin.done:
	case <-time.After(2 * time.Second):
		t.Fatal("trace never reached the connector")
	}
	if !plugin.sawTransportLog {
		t.Error("transport plugin logs were dropped; only the transport defer can see them")
	}
}

// Records whether the transport's own log entry survived.
type capturePluginLogsPlugin struct {
	done            chan struct{}
	sawTransportLog bool
}

func (p *capturePluginLogsPlugin) GetName() string { return "capture-plugin-logs" }
func (p *capturePluginLogsPlugin) Cleanup() error  { return nil }
func (p *capturePluginLogsPlugin) Inject(_ context.Context, trace *schemas.Trace) error {
	defer close(p.done)
	for _, entry := range trace.PluginLogs {
		if entry.Message == "transport-log-marker" {
			p.sawTransportLog = true
		}
	}
	return nil
}

// TestIsSameOrigin pins how an Origin is compared with the Host the request was sent to: hosts
// case-insensitively, the scheme's default port made explicit on either side, IPv6 brackets kept,
// userinfo ignored, and anything that is not an http(s) origin never the same.
func TestIsSameOrigin(t *testing.T) {
	cases := []struct {
		origin, host string
		want         bool
	}{
		{"http://bifrost.internal:8080", "bifrost.internal:8080", true},
		{"http://Bifrost.Internal:8080", "bifrost.internal:8080", true},
		{"HTTP://bifrost.internal:8080", "bifrost.internal:8080", true},
		{"https://b.example", "b.example:443", true},
		{"https://b.example:443", "b.example", true},
		{"http://b.example", "b.example:80", true},
		{"http://b.example", "b.example", true},
		{"http://[::1]:8080", "[::1]:8080", true},
		{"http://[::1]", "[::1]:80", true},
		{"http://user@b.example:8080", "b.example:8080", true},
		{"http://b.example:8080/", "b.example:8080", true},
		{"http://b.example:8080", "b.example:8081", false},
		{"https://b.example", "b.example:80", false},
		{"http://b.example", "b.example:443", false},
		{"http://evil.example", "b.example", false},
		{"ftp://b.example", "b.example", false},
		{"null", "b.example", false},
		{"", "b.example", false},
		{"http://b.example", "", false},
		{"http://", "b.example", false},
		{"http://b.example:port", "b.example:port", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, isSameOrigin(tc.origin, tc.host), "isSameOrigin(%q, %q)", tc.origin, tc.host)
	}
}

// BenchmarkAuthBypassedMiddlewareOriginCheck measures the auth-bypassed management middleware on
// an /api request with no Origin (CLI and SDKs), a same-origin dashboard request served from a
// non-localhost address (localhost origins are accepted before the host comparison runs), and a
// request from an origin listed in allowed_origins.
func BenchmarkAuthBypassedMiddlewareOriginCheck(b *testing.B) {
	handler := AuthBypassedMiddleware([]string{"https://app.example.com"}, nil)(func(*fasthttp.RequestCtx) {})
	cases := []struct{ name, host, origin string }{
		{"no_origin", "bifrost.internal:8080", ""},
		{"same_origin", "bifrost.internal:8080", "http://bifrost.internal:8080"},
		{"allowed_origin", "bifrost.internal:8080", "https://app.example.com"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			// fasthttp reuses one RequestCtx per connection; so does the benchmark.
			ctx := &fasthttp.RequestCtx{}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ctx.Request.Reset()
				ctx.ResetUserValues()
				ctx.Request.SetRequestURI("/api/providers")
				ctx.Request.Header.SetHost(tc.host)
				if tc.origin != "" {
					ctx.Request.Header.Set("Origin", tc.origin)
				}
				handler(ctx)
				if ctx.Response.StatusCode() == fasthttp.StatusForbidden {
					b.Fatalf("%s was refused: %s", tc.name, ctx.Response.Body())
				}
			}
		})
	}
}

// BenchmarkRequestDecompressionMiddleware measures the decompression middleware on a 4 KiB JSON
// request body sent uncompressed (the common case, which it passes straight through) and gzipped
// (decompressed under the expansion bound and the in-flight budget), plus a 10 MiB SDK-like body
// (about 1.5 MiB gzipped) and 1 MiB of zeros (about 1 KiB gzipped, the high-ratio extreme).
func BenchmarkRequestDecompressionMiddleware(b *testing.B) {
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{MaxRequestBodySizeMB: 100}}
	handler := RequestDecompressionMiddleware(config)(func(*fasthttp.RequestCtx) {})
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"` + strings.Repeat("hello world ", 340) + `"}]}`)
	compressed, err := gzipCompress(body)
	if err != nil {
		b.Fatal(err)
	}
	sdkCompressed, err := gzipCompress(sdkLikeJSONBody(b, 10*1024*1024))
	if err != nil {
		b.Fatal(err)
	}
	zeros, err := gzipCompress(make([]byte, 1024*1024))
	if err != nil {
		b.Fatal(err)
	}
	cases := []struct {
		name, encoding string
		payload        []byte
	}{
		{"uncompressed", "", body},
		{"gzip", "gzip", compressed},
		{"gzip-sdk-10MiB", "gzip", sdkCompressed},
		{"gzip-zeros-1MiB", "gzip", zeros},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			// fasthttp reuses one RequestCtx per connection; so does the benchmark.
			ctx := &fasthttp.RequestCtx{}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ctx.Request.Reset()
				ctx.Request.Header.SetMethod(fasthttp.MethodPost)
				ctx.Request.SetRequestURI("/v1/chat/completions")
				ctx.Request.Header.SetContentType("application/json")
				if tc.encoding != "" {
					ctx.Request.Header.Set("Content-Encoding", tc.encoding)
				}
				ctx.Request.SetBody(tc.payload)
				handler(ctx)
			}
		})
	}
}

// BenchmarkRequestDecompressionMiddlewareParallel sends a 10 MiB SDK-like body, about 1.5 MiB
// gzipped, through one middleware instance from many goroutines at once, with the handler held
// for 5ms to stand in for the upstream call the in-flight budget is held across. 503/op is the
// share of requests refused for budget; the body inflates to a tenth of the 100 MiB cap, so a
// budget of four times the cap should refuse none of them below about 40 concurrent.
func BenchmarkRequestDecompressionMiddlewareParallel(b *testing.B) {
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{MaxRequestBodySizeMB: 100}}
	compressed, err := gzipCompress(sdkLikeJSONBody(b, 10*1024*1024))
	if err != nil {
		b.Fatal(err)
	}
	for _, perProc := range []int{1, 2, 4} {
		goroutines := perProc * runtime.GOMAXPROCS(0)
		b.Run(fmt.Sprintf("concurrent-%d", goroutines), func(b *testing.B) {
			handler := RequestDecompressionMiddleware(config)(func(*fasthttp.RequestCtx) {
				time.Sleep(5 * time.Millisecond)
			})
			var total, refused atomic.Int64
			b.SetParallelism(perProc)
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				ctx := &fasthttp.RequestCtx{}
				for pb.Next() {
					ctx.Request.Reset()
					ctx.Response.Reset()
					ctx.Request.Header.SetMethod(fasthttp.MethodPost)
					ctx.Request.SetRequestURI("/v1/chat/completions")
					ctx.Request.Header.SetContentType("application/json")
					ctx.Request.Header.Set("Content-Encoding", "gzip")
					ctx.Request.SetBody(compressed)
					handler(ctx)
					total.Add(1)
					if ctx.Response.StatusCode() == fasthttp.StatusServiceUnavailable {
						refused.Add(1)
					}
				}
			})
			b.ReportMetric(float64(refused.Load())/float64(total.Load()), "503/op")
		})
	}
}

// A connector rebuilding the caller's parent context needs the vendor trace state, which
// rides tracestate rather than traceparent. It is captured as a trace attribute, not from
// RequestHeaders, which exist only when a plugin opted into header capture.
func TestTracingMiddleware_CapturesTraceState(t *testing.T) {
	SetLogger(&mockLogger{})

	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()
	mw := NewTracingMiddleware(tracer).Middleware()

	const traceState = "dd=s:2;t.llmobs_ml_app:checkout-agent"
	// Read inside the handler: the middleware completes the trace on the way out and the
	// pooled object is reset, so a pointer held past the call sees nil attributes.
	run := func(headers map[string]string) string {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/openai/v1/chat/completions")
		ctx.Request.Header.SetMethod("POST")
		for k, v := range headers {
			ctx.Request.Header.Set(k, v)
		}
		var got string
		mw(func(c *fasthttp.RequestCtx) {
			id, _ := c.UserValue(schemas.BifrostContextKeyTraceID).(string)
			if trace := store.GetTrace(id); trace != nil {
				v, _ := trace.GetAttribute(schemas.TraceAttrTraceState)
				got, _ = v.(string)
			}
		})(ctx)
		return got
	}

	if got := run(map[string]string{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"tracestate":  traceState,
	}); got != traceState {
		t.Errorf("tracestate attribute = %q, want %q", got, traceState)
	}

	// No traceparent means no parent to rebuild, so a lone tracestate is not stored.
	if got := run(map[string]string{"tracestate": traceState}); got != "" {
		t.Errorf("tracestate stored with no traceparent: %q", got)
	}
}

// TestLogRequestTargetRedactsAPIKeyQuery pins that a key sent in the query string never reaches the access log,
// including under a percent-encoded name.
func TestLogRequestTargetRedactsAPIKeyQuery(t *testing.T) {
	for uri, want := range map[string]string{
		"/voice-live/realtime?api-version=2026-07-15&model=gpt-realtime-vl&api-key=sk-bf-secret": "/voice-live/realtime?api-version=2026-07-15&model=gpt-realtime-vl&api-key=REDACTED",
		"/v1/chat/completions?stream=true":                                  "/v1/chat/completions?stream=true",
		"/voice-live/realtime?%61pi-key=sk-bf-secret&model=gpt-realtime-vl": "/voice-live/realtime?api-key=REDACTED&model=gpt-realtime-vl",
		"/azure/voice-live/realtime?api%2Dkey=sk-bf-secret":                 "/azure/voice-live/realtime?api-key=REDACTED",
	} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(uri)
		if got := logRequestTarget(ctx); got != want {
			t.Errorf("logRequestTarget(%q) = %q, want %q", uri, got, want)
		}
	}
}

// BenchmarkLogRequestTarget measures the access log target on a request without a query string, the common case.
func BenchmarkLogRequestTarget(b *testing.B) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/chat/completions")
	b.ReportAllocs()
	for b.Loop() {
		_ = logRequestTarget(ctx)
	}
}

// manyMCPParamHeaders is an Access-Control-Request-Headers value naming n mcp-param- headers.
func manyMCPParamHeaders(n int) string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("mcp-param-p%d", i)
	}
	return strings.Join(names, ", ")
}

// Echoing a preflight's mcp-param- names costs one allocation however many it lists, and a
// disallowed origin gets no CORS headers.
func TestCorsMiddleware_MCPParamHeadersAreBounded(t *testing.T) {
	requested := manyMCPParamHeaders(500)
	defaults, requestedBytes := corsAllowHeaders(nil), []byte(requested)
	if got, want := appendMCPParamHeaders(defaults, requestedBytes), defaults+", "+requested; got != want {
		t.Fatalf("every name must be echoed, in order: got %d bytes, want %d", len(got), len(want))
	}
	if allocs := testing.AllocsPerRun(20, func() { _ = appendMCPParamHeaders(defaults, requestedBytes) }); allocs > 1 {
		t.Fatalf("the list must be built once, not copied per name: %.0f allocs", allocs)
	}
	noParams := []byte("content-type, mcp-method")
	if allocs := testing.AllocsPerRun(20, func() { _ = appendMCPParamHeaders(defaults, noParams) }); allocs != 0 {
		t.Fatalf("a preflight asking for no mcp-param- names must not allocate: %.0f allocs", allocs)
	}

	SetLogger(&mockLogger{})
	handler := NewCorsMiddleware(&lib.Config{ClientConfig: &configstore.ClientConfig{AllowedOrigins: []string{"https://app.example.com"}}}).Middleware()(func(ctx *fasthttp.RequestCtx) {})
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(fasthttp.MethodOptions)
	ctx.Request.Header.Set("Origin", "https://evil.example.com")
	ctx.Request.Header.Set("Access-Control-Request-Headers", requested)
	handler(&ctx)
	if got := ctx.Response.Header.Peek("Access-Control-Allow-Headers"); len(got) != 0 {
		t.Fatalf("a disallowed origin must get no CORS headers, got %q", got)
	}
}

// BenchmarkCorsMiddleware measures the CORS headers on a cross-origin request, which every route
// pays, and on an MCP client's preflight.
func BenchmarkCorsMiddleware(b *testing.B) {
	SetLogger(&mockLogger{})
	handler := NewCorsMiddleware(&lib.Config{ClientConfig: &configstore.ClientConfig{AllowedOrigins: []string{"*"}}}).Middleware()(func(ctx *fasthttp.RequestCtx) {})
	cases := []struct {
		name    string
		method  string
		request string
	}{
		{"request", fasthttp.MethodPost, ""},
		{"preflight-mcp", fasthttp.MethodOptions, "content-type, mcp-protocol-version, mcp-method, mcp-name, mcp-param-region"},
		{"preflight-many-params", fasthttp.MethodOptions, manyMCPParamHeaders(500)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			var ctx fasthttp.RequestCtx
			ctx.Request.Header.SetMethod(tc.method)
			ctx.Request.Header.Set("Origin", "https://app.example.com")
			if tc.request != "" {
				ctx.Request.Header.Set("Access-Control-Request-Headers", tc.request)
			}
			b.ReportAllocs()
			for b.Loop() {
				ctx.Response.Reset()
				handler(&ctx)
			}
		})
	}
}

// basePathTestConfig returns a minimal Config with server.base_path set.
func basePathTestConfig(basePath string) *lib.Config {
	return &lib.Config{ClientConfig: &configstore.ClientConfig{}, ServerConfig: &lib.ServerConfig{BasePath: basePath}}
}

// TestBasePathMiddleware_StripsPrefix pins the URI rewrite: the prefix is removed
// on whole-segment matches only, the query survives, and Path() and
// PathOriginal() both see the stripped path.
func TestBasePathMiddleware_StripsPrefix(t *testing.T) {
	cases := []struct {
		uri, wantPath, wantQuery string
	}{
		{"/bifrost", "/", ""},
		{"/bifrost/", "/", ""},
		{"/bifrost/workspace/logs", "/workspace/logs", ""},
		{"/bifrost/api/logs?limit=10&offset=0", "/api/logs", "limit=10&offset=0"},
		{"/bifrost/v1/chat/completions", "/v1/chat/completions", ""},
		{"/bifrost/assets/index-abc.js", "/assets/index-abc.js", ""},
		// Unprefixed requests (a stripping proxy, a /health probe) pass through.
		{"/health", "/health", ""},
		{"/api/logs?limit=1", "/api/logs", "limit=1"},
		{"/", "/", ""},
		// Lookalike prefixes are not segment matches.
		{"/bifrostx/api/logs", "/bifrostx/api/logs", ""},
		{"/bifrost-ui", "/bifrost-ui", ""},
	}
	for _, tc := range cases {
		var gotPath, gotOriginal, gotQuery string
		handler := basePathMiddleware("/bifrost")(func(ctx *fasthttp.RequestCtx) {
			gotPath = string(ctx.Path())
			gotOriginal = string(ctx.Request.URI().PathOriginal())
			gotQuery = string(ctx.QueryArgs().QueryString())
		})
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(tc.uri)
		ctx.Request.Header.SetHost("abc.com")
		handler(ctx)
		assert.Equal(t, tc.wantPath, gotPath, tc.uri)
		assert.Equal(t, tc.wantPath, gotOriginal, tc.uri)
		assert.Equal(t, tc.wantQuery, gotQuery, tc.uri)
		assert.Equal(t, "abc.com", string(ctx.Host()), tc.uri)
	}
}

// TestBasePathMiddleware_NestedPrefix covers a multi-segment prefix.
func TestBasePathMiddleware_NestedPrefix(t *testing.T) {
	var gotPath string
	handler := basePathMiddleware("/tools/llm-gw")(func(ctx *fasthttp.RequestCtx) { gotPath = string(ctx.Path()) })
	for uri, want := range map[string]string{
		"/tools/llm-gw/api/version": "/api/version",
		"/tools/llm-gw":             "/",
		"/tools/api/version":        "/tools/api/version",
	} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(uri)
		handler(ctx)
		assert.Equal(t, want, gotPath, uri)
	}
}

// TestServerRootHandler_BasePathRouting runs prefixed and unprefixed requests
// through the production root chain and a real router, and checks the stripped
// path is the one the auth whitelist matches against.
func TestServerRootHandler_BasePathRouting(t *testing.T) {
	SetLogger(&mockLogger{})
	r := router.New()
	var hitPath string
	record := func(ctx *fasthttp.RequestCtx) { hitPath = string(ctx.Request.URI().PathOriginal()) }
	r.GET("/health", record)
	r.GET("/api/session/is-auth-enabled", record)
	r.GET("/{filepath:*}", func(ctx *fasthttp.RequestCtx) { hitPath = "ui:" + string(ctx.Path()) })

	handler := ServerRootHandler(newRecoveryTestCors(), basePathTestConfig("/bifrost"), r.Handler)
	for uri, want := range map[string]string{
		"/bifrost/api/session/is-auth-enabled": "/api/session/is-auth-enabled",
		"/api/session/is-auth-enabled":         "/api/session/is-auth-enabled",
		"/bifrost/health":                      "/health",
		"/health":                              "/health",
		"/bifrost/workspace/logs":              "ui:/workspace/logs",
		"/bifrost/login":                       "ui:/login",
		// The root chain strips the prefix exactly once.
		"/bifrost/bifrost/workspace": "ui:/bifrost/workspace",
	} {
		hitPath = ""
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(uri)
		ctx.Request.Header.SetMethod("GET")
		handler(ctx)
		assert.Equal(t, want, hitPath, uri)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/bifrost/login")
	basePathMiddleware("/bifrost")(func(ctx *fasthttp.RequestCtx) {
		assert.True(t, isAPIRouteWhitelistedAmong(string(ctx.Request.URI().PathOriginal()), nil), "stripped /login must match the system whitelist")
	})(ctx)

	// API responses under the prefix still get the /api/ no-store header.
	ctx = &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/bifrost/api/session/is-auth-enabled")
	ctx.Request.Header.SetMethod("GET")
	handler(ctx)
	assert.Equal(t, "no-store", string(ctx.Response.Header.Peek("Cache-Control")))
}

// TestBasePathMiddleware_RedirectsKeepPrefix pins that a same-origin redirect
// answering a prefixed request stays under the prefix. The router builds its
// trailing-slash and fixed-path redirects from the stripped path, so without
// this the browser would leave the proxy prefix. Redirects that already carry
// the prefix, point at another origin, or answer an unprefixed request are
// left alone.
func TestBasePathMiddleware_RedirectsKeepPrefix(t *testing.T) {
	SetLogger(&mockLogger{})
	r := router.New()
	r.GET("/api/providers", func(*fasthttp.RequestCtx) {})
	r.POST("/api/providers", func(*fasthttp.RequestCtx) {})
	r.GET("/handler/{target}", func(ctx *fasthttp.RequestCtx) {
		switch ctx.UserValue("target") {
		case "prefixed":
			ctx.Redirect("/bifrost/workspace", fasthttp.StatusFound)
		case "external":
			ctx.Redirect("https://idp.example.org/authorize?x=1", fasthttp.StatusFound)
		case "relative":
			ctx.Response.Header.Set("Location", "/workspace/logs?x=1")
			ctx.SetStatusCode(fasthttp.StatusFound)
		case "protocol-relative":
			ctx.Response.Header.Set("Location", "//evil.example.org/x")
			ctx.SetStatusCode(fasthttp.StatusFound)
		case "backslash":
			ctx.Response.Header.Set("Location", "/\\evil.example.org/x")
			ctx.SetStatusCode(fasthttp.StatusFound)
		case "lookalike":
			ctx.Redirect("/bifrostx/y", fasthttp.StatusFound)
		}
	})
	handler := ServerRootHandler(newRecoveryTestCors(), basePathTestConfig("/bifrost"), r.Handler)

	cases := []struct {
		method, uri string
		wantStatus  int
		wantLoc     string
	}{
		// Router-generated trailing-slash redirects.
		{"GET", "/bifrost/api/providers/", fasthttp.StatusMovedPermanently, "http://gw.example.com/bifrost/api/providers"},
		{"POST", "/bifrost/api/providers/?a=b", fasthttp.StatusPermanentRedirect, "http://gw.example.com/bifrost/api/providers?a=b"},
		// Router-generated fixed-path (case) redirect.
		{"GET", "/bifrost/API/Providers", fasthttp.StatusMovedPermanently, "http://gw.example.com/bifrost/api/providers"},
		// Unprefixed requests keep the unprefixed redirect.
		{"GET", "/api/providers/", fasthttp.StatusMovedPermanently, "http://gw.example.com/api/providers"},
		// Handler redirects.
		{"GET", "/bifrost/handler/prefixed", fasthttp.StatusFound, "http://gw.example.com/bifrost/workspace"},
		{"GET", "/bifrost/handler/external", fasthttp.StatusFound, "https://idp.example.org/authorize?x=1"},
		{"GET", "/bifrost/handler/relative", fasthttp.StatusFound, "/bifrost/workspace/logs?x=1"},
		{"GET", "/bifrost/handler/protocol-relative", fasthttp.StatusFound, "//evil.example.org/x"},
		{"GET", "/bifrost/handler/backslash", fasthttp.StatusFound, "/\\evil.example.org/x"},
		{"GET", "/bifrost/handler/lookalike", fasthttp.StatusFound, "http://gw.example.com/bifrost/bifrostx/y"},
	}
	for _, tc := range cases {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(tc.uri)
		ctx.Request.Header.SetMethod(tc.method)
		ctx.Request.Header.SetHost("gw.example.com")
		handler(ctx)
		assert.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), tc.method+" "+tc.uri)
		assert.Equal(t, tc.wantLoc, string(ctx.Response.Header.Peek("Location")), tc.method+" "+tc.uri)
	}
}

// TestServerRootHandler_NoBasePathNotWired pins that without a base path the
// prefix is never stripped, i.e. the middleware is not part of the chain.
func TestServerRootHandler_NoBasePathNotWired(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, config := range []*lib.Config{
		basePathTestConfig(""),
		{ClientConfig: &configstore.ClientConfig{}},
	} {
		var gotPath string
		handler := ServerRootHandler(newRecoveryTestCors(), config, func(ctx *fasthttp.RequestCtx) { gotPath = string(ctx.Path()) })
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/bifrost/api/logs")
		handler(ctx)
		assert.Equal(t, "/bifrost/api/logs", gotPath)
	}
}

// TestBasePathRootHandler pins the exported root wrapper that builds with their
// own root chain (enterprise) use in place of ServerRootHandler: it strips the
// configured prefix once, and without a base path it returns next unchanged.
func TestBasePathRootHandler(t *testing.T) {
	var gotPath string
	next := func(ctx *fasthttp.RequestCtx) { gotPath = string(ctx.Request.URI().PathOriginal()) }

	handler := BasePathRootHandler(basePathTestConfig("/bifrost"), next)
	for uri, want := range map[string]string{
		"/bifrost/api/logs":         "/api/logs",
		"/api/logs":                 "/api/logs",
		"/bifrost":                  "/",
		"/bifrostx/api/logs":        "/bifrostx/api/logs",
		"/bifrost/bifrost/api/logs": "/bifrost/api/logs",
	} {
		gotPath = ""
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(uri)
		handler(ctx)
		assert.Equal(t, want, gotPath, uri)
	}

	for _, config := range []*lib.Config{nil, {}, basePathTestConfig("")} {
		handler := BasePathRootHandler(config, next)
		assert.Equal(t, reflect.ValueOf(next).Pointer(), reflect.ValueOf(handler).Pointer(), "no base path: next is returned as is")
	}
}

// BenchmarkBasePathMiddleware measures the per-request cost of the prefix strip
// when a base path is configured (it is not in the chain otherwise).
func BenchmarkBasePathMiddleware(b *testing.B) {
	noop := func(*fasthttp.RequestCtx) {}
	handler := basePathMiddleware("/bifrost")(noop)
	for name, uri := range map[string]string{
		"prefixed":   "/bifrost/v1/chat/completions?stream=true",
		"unprefixed": "/v1/chat/completions?stream=true",
	} {
		b.Run(name, func(b *testing.B) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetHost("abc.com")
			b.ReportAllocs()
			for b.Loop() {
				ctx.Request.SetRequestURI(uri)
				handler(ctx)
			}
		})
	}
	b.Run("baseline", func(b *testing.B) {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetHost("abc.com")
		b.ReportAllocs()
		for b.Loop() {
			ctx.Request.SetRequestURI("/v1/chat/completions?stream=true")
			_ = ctx.Request.URI()
			noop(ctx)
		}
	})
}
