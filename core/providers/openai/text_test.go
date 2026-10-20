package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestTextCompletionPreservesZeroUsage(t *testing.T) {
	for _, input := range []string{
		`{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`,
		`{"total_tokens":12}`,
	} {
		for _, streaming := range []bool{false, true} {
			name := input + "/unary"
			if streaming {
				name = input + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/completions" {
						t.Errorf("unexpected upstream path: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, `data: {"id":"text-usage","model":"fixture","choices":[{"index":0,"text":"hello","finish_reason":"stop"}]}`+"\n\n")
						io.WriteString(w, `data: {"id":"text-usage","model":"fixture","choices":[],"usage":`+input+"}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"id":"text-usage","model":"fixture","choices":[{"index":0,"text":"hello","finish_reason":"stop"}],"usage":`+input+`}`)
					}
				}))
				defer server.Close()
				provider := NewOpenAIProvider(&schemas.ProviderConfig{
					NetworkConfig:        schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
					CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, CustomProviderKey: "gemini-web"},
				}, testNoopLogger{})
				ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				req := &schemas.BifrostTextCompletionRequest{Provider: "gemini-web", Model: "fixture", Input: &schemas.TextCompletionInput{PromptStr: schemas.Ptr("hello")}}
				check := func(response *schemas.BifrostTextCompletionResponse) {
					t.Helper()
					assertCompletionUsage(t, response.WithPreservedUsage(), input)
				}
				if !streaming {
					response, err := provider.TextCompletion(ctx, testKey(), req)
					require.Nil(t, err)
					require.NotNil(t, response)
					check(response)
				} else {
					stream, err := provider.TextCompletionStream(ctx, passthroughPostHook, nil, testKey(), req)
					require.Nil(t, err)
					sawUsage := false
					for _, chunk := range collectChunks(t, stream) {
						require.Nil(t, chunk.BifrostError)
						if response := chunk.BifrostTextCompletionResponse; response != nil && response.Usage != nil {
							sawUsage = true
							check(response)
						}
					}
					require.True(t, sawUsage)
				}
			})
		}
	}
}

func TestToOpenAITextCompletionRequest_FireworksUsesCacheIsolation(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	cacheKey := "cache-key-1"
	prompt := "A is for apple and B is for"
	extraParams := map[string]interface{}{
		"prompt_cache_key": cacheKey,
		"top_k":            4,
	}

	bifrostReq := &schemas.BifrostTextCompletionRequest{
		Provider: schemas.Fireworks,
		Model:    "accounts/fireworks/models/deepseek-v3p2",
		Input: &schemas.TextCompletionInput{
			PromptStr: &prompt,
		},
		Params: &schemas.TextCompletionParameters{
			ExtraParams: extraParams,
		},
	}

	result := ToOpenAITextCompletionRequest(bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.PromptCacheIsolationKey == nil || *result.PromptCacheIsolationKey != cacheKey {
		t.Fatalf("expected prompt_cache_isolation_key %q, got %v", cacheKey, result.PromptCacheIsolationKey)
	}
	if _, ok := result.ExtraParams["prompt_cache_key"]; ok {
		t.Fatalf("expected prompt_cache_key to be removed from extra params, got %#v", result.ExtraParams)
	}
	if _, ok := bifrostReq.Params.ExtraParams["prompt_cache_key"]; !ok {
		t.Fatalf("expected original extra params to remain unchanged, got %#v", bifrostReq.Params.ExtraParams)
	}

	wireBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToOpenAITextCompletionRequest(bifrostReq), nil
		},
	)
	if bifrostErr != nil {
		t.Fatalf("failed to build request body: %v", bifrostErr.Error.Message)
	}

	var jsonMap map[string]interface{}
	if err := sonic.Unmarshal(wireBody, &jsonMap); err != nil {
		t.Fatalf("failed to parse marshaled request body: %v", err)
	}

	if got, ok := jsonMap["prompt_cache_isolation_key"].(string); !ok || got != cacheKey {
		t.Fatalf("expected prompt_cache_isolation_key %q in wire payload, got %#v", cacheKey, jsonMap["prompt_cache_isolation_key"])
	}
	if _, ok := jsonMap["prompt_cache_key"]; ok {
		t.Fatalf("expected prompt_cache_key to be absent from wire payload, got %#v", jsonMap["prompt_cache_key"])
	}
	if got, ok := jsonMap["top_k"].(float64); !ok || got != 4 {
		t.Fatalf("expected top_k extra param to be preserved, got %#v", jsonMap["top_k"])
	}
}
