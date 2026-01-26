package integrations

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

// mockHandlerStore implements lib.HandlerStore for testing
type mockHandlerStore struct {
	allowDirectKeys    bool
	headerFilterConfig *configstoreTables.GlobalHeaderFilterConfig
	availableProviders []schemas.ModelProvider
}

func (m *mockHandlerStore) ShouldAllowDirectKeys() bool {
	return m.allowDirectKeys
}

func (m *mockHandlerStore) GetHeaderFilterConfig() *configstoreTables.GlobalHeaderFilterConfig {
	return m.headerFilterConfig
}

func (m *mockHandlerStore) GetAvailableProviders() []schemas.ModelProvider {
	return m.availableProviders
}

func (m *mockHandlerStore) GetStreamChunkInterceptor() lib.StreamChunkInterceptor {
	return nil
}

// Ensure mockHandlerStore implements lib.HandlerStore
var _ lib.HandlerStore = (*mockHandlerStore)(nil)

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
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
	routes := CreateBedrockRouteConfigs("/bedrock", handlerStore)

	assert.Len(t, routes, 4, "should have 4 bedrock routes")

	expectedRoutes := []struct {
		path   string
		method string
	}{
		{"/bedrock/model/{modelId}/converse", "POST"},
		{"/bedrock/model/{modelId}/converse-stream", "POST"},
		{"/bedrock/model/{modelId}/invoke-with-response-stream", "POST"},
		{"/bedrock/model/{modelId}/invoke", "POST"},
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
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
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
	reqInstance := route.GetRequestTypeInstance()
	_, ok := reqInstance.(*bedrock.BedrockConverseRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockConverseRequest")
}

func Test_createBedrockConverseStreamRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
	route := createBedrockConverseStreamRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/converse-stream", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.StreamConfig)
	assert.NotNil(t, route.StreamConfig.ResponsesStreamResponseConverter)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance()
	_, ok := reqInstance.(*bedrock.BedrockConverseRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockConverseRequest")
}

func Test_createBedrockInvokeRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
	route := createBedrockInvokeRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/invoke", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.TextResponseConverter)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance()
	_, ok := reqInstance.(*bedrock.BedrockTextCompletionRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockTextCompletionRequest")
}

func Test_createBedrockInvokeWithResponseStreamRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
	route := createBedrockInvokeWithResponseStreamRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/invoke-with-response-stream", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.StreamConfig)
	assert.NotNil(t, route.StreamConfig.TextStreamResponseConverter)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance()
	_, ok := reqInstance.(*bedrock.BedrockTextCompletionRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockTextCompletionRequest")
}

func Test_createBedrockBatchRouteConfigs(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
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
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
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
	handlerStore := &mockHandlerStore{allowDirectKeys: false}

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
	handlerStore := &mockHandlerStore{allowDirectKeys: false}

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
	handlerStore := &mockHandlerStore{allowDirectKeys: false}

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
	handlerStore := &mockHandlerStore{allowDirectKeys: false}

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
