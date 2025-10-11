// Package integrations provides a generic router framework for handling different LLM provider APIs.
//
// CENTRALIZED STREAMING ARCHITECTURE:
//
// This package implements a centralized streaming approach where all stream handling logic
// is consolidated in the GenericRouter, eliminating the need for provider-specific StreamHandler
// implementations. The key components are:
//
// 1. StreamConfig: Defines streaming configuration for each route, including:
//   - ResponseConverter: Converts BifrostResponse to provider-specific streaming format
//   - ErrorConverter: Converts BifrostError to provider-specific streaming error format
//
// 2. Centralized Stream Processing: The GenericRouter handles all streaming logic:
//   - SSE header management
//   - Stream channel processing
//   - Error handling and conversion
//   - Response formatting and flushing
//   - Stream closure (handled automatically by provider implementation)
//
// 3. Provider-Specific Type Conversion: Integration types.go files only handle type conversion:
//   - Derive{Provider}StreamFromBifrostResponse: Convert responses to streaming format
//   - Derive{Provider}StreamFromBifrostError: Convert errors to streaming error format
//
// BENEFITS:
// - Eliminates code duplication across provider-specific stream handlers
// - Centralizes streaming logic for consistency and maintainability
// - Separates concerns: routing logic vs type conversion
// - Automatic stream closure management by provider implementations
// - Consistent error handling across all providers
//
// USAGE EXAMPLE:
//
//	routes := []RouteConfig{
//	  {
//	    Path: "/openai/chat/completions",
//	    Method: "POST",
//	    // ... other configs ...
//	    StreamConfig: &StreamConfig{
//	      ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
//	        return DeriveOpenAIStreamFromBifrostResponse(resp), nil
//	      },
//	      ErrorConverter: func(err *schemas.BifrostError) interface{} {
//	        return DeriveOpenAIStreamFromBifrostError(err)
//	      },
//	    },
//	  },
//	}
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"

	"bufio"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ExtensionRouter defines the interface that all integration routers must implement
// to register their routes with the main HTTP router.
type ExtensionRouter interface {
	RegisterRoutes(r *router.Router, middlewares ...lib.BifrostHTTPMiddleware)
}

// StreamingRequest interface for requests that support streaming
type StreamingRequest interface {
	IsStreamingRequested() bool
}

// RequestConverter is a function that converts integration-specific requests to Bifrost format.
// It takes the parsed request object and returns a BifrostRequest ready for processing.
type RequestConverter func(req interface{}) (*schemas.BifrostRequest, error)

// ResponseConverter is a function that converts Bifrost responses to integration-specific format.
// It takes a BifrostResponse and returns the format expected by the specific integration.
type ResponseConverter func(*schemas.BifrostResponse) (interface{}, error)

// StreamResponseConverter is a function that converts Bifrost responses to integration-specific streaming format.
// It takes a BifrostResponse and returns the streaming format expected by the specific integration.
type StreamResponseConverter func(*schemas.BifrostResponse) (interface{}, error)

// ErrorConverter is a function that converts BifrostError to integration-specific format.
// It takes a BifrostError and returns the format expected by the specific integration.
type ErrorConverter func(*schemas.BifrostError) interface{}

// StreamErrorConverter is a function that converts BifrostError to integration-specific streaming error format.
// It takes a BifrostError and returns the streaming error format expected by the specific integration.
type StreamErrorConverter func(*schemas.BifrostError) interface{}

// RequestParser is a function that handles custom request body parsing.
// It replaces the default JSON parsing when configured (e.g., for multipart/form-data).
// The parser should populate the provided request object from the fasthttp context.
// If it returns an error, the request processing stops.
type RequestParser func(ctx *fasthttp.RequestCtx, req interface{}) error

// PreRequestCallback is called after parsing the request but before processing through Bifrost.
// It can be used to modify the request object (e.g., extract model from URL parameters)
// or perform validation. If it returns an error, the request processing stops.
type PreRequestCallback func(ctx *fasthttp.RequestCtx, req interface{}) error

// PostRequestCallback is called after processing the request but before sending the response.
// It can be used to modify the response or perform additional logging/metrics.
// If it returns an error, an error response is sent instead of the success response.
type PostRequestCallback func(ctx *fasthttp.RequestCtx, req interface{}, resp *schemas.BifrostResponse) error

// StreamConfig defines streaming-specific configuration for an integration
//
// SSE FORMAT BEHAVIOR:
//
// The ResponseConverter and ErrorConverter functions in StreamConfig can return either:
//
// 1. OBJECTS (interface{} that's not a string):
//   - Will be JSON marshaled and sent as standard SSE: data: {json}\n\n
//   - Use this for most providers (OpenAI, Google, etc.)
//   - Example: return map[string]interface{}{"delta": {"content": "hello"}}
//   - Result: data: {"delta":{"content":"hello"}}\n\n
//
// 2. STRINGS:
//   - Will be sent directly as-is without any modification
//   - Use this for providers requiring custom SSE event types (Anthropic, etc.)
//   - Example: return "event: content_block_delta\ndata: {\"type\":\"text\"}\n\n"
//   - Result: event: content_block_delta
//     data: {"type":"text"}
//
// Choose the appropriate return type based on your provider's SSE specification.
type StreamConfig struct {
	ResponseConverter StreamResponseConverter // Function to convert BifrostResponse to streaming format
	ErrorConverter    StreamErrorConverter    // Function to convert BifrostError to streaming error format
}

// RouteConfig defines the configuration for a single route in an integration.
// It specifies the path, method, and handlers for request/response conversion.
type RouteConfig struct {
	Path                   string              // HTTP path pattern (e.g., "/openai/v1/chat/completions")
	Method                 string              // HTTP method (POST, GET, PUT, DELETE)
	GetRequestTypeInstance func() interface{}  // Factory function to create request instance (SHOULD NOT BE NIL)
	RequestParser          RequestParser       // Optional: custom request parsing (e.g., multipart/form-data)
	RequestConverter       RequestConverter    // Function to convert request to BifrostRequest (SHOULD NOT BE NIL)
	ResponseConverter      ResponseConverter   // Function to convert BifrostResponse to integration format (SHOULD NOT BE NIL)
	ErrorConverter         ErrorConverter      // Function to convert BifrostError to integration format (SHOULD NOT BE NIL)
	StreamConfig           *StreamConfig       // Optional: Streaming configuration (if nil, streaming not supported)
	PreCallback            PreRequestCallback  // Optional: called after parsing but before Bifrost processing
	PostCallback           PostRequestCallback // Optional: called after request processing
}

// GenericRouter provides a reusable router implementation for all integrations.
// It handles the common flow of: parse request → convert to Bifrost → execute → convert response.
// Integration-specific logic is handled through the RouteConfig callbacks and converters.
type GenericRouter struct {
	client       *bifrost.Bifrost // Bifrost client for executing requests
	handlerStore lib.HandlerStore // Config provider for the router
	routes       []RouteConfig    // List of route configurations
}

// NewGenericRouter creates a new generic router with the given bifrost client and route configurations.
// Each integration should create their own routes and pass them to this constructor.
func NewGenericRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, routes []RouteConfig) *GenericRouter {
	return &GenericRouter{
		client:       client,
		handlerStore: handlerStore,
		routes:       routes,
	}
}

// RegisterRoutes registers all configured routes on the given fasthttp router.
// This method implements the ExtensionRouter interface.
func (g *GenericRouter) RegisterRoutes(r *router.Router, middlewares ...lib.BifrostHTTPMiddleware) {
	for _, route := range g.routes {
		// Validate route configuration at startup to fail fast
		if route.GetRequestTypeInstance == nil {
			log.Println("[WARN] route configuration is invalid: GetRequestTypeInstance cannot be nil for route " + route.Path)
			continue
		}
		if route.RequestConverter == nil {
			log.Println("[WARN] route configuration is invalid: RequestConverter cannot be nil for route " + route.Path)
			continue
		}
		if route.ResponseConverter == nil {
			log.Println("[WARN] route configuration is invalid: ResponseConverter cannot be nil for route " + route.Path)
			continue
		}
		if route.ErrorConverter == nil {
			log.Println("[WARN] route configuration is invalid: ErrorConverter cannot be nil for route " + route.Path)
			continue
		}

		// Test that GetRequestTypeInstance returns a valid instance
		if testInstance := route.GetRequestTypeInstance(); testInstance == nil {
			log.Println("[WARN] route configuration is invalid: GetRequestTypeInstance returned nil for route " + route.Path)
			continue
		}

		handler := g.createHandler(route)
		switch strings.ToUpper(route.Method) {
		case fasthttp.MethodPost:
			r.POST(route.Path, lib.ChainMiddlewares(handler, middlewares...))
		case fasthttp.MethodGet:
			r.GET(route.Path, lib.ChainMiddlewares(handler, middlewares...))
		case fasthttp.MethodPut:
			r.PUT(route.Path, lib.ChainMiddlewares(handler, middlewares...))
		case fasthttp.MethodDelete:
			r.DELETE(route.Path, lib.ChainMiddlewares(handler, middlewares...))
		default:
			r.POST(route.Path, lib.ChainMiddlewares(handler, middlewares...)) // Default to POST
		}
	}
}

// createHandler creates a fasthttp handler for the given route configuration.
// The handler follows this flow:
// 1. Parse JSON request body into the configured request type (for methods that expect bodies)
// 2. Execute pre-callback (if configured) for request modification/validation
// 3. Convert request to BifrostRequest using the configured converter
// 4. Execute the request through Bifrost (streaming or non-streaming)
// 5. Execute post-callback (if configured) for response modification
// 6. Convert and send the response using the configured response converter
func (g *GenericRouter) createHandler(config RouteConfig) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		// Parse request body into the integration-specific request type
		// Note: config validation is performed at startup in RegisterRoutes
		req := config.GetRequestTypeInstance()

		method := string(ctx.Method())

		// Parse request body based on configuration
		if method != fasthttp.MethodGet && method != fasthttp.MethodDelete {
			if config.RequestParser != nil {
				// Use custom parser (e.g., for multipart/form-data)
				if err := config.RequestParser(ctx, req); err != nil {
					g.sendError(ctx, config.ErrorConverter, newBifrostError(err, "failed to parse request"))
					return
				}
			} else {
				// Use default JSON parsing
				body := ctx.Request.Body()
				if len(body) > 0 {
					if err := json.Unmarshal(body, req); err != nil {
						g.sendError(ctx, config.ErrorConverter, newBifrostError(err, "Invalid JSON"))
						return
					}
				}
			}
		}

		// Execute pre-request callback if configured
		// This is typically used for extracting data from URL parameters
		// or performing request validation after parsing
		if config.PreCallback != nil {
			if err := config.PreCallback(ctx, req); err != nil {
				g.sendError(ctx, config.ErrorConverter, newBifrostError(err, "failed to execute pre-request callback: "+err.Error()))
				return
			}
		}

		// Convert the integration-specific request to Bifrost format
		bifrostReq, err := config.RequestConverter(req)
		if err != nil {
			g.sendError(ctx, config.ErrorConverter, newBifrostError(err, "failed to convert request to Bifrost format"))
			return
		}
		if bifrostReq == nil {
			g.sendError(ctx, config.ErrorConverter, newBifrostError(nil, "Invalid request"))
			return
		}

		// Extract and parse fallbacks from the request if present
		if err := g.extractAndParseFallbacks(req, bifrostReq); err != nil {
			g.sendError(ctx, config.ErrorConverter, newBifrostError(err, "failed to parse fallbacks: "+err.Error()))
			return
		}

		// Check if streaming is requested
		isStreaming := false
		if streamingReq, ok := req.(StreamingRequest); ok {
			isStreaming = streamingReq.IsStreamingRequested()
		}

		// Execute the request through Bifrost
		bifrostCtx := lib.ConvertToBifrostContext(ctx, g.handlerStore.ShouldAllowDirectKeys())

		if ctx.UserValue(string(schemas.BifrostContextKeyDirectKey)) != nil {
			key, ok := ctx.UserValue(string(schemas.BifrostContextKeyDirectKey)).(schemas.Key)
			if ok {
				*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyDirectKey, key)
			}
		}

		if isStreaming {
			g.handleStreamingRequest(ctx, config, bifrostReq, bifrostCtx)
		} else {
			g.handleNonStreamingRequest(ctx, config, req, bifrostReq, bifrostCtx)
		}
	}
}

// handleNonStreamingRequest handles regular (non-streaming) requests
func (g *GenericRouter) handleNonStreamingRequest(ctx *fasthttp.RequestCtx, config RouteConfig, req interface{}, bifrostReq *schemas.BifrostRequest, bifrostCtx *context.Context) {
	var result *schemas.BifrostResponse
	var bifrostErr *schemas.BifrostError

	// Handle different request types
	if bifrostReq.TextCompletionRequest != nil {
		result, bifrostErr = g.client.TextCompletionRequest(*bifrostCtx, bifrostReq.TextCompletionRequest)
	} else if bifrostReq.ChatRequest != nil {
		result, bifrostErr = g.client.ChatCompletionRequest(*bifrostCtx, bifrostReq.ChatRequest)
	} else if bifrostReq.EmbeddingRequest != nil {
		result, bifrostErr = g.client.EmbeddingRequest(*bifrostCtx, bifrostReq.EmbeddingRequest)
	} else if bifrostReq.SpeechRequest != nil {
		result, bifrostErr = g.client.SpeechRequest(*bifrostCtx, bifrostReq.SpeechRequest)
	} else if bifrostReq.TranscriptionRequest != nil {
		result, bifrostErr = g.client.TranscriptionRequest(*bifrostCtx, bifrostReq.TranscriptionRequest)
	} else if bifrostReq.ResponsesRequest != nil {
		result, bifrostErr = g.client.ResponsesRequest(*bifrostCtx, bifrostReq.ResponsesRequest)
	}

	// Handle errors
	if bifrostErr != nil {
		g.sendError(ctx, config.ErrorConverter, bifrostErr)
		return
	}

	// Execute post-request callback if configured
	// This is typically used for response modification or additional processing
	if config.PostCallback != nil {
		if err := config.PostCallback(ctx, req, result); err != nil {
			g.sendError(ctx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
			return
		}
	}

	if result == nil {
		g.sendError(ctx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
		return
	}

	// Convert Bifrost response to integration-specific format and send
	response, err := config.ResponseConverter(result)
	if err != nil {
		g.sendError(ctx, config.ErrorConverter, newBifrostError(err, "failed to encode response"))
		return
	}

	if result.Speech != nil {
		responseBytes, ok := response.([]byte)
		if ok {
			ctx.Response.Header.Set("Content-Type", "audio/mpeg")
			ctx.Response.Header.Set("Content-Disposition", "attachment; filename=speech.mp3")
			ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(responseBytes)))
			ctx.Response.SetBody(responseBytes)
			return
		}
	}

	g.sendSuccess(ctx, config.ErrorConverter, response)
}

// handleStreamingRequest handles streaming requests using Server-Sent Events (SSE)
func (g *GenericRouter) handleStreamingRequest(ctx *fasthttp.RequestCtx, config RouteConfig, bifrostReq *schemas.BifrostRequest, bifrostCtx *context.Context) {
	// Set common SSE headers
	ctx.SetContentType("text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")
	ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")

	var stream chan *schemas.BifrostStream
	var bifrostErr *schemas.BifrostError

	// Handle different request types
	if bifrostReq.TextCompletionRequest != nil {
		stream, bifrostErr = g.client.TextCompletionStreamRequest(*bifrostCtx, bifrostReq.TextCompletionRequest)
	} else if bifrostReq.ChatRequest != nil {
		stream, bifrostErr = g.client.ChatCompletionStreamRequest(*bifrostCtx, bifrostReq.ChatRequest)
	} else if bifrostReq.SpeechRequest != nil {
		stream, bifrostErr = g.client.SpeechStreamRequest(*bifrostCtx, bifrostReq.SpeechRequest)
	} else if bifrostReq.TranscriptionRequest != nil {
		stream, bifrostErr = g.client.TranscriptionStreamRequest(*bifrostCtx, bifrostReq.TranscriptionRequest)
	} else if bifrostReq.ResponsesRequest != nil {
		stream, bifrostErr = g.client.ResponsesStreamRequest(*bifrostCtx, bifrostReq.ResponsesRequest)
	}

	// Get the streaming channel from Bifrost
	if bifrostErr != nil {
		// Send error in SSE format
		g.sendStreamError(ctx, config, bifrostErr)
		return
	}

	// Check if streaming is configured for this route
	if config.StreamConfig == nil {
		g.sendStreamError(ctx, config, newBifrostError(nil, "streaming is not supported for this integration"))
		return
	}

	// Handle streaming using the centralized approach
	g.handleStreaming(ctx, config, stream)
}

// handleStreaming processes a stream of BifrostResponse objects and sends them as Server-Sent Events (SSE).
// It handles both successful responses and errors in the streaming format.
//
// SSE FORMAT HANDLING:
//
// By default, all responses and errors are sent in the standard SSE format:
//
//	data: {"response": "content"}\n\n
//
// However, some providers (like Anthropic) require custom SSE event formats with explicit event types:
//
//	event: content_block_delta
//	data: {"type": "content_block_delta", "delta": {...}}
//
//	event: message_stop
//	data: {"type": "message_stop"}
//
// STREAMCONFIG CONVERTER BEHAVIOR:
//
// The StreamConfig.ResponseConverter and StreamConfig.ErrorConverter functions can return:
//
// 1. OBJECTS (default behavior):
//   - Return any Go struct/map/interface{}
//   - Will be JSON marshaled and wrapped as: data: {json}\n\n
//   - Example: return map[string]interface{}{"content": "hello"}
//   - Result: data: {"content":"hello"}\n\n
//
// 2. STRINGS (custom SSE format):
//   - Return a complete SSE string with custom event types and formatting
//   - Will be sent directly without any wrapping or modification
//   - Example: return "event: content_block_delta\ndata: {\"type\":\"text\"}\n\n"
//   - Result: event: content_block_delta
//     data: {"type":"text"}
//
// IMPLEMENTATION GUIDELINES:
//
// For standard providers (OpenAI, etc.): Return objects from converters
// For custom SSE providers (Anthropic, etc.): Return pre-formatted SSE strings
//
// When returning strings, ensure they:
// - Include proper event: lines (if needed)
// - Include data: lines with JSON content
// - End with \n\n for proper SSE formatting
// - Follow the provider's specific SSE event specification
func (g *GenericRouter) handleStreaming(ctx *fasthttp.RequestCtx, config RouteConfig, streamChan chan *schemas.BifrostStream) {
	// Use streaming response writer
	ctx.Response.SetBodyStreamWriter(func(w *bufio.Writer) {
		defer w.Flush()

		var requestType schemas.RequestType

		// Process streaming responses
		for response := range streamChan {
			if response == nil {
				continue
			}

			// Extract request type from the first response for stream format detection
			if requestType == "" {
				if response.BifrostResponse != nil {
					requestType = response.BifrostResponse.ExtraFields.RequestType
				} else if response.BifrostError != nil {
					requestType = response.BifrostError.ExtraFields.RequestType
				}
			}

			// Check for context cancellation
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Handle errors
			if response.BifrostError != nil {
				var errorResponse interface{}
				var errorJSON []byte
				var err error

				// Use stream error converter if available, otherwise fallback to regular error converter
				if config.StreamConfig != nil && config.StreamConfig.ErrorConverter != nil {
					errorResponse = config.StreamConfig.ErrorConverter(response.BifrostError)
				} else if config.ErrorConverter != nil {
					errorResponse = config.ErrorConverter(response.BifrostError)
				} else {
					// Default error response
					errorResponse = map[string]interface{}{
						"error": map[string]interface{}{
							"type":    "internal_error",
							"message": "An error occurred while processing your request",
						},
					}
				}

				// Check if the error converter returned a raw SSE string or JSON object
				if sseErrorString, ok := errorResponse.(string); ok {
					// CUSTOM SSE FORMAT: The converter returned a complete SSE string
					// This is used by providers like Anthropic that need custom event types
					// Example: "event: error\ndata: {...}\n\n"
					if _, err := fmt.Fprint(w, sseErrorString); err != nil {
						return
					}
				} else {
					// STANDARD SSE FORMAT: The converter returned an object
					// This will be JSON marshaled and wrapped as "data: {json}\n\n"
					// Used by most providers (OpenAI, Google, etc.)
					errorJSON, err = json.Marshal(errorResponse)
					if err != nil {
						// Fallback to basic error if marshaling fails
						basicError := map[string]interface{}{
							"error": map[string]interface{}{
								"type":    "internal_error",
								"message": "An error occurred while processing your request",
							},
						}
						if errorJSON, err = json.Marshal(basicError); err != nil {
							return // Can't even send basic error
						}
					}

					// Send error as SSE data
					if _, err := fmt.Fprintf(w, "data: %s\n\n", errorJSON); err != nil {
						return
					}
				}

				// Flush and return on error
				if err := w.Flush(); err != nil {
					return
				}
				return // End stream on error
			}

			// Handle successful responses
			if response.BifrostResponse != nil {
				// Convert response to integration-specific streaming format
				var convertedResponse interface{}
				var err error

				if config.StreamConfig.ResponseConverter != nil {
					convertedResponse, err = config.StreamConfig.ResponseConverter(response.BifrostResponse)
				} else {
					// Fallback to regular response converter
					convertedResponse, err = config.ResponseConverter(response.BifrostResponse)
				}

				if err != nil {
					// Log conversion error but continue processing
					log.Printf("Failed to convert streaming response: %v", err)
					continue
				}

				// Check if the converter returned a raw SSE string or JSON object
				if sseString, ok := convertedResponse.(string); ok {
					// CUSTOM SSE FORMAT: The converter returned a complete SSE string
					// This is used by providers like Anthropic that need custom event types
					// Example: "event: content_block_delta\ndata: {...}\n\n"
					if _, err := fmt.Fprint(w, sseString); err != nil {
						return // Network error, stop streaming
					}
				} else {
					// Handle different streaming formats based on request type
					if requestType == schemas.ResponsesStreamRequest {
						// OPENAI RESPONSES FORMAT: Use event: and data: lines for OpenAI responses API compatibility
						eventType := ""
						if response.BifrostResponse.ResponsesStreamResponse != nil {
							eventType = string(response.BifrostResponse.ResponsesStreamResponse.Type)
						}

						// Send event line if available
						if eventType != "" {
							if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
								return // Network error, stop streaming
							}
						}

						// Send data line
						responseJSON, err := json.Marshal(convertedResponse)
						if err != nil {
							// Log JSON marshaling error but continue processing
							log.Printf("Failed to marshal streaming response: %v", err)
							continue
						}

						if _, err := fmt.Fprintf(w, "data: %s\n\n", responseJSON); err != nil {
							return // Network error, stop streaming
						}
					} else {
						// STANDARD SSE FORMAT: The converter returned an object
						// This will be JSON marshaled and wrapped as "data: {json}\n\n"
						// Used by most providers (OpenAI chat/completions, Google, etc.)
						responseJSON, err := json.Marshal(convertedResponse)
						if err != nil {
							// Log JSON marshaling error but continue processing
							log.Printf("Failed to marshal streaming response: %v", err)
							continue
						}

						// Send as SSE data
						if _, err := fmt.Fprintf(w, "data: %s\n\n", responseJSON); err != nil {
							return // Network error, stop streaming
						}
					}
				}

				// Flush immediately to send the chunk
				if err := w.Flush(); err != nil {
					return // Network error, stop streaming
				}
			}
		}

		// Send [DONE] marker only for non-responses APIs (OpenAI responses API doesn't use [DONE])
		if requestType != schemas.ResponsesStreamRequest {
			if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
				log.Printf("Failed to write SSE done marker: %v", err)
			}
		}
		// Note: OpenAI responses API doesn't use [DONE] marker, it ends when the stream closes
	})
}

// sendStreamError sends an error in streaming format using the stream error converter if available
func (g *GenericRouter) sendStreamError(ctx *fasthttp.RequestCtx, config RouteConfig, bifrostErr *schemas.BifrostError) {
	var errorResponse interface{}

	// Use stream error converter if available, otherwise fallback to regular error converter
	if config.StreamConfig != nil && config.StreamConfig.ErrorConverter != nil {
		errorResponse = config.StreamConfig.ErrorConverter(bifrostErr)
	} else {
		errorResponse = config.ErrorConverter(bifrostErr)
	}

	errorJSON, err := json.Marshal(map[string]interface{}{
		"error": errorResponse,
	})
	if err != nil {
		log.Printf("Failed to marshal error for SSE: %v", err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		return
	}

	if _, err := fmt.Fprintf(ctx, "data: %s\n\n", errorJSON); err != nil {
		log.Printf("Failed to write SSE error: %v", err)
	}
}

// sendError sends an error response with the appropriate status code and JSON body.
// It handles different error types (string, error interface, or arbitrary objects).
func (g *GenericRouter) sendError(ctx *fasthttp.RequestCtx, errorConverter ErrorConverter, bifrostErr *schemas.BifrostError) {
	if bifrostErr.StatusCode != nil {
		ctx.SetStatusCode(*bifrostErr.StatusCode)
	} else {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
	}
	ctx.SetContentType("application/json")

	errorBody, err := json.Marshal(errorConverter(bifrostErr))
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetBodyString(fmt.Sprintf("failed to encode error response: %v", err))
		return
	}

	ctx.SetBody(errorBody)
}

// sendSuccess sends a successful response with HTTP 200 status and JSON body.
func (g *GenericRouter) sendSuccess(ctx *fasthttp.RequestCtx, errorConverter ErrorConverter, response interface{}) {
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")

	responseBody, err := json.Marshal(response)
	if err != nil {
		g.sendError(ctx, errorConverter, newBifrostError(err, "failed to encode response"))
		return
	}

	ctx.SetBody(responseBody)
}

// newBifrostError wraps a standard error into a BifrostError with IsBifrostError set to false.
// This helper function reduces code duplication when handling non-Bifrost errors.
func newBifrostError(err error, message string) *schemas.BifrostError {
	if err == nil {
		return &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: message,
			},
		}
	}

	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: message,
			Error:   err,
		},
	}
}

// extractAndParseFallbacks extracts fallbacks from the integration request and adds them to the BifrostRequest
func (g *GenericRouter) extractAndParseFallbacks(req interface{}, bifrostReq *schemas.BifrostRequest) error {
	// Check if the request has a fallbacks field ([]string)
	fallbacks, err := g.extractFallbacksFromRequest(req)
	if err != nil {
		return fmt.Errorf("failed to extract fallbacks: %w", err)
	}

	if len(fallbacks) == 0 {
		return nil // No fallbacks to process
	}

	// Parse fallbacks from strings to Fallback structs
	parsedFallbacks := make([]schemas.Fallback, 0, len(fallbacks))
	for _, fallbackStr := range fallbacks {
		if fallbackStr == "" {
			continue // Skip empty strings
		}

		// Use ParseModelString to extract provider and model
		provider, model := schemas.ParseModelString(fallbackStr, bifrostReq.Provider)

		parsedFallback := schemas.Fallback{
			Provider: provider,
			Model:    model,
		}
		parsedFallbacks = append(parsedFallbacks, parsedFallback)
	}

	if len(parsedFallbacks) == 0 {
		return nil // No valid fallbacks found
	}

	// Add fallbacks to the main BifrostRequest
	bifrostReq.Fallbacks = parsedFallbacks

	// Also add fallbacks to the specific request type if it exists
	switch bifrostReq.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		if bifrostReq.TextCompletionRequest != nil {
			bifrostReq.TextCompletionRequest.Fallbacks = parsedFallbacks
		}
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		if bifrostReq.ChatRequest != nil {
			bifrostReq.ChatRequest.Fallbacks = parsedFallbacks
		}
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		if bifrostReq.ResponsesRequest != nil {
			bifrostReq.ResponsesRequest.Fallbacks = parsedFallbacks
		}
	case schemas.EmbeddingRequest:
		if bifrostReq.EmbeddingRequest != nil {
			bifrostReq.EmbeddingRequest.Fallbacks = parsedFallbacks
		}
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		if bifrostReq.SpeechRequest != nil {
			bifrostReq.SpeechRequest.Fallbacks = parsedFallbacks
		}
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		if bifrostReq.TranscriptionRequest != nil {
			bifrostReq.TranscriptionRequest.Fallbacks = parsedFallbacks
		}
	}

	return nil
}

// extractFallbacksFromRequest uses reflection to extract fallbacks field from any request type
func (g *GenericRouter) extractFallbacksFromRequest(req interface{}) ([]string, error) {
	if req == nil {
		return nil, nil
	}

	// Try to use reflection to find a "fallbacks" field
	reqValue := reflect.ValueOf(req)
	if reqValue.Kind() == reflect.Ptr {
		reqValue = reqValue.Elem()
	}

	if reqValue.Kind() != reflect.Struct {
		return nil, nil // Not a struct, no fallbacks
	}

	// Look for the "fallbacks" field
	fallbacksField := reqValue.FieldByName("fallbacks")
	if !fallbacksField.IsValid() {
		return nil, nil // No fallbacks field found
	}

	// Handle different types of fallbacks field
	switch fallbacksField.Kind() {
	case reflect.Slice:
		if fallbacksField.Type().Elem().Kind() == reflect.String {
			// []string case
			fallbacks := make([]string, fallbacksField.Len())
			for i := 0; i < fallbacksField.Len(); i++ {
				fallbacks[i] = fallbacksField.Index(i).String()
			}
			return fallbacks, nil
		}
	case reflect.String:
		// Single string case - treat as one fallback
		return []string{fallbacksField.String()}, nil
	}

	return nil, nil
}
