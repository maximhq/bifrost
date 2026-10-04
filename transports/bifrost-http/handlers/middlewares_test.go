package handlers

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	cryptoRand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/fasthttp/router"
	"github.com/klauspost/compress/zstd"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/temptoken"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
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
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Headers")) != "Content-Type, Authorization, X-Requested-With, X-Stainless-Timeout, X-Api-Key, X-OpenAI-Agents-SDK, X-Operation-ID" {
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
	expectedHeaders := "Content-Type, Authorization, X-Requested-With, X-Stainless-Timeout, X-Api-Key, X-OpenAI-Agents-SDK, X-Operation-ID"
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

	// Non-credentialed: wildcard is valid per spec
	actualHeaders := string(ctx.Response.Header.Peek("Access-Control-Allow-Headers"))
	if actualHeaders != "*" {
		t.Errorf("Expected Access-Control-Allow-Headers to be *, got %s", actualHeaders)
	}

	if !nextCalled {
		t.Error("Next handler was not called")
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
// carries x-request-id and x-bifrost-trace-id so callers can pivot a request into
// its logs and trace in Grafana/Tempo/Loki (BF-1041).
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
		if got := string(ctx.Response.Header.Peek("x-request-id")); got == "" {
			t.Error("expected x-request-id response header to be set")
		}
	})

	t.Run("echoes caller-supplied request id", func(t *testing.T) {
		ctx := newCtx()
		ctx.Request.Header.Set("x-request-id", "req-abc-123")
		mw(func(*fasthttp.RequestCtx) {})(ctx)

		if got := string(ctx.Response.Header.Peek("x-request-id")); got != "req-abc-123" {
			t.Errorf("x-request-id = %q, want req-abc-123", got)
		}
		if got := string(ctx.Response.Header.Peek("x-bifrost-trace-id")); got == "" {
			t.Error("expected x-bifrost-trace-id response header to be set")
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
	})
}

// TestTracingMiddleware_ProviderHeadersPreserveCorrelation exercises the real
// tracing/context/extraction/forwarding chain. Provider IDs must not replace the
// gateway IDs used by access logs and the exported W3C trace.
func TestTracingMiddleware_ProviderHeadersPreserveCorrelation(t *testing.T) {
	SetLogger(&mockLogger{})
	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()
	mw := NewTracingMiddleware(tracer).Middleware()
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	paths := []struct {
		name  string
		write func(*fasthttp.RequestCtx, *schemas.BifrostContext, map[string]string)
	}{
		{"success", func(c *fasthttp.RequestCtx, b *schemas.BifrostContext, headers map[string]string) {
			lib.ApplyBifrostResponseHeaders(c, b, schemas.BifrostResponseExtraFields{
				Provider: schemas.OpenAI, ProviderResponseHeaders: headers,
			})
			c.SetBodyString(`{"id":"chatcmpl-offline"}`)
		}},
		{"error", func(c *fasthttp.RequestCtx, b *schemas.BifrostContext, _ map[string]string) {
			forwardProviderHeadersFromContext(c, b)
			SendBifrostError(c, &schemas.BifrostError{
				StatusCode:  schemas.Ptr(fasthttp.StatusBadGateway),
				Error:       &schemas.ErrorField{Message: "offline provider error"},
				ExtraFields: schemas.BifrostErrorExtraFields{Provider: schemas.OpenAI},
			})
		}},
		{"stream_headers", func(c *fasthttp.RequestCtx, b *schemas.BifrostContext, _ map[string]string) {
			forwardProviderHeadersFromContext(c, b)
			lib.ApplyBifrostStreamResponseHeaders(c, b, schemas.ChatCompletionStreamRequest)
			c.SetContentType("text/event-stream")
		}},
	}
	for _, path := range paths {
		for _, suppliedID := range []bool{false, true} {
			for _, mixedCase := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/caller_id_%t/mixed_case_%t", path.name, suppliedID, mixedCase), func(t *testing.T) {
					ctx := &fasthttp.RequestCtx{}
					ctx.Request.SetRequestURI("/v1/chat/completions")
					ctx.Request.Header.SetMethod("POST")
					ctx.Request.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
					if suppliedID {
						ctx.Request.Header.Set("x-request-id", "caller-request-7592")
					}
					var gatewayRequestID string
					mw(func(c *fasthttp.RequestCtx) {
						bifrostCtx, cancel := lib.ConvertToBifrostContext(c, nil)
						defer cancel()
						gatewayRequestID, _ = bifrostCtx.Value(schemas.BifrostContextKeyRequestID).(string)
						if gatewayRequestID == "" || (suppliedID && gatewayRequestID != "caller-request-7592") {
							t.Fatalf("gateway request identity was not established: %q", gatewayRequestID)
						}
						if got := c.UserValue(schemas.BifrostContextKeyExportTraceID); got != traceID {
							t.Fatalf("exported W3C trace = %v, want %s", got, traceID)
						}
						// A real upstream response goes through the same extraction helper
						// providers use; disable normalization to retain mixed-case names.
						upstream := &fasthttp.Response{}
						upstream.Header.DisableNormalizing()
						requestHeader, traceHeader := "x-request-id", "x-bifrost-trace-id"
						if mixedCase {
							requestHeader, traceHeader = "X-ReQuEsT-Id", "X-BiFrOsT-TrAcE-Id"
						}
						upstream.Header.Set(requestHeader, "provider-request-7592")
						upstream.Header.Set(traceHeader, "provider-trace-7592")
						upstream.Header.Set("x-amzn-requestid", "provider-specific-7592")
						upstream.Header.Set("x-ratelimit-remaining-requests", "19")
						headers := providerUtils.ExtractProviderResponseHeaders(upstream)
						bifrostCtx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)
						path.write(c, bifrostCtx, headers)
					})(ctx)

					if got := string(ctx.Response.Header.Peek("x-request-id")); got != gatewayRequestID {
						t.Errorf("x-request-id = %q, want gateway request %q", got, gatewayRequestID)
					}
					if got := string(ctx.Response.Header.Peek("x-bifrost-trace-id")); got != traceID {
						t.Errorf("x-bifrost-trace-id = %q, want exported W3C trace %q", got, traceID)
					}
					for key, want := range map[string]string{"x-amzn-requestid": "provider-specific-7592", "x-ratelimit-remaining-requests": "19"} {
						if got := string(ctx.Response.Header.Peek(key)); got != want {
							t.Errorf("provider header %s = %q, want %q", key, got, want)
						}
					}
				})
			}
		}
	}
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

func TestTransportHooks_CanExplicitlyReplaceCorrelation(t *testing.T) {
	SetLogger(&mockLogger{})
	store := tracing.NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := tracing.NewTracer(store, nil, nil)
	defer tracer.Stop()
	var postSawGateway, headersSawPost bool
	plugin := &fakeResponseHeadersPlugin{
		name: "explicit-correlation",
		postHook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
			for key, value := range resp.Headers {
				if strings.EqualFold(key, "x-request-id") {
					postSawGateway = value == "caller-request"
					resp.Headers[key] = "plugin-request"
				}
			}
			return nil
		},
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
			headersSawPost = resp.Header("x-request-id") == "plugin-request"
			resp.SetHeader("x-bifrost-trace-id", "plugin-trace")
			return nil
		},
	}
	handler := NewTracingMiddleware(tracer).Middleware()(TransportInterceptorMiddleware(responseHeadersTestConfig(t, plugin))(func(ctx *fasthttp.RequestCtx) {
		lib.ApplyBifrostResponseHeaders(ctx, nil, schemas.BifrostResponseExtraFields{
			ProviderResponseHeaders: map[string]string{"x-request-id": "provider-request", "x-bifrost-trace-id": "provider-trace"},
		})
		ctx.SetBodyString("ok")
	}))
	ctx := preAuthTestCtx()
	ctx.Request.Header.Set("x-request-id", "caller-request")
	handler(ctx)
	if !postSawGateway || !headersSawPost {
		t.Fatal("transport hooks did not observe gateway identity followed by the explicit post-hook replacement")
	}
	if string(ctx.Response.Header.Peek("x-request-id")) != "plugin-request" || string(ctx.Response.Header.Peek("x-bifrost-trace-id")) != "plugin-trace" {
		t.Fatal("explicit transport plugin correlation replacements must remain supported")
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
	}, AuthBypassedMiddleware())

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
