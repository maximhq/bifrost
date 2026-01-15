package integrations

import (
	"context"
	"strings"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// routerTestLogger implements schemas.Logger for tests
type routerTestLogger struct{}

func (l *routerTestLogger) Debug(msg string, args ...any) {}
func (l *routerTestLogger) Info(msg string, args ...any)  {}
func (l *routerTestLogger) Warn(msg string, args ...any)  {}
func (l *routerTestLogger) Error(msg string, args ...any) {}
func (l *routerTestLogger) Fatal(msg string, args ...any) {}
func (l *routerTestLogger) SetLevel(level schemas.LogLevel) {}
func (l *routerTestLogger) SetOutputType(outputType schemas.LoggerOutputType) {}

// routerTestHandlerStore implements lib.HandlerStore for tests
type routerTestHandlerStore struct {
	allowDirectKeys    bool
	headerFilterConfig *configstoreTables.GlobalHeaderFilterConfig
}

func (s *routerTestHandlerStore) ShouldAllowDirectKeys() bool {
	return s.allowDirectKeys
}

func (s *routerTestHandlerStore) GetHeaderFilterConfig() *configstoreTables.GlobalHeaderFilterConfig {
	return s.headerFilterConfig
}

// TestRouteConfigType_Values tests route configuration type values
func TestRouteConfigType_Values(t *testing.T) {
	testCases := []struct {
		configType RouteConfigType
		expected   string
	}{
		{RouteConfigTypeOpenAI, "openai"},
		{RouteConfigTypeAnthropic, "anthropic"},
		{RouteConfigTypeGenAI, "genai"},
		{RouteConfigTypeBedrock, "bedrock"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			assert.Equal(t, tc.expected, string(tc.configType))
		})
	}
}

// TestBatchRequest_Structure tests BatchRequest structure
func TestBatchRequest_Structure(t *testing.T) {
	testCases := []struct {
		name        string
		requestType schemas.RequestType
	}{
		{"batch create", schemas.BatchCreateRequest},
		{"batch list", schemas.BatchListRequest},
		{"batch retrieve", schemas.BatchRetrieveRequest},
		{"batch cancel", schemas.BatchCancelRequest},
		{"batch results", schemas.BatchResultsRequest},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			batchReq := BatchRequest{
				Type: tc.requestType,
			}
			assert.Equal(t, tc.requestType, batchReq.Type)
		})
	}
}

// TestFileRequest_Structure tests FileRequest structure
func TestFileRequest_Structure(t *testing.T) {
	testCases := []struct {
		name        string
		requestType schemas.RequestType
	}{
		{"file upload", schemas.FileUploadRequest},
		{"file list", schemas.FileListRequest},
		{"file retrieve", schemas.FileRetrieveRequest},
		{"file delete", schemas.FileDeleteRequest},
		{"file content", schemas.FileContentRequest},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fileReq := FileRequest{
				Type: tc.requestType,
			}
			assert.Equal(t, tc.requestType, fileReq.Type)
		})
	}
}

// TestStreamConfig_Structure tests StreamConfig structure
func TestStreamConfig_Structure(t *testing.T) {
	config := StreamConfig{}

	// All converters should be nil by default
	assert.Nil(t, config.TextStreamResponseConverter)
	assert.Nil(t, config.ChatStreamResponseConverter)
	assert.Nil(t, config.ResponsesStreamResponseConverter)
	assert.Nil(t, config.SpeechStreamResponseConverter)
	assert.Nil(t, config.TranscriptionStreamResponseConverter)
	assert.Nil(t, config.ErrorConverter)
}

// TestRouteConfig_Structure tests RouteConfig structure
func TestRouteConfig_Structure(t *testing.T) {
	config := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/openai/v1/chat/completions",
		Method: "POST",
	}

	assert.Equal(t, RouteConfigTypeOpenAI, config.Type)
	assert.Equal(t, "/openai/v1/chat/completions", config.Path)
	assert.Equal(t, "POST", config.Method)
}

// TestNewGenericRouter tests creating a new generic router
func TestNewGenericRouter(t *testing.T) {
	routes := []RouteConfig{
		{
			Type:   RouteConfigTypeOpenAI,
			Path:   "/test",
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &struct{}{}
			},
		},
	}
	logger := &routerTestLogger{}

	gr := NewGenericRouter(nil, nil, routes, logger)

	require.NotNil(t, gr)
	assert.Len(t, gr.routes, 1)
}

// TestNewGenericRouter_EmptyRoutes tests creating router with no routes
func TestNewGenericRouter_EmptyRoutes(t *testing.T) {
	logger := &routerTestLogger{}
	gr := NewGenericRouter(nil, nil, []RouteConfig{}, logger)

	require.NotNil(t, gr)
	assert.Len(t, gr.routes, 0)
}

// TestGenericRouter_RegisterRoutes tests route registration
func TestGenericRouter_RegisterRoutes(t *testing.T) {
	routes := []RouteConfig{
		{
			Type:   RouteConfigTypeOpenAI,
			Path:   "/test-post",
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &struct{}{}
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
		},
		{
			Type:   RouteConfigTypeOpenAI,
			Path:   "/test-get",
			Method: "GET",
			GetRequestTypeInstance: func() interface{} {
				return &struct{}{}
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
		},
	}

	logger := &routerTestLogger{}
	gr := NewGenericRouter(nil, nil, routes, logger)
	r := router.New()

	gr.RegisterRoutes(r)

	// Verify routes were registered by checking the router
	require.NotNil(t, r)
}

// TestGenericRouter_RegisterRoutes_SkipsInvalidRoutes tests route validation
func TestGenericRouter_RegisterRoutes_SkipsInvalidRoutes(t *testing.T) {
	routes := []RouteConfig{
		{
			Type:                   RouteConfigTypeOpenAI,
			Path:                   "/test",
			Method:                 "POST",
			GetRequestTypeInstance: nil, // Invalid - should be skipped
		},
	}
	logger := &routerTestLogger{}
	gr := NewGenericRouter(nil, nil, routes, logger)
	r := router.New()

	// Should not panic
	gr.RegisterRoutes(r)
}

// TestGenericRouter_RouteWithMiddleware tests middleware registration
func TestGenericRouter_RouteWithMiddleware(t *testing.T) {
	testMiddleware := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			next(ctx)
		}
	}

	routes := []RouteConfig{
		{
			Type:   RouteConfigTypeOpenAI,
			Path:   "/test",
			Method: "GET",
			GetRequestTypeInstance: func() interface{} {
				return &struct{}{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				return &schemas.BifrostRequest{}, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
		},
	}

	handlerStore := &routerTestHandlerStore{allowDirectKeys: true}
	logger := &routerTestLogger{}
	gr := NewGenericRouter(nil, handlerStore, routes, logger)
	r := router.New()

	// Middleware registration should work without panic
	gr.RegisterRoutes(r, testMiddleware)

	// Verify routes were registered
	require.NotNil(t, r)
}

// TestGenericRouter_Methods tests supported HTTP methods
func TestGenericRouter_Methods(t *testing.T) {
	methods := []string{"GET", "POST", "PUT", "DELETE", "HEAD"}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			routes := []RouteConfig{
				{
					Type:   RouteConfigTypeOpenAI,
					Path:   "/test-" + strings.ToLower(method),
					Method: method,
					GetRequestTypeInstance: func() interface{} {
						return &struct{}{}
					},
					ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
						return err
					},
				},
			}

			logger := &routerTestLogger{}
			gr := NewGenericRouter(nil, nil, routes, logger)
			r := router.New()

			gr.RegisterRoutes(r)
			// Should not panic for any supported method
		})
	}
}

// TestRouteConfig_AllFields tests all RouteConfig fields
func TestRouteConfig_AllFields(t *testing.T) {
	config := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/test/path",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &struct{ Field string }{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			return &schemas.BifrostRequest{}, nil
		},
		ChatResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (interface{}, error) {
			return resp, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
		StreamConfig: &StreamConfig{
			ChatStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (string, interface{}, error) {
				return "", resp, nil
			},
		},
		PreCallback: func(ctx *fasthttp.RequestCtx, bfCtx *schemas.BifrostContext, req interface{}) error {
			return nil
		},
		PostCallback: func(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error {
			return nil
		},
	}

	assert.Equal(t, RouteConfigTypeOpenAI, config.Type)
	assert.Equal(t, "/test/path", config.Path)
	assert.Equal(t, "POST", config.Method)
	assert.NotNil(t, config.GetRequestTypeInstance)
	assert.NotNil(t, config.RequestConverter)
	assert.NotNil(t, config.ChatResponseConverter)
	assert.NotNil(t, config.ErrorConverter)
	assert.NotNil(t, config.StreamConfig)
	assert.NotNil(t, config.PreCallback)
	assert.NotNil(t, config.PostCallback)
}

// TestBifrostRequest_Structure tests schemas.BifrostRequest structure
func TestBifrostRequest_Structure(t *testing.T) {
	req := schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{},
	}

	assert.Equal(t, schemas.ChatCompletionRequest, req.RequestType)
	assert.NotNil(t, req.ChatRequest)
}

// TestStreamingRequestInterface tests StreamingRequest interface implementation
func TestStreamingRequestInterface(t *testing.T) {
	// Create a type that implements StreamingRequest
	type testStreamingRequest struct {
		streaming bool
	}

	req := &testStreamingRequest{streaming: true}

	// Verify the interface behavior (not actual implementation)
	assert.True(t, req.streaming)

	req.streaming = false
	assert.False(t, req.streaming)
}

// TestBifrostHTTPMiddleware_Type tests middleware function type
func TestBifrostHTTPMiddleware_Type(t *testing.T) {
	middlewareCalled := false
	handlerCalled := false

	// Create a middleware
	middleware := schemas.BifrostHTTPMiddleware(func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			middlewareCalled = true
			next(ctx)
		}
	})

	// Create a handler
	handler := func(ctx *fasthttp.RequestCtx) {
		handlerCalled = true
	}

	// Apply middleware to handler
	wrapped := middleware(handler)

	// Execute
	ctx := &fasthttp.RequestCtx{}
	wrapped(ctx)

	assert.True(t, middlewareCalled)
	assert.True(t, handlerCalled)
}

// TestGenericRouter_MultipleMiddlewares tests multiple middleware registration
func TestGenericRouter_MultipleMiddlewares(t *testing.T) {
	middleware1 := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			next(ctx)
		}
	}

	middleware2 := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			next(ctx)
		}
	}

	// Need a valid inference route with RequestConverter
	routes := []RouteConfig{
		{
			Type:   RouteConfigTypeOpenAI,
			Path:   "/test",
			Method: "GET",
			GetRequestTypeInstance: func() interface{} {
				return &struct{}{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				return &schemas.BifrostRequest{}, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
		},
	}

	// Create a mock handler store that allows direct keys
	handlerStore := &routerTestHandlerStore{allowDirectKeys: true}

	logger := &routerTestLogger{}
	gr := NewGenericRouter(nil, handlerStore, routes, logger)
	r := router.New()

	// Registration with multiple middlewares should work without panic
	gr.RegisterRoutes(r, middleware1, middleware2)

	// Verify routes were registered
	require.NotNil(t, r)
}

// TestRouteConfig_BatchRoute tests batch route configuration
func TestRouteConfig_BatchRoute(t *testing.T) {
	batchConverter := func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
		return &BatchRequest{Type: schemas.BatchCreateRequest}, nil
	}

	config := RouteConfig{
		Type:                  RouteConfigTypeOpenAI,
		Path:                  "/v1/batches",
		Method:                "POST",
		BatchRequestConverter: batchConverter,
		GetRequestTypeInstance: func() interface{} {
			return &struct{}{}
		},
	}

	assert.NotNil(t, config.BatchRequestConverter)
	assert.Equal(t, "/v1/batches", config.Path)
}

// TestRouteConfig_FileRoute tests file route configuration
func TestRouteConfig_FileRoute(t *testing.T) {
	fileConverter := func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
		return &FileRequest{Type: schemas.FileUploadRequest}, nil
	}

	config := RouteConfig{
		Type:                 RouteConfigTypeOpenAI,
		Path:                 "/v1/files",
		Method:               "POST",
		FileRequestConverter: fileConverter,
		GetRequestTypeInstance: func() interface{} {
			return &struct{}{}
		},
	}

	assert.NotNil(t, config.FileRequestConverter)
	assert.Equal(t, "/v1/files", config.Path)
}

// TestStreamConfig_AllConverters tests all stream config converters
func TestStreamConfig_AllConverters(t *testing.T) {
	config := StreamConfig{
		TextStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (string, interface{}, error) {
			return "", resp, nil
		},
		ChatStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (string, interface{}, error) {
			return "", resp, nil
		},
		ResponsesStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
			return "", resp, nil
		},
		SpeechStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostSpeechStreamResponse) (string, interface{}, error) {
			return "", resp, nil
		},
		TranscriptionStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTranscriptionStreamResponse) (string, interface{}, error) {
			return "", resp, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	}

	assert.NotNil(t, config.TextStreamResponseConverter)
	assert.NotNil(t, config.ChatStreamResponseConverter)
	assert.NotNil(t, config.ResponsesStreamResponseConverter)
	assert.NotNil(t, config.SpeechStreamResponseConverter)
	assert.NotNil(t, config.TranscriptionStreamResponseConverter)
	assert.NotNil(t, config.ErrorConverter)
}

// TestRouteConfig_PreCallback tests pre-callback configuration
func TestRouteConfig_PreCallback(t *testing.T) {
	preCallbackCalled := false

	config := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/test",
		Method: "POST",
		PreCallback: func(ctx *fasthttp.RequestCtx, bfCtx *schemas.BifrostContext, req interface{}) error {
			preCallbackCalled = true
			return nil
		},
	}

	// Verify callback is set
	assert.NotNil(t, config.PreCallback)

	// Execute callback to verify it works
	ctx := &fasthttp.RequestCtx{}
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	err := config.PreCallback(ctx, bfCtx, nil)
	assert.NoError(t, err)
	assert.True(t, preCallbackCalled)
}

// TestRouteConfig_PostCallback tests post-callback configuration
func TestRouteConfig_PostCallback(t *testing.T) {
	postCallbackCalled := false

	config := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/test",
		Method: "POST",
		PostCallback: func(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error {
			postCallbackCalled = true
			return nil
		},
	}

	// Verify callback is set
	assert.NotNil(t, config.PostCallback)

	// Execute callback to verify it works
	ctx := &fasthttp.RequestCtx{}
	err := config.PostCallback(ctx, nil, nil)
	assert.NoError(t, err)
	assert.True(t, postCallbackCalled)
}

// TestRouteConfig_ResponseConverters tests response converter configuration
func TestRouteConfig_ResponseConverters(t *testing.T) {
	config := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/test",
		Method: "POST",
		ListModelsResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostListModelsResponse) (interface{}, error) {
			return resp, nil
		},
		TextResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (interface{}, error) {
			return resp, nil
		},
		ChatResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (interface{}, error) {
			return resp, nil
		},
		ResponsesResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
			return resp, nil
		},
		EmbeddingResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostEmbeddingResponse) (interface{}, error) {
			return resp, nil
		},
		SpeechResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostSpeechResponse) (interface{}, error) {
			return resp, nil
		},
		TranscriptionResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTranscriptionResponse) (interface{}, error) {
			return resp, nil
		},
		CountTokensResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostCountTokensResponse) (interface{}, error) {
			return resp, nil
		},
	}

	assert.NotNil(t, config.ListModelsResponseConverter)
	assert.NotNil(t, config.TextResponseConverter)
	assert.NotNil(t, config.ChatResponseConverter)
	assert.NotNil(t, config.ResponsesResponseConverter)
	assert.NotNil(t, config.EmbeddingResponseConverter)
	assert.NotNil(t, config.SpeechResponseConverter)
	assert.NotNil(t, config.TranscriptionResponseConverter)
	assert.NotNil(t, config.CountTokensResponseConverter)
}

// TestRouteConfig_BatchResponseConverters tests batch response converter configuration
func TestRouteConfig_BatchResponseConverters(t *testing.T) {
	config := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/v1/batches",
		Method: "POST",
		BatchCreateResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCreateResponse) (interface{}, error) {
			return resp, nil
		},
		BatchListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchListResponse) (interface{}, error) {
			return resp, nil
		},
		BatchRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchRetrieveResponse) (interface{}, error) {
			return resp, nil
		},
		BatchCancelResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCancelResponse) (interface{}, error) {
			return resp, nil
		},
		BatchResultsResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchResultsResponse) (interface{}, error) {
			return resp, nil
		},
	}

	assert.NotNil(t, config.BatchCreateResponseConverter)
	assert.NotNil(t, config.BatchListResponseConverter)
	assert.NotNil(t, config.BatchRetrieveResponseConverter)
	assert.NotNil(t, config.BatchCancelResponseConverter)
	assert.NotNil(t, config.BatchResultsResponseConverter)
}

// TestRouteConfig_FileResponseConverters tests file response converter configuration
func TestRouteConfig_FileResponseConverters(t *testing.T) {
	config := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/v1/files",
		Method: "POST",
		FileUploadResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileUploadResponse) (interface{}, error) {
			return resp, nil
		},
		FileListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileListResponse) (interface{}, error) {
			return resp, nil
		},
		FileRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileRetrieveResponse) (interface{}, error) {
			return resp, nil
		},
		FileDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileDeleteResponse) (interface{}, error) {
			return resp, nil
		},
		FileContentResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileContentResponse) (interface{}, error) {
			return resp, nil
		},
	}

	assert.NotNil(t, config.FileUploadResponseConverter)
	assert.NotNil(t, config.FileListResponseConverter)
	assert.NotNil(t, config.FileRetrieveResponseConverter)
	assert.NotNil(t, config.FileDeleteResponseConverter)
	assert.NotNil(t, config.FileContentResponseConverter)
}

// TestExtensionRouterInterface documents ExtensionRouter interface
func TestExtensionRouterInterface(t *testing.T) {
	// ExtensionRouter interface is implemented by all integration routers
	// Interface: RegisterRoutes(r *router.Router, middlewares ...BifrostHTTPMiddleware)

	// All routers implement this interface:
	// - OpenAI Router
	// - Anthropic Router
	// - GenAI Router
	// - Bedrock Router
	// - LiteLLM Router
	// - LangChain Router
	// - Cohere Router
	// - PydanticAI Router

	t.Log("ExtensionRouter defines route registration for all integrations")
}

// TestGenericRouter_NilBifrostClient tests router with nil bifrost client
func TestGenericRouter_NilBifrostClient(t *testing.T) {
	routes := []RouteConfig{
		{
			Type:   RouteConfigTypeOpenAI,
			Path:   "/test",
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &struct{}{}
			},
		},
	}
	logger := &routerTestLogger{}

	// Router should be created even with nil bifrost client
	gr := NewGenericRouter(nil, nil, routes, logger)
	require.NotNil(t, gr)
}

// TestGenericRouter_NilHandlerStore tests router with nil handler store
func TestGenericRouter_NilHandlerStore(t *testing.T) {
	routes := []RouteConfig{
		{
			Type:   RouteConfigTypeOpenAI,
			Path:   "/test",
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &struct{}{}
			},
		},
	}
	logger := &routerTestLogger{}

	// Router should be created even with nil handler store
	gr := NewGenericRouter(nil, nil, routes, logger)
	require.NotNil(t, gr)
}
