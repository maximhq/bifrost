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
	"fmt"
	"log"
	"strconv"
	"strings"

	"bufio"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ExtensionRouter defines the interface that all integration routers must implement
// to register their routes with the main HTTP router.
type ExtensionRouter interface {
	RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware)
}

// StreamingRequest interface for requests that support streaming
type StreamingRequest interface {
	IsStreamingRequested() bool
}

// BatchRequest wraps a Bifrost batch request with its type information.
type BatchRequest struct {
	Type            schemas.RequestType
	CreateRequest   *schemas.BifrostBatchCreateRequest
	ListRequest     *schemas.BifrostBatchListRequest
	RetrieveRequest *schemas.BifrostBatchRetrieveRequest
	CancelRequest   *schemas.BifrostBatchCancelRequest
	ResultsRequest  *schemas.BifrostBatchResultsRequest
}

// FileRequest wraps a Bifrost file request with its type information.
type FileRequest struct {
	Type            schemas.RequestType
	UploadRequest   *schemas.BifrostFileUploadRequest
	ListRequest     *schemas.BifrostFileListRequest
	RetrieveRequest *schemas.BifrostFileRetrieveRequest
	DeleteRequest   *schemas.BifrostFileDeleteRequest
	ContentRequest  *schemas.BifrostFileContentRequest
}

// ContainerRequest wraps a Bifrost container request with its type information.
type ContainerRequest struct {
	Type            schemas.RequestType
	CreateRequest   *schemas.BifrostContainerCreateRequest
	ListRequest     *schemas.BifrostContainerListRequest
	RetrieveRequest *schemas.BifrostContainerRetrieveRequest
	DeleteRequest   *schemas.BifrostContainerDeleteRequest
}

// ContainerFileRequest is a wrapper for Bifrost container file requests.
type ContainerFileRequest struct {
	Type            schemas.RequestType
	CreateRequest   *schemas.BifrostContainerFileCreateRequest
	ListRequest     *schemas.BifrostContainerFileListRequest
	RetrieveRequest *schemas.BifrostContainerFileRetrieveRequest
	ContentRequest  *schemas.BifrostContainerFileContentRequest
	DeleteRequest   *schemas.BifrostContainerFileDeleteRequest
}

// BatchRequestConverter is a function that converts integration-specific batch requests to Bifrost format.
type BatchRequestConverter func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error)

// FileRequestConverter is a function that converts integration-specific file requests to Bifrost format.
type FileRequestConverter func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error)

// ContainerRequestConverter is a function that converts integration-specific container requests to Bifrost format.
type ContainerRequestConverter func(ctx *schemas.BifrostContext, req interface{}) (*ContainerRequest, error)

// ContainerFileRequestConverter is a function that converts integration-specific container file requests to Bifrost format.
type ContainerFileRequestConverter func(ctx *schemas.BifrostContext, req interface{}) (*ContainerFileRequest, error)

// RequestConverter is a function that converts integration-specific requests to Bifrost format.
// It takes the parsed request object and returns a BifrostRequest ready for processing.
type RequestConverter func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error)

// ListModelsResponseConverter is a function that converts BifrostListModelsResponse to integration-specific format.
// It takes a BifrostListModelsResponse and returns the format expected by the specific integration.
type ListModelsResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostListModelsResponse) (interface{}, error)

// TextResponseConverter is a function that converts BifrostTextCompletionResponse to integration-specific format.
// It takes a BifrostTextCompletionResponse and returns the format expected by the specific integration.
type TextResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (interface{}, error)

// ChatResponseConverter is a function that converts BifrostChatResponse to integration-specific format.
// It takes a BifrostChatResponse and returns the format expected by the specific integration.
type ChatResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (interface{}, error)

// ResponsesResponseConverter is a function that converts BifrostResponsesResponse to integration-specific format.
// It takes a BifrostResponsesResponse and returns the format expected by the specific integration.
type ResponsesResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error)

// EmbeddingResponseConverter is a function that converts BifrostEmbeddingResponse to integration-specific format.
// It takes a BifrostEmbeddingResponse and returns the format expected by the specific integration.
type EmbeddingResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostEmbeddingResponse) (interface{}, error)

// SpeechResponseConverter is a function that converts BifrostSpeechResponse to integration-specific format.
// It takes a BifrostSpeechResponse and returns the format expected by the specific integration.
type SpeechResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostSpeechResponse) (interface{}, error)

// TranscriptionResponseConverter is a function that converts BifrostTranscriptionResponse to integration-specific format.
// It takes a BifrostTranscriptionResponse and returns the format expected by the specific integration.
type TranscriptionResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostTranscriptionResponse) (interface{}, error)

// BatchCreateResponseConverter is a function that converts BifrostBatchCreateResponse to integration-specific format.
// It takes a BifrostBatchCreateResponse and returns the format expected by the specific integration.
type BatchCreateResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCreateResponse) (interface{}, error)

// BatchListResponseConverter is a function that converts BifrostBatchListResponse to integration-specific format.
// It takes a BifrostBatchListResponse and returns the format expected by the specific integration.
type BatchListResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchListResponse) (interface{}, error)

// BatchRetrieveResponseConverter is a function that converts BifrostBatchRetrieveResponse to integration-specific format.
// It takes a BifrostBatchRetrieveResponse and returns the format expected by the specific integration.
type BatchRetrieveResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchRetrieveResponse) (interface{}, error)

// BatchCancelResponseConverter is a function that converts BifrostBatchCancelResponse to integration-specific format.
// It takes a BifrostBatchCancelResponse and returns the format expected by the specific integration.
type BatchCancelResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCancelResponse) (interface{}, error)

// BatchResultsResponseConverter is a function that converts BifrostBatchResultsResponse to integration-specific format.
// It takes a BifrostBatchResultsResponse and returns the format expected by the specific integration.
type BatchResultsResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchResultsResponse) (interface{}, error)

// FileUploadResponseConverter is a function that converts BifrostFileUploadResponse to integration-specific format.
// It takes a BifrostFileUploadResponse and returns the format expected by the specific integration.
type FileUploadResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileUploadResponse) (interface{}, error)

// FileListResponseConverter is a function that converts BifrostFileListResponse to integration-specific format.
// It takes a BifrostFileListResponse and returns the format expected by the specific integration.
type FileListResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileListResponse) (interface{}, error)

// FileRetrieveResponseConverter is a function that converts BifrostFileRetrieveResponse to integration-specific format.
// It takes a BifrostFileRetrieveResponse and returns the format expected by the specific integration.
type FileRetrieveResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileRetrieveResponse) (interface{}, error)

// FileDeleteResponseConverter is a function that converts BifrostFileDeleteResponse to integration-specific format.
// It takes a BifrostFileDeleteResponse and returns the format expected by the specific integration.
type FileDeleteResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileDeleteResponse) (interface{}, error)

// FileContentResponseConverter is a function that converts BifrostFileContentResponse to integration-specific format.
// It takes a BifrostFileContentResponse and returns the format expected by the specific integration.
// Note: This may return binary data or a wrapper object depending on the integration.
type FileContentResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileContentResponse) (interface{}, error)

// ContainerCreateResponseConverter is a function that converts BifrostContainerCreateResponse to integration-specific format.
// It takes a BifrostContainerCreateResponse and returns the format expected by the specific integration.
type ContainerCreateResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerCreateResponse) (interface{}, error)

// ContainerListResponseConverter is a function that converts BifrostContainerListResponse to integration-specific format.
// It takes a BifrostContainerListResponse and returns the format expected by the specific integration.
type ContainerListResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerListResponse) (interface{}, error)

// ContainerRetrieveResponseConverter is a function that converts BifrostContainerRetrieveResponse to integration-specific format.
// It takes a BifrostContainerRetrieveResponse and returns the format expected by the specific integration.
type ContainerRetrieveResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerRetrieveResponse) (interface{}, error)

// ContainerDeleteResponseConverter is a function that converts BifrostContainerDeleteResponse to integration-specific format.
// It takes a BifrostContainerDeleteResponse and returns the format expected by the specific integration.
type ContainerDeleteResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerDeleteResponse) (interface{}, error)

// ContainerFileCreateResponseConverter is a function that converts BifrostContainerFileCreateResponse to integration-specific format.
// It takes a BifrostContainerFileCreateResponse and returns the format expected by the specific integration.
type ContainerFileCreateResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileCreateResponse) (interface{}, error)

// ContainerFileListResponseConverter is a function that converts BifrostContainerFileListResponse to integration-specific format.
// It takes a BifrostContainerFileListResponse and returns the format expected by the specific integration.
type ContainerFileListResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileListResponse) (interface{}, error)

// ContainerFileRetrieveResponseConverter is a function that converts BifrostContainerFileRetrieveResponse to integration-specific format.
// It takes a BifrostContainerFileRetrieveResponse and returns the format expected by the specific integration.
type ContainerFileRetrieveResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileRetrieveResponse) (interface{}, error)

// ContainerFileContentResponseConverter is a function that converts BifrostContainerFileContentResponse to integration-specific format.
// It takes a BifrostContainerFileContentResponse and returns the format expected by the specific integration.
type ContainerFileContentResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileContentResponse) (interface{}, error)

// ContainerFileDeleteResponseConverter is a function that converts BifrostContainerFileDeleteResponse to integration-specific format.
// It takes a BifrostContainerFileDeleteResponse and returns the format expected by the specific integration.
type ContainerFileDeleteResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileDeleteResponse) (interface{}, error)

// CountTokensResponseConverter is a function that converts BifrostCountTokensResponse to integration-specific format.
// It takes a BifrostCountTokensResponse and returns the format expected by the specific integration.
type CountTokensResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostCountTokensResponse) (interface{}, error)

// TextStreamResponseConverter is a function that converts BifrostTextCompletionResponse to integration-specific streaming format.
// It takes a BifrostTextCompletionResponse and returns the event type and the streaming format expected by the specific integration.
type TextStreamResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (string, interface{}, error)

// ChatStreamResponseConverter is a function that converts BifrostChatResponse to integration-specific streaming format.
// It takes a BifrostChatResponse and returns the event type and the streaming format expected by the specific integration.
type ChatStreamResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (string, interface{}, error)

// ResponsesStreamResponseConverter is a function that converts BifrostResponsesStreamResponse to integration-specific streaming format.
// It takes a BifrostResponsesStreamResponse and returns a single event type and payload, which can itself encode one or more SSE events if needed by the integration.
type ResponsesStreamResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error)

// SpeechStreamResponseConverter is a function that converts BifrostSpeechStreamResponse to integration-specific streaming format.
// It takes a BifrostSpeechStreamResponse and returns the event type and the streaming format expected by the specific integration.
type SpeechStreamResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostSpeechStreamResponse) (string, interface{}, error)

// TranscriptionStreamResponseConverter is a function that converts BifrostTranscriptionStreamResponse to integration-specific streaming format.
// It takes a BifrostTranscriptionStreamResponse and returns the event type and the streaming format expected by the specific integration.
type TranscriptionStreamResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostTranscriptionStreamResponse) (string, interface{}, error)

// ImageGenerationResponseConverter is a function that converts BifrostImageGenerationResponse to integration-specific format.
// It takes a BifrostImageGenerationResponse and returns the format expected by the specific integration.
type ImageGenerationResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationResponse) (interface{}, error)

// ImageGenerationStreamResponseConverter is a function that converts BifrostImageGenerationStreamResponse to integration-specific streaming format.
// It takes a BifrostImageGenerationStreamResponse and returns the event type and the streaming format expected by the specific integration.
type ImageGenerationStreamResponseConverter func(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationStreamResponse) (string, interface{}, error)

// ErrorConverter is a function that converts BifrostError to integration-specific format.
// It takes a BifrostError and returns the format expected by the specific integration.
type ErrorConverter func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{}

// StreamErrorConverter is a function that converts BifrostError to integration-specific streaming error format.
// It takes a BifrostError and returns the streaming error format expected by the specific integration.
type StreamErrorConverter func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{}

// RequestParser is a function that handles custom request body parsing.
// It replaces the default JSON parsing when configured (e.g., for multipart/form-data).
// The parser should populate the provided request object from the fasthttp context.
// If it returns an error, the request processing stops.
type RequestParser func(ctx *fasthttp.RequestCtx, req interface{}) error

// PreRequestCallback is called after parsing the request but before processing through Bifrost.
// It can be used to modify the request object (e.g., extract model from URL parameters)
// or perform validation. If it returns an error, the request processing stops.
// It can also modify the bifrost context based on the request context before it is given to Bifrost.
type PreRequestCallback func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error

// PostRequestCallback is called after processing the request but before sending the response.
// It can be used to modify the response or perform additional logging/metrics.
// If it returns an error, an error response is sent instead of the success response.
type PostRequestCallback func(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error

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
	TextStreamResponseConverter            TextStreamResponseConverter            // Function to convert BifrostTextCompletionResponse to streaming format
	ChatStreamResponseConverter            ChatStreamResponseConverter            // Function to convert BifrostChatResponse to streaming format
	ResponsesStreamResponseConverter       ResponsesStreamResponseConverter       // Function to convert BifrostResponsesResponse to streaming format
	SpeechStreamResponseConverter          SpeechStreamResponseConverter          // Function to convert BifrostSpeechResponse to streaming format
	TranscriptionStreamResponseConverter   TranscriptionStreamResponseConverter   // Function to convert BifrostTranscriptionResponse to streaming format
	ImageGenerationStreamResponseConverter ImageGenerationStreamResponseConverter // Function to convert BifrostImageGenerationStreamResponse to streaming format
	ErrorConverter                         StreamErrorConverter                   // Function to convert BifrostError to streaming error format
}

type RouteConfigType string

const (
	RouteConfigTypeOpenAI    RouteConfigType = "openai"
	RouteConfigTypeAnthropic RouteConfigType = "anthropic"
	RouteConfigTypeGenAI     RouteConfigType = "genai"
	RouteConfigTypeBedrock   RouteConfigType = "bedrock"
)

// RouteConfig defines the configuration for a single route in an integration.
// It specifies the path, method, and handlers for request/response conversion.
type RouteConfig struct {
	Type                                   RouteConfigType                        // Type of the route
	Path                                   string                                 // HTTP path pattern (e.g., "/openai/v1/chat/completions")
	Method                                 string                                 // HTTP method (POST, GET, PUT, DELETE)
	GetRequestTypeInstance                 func() interface{}                     // Factory function to create request instance (SHOULD NOT BE NIL)
	RequestParser                          RequestParser                          // Optional: custom request parsing (e.g., multipart/form-data)
	RequestConverter                       RequestConverter                       // Function to convert request to BifrostRequest (for inference requests)
	BatchRequestConverter                  BatchRequestConverter                  // Function to convert request to BatchRequest (for batch operations)
	FileRequestConverter                   FileRequestConverter                   // Function to convert request to FileRequest (for file operations)
	ContainerRequestConverter              ContainerRequestConverter              // Function to convert request to ContainerRequest (for container operations)
	ContainerFileRequestConverter          ContainerFileRequestConverter          // Function to convert request to ContainerFileRequest (for container file operations)
	ListModelsResponseConverter            ListModelsResponseConverter            // Function to convert BifrostListModelsResponse to integration format (SHOULD NOT BE NIL)
	TextResponseConverter                  TextResponseConverter                  // Function to convert BifrostTextCompletionResponse to integration format (SHOULD NOT BE NIL)
	ChatResponseConverter                  ChatResponseConverter                  // Function to convert BifrostChatResponse to integration format (SHOULD NOT BE NIL)
	ResponsesResponseConverter             ResponsesResponseConverter             // Function to convert BifrostResponsesResponse to integration format (SHOULD NOT BE NIL)
	EmbeddingResponseConverter             EmbeddingResponseConverter             // Function to convert BifrostEmbeddingResponse to integration format (SHOULD NOT BE NIL)
	SpeechResponseConverter                SpeechResponseConverter                // Function to convert BifrostSpeechResponse to integration format (SHOULD NOT BE NIL)
	TranscriptionResponseConverter         TranscriptionResponseConverter         // Function to convert BifrostTranscriptionResponse to integration format (SHOULD NOT BE NIL)
	ImageGenerationResponseConverter       ImageGenerationResponseConverter       // Function to convert BifrostImageGenerationResponse to integration format (SHOULD NOT BE NIL)
	BatchCreateResponseConverter           BatchCreateResponseConverter           // Function to convert BifrostBatchCreateResponse to integration format
	BatchListResponseConverter             BatchListResponseConverter             // Function to convert BifrostBatchListResponse to integration format
	BatchRetrieveResponseConverter         BatchRetrieveResponseConverter         // Function to convert BifrostBatchRetrieveResponse to integration format
	BatchCancelResponseConverter           BatchCancelResponseConverter           // Function to convert BifrostBatchCancelResponse to integration format
	BatchResultsResponseConverter          BatchResultsResponseConverter          // Function to convert BifrostBatchResultsResponse to integration format
	FileUploadResponseConverter            FileUploadResponseConverter            // Function to convert BifrostFileUploadResponse to integration format
	FileListResponseConverter              FileListResponseConverter              // Function to convert BifrostFileListResponse to integration format
	FileRetrieveResponseConverter          FileRetrieveResponseConverter          // Function to convert BifrostFileRetrieveResponse to integration format
	FileDeleteResponseConverter            FileDeleteResponseConverter            // Function to convert BifrostFileDeleteResponse to integration format
	FileContentResponseConverter           FileContentResponseConverter           // Function to convert BifrostFileContentResponse to integration format
	ContainerCreateResponseConverter       ContainerCreateResponseConverter       // Function to convert BifrostContainerCreateResponse to integration format
	ContainerListResponseConverter         ContainerListResponseConverter         // Function to convert BifrostContainerListResponse to integration format
	ContainerRetrieveResponseConverter     ContainerRetrieveResponseConverter     // Function to convert BifrostContainerRetrieveResponse to integration format
	ContainerDeleteResponseConverter       ContainerDeleteResponseConverter       // Function to convert BifrostContainerDeleteResponse to integration format
	ContainerFileCreateResponseConverter   ContainerFileCreateResponseConverter   // Function to convert BifrostContainerFileCreateResponse to integration format
	ContainerFileListResponseConverter     ContainerFileListResponseConverter     // Function to convert BifrostContainerFileListResponse to integration format
	ContainerFileRetrieveResponseConverter ContainerFileRetrieveResponseConverter // Function to convert BifrostContainerFileRetrieveResponse to integration format
	ContainerFileContentResponseConverter  ContainerFileContentResponseConverter  // Function to convert BifrostContainerFileContentResponse to integration format
	ContainerFileDeleteResponseConverter   ContainerFileDeleteResponseConverter   // Function to convert BifrostContainerFileDeleteResponse to integration format
	CountTokensResponseConverter           CountTokensResponseConverter           // Function to convert BifrostCountTokensResponse to integration format
	ErrorConverter                         ErrorConverter                         // Function to convert BifrostError to integration format (SHOULD NOT BE NIL)
	StreamConfig                           *StreamConfig                          // Optional: Streaming configuration (if nil, streaming not supported)
	PreCallback                            PreRequestCallback                     // Optional: called after parsing but before Bifrost processing
	PostCallback                           PostRequestCallback                    // Optional: called after request processing
}

// GenericRouter provides a reusable router implementation for all integrations.
// It handles the common flow of: parse request → convert to Bifrost → execute → convert response.
// Integration-specific logic is handled through the RouteConfig callbacks and converters.
type GenericRouter struct {
	client       *bifrost.Bifrost // Bifrost client for executing requests
	handlerStore lib.HandlerStore // Config provider for the router
	routes       []RouteConfig    // List of route configurations
	logger       schemas.Logger   // Logger for the router
}

// NewGenericRouter creates a new generic router with the given bifrost client and route configurations.
// Each integration should create their own routes and pass them to this constructor.
func NewGenericRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, routes []RouteConfig, logger schemas.Logger) *GenericRouter {
	return &GenericRouter{
		client:       client,
		handlerStore: handlerStore,
		routes:       routes,
		logger:       logger,
	}
}

// RegisterRoutes registers all configured routes on the given fasthttp router.
// This method implements the ExtensionRouter interface.
func (g *GenericRouter) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	for _, route := range g.routes {
		// Validate route configuration at startup to fail fast
		method := strings.ToUpper(route.Method)

		if route.GetRequestTypeInstance == nil {
			g.logger.Warn("route configuration is invalid: GetRequestTypeInstance cannot be nil for route " + route.Path)
			continue
		}

		// Test that GetRequestTypeInstance returns a valid instance
		if testInstance := route.GetRequestTypeInstance(); testInstance == nil {
			g.logger.Warn("route configuration is invalid: GetRequestTypeInstance returned nil for route " + route.Path)
			continue
		}

		// Determine route type: inference, batch, file, container, or container file
		isBatchRoute := route.BatchRequestConverter != nil
		isFileRoute := route.FileRequestConverter != nil
		isContainerRoute := route.ContainerRequestConverter != nil
		isContainerFileRoute := route.ContainerFileRequestConverter != nil
		isInferenceRoute := !isBatchRoute && !isFileRoute && !isContainerRoute && !isContainerFileRoute

		// For inference routes, require RequestConverter
		if isInferenceRoute && route.RequestConverter == nil {
			g.logger.Warn("route configuration is invalid: RequestConverter cannot be nil for inference route " + route.Path)
			continue
		}

		if route.ErrorConverter == nil {
			g.logger.Warn("route configuration is invalid: ErrorConverter cannot be nil for route " + route.Path)
			continue
		}

		handler := g.createHandler(route)
		switch method {
		case fasthttp.MethodPost:
			r.POST(route.Path, lib.ChainMiddlewares(handler, middlewares...))
		case fasthttp.MethodGet:
			r.GET(route.Path, lib.ChainMiddlewares(handler, middlewares...))
		case fasthttp.MethodPut:
			r.PUT(route.Path, lib.ChainMiddlewares(handler, middlewares...))
		case fasthttp.MethodDelete:
			r.DELETE(route.Path, lib.ChainMiddlewares(handler, middlewares...))
		case fasthttp.MethodHead:
			r.HEAD(route.Path, lib.ChainMiddlewares(handler, middlewares...))
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
		method := string(ctx.Method())

		// Parse request body into the integration-specific request type
		// Note: config validation is performed at startup in RegisterRoutes
		req := config.GetRequestTypeInstance()
		var rawBody []byte

		// Execute the request through Bifrost
		bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, g.handlerStore.ShouldAllowDirectKeys(), g.handlerStore.GetHeaderFilterConfig())

		// Set integration type to context
		bifrostCtx.SetValue(schemas.BifrostContextKeyIntegrationType, string(config.Type))

		// Set available providers to context
		availableProviders := g.handlerStore.GetAvailableProviders()
		bifrostCtx.SetValue(schemas.BifrostContextKeyAvailableProviders, availableProviders)

		// Parse request body based on configuration
		if method != fasthttp.MethodGet && method != fasthttp.MethodHead {
			if config.RequestParser != nil {
				// Use custom parser (e.g., for multipart/form-data)
				if err := config.RequestParser(ctx, req); err != nil {
					g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to parse request"))
					return
				}
			} else {
				// Use default JSON parsing
				rawBody = ctx.Request.Body()
				if len(rawBody) > 0 {
					if err := sonic.Unmarshal(rawBody, req); err != nil {
						g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "Invalid JSON"))
						return
					}
				}
			}
		}

		// Execute pre-request callback if configured
		// This is typically used for extracting data from URL parameters
		// or performing request validation after parsing
		if config.PreCallback != nil {
			if err := config.PreCallback(ctx, bifrostCtx, req); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute pre-request callback: "+err.Error()))
				return
			}
		}

		// Set direct key from context if available
		if ctx.UserValue(string(schemas.BifrostContextKeyDirectKey)) != nil {
			key, ok := ctx.UserValue(string(schemas.BifrostContextKeyDirectKey)).(schemas.Key)
			if ok {
				bifrostCtx.SetValue(schemas.BifrostContextKeyDirectKey, key)
			}
		}

		// Handle batch requests if BatchRequestConverter is set
		if config.BatchRequestConverter != nil {
			defer cancel()
			batchReq, err := config.BatchRequestConverter(bifrostCtx, req)
			if err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert batch request"))
				return
			}
			if batchReq == nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid batch request"))
				return
			}
			g.handleBatchRequest(ctx, config, req, batchReq, bifrostCtx)
			return
		}

		// Handle file requests if FileRequestConverter is set
		if config.FileRequestConverter != nil {
			defer cancel()
			fileReq, err := config.FileRequestConverter(bifrostCtx, req)
			if err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert file request"))
				return
			}
			if fileReq == nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid file request"))
				return
			}
			g.handleFileRequest(ctx, config, req, fileReq, bifrostCtx)
			return
		}

		// Handle container requests if ContainerRequestConverter is set
		if config.ContainerRequestConverter != nil {
			defer cancel()
			containerReq, err := config.ContainerRequestConverter(bifrostCtx, req)
			if err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert container request"))
				return
			}
			if containerReq == nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container request"))
				return
			}
			g.handleContainerRequest(ctx, config, req, containerReq, bifrostCtx)
			return
		}

		// Handle container file requests if ContainerFileRequestConverter is set
		if config.ContainerFileRequestConverter != nil {
			defer cancel()
			containerFileReq, err := config.ContainerFileRequestConverter(bifrostCtx, req)
			if err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert container file request"))
				return
			}
			if containerFileReq == nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container file request"))
				return
			}
			g.handleContainerFileRequest(ctx, config, req, containerFileReq, bifrostCtx)
			return
		}

		// Convert the integration-specific request to Bifrost format (inference requests)
		bifrostReq, err := config.RequestConverter(bifrostCtx, req)
		if err != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert request to Bifrost format"))
			return
		}
		if bifrostReq == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid request"))
			return
		}
		if sendRawRequestBody, ok := (*bifrostCtx).Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && sendRawRequestBody {
			bifrostReq.SetRawRequestBody(rawBody)
		}

		// Extract and parse fallbacks from the request if present
		if err := g.extractAndParseFallbacks(req, bifrostReq); err != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to parse fallbacks: "+err.Error()))
			return
		}

		// Check if streaming is requested
		isStreaming := false
		if streamingReq, ok := req.(StreamingRequest); ok {
			isStreaming = streamingReq.IsStreamingRequested()
		}

		if isStreaming {
			g.handleStreamingRequest(ctx, config, bifrostReq, bifrostCtx, cancel)
		} else {
			defer cancel() // Ensure cleanup on function exit
			g.handleNonStreamingRequest(ctx, config, req, bifrostReq, bifrostCtx)
		}
	}
}

// handleNonStreamingRequest handles regular (non-streaming) requests
func (g *GenericRouter) handleNonStreamingRequest(ctx *fasthttp.RequestCtx, config RouteConfig, req interface{}, bifrostReq *schemas.BifrostRequest, bifrostCtx *schemas.BifrostContext) {
	// Use the cancellable context from ConvertToBifrostContext
	// While we can't detect client disconnects until we try to write, having a cancellable context
	// allows providers that check ctx.Done() to cancel early if needed. This is less critical than
	// streaming requests (where we actively detect write errors), but still provides a mechanism
	// for providers to respect cancellation.
	var response interface{}

	var err error

	switch {
	case bifrostReq.ListModelsRequest != nil:
		// Get provider from header - if not set or "all", list from all providers
		// Otherwise, list models from the specified provider
		listModelsProvider := strings.ToLower(string(ctx.Request.Header.Peek("x-bf-list-models-provider")))

		var listModelsResponse *schemas.BifrostListModelsResponse
		var bifrostErr *schemas.BifrostError

		if listModelsProvider == "" || listModelsProvider == "all" {
			// No specific provider requested - list from all providers
			listModelsResponse, bifrostErr = g.client.ListAllModels(bifrostCtx, bifrostReq.ListModelsRequest)
		} else {
			// Specific provider requested - override the provider in the request
			bifrostReq.ListModelsRequest.Provider = schemas.ModelProvider(listModelsProvider)
			listModelsResponse, bifrostErr = g.client.ListModelsRequest(bifrostCtx, bifrostReq.ListModelsRequest)
		}

		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}

		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, listModelsResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if listModelsResponse == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		response, err = config.ListModelsResponseConverter(bifrostCtx, listModelsResponse)
	case bifrostReq.TextCompletionRequest != nil:
		textCompletionResponse, bifrostErr := g.client.TextCompletionRequest(bifrostCtx, bifrostReq.TextCompletionRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}

		// Execute post-request callback if configured
		// This is typically used for response modification or additional processing
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, textCompletionResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if textCompletionResponse == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		// Convert Bifrost response to integration-specific format and send
		response, err = config.TextResponseConverter(bifrostCtx, textCompletionResponse)
	case bifrostReq.ChatRequest != nil:
		chatResponse, bifrostErr := g.client.ChatCompletionRequest(bifrostCtx, bifrostReq.ChatRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}

		// Execute post-request callback if configured
		// This is typically used for response modification or additional processing
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, chatResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if chatResponse == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		// Convert Bifrost response to integration-specific format and send
		response, err = config.ChatResponseConverter(bifrostCtx, chatResponse)
	case bifrostReq.ResponsesRequest != nil:
		responsesResponse, bifrostErr := g.client.ResponsesRequest(bifrostCtx, bifrostReq.ResponsesRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}

		// Execute post-request callback if configured
		// This is typically used for response modification or additional processing
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, responsesResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if responsesResponse == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		// Convert Bifrost response to integration-specific format and send
		response, err = config.ResponsesResponseConverter(bifrostCtx, responsesResponse)
	case bifrostReq.EmbeddingRequest != nil:
		embeddingResponse, bifrostErr := g.client.EmbeddingRequest(bifrostCtx, bifrostReq.EmbeddingRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}

		// Execute post-request callback if configured
		// This is typically used for response modification or additional processing
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, embeddingResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if embeddingResponse == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		// Convert Bifrost response to integration-specific format and send
		response, err = config.EmbeddingResponseConverter(bifrostCtx, embeddingResponse)
	case bifrostReq.SpeechRequest != nil:
		speechResponse, bifrostErr := g.client.SpeechRequest(bifrostCtx, bifrostReq.SpeechRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}

		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, speechResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if speechResponse == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		if config.SpeechResponseConverter != nil {
			response, err = config.SpeechResponseConverter(bifrostCtx, speechResponse)
			if err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert speech response"))
				return
			}
			g.sendSuccess(ctx, bifrostCtx, config.ErrorConverter, response)
			return
		} else {
			ctx.Response.Header.Set("Content-Type", "audio/mpeg")
			ctx.Response.Header.Set("Content-Disposition", "attachment; filename=speech.mp3")
			ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(speechResponse.Audio)))
			ctx.Response.SetBody(speechResponse.Audio)
			return
		}
	case bifrostReq.TranscriptionRequest != nil:
		transcriptionResponse, bifrostErr := g.client.TranscriptionRequest(bifrostCtx, bifrostReq.TranscriptionRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}

		// Execute post-request callback if configured
		// This is typically used for response modification or additional processing
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, transcriptionResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if transcriptionResponse == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		// Convert Bifrost response to integration-specific format and send
		response, err = config.TranscriptionResponseConverter(bifrostCtx, transcriptionResponse)
	case bifrostReq.ImageGenerationRequest != nil:
		imageGenerationResponse, bifrostErr := g.client.ImageGenerationRequest(bifrostCtx, bifrostReq.ImageGenerationRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}

		// Execute post-request callback if configured
		// This is typically used for response modification or additional processing
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, imageGenerationResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if imageGenerationResponse == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		if config.ImageGenerationResponseConverter == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "missing ImageGenerationResponseConverter for integration"))
			return
		}

		// Convert Bifrost response to integration-specific format and send
		response, err = config.ImageGenerationResponseConverter(bifrostCtx, imageGenerationResponse)
	case bifrostReq.CountTokensRequest != nil:
		countTokensResponse, bifrostErr := g.client.CountTokensRequest(bifrostCtx, bifrostReq.CountTokensRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}

		// Execute post-request callback if configured
		// This is typically used for response modification or additional processing
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, countTokensResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}

		if countTokensResponse == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Bifrost response is nil after post-request callback"))
			return
		}

		// Convert Bifrost response to integration-specific format and send
		if config.CountTokensResponseConverter == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "CountTokensResponseConverter not configured"))
			return
		}
		response, err = config.CountTokensResponseConverter(bifrostCtx, countTokensResponse)
	default:
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid request type"))
		return
	}

	if err != nil {
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to encode response"))
		return
	}

	g.sendSuccess(ctx, bifrostCtx, config.ErrorConverter, response)
}

// handleBatchRequest handles batch API requests (create, list, retrieve, cancel, results)
func (g *GenericRouter) handleBatchRequest(ctx *fasthttp.RequestCtx, config RouteConfig, req interface{}, batchReq *BatchRequest, bifrostCtx *schemas.BifrostContext) {
	var response interface{}
	var err error

	switch batchReq.Type {
	case schemas.BatchCreateRequest:
		if batchReq.CreateRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid batch create request"))
			return
		}
		batchResponse, bifrostErr := g.client.BatchCreateRequest(bifrostCtx, batchReq.CreateRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, batchResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.BatchCreateResponseConverter != nil {
			response, err = config.BatchCreateResponseConverter(bifrostCtx, batchResponse)
		} else {
			response = batchResponse
		}

	case schemas.BatchListRequest:
		if batchReq.ListRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid batch list request"))
			return
		}
		batchResponse, bifrostErr := g.client.BatchListRequest(bifrostCtx, batchReq.ListRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, batchResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.BatchListResponseConverter != nil {
			response, err = config.BatchListResponseConverter(bifrostCtx, batchResponse)
		} else {
			response = batchResponse
		}

	case schemas.BatchRetrieveRequest:
		if batchReq.RetrieveRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid batch retrieve request"))
			return
		}
		batchResponse, bifrostErr := g.client.BatchRetrieveRequest(bifrostCtx, batchReq.RetrieveRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, batchResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.BatchRetrieveResponseConverter != nil {
			response, err = config.BatchRetrieveResponseConverter(bifrostCtx, batchResponse)
		} else {
			response = batchResponse
		}

	case schemas.BatchCancelRequest:
		if batchReq.CancelRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid batch cancel request"))
			return
		}
		batchResponse, bifrostErr := g.client.BatchCancelRequest(bifrostCtx, batchReq.CancelRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, batchResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.BatchCancelResponseConverter != nil {
			response, err = config.BatchCancelResponseConverter(bifrostCtx, batchResponse)
		} else {
			response = batchResponse
		}

	case schemas.BatchResultsRequest:
		if batchReq.ResultsRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid batch results request"))
			return
		}
		batchResponse, bifrostErr := g.client.BatchResultsRequest(bifrostCtx, batchReq.ResultsRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, batchResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.BatchResultsResponseConverter != nil {
			response, err = config.BatchResultsResponseConverter(bifrostCtx, batchResponse)
		} else {
			response = batchResponse
		}

	default:
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Unknown batch request type"))
		return
	}

	if err != nil {
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert batch response"))
		return
	}

	g.sendSuccess(ctx, bifrostCtx, config.ErrorConverter, response)
}

// handleFileRequest handles file API requests (upload, list, retrieve, delete, content)
func (g *GenericRouter) handleFileRequest(ctx *fasthttp.RequestCtx, config RouteConfig, req interface{}, fileReq *FileRequest, bifrostCtx *schemas.BifrostContext) {

	var response interface{}
	var err error

	switch fileReq.Type {
	case schemas.FileUploadRequest:
		if fileReq.UploadRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid file upload request"))
			return
		}
		fileResponse, bifrostErr := g.client.FileUploadRequest(bifrostCtx, fileReq.UploadRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, fileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.FileUploadResponseConverter != nil {
			response, err = config.FileUploadResponseConverter(bifrostCtx, fileResponse)
		} else {
			response = fileResponse
		}

	case schemas.FileListRequest:
		if fileReq.ListRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid file list request"))
			return
		}
		fileResponse, bifrostErr := g.client.FileListRequest(bifrostCtx, fileReq.ListRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, fileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.FileListResponseConverter != nil {
			response, err = config.FileListResponseConverter(bifrostCtx, fileResponse)
			if err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert file list response"))
				return
			}
			// Handle raw byte responses (e.g., XML for S3 APIs)
			if rawBytes, ok := response.([]byte); ok {
				ctx.SetBody(rawBytes)
				return
			}
		} else {
			response = fileResponse
		}

	case schemas.FileRetrieveRequest:
		if fileReq.RetrieveRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid file retrieve request"))
			return
		}
		fileResponse, bifrostErr := g.client.FileRetrieveRequest(bifrostCtx, fileReq.RetrieveRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, fileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.FileRetrieveResponseConverter != nil {
			response, err = config.FileRetrieveResponseConverter(bifrostCtx, fileResponse)
		} else {
			response = fileResponse
		}

	case schemas.FileDeleteRequest:
		if fileReq.DeleteRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid file delete request"))
			return
		}
		fileResponse, bifrostErr := g.client.FileDeleteRequest(bifrostCtx, fileReq.DeleteRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, fileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.FileDeleteResponseConverter != nil {
			response, err = config.FileDeleteResponseConverter(bifrostCtx, fileResponse)
		} else {
			response = fileResponse
		}

	case schemas.FileContentRequest:
		if fileReq.ContentRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid file content request"))
			return
		}
		fileResponse, bifrostErr := g.client.FileContentRequest(bifrostCtx, fileReq.ContentRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, fileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		// For file content, handle binary response specially if no converter is set
		if config.FileContentResponseConverter != nil {
			response, err = config.FileContentResponseConverter(bifrostCtx, fileResponse)
			if err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert file content response"))
				return
			}
			// Check if response is raw bytes - write directly without JSON encoding
			if rawBytes, ok := response.([]byte); ok {
				ctx.Response.Header.Set("Content-Type", fileResponse.ContentType)
				ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(rawBytes)))
				ctx.Response.SetBody(rawBytes)
			} else {
				g.sendSuccess(ctx, bifrostCtx, config.ErrorConverter, response)
			}
		} else {
			// Return raw file content
			ctx.Response.Header.Set("Content-Type", fileResponse.ContentType)
			ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(fileResponse.Content)))
			ctx.Response.SetBody(fileResponse.Content)
		}
		return

	default:
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Unknown file request type"))
		return
	}

	if err != nil {
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert file response"))
		return
	}

	// If response is nil, PostCallback has set headers/status - return without body
	if response == nil {
		return
	}

	g.sendSuccess(ctx, bifrostCtx, config.ErrorConverter, response)
}

// handleContainerRequest handles container API requests (create, list, retrieve, delete)
func (g *GenericRouter) handleContainerRequest(ctx *fasthttp.RequestCtx, config RouteConfig, req interface{}, containerReq *ContainerRequest, bifrostCtx *schemas.BifrostContext) {
	var response interface{}
	var err error

	switch containerReq.Type {
	case schemas.ContainerCreateRequest:
		if containerReq.CreateRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container create request"))
			return
		}
		containerResponse, bifrostErr := g.client.ContainerCreateRequest(bifrostCtx, containerReq.CreateRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, containerResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.ContainerCreateResponseConverter != nil {
			response, err = config.ContainerCreateResponseConverter(bifrostCtx, containerResponse)
		} else {
			response = containerResponse
		}

	case schemas.ContainerListRequest:
		if containerReq.ListRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container list request"))
			return
		}
		containerResponse, bifrostErr := g.client.ContainerListRequest(bifrostCtx, containerReq.ListRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, containerResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.ContainerListResponseConverter != nil {
			response, err = config.ContainerListResponseConverter(bifrostCtx, containerResponse)
		} else {
			response = containerResponse
		}

	case schemas.ContainerRetrieveRequest:
		if containerReq.RetrieveRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container retrieve request"))
			return
		}
		containerResponse, bifrostErr := g.client.ContainerRetrieveRequest(bifrostCtx, containerReq.RetrieveRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, containerResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.ContainerRetrieveResponseConverter != nil {
			response, err = config.ContainerRetrieveResponseConverter(bifrostCtx, containerResponse)
		} else {
			response = containerResponse
		}

	case schemas.ContainerDeleteRequest:
		if containerReq.DeleteRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container delete request"))
			return
		}
		containerResponse, bifrostErr := g.client.ContainerDeleteRequest(bifrostCtx, containerReq.DeleteRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, containerResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.ContainerDeleteResponseConverter != nil {
			response, err = config.ContainerDeleteResponseConverter(bifrostCtx, containerResponse)
		} else {
			response = containerResponse
		}

	default:
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Unknown container request type"))
		return
	}

	if err != nil {
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert container response"))
		return
	}

	g.sendSuccess(ctx, bifrostCtx, config.ErrorConverter, response)
}

// handleContainerFileRequest handles container file API requests (create, list, retrieve, content, delete)
func (g *GenericRouter) handleContainerFileRequest(ctx *fasthttp.RequestCtx, config RouteConfig, req interface{}, containerFileReq *ContainerFileRequest, bifrostCtx *schemas.BifrostContext) {
	var response interface{}
	var err error

	switch containerFileReq.Type {
	case schemas.ContainerFileCreateRequest:
		if containerFileReq.CreateRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container file create request"))
			return
		}
		containerFileResponse, bifrostErr := g.client.ContainerFileCreateRequest(bifrostCtx, containerFileReq.CreateRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, containerFileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.ContainerFileCreateResponseConverter != nil {
			response, err = config.ContainerFileCreateResponseConverter(bifrostCtx, containerFileResponse)
		} else {
			response = containerFileResponse
		}

	case schemas.ContainerFileListRequest:
		if containerFileReq.ListRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container file list request"))
			return
		}
		containerFileResponse, bifrostErr := g.client.ContainerFileListRequest(bifrostCtx, containerFileReq.ListRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, containerFileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.ContainerFileListResponseConverter != nil {
			response, err = config.ContainerFileListResponseConverter(bifrostCtx, containerFileResponse)
		} else {
			response = containerFileResponse
		}

	case schemas.ContainerFileRetrieveRequest:
		if containerFileReq.RetrieveRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container file retrieve request"))
			return
		}
		containerFileResponse, bifrostErr := g.client.ContainerFileRetrieveRequest(bifrostCtx, containerFileReq.RetrieveRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, containerFileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.ContainerFileRetrieveResponseConverter != nil {
			response, err = config.ContainerFileRetrieveResponseConverter(bifrostCtx, containerFileResponse)
		} else {
			response = containerFileResponse
		}

	case schemas.ContainerFileContentRequest:
		if containerFileReq.ContentRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container file content request"))
			return
		}
		containerFileResponse, bifrostErr := g.client.ContainerFileContentRequest(bifrostCtx, containerFileReq.ContentRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, containerFileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		// For content requests, handle binary response specially if converter is set
		if config.ContainerFileContentResponseConverter != nil {
			response, err = config.ContainerFileContentResponseConverter(bifrostCtx, containerFileResponse)
			if err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert container file content response"))
				return
			}
			// Check if response is raw bytes - write directly without JSON encoding
			if rawBytes, ok := response.([]byte); ok {
				ctx.Response.Header.Set("Content-Type", containerFileResponse.ContentType)
				ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(rawBytes)))
				ctx.Response.SetBody(rawBytes)
			} else {
				g.sendSuccess(ctx, bifrostCtx, config.ErrorConverter, response)
			}
		} else {
			// Return raw binary content
			ctx.Response.Header.Set("Content-Type", containerFileResponse.ContentType)
			ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(containerFileResponse.Content)))
			ctx.Response.SetBody(containerFileResponse.Content)
		}
		return

	case schemas.ContainerFileDeleteRequest:
		if containerFileReq.DeleteRequest == nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Invalid container file delete request"))
			return
		}
		containerFileResponse, bifrostErr := g.client.ContainerFileDeleteRequest(bifrostCtx, containerFileReq.DeleteRequest)
		if bifrostErr != nil {
			g.sendError(ctx, bifrostCtx, config.ErrorConverter, bifrostErr)
			return
		}
		if config.PostCallback != nil {
			if err := config.PostCallback(ctx, req, containerFileResponse); err != nil {
				g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to execute post-request callback"))
				return
			}
		}
		if config.ContainerFileDeleteResponseConverter != nil {
			response, err = config.ContainerFileDeleteResponseConverter(bifrostCtx, containerFileResponse)
		} else {
			response = containerFileResponse
		}

	default:
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(nil, "Unknown container file request type"))
		return
	}

	if err != nil {
		g.sendError(ctx, bifrostCtx, config.ErrorConverter, newBifrostError(err, "failed to convert container file response"))
		return
	}

	g.sendSuccess(ctx, bifrostCtx, config.ErrorConverter, response)
}

// handleStreamingRequest handles streaming requests using Server-Sent Events (SSE)
func (g *GenericRouter) handleStreamingRequest(ctx *fasthttp.RequestCtx, config RouteConfig, bifrostReq *schemas.BifrostRequest, bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc) {
	// Set headers based on route type
	if config.Type == RouteConfigTypeBedrock {
		// AWS Event Stream headers for Bedrock
		ctx.SetContentType("application/vnd.amazon.eventstream")
		ctx.Response.Header.Set("x-amzn-bedrock-content-type", "application/json")
	} else {
		// Common SSE headers for other providers
		ctx.SetContentType("text/event-stream")
	}

	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")
	ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")

	// Use the cancellable context from ConvertToBifrostContext
	// ctx.Done() never fires here in practice: fasthttp.RequestCtx.Done only closes when the whole server shuts down, not when an individual connection drops.
	// As a result we'll leave the provider stream running until it naturally completes, even if the client went away (write error, network drop, etc.).
	// That keeps goroutines and upstream tokens alive long after the SSE writer has exited.
	//
	// We now get a cancellable context from ConvertToBifrostContext so we can cancel the upstream stream immediately when the client disconnects.
	var stream chan *schemas.BifrostStreamChunk
	var bifrostErr *schemas.BifrostError

	// Handle different request types
	if bifrostReq.TextCompletionRequest != nil {
		stream, bifrostErr = g.client.TextCompletionStreamRequest(bifrostCtx, bifrostReq.TextCompletionRequest)
	} else if bifrostReq.ChatRequest != nil {
		stream, bifrostErr = g.client.ChatCompletionStreamRequest(bifrostCtx, bifrostReq.ChatRequest)
	} else if bifrostReq.ResponsesRequest != nil {
		stream, bifrostErr = g.client.ResponsesStreamRequest(bifrostCtx, bifrostReq.ResponsesRequest)
	} else if bifrostReq.SpeechRequest != nil {
		stream, bifrostErr = g.client.SpeechStreamRequest(bifrostCtx, bifrostReq.SpeechRequest)
	} else if bifrostReq.TranscriptionRequest != nil {
		stream, bifrostErr = g.client.TranscriptionStreamRequest(bifrostCtx, bifrostReq.TranscriptionRequest)
	} else if bifrostReq.ImageGenerationRequest != nil {
		stream, bifrostErr = g.client.ImageGenerationStreamRequest(bifrostCtx, bifrostReq.ImageGenerationRequest)
	}

	// Get the streaming channel from Bifrost
	if bifrostErr != nil {
		// Send error in SSE format and cancel stream context since we're not proceeding
		cancel()
		g.sendStreamError(ctx, bifrostCtx, config, bifrostErr)
		return
	}

	// Check if streaming is configured for this route
	if config.StreamConfig == nil {
		// Cancel stream context since we're not proceeding, and close the stream channel to prevent goroutine leaks
		cancel()
		// Drain the stream channel to prevent goroutine leaks
		go func() {
			for range stream {
			}
		}()
		g.sendStreamError(ctx, bifrostCtx, config, newBifrostError(nil, "streaming is not supported for this integration"))
		return
	}

	// Handle streaming using the centralized approach
	// Pass cancel function so it can be called when the writer exits (errors, completion, etc.)
	g.handleStreaming(ctx, bifrostCtx, config, stream, cancel)
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
//
// CONTEXT CANCELLATION:
//
// The cancel function is called ONLY when client disconnects are detected via write errors.
// Bifrost handles cleanup internally for normal completion and errors, so we only cancel
// upstream streams when write errors indicate the client has disconnected.
func (g *GenericRouter) handleStreaming(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, config RouteConfig, streamChan chan *schemas.BifrostStreamChunk, cancel context.CancelFunc) {
	// Signal to tracing middleware that trace completion should be deferred
	// The streaming callback will complete the trace after the stream ends
	ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)

	// Get the trace completer function for use in the streaming callback
	traceCompleter, _ := ctx.UserValue(schemas.BifrostContextKeyTraceCompleter).(func())

	// Get stream chunk interceptor for plugin hooks
	interceptor := g.handlerStore.GetStreamChunkInterceptor()
	var httpReq *schemas.HTTPRequest	
	if interceptor != nil {
		httpReq = lib.BuildHTTPRequestFromFastHTTP(ctx)
	}

	// Use streaming response writer
	ctx.Response.SetBodyStreamWriter(func(w *bufio.Writer) {
		defer func() {
			schemas.ReleaseHTTPRequest(httpReq)
			w.Flush()
			// Complete the trace after streaming finishes
			// This ensures all spans (including llm.call) are properly ended before the trace is sent to OTEL
			if traceCompleter != nil {
				traceCompleter()
			}
		}()

		// Create encoder for AWS Event Stream if needed
		var eventStreamEncoder *eventstream.Encoder
		if config.Type == RouteConfigTypeBedrock {
			eventStreamEncoder = eventstream.NewEncoder()
		}

		shouldSendDoneMarker := true
		if config.Type == RouteConfigTypeAnthropic || strings.Contains(config.Path, "/responses") || strings.Contains(config.Path, "/images/generations") {
			shouldSendDoneMarker = false
		}

		// Process streaming responses
		for chunk := range streamChan {
			if chunk == nil {
				continue
			}

			// Note: We no longer check ctx.Done() here because fasthttp.RequestCtx.Done()
			// only closes when the whole server shuts down, not when an individual client disconnects.
			// Client disconnects are detected via write errors, which trigger the defer cancel() above.

			// Handle errors
			if chunk.BifrostError != nil {
				var errorResponse interface{}
				var errorJSON []byte
				var err error

				// Use stream error converter if available, otherwise fallback to regular error converter
				if config.StreamConfig != nil && config.StreamConfig.ErrorConverter != nil {
					errorResponse = config.StreamConfig.ErrorConverter(bifrostCtx, chunk.BifrostError)
				} else if config.ErrorConverter != nil {
					errorResponse = config.ErrorConverter(bifrostCtx, chunk.BifrostError)
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
						cancel() // Client disconnected (write error), cancel upstream stream
						return
					}
				} else {
					// STANDARD SSE FORMAT: The converter returned an object
					// This will be JSON marshaled and wrapped as "data: {json}\n\n"
					// Used by most providers (OpenAI, Google, etc.)
					errorJSON, err = sonic.Marshal(errorResponse)
					if err != nil {
						// Fallback to basic error if marshaling fails
						basicError := map[string]interface{}{
							"error": map[string]interface{}{
								"type":    "internal_error",
								"message": "An error occurred while processing your request",
							},
						}
						if errorJSON, err = sonic.Marshal(basicError); err != nil {
							cancel() // Can't send error (client likely disconnected), cancel upstream stream
							return
						}
					}

					// Send error as SSE data
					if _, err := fmt.Fprintf(w, "data: %s\n\n", errorJSON); err != nil {
						cancel() // Client disconnected (write error), cancel upstream stream
						return
					}
				}

				// Flush and return on error
				if err := w.Flush(); err != nil {
					cancel() // Client disconnected (write error), cancel upstream stream
					return
				}
				return // End stream on error, Bifrost handles cleanup internally
			} else {
				// Allow plugins to modify/filter the chunk via StreamChunkInterceptor
				if interceptor != nil {
					var err error
					chunk, err = interceptor.InterceptChunk(bifrostCtx, httpReq, chunk)
					if err != nil {
						if chunk == nil {							
							errorJSON, marshalErr := sonic.Marshal(map[string]string{"error": err.Error()})
							if marshalErr != nil {
								cancel()								
								return
							}
							// Return error event and stopping the streaming
							if _, err := fmt.Fprintf(w, "event: error\ndata: %s\n\n", errorJSON); err != nil {
								cancel()
								return
							}
							_ = w.Flush() 
							cancel()
							return
						}
						// Else add warn log and continue
						g.logger.Warn("%v", err)
					}
					if chunk == nil {
						// Skip chunk if plugin wants to skip it
						continue
					}
				}
				// Handle successful responses
				// Convert response to integration-specific streaming format
				var eventType string
				var convertedResponse interface{}
				var err error

				switch {
				case chunk.BifrostTextCompletionResponse != nil:
					eventType, convertedResponse, err = config.StreamConfig.TextStreamResponseConverter(bifrostCtx, chunk.BifrostTextCompletionResponse)
				case chunk.BifrostChatResponse != nil:
					eventType, convertedResponse, err = config.StreamConfig.ChatStreamResponseConverter(bifrostCtx, chunk.BifrostChatResponse)
				case chunk.BifrostResponsesStreamResponse != nil:
					eventType, convertedResponse, err = config.StreamConfig.ResponsesStreamResponseConverter(bifrostCtx, chunk.BifrostResponsesStreamResponse)
				case chunk.BifrostSpeechStreamResponse != nil:
					eventType, convertedResponse, err = config.StreamConfig.SpeechStreamResponseConverter(bifrostCtx, chunk.BifrostSpeechStreamResponse)
				case chunk.BifrostTranscriptionStreamResponse != nil:
					eventType, convertedResponse, err = config.StreamConfig.TranscriptionStreamResponseConverter(bifrostCtx, chunk.BifrostTranscriptionStreamResponse)
				case chunk.BifrostImageGenerationStreamResponse != nil:
					eventType, convertedResponse, err = config.StreamConfig.ImageGenerationStreamResponseConverter(bifrostCtx, chunk.BifrostImageGenerationStreamResponse)
				default:
					requestType := safeGetRequestType(chunk)
					convertedResponse, err = nil, fmt.Errorf("no response converter found for request type: %s", requestType)
				}

				if convertedResponse == nil && err == nil {
					// Skip streaming chunk if no response is available and no error is returned
					continue
				}

				if err != nil {
					// Log conversion error but continue processing
					log.Printf("Failed to convert streaming response: %v", err)
					continue
				}

				if eventType != "" {
					// OPENAI RESPONSES FORMAT: Use event: and data: lines for OpenAI responses API compatibility
					if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
						cancel() // Client disconnected (write error), cancel upstream stream
						return
					}
				}
				// Handle Bedrock Event Stream format
				if config.Type == RouteConfigTypeBedrock && eventStreamEncoder != nil {
					// We need to cast to BedrockStreamEvent to determine event type and structure
					if bedrockEvent, ok := convertedResponse.(*bedrock.BedrockStreamEvent); ok {
						// Passing it to interceptor to modify the event
						// Convert to sequence of specific Bedrock events
						events := bedrockEvent.ToEncodedEvents()

						// Send all collected events
						for _, evt := range events {
							jsonData, err := sonic.Marshal(evt.Payload)
							if err != nil {
								log.Printf("Failed to marshal bedrock payload: %v", err)
								continue
							}

							headers := eventstream.Headers{
								{
									Name:  ":content-type",
									Value: eventstream.StringValue("application/json"),
								},
								{
									Name:  ":event-type",
									Value: eventstream.StringValue(evt.EventType),
								},
								{
									Name:  ":message-type",
									Value: eventstream.StringValue("event"),
								},
							}

							message := eventstream.Message{
								Headers: headers,
								Payload: jsonData,
							}

							if err := eventStreamEncoder.Encode(w, message); err != nil {
								log.Printf("[Bedrock Stream] Failed to encode message: %v", err)
								cancel()
								return
							}

							// Flush each message to ensure proper delivery
							if err := w.Flush(); err != nil {
								log.Printf("[Bedrock Stream] Failed to flush writer: %v", err)
								cancel()
								return
							}
						}
					}
					// Continue to next chunk (we handled flushing internally)
					continue
				} else if sseString, ok := convertedResponse.(string); ok {
					// CUSTOM SSE FORMAT: The converter returned a complete SSE string
					// This is used by providers like Anthropic that need custom event types
					// Example: "event: content_block_delta\ndata: {...}\n\n"
					if !strings.HasPrefix(sseString, "data: ") && !strings.HasPrefix(sseString, "event: ") {
						sseString = fmt.Sprintf("data: %s\n\n", sseString)
					}
					if _, err := fmt.Fprint(w, sseString); err != nil {
						cancel() // Client disconnected (write error), cancel upstream stream
						return
					}
				} else {
					// STANDARD SSE FORMAT: The converter returned an object
					// This will be JSON marshaled and wrapped as "data: {json}\n\n"
					// Used by most providers (OpenAI chat/completions, Google, etc.)
					responseJSON, err := sonic.Marshal(convertedResponse)
					if err != nil {
						// Log JSON marshaling error but continue processing
						log.Printf("Failed to marshal streaming response: %v", err)
						continue
					}

					// Send as SSE data
					if _, err := fmt.Fprintf(w, "data: %s\n\n", responseJSON); err != nil {
						cancel() // Client disconnected (write error), cancel upstream stream
						return
					}
				}

				// Flush immediately to send the chunk
				if err := w.Flush(); err != nil {
					cancel() // Client disconnected (write error), cancel upstream stream
					return
				}
			}
		}

		// Only send the [DONE] marker for plain SSE APIs that expect it.
		// Do NOT send [DONE] for the following cases:
		//   - OpenAI "responses" API and Anthropic messages API: they signal completion by simply closing the stream, not sending [DONE].
		//   - Bedrock: uses AWS Event Stream format rather than SSE with [DONE].
		// Bifrost handles any additional cleanup internally on normal stream completion.
		if shouldSendDoneMarker && config.Type != RouteConfigTypeGenAI && config.Type != RouteConfigTypeBedrock {
			if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
				g.logger.Warn("Failed to write SSE done marker: %v", err)
				cancel()
				return // End stream on error, Bifrost handles cleanup internally
			}
		}
	})
}
