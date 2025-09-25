package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// TestCorsMiddleware_LocalhostOrigins tests that localhost origins are always allowed
func TestCorsMiddleware_LocalhostOrigins(t *testing.T) {
	config := &lib.Config{
		ClientConfig: configstore.ClientConfig{
			AllowedOrigins: []string{},
		},
	}

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
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Methods")) != "GET, POST, PUT, DELETE, OPTIONS" {
				t.Errorf("Access-Control-Allow-Methods header not set correctly")
			}
			if string(ctx.Response.Header.Peek("Access-Control-Allow-Headers")) != "Content-Type, Authorization, X-Requested-With" {
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

// TestChainMiddlewares_NoMiddlewares tests chaining with no middlewares
func TestChainMiddlewares_NoMiddlewares(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	handlerCalled := false

	handler := func(ctx *fasthttp.RequestCtx) {
		handlerCalled = true
	}

	chained := ChainMiddlewares(handler)
	chained(ctx)

	if !handlerCalled {
		t.Error("Handler was not called when no middlewares are present")
	}
}

// TestChainMiddlewares_SingleMiddleware tests chaining with a single middleware
func TestChainMiddlewares_SingleMiddleware(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	middlewareCalled := false
	handlerCalled := false

	middleware := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			middlewareCalled = true
			next(ctx)
		}
	})

	handler := func(ctx *fasthttp.RequestCtx) {
		handlerCalled = true
	}

	chained := ChainMiddlewares(handler, middleware)
	chained(ctx)

	if !middlewareCalled {
		t.Error("Middleware was not called")
	}
	if !handlerCalled {
		t.Error("Handler was not called")
	}
}

// TestChainMiddlewares_MultipleMiddlewares tests chaining with multiple middlewares
func TestChainMiddlewares_MultipleMiddlewares(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	executionOrder := []int{}

	middleware1 := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 1)
			next(ctx)
		}
	})

	middleware2 := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 2)
			next(ctx)
		}
	})

	middleware3 := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 3)
			next(ctx)
		}
	})

	handler := func(ctx *fasthttp.RequestCtx) {
		executionOrder = append(executionOrder, 4)
	}

	chained := ChainMiddlewares(handler, middleware1, middleware2, middleware3)
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

// TestChainMiddlewares_MiddlewareCanModifyContext tests that middlewares can modify the context
func TestChainMiddlewares_MiddlewareCanModifyContext(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	middleware := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
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

	chained := ChainMiddlewares(handler, middleware)
	chained(ctx)
}

// TestChainMiddlewares_ShortCircuit tests that when a middleware writes a response
// and does not call next, subsequent middlewares and handler do not execute.
func TestChainMiddlewares_ShortCircuit(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	executionOrder := []int{}

	// First middleware - writes response and short-circuits by not calling next
	middleware1 := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 1)
			ctx.SetStatusCode(fasthttp.StatusUnauthorized)
			ctx.SetBodyString("Unauthorized")
			// Not calling next(ctx) to short-circuit
		}
	})

	// Second middleware - should NOT execute when middleware1 short-circuits
	middleware2 := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 2)
			next(ctx)
		}
	})

	// Third middleware - should NOT execute when middleware1 short-circuits
	middleware3 := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
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

	chained := ChainMiddlewares(handler, middleware1, middleware2, middleware3)
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

// TestChainMiddlewares_ShortCircuitMiddlePosition tests that middleware in the middle
// can short-circuit, preventing later middlewares and handler from executing.
func TestChainMiddlewares_ShortCircuitMiddlePosition(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	executionOrder := []int{}

	// First middleware - executes and calls next
	middleware1 := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 1)
			next(ctx)
		}
	})

	// Second middleware - writes response and short-circuits
	middleware2 := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			executionOrder = append(executionOrder, 2)
			ctx.SetStatusCode(fasthttp.StatusUnauthorized)
			ctx.SetBodyString("Unauthorized")
			// Not calling next(ctx) to short-circuit
		}
	})

	// Third middleware - should NOT execute
	middleware3 := BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
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

	chained := ChainMiddlewares(handler, middleware1, middleware2, middleware3)
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
