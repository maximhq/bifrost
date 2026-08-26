package integrations

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
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
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type fileUploadTestHandlerStore struct {
	*mockHandlerStore
	kvStore *kvstore.Store
}

const (
	bytesPerKB        = 1024
	bytesPerMB        = 1024 * bytesPerKB
	testUploadLimitKB = 100
)

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
	uploadID := "retry-upload-id"

	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "retry-test.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
		NextOffset:  0,
		Chunks:      make(map[int64][]byte),
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	bifrostCtx := schemas.NewBifrostContext(
		context.Background(),
		schemas.NoDeadline,
	)

	chunk := []byte("first upload chunk")

	// First upload of the chunk.
	firstReq := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		FileData:        chunk,
		UploadCommand:   "upload",
		UploadOffset:    0,
		HasUploadOffset: true,
	}

	firstFileReq, err := route.FileRequestConverter(bifrostCtx, firstReq)
	require.NoError(t, err)
	require.NotNil(t, firstFileReq)

	assert.True(t, firstFileReq.Handled)
	assert.Nil(t, firstFileReq.UploadRequest)
	assert.Equal(t, "active", firstFileReq.HandledHeaders["X-Goog-Upload-Status"])

	storedSessionValue, err := kvStore.Get(uploadID)
	require.NoError(t, err)

	storedSession, ok := storedSessionValue.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	assert.Equal(t, int64(len(chunk)), storedSession.NextOffset)
	require.Contains(t, storedSession.Chunks, int64(0))
	assert.Equal(t, chunk, storedSession.Chunks[0])

	retryReq := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		FileData:        chunk,
		UploadCommand:   "upload",
		UploadOffset:    0,
		HasUploadOffset: true,
	}

	retryFileReq, err := route.FileRequestConverter(bifrostCtx, retryReq)

	require.NoError(t, err)
	require.NotNil(t, retryFileReq)

	assert.True(t, retryFileReq.Handled)
	assert.Nil(t, retryFileReq.UploadRequest)
	assert.Equal(t, "active", retryFileReq.HandledHeaders["X-Goog-Upload-Status"])

	storedSessionValue, err = kvStore.Get(uploadID)
	require.NoError(t, err)

	storedSession, ok = storedSessionValue.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	assert.Equal(t, int64(len(chunk)), storedSession.NextOffset)
	assert.Equal(t, chunk, storedSession.Chunks[0])

	// Verify that the original session metadata is preserved.
	assert.Equal(t, session.DisplayName, storedSession.DisplayName)
	assert.Equal(t, session.MimeType, storedSession.MimeType)
	assert.Equal(t, session.Provider, storedSession.Provider)
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
		Chunks:      make(map[int64][]byte),
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	chunk := []byte("first upload chunk")

	// First upload.
	firstReq := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		UploadOffset:    0,
		FileData:        chunk,
	}

	firstFileReq, err := route.FileRequestConverter(bifrostCtx, firstReq)

	require.NoError(t, err)
	require.NotNil(t, firstFileReq)
	require.True(t, firstFileReq.Handled)

	// Verify the chunk was stored and NextOffset advanced.
	storedValue, err := kvStore.Get(uploadID)
	require.NoError(t, err)

	storedSession, ok := storedValue.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	assert.Equal(t, int64(len(chunk)), storedSession.NextOffset)
	assert.Equal(t, chunk, storedSession.Chunks[0])

	// Retry the SAME offset with the SAME bytes.
	retryReq := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		UploadOffset:    0,
		FileData:        chunk,
	}

	retryFileReq, err := route.FileRequestConverter(bifrostCtx, retryReq)
	require.NoError(t, err)
	require.NotNil(t, retryFileReq)
	require.True(t, retryFileReq.Handled)

	// Retry must not advance NextOffset again.
	storedValue, err = kvStore.Get(uploadID)
	require.NoError(t, err)

	storedSession, ok = storedValue.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	assert.Equal(t, int64(len(chunk)), storedSession.NextOffset)
	assert.Equal(t, chunk, storedSession.Chunks[0])
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
		UploadID:      uploadID,
		FileData:      []byte("test file data"),
		UploadCommand: "upload, finalize",
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

// Test that PostCallback deletes the session after successful finalization.
func TestFileUploadPostCallback_DeletesSessionAfterFinalization(t *testing.T) {
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

	uploadID := "finalize-id"
	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "finalize-test.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:      uploadID,
		FileData:      []byte("final chunk"),
		UploadCommand: "upload,finalize",
	}

	// Finalize request succeeds - PostCallback is called and deletes session.
	ctx := &fasthttp.RequestCtx{}

	err = route.PostCallback(ctx, req, nil)
	require.NoError(t, err)

	// Response should indicate that the upload is finalized.
	assert.Equal(t, "final", string(ctx.Response.Header.Peek("X-Goog-Upload-Status")))

	// Session should be deleted after successful finalization.
	stored, err := kvStore.Get(uploadID)
	assert.Error(t, err, "session should be deleted after finalization")
	assert.Nil(t, stored)
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
	var providerFailureCount atomic.Int32

	providerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount := providerCallCount.Add(1)

		if r.URL.Path != "/upload/v1beta/files" {
			http.Error(w, "unexpected provider path", http.StatusNotFound)
			return
		}

		expectedUploadIDValue := expectedUploadID.Load()
		if expectedUploadIDValue == nil {
			http.Error(w, "upload ID was not captured", http.StatusInternalServerError)
			return
		}

		// The upload session must still exist when the provider is called.
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

		// Fail the first provider request intentionally.
		// This simulates an upstream failure after the chunk has already
		// been atomically persisted in the resumable upload session.
		if callCount == 1 {
			providerFailureCount.Add(1)

			http.Error(w, "simulated provider failure", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Goog-Upload-Status", "final")

		_, _ = w.Write([]byte(
			`{"file":{"name":"files/test","displayName":"test.pdf","mimeType":"application/pdf","sizeBytes":"20","createTime":"2026-07-01T00:00:00Z","state":"ACTIVE","uri":"https://example.test/files/test"}}`,
		))
	})

	providerServer := httptest.NewServer(providerHandler)
	t.Cleanup(providerServer.Close)

	providerBaseURL := providerServer.URL
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: &genAIUploadTestAccount{
			baseURL: providerBaseURL,
		},
		Logger: bifrost.NewNoOpLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)

	fileRoutes := CreateGenAIFileRouteConfigs("/genai", handlerStore)

	genAIRouter := NewGenericRouter(client, handlerStore, fileRoutes, nil, bifrost.NewNoOpLogger())
	httpRouter := router.New()
	genAIRouter.RegisterRoutes(httpRouter)

	gatewayServer := &fasthttp.Server{
		Handler: httpRouter.Handler,
	}

	gatewayListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() {
		_ = gatewayServer.Serve(gatewayListener)
	}()

	t.Cleanup(func() {
		_ = gatewayServer.Shutdown()
	})

	httpClient := &http.Client{}
	startBody := strings.NewReader(
		`{"file":{"display_name":"test.pdf"},"mime_type":"application/pdf"}`,
	)

	startRequest, err := http.NewRequest(http.MethodPost, "http://"+gatewayListener.Addr().String()+"/genai/upload/v1beta/files", startBody)
	require.NoError(t, err)

	startRequest.Header.Set("Content-Type", "application/json")
	startRequest.Header.Set("X-Goog-Upload-Protocol", "resumable")
	startRequest.Header.Set("X-Goog-Upload-Command", "start")
	startRequest.Header.Set("X-Goog-Upload-Header-Content-Type", "application/pdf")

	startResponse, err := httpClient.Do(startRequest)
	require.NoError(t, err)
	defer startResponse.Body.Close()

	require.Equal(t, http.StatusOK, startResponse.StatusCode)
	assert.Equal(t, "active", startResponse.Header.Get("X-Goog-Upload-Status"))

	uploadURL := startResponse.Header.Get("X-Goog-Upload-URL")
	require.NotEmpty(t, uploadURL)

	uploadURLParsed, err := url.Parse(uploadURL)
	require.NoError(t, err)

	uploadID := uploadURLParsed.Query().Get("upload_id")
	require.NotEmpty(t, uploadID)

	expectedUploadID.Store(&uploadID)

	// Verify that START created the session.
	stored, err := kvStore.Get(uploadID)
	require.NoError(t, err)

	session, ok := stored.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	assert.Equal(t, "test.pdf", session.DisplayName)
	assert.Equal(t, "application/pdf", session.MimeType)
	assert.Equal(t, int64(0), session.NextOffset)

	firstChunk := "PDF file "
	firstUpload, err := http.NewRequest(http.MethodPost, uploadURL, strings.NewReader(firstChunk))
	require.NoError(t, err)

	firstUpload.Header.Set("Content-Type", "application/octet-stream")
	firstUpload.Header.Set("X-Goog-Upload-Command", "upload")
	firstUpload.Header.Set("X-Goog-Upload-Offset", "0")

	firstResponse, err := httpClient.Do(firstUpload)
	require.NoError(t, err)
	defer firstResponse.Body.Close()

	require.Equal(t, http.StatusOK, firstResponse.StatusCode)
	assert.Equal(t, "active", firstResponse.Header.Get("X-Goog-Upload-Status"))

	// The provider must not be called for a non-final chunk.
	assert.Equal(t, int32(0), providerCallCount.Load())

	// Verify the first chunk was atomically persisted.
	stored, err = kvStore.Get(uploadID)
	require.NoError(t, err)

	session, ok = stored.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	assert.Equal(t, int64(len(firstChunk)), session.NextOffset)
	require.Contains(t, session.Chunks, int64(0))

	assert.Equal(t, []byte(firstChunk), session.Chunks[0])

	finalChunk := "content here"
	finalOffset := len(firstChunk)

	finalUpload, err := http.NewRequest(http.MethodPost, uploadURL, strings.NewReader(finalChunk))
	require.NoError(t, err)

	finalUpload.Header.Set("Content-Type", "application/octet-stream")
	finalUpload.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	finalUpload.Header.Set("X-Goog-Upload-Offset", strconv.Itoa(finalOffset))

	finalResponse, err := httpClient.Do(finalUpload)
	require.NoError(t, err)
	defer finalResponse.Body.Close()

	// Provider intentionally fails.
	assert.Equal(t, http.StatusInternalServerError, finalResponse.StatusCode)

	assert.Equal(t, int32(1), providerCallCount.Load())
	assert.Equal(t, int32(1), providerFailureCount.Load())

	stored, err = kvStore.Get(uploadID)
	require.NoError(t, err, "upload session must survive provider failure")

	session, ok = stored.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	assert.Equal(t, int64(len(firstChunk)+len(finalChunk)), session.NextOffset)
	assert.Equal(t, []byte(firstChunk), session.Chunks[0])

	assert.Equal(t, []byte(finalChunk), session.Chunks[int64(finalOffset)])

	retryFinalUpload, err := http.NewRequest(http.MethodPost, uploadURL, strings.NewReader(finalChunk))
	require.NoError(t, err)

	retryFinalUpload.Header.Set("Content-Type", "application/octet-stream")
	retryFinalUpload.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	retryFinalUpload.Header.Set("X-Goog-Upload-Offset", strconv.Itoa(finalOffset))

	retryResponse, err := httpClient.Do(retryFinalUpload)
	require.NoError(t, err)

	retryResponseBody, err := io.ReadAll(retryResponse.Body)
	require.NoError(t, err)
	defer retryResponse.Body.Close()

	require.Equal(t, http.StatusOK, retryResponse.StatusCode, "retry body: %s", retryResponseBody)
	assert.Equal(t, "final", retryResponse.Header.Get("X-Goog-Upload-Status"))

	// A retry is allowed after the failed provider request cleared Finalizing.
	assert.Equal(t, int32(2), providerCallCount.Load())
	assert.Equal(t, int32(1), providerFailureCount.Load())

	_, err = kvStore.Get(uploadID)
	assert.ErrorIs(t, err, kvstore.ErrNotFound, "successful retry should delete the upload session")
}

// A non-final upload chunk within the configured size limit should be accepted.
func TestFileRequestConverter_AllowsNonFinalChunkWithinConfiguredLimit(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}
	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	uploadID := "large-intermediate-chunk"
	require.NoError(t, kvStore.SetWithTTL(uploadID, &gemini.GeminiResumableUploadSession{
		Provider: schemas.Gemini,
		Chunks:   make(map[int64][]byte),
	}, time.Minute))

	const largeChunkSizeKB = 11
	largeChunk := make([]byte, largeChunkSizeKB*bytesPerKB)
	firstReq := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		FileData:        largeChunk,
	}

	fileReq, err := route.FileRequestConverter(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), firstReq)
	require.NoError(t, err)
	require.NotNil(t, fileReq)
	assert.True(t, fileReq.Handled)

	secondReq := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		UploadOffset:    int64(len(largeChunk)),
		FileData:        []byte("tail"),
	}
	fileReq, err = route.FileRequestConverter(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), secondReq)
	require.NoError(t, err)
	require.NotNil(t, fileReq)
	assert.True(t, fileReq.Handled)

	stored, err := kvStore.Get(uploadID)
	require.NoError(t, err)
	assert.Equal(t, int64(len(largeChunk)+len("tail")), stored.(*gemini.GeminiResumableUploadSession).NextOffset)
}

func TestFileRequestConverter_RejectsRetryWithDifferentData(t *testing.T) {
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

	uploadID := "different-data-retry"
	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "test.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
		Chunks: map[int64][]byte{
			0: []byte("original chunk"),
		},
		NextOffset: int64(len("original chunk")),
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	bifrostCtx := schemas.NewBifrostContext(
		context.Background(),
		schemas.NoDeadline,
	)

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		UploadOffset:    0,
		FileData:        []byte("different chunk"),
	}

	fileReq, err := route.FileRequestConverter(bifrostCtx, req)

	require.Error(t, err)
	assert.Nil(t, fileReq)
	assert.Contains(t, err.Error(), "already exists with different data")

	// Verify the original chunk was not modified.
	storedValue, err := kvStore.Get(uploadID)
	require.NoError(t, err)

	storedSession := storedValue.(*gemini.GeminiResumableUploadSession)

	assert.Equal(t, []byte("original chunk"), storedSession.Chunks[0])
	assert.Equal(t, int64(len("original chunk")), storedSession.NextOffset)
}

// missing offset
func TestFileRequestConverter_RejectsMissingUploadCommand(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        "test-upload",
		HasUploadOffset: true,
		UploadOffset:    0,
		FileData:        []byte("chunk"),
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	fileReq, err := route.FileRequestConverter(ctx, req)
	require.Error(t, err)

	assert.Nil(t, fileReq)
	assert.Equal(t, "missing X-Goog-Upload-Command header", err.Error())
}

// invalid headers don't mutate the session
func TestFileRequestConverter_RejectsUnsupportedUploadCommand(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        "test-upload",
		UploadCommand:   "query",
		HasUploadOffset: true,
		UploadOffset:    0,
		FileData:        []byte("chunk"),
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	fileReq, err := route.FileRequestConverter(ctx, req)
	require.Error(t, err)

	assert.Nil(t, fileReq)
	assert.Contains(
		t,
		err.Error(),
		"unsupported X-Goog-Upload-Command",
	)
}

// concurrent requests test
func TestFileRequestConverter_ConcurrentRequestsSameUploadID(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	uploadID := "concurrent-upload"
	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "concurrent.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
		Chunks:      make(map[int64][]byte),
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	ctx1 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx2 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	req1 := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		UploadOffset:    0,
		FileData:        []byte("chunk"),
	}

	req2 := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		UploadOffset:    0,
		FileData:        []byte("chunk"),
	}

	var wg sync.WaitGroup
	wg.Add(2)

	var err1, err2 error

	go func() {
		defer wg.Done()
		_, err1 = route.FileRequestConverter(ctx1, req1)
	}()

	go func() {
		defer wg.Done()
		_, err2 = route.FileRequestConverter(ctx2, req2)
	}()

	wg.Wait()

	require.NoError(t, err1)
	require.NoError(t, err2)

	storedValue, err := kvStore.Get(uploadID)
	require.NoError(t, err)

	storedSession := storedValue.(*gemini.GeminiResumableUploadSession)

	// The same chunk must only advance the offset once.
	assert.Equal(t, int64(len("chunk")), storedSession.NextOffset)
	assert.Equal(t, []byte("chunk"), storedSession.Chunks[0])
}

// Test that concurrent finalize requests only allow one to reach the provider.
// The second finalize request should be rejected with an error.
func TestFileRequestConverter_ConcurrentFinalizeRequestsOnlyOneReachesProvider(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	uploadID := "concurrent-finalize"
	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "concurrent-final.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
		NextOffset:  5,
		Chunks: map[int64][]byte{
			0: []byte("chunk"),
		},
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	ctx1 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx2 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	req1 := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload,finalize",
		HasUploadOffset: true,
		UploadOffset:    5,
		FileData:        []byte("final"),
	}

	req2 := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload,finalize",
		HasUploadOffset: true,
		UploadOffset:    5,
		FileData:        []byte("final"),
	}

	var wg sync.WaitGroup
	wg.Add(2)

	var err1, err2 error
	var fileReq1, fileReq2 *FileRequest

	go func() {
		defer wg.Done()
		fileReq1, err1 = route.FileRequestConverter(ctx1, req1)
	}()

	go func() {
		defer wg.Done()
		fileReq2, err2 = route.FileRequestConverter(ctx2, req2)
	}()

	wg.Wait()

	// One request should succeed (no error), the other should fail with "already being finalized" error
	successCount := 0
	if err1 == nil {
		successCount++
		require.NotNil(t, fileReq1)
	}
	if err2 == nil {
		successCount++
		require.NotNil(t, fileReq2)
	}

	assert.Equal(t, 1, successCount, "exactly one finalize request should succeed")

	// Check that one has the error indicating concurrent finalization
	if err1 != nil {
		assert.Contains(t, err1.Error(), "already being finalized")
	}
	if err2 != nil {
		assert.Contains(t, err2.Error(), "already being finalized")
	}

	// Verify session is marked as finalizing
	storedValue, err := kvStore.Get(uploadID)
	require.NoError(t, err)

	storedSession := storedValue.(*gemini.GeminiResumableUploadSession)
	assert.True(t, storedSession.Finalizing, "session should be marked as finalizing")
}

func TestFileRequestConverter_RejectsInvalidUploadOffset(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	uploadID := "invalid-offset-upload"
	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "test.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
		NextOffset:  10,
		Chunks: map[int64][]byte{
			0: []byte("1234567890"),
		},
	}

	require.NoError(t, kvStore.SetWithTTL(uploadID, session, time.Minute))

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		UploadOffset:    5,
		FileData:        []byte("chunk"),
	}

	fileReq, err := route.FileRequestConverter(ctx, req)

	require.Error(t, err)
	assert.Nil(t, fileReq)
	assert.Contains(t, err.Error(), "invalid upload offset")

	stored, err := kvStore.Get(uploadID)
	require.NoError(t, err)

	storedSession := stored.(*gemini.GeminiResumableUploadSession)
	assert.Equal(t, int64(10), storedSession.NextOffset)
}

// Total upload size exactly 100 MB should succeed.
func TestFileRequestConverter_AllowsUploadAtMaxSize(t *testing.T) {
	t.Parallel()
	const finalChunkSizeKB = 10
	maxUploadSize := testUploadLimitKB * bytesPerKB

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := createGenAIFileRouteConfigs("/genai", handlerStore, int64(maxUploadSize))
	require.NotEmpty(t, routes)

	converter := routes[0].FileRequestConverter
	require.NotNil(t, converter)

	uploadID := "test-upload-max-size"

	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "test.pdf",
		MimeType:    "application/pdf",
		Provider:    "gemini",
		NextOffset:  int64(maxUploadSize - finalChunkSizeKB*bytesPerKB),
		Chunks: map[int64][]byte{
			0: make([]byte, maxUploadSize-finalChunkSizeKB*bytesPerKB),
		},
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	ctx := &schemas.BifrostContext{}

	// Add the final chunk to reach the configured maximum.
	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		UploadOffset:    int64(maxUploadSize - finalChunkSizeKB*bytesPerKB),
		HasUploadOffset: true,
		FileData:        make([]byte, finalChunkSizeKB*bytesPerKB),
	}

	result, err := converter(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Handled)
}

// Upload exceeding the 100 MB total size limit should fail.
func TestFileRequestConverter_RejectsUploadExceedingMaxSize(t *testing.T) {
	t.Parallel()
	const finalChunkSizeKB = 10
	maxUploadSize := testUploadLimitKB * bytesPerKB

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := createGenAIFileRouteConfigs("/genai", handlerStore, int64(maxUploadSize))
	require.NotEmpty(t, routes)

	converter := routes[0].FileRequestConverter
	require.NotNil(t, converter)

	uploadID := "test-upload-over-limit"

	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "test.pdf",
		MimeType:    "application/pdf",
		Provider:    "gemini",
		NextOffset:  int64(maxUploadSize - (finalChunkSizeKB-1)*bytesPerKB),
		Chunks: map[int64][]byte{
			0: make([]byte, maxUploadSize-(finalChunkSizeKB-1)*bytesPerKB),
		},
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	ctx := &schemas.BifrostContext{}

	// The chunk itself is within the request limit, but the cumulative total exceeds the configured limit.
	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		UploadOffset:    int64(maxUploadSize - (finalChunkSizeKB-1)*bytesPerKB),
		HasUploadOffset: true,
		FileData:        make([]byte, finalChunkSizeKB*bytesPerKB),
	}

	result, err := converter(ctx, req)

	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "resumable upload exceeds maximum size")
}

// Multiple chunks that total exactly 100 MB should succeed.
func TestFileRequestConverter_AllowsMultipleChunksUpToMaxSize(t *testing.T) {
	t.Parallel()
	const chunkSizeKB = 10
	maxUploadSize := testUploadLimitKB * bytesPerKB

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := createGenAIFileRouteConfigs("/genai", handlerStore, int64(maxUploadSize))
	require.NotEmpty(t, routes)

	converter := routes[0].FileRequestConverter
	require.NotNil(t, converter)

	uploadID := "test-upload-multiple-chunks"

	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "test.pdf",
		MimeType:    "application/pdf",
		Provider:    "gemini",
		NextOffset:  int64(maxUploadSize - chunkSizeKB*bytesPerKB),
		Chunks: map[int64][]byte{
			0: make([]byte, maxUploadSize-chunkSizeKB*bytesPerKB),
		},
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	ctx := &schemas.BifrostContext{}

	// Add one chunk to reach exactly the configured total upload limit.
	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		UploadOffset:    int64(maxUploadSize - chunkSizeKB*bytesPerKB),
		HasUploadOffset: true,
		FileData:        make([]byte, chunkSizeKB*bytesPerKB),
	}

	result, err := converter(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Handled)

	// Verify the total uploaded size is exactly 100 MB.
	value, err := kvStore.Get(uploadID)
	require.NoError(t, err)

	updatedSession, ok := value.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)
	require.Equal(t, int64(maxUploadSize), updatedSession.NextOffset)
}

// A single-shot upload and finalize request within the configured size limit should succeed.
func TestFileRequestConverter_AllowsSingleShotFinalizeWithinConfiguredLimit(t *testing.T) {
	t.Parallel()
	const uploadSize = 11 * bytesPerKB

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}
	routes := createGenAIFileRouteConfigs("/genai", handlerStore, uploadSize)
	route := findGenAIRouteForTest(t, routes, "/genai/upload/v1beta/files", "POST")

	uploadID := "single-shot-over-ten-mb"
	require.NoError(t, kvStore.SetWithTTL(uploadID, &gemini.GeminiResumableUploadSession{
		DisplayName: "large.pdf",
		MimeType:    "application/pdf",
		Provider:    schemas.Gemini,
		Chunks:      make(map[int64][]byte),
	}, time.Minute))

	fileReq, err := route.FileRequestConverter(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload,finalize",
		UploadOffset:    0,
		HasUploadOffset: true,
		FileData:        make([]byte, uploadSize),
	})

	require.NoError(t, err)
	require.NotNil(t, fileReq)
	require.NotNil(t, fileReq.UploadRequest)
	assert.Len(t, fileReq.UploadRequest.File, uploadSize)
}

// Retried chunk should not be counted twice
func TestFileRequestConverter_RetryDoesNotIncreaseUploadSize(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := createGenAIFileRouteConfigs("/genai", handlerStore, int64(testUploadLimitKB*bytesPerKB))
	require.NotEmpty(t, routes)

	converter := routes[0].FileRequestConverter
	require.NotNil(t, converter)

	uploadID := "test-upload-retry"

	const chunkSizeKB = 10
	chunk := make([]byte, chunkSizeKB*bytesPerKB)

	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "test.pdf",
		MimeType:    "application/pdf",
		Provider:    "gemini",
		NextOffset:  int64(chunkSizeKB * bytesPerKB),
		Chunks: map[int64][]byte{
			0: chunk,
		},
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	ctx := &schemas.BifrostContext{}

	// Retry the same 10 MB chunk.
	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		UploadOffset:    0,
		HasUploadOffset: true,
		FileData:        chunk,
	}

	result, err := converter(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify the offset is still 10 MB.
	value, err := kvStore.Get(uploadID)
	require.NoError(t, err)

	updatedSession, ok := value.(*gemini.GeminiResumableUploadSession)
	require.True(t, ok)

	require.Equal(t, int64(chunkSizeKB*bytesPerKB), updatedSession.NextOffset)
}

// A single request body larger than the configured request-body limit should fail.
func TestFileRequestConverter_RejectsRequestExceedingMaxSize(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := createGenAIFileRouteConfigs("/genai", handlerStore, int64(testUploadLimitKB*bytesPerKB))
	require.NotEmpty(t, routes)

	converter := routes[0].FileRequestConverter
	require.NotNil(t, converter)

	uploadID := "test-upload-chunk-over-limit"

	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "test.pdf",
		MimeType:    "application/pdf",
		Provider:    "gemini",
		NextOffset:  0,
		Chunks:      make(map[int64][]byte),
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	ctx := &schemas.BifrostContext{}

	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		UploadOffset:    0,
		HasUploadOffset: true,
		FileData:        make([]byte, testUploadLimitKB*bytesPerKB+1),
	}

	result, err := converter(ctx, req)

	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "upload exceeds maximum size")
}

func TestFileRequestConverter_UsesConfiguredRequestBodyLimit(t *testing.T) {
	t.Parallel()

	const configuredLimitKB = 5

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{maxRequestBodySizeMB: lib.DefaultMaxRequestBodySizeMB},
		kvStore:          kvStore,
	}
	routes := createGenAIFileRouteConfigs("/genai", handlerStore, configuredLimitKB*bytesPerKB)
	converter := routes[0].FileRequestConverter
	require.NotNil(t, converter)

	underLimitID := "configured-limit-under"

	underLimit := make([]byte, configuredLimitKB*bytesPerKB-1)
	require.NoError(t, kvStore.SetWithTTL(underLimitID, &gemini.GeminiResumableUploadSession{
		Provider: schemas.Gemini,
		Chunks:   make(map[int64][]byte),
	}, time.Minute))

	underLimitReq, err := converter(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &gemini.GeminiFileUploadHandlerReq{
		UploadID:        underLimitID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		FileData:        underLimit,
	})
	require.NoError(t, err)
	require.NotNil(t, underLimitReq)
	assert.True(t, underLimitReq.Handled)

	// Construct another route set with a different limit. The first route set
	// must retain its captured 5 MB limit.
	handlerStore.mockHandlerStore.maxRequestBodySizeMB = lib.DefaultMaxRequestBodySizeMB
	defaultRoutes := CreateGenAIFileRouteConfigs("/genai", handlerStore)
	require.NotNil(t, defaultRoutes[0].FileRequestConverter)

	overLimitID := "configured-limit-over"
	require.NoError(t, kvStore.SetWithTTL(overLimitID, &gemini.GeminiResumableUploadSession{
		Provider: schemas.Gemini,
		Chunks:   make(map[int64][]byte),
	}, time.Minute))

	overLimitReq, err := converter(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &gemini.GeminiFileUploadHandlerReq{
		UploadID:        overLimitID,
		UploadCommand:   "upload",
		HasUploadOffset: true,
		FileData:        make([]byte, configuredLimitKB*bytesPerKB+1),
	})
	require.Error(t, err)
	assert.Nil(t, overLimitReq)
	assert.Contains(t, err.Error(), "upload exceeds maximum size")
}

func TestFileRequestConverter_RejectsCumulativeUploadExceedingMaxSize(t *testing.T) {
	t.Parallel()

	kvStore, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	defer kvStore.Close()

	handlerStore := &fileUploadTestHandlerStore{
		mockHandlerStore: &mockHandlerStore{},
		kvStore:          kvStore,
	}

	routes := createGenAIFileRouteConfigs("/genai", handlerStore, int64(testUploadLimitKB*bytesPerKB))
	require.NotEmpty(t, routes)

	converter := routes[0].FileRequestConverter
	require.NotNil(t, converter)

	uploadID := "test-cumulative-upload-over-limit"

	session := &gemini.GeminiResumableUploadSession{
		DisplayName: "test.pdf",
		MimeType:    "application/pdf",
		Provider:    "gemini",
		NextOffset:  0,
		Chunks:      make(map[int64][]byte),
	}

	err = kvStore.SetWithTTL(uploadID, session, time.Minute)
	require.NoError(t, err)

	ctx := &schemas.BifrostContext{}

	// Upload 11 valid chunks of 9 MB each = 99 MB.
	const chunkSizeKB = 9
	const finalChunkSizeKB = 2
	chunkSize := int64(chunkSizeKB * bytesPerKB)

	for i := int64(0); i < 11; i++ {
		req := &gemini.GeminiFileUploadHandlerReq{
			UploadID:        uploadID,
			UploadCommand:   "upload",
			UploadOffset:    i * chunkSize,
			HasUploadOffset: true,
			FileData:        make([]byte, chunkSize),
		}

		result, err := converter(ctx, req)

		require.NoError(t, err)
		require.NotNil(t, result)
		require.True(t, result.Handled)
	}

	// 99 MB has already been uploaded.
	// Adding a valid 2 MB chunk would make the total 101 MB.
	req := &gemini.GeminiFileUploadHandlerReq{
		UploadID:        uploadID,
		UploadCommand:   "upload",
		UploadOffset:    int64(11) * chunkSize,
		HasUploadOffset: true,
		FileData:        make([]byte, finalChunkSizeKB*bytesPerKB),
	}

	result, err := converter(ctx, req)

	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "resumable upload exceeds maximum size")
}
