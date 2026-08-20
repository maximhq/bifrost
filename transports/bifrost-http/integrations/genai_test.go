package integrations

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/vertex"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type fileUploadTestHandlerStore struct {
	*mockHandlerStore
	kvStore *kvstore.Store
}

func (m *fileUploadTestHandlerStore) GetKVStore() *kvstore.Store {
	return m.kvStore
}
func TestCreateGenAIRerankRouteConfig(t *testing.T) {
	route := createGenAIRerankRouteConfig("/genai")

	assert.Equal(t, "/genai/v1/rank", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeGenAI, route.Type)
	assert.NotNil(t, route.GetHTTPRequestType)
	assert.Equal(t, schemas.RerankRequest, route.GetHTTPRequestType(nil))
	assert.NotNil(t, route.GetRequestTypeInstance)
	assert.NotNil(t, route.RequestConverter)
	assert.NotNil(t, route.RerankResponseConverter)
	assert.NotNil(t, route.ErrorConverter)
	// The route resolves x-model-provider so it can be served cross-provider.
	assert.NotNil(t, route.PreCallback)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*vertex.VertexRankRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *vertex.VertexRankRequest")
}

func TestCreateGenAIRouteConfigsIncludesRerank(t *testing.T) {
	routes := CreateGenAIRouteConfigs("/genai")

	found := false
	for _, route := range routes {
		if route.Path == "/genai/v1/rank" && route.Method == "POST" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected rerank route in genai route configs")
}

func findGenAIRouteForTest(t *testing.T, routes []RouteConfig, path, method string) RouteConfig {
	t.Helper()
	for _, route := range routes {
		if route.Path == path && route.Method == method {
			return route
		}
	}
	t.Fatalf("route %s %s not found", method, path)
	return RouteConfig{}
}

func TestExtractAndSetModelAndRequestTypePreservesRawBodyForGenerateContent(t *testing.T) {
	rawBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"responseJsonSchema":{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"string"}}}}}`)
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("model", "gemini-2.5-flash:generateContent")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("x-model-provider", "gemini")
	ctx.Request.SetBody(rawBody)

	req := &gemini.GeminiGenerationRequest{}
	require.NoError(t, sonic.Unmarshal(rawBody, req))
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	err := extractAndSetModelAndRequestType(ctx, bifrostCtx, req)
	require.NoError(t, err)

	assert.Equal(t, true, bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody))
	assert.Equal(t, rawBody, bifrostCtx.Value(genAIRawRequestBodyContextKey))
}

func TestExtractAndSetModelAndRequestTypeNoRawPassthroughWithoutExplicitGemini(t *testing.T) {
	// A bare model with no gemini/ prefix and no x-model-provider header may
	// resolve to Vertex (or another provider) downstream, so the raw-body
	// passthrough must not engage on the silent Gemini default.
	rawBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("model", "gemini-2.5-flash:generateContent")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetBody(rawBody)

	req := &gemini.GeminiGenerationRequest{}
	require.NoError(t, sonic.Unmarshal(rawBody, req))
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	err := extractAndSetModelAndRequestType(ctx, bifrostCtx, req)
	require.NoError(t, err)

	assert.Nil(t, bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody))
	assert.Nil(t, bifrostCtx.Value(genAIRawRequestBodyContextKey))
}

func TestExtractAndSetModelAndRequestTypeDoesNotRawPassthroughEmbedding(t *testing.T) {
	rawBody := []byte(`{"content":{"parts":[{"text":"hello"}]}}`)
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("model", "gemini-embedding-001:embedContent")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetBody(rawBody)

	req := &gemini.GeminiEmbeddingRequest{}
	require.NoError(t, sonic.Unmarshal(rawBody, req))
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	err := extractAndSetModelAndRequestType(ctx, bifrostCtx, req)
	require.NoError(t, err)

	assert.Nil(t, bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody))
	assert.Nil(t, bifrostCtx.Value(genAIRawRequestBodyContextKey))
}

func TestGenAIBatchCreateConverterCarriesRawBody(t *testing.T) {
	rawBody := []byte(`{"batch":{"inputConfig":{"requests":{"requests":[{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"temperature":0.2}},"metadata":{"key":"req-1"}}]}}}}`)
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("model", "gemini-2.5-flash:batchGenerateContent")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.Set("x-model-provider", "gemini")
	ctx.Request.SetBody(rawBody)

	req := &gemini.GeminiBatchCreateRequest{}
	require.NoError(t, sonic.Unmarshal(rawBody, req))
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, extractAndSetModelAndRequestType(ctx, bifrostCtx, req))

	route := findGenAIRouteForTest(t, CreateGenAIRouteConfigs("/genai"), "/genai/v1beta/models/{model:*}", "POST")
	batchReq, err := route.BatchRequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, batchReq)
	require.NotNil(t, batchReq.CreateRequest)

	assert.Equal(t, rawBody, batchReq.CreateRequest.RawRequestBody)
	assert.Equal(t, true, bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody))
}

func TestGenAICachedContentCreateParserRejectsNonStringScalars(t *testing.T) {
	rawBody := []byte(`{"model":123,"ttl":3600}`)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetBody(rawBody)

	route := findGenAIRouteForTest(t, CreateGenAICachedContentRouteConfigs("/genai", nil), "/genai/v1beta/cachedContents", "POST")
	req := route.GetRequestTypeInstance(context.Background())

	err := route.RequestParser(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model must be a string")
}

func TestGenAICachedContentCreateParserCarriesRawBody(t *testing.T) {
	rawBody := []byte(`{"model":"models/gemini-2.5-flash","contents":[{"role":"user","parts":[{"text":"alpha"}]}],"tools":[{"functionDeclarations":[{"name":"lookup","parametersJsonSchema":{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"string"}}}}]}],"ttl":"3600s"}`)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetBody(rawBody)

	route := findGenAIRouteForTest(t, CreateGenAICachedContentRouteConfigs("/genai", nil), "/genai/v1beta/cachedContents", "POST")
	req := route.GetRequestTypeInstance(context.Background())
	require.NoError(t, route.RequestParser(ctx, req))

	createReq := req.(*schemas.BifrostCachedContentCreateRequest)
	assert.Equal(t, rawBody, createReq.RawRequestBody)
	assert.Equal(t, "gemini-2.5-flash", createReq.Model)
	require.NotNil(t, createReq.TTL)
	assert.Equal(t, "3600s", *createReq.TTL)

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	converted, err := route.CachedContentRequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, converted)
	assert.Equal(t, true, bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody))
}

func TestCreateGenAIRouteConfigsIncludesRerankForCompositePrefixes(t *testing.T) {
	prefixes := []string{"/litellm", "/langchain", "/pydanticai"}

	for _, prefix := range prefixes {
		routes := CreateGenAIRouteConfigs(prefix)
		found := false
		for _, route := range routes {
			if route.Path == prefix+"/v1/rank" && route.Method == "POST" {
				found = true
				break
			}
		}
		assert.Truef(t, found, "expected rerank route for prefix %s", prefix)
	}
}

func TestGenAIRerankRequestConverter(t *testing.T) {
	route := createGenAIRerankRouteConfig("/genai")
	require.NotNil(t, route.RequestConverter)

	model := "semantic-ranker-default@latest"
	topN := 2
	content1 := "Paris is capital of France"
	content2 := "Berlin is capital of Germany"
	req := &vertex.VertexRankRequest{
		Model: &model,
		Query: "capital of france",
		Records: []vertex.VertexRankRecord{
			{ID: "rec-1", Content: &content1},
			{ID: "rec-2", Content: &content2},
		},
		TopN: &topN,
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := route.RequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.RerankRequest)
	// Provider resolution is deferred to the route header and the modelcatalogresolver plugin.
	assert.Equal(t, schemas.ModelProvider(""), bifrostReq.RerankRequest.Provider)
	assert.Equal(t, "semantic-ranker-default@latest", bifrostReq.RerankRequest.Model)
	assert.Equal(t, "capital of france", bifrostReq.RerankRequest.Query)
	require.Len(t, bifrostReq.RerankRequest.Documents, 2)
	assert.Equal(t, "Paris is capital of France", bifrostReq.RerankRequest.Documents[0].Text)
	assert.Equal(t, "Berlin is capital of Germany", bifrostReq.RerankRequest.Documents[1].Text)
	require.NotNil(t, bifrostReq.RerankRequest.Params)
	require.NotNil(t, bifrostReq.RerankRequest.Params.TopN)
	assert.Equal(t, 2, *bifrostReq.RerankRequest.Params.TopN)
}

func TestGenAIRerankResponseConverterRestoresCallerRecordIDs(t *testing.T) {
	route := createGenAIRerankRouteConfig("/genai")
	require.NotNil(t, route.RerankResponseConverter)

	resp := &schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{
			{Index: 1, RelevanceScore: 0.88, Document: &schemas.RerankDocument{
				ID: new("doc-paris"), Text: "Paris is capital of France", Meta: map[string]interface{}{"title": "Paris"},
			}},
			{Index: 0, RelevanceScore: 0.12, Document: &schemas.RerankDocument{
				ID: new("doc-berlin"), Text: "Berlin is capital of Germany",
			}},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Vertex,
			// Raw carries synthetic idx:N record IDs, so it must never be returned.
			RawResponse: map[string]interface{}{"records": []interface{}{map[string]interface{}{"id": "idx:1"}}},
		},
	}

	converted, err := route.RerankResponseConverter(nil, resp)
	require.NoError(t, err)

	rankResp, ok := converted.(*vertex.VertexRankResponse)
	require.True(t, ok, "converter should emit *vertex.VertexRankResponse")
	require.Len(t, rankResp.Records, 2)
	assert.Equal(t, "doc-paris", rankResp.Records[0].ID)
	assert.InDelta(t, 0.88, rankResp.Records[0].Score, 1e-9)
	require.NotNil(t, rankResp.Records[0].Content)
	assert.Equal(t, "Paris is capital of France", *rankResp.Records[0].Content)
	require.NotNil(t, rankResp.Records[0].Title)
	assert.Equal(t, "Paris", *rankResp.Records[0].Title)
	assert.Equal(t, "doc-berlin", rankResp.Records[1].ID)
	assert.Nil(t, rankResp.Records[1].Title)
}

func TestGenAIRerankRequestConverterRequestsDocuments(t *testing.T) {
	route := createGenAIRerankRouteConfig("/genai")
	require.NotNil(t, route.RequestConverter)

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := route.RequestConverter(bifrostCtx, &vertex.VertexRankRequest{
		Query:   "capital of france",
		Records: []vertex.VertexRankRecord{{ID: "doc-paris", Content: new("Paris is capital of France")}},
	})
	require.NoError(t, err)
	require.NotNil(t, bifrostReq.RerankRequest)
	require.NotNil(t, bifrostReq.RerankRequest.Params)
	// Ranked records are keyed by caller record ID, which only the document carries.
	require.NotNil(t, bifrostReq.RerankRequest.Params.ReturnDocuments)
	assert.True(t, *bifrostReq.RerankRequest.Params.ReturnDocuments)
}

func TestCreateGenAIRouteConfigsIncludesModelMetadataRoute(t *testing.T) {
	routes := CreateGenAIRouteConfigs("/genai")

	found := false
	for _, route := range routes {
		if route.Path == "/genai/v1beta/models/{model}" && route.Method == "GET" {
			found = true
			assert.Equal(t, schemas.ListModelsRequest, route.GetHTTPRequestType(nil))
			require.NotNil(t, route.PreCallback)
			require.NotNil(t, route.ListModelsResponseConverter)
			break
		}
	}

	assert.True(t, found, "expected model metadata route in genai route configs")
}

func TestExtractGeminiModelMetadataParams(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("model", "models/gemini-3-pro-preview")

	listReq := &schemas.BifrostListModelsRequest{}
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	err := extractGeminiModelMetadataParams(ctx, bifrostCtx, listReq)
	require.NoError(t, err)
	assert.Equal(t, schemas.Gemini, listReq.Provider)
	assert.Equal(t, "/models/gemini-3-pro-preview", bifrostCtx.Value(schemas.BifrostContextKeyURLPath))
	assert.Equal(t, "gemini-3-pro-preview", bifrostCtx.Value(requestedGeminiModelMetadataContextKey))
}

func TestConvertGeminiModelMetadataResponse(t *testing.T) {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(requestedGeminiModelMetadataContextKey, "gemini-2.5-pro")

	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{{ID: "gemini/gemini-2.5-pro", Name: schemas.Ptr("Gemini 2.5 Pro")}},
	}

	converted, err := convertGeminiModelMetadataResponse(bifrostCtx, resp)
	require.NoError(t, err)

	model, ok := converted.(gemini.GeminiModel)
	require.True(t, ok, "expected gemini.GeminiModel")
	assert.Equal(t, "models/gemini-2.5-pro", model.Name)
	assert.Equal(t, "Gemini 2.5 Pro", model.DisplayName)
}

func TestConvertGeminiModelMetadataResponse_MatchesRequestedModelNotFirst(t *testing.T) {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(requestedGeminiModelMetadataContextKey, "gemini-3-pro-preview")

	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "gemini/gemini-1.5-pro", Name: schemas.Ptr("Gemini 1.5 Pro")},
			{ID: "gemini/gemini-3-pro-preview", Name: schemas.Ptr("Gemini 3 Pro Preview")},
		},
	}

	converted, err := convertGeminiModelMetadataResponse(bifrostCtx, resp)
	require.NoError(t, err)

	model, ok := converted.(gemini.GeminiModel)
	require.True(t, ok, "expected gemini.GeminiModel")
	assert.Equal(t, "models/gemini-3-pro-preview", model.Name)
	assert.Equal(t, "Gemini 3 Pro Preview", model.DisplayName)
}

func TestConvertGeminiModelMetadataResponse_EmptyReturnsMinimalModel(t *testing.T) {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(requestedGeminiModelMetadataContextKey, "gemini-3-pro-preview")

	converted, err := convertGeminiModelMetadataResponse(bifrostCtx, &schemas.BifrostListModelsResponse{Data: []schemas.Model{}})
	require.NoError(t, err)
	model, ok := converted.(gemini.GeminiModel)
	require.True(t, ok, "expected gemini.GeminiModel")
	assert.Equal(t, "models/gemini-3-pro-preview", model.Name)
}

// Test that FileRequestConverter retains the resumable upload session for retries.
func TestFileRequestConverter_RetainsResumableUploadSession(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	require.NotEmpty(t, routes)

	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	uploadID := "test-upload-id"
	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "test.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	// Verify the session exists before FileRequestConverter.
	storedBefore, err := kvStore.Get(uploadID)
	require.NoError(t, err)
	require.NotNil(t, storedBefore)

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID: uploadID,
		FileData: []byte("test file data"),
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	fileReq, err := route.FileRequestConverter(bifrostCtx, req)

	require.NoError(t, err)
	require.NotNil(t, fileReq)

	assert.Equal(t, schemas.FileUploadRequest, fileReq.Type)
	require.NotNil(t, fileReq.UploadRequest)

	assert.Equal(t, []byte("test file data"), fileReq.UploadRequest.File)
	assert.Equal(t, "test.pdf", fileReq.UploadRequest.Filename)
	assert.Equal(t, schemas.Gemini, fileReq.UploadRequest.Provider)

	// FileRequestConverter must NOT delete the resumable upload session.
	storedAfter, err := kvStore.Get(uploadID)
	require.NoError(t, err)
	require.NotNil(t, storedAfter)

	stored, ok := storedAfter.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	assert.Equal(t, session.DisplayName, stored.DisplayName)
	assert.Equal(t, session.MimeType, stored.MimeType)
	assert.Equal(t, session.Provider, stored.Provider)
}

// Test that the resumable upload session can be reused for retries.
func TestFileRequestConverter_ReusesResumableUploadSessionForRetry(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	require.NotEmpty(t, routes)

	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	uploadID := "retry-upload-id"
	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "retry-test.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	firstReq := &gemini.GeminiFileUploadHandlerReq{
		UploadID: uploadID,
		FileData: []byte("first upload chunk"),
	}

	firstFileReq, err := route.FileRequestConverter(bifrostCtx, firstReq)

	require.NoError(t, err)
	require.NotNil(t, firstFileReq)
	require.NotNil(t, firstFileReq.UploadRequest)

	assert.Equal(t, schemas.FileUploadRequest, firstFileReq.Type)
	assert.Equal(t, []byte("first upload chunk"), firstFileReq.UploadRequest.File)
	assert.Equal(t, "retry-test.pdf", firstFileReq.UploadRequest.Filename)
	assert.Equal(t, schemas.Gemini, firstFileReq.UploadRequest.Provider)

	// Retry using the same upload session.
	secondReq := &gemini.GeminiFileUploadHandlerReq{
		UploadID: uploadID,
		FileData: []byte("retry upload chunk"),
	}

	secondFileReq, err := route.FileRequestConverter(bifrostCtx, secondReq)
	require.NoError(t, err)
	require.NotNil(t, secondFileReq)
	require.NotNil(t, secondFileReq.UploadRequest)

	assert.Equal(t, schemas.FileUploadRequest, secondFileReq.Type)
	assert.Equal(t, []byte("retry upload chunk"), secondFileReq.UploadRequest.File)
	assert.Equal(t, "retry-test.pdf", secondFileReq.UploadRequest.Filename)
	assert.Equal(t, schemas.Gemini, secondFileReq.UploadRequest.Provider)

	// Verify the session is still available after the retry.
	storedSession, err := kvStore.Get(uploadID)
	require.NoError(t, err)
	require.NotNil(t, storedSession)

	stored, ok := storedSession.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	assert.Equal(t, session.DisplayName, stored.DisplayName)
	assert.Equal(t, session.MimeType, stored.MimeType)
	assert.Equal(t, session.Provider, stored.Provider)
}

// Test that the upload session is deleted after successful finalization.
func TestFileUploadPostCallback_DeletesSessionAfterSuccessfulFinalization(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	require.NotEmpty(t, routes)

	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")
	uploadID := "finalize-upload-id"
	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "finalize-test.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	// Verify the session exists before finalization.
	storedBefore, err := kvStore.Get(uploadID)
	require.NoError(t, err)
	require.NotNil(t, storedBefore)

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID: uploadID,
		FileData: []byte("test file data"),
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	err = route.PostCallback(ctx, req, nil)
	require.NoError(t, err)

	assert.Equal(t, "final", string(ctx.Response.Header.Peek("X-Goog-Upload-Status")))

	// Verify the session was deleted after finalization.
	_, err = kvStore.Get(uploadID)
	assert.ErrorIs(t, err, kvstore.ErrNotFound)
}

// Test that PostCallback preserves session for upload-only commands and deletes only on finalize.
func TestFileUploadPostCallback_PreservesSessionForUploadOnly(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	require.NotEmpty(t, routes)

	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")
	uploadID := "upload-retry-id"
	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "upload-retry-test.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID: uploadID,
		FileData: []byte("first chunk"),
	}

	// First request: "upload" command (no finalize) should preserve session
	ctx1 := &fasthttp.RequestCtx{}
	ctx1.Request.Header.Set("X-Goog-Upload-Command", "upload")
	err = route.PostCallback(ctx1, req, nil)
	require.NoError(t, err)

	// Response should indicate upload in progress, not final
	assert.Equal(t, "active", string(ctx1.Response.Header.Peek("X-Goog-Upload-Status")))

	// Session should still exist for retries
	stored, err := kvStore.Get(uploadID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	storedSession, ok := stored.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)
	assert.Equal(t, "upload-retry-test.pdf", storedSession.DisplayName)

	// Second request: "upload, finalize" command should finalize and delete session
	ctx2 := &fasthttp.RequestCtx{}
	ctx2.Request.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	err = route.PostCallback(ctx2, req, nil)
	require.NoError(t, err)

	// Response should indicate finalization complete
	assert.Equal(t, "final", string(ctx2.Response.Header.Peek("X-Goog-Upload-Status")))

	// Session should now be deleted
	_, err = kvStore.Get(uploadID)
	assert.ErrorIs(t, err, kvstore.ErrNotFound)
}

// Test that the request is rejected without an upload session.
func TestFileRequestConverter_RejectsEmptyUploadID(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	require.NotEmpty(t, routes)

	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID: "",
		FileData: []byte("test file data"),
	}

	bifrostCtx := schemas.NewBifrostContext(
		context.Background(),
		schemas.NoDeadline,
	)

	fileReq, err := route.FileRequestConverter(bifrostCtx, req)

	require.Error(t, err)
	assert.Nil(t, fileReq)

	assert.Equal(t, "upload_id missing — step 1 should have been short-circuited", err.Error())
}

type genAIUploadTestAccount struct {
	baseURL string
}

func (a *genAIUploadTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.Gemini}, nil
}

func (a *genAIUploadTestAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	useForBatchAPI := true
	return []schemas.Key{{
		ID:             "genai-upload-test-key",
		Value:          *schemas.NewSecretVar("test-gemini-key"),
		Weight:         100,
		UseForBatchAPI: &useForBatchAPI,
	}}, nil
}

func (a *genAIUploadTestAccount) GetConfigForProvider(schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        a.baseURL + "/v1beta",
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, nil
}

var _ schemas.Account = (*genAIUploadTestAccount)(nil)

// TestGenAIResumableUploadE2EFlow verifies the complete resumable upload flow
// and ensures the session remains available when reaching the provider.
func TestGenAIResumableUploadE2EFlow(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()
	RegisterKVDecoders(kvStore)

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	var expectedUploadID atomic.Pointer[string]
	var providerCallCount atomic.Int32
	providerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCallCount.Add(1)
		if r.URL.Path != "/upload/v1beta/files" {
			http.Error(w, "unexpected provider path", http.StatusNotFound)
			return
		}
		expectedUploadIDValue := expectedUploadID.Load()
		if expectedUploadIDValue == nil {
			http.Error(w, "upload ID was not captured", http.StatusInternalServerError)
			return
		}
		if _, err := kvStore.Get(*expectedUploadIDValue); err != nil {
			http.Error(w, fmt.Sprintf("upload session unavailable: %v", err), http.StatusInternalServerError)
			return
		}
		multipartReader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid multipart upload: %v", err), http.StatusBadRequest)
			return
		}
		var sawMetadata, sawFile bool
		for {
			part, err := multipartReader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid multipart part: %v", err), http.StatusBadRequest)
				return
			}
			partBody, err := io.ReadAll(part)
			if err != nil {
				http.Error(w, fmt.Sprintf("failed to read multipart part: %v", err), http.StatusBadRequest)
				return
			}
			switch part.FormName() {
			case "metadata":
				sawMetadata = strings.Contains(string(partBody), `"displayName":"test.pdf"`) && strings.Contains(string(partBody), `"mimeType":"application/pdf"`)
			case "file":
				sawFile = string(partBody) == "PDF file content here"
			}
		}
		if !sawMetadata || !sawFile {
			http.Error(w, "missing or invalid multipart upload parts", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"file":{"name":"files/test","displayName":"test.pdf","mimeType":"application/pdf","sizeBytes":"20","createTime":"2026-07-01T00:00:00Z","state":"ACTIVE","uri":"https://example.test/files/test"}}`))
	})
	providerServer := httptest.NewServer(providerHandler)
	t.Cleanup(providerServer.Close)
	providerBaseURL := providerServer.URL

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: &genAIUploadTestAccount{baseURL: providerBaseURL},
		Logger:  bifrost.NewNoOpLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)

	fileRoutes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	genAIRouter := NewGenericRouter(client, handlerStore, fileRoutes, nil, bifrost.NewNoOpLogger())
	httpRouter := router.New()
	genAIRouter.RegisterRoutes(httpRouter)
	gatewayServer := &fasthttp.Server{Handler: httpRouter.Handler}
	gatewayListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() { _ = gatewayServer.Serve(gatewayListener) }()
	t.Cleanup(func() { _ = gatewayServer.Shutdown() })

	httpClient := &http.Client{}
	startBody := strings.NewReader(`{"file":{"display_name":"test.pdf"},"mime_type":"application/pdf"}`)
	startRequest, err := http.NewRequest(http.MethodPost, "http://"+gatewayListener.Addr().String()+"/genai/upload/v1beta/files", startBody)
	require.NoError(t, err)
	startRequest.Header.Set("Content-Type", "application/json")
	startRequest.Header.Set("X-Goog-Upload-Protocol", "resumable")
	startRequest.Header.Set("X-Goog-Upload-Command", "start")
	startRequest.Header.Set("X-Goog-Upload-Header-Content-Type", "application/pdf")

	startResponse, err := httpClient.Do(startRequest)
	require.NoError(t, err)
	defer startResponse.Body.Close()

	assert.Equal(t, http.StatusOK, startResponse.StatusCode)
	assert.Equal(t, "active", startResponse.Header.Get("X-Goog-Upload-Status"))
	uploadURL := startResponse.Header.Get("X-Goog-Upload-URL")
	require.NotEmpty(t, uploadURL)

	uploadURLParsed, err := url.Parse(uploadURL)
	require.NoError(t, err)
	uploadID := uploadURLParsed.Query().Get("upload_id")
	require.NotEmpty(t, uploadID)
	expectedUploadID.Store(&uploadID)

	// Verify session exists after START
	stored, err := kvStore.Get(uploadID)
	require.NoError(t, err)
	session, ok := stored.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)
	assert.Equal(t, "test.pdf", session.DisplayName)
	assert.Equal(t, "application/pdf", session.MimeType)

	// Upload with finalize command
	uploadURI, err := http.NewRequest(http.MethodPost, uploadURL, strings.NewReader("PDF file content here"))
	require.NoError(t, err)
	uploadURI.Header.Set("Content-Type", "application/octet-stream")
	uploadURI.Header.Set("X-Goog-Upload-Command", "upload, finalize")

	uploadResponse, err := httpClient.Do(uploadURI)
	require.NoError(t, err)
	defer uploadResponse.Body.Close()

	uploadResponseBody, err := io.ReadAll(uploadResponse.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, uploadResponse.StatusCode, "upload response body: %s", uploadResponseBody)
	assert.Equal(t, "final", uploadResponse.Header.Get("X-Goog-Upload-Status"))
	assert.Equal(t, int32(1), providerCallCount.Load())

	_, err = kvStore.Get(uploadID)
	assert.ErrorIs(t, err, kvstore.ErrNotFound)
}
