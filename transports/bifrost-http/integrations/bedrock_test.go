package integrations

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// mockHandlerStore implements lib.HandlerStore for testing
type mockHandlerStore struct {
	headerMatcher              *lib.HeaderMatcher
	availableProviders         []schemas.ModelProvider
	mcpHeaderCombinedAllowlist schemas.WhiteList
	modelCatalog               *modelcatalog.ModelCatalog
}

func (m *mockHandlerStore) GetHeaderMatcher() *lib.HeaderMatcher {
	return m.headerMatcher
}

func (m *mockHandlerStore) GetStreamChunkInterceptor() lib.StreamChunkInterceptor {
	return nil
}

func (m *mockHandlerStore) GetAsyncJobExecutor() *logstore.AsyncJobExecutor {
	return nil
}

func (m *mockHandlerStore) GetAsyncJobResultTTL() int {
	return 3600
}

func (m *mockHandlerStore) GetKVStore() *kvstore.Store {
	return nil
}

func (m *mockHandlerStore) GetMCPHeaderCombinedAllowlist() schemas.WhiteList {
	return m.mcpHeaderCombinedAllowlist
}

func (m *mockHandlerStore) ShouldAllowPerRequestStorageOverride() bool {
	return false
}

func (m *mockHandlerStore) ShouldAllowPerRequestRawOverride() bool {
	return false
}

func (m *mockHandlerStore) ShouldAllowDirectKeys() bool {
	return false
}

func (m *mockHandlerStore) GetMCPExternalServerURL() string {
	return ""
}

func (m *mockHandlerStore) GetMCPExternalClientURL() string {
	return ""
}

func (m *mockHandlerStore) GetModelCatalog() *modelcatalog.ModelCatalog {
	return m.modelCatalog
}

// Ensure mockHandlerStore implements lib.HandlerStore
var _ lib.HandlerStore = (*mockHandlerStore)(nil)

func TestGenericRouter_MarkDeprecatedListModelsResponseUsesCatalog(t *testing.T) {
	pricingPath := filepath.Join(t.TempDir(), "pricing.json")
	pricingJSON := []byte(`{
		"deprecated-model": {"provider":"openai","mode":"chat","base_model":"deprecated-model","is_deprecated":true},
		"current-model": {"provider":"openai","mode":"chat","base_model":"current-model"}
	}`)
	require.NoError(t, os.WriteFile(pricingPath, pricingJSON, 0o600))
	ds := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + pricingPath})
	require.NoError(t, ds.LoadFromURLIntoMemory(t.Context()))
	router := NewGenericRouter(nil, &mockHandlerStore{modelCatalog: modelcatalog.NewTestCatalogWithDatasheet(ds)}, nil, nil, nil)
	resp := &schemas.BifrostListModelsResponse{Data: []schemas.Model{
		{ID: "openai/deprecated-model"},
		{ID: "openai/current-model"},
		{ID: "openai/provider-deprecated", IsDeprecated: true},
	}}

	router.markDeprecatedListModelsResponse(resp)

	// No models are removed; deprecated ones are flagged instead.
	require.Len(t, resp.Data, 3)
	byID := map[string]schemas.Model{}
	for _, m := range resp.Data {
		byID[m.ID] = m
	}
	assert.True(t, byID["openai/deprecated-model"].IsDeprecated)
	assert.False(t, byID["openai/current-model"].IsDeprecated)
	assert.True(t, byID["openai/provider-deprecated"].IsDeprecated)
}

func Test_parseS3URI(t *testing.T) {
	tests := []struct {
		name       string
		uri        string
		wantBucket string
		wantKey    string
	}{
		{
			name:       "full S3 URI with key",
			uri:        "s3://my-bucket/path/to/file.jsonl",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.jsonl",
		},
		{
			name:       "S3 URI with bucket only",
			uri:        "s3://my-bucket/",
			wantBucket: "my-bucket",
			wantKey:    "",
		},
		{
			name:       "S3 URI with bucket no trailing slash",
			uri:        "s3://my-bucket",
			wantBucket: "my-bucket",
			wantKey:    "",
		},
		{
			name:       "plain bucket name",
			uri:        "my-bucket",
			wantBucket: "my-bucket",
			wantKey:    "",
		},
		{
			name:       "S3 URI with nested key",
			uri:        "s3://bucket-name/folder1/folder2/file.txt",
			wantBucket: "bucket-name",
			wantKey:    "folder1/folder2/file.txt",
		},
		{
			name:       "empty string",
			uri:        "",
			wantBucket: "",
			wantKey:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBucket, gotKey := parseS3URI(tt.uri)
			assert.Equal(t, tt.wantBucket, gotBucket, "bucket mismatch")
			assert.Equal(t, tt.wantKey, gotKey, "key mismatch")
		})
	}
}

func Test_createBedrockRouteConfigs(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	routes := CreateBedrockRouteConfigs("/bedrock", handlerStore)

	assert.Len(t, routes, 8, "should have 8 bedrock routes")

	expectedRoutes := []struct {
		path   string
		method string
	}{
		{"/bedrock/inference-profiles", "GET"},
		{"/bedrock/inference-profiles/{inferenceProfileIdentifier}", "GET"},
		{"/bedrock/model/{modelId}/converse", "POST"},
		{"/bedrock/model/{modelId}/converse-stream", "POST"},
		{"/bedrock/model/{modelId}/invoke-with-response-stream", "POST"},
		{"/bedrock/model/{modelId}/invoke", "POST"},
		{"/bedrock/rerank", "POST"},
		{"/bedrock/model/{modelId}/count-tokens", "POST"},
	}

	for i, expected := range expectedRoutes {
		assert.Equal(t, expected.path, routes[i].Path, "route %d path mismatch", i)
		assert.Equal(t, expected.method, routes[i].Method, "route %d method mismatch", i)
		assert.Equal(t, RouteConfigTypeBedrock, routes[i].Type, "route %d type mismatch", i)
		assert.NotNil(t, routes[i].GetRequestTypeInstance, "route %d GetRequestTypeInstance should not be nil", i)
		assert.NotNil(t, routes[i].ErrorConverter, "route %d ErrorConverter should not be nil", i)
	}
}

func Test_createBedrockConverseRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockConverseRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/converse", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.GetRequestTypeInstance)
	assert.NotNil(t, route.RequestConverter)
	assert.NotNil(t, route.ResponsesResponseConverter)
	assert.NotNil(t, route.ErrorConverter)
	assert.NotNil(t, route.PreCallback)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockConverseRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockConverseRequest")
}

func Test_createBedrockConverseStreamRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockConverseStreamRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/converse-stream", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.StreamConfig)
	assert.NotNil(t, route.StreamConfig.ErrorConverter)
	assert.NotNil(t, route.StreamConfig.ResponsesStreamResponseConverter)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockConverseRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockConverseRequest")
}

func Test_createBedrockInvokeRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockInvokeRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/invoke", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.TextResponseConverter)
	assert.NotNil(t, route.ResponsesResponseConverter)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockInvokeRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockInvokeRequest")
}

func Test_createBedrockInvokeWithResponseStreamRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockInvokeWithResponseStreamRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/invoke-with-response-stream", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.StreamConfig)
	assert.NotNil(t, route.StreamConfig.ErrorConverter)
	assert.NotNil(t, route.StreamConfig.TextStreamResponseConverter)
	assert.NotNil(t, route.StreamConfig.ResponsesStreamResponseConverter)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockInvokeRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockInvokeRequest")
}

func Test_bedrockStreamErrorConverterEncodesEventStreamException(t *testing.T) {
	errType := "PluginDenied"
	statusCode := fasthttp.StatusForbidden
	bifrostErr := &schemas.BifrostError{
		Type:       &errType,
		StatusCode: &statusCode,
		Error: &schemas.ErrorField{
			Message: "blocked by policy",
		},
	}

	converted := bedrockStreamErrorConverter(nil, bifrostErr)
	exception, ok := converted.(*bedrockEventStreamException)
	require.True(t, ok)

	reader := lib.NewSSEStreamReader()
	require.True(t, sendBedrockEventStreamException(reader, eventstream.NewEncoder(), exception, bifrost.NewNoOpLogger()))
	reader.Done()

	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.False(t, bytes.HasPrefix(body, []byte("data: ")), "Bedrock streaming errors must not be plain SSE")

	msg, err := eventstream.NewDecoder().Decode(bytes.NewReader(body), nil)
	require.NoError(t, err)
	assert.Equal(t, "exception", eventStreamHeaderString(t, msg.Headers, ":message-type"))
	assert.Equal(t, "PluginDenied", eventStreamHeaderString(t, msg.Headers, ":exception-type"))
	assert.JSONEq(t, `{"__type":"PluginDenied","message":"blocked by policy"}`, string(msg.Payload))
	assert.False(t, strings.Contains(string(body), "data:"), "AWS EventStream bytes must not contain SSE framing")
}

func Test_toBedrockEventStreamExceptionAcceptsBedrockError(t *testing.T) {
	exception, ok := toBedrockEventStreamException(&bedrock.BedrockError{
		Type:    "ValidationException",
		Message: "invalid request",
	})
	require.True(t, ok)

	reader := lib.NewSSEStreamReader()
	require.True(t, sendBedrockEventStreamException(reader, eventstream.NewEncoder(), exception, bifrost.NewNoOpLogger()))
	reader.Done()

	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.False(t, bytes.HasPrefix(body, []byte("data: ")), "Bedrock streaming errors must not be plain SSE")

	msg, err := eventstream.NewDecoder().Decode(bytes.NewReader(body), nil)
	require.NoError(t, err)
	assert.Equal(t, "exception", eventStreamHeaderString(t, msg.Headers, ":message-type"))
	assert.Equal(t, "ValidationException", eventStreamHeaderString(t, msg.Headers, ":exception-type"))
	assert.JSONEq(t, `{"__type":"ValidationException","message":"invalid request"}`, string(msg.Payload))
}

func Test_handleStreamingBedrockUnknownErrorResponseFallsBackToEventStreamException(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 1)
	statusCode := fasthttp.StatusInternalServerError
	stream <- &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			StatusCode: &statusCode,
			Error: &schemas.ErrorField{
				Message: "plugin returned an unsupported error envelope",
			},
		},
	}
	close(stream)

	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, bifrost.NewNoOpLogger())
	ctx := &fasthttp.RequestCtx{}
	cancelCalled := false
	router.handleStreaming(ctx, nil, RouteConfig{
		Type: RouteConfigTypeBedrock,
		StreamConfig: &StreamConfig{
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return map[string]interface{}{
					"unexpected": "shape",
				}
			},
		},
	}, stream, func() {
		cancelCalled = true
	})

	body, err := io.ReadAll(ctx.Response.BodyStream())
	require.NoError(t, err)
	require.NotEmpty(t, body)
	require.False(t, bytes.HasPrefix(body, []byte("data: ")), "Bedrock fallback errors must not be plain SSE")

	msg, err := eventstream.NewDecoder().Decode(bytes.NewReader(body), nil)
	require.NoError(t, err)
	assert.Equal(t, "exception", eventStreamHeaderString(t, msg.Headers, ":message-type"))
	assert.Equal(t, "InternalServerException", eventStreamHeaderString(t, msg.Headers, ":exception-type"))
	assert.JSONEq(t, `{"__type":"InternalServerException","message":"An error occurred while processing your request"}`, string(msg.Payload))
	assert.False(t, cancelCalled, "fallback write should not cancel unless the client disconnects")
}

func eventStreamHeaderString(t *testing.T, headers eventstream.Headers, name string) string {
	t.Helper()
	for _, header := range headers {
		if header.Name == name {
			value, ok := header.Value.Get().(string)
			require.True(t, ok, "%s header must be a string", name)
			return value
		}
	}
	t.Fatalf("missing EventStream header %s", name)
	return ""
}

func Test_createBedrockRerankRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockRerankRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/rerank", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.GetHTTPRequestType)
	assert.Equal(t, schemas.RerankRequest, route.GetHTTPRequestType(nil))
	assert.NotNil(t, route.GetRequestTypeInstance)
	assert.NotNil(t, route.RequestConverter)
	assert.NotNil(t, route.RerankResponseConverter)
	assert.NotNil(t, route.ErrorConverter)
	assert.NotNil(t, route.PreCallback)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockRerankRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockRerankRequest")
}

func Test_createBedrockRerankResponseConverterUsesRawResponse(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockRerankRouteConfig("/bedrock", handlerStore)
	require.NotNil(t, route.RerankResponseConverter)

	raw := map[string]interface{}{"results": []interface{}{}}
	resp := &schemas.BifrostRerankResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.Bedrock,
			RawResponse: raw,
		},
	}
	converted, err := route.RerankResponseConverter(nil, resp)
	require.NoError(t, err)
	assert.Equal(t, raw, converted)
}

func Test_createBedrockRerankRouteRequestConverter(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockRerankRouteConfig("/bedrock", handlerStore)
	require.NotNil(t, route.RequestConverter)

	topN := 1
	req := &bedrock.BedrockRerankRequest{
		Queries: []bedrock.BedrockRerankQuery{
			{
				Type:      "TEXT",
				TextQuery: bedrock.BedrockRerankTextRef{Text: "capital of france"},
			},
		},
		Sources: []bedrock.BedrockRerankSource{
			{
				Type: "INLINE",
				InlineDocumentSource: bedrock.BedrockRerankInlineSource{
					Type:         "TEXT",
					TextDocument: bedrock.BedrockRerankTextValue{Text: "Paris is capital of France"},
				},
			},
		},
		RerankingConfiguration: bedrock.BedrockRerankingConfiguration{
			Type: "BEDROCK_RERANKING_MODEL",
			BedrockRerankingConfiguration: bedrock.BedrockRerankingModelConfiguration{
				NumberOfResults: &topN,
				ModelConfiguration: bedrock.BedrockRerankModelConfiguration{
					ModelARN: "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
				},
			},
		},
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := route.RequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.RerankRequest)
	// Converters leave Provider empty; resolution happens later in the
	// modelcatalogresolver PreRequestHook.
	assert.Equal(t, schemas.ModelProvider(""), bifrostReq.RerankRequest.Provider)
	assert.Equal(t, "capital of france", bifrostReq.RerankRequest.Query)
	require.Len(t, bifrostReq.RerankRequest.Documents, 1)
	assert.Equal(t, "Paris is capital of France", bifrostReq.RerankRequest.Documents[0].Text)
	require.NotNil(t, bifrostReq.RerankRequest.Params)
	require.NotNil(t, bifrostReq.RerankRequest.Params.TopN)
	assert.Equal(t, 1, *bifrostReq.RerankRequest.Params.TopN)
}

func Test_createBedrockRouteConfigsIncludesRerankForCompositePrefixes(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	prefixes := []string{"/litellm", "/langchain", "/pydanticai"}

	for _, prefix := range prefixes {
		routes := CreateBedrockRouteConfigs(prefix, handlerStore)
		found := false
		for _, route := range routes {
			if route.Path == prefix+"/rerank" && route.Method == "POST" {
				found = true
				break
			}
		}
		assert.Truef(t, found, "expected rerank route for prefix %s", prefix)
	}
}

func Test_createBedrockBatchRouteConfigs(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	routes := createBedrockBatchRouteConfigs("/bedrock", handlerStore)

	assert.Len(t, routes, 4, "should have 4 batch routes")

	expectedRoutes := []struct {
		path   string
		method string
	}{
		{"/bedrock/model-invocation-job", "POST"},
		{"/bedrock/model-invocation-jobs", "GET"},
		{"/bedrock/model-invocation-job/{job_arn}", "GET"},
		{"/bedrock/model-invocation-job/{job_arn}/stop", "POST"},
	}

	for i, expected := range expectedRoutes {
		assert.Equal(t, expected.path, routes[i].Path, "batch route %d path mismatch", i)
		assert.Equal(t, expected.method, routes[i].Method, "batch route %d method mismatch", i)
		assert.Equal(t, RouteConfigTypeBedrock, routes[i].Type, "batch route %d type mismatch", i)
		assert.NotNil(t, routes[i].GetRequestTypeInstance, "batch route %d GetRequestTypeInstance should not be nil", i)
		assert.NotNil(t, routes[i].BatchRequestConverter, "batch route %d BatchCreateRequestConverter should not be nil", i)
		assert.NotNil(t, routes[i].ErrorConverter, "batch route %d ErrorConverter should not be nil", i)
		assert.NotNil(t, routes[i].PreCallback, "batch route %d PreCallback should not be nil", i)
	}
}

func Test_createBedrockFilesRouteConfigs(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	routes := createBedrockFilesRouteConfigs("/bedrock/files", handlerStore)

	assert.Len(t, routes, 5, "should have 5 file routes")

	expectedRoutes := []struct {
		path   string
		method string
	}{
		{"/bedrock/files/{bucket}/{key:*}", "PUT"},
		{"/bedrock/files/{bucket}/{key:*}", "GET"},
		{"/bedrock/files/{bucket}/{key:*}", "HEAD"},
		{"/bedrock/files/{bucket}/{key:*}", "DELETE"},
		{"/bedrock/files/{bucket}", "GET"},
	}

	for i, expected := range expectedRoutes {
		assert.Equal(t, expected.path, routes[i].Path, "file route %d path mismatch", i)
		assert.Equal(t, expected.method, routes[i].Method, "file route %d method mismatch", i)
		assert.Equal(t, RouteConfigTypeBedrock, routes[i].Type, "file route %d type mismatch", i)
		assert.NotNil(t, routes[i].GetRequestTypeInstance, "file route %d GetRequestTypeInstance should not be nil", i)
		assert.NotNil(t, routes[i].ErrorConverter, "file route %d ErrorConverter should not be nil", i)
	}
}

func Test_parseS3PutObjectRequest(t *testing.T) {
	tests := []struct {
		name         string
		bucket       string
		key          string
		body         []byte
		wantErr      bool
		wantBucket   string
		wantKey      string
		wantFilename string
	}{
		{
			name:         "valid request",
			bucket:       "my-bucket",
			key:          "folder/file.jsonl",
			body:         []byte(`{"test": "data"}`),
			wantErr:      false,
			wantBucket:   "my-bucket",
			wantKey:      "folder/file.jsonl",
			wantFilename: "file.jsonl",
		},
		{
			name:         "simple key without folder",
			bucket:       "bucket",
			key:          "file.txt",
			body:         []byte("content"),
			wantErr:      false,
			wantBucket:   "bucket",
			wantKey:      "file.txt",
			wantFilename: "file.txt",
		},
		{
			name:    "missing bucket",
			bucket:  "",
			key:     "file.txt",
			body:    []byte("content"),
			wantErr: true,
		},
		{
			name:    "missing key",
			bucket:  "bucket",
			key:     "",
			body:    []byte("content"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetBody(tt.body)

			if tt.bucket != "" {
				ctx.SetUserValue("bucket", tt.bucket)
			}
			if tt.key != "" {
				ctx.SetUserValue("key", tt.key)
			}

			req := &bedrock.BedrockFileUploadRequest{}
			err := parseS3PutObjectRequest(ctx, req)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantBucket, req.Bucket)
			assert.Equal(t, tt.wantKey, req.Key)
			assert.Equal(t, tt.wantFilename, req.Filename)
			assert.Equal(t, "batch", req.Purpose)
			assert.Equal(t, tt.body, req.Body)
		})
	}
}

func Test_parseS3PutObjectRequest_invalidType(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("bucket", "bucket")
	ctx.SetUserValue("key", "key")

	// Pass wrong type
	err := parseS3PutObjectRequest(ctx, "invalid type")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid request type")
}

func Test_s3PutObjectPostCallback(t *testing.T) {
	tests := []struct {
		name       string
		response   interface{}
		wantStatus int
		wantETag   string
	}{
		{
			name: "valid response with ID",
			response: &schemas.BifrostFileUploadResponse{
				ID: "file-123",
			},
			wantStatus: 200,
			wantETag:   "\"file-123\"",
		},
		{
			name:       "nil response",
			response:   nil,
			wantStatus: 200,
			wantETag:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			err := s3PutObjectPostCallback(ctx, nil, tt.response)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantStatus, ctx.Response.StatusCode())
			assert.Equal(t, "application/xml", string(ctx.Response.Header.ContentType()))
			assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))

			if tt.wantETag != "" {
				assert.Equal(t, tt.wantETag, string(ctx.Response.Header.Peek("ETag")))
			}
		})
	}
}

func Test_s3GetObjectPostCallback(t *testing.T) {
	tests := []struct {
		name            string
		response        interface{}
		wantContentType string
		wantLength      string
		wantETag        string
	}{
		{
			name: "valid response",
			response: &schemas.BifrostFileContentResponse{
				Content:     []byte("test content"),
				ContentType: "application/json",
				FileID:      "file-456",
			},
			wantContentType: "application/json",
			wantLength:      "12",
			wantETag:        "\"file-456\"",
		},
		{
			name:            "nil response",
			response:        nil,
			wantContentType: "",
			wantLength:      "",
			wantETag:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			err := s3GetObjectPostCallback(ctx, nil, tt.response)

			assert.NoError(t, err)

			if tt.wantContentType != "" {
				assert.Equal(t, tt.wantContentType, string(ctx.Response.Header.Peek("Content-Type")))
				assert.Equal(t, tt.wantLength, string(ctx.Response.Header.Peek("Content-Length")))
				assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))
			}

			if tt.wantETag != "" {
				assert.Equal(t, tt.wantETag, string(ctx.Response.Header.Peek("ETag")))
			}
		})
	}
}

func Test_s3HeadObjectPostCallback(t *testing.T) {
	tests := []struct {
		name       string
		response   interface{}
		wantStatus int
		wantLength string
		wantETag   string
	}{
		{
			name: "valid response",
			response: &schemas.BifrostFileRetrieveResponse{
				ID:    "file-789",
				Bytes: 1024,
			},
			wantStatus: 200,
			wantLength: "1024",
			wantETag:   "\"file-789\"",
		},
		{
			name:       "nil response",
			response:   nil,
			wantStatus: 200,
			wantLength: "",
			wantETag:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			err := s3HeadObjectPostCallback(ctx, nil, tt.response)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantStatus, ctx.Response.StatusCode())

			if tt.wantLength != "" {
				assert.Equal(t, "application/octet-stream", string(ctx.Response.Header.Peek("Content-Type")))
				assert.Equal(t, tt.wantLength, string(ctx.Response.Header.Peek("Content-Length")))
				assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))
				assert.Equal(t, tt.wantETag, string(ctx.Response.Header.Peek("ETag")))
			}
		})
	}
}

func Test_s3DeleteObjectPostCallback(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	err := s3DeleteObjectPostCallback(ctx, nil, nil)

	assert.NoError(t, err)
	assert.Equal(t, 204, ctx.Response.StatusCode())
	assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))
}

func Test_s3ListObjectsV2PostCallback(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	err := s3ListObjectsV2PostCallback(ctx, nil, nil)

	assert.NoError(t, err)
	assert.Equal(t, "application/xml", string(ctx.Response.Header.ContentType()))
	assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))
}

func Test_extractBedrockBatchListQueryParams(t *testing.T) {
	handlerStore := &mockHandlerStore{}

	tests := []struct {
		name           string
		queryParams    map[string]string
		wantMaxResults int
		wantNextToken  string
		wantStatus     string
		wantName       string
	}{
		{
			name: "all params",
			queryParams: map[string]string{
				"maxResults":   "50",
				"nextToken":    "token123",
				"statusEquals": "InProgress",
				"nameContains": "test-job",
			},
			wantMaxResults: 50,
			wantNextToken:  "token123",
			wantStatus:     "InProgress",
			wantName:       "test-job",
		},
		{
			name:           "no params",
			queryParams:    map[string]string{},
			wantMaxResults: 0,
			wantNextToken:  "",
			wantStatus:     "",
			wantName:       "",
		},
		{
			name: "invalid maxResults",
			queryParams: map[string]string{
				"maxResults": "invalid",
			},
			wantMaxResults: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			for k, v := range tt.queryParams {
				ctx.QueryArgs().Add(k, v)
			}

			req := &bedrock.BedrockBatchListRequest{}
			callback := extractBedrockBatchListQueryParams(handlerStore)

			bifrostCtx := createTestBifrostContext()
			err := callback(ctx, bifrostCtx, req)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantMaxResults, req.MaxResults)
			assert.Equal(t, tt.wantStatus, req.StatusEquals)
			assert.Equal(t, tt.wantName, req.NameContains)

			if tt.wantNextToken != "" {
				assert.NotNil(t, req.NextToken)
				assert.Equal(t, tt.wantNextToken, *req.NextToken)
			}
		})
	}
}

func Test_extractBedrockJobArnFromPath(t *testing.T) {
	handlerStore := &mockHandlerStore{}

	tests := []struct {
		name        string
		jobArn      interface{}
		provider    schemas.ModelProvider
		wantErr     bool
		wantJobArn  string
		errContains string
	}{
		{
			name:       "valid job ARN for Bedrock",
			jobArn:     "arn:aws:bedrock:us-east-1:123456789012:batch:job-123",
			provider:   schemas.Bedrock,
			wantErr:    false,
			wantJobArn: "arn:aws:bedrock:us-east-1:123456789012:batch:job-123",
		},
		{
			name:       "URL encoded job ARN",
			jobArn:     "arn%3Aaws%3Abedrock%3Aus-east-1%3A123456789012%3Abatch%3Ajob-123",
			provider:   schemas.Bedrock,
			wantErr:    false,
			wantJobArn: "arn:aws:bedrock:us-east-1:123456789012:batch:job-123",
		},
		{
			name:       "non-Bedrock provider strips ARN prefix",
			jobArn:     "arn:aws:bedrock:us-east-1:444444444444:batch:job-456",
			provider:   schemas.OpenAI,
			wantErr:    false,
			wantJobArn: "job-456",
		},
		{
			name:        "missing job_arn",
			jobArn:      nil,
			provider:    schemas.Bedrock,
			wantErr:     true,
			errContains: "job_arn is required",
		},
		{
			name:        "empty job_arn",
			jobArn:      "",
			provider:    schemas.Bedrock,
			wantErr:     true,
			errContains: "job_arn must be a non-empty string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tt.jobArn != nil {
				ctx.SetUserValue("job_arn", tt.jobArn)
			}

			req := &bedrock.BedrockBatchRetrieveRequest{}
			callback := extractBedrockJobArnFromPath(handlerStore)

			bifrostCtx := createTestBifrostContextWithProvider(tt.provider)
			err := callback(ctx, bifrostCtx, req)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantJobArn, req.JobIdentifier)
		})
	}
}

func Test_extractS3ListObjectsV2Params(t *testing.T) {
	handlerStore := &mockHandlerStore{}

	tests := []struct {
		name                  string
		bucket                string
		queryParams           map[string]string
		wantErr               bool
		wantBucket            string
		wantPrefix            string
		wantMaxKeys           int
		wantContinuationToken string
	}{
		{
			name:   "all params",
			bucket: "my-bucket",
			queryParams: map[string]string{
				"prefix":             "folder/",
				"max-keys":           "100",
				"continuation-token": "token-abc",
			},
			wantErr:               false,
			wantBucket:            "my-bucket",
			wantPrefix:            "folder/",
			wantMaxKeys:           100,
			wantContinuationToken: "token-abc",
		},
		{
			name:        "bucket only",
			bucket:      "simple-bucket",
			queryParams: map[string]string{},
			wantErr:     false,
			wantBucket:  "simple-bucket",
			wantPrefix:  "",
			wantMaxKeys: 1000,
		},
		{
			name:    "missing bucket",
			bucket:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tt.bucket != "" {
				ctx.SetUserValue("bucket", tt.bucket)
			}
			for k, v := range tt.queryParams {
				ctx.QueryArgs().Add(k, v)
			}

			req := &bedrock.BedrockFileListRequest{}
			callback := extractS3ListObjectsV2Params(handlerStore)

			bifrostCtx := createTestBifrostContext()
			err := callback(ctx, bifrostCtx, req)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantBucket, req.Bucket)
			assert.Equal(t, tt.wantPrefix, req.Prefix)
			assert.Equal(t, tt.wantMaxKeys, req.MaxKeys)
			assert.Equal(t, tt.wantContinuationToken, req.ContinuationToken)

			// Verify context values
			assert.Equal(t, tt.wantBucket, bifrostCtx.Value(s3ContextKeyBucket))
			assert.Equal(t, tt.wantPrefix, bifrostCtx.Value(s3ContextKeyPrefix))
			assert.Equal(t, tt.wantMaxKeys, bifrostCtx.Value(s3ContextKeyMaxKeys))
		})
	}
}

func Test_extractS3BucketKeyFromPath(t *testing.T) {
	handlerStore := &mockHandlerStore{}

	tests := []struct {
		name       string
		bucket     string
		key        string
		fileID     string
		opType     string
		wantErr    bool
		wantBucket string
		wantKey    string
		wantS3URI  string
	}{
		{
			name:       "content operation",
			bucket:     "my-bucket",
			key:        "path/to/file.txt",
			fileID:     "file-123",
			opType:     "content",
			wantErr:    false,
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
			wantS3URI:  "s3://my-bucket/path/to/file.txt",
		},
		{
			name:    "missing bucket",
			bucket:  "",
			key:     "file.txt",
			opType:  "content",
			wantErr: true,
		},
		{
			name:    "missing key",
			bucket:  "bucket",
			key:     "",
			opType:  "content",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tt.bucket != "" {
				ctx.SetUserValue("bucket", tt.bucket)
			}
			if tt.key != "" {
				ctx.SetUserValue("key", tt.key)
			}
			if tt.fileID != "" {
				ctx.Request.Header.Set("If-Match", tt.fileID)
			}

			callback := extractS3BucketKeyFromPath(handlerStore, tt.opType)
			bifrostCtx := createTestBifrostContext()

			var req interface{}
			switch tt.opType {
			case "content":
				req = &bedrock.BedrockFileContentRequest{}
			case "retrieve":
				req = &bedrock.BedrockFileRetrieveRequest{}
			case "delete":
				req = &bedrock.BedrockFileDeleteRequest{}
			}

			err := callback(ctx, bifrostCtx, req)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			switch r := req.(type) {
			case *bedrock.BedrockFileContentRequest:
				assert.Equal(t, tt.wantBucket, r.Bucket)
				assert.Equal(t, tt.wantKey, r.Prefix)
				assert.Equal(t, tt.wantS3URI, r.S3Uri)
				assert.Equal(t, tt.fileID, r.ETag)
			}
		})
	}
}

// Helper functions for creating test contexts

func createTestBifrostContext() *schemas.BifrostContext {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
	return bifrostCtx
}

func createTestBifrostContextWithProvider(provider schemas.ModelProvider) *schemas.BifrostContext {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(bifrostContextKeyProvider, provider)
	return bifrostCtx
}
