//go:build !tinygo && !wasm

package schemas

import (
	"github.com/valyala/fasthttp"
)

// BifrostHTTPMiddleware is a middleware function for the Bifrost HTTP transport.
// It follows the standard pattern: receives the next handler and returns a new handler.
// Used internally for CORS, Auth, Tracing middleware. Plugins use HTTPTransportIntercept instead.
type BifrostHTTPMiddleware func(next fasthttp.RequestHandler) fasthttp.RequestHandler

// LLMPluginShortCircuit represents a plugin's decision to short-circuit the normal flow.
// It can contain either a response (success short-circuit), a stream (streaming short-circuit), or an error (error short-circuit).
type LLMPluginShortCircuit struct {
	Response *BifrostResponse    // If set, short-circuit with this response (skips provider call)
	Stream   chan *BifrostStreamChunk // If set, short-circuit with this stream (skips provider call)
	Error    *BifrostError       // If set, short-circuit with this error (can set AllowFallbacks field)
}

// MCPPluginShortCircuit represents a plugin's decision to short-circuit the normal flow.
// It can contain either a response (success short-circuit), or an error (error short-circuit).
type MCPPluginShortCircuit struct {
	Response *BifrostMCPResponse // If set, short-circuit with this response (skips MCP call)
	Error    *BifrostError       // If set, short-circuit with this error (can set AllowFallbacks field)
}
