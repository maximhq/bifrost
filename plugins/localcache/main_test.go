package localcache

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

func dropSharedTestNamespace() {
	cfg := getWeaviateConfigFromEnv()
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Type:    vectorstore.VectorStoreTypeWeaviate,
		Config:  cfg,
		Enabled: true,
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = store.DeleteNamespace(ctx, SharedTestNamespace)
}
