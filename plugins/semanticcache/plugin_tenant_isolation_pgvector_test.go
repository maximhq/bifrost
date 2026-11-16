package semanticcache

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// pgvectorIsolationPlugin builds a plugin over a real PostgreSQL + pgvector
// store whose embedding is the same for every prompt, so any two prompts are
// a perfect semantic match (similarity 1.0). The only thing that can keep one
// tenant away from another tenant's entry is the filter the plugin sends to
// the database. The test skips when no pgvector server is reachable.
func pgvectorIsolationPlugin(t *testing.T, configJSON string) *Plugin {
	t.Helper()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Type:    vectorstore.VectorStoreTypePgVector,
		Config:  getPgVectorConfigFromEnv(),
		Enabled: true,
	}, logger)
	if err != nil {
		t.Skipf("pgvector not available: %v", err)
	}
	namespace := "tenant_iso_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.CreateNamespace(ctx, namespace, 3, VectorStoreProperties); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = store.DeleteNamespace(c, namespace)
	})

	plugin := identityScopeTestPlugin(t, store, configJSON)
	plugin.config.VectorStoreNamespace = namespace
	plugin.config.Dimension = 3
	plugin.config.Provider = schemas.OpenAI
	plugin.config.EmbeddingModel = "text-embedding-3-small"
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{
			Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.1, 0.2, 0.3}}}},
		}, nil
	})
	return plugin
}

// TestPgVectorTenantIsolation attacks the cache the way the reported leaks
// work: tenant B asks the very same question, and a paraphrase that is a
// perfect semantic match, after tenant A has cached the answer. Both must
// miss, on a real PostgreSQL + pgvector store.
func TestPgVectorTenantIsolation(t *testing.T) {
	const prompt = "What is the secret launch date of project Falcon?"
	const paraphrase = "Tell me when project Falcon secretly launches."
	vkA, vkB, anonymous := identityRef{vk: "vk-A"}, identityRef{vk: "vk-B"}, identityRef{}

	t.Run("another tenant is never served a match", func(t *testing.T) {
		plugin := pgvectorIsolationPlugin(t, "")
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))

		// Control: the paraphrase IS a semantic hit for the tenant that wrote
		// the entry, so a miss below can only come from isolation.
		semantic := identityContext(t, key, vkA)
		semantic.SetValue(CacheTypeKey, CacheTypeSemantic)
		if !cacheHits(t, plugin, semantic, identityTestChatRequest(paraphrase, nil)) {
			t.Fatal("control failed: tenant A should get a semantic hit on the paraphrase (similarity 1.0)")
		}

		for name, who := range map[string]identityRef{"vk-B": vkB, "anonymous": anonymous, "user-9": {user: "user-9"}} {
			for label, text := range map[string]string{"same question": prompt, "paraphrase": paraphrase} {
				ctx := identityContext(t, key, who)
				if cacheHits(t, plugin, ctx, identityTestChatRequest(text, nil)) {
					t.Errorf("LEAK: %s was served tenant A's answer for the %s", name, label)
				}
				ctx = identityContext(t, key, who)
				ctx.SetValue(CacheTypeKey, CacheTypeSemantic)
				if cacheHits(t, plugin, ctx, identityTestChatRequest(text, nil)) {
					t.Errorf("LEAK (semantic only): %s was served tenant A's answer for the %s", name, label)
				}
			}
		}
	})

	t.Run("a forged identity_scope in the request body cannot borrow another tenant's entry", func(t *testing.T) {
		plugin := pgvectorIsolationPlugin(t, "")
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))
		forged := identityTestChatRequest(prompt, map[string]interface{}{"identity_scope": map[string]interface{}{"vk": "vk-A"}})
		ctx := identityContext(t, key, vkB)
		ctx.SetValue(CacheTypeKey, CacheTypeSemantic)
		if cacheHits(t, plugin, ctx, forged) {
			t.Fatal("LEAK: a request body field named identity_scope claimed tenant A's entry")
		}
	})

	// The opt-out is what makes the leak possible, which shows the filter is
	// what protects the default: with scoping off, tenant B gets A's answer.
	t.Run("with identity scoping disabled the same attack succeeds", func(t *testing.T) {
		plugin := pgvectorIsolationPlugin(t, `{"disable_identity_scoping": true}`)
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))
		ctx := identityContext(t, key, vkB)
		ctx.SetValue(CacheTypeKey, CacheTypeSemantic)
		if !cacheHits(t, plugin, ctx, identityTestChatRequest(paraphrase, nil)) {
			t.Fatal("control failed: with disable_identity_scoping the paraphrase should match across tenants")
		}
	})
}
