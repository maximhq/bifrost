package integrations

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/vertex"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// TestRewriteGenAIRawRequestBodyRedactsOnlyContentFields verifies Gemini redaction covers native text and function-result values without touching metadata or function-call arguments.
func TestRewriteGenAIRawRequestBodyRedactsOnlyContentFields(t *testing.T) {
	rawBody := []byte(`{
		"systemInstruction":{"parts":[{"text":"system alice@example.com"}]},
		"contents":[
			{"role":"user","parts":[{"text":"first alice@example.com"}]},
			{"role":"model","parts":[
				{"thought":true,"text":"reason alice@example.com","thoughtSignature":"alice@example.com"},
				{"functionCall":{"name":"lookup","args":{"email":"alice@example.com"}}}
			]},
			{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"output":"tool alice@example.com","nested.value":{"contact:key":"alice@example.com"}}}}]}
		],
		"instances":[{"prompt":"image alice@example.com"}],
		"labels":{"owner":"alice@example.com"},
		"tools":[{"functionDeclarations":[{"name":"lookup","description":"alice@example.com"}]}]
	}`)

	got, err := rewriteGenAIRawRequestBody(rawBody, map[string]string{"alice@example.com": "[EMAIL]"})
	require.NoError(t, err)

	redactedPaths := []string{
		"systemInstruction.parts.0.text",
		"contents.0.parts.0.text",
		"contents.1.parts.0.text",
		"contents.2.parts.0.functionResponse.response.output",
		`contents.2.parts.0.functionResponse.response.nested\.value.contact:key`,
		"instances.0.prompt",
	}
	for _, path := range redactedPaths {
		value := gjson.GetBytes(got, path).String()
		assert.NotContains(t, value, "alice@example.com", path)
		assert.Contains(t, value, "[EMAIL]", path)
	}

	untouchedPaths := []string{
		"contents.1.parts.0.thoughtSignature",
		"contents.1.parts.1.functionCall.args.email",
		"labels.owner",
		"tools.0.functionDeclarations.0.description",
	}
	for _, path := range untouchedPaths {
		assert.Equal(t, "alice@example.com", gjson.GetBytes(got, path).String(), path)
	}
	assert.True(t, strings.Contains(string(got), `"labels":{"owner":"alice@example.com"}`))
}

// TestRewriteGenAIRawRequestBodySupportsCountTokensEnvelope verifies both documented envelope spelling and snake-case system instructions use the same content allowlist.
func TestRewriteGenAIRawRequestBodySupportsCountTokensEnvelope(t *testing.T) {
	rawBody := []byte(`{"generate_content_request":{"system_instruction":{"parts":[{"text":"system alice@example.com"}]},"contents":[{"parts":[{"text":"user alice@example.com"}]}]}}`)

	got, err := rewriteGenAIRawRequestBody(rawBody, map[string]string{"alice@example.com": "[EMAIL]"})
	require.NoError(t, err)
	assert.Equal(t, "system [EMAIL]", gjson.GetBytes(got, "generate_content_request.system_instruction.parts.0.text").String())
	assert.Equal(t, "user [EMAIL]", gjson.GetBytes(got, "generate_content_request.contents.0.parts.0.text").String())
}

// TestRewriteGenAIRawRequestBodyRejectsUnmappedLiteral verifies a normalized runtime mutation cannot silently leave Gemini content unredacted.
func TestRewriteGenAIRawRequestBodyRejectsUnmappedLiteral(t *testing.T) {
	_, err := rewriteGenAIRawRequestBody(
		[]byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
		map[string]string{"alice@example.com": "[EMAIL]"},
	)
	require.Error(t, err)
}

// TestRewriteGenAIRawRequestBodyRejectsSensitiveObjectKeys verifies unsupported key mutation fails closed inside a function-result subtree.
func TestRewriteGenAIRawRequestBodyRejectsSensitiveObjectKeys(t *testing.T) {
	_, err := rewriteGenAIRawRequestBody(
		[]byte(`{"contents":[{"parts":[{"functionResponse":{"response":{"alice@example.com":"value"}}}]}]}`),
		map[string]string{"alice@example.com": "[EMAIL]"},
	)
	require.ErrorContains(t, err, "unsupported JSON object key")
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
	_, hasRewriter := bifrostCtx.Value(schemas.BifrostContextKeyRawRequestBodyTextRewriter).(schemas.RawRequestBodyTextRewriter)
	assert.True(t, hasRewriter)
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
	assert.Nil(t, bifrostCtx.Value(schemas.BifrostContextKeyRawRequestBodyTextRewriter))
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

// TestGenAIBatchCreateConverterCarriesDisplayNameAndInlineTools verifies the typed batch
// create path (used when the request is not an explicit-gemini raw passthrough) carries the
// batch display name and every modeled inline-request field (tools, cachedContent) through.
func TestGenAIBatchCreateConverterCarriesDisplayNameAndInlineTools(t *testing.T) {
	rawBody := []byte(`{"batch":{"displayName":"my-batch","inputConfig":{"requests":{"requests":[{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"get_weather"}]}],"cachedContent":"cachedContents/abc"},"metadata":{"key":"req-1"}}]}}}}`)
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("model", "gemini-2.5-flash:batchGenerateContent")
	ctx.Request.Header.SetMethod("POST")
	// No x-model-provider header, so the typed path (not raw passthrough) is exercised.
	ctx.Request.SetBody(rawBody)

	req := &gemini.GeminiBatchCreateRequest{}
	require.NoError(t, sonic.Unmarshal(rawBody, req))
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, extractAndSetModelAndRequestType(ctx, bifrostCtx, req))

	route := findGenAIRouteForTest(t, CreateGenAIRouteConfigs("/genai"), "/genai/v1beta/models/{model:*}", "POST")
	batchReq, err := route.BatchRequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, batchReq.CreateRequest)

	// #2: batch display name carried through.
	require.NotNil(t, batchReq.CreateRequest.DisplayName)
	assert.Equal(t, "my-batch", *batchReq.CreateRequest.DisplayName)

	// #3: inline tools + cachedContent survive (not dropped by the converter).
	require.Len(t, batchReq.CreateRequest.Requests, 1)
	body := batchReq.CreateRequest.Requests[0].Body
	assert.Contains(t, body, "tools")
	assert.Equal(t, "cachedContents/abc", body["cachedContent"])
	assert.Equal(t, "req-1", batchReq.CreateRequest.Requests[0].CustomID)
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
