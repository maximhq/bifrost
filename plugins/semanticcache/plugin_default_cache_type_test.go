package semanticcache

import (
	"context"
	"encoding/json"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestDefaultCacheType_DirectOnly verifies that setting DefaultCacheType to "direct"
// skips semantic search and only uses exact hash matching.
func TestDefaultCacheType_DirectOnly(t *testing.T) {
	config := getDefaultTestConfig()
	ct := CacheTypeDirect
	config.DefaultCacheType = &ct

	setup := NewTestSetupWithConfig(t, config)
	defer setup.Cleanup()

	ctx1 := CreateContextWithCacheKey("test-default-cache-type-direct")
	testRequest := CreateBasicChatRequest("What is Bifrost?", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Exact same request should hit direct cache
	ctx2 := CreateContextWithCacheKey("test-default-cache-type-direct")
	t.Log("Making second identical request (should hit direct cache)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	// Similar but not identical request should miss (semantic search is off)
	similarRequest := CreateBasicChatRequest("Explain what Bifrost is", 0.7, 50)
	ctx3 := CreateContextWithCacheKey("test-default-cache-type-direct")
	t.Log("Making similar but different request (should miss without semantic search)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx3, similarRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3})
}

// TestDefaultCacheType_SemanticOnly verifies that setting DefaultCacheType to "semantic"
// skips direct hash matching and only uses semantic similarity search.
// Uses a low threshold (0.1) to guarantee semantic hits for closely related phrases.
func TestDefaultCacheType_SemanticOnly(t *testing.T) {
	config := getDefaultTestConfig()
	config.Threshold = 0.1
	ct := CacheTypeSemantic
	config.DefaultCacheType = &ct

	setup := NewTestSetupWithConfig(t, config)
	defer setup.Cleanup()

	ctx1 := CreateContextWithCacheKey("test-default-cache-type-semantic")
	testRequest := CreateBasicChatRequest("Explain machine learning concepts", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	similarRequest := CreateBasicChatRequest("Can you explain concepts in machine learning", 0.7, 50)
	ctx2 := CreateContextWithCacheKey("test-default-cache-type-semantic")
	t.Log("Making similar request (should hit semantic cache with low threshold)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, similarRequest)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "semantic")
}

// TestDefaultCacheType_Unset verifies that when DefaultCacheType is nil (unset),
// both direct and semantic search are performed (the default behavior).
func TestDefaultCacheType_Unset(t *testing.T) {
	config := getDefaultTestConfig()
	// DefaultCacheType intentionally left nil

	setup := NewTestSetupWithConfig(t, config)
	defer setup.Cleanup()

	ctx1 := CreateContextWithCacheKey("test-default-cache-type-unset")
	testRequest := CreateBasicChatRequest("Define artificial intelligence", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Exact match should hit direct cache
	ctx2 := CreateContextWithCacheKey("test-default-cache-type-unset")
	t.Log("Making identical request (should hit direct cache)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")
}

// TestDefaultCacheType_PerRequestOverridesConfig verifies that a per-request
// CacheTypeKey overrides the config-level DefaultCacheType.
// Uses a low threshold (0.1) to guarantee the semantic hit on override.
func TestDefaultCacheType_PerRequestOverridesConfig(t *testing.T) {
	config := getDefaultTestConfig()
	config.Threshold = 0.1
	ct := CacheTypeDirect
	config.DefaultCacheType = &ct

	setup := NewTestSetupWithConfig(t, config)
	defer setup.Cleanup()

	ctx1 := CreateContextWithCacheKey("test-default-cache-type-override")
	testRequest := CreateBasicChatRequest("Explain machine learning concepts", 0.7, 50)

	t.Log("Making first request to populate cache (config default is direct)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Per-request override to semantic — should attempt semantic search despite config
	similarRequest := CreateBasicChatRequest("Can you explain concepts in machine learning", 0.7, 50)
	ctx2 := CreateContextWithCacheKeyAndType("test-default-cache-type-override", CacheTypeSemantic)
	t.Log("Making similar request with per-request CacheTypeKey=semantic (overrides config default of direct)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, similarRequest)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "semantic")
}

// TestDefaultCacheType_InvalidPerRequestValue verifies that an invalid per-request
// CacheTypeKey falls back to the config default rather than silently disabling both search types.
func TestDefaultCacheType_InvalidPerRequestValue(t *testing.T) {
	config := getDefaultTestConfig()

	setup := NewTestSetupWithConfig(t, config)
	defer setup.Cleanup()

	ctx1 := CreateContextWithCacheKey("test-default-cache-type-invalid-per-req")
	testRequest := CreateBasicChatRequest("What is Bifrost?", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Set an invalid cache type — should fall back to config default (nil = both)
	ctx2 := CreateContextWithCacheKey("test-default-cache-type-invalid-per-req")
	ctx2.SetValue(CacheTypeKey, CacheType("bogus"))

	t.Log("Making identical request with invalid per-request CacheTypeKey (should still hit direct cache)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")
}

// TestDefaultCacheType_InvalidValueRejected verifies that Init rejects
// invalid DefaultCacheType values with a clear error.
func TestDefaultCacheType_InvalidValueRejected(t *testing.T) {
	config := getDefaultTestConfig()
	invalid := CacheType("invalid")
	config.DefaultCacheType = &invalid

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)

	store := &MockUnsupportedStore{}

	_, err := Init(ctx, config, logger, store)
	if err == nil {
		t.Fatal("Expected Init to return error for invalid DefaultCacheType")
	}

	t.Logf("Init correctly rejected invalid DefaultCacheType: %v", err)
}

// TestDefaultCacheType_JSONUnmarshal verifies that DefaultCacheType deserializes
// correctly from JSON config.
func TestDefaultCacheType_JSONUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected *CacheType
	}{
		{
			name:     "direct",
			json:     `{"dimension": 1536, "default_cache_type": "direct"}`,
			expected: bifrost.Ptr(CacheTypeDirect),
		},
		{
			name:     "semantic",
			json:     `{"dimension": 1536, "default_cache_type": "semantic"}`,
			expected: bifrost.Ptr(CacheTypeSemantic),
		},
		{
			name:     "omitted",
			json:     `{"dimension": 1536}`,
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var config Config
			if err := json.Unmarshal([]byte(tc.json), &config); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			if tc.expected == nil {
				if config.DefaultCacheType != nil {
					t.Errorf("Expected nil DefaultCacheType, got %q", *config.DefaultCacheType)
				}
			} else {
				if config.DefaultCacheType == nil {
					t.Fatalf("Expected DefaultCacheType %q, got nil", *tc.expected)
				}
				if *config.DefaultCacheType != *tc.expected {
					t.Errorf("Expected DefaultCacheType %q, got %q", *tc.expected, *config.DefaultCacheType)
				}
			}
		})
	}
}
