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
// keep the caller's Gemini body: Gemini and Vertex do, any other provider gets it converted, and
// requests from other integrations are left alone.
func TestClearGenAIPassthroughForNonGoogleProvider(t *testing.T) {
	cases := []struct {
		name            string
		integrationType string
		baseProvider    schemas.ModelProvider
		wantCleared     bool
	}{
		{"genai to gemini keeps the raw body", "genai", schemas.Gemini, false},
		{"genai to vertex keeps the raw body", "genai", schemas.Vertex, false},
		{"genai to openai converts", "genai", schemas.OpenAI, true},
		{"genai to anthropic converts", "genai", schemas.Anthropic, true},
		{"genai to bedrock converts", "genai", schemas.Bedrock, true},
		{"anthropic integration is not this helper's", "anthropic", schemas.OpenAI, false},
		{"no integration is left alone", "", schemas.OpenAI, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tc.integrationType != "" {
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tc.integrationType)
			}
			ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
			ctx.SetValue(schemas.BifrostContextKeyRawRequestBodyTextRewriter, schemas.RawRequestBodyTextRewriter(func(body []byte, _ map[string]string) ([]byte, error) { return body, nil }))

			clearGenAIPassthroughForNonGoogleProvider(ctx, tc.baseProvider)

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
