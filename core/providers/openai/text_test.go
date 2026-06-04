package openai

import (
	"testing"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

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
