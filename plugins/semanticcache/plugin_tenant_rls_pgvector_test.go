package semanticcache

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

const (
	rlsPluginRole     = "bifrost_rls_app"
	rlsPluginPassword = "rls_app_pw"
)

// rlsPgVectorPlugin builds a plugin over a real pgvector store that connects as an ordinary role
// (not a superuser, which would skip row-level security) with row_level_security on. The embedding
// is the same for every prompt, so only the tenant walls can keep tenants apart. The test skips
// when no pgvector server is reachable. It returns the plugin, the store it uses, and an
// administrator store on the same namespace, for looking at the stored rows.
func rlsPgVectorPlugin(t *testing.T, configJSON string) (*Plugin, vectorstore.VectorStore, vectorstore.VectorStore) {
	t.Helper()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adminConfig := getPgVectorConfigFromEnv()
	admin, err := vectorstore.NewVectorStore(ctx, &vectorstore.Config{Type: vectorstore.VectorStoreTypePgVector, Config: adminConfig, Enabled: true}, logger)
	if err != nil {
		t.Skipf("pgvector not available: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background(), "") })
	if err := vectorstore.EnsureRestrictedRole(ctx, adminConfig, rlsPluginRole, rlsPluginPassword); err != nil {
		t.Fatalf("create the restricted role: %v", err)
	}

	appConfig := adminConfig
	appConfig.User = *schemas.NewSecretVar(rlsPluginRole)
	appConfig.Password = *schemas.NewSecretVar(rlsPluginPassword)
	appConfig.RowLevelSecurity = true
	store, err := vectorstore.NewVectorStore(ctx, &vectorstore.Config{Type: vectorstore.VectorStoreTypePgVector, Config: appConfig, Enabled: true}, logger)
	if err != nil {
		t.Fatalf("open the store as the restricted role: %v", err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background(), "") })

	namespace := "tenant_rls_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
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
	return plugin, store, admin
}

func TestTenantScopeFor(t *testing.T) {
	on := &Plugin{config: &Config{TenantIsolation: true}}
	off := &Plugin{config: &Config{}}
	a := map[string]interface{}{"vk": "vk-A"}

	if got := off.tenantScopeFor(a); got != "" {
		t.Fatalf("tenant_isolation off must give no scope, got %q", got)
	}
	if got := on.tenantScopeFor(nil); got != "" {
		t.Fatalf("identity scoping off (nil scope) must give no scope, got %q", got)
	}
	scopeA := on.tenantScopeFor(a)
	if scopeA == "" || scopeA != on.tenantScopeFor(map[string]interface{}{"vk": "vk-A"}) {
		t.Fatalf("the same identity must give the same non-empty scope, got %q", scopeA)
	}
	seen := map[string]string{}
	for name, scope := range map[string]map[string]interface{}{
		"vk-B": {"vk": "vk-B"}, "user": {"user": "vk-A"}, "both": {"vk": "vk-A", "user": "u"}, "anonymous": {},
	} {
		got := on.tenantScopeFor(scope)
		if got == scopeA {
			t.Fatalf("%s must not share vk-A's scope", name)
		}
		if other, dup := seen[got]; dup {
			t.Fatalf("%s and %s share a scope", name, other)
		}
		seen[got] = name
	}
	for _, raw := range []string{"vk-A", "vk", "user"} {
		if strings.Contains(scopeA, raw) {
			t.Fatalf("the scope %q must not contain the raw id %q", scopeA, raw)
		}
	}
	if on.tenantScopeFor(map[string]interface{}{}) != on.tenantScopeFor(map[string]interface{}{}) {
		t.Fatal("the anonymous scope must be one fixed value")
	}
}

func TestNewConfigFieldsDefaultOffAndParse(t *testing.T) {
	var config Config
	if err := json.Unmarshal([]byte(`{"dimension": 3}`), &config); err != nil {
		t.Fatal(err)
	}
	if config.TenantIsolation || config.CleanupInterval != 0 || config.CleanupBatchSize != 0 {
		t.Fatalf("the new options must default to off: %+v", config)
	}
	for _, c := range []struct {
		json string
		want time.Duration
	}{{`"90s"`, 90 * time.Second}, {`300`, 5 * time.Minute}, {`"1h"`, time.Hour}} {
		var parsed Config
		if err := json.Unmarshal([]byte(`{"dimension":3,"tenant_isolation":true,"cleanup_batch_size":50,"cleanup_interval":`+c.json+`}`), &parsed); err != nil {
			t.Fatalf("%s: %v", c.json, err)
		}
		if !parsed.TenantIsolation || parsed.CleanupBatchSize != 50 || parsed.CleanupInterval != c.want {
			t.Fatalf("%s: got %+v", c.json, parsed)
		}
	}
	for _, bad := range []string{`"-1m"`, `"soon"`, `true`} {
		var parsed Config
		if err := json.Unmarshal([]byte(`{"dimension":3,"cleanup_interval":`+bad+`}`), &parsed); err == nil {
			t.Fatalf("cleanup_interval %s must be rejected", bad)
		}
	}
}

func TestInitRejectsInconsistentTenantAndCleanupConfig(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	for name, config := range map[string]*Config{
		"tenant_isolation without identity scoping": {Dimension: 1, TenantIsolation: true, DisableIdentityScoping: true},
		"negative cleanup_interval":                 {Dimension: 1, CleanupInterval: -time.Second},
		"negative cleanup_batch_size":               {Dimension: 1, CleanupBatchSize: -1},
	} {
		if _, err := Init(context.Background(), config, logger, newObservableStore()); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// TestPgVectorRowLevelSecurityEndToEnd runs the plugin against PostgreSQL with row-level security
// as the second wall.
func TestPgVectorRowLevelSecurityEndToEnd(t *testing.T) {
	const prompt = "What is the secret launch date of project Falcon?"
	const paraphrase = "Tell me when project Falcon secretly launches."
	vkA, vkB, anonymous := identityRef{vk: "vk-A"}, identityRef{vk: "vk-B"}, identityRef{}

	t.Run("entries are stamped, served to their owner and hidden from everyone else", func(t *testing.T) {
		plugin, store, admin := rlsPgVectorPlugin(t, `{"tenant_isolation": true}`)
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))

		// Control: the owner gets a direct hit and a semantic hit.
		if !cacheHits(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil)) {
			t.Fatal("control failed: tenant A should hit its own entry")
		}
		semantic := identityContext(t, key, vkA)
		semantic.SetValue(CacheTypeKey, CacheTypeSemantic)
		if !cacheHits(t, plugin, semantic, identityTestChatRequest(paraphrase, nil)) {
			t.Fatal("control failed: tenant A should get a semantic hit on the paraphrase")
		}
		for name, who := range map[string]identityRef{"vk-B": vkB, "anonymous": anonymous} {
			ctx := identityContext(t, key, who)
			ctx.SetValue(CacheTypeKey, CacheTypeSemantic)
			if cacheHits(t, plugin, ctx, identityTestChatRequest(paraphrase, nil)) {
				t.Errorf("LEAK: %s was served tenant A's answer", name)
			}
		}

		// The stored row carries the hashed scope and never the raw identity.
		// The administrator store is a superuser, which sees rows regardless of the policy.
		rows, _, err := admin.GetAll(context.Background(), plugin.config.VectorStoreNamespace, nil, nil, nil, 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("expected one stored row, got %d (%v)", len(rows), err)
		}
		scope, _ := rows[0].Properties["tenant_scope"].(string)
		want := plugin.tenantScopeFor(map[string]interface{}{"vk": "vk-A"})
		if scope == "" || scope != want {
			t.Fatalf("stored tenant_scope = %q, want the hash %q", scope, want)
		}
		raw, _ := json.Marshal(rows[0].Properties)
		if strings.Contains(string(raw), "vk-A") {
			t.Fatalf("the raw virtual key id must not be stored: %s", raw)
		}

		// Without a tenant scope the application's own role sees nothing at all.
		none, _, err := store.GetAll(context.Background(), plugin.config.VectorStoreNamespace, nil, nil, nil, 10)
		if err != nil || len(none) != 0 {
			t.Fatalf("the app role with no tenant scope must see no rows, got %d (%v)", len(none), err)
		}
	})

	// ATTACK: the plugin's filters are bypassed. The store is called directly, as tenant B, with a
	// filter that selects tenant A's row by its exact stored values.
	t.Run("a call that skips the plugin's filters still cannot read another tenant's row", func(t *testing.T) {
		plugin, store, admin := rlsPgVectorPlugin(t, `{"tenant_isolation": true}`)
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))
		rows, _, err := admin.GetAll(context.Background(), plugin.config.VectorStoreNamespace, nil, nil, nil, 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("expected one stored row, got %d (%v)", len(rows), err)
		}
		aRow := rows[0].Properties
		filters := []vectorstore.Query{
			{Field: "cache_key", Operator: vectorstore.QueryOperatorEqual, Value: aRow["cache_key"]},
			{Field: "params_hash", Operator: vectorstore.QueryOperatorEqual, Value: aRow["params_hash"]},
			{Field: "tenant_scope", Operator: vectorstore.QueryOperatorEqual, Value: aRow["tenant_scope"]},
		}
		asB := vectorstore.WithTenantScope(context.Background(), plugin.tenantScopeFor(map[string]interface{}{"vk": "vk-B"}))
		hits, err := store.GetNearest(asB, plugin.config.VectorStoreNamespace, []float32{0.1, 0.2, 0.3}, filters, nil, -1, 10)
		if err != nil || len(hits) != 0 {
			t.Fatalf("LEAK: tenant B reached tenant A's row through the store: %d rows (%v)", len(hits), err)
		}
		if _, err := store.GetChunk(asB, plugin.config.VectorStoreNamespace, rows[0].ID); err == nil {
			t.Fatal("LEAK: tenant B fetched tenant A's row by id")
		}
	})

	t.Run("clear by key reaches every tenant, as it does without tenant_isolation", func(t *testing.T) {
		plugin, _, admin := rlsPgVectorPlugin(t, `{"tenant_isolation": true}`)
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))
		writeThroughCache(t, plugin, identityContext(t, key, vkB), identityTestChatRequest(prompt, nil))
		if err := plugin.ClearCacheForKey(key); err != nil {
			t.Fatal(err)
		}
		rows, _, err := admin.GetAll(context.Background(), plugin.config.VectorStoreNamespace, nil, nil, nil, 10)
		if err != nil || len(rows) != 0 {
			t.Fatalf("clear-by-key left %d rows (%v)", len(rows), err)
		}
	})

	t.Run("an expired entry is deleted by the lookup that finds it, for its own tenant", func(t *testing.T) {
		plugin, _, admin := rlsPgVectorPlugin(t, `{"tenant_isolation": true, "ttl": "1s"}`)
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))
		time.Sleep(2100 * time.Millisecond)
		if cacheHits(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil)) {
			t.Fatal("an expired entry must not be served")
		}
		plugin.WaitForPendingOperations()
		rows, _, err := admin.GetAll(context.Background(), plugin.config.VectorStoreNamespace, nil, nil, nil, 10)
		if err != nil || len(rows) != 0 {
			t.Fatalf("the lazy delete should have removed the expired row under row-level security, %d left (%v)", len(rows), err)
		}
	})

	t.Run("CleanupExpired removes expired entries of every tenant in bounded batches", func(t *testing.T) {
		plugin, store, admin := rlsPgVectorPlugin(t, `{"tenant_isolation": true, "cleanup_batch_size": 2}`)
		ns := plugin.config.VectorStoreNamespace
		past := time.Now().Add(-time.Minute).Unix()
		future := time.Now().Add(time.Hour).Unix()
		for i, who := range []string{"a", "b", "c", "d", "e"} {
			scope := plugin.tenantScopeFor(map[string]interface{}{"vk": "vk-" + who})
			ctx := vectorstore.WithTenantScope(context.Background(), scope)
			meta := func(expires int64) map[string]interface{} {
				return map[string]interface{}{"tenant_scope": scope, "expires_at": expires, "from_bifrost_semantic_cache_plugin": true}
			}
			if err := store.Add(ctx, ns, "old-"+who, []float32{0.1, 0.2, 0.3}, meta(past-int64(i))); err != nil {
				t.Fatal(err)
			}
			if err := store.Add(ctx, ns, "new-"+who, []float32{0.1, 0.2, 0.3}, meta(future)); err != nil {
				t.Fatal(err)
			}
		}
		deleted, err := plugin.CleanupExpired(context.Background())
		if err != nil || deleted != 5 {
			t.Fatalf("CleanupExpired deleted %d (%v), want the 5 expired rows", deleted, err)
		}
		rows, _, err := admin.GetAll(context.Background(), ns, nil, nil, nil, 50)
		if err != nil || len(rows) != 5 {
			t.Fatalf("expected the 5 live rows to remain, got %d (%v)", len(rows), err)
		}
		for _, row := range rows {
			if !strings.HasPrefix(row.ID, "new-") {
				t.Fatalf("an expired row survived: %s", row.ID)
			}
		}
	})
}

// cleanupPgVectorPlugin is a plugin on a plain (no row-level security) pgvector namespace.
func cleanupPgVectorPlugin(t *testing.T, configJSON string) (*Plugin, vectorstore.VectorStore) {
	t.Helper()
	plugin := pgvectorIsolationPlugin(t, configJSON)
	return plugin, plugin.store
}

func addCacheRow(t *testing.T, store vectorstore.VectorStore, namespace, id string, expiresAt int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	meta := map[string]interface{}{"from_bifrost_semantic_cache_plugin": true, "cache_key": "k", "response": "r"}
	if expiresAt != 0 {
		meta["expires_at"] = expiresAt
	}
	if err := store.Add(ctx, namespace, id, []float32{0.1, 0.2, 0.3}, meta); err != nil {
		t.Fatal(err)
	}
}

func rowIDs(t *testing.T, store vectorstore.VectorStore, namespace string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, _, err := store.GetAll(ctx, namespace, nil, nil, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// Expired rows that are never read again stay in PostgreSQL unless cleanup is switched on.
func TestPgVectorExpiredRowsStayWithoutCleanup(t *testing.T) {
	plugin, store := cleanupPgVectorPlugin(t, "")
	ns := plugin.config.VectorStoreNamespace
	addCacheRow(t, store, ns, "expired-never-read", time.Now().Add(-time.Hour).Unix())
	if n, err := plugin.CleanupExpired(context.Background()); err != nil || n != 1 {
		t.Fatalf("an explicit CleanupExpired call removes it: %d (%v)", n, err)
	}
	addCacheRow(t, store, ns, "expired-never-read-2", time.Now().Add(-time.Hour).Unix())
	time.Sleep(300 * time.Millisecond) // no background loop runs by default
	if ids := rowIDs(t, store, ns); len(ids) != 1 {
		t.Fatalf("without cleanup_interval nothing deletes the expired row, got %v", ids)
	}
}

func TestPgVectorCleanupExpiredBatchLimitAndLiveRows(t *testing.T) {
	plugin, store := cleanupPgVectorPlugin(t, `{"cleanup_batch_size": 3}`)
	ns := plugin.config.VectorStoreNamespace
	past := time.Now().Add(-time.Hour).Unix()
	for i := 0; i < 8; i++ {
		addCacheRow(t, store, ns, "expired-"+string(rune('a'+i)), past-int64(i))
	}
	addCacheRow(t, store, ns, "live", time.Now().Add(time.Hour).Unix())
	addCacheRow(t, store, ns, "no-expiry", 0)

	// Calling the store with the same batch size deletes exactly that many.
	remover := store.(vectorstore.ExpiredEntryRemover)
	n, err := remover.DeleteExpired(context.Background(), ns, time.Now(), 3)
	if err != nil || n != 3 {
		t.Fatalf("one batch must delete exactly the batch size, got %d (%v)", n, err)
	}
	// The plugin works off the rest and spares live rows.
	n, err = plugin.CleanupExpired(context.Background())
	if err != nil || n != 5 {
		t.Fatalf("CleanupExpired deleted %d (%v), want the remaining 5", n, err)
	}
	ids := rowIDs(t, store, ns)
	if len(ids) != 2 || ids[0] != "live" || ids[1] != "no-expiry" {
		t.Fatalf("live rows must remain, got %v", ids)
	}
}

func TestPgVectorBackgroundCleanupLoop(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	helper := pgvectorIsolationPlugin(t, "")
	store, ns := helper.store, helper.config.VectorStoreNamespace
	addCacheRow(t, store, ns, "expired", time.Now().Add(-time.Hour).Unix())
	addCacheRow(t, store, ns, "live", time.Now().Add(time.Hour).Unix())

	config := &Config{Dimension: 3, VectorStoreNamespace: ns, CleanupInterval: 100 * time.Millisecond}
	started, err := Init(context.Background(), config, logger, store)
	if err != nil {
		t.Fatal(err)
	}
	if config.CleanupBatchSize != defaultCleanupBatchSize {
		t.Fatalf("cleanup_batch_size should default to %d, got %d", defaultCleanupBatchSize, config.CleanupBatchSize)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ids := rowIDs(t, store, ns); len(ids) == 1 && ids[0] == "live" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background loop did not remove the expired row: %v", rowIDs(t, store, ns))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := started.Cleanup(); err != nil {
		t.Fatal(err)
	}
}
