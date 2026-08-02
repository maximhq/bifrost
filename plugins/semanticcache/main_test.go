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
