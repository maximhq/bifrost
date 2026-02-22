package semanticcache

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestDefaultCacheKey_CachesWithoutPerRequestKey verifies that when DefaultCacheKey
// is configured, requests without an explicit cache key are cached automatically.
func TestDefaultCacheKey_CachesWithoutPerRequestKey(t *testing.T) {
	config := getDefaultTestConfig()
	config.DefaultCacheKey = "test-default-key"

	setup := NewTestSetupWithConfig(t, config)
	defer setup.Cleanup()

	// Context with NO per-request cache key
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	testRequest := CreateBasicChatRequest("What is Bifrost? Answer in one short sentence.", 0.7, 50)

	t.Log("Making first request without per-request cache key (should use default and be cached)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}

	if response1 == nil || len(response1.Choices) == 0 || response1.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("First response is invalid")
	}

	// First request should NOT be a cache hit
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	t.Log("Making second identical request without per-request cache key (should hit cache)...")
	ctx2 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		}
		t.Fatalf("Second request failed: %v", err2)
	}

	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, string(CacheTypeDirect))
	t.Log("Default cache key correctly enabled caching without per-request key")
}

// TestDefaultCacheKey_PerRequestKeyOverridesDefault verifies that an explicit
// per-request cache key takes precedence over the configured default.
func TestDefaultCacheKey_PerRequestKeyOverridesDefault(t *testing.T) {
	config := getDefaultTestConfig()
	config.DefaultCacheKey = "test-default-key"

	setup := NewTestSetupWithConfig(t, config)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("What is the capital of France?", 0.5, 50)

	// Cache with the default key (no per-request key)
	ctx1 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}

	WaitForCache()

	// Verify the cache was actually populated with the default key
	ctxDefault2 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	responseDefault2, errDefault2 := setup.Client.ChatCompletionRequest(ctxDefault2, testRequest)
	if errDefault2 != nil {
		if errDefault2.Error != nil {
			t.Fatalf("Default-key verification request failed: %v", errDefault2.Error.Message)
		}
		t.Fatalf("Default-key verification request failed: %v", errDefault2)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: responseDefault2}, string(CacheTypeDirect))

	// Same request but with a DIFFERENT per-request key — should miss
	ctx2 := CreateContextWithCacheKey("override-key")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		}
		t.Fatalf("Second request failed: %v", err2)
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2})
	t.Log("Per-request cache key correctly overrides default (different namespace = cache miss)")
}

// TestDefaultCacheKey_EmptyDefault_NoCaching verifies that when DefaultCacheKey
// is empty (default zero value), requests without a per-request key bypass caching.
func TestDefaultCacheKey_EmptyDefault_NoCaching(t *testing.T) {
	config := getDefaultTestConfig()
	// DefaultCacheKey is intentionally left empty (zero value)

	setup := NewTestSetupWithConfig(t, config)
	defer setup.Cleanup()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	testRequest := CreateBasicChatRequest("What is deep learning", 0.7, 50)

	t.Log("Making first request without any cache key and no default (should not cache)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	t.Log("Making second identical request (should still not cache)...")
	ctx2 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		}
		t.Fatalf("Second request failed: %v", err2)
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2})
	t.Log("Empty default cache key correctly preserves opt-in behavior")
}
