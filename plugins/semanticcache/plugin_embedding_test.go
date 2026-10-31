package semanticcache

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestEmbeddingRequestsCaching tests that embedding requests are properly cached using direct hash matching
func TestEmbeddingRequestsCaching(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-embedding-cache")

	// Create embedding request
	embeddingRequest := CreateEmbeddingRequest([]string{
		"What is machine learning?",
		"Explain artificial intelligence in simple terms.",
	})

	t.Log("Making first embedding request (should go to OpenAI and be cached)...")

	// Make first request (will go to OpenAI and be cached) - with retries
	start1 := time.Now()
	response1, err1 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	duration1 := time.Since(start1)

	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}

	if response1 == nil || len(response1.Data) == 0 {
		t.Fatal("First embedding response is invalid")
	}

	t.Logf("First embedding request completed in %v", duration1)
	t.Logf("Response contains %d embeddings", len(response1.Data))

	// Wait for cache to be written
	WaitForCache(setup.Plugin)

	t.Log("Making second identical embedding request (should be served from cache)...")

	// Make second identical request (should be cached)
	start2 := time.Now()
	response2, err2 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		t.Fatalf("Second embedding request failed: %v", err2)
	}

	if response2 == nil || len(response2.Data) == 0 {
		t.Fatal("Second embedding response is invalid")
	}

	// Verify cache hit
	AssertCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2}, "direct")

	t.Logf("Second embedding request completed in %v", duration2)

	// Cache should be significantly faster
	if duration2 >= duration1 { // Allow some margin but cache should be much faster
		t.Log("⚠️  Cache doesn't seem faster, but this could be due to test environment")
	}

	// Responses should be identical
	if len(response1.Data) != len(response2.Data) {
		t.Errorf("Response lengths differ: %d vs %d", len(response1.Data), len(response2.Data))
	}

	t.Log("✅ Embedding requests properly cached using direct hash matching")
}

// TestEmbeddingRequestsNoCacheWithoutCacheKey tests that embedding requests without cache key are not cached
func TestEmbeddingRequestsNoCacheWithoutCacheKey(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Don't set cache key in context. CreateContextWithCacheKey(t, "") would
	// still populate CacheKey from t.Name() and turn this into a keyed
	// request — using a base context keeps CacheKey unset so we exercise
	// the cache-disabled path.
	ctx := newBaseTestContext()

	embeddingRequest := CreateEmbeddingRequest([]string{"Test embedding without cache key"})

	t.Log("Making first embedding request without cache key...")
	response1, err := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	if err != nil {
		t.Fatalf("Embedding request failed: %v", err)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

	WaitForCache(setup.Plugin)

	// Real check: a second identical request must ALSO miss. If the cache
	// silently keyed off something else (e.g. a default key), this would
	// surface as a hit and fail the assertion.
	t.Log("Making second identical request — must also miss because nothing was cached...")
	ctx2 := newBaseTestContext()
	response2, err := setup.Client.EmbeddingRequest(ctx2, embeddingRequest)
	if err != nil {
		t.Fatalf("Second embedding request failed: %v", err)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2})

	t.Log("✅ Embedding requests without cache key are properly not cached")
}

// TestEmbeddingRequestsDifferentTexts tests that different embedding texts produce different cache entries
func TestEmbeddingRequestsDifferentTexts(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-embedding-different")

	// Create two different embedding requests
	request1 := CreateEmbeddingRequest([]string{"First set of texts"})
	request2 := CreateEmbeddingRequest([]string{"Second set of texts"})

	t.Log("Making first embedding request...")
	response1, err1 := setup.Client.EmbeddingRequest(ctx, request1)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

	WaitForCache(setup.Plugin)

	t.Log("Making second different embedding request...")
	response2, err2 := setup.Client.EmbeddingRequest(ctx, request2)
	if err2 != nil {
		t.Skipf("upstream request error, skipping test: %v", err2)
	}
	// Should not be a cache hit since texts are different
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2})

	t.Log("✅ Different embedding texts produce different cache entries")
}

// TestEmbeddingRequestsCacheExpiration tests TTL functionality for embedding requests
func TestEmbeddingRequestsCacheExpiration(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Set very short TTL for testing
	shortTTL := 5 * time.Second
	ctx := CreateContextWithCacheKeyAndTTL(t, "test-embedding-ttl", shortTTL)

	embeddingRequest := CreateEmbeddingRequest([]string{"TTL test embedding"})

	t.Log("Making first embedding request with short TTL...")
	response1, err1 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

	WaitForCache(setup.Plugin)

	t.Log("Making second request before TTL expiration...")
	response2, err2 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2}, "direct")

	t.Logf("Waiting for TTL expiration (%v)...", shortTTL)
	// expires_at is stored at second-precision Unix(); a 1s buffer can land
	// on the same boundary as the entry's expiry under load. 2s is the
	// minimum margin that's robust to seconds-level rounding + a slow CI.
	time.Sleep(shortTTL + 2*time.Second)

	t.Log("Making third request after TTL expiration...")
	response3, err3 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	if err3 != nil {
		t.Skipf("upstream request error, skipping test: %v", err3)
	}
	// Should not be a cache hit since TTL expired
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response3})

	t.Log("✅ Embedding requests properly handle TTL expiration")
}

// task_type / title / auto_truncate change the vector a provider returns, so they must
// be part of the params hash; otherwise a RETRIEVAL_QUERY request hits a cached
// RETRIEVAL_DOCUMENT vector.
func TestEmbeddingParamsHashIncludesTaskTypeTitleAutoTruncate(t *testing.T) {
	plugin := &Plugin{config: getDefaultTestConfig()}
	text := "hello world"
	hashFor := func(params *schemas.EmbeddingParameters) string {
		t.Helper()
		req := &schemas.BifrostRequest{
			RequestType: schemas.EmbeddingRequest,
			EmbeddingRequest: &schemas.BifrostEmbeddingRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-embedding-001",
				Input:    []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
				Params:   params,
			},
		}
		metadata, err := plugin.buildRequestMetadataForCaching(plugin.createCacheState("req"), req)
		if err != nil {
			t.Fatalf("buildRequestMetadataForCaching failed: %v", err)
		}
		hash, err := hashMap(metadata)
		if err != nil {
			t.Fatalf("hashMap failed: %v", err)
		}
		return hash
	}

	query := hashFor(&schemas.EmbeddingParameters{TaskType: schemas.Ptr("RETRIEVAL_QUERY")})
	if query != hashFor(&schemas.EmbeddingParameters{TaskType: schemas.Ptr("RETRIEVAL_QUERY")}) {
		t.Fatal("identical params must hash the same")
	}
	variants := map[string]*schemas.EmbeddingParameters{
		"task_type":     {TaskType: schemas.Ptr("RETRIEVAL_DOCUMENT")},
		"title":         {TaskType: schemas.Ptr("RETRIEVAL_QUERY"), Title: schemas.Ptr("t")},
		"auto_truncate": {TaskType: schemas.Ptr("RETRIEVAL_QUERY"), AutoTruncate: schemas.Ptr(false)},
	}
	for name, params := range variants {
		if hashFor(params) == query {
			t.Errorf("%s change must change the params hash", name)
		}
	}
}
