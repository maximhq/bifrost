package semanticcache

import (
	"context"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// TestResolveCacheKey covers the tenant-scoping behavior: a shared cache_key or
// default_cache_key must never let two different virtual keys collide on the same cache
// bucket. Pure-logic test - no vector store needed, since resolveCacheKey only reads
// plugin.config and the request context.
func TestResolveCacheKey(t *testing.T) {
	plugin := &Plugin{config: &Config{DefaultCacheKey: "shared-default-bucket"}}

	newCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	t.Run("no header, no VK, falls back to default_cache_key unscoped", func(t *testing.T) {
		key, ok := plugin.resolveCacheKey(newCtx())
		if !ok || key != "shared-default-bucket" {
			t.Fatalf("got (%q, %v), want (\"shared-default-bucket\", true)", key, ok)
		}
	})

	t.Run("explicit header, no VK, unscoped", func(t *testing.T) {
		ctx := newCtx()
		ctx.SetValue(CacheKey, "tenant-x")
		key, ok := plugin.resolveCacheKey(ctx)
		if !ok || key != "tenant-x" {
			t.Fatalf("got (%q, %v), want (\"tenant-x\", true)", key, ok)
		}
	})

	t.Run("no cache key configured or supplied, caching skipped", func(t *testing.T) {
		bare := &Plugin{config: &Config{}}
		_, ok := bare.resolveCacheKey(newCtx())
		if ok {
			t.Fatal("expected caching to be skipped with no cache key available")
		}
	})

	t.Run("default_cache_key scoped by VK - two VKs never collide", func(t *testing.T) {
		ctxA := newCtx()
		ctxA.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-A")
		keyA, okA := plugin.resolveCacheKey(ctxA)

		ctxB := newCtx()
		ctxB.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-B")
		keyB, okB := plugin.resolveCacheKey(ctxB)

		if !okA || !okB {
			t.Fatalf("expected both to resolve: okA=%v okB=%v", okA, okB)
		}
		if keyA == keyB {
			t.Fatalf("two different virtual keys resolved to the same cache bucket: %q", keyA)
		}
		if keyA != "vk:vk-A:shared-default-bucket" {
			t.Errorf("got %q, want \"vk:vk-A:shared-default-bucket\"", keyA)
		}
	})

	t.Run("same VK, same header, same bucket - legitimate same-tenant caching still works", func(t *testing.T) {
		ctx1 := newCtx()
		ctx1.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-A")
		ctx1.SetValue(CacheKey, "session-1")
		key1, _ := plugin.resolveCacheKey(ctx1)

		ctx2 := newCtx()
		ctx2.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-A")
		ctx2.SetValue(CacheKey, "session-1")
		key2, _ := plugin.resolveCacheKey(ctx2)

		if key1 != key2 {
			t.Fatalf("identical VK + cache key should resolve to the same bucket: %q vs %q", key1, key2)
		}
	})

	t.Run("unscoped key shaped like a VK key never collides with a real VK bucket", func(t *testing.T) {
		ctxVK := newCtx()
		ctxVK.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-A")
		keyVK, _ := plugin.resolveCacheKey(ctxVK)

		forged := newCtx()
		forged.SetValue(CacheKey, "vk:vk-A:shared-default-bucket")
		keyForged, ok := plugin.resolveCacheKey(forged)
		if !ok {
			t.Fatal("expected the forged request to still resolve a cache key")
		}
		if keyForged == keyVK {
			t.Fatalf("a request with no virtual key resolved to a VK-scoped bucket: %q", keyForged)
		}
		if keyForged != "raw:vk:vk-A:shared-default-bucket" {
			t.Errorf("got %q, want %q", keyForged, "raw:vk:vk-A:shared-default-bucket")
		}
	})

	t.Run("empty VK ID treated as no VK", func(t *testing.T) {
		ctx := newCtx()
		ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "")
		key, ok := plugin.resolveCacheKey(ctx)
		if !ok || key != "shared-default-bucket" {
			t.Fatalf("got (%q, %v), want (\"shared-default-bucket\", true)", key, ok)
		}
	})
}

// TestResolveCacheThreshold covers the floor: a caller may raise the similarity bar but
// never lower it below the operator's configured value.
func TestResolveCacheThreshold(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	plugin := &Plugin{config: &Config{Threshold: 0.8}, logger: logger}

	newCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	tests := []struct {
		name      string
		override  any
		wantValue float64
	}{
		{"no override, uses configured threshold", nil, 0.8},
		{"override of 0 (defeat attempt) is floored at configured threshold", 0.0, 0.8},
		{"override below configured is floored at configured threshold", 0.5, 0.8},
		{"override above configured raises the bar", 0.95, 0.95},
		{"override exactly at configured threshold is a no-op", 0.8, 0.8},
		{"non-float override falls back to configured threshold", "not-a-float", 0.8},
		{"override above 1 is capped at 1", 1.7, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newCtx()
			if tt.override != nil {
				ctx.SetValue(CacheThresholdKey, tt.override)
			}
			got := plugin.resolveCacheThreshold(ctx)
			if got != tt.wantValue {
				t.Errorf("resolveCacheThreshold() = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

// TestResponseHasToolCalls_ShellAndApplyPatch pins that shell and apply_patch calls
// count as client-executed tool calls, so cache_tool_call_responses=false skips them.
func TestResponseHasToolCalls_ShellAndApplyPatch(t *testing.T) {
	for _, itemType := range []schemas.ResponsesMessageType{
		schemas.ResponsesMessageTypeShellCall,
		schemas.ResponsesMessageTypeApplyPatchCall,
	} {
		t.Run(string(itemType), func(t *testing.T) {
			item := schemas.ResponsesMessage{Type: schemas.Ptr(itemType)}
			if !responseHasToolCalls(&schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{item}}}) {
				t.Errorf("non-stream %s not treated as a tool call", itemType)
			}
			if !responseHasToolCalls(&schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{Item: &item}}) {
				t.Errorf("stream %s not treated as a tool call", itemType)
			}
		})
	}
}

// TestMain drops the shared test namespace BEFORE the run starts (in case a
// previous run was interrupted and left stale entries) AND once after — both
// matter: tests share one namespace + one cache_key prefix per t.Name(),
// so stale writes from a prior interrupted run would surface as spurious
// cache hits on the first request of the next run.
func TestMain(m *testing.M) {
	dropSharedTestNamespace() // pre-run sweep
	code := m.Run()
	dropSharedTestNamespace() // post-run sweep
	os.Exit(code)
}

// dropSharedTestNamespace removes the shared test namespace from EVERY vector
// store backend the suite exercises - not just Weaviate. Redis, Qdrant, and
// Pinecone are persistent external services, so a deterministic per-t.Name()
// cache_key written by one run is still present on the next run (within TTL)
// and surfaces as a spurious cache hit on the first request. Sweeping all
// backends here is the suite's only cleanup, since clearTestKeysWithStore is a
// no-op. Stores that aren't configured/reachable in this environment are
// skipped silently.
func dropSharedTestNamespace() {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	for _, tc := range getVectorStoreTestCases() {
		storeConfig, ok := storeConfigForType(tc.StoreType)
		if !ok {
			continue
		}
		func() {
			store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
				Type:    tc.StoreType,
				Config:  storeConfig,
				Enabled: true,
			}, logger)
			if err != nil {
				return // backend not configured/available in this environment
			}
			defer store.Close(context.Background(), SharedTestNamespace)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = store.DeleteNamespace(ctx, SharedTestNamespace)
		}()
	}
}
