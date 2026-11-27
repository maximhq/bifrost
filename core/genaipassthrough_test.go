package bifrost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestClearGenAIPassthroughForNonGoogleProvider pins which attempts of a GenAI-integration request
// keep the caller's Gemini body. Gemini does. Vertex does for the models it serves with a Google body
// (Gemini, Gemma, Imagen, Veo and numeric tuned endpoints), for batch jobs, which Vertex runs for
// Claude and Llama too, and for file and cached-content requests, which name no model or a Gemini one;
// a Claude, Mistral or other Model Garden model gets it converted, as does any other provider.
// Requests from other integrations are left alone.
func TestClearGenAIPassthroughForNonGoogleProvider(t *testing.T) {
	cases := []struct {
		name            string
		integrationType string
		baseProvider    schemas.ModelProvider
		requestType     schemas.RequestType
		model           string
		alias           *schemas.AliasConfig
		wantCleared     bool
	}{
		{"genai to gemini keeps the raw body", "genai", schemas.Gemini, schemas.ChatCompletionRequest, "gemini-2.5-flash", nil, false},
		{"genai to vertex gemini keeps the raw body", "genai", schemas.Vertex, schemas.ChatCompletionRequest, "gemini-2.5-flash", nil, false},
		{"genai to vertex gemma keeps the raw body", "genai", schemas.Vertex, schemas.ChatCompletionStreamRequest, "gemma-3-27b-it", nil, false},
		{"genai to a vertex tuned endpoint keeps the raw body", "genai", schemas.Vertex, schemas.ChatCompletionRequest, "1234567890123456789", nil, false},
		{"genai to a numeric Vertex OpenAI endpoint converts", "genai", schemas.Vertex, schemas.ChatCompletionRequest, "1234567890", &schemas.AliasConfig{VertexAliasCfg: &schemas.VertexAliasCfg{VertexEndpoint: &schemas.VertexEndpoint{EndpointID: "2222", Format: schemas.VertexEndpointOpenAIChat}}}, true},
		{"genai to a Gemma Vertex OpenAI endpoint converts", "genai", schemas.Vertex, schemas.ResponsesStreamRequest, "gemma-custom", &schemas.AliasConfig{ModelFamily: schemas.Ptr(schemas.ModelFamilyGemma), VertexAliasCfg: &schemas.VertexAliasCfg{VertexEndpoint: &schemas.VertexEndpoint{EndpointID: "2222", Format: schemas.VertexEndpointOpenAIChat}}}, true},
		{"genai to vertex imagen keeps the raw body", "genai", schemas.Vertex, schemas.ImageGenerationRequest, "imagen-4.0-generate-001", nil, false},
		{"genai to vertex veo keeps the raw body", "genai", schemas.Vertex, schemas.VideoGenerationRequest, "veo-3.0-generate-001", nil, false},
		{"genai to vertex claude converts", "genai", schemas.Vertex, schemas.ChatCompletionRequest, "claude-sonnet-4-5", nil, true},
		{"genai to vertex claude streaming responses converts", "genai", schemas.Vertex, schemas.ResponsesStreamRequest, "claude-opus-4-7", nil, true},
		{"genai to vertex mistral converts", "genai", schemas.Vertex, schemas.ChatCompletionRequest, "mistral-large-2411", nil, true},
		{"genai to a vertex model garden model converts", "genai", schemas.Vertex, schemas.ChatCompletionRequest, "meta/llama-3.3-70b-instruct-maas", nil, true},
		{"genai to a vertex alias for claude converts", "genai", schemas.Vertex, schemas.ChatCompletionRequest, "house-model", &schemas.AliasConfig{ModelID: "house-model", ModelName: schemas.Ptr("claude-sonnet-4-5")}, true},
		{"genai batch job on vertex claude keeps the raw body", "genai", schemas.Vertex, schemas.BatchCreateRequest, "claude-sonnet-4-5", nil, false},
		{"genai cached content create on vertex keeps the raw body", "genai", schemas.Vertex, schemas.CachedContentCreateRequest, "gemini-2.5-flash", nil, false},
		{"genai cached content update naming no model on vertex keeps the raw body", "genai", schemas.Vertex, schemas.CachedContentUpdateRequest, "", nil, false},
		{"genai file upload on vertex keeps the raw body", "genai", schemas.Vertex, schemas.FileUploadRequest, "", nil, false},
		{"genai to openai converts", "genai", schemas.OpenAI, schemas.ChatCompletionRequest, "gpt-4o-mini", nil, true},
		{"genai to anthropic converts", "genai", schemas.Anthropic, schemas.ChatCompletionRequest, "claude-sonnet-4-5", nil, true},
		{"genai to bedrock converts", "genai", schemas.Bedrock, schemas.ChatCompletionRequest, "claude-sonnet-4-5", nil, true},
		{"anthropic integration is not this helper's", "anthropic", schemas.OpenAI, schemas.ChatCompletionRequest, "gpt-4o-mini", nil, false},
		{"no integration is left alone", "", schemas.OpenAI, schemas.ChatCompletionRequest, "gpt-4o-mini", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tc.integrationType != "" {
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tc.integrationType)
			}
			if tc.alias != nil {
				ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: tc.model, Config: tc.alias})
			}
			ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
			ctx.SetValue(schemas.BifrostContextKeyRawRequestBodyTextRewriter, schemas.RawRequestBodyTextRewriter(func(body []byte, _ map[string]string) ([]byte, error) { return body, nil }))

			clearGenAIPassthroughForNonGoogleProvider(ctx, tc.baseProvider, tc.requestType, tc.model)

			raw, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
			_, hasRewriter := ctx.Value(schemas.BifrostContextKeyRawRequestBodyTextRewriter).(schemas.RawRequestBodyTextRewriter)
			if raw == tc.wantCleared || hasRewriter == tc.wantCleared {
				t.Fatalf("raw body on=%v rewriter=%v, want cleared=%v", raw, hasRewriter, tc.wantCleared)
			}
		})
	}
}

// A /genai request that names gemini/ is forwarded to Gemini as the caller's own body. When Gemini
// fails and a fallback on another provider runs, that provider gets the request converted to its own
// format: before, it was sent the Gemini body and refused it ("Missing required parameter: 'model'").
func TestGenAIFallbackToAnotherProviderIsConverted(t *testing.T) {
	const provGemini schemas.ModelProvider = "prov-gemini"
	var geminiCalls atomic.Int32
	gemini := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		geminiCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"internal error","status":"INTERNAL"}}`))
	}))
	defer gemini.Close()

	u := newScenarioUpstream(t)
	account := scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{provB: {scenarioKey("b1", 1)}})
	account.AddProviderWithBaseURL(provGemini, 4, 256, gemini.URL)
	account.SetCustomProviderConfig(provGemini, &schemas.CustomProviderConfig{BaseProviderType: schemas.Gemini})
	account.SetKeysForProvider(provGemini, []schemas.Key{scenarioKey("g1", 1)})
	account.mu.Lock()
	account.configs[provGemini].NetworkConfig.MaxRetries = 0
	account.mu.Unlock()
	client := scenarioClient(t, account, nil, nil)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "genai")
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	req := scenarioRequest(provGemini, "m", []schemas.Fallback{{Provider: provB, Model: "m"}})
	req.RawRequestBody = []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":8}}`)

	resp, err := client.ChatCompletionRequest(ctx, req)
	if err != nil {
		t.Fatalf("the fallback should have served, got %s", err.GetErrorString())
	}
	if got := resp.ExtraFields.RoutingInfo.Provider; got != provB {
		t.Fatalf("served by %s, want the fallback %s", got, provB)
	}
	if geminiCalls.Load() == 0 {
		t.Fatal("the Gemini primary was never called")
	}
	// The upstream reads "model" from the body: a converted request carries m, the Gemini body none.
	requireHits(t, u, map[string]int{"sk-b1|m": 1, "sk-b1|": 0})
}
