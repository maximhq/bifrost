package integrations

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/vertex"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

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
	assert.Nil(t, route.PreCallback)

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
	assert.Equal(t, schemas.Vertex, bifrostReq.RerankRequest.Provider)
	assert.Equal(t, "semantic-ranker-default@latest", bifrostReq.RerankRequest.Model)
	assert.Equal(t, "capital of france", bifrostReq.RerankRequest.Query)
	require.Len(t, bifrostReq.RerankRequest.Documents, 2)
	assert.Equal(t, "Paris is capital of France", bifrostReq.RerankRequest.Documents[0].Text)
	assert.Equal(t, "Berlin is capital of Germany", bifrostReq.RerankRequest.Documents[1].Text)
	require.NotNil(t, bifrostReq.RerankRequest.Params)
	require.NotNil(t, bifrostReq.RerankRequest.Params.TopN)
	assert.Equal(t, 2, *bifrostReq.RerankRequest.Params.TopN)
}

func TestGenAIRerankResponseConverterUsesRawResponse(t *testing.T) {
	route := createGenAIRerankRouteConfig("/genai")
	require.NotNil(t, route.RerankResponseConverter)

	raw := map[string]interface{}{"records": []interface{}{}}
	resp := &schemas.BifrostRerankResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.Vertex,
			RawResponse: raw,
		},
	}
	converted, err := route.RerankResponseConverter(nil, resp)
	require.NoError(t, err)
	assert.Equal(t, raw, converted)
}

func TestGenAIRerankResponseConverterFallsBackWhenNotVertex(t *testing.T) {
	route := createGenAIRerankRouteConfig("/genai")
	require.NotNil(t, route.RerankResponseConverter)

	resp := &schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{
			{Index: 0, RelevanceScore: 0.9},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Cohere,
		},
	}
	converted, err := route.RerankResponseConverter(nil, resp)
	require.NoError(t, err)
	assert.Equal(t, resp, converted)
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

// Issue #5314: isImageEditRequest must be order-insensitive across parts.
// The native Gemini API accepts inlineData in any position within a content's
// parts, so a text part preceding the image must still classify as an edit.
func TestIsImageEditRequest_ImageFirst(t *testing.T) {
	req := &gemini.GeminiGenerationRequest{
		Contents: []gemini.Content{
			{
				Parts: []*gemini.Part{
					{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "AAAA"}},
					{Text: "Add one small yellow circle at the exact center."},
				},
			},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityImage},
		},
	}

	assert.True(t, isImageEditRequest(req), "image-first ordering must classify as image edit")
}

func TestIsImageEditRequest_TextFirst(t *testing.T) {
	req := &gemini.GeminiGenerationRequest{
		Contents: []gemini.Content{
			{
				Parts: []*gemini.Part{
					{Text: "Add one small yellow circle at the exact center."},
					{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "AAAA"}},
				},
			},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityImage},
		},
	}

	assert.True(t, isImageEditRequest(req), "text-first ordering must also classify as image edit (order-insensitive)")
}

func TestIsImageEditRequest_TextOnly_NotEdit(t *testing.T) {
	req := &gemini.GeminiGenerationRequest{
		Contents: []gemini.Content{
			{
				Parts: []*gemini.Part{
					{Text: "A photo of a cat."},
				},
			},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityImage},
		},
	}

	assert.False(t, isImageEditRequest(req), "text-only request must not classify as image edit")
}

// Scope is intentionally limited to the first content — matching the original
// (pre-fix) target — since deciding whether an image in a later/earlier
// content "belongs" to the current turn is ambiguous in multi-turn
// conversations (a stale unrelated image and an intentional continued-edit
// reference look identical) and is out of scope for the part-ordering bug
// this fix addresses.
func TestIsImageEditRequest_ImageOnlyInLaterContent_NotEdit(t *testing.T) {
	req := &gemini.GeminiGenerationRequest{
		Contents: []gemini.Content{
			{Parts: []*gemini.Part{{Text: "first turn"}}},
			{
				Parts: []*gemini.Part{
					{Text: "second turn prompt"},
					{InlineData: &gemini.Blob{MIMEType: "image/jpeg", Data: "AAAA"}},
				},
			},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityImage},
		},
	}

	assert.False(t, isImageEditRequest(req), "an image in a non-first content must not be treated as an edit source; only the first content is inspected")
}

// A historical image left over from an earlier turn (e.g. a previous model
// response) in a multi-turn conversation must not be picked up as the edit
// source for a new, unrelated, text-only request in a later turn.
func TestIsImageEditRequest_HistoricalImageInEarlierContent_NotEdit(t *testing.T) {
	req := &gemini.GeminiGenerationRequest{
		Contents: []gemini.Content{
			{Role: "user", Parts: []*gemini.Part{{Text: "generate a cat"}}},
			{Role: "model", Parts: []*gemini.Part{{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "AAAA"}}}},
			{Role: "user", Parts: []*gemini.Part{{Text: "now generate a completely different dog picture"}}},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityImage},
		},
	}

	assert.False(t, isImageEditRequest(req), "an image from earlier conversation history must not be treated as an edit source for a new text-only turn")
}

func TestIsImageEditRequest_CaseInsensitiveImageMimeType(t *testing.T) {
	req := &gemini.GeminiGenerationRequest{
		Contents: []gemini.Content{
			{
				Parts: []*gemini.Part{
					{Text: "prompt"},
					{InlineData: &gemini.Blob{MIMEType: "IMAGE/PNG", Data: "AAAA"}},
				},
			},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityImage},
		},
	}

	assert.True(t, isImageEditRequest(req), "MIME type match must be case-insensitive")
}

func TestIsImageEditRequest_NilPartInList(t *testing.T) {
	req := &gemini.GeminiGenerationRequest{
		Contents: []gemini.Content{
			{
				Parts: []*gemini.Part{
					nil,
					{Text: "prompt"},
					{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "AAAA"}},
				},
			},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityImage},
		},
	}

	assert.NotPanics(t, func() {
		assert.True(t, isImageEditRequest(req))
	})
}

func TestIsImageEditRequest_NonImageInlineData_NotEdit(t *testing.T) {
	req := &gemini.GeminiGenerationRequest{
		Contents: []gemini.Content{
			{
				Parts: []*gemini.Part{
					{Text: "prompt"},
					{InlineData: &gemini.Blob{MIMEType: "application/pdf", Data: "AAAA"}},
				},
			},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityImage},
		},
	}

	assert.False(t, isImageEditRequest(req), "non-image inline data must not classify as image edit")
}

func TestIsImageEditRequest_ImageWithoutImageModality_NotEdit(t *testing.T) {
	req := &gemini.GeminiGenerationRequest{
		Contents: []gemini.Content{
			{
				Parts: []*gemini.Part{
					{Text: "prompt"},
					{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "AAAA"}},
				},
			},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityText},
		},
	}

	assert.False(t, isImageEditRequest(req), "image without IMAGE response modality must not classify as image edit")
}

// End-to-end: text-first ordering must route through ToBifrostImageEditRequest
// and preserve the inline image bytes, not silently drop them via the
// image-generation path (the actual symptom reported in issue #5314).
func TestIsImageEditRequest_TextFirst_EndToEndPreservesImage(t *testing.T) {
	imageB64 := "AQIDBA=="
	req := &gemini.GeminiGenerationRequest{
		Model: "gemini-2.5-flash-image",
		Contents: []gemini.Content{
			{
				Parts: []*gemini.Part{
					{Text: "Add a yellow circle."},
					{InlineData: &gemini.Blob{MIMEType: "image/png", Data: imageB64}},
				},
			},
		},
		GenerationConfig: gemini.GenerationConfig{
			ResponseModalities: []gemini.Modality{gemini.ModalityImage},
		},
	}

	require.True(t, isImageEditRequest(req), "text-first request must classify as image edit")

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	editReq := req.ToBifrostImageEditRequest(bifrostCtx)
	require.NotNil(t, editReq)
	require.NotNil(t, editReq.Input)
	require.NotEmpty(t, editReq.Input.Images, "input image must be preserved, not dropped")

	decoded, err := base64.StdEncoding.DecodeString(imageB64)
	require.NoError(t, err)
	assert.Equal(t, decoded, editReq.Input.Images[0].Image)
}
