# PRD: Per-Virtual-Key Semantic Cache Configuration

## Summary

Currently, the semantic cache plugin has a single global config (from `config.json` or from the `config_plugins` DB table). This makes per-tenant customization impossible — all virtual keys share the same embedding model, dimension, threshold, TTL, and namespace. This plan adds a new database table `governance_virtual_key_semantic_cache_configs` that stores per-VK semantic cache overrides. The semantic cache plugin will read the virtual key from `BifrostContext` (already set by the governance plugin's `HTTPTransportPreHook`) and, on every `PreLLMHook`/`PostLLMHook` invocation, resolve the effective config by merging global defaults with VK-specific overrides. The UI, governance handler API, and in-memory governance store all get extended to manage these configs, and the configuration flows through the existing `ConfigStore` / GORM persistence layer with proper change propagation.

---

## Implementation Steps

### Step 1 — Add Database Table: `governance_virtual_key_semantic_cache_configs`

**File: `framework/configstore/tables/virtualkey_semantic_cache.go`** (new)

Create a new GORM model:

```go
type TableVirtualKeySemanticCacheConfig struct {
    ID               uint      `gorm:"primaryKey;autoIncrement" json:"id"`
    VirtualKeyID     string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_vk_semantic_cache" json:"virtual_key_id"`
    Provider         string    `gorm:"type:varchar(50)" json:"provider,omitempty"`              // nil → inherit global
    EmbeddingModel   string    `gorm:"type:varchar(255)" json:"embedding_model,omitempty"`       // nil → inherit global
    Dimension        *int      `json:"dimension,omitempty"`                                      // nil → inherit global
    TTLSeconds       *int64    `json:"ttl_seconds,omitempty"`                                    // nil → inherit global; stored as seconds for DB
    Threshold        *float64  `json:"threshold,omitempty"`                                      // nil → inherit global
    VectorStoreNamespace *string `gorm:"type:varchar(255)" json:"vector_store_namespace,omitempty"` // nil → inherit global; each VK can use isolated namespace
    ConversationHistoryThreshold *int `json:"conversation_history_threshold,omitempty"`
    CacheByModel     *bool     `json:"cache_by_model,omitempty"`
    CacheByProvider  *bool     `json:"cache_by_provider,omitempty"`
    ExcludeSystemPrompt *bool  `json:"exclude_system_prompt,omitempty"`
    DefaultCacheKey  string    `gorm:"type:varchar(255)" json:"default_cache_key,omitempty"`
    CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
    UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`
}

func (TableVirtualKeySemanticCacheConfig) TableName() string {
    return "governance_virtual_key_semantic_cache_configs"
}
```

**Migration**: Add a single migration step in `framework/configstore/migrations.go` that creates this table with `AutoMigrate`. Attach a foreign key to `governance_virtual_keys(id)` for referential integrity.

**Design rationale for nullable fields**: Every field is nullable (pointer or `omitempty`). A `nil` value means "inherit from the global plugin config." This lets VKs override only the fields they care about (e.g., just change the threshold) while everything else falls through to the global defaults.

---

### Step 2 — Add ConfigStore CRUD Methods

**File: `framework/configstore/store.go`**

Add to the `ConfigStore` interface:

```go
GetVirtualKeySemanticCacheConfig(ctx context.Context, virtualKeyID string) (*tables.TableVirtualKeySemanticCacheConfig, error)
UpsertVirtualKeySemanticCacheConfig(ctx context.Context, config *tables.TableVirtualKeySemanticCacheConfig, tx ...*gorm.DB) error
DeleteVirtualKeySemanticCacheConfig(ctx context.Context, virtualKeyID string, tx ...*gorm.DB) error
```

**File: `framework/configstore/rdb.go`** — Implement these against the `RDBConfigStore`:
- `GetVirtualKeySemanticCacheConfig`: `FirstOrInit` / `Where("virtual_key_id = ?", vkID).First()`. Return `nil` (not error) when not found.
- `UpsertVirtualKeySemanticCacheConfig`: Use `clause.OnConflict` with `WHERE virtual_key_id = ?` to upsert.
- `DeleteVirtualKeySemanticCacheConfig`: `Where("virtual_key_id = ?", vkID).Delete()`

---

### Step 3 — Extend Governance In-Memory Store to Cache VK Semantic Configs

**File: `plugins/governance/store.go`**

Add a new `sync.Map` field to `LocalGovernanceStore`:

```go
vkSemanticCacheConfigs sync.Map // string (virtualKeyID) → *tables.TableVirtualKeySemanticCacheConfig
```

Add a public accessor method:

```go
func (gs *LocalGovernanceStore) GetVirtualKeySemanticCacheConfig(ctx context.Context, virtualKeyID string) (*tables.TableVirtualKeySemanticCacheConfig, bool) {
    v, ok := gs.vkSemanticCacheConfigs.Load(virtualKeyID)
    if !ok {
        return nil, false
    }
    return v.(*tables.TableVirtualKeySemanticCacheConfig), true
}
```

**Loading from database**: In `loadFromDatabase()` (around line 226-228 in `store.go`), add a query that loads ALL VK semantic cache configs and populates the `sync.Map`. This runs on startup.

**Loading from config memory**: In `loadFromConfigMemory()`, the config can be embedded inside the governance/virtual key structure in `config.json` (backward-compat), but the primary flow is DB-driven.

**Refresh on admin update**: The existing `UpsertVirtualKeySemanticCacheConfig` call (from the handler in Step 4) should also call `gs.vkSemanticCacheConfigs.Store(vkID, config)`. If a config row is deleted, call `gs.vkSemanticCacheConfigs.Delete(vkID)`.

---

### Step 4 — Add HTTP API Handlers for VK Semantic Cache Config

**File: `transports/bifrost-http/handlers/governance.go`**

Add new request/response types:

```go
type VirtualKeySemanticCacheConfigRequest struct {
    Provider                    *string  `json:"provider,omitempty"`
    EmbeddingModel              *string  `json:"embedding_model,omitempty"`
    Dimension                   *int     `json:"dimension,omitempty"`
    TTL                         *string  `json:"ttl,omitempty"` // duration string like "5m"
    Threshold                   *float64 `json:"threshold,omitempty"`
    VectorStoreNamespace        *string  `json:"vector_store_namespace,omitempty"`
    ConversationHistoryThreshold *int    `json:"conversation_history_threshold,omitempty"`
    CacheByModel                *bool    `json:"cache_by_model,omitempty"`
    CacheByProvider             *bool    `json:"cache_by_provider,omitempty"`
    ExcludeSystemPrompt         *bool    `json:"exclude_system_prompt,omitempty"`
    DefaultCacheKey             *string  `json:"default_cache_key,omitempty"`
}
```

Add CRUD handler methods on `GovernanceHandler`:

- **`PUT /v1/gateway/virtual-keys/{id}/semantic-cache`** — Upsert semantic cache config for a VK. Validates the VK exists, parses the TTL duration, populates `TableVirtualKeySemanticCacheConfig`, calls `configStore.UpsertVirtualKeySemanticCacheConfig`. Also updates the in-memory store's `sync.Map`.
- **`GET /v1/gateway/virtual-keys/{id}/semantic-cache`** — Retrieve the config. Return the merged effective config (global defaults overlaid with VK overrides) for UX convenience.
- **`DELETE /v1/gateway/virtual-keys/{id}/semantic-cache`** — Delete the VK-specific config, reverting to global defaults.

**File: `transports/bifrost-http/server/routes.go`** — Register the new routes.

**File: `transports/bifrost-http/handlers/governance.go`** — Extend the existing `CreateVirtualKey` handler to optionally include a `semantic_cache_config` field in the request body, so the config can be set at creation time.

---

### Step 5 — Refactor Semantic Cache Plugin for Per-VK Resolution

**File: `plugins/semanticcache/main.go`**

This is the core change. The `Plugin` struct needs a reference to the governance store (to look up VK configs) and the global vector store (to create per-VK namespaces).

**5a. Extend the `Plugin` struct:**

```go
type Plugin struct {
    store                    vectorstore.VectorStore
    config                   *Config                     // global defaults
    logger                   schemas.Logger
    embeddingRequestExecutor EmbeddingRequestExecutor
    
    // New fields:
    vkConfigResolver         VKConfigResolver            // interface to resolve per-VK config
    vkStores                 sync.Map                    // vkID → *perVKCacheState with its own store
    
    // ... existing fields ...
}
```

Define the resolver interface:

```go
// VKConfigResolver resolves semantic cache configuration for a given virtual key.
// Returns nil if no VK-specific config exists (use global defaults).
type VKConfigResolver interface {
    GetSemanticCacheConfig(ctx context.Context, virtualKeyID string) (*tables.TableVirtualKeySemanticCacheConfig, bool)
}
```

The governance store's `LocalGovernanceStore` already implements the accessor from Step 3, so it satisfies this interface.

**5b. Create a `resolvedConfig` helper:**

```go
// resolvedConfig merges VK-specific overrides with global defaults.
// Returns a complete, non-nil Config with all values filled in.
func (plugin *Plugin) resolvedConfig(ctx *schemas.BifrostContext) *Config {
    base := *plugin.config // shallow copy
    
    vkValue, ok := ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
    if !ok || vkValue == "" {
        return &base // no VK → use global defaults
    }
    
    // Look up the VK's semantic cache config
    vkID := plugin.resolveVKID(ctx, vkValue) // see 5c
    if vkID == "" {
        return &base
    }
    
    vkConfig, ok := plugin.vkConfigResolver.GetSemanticCacheConfig(ctx, vkID)
    if !ok || vkConfig == nil {
        return &base // no VK-specific config → use global defaults
    }
    
    // Override only what's set (non-nil fields)
    if vkConfig.Provider != "" {
        base.Provider = schemas.ModelProvider(vkConfig.Provider)
    }
    if vkConfig.EmbeddingModel != "" {
        base.EmbeddingModel = vkConfig.EmbeddingModel
    }
    if vkConfig.Dimension != nil {
        base.Dimension = *vkConfig.Dimension
    }
    if vkConfig.TTLSeconds != nil {
        base.TTL = time.Duration(*vkConfig.TTLSeconds) * time.Second
    }
    if vkConfig.Threshold != nil {
        base.Threshold = *vkConfig.Threshold
    }
    if vkConfig.VectorStoreNamespace != nil {
        base.VectorStoreNamespace = *vkConfig.VectorStoreNamespace
    }
    // ... same pattern for ConversationHistoryThreshold, CacheByModel, etc.
    
    return &base
}
```

**5c. Resolve VK ID from VK value:**

The `BifrostContext` has the VK *value* (e.g., `sk-bf-abc123`), but the config store lookup needs the VK *ID* (a UUID stored in the DB). We need to bridge this:

- **Option A** (recommended): The governance plugin already stores the VK ID on the context via `stampGovernanceCtxFromVK`. Extend that function to also set `BifrostContextKeyGovernanceVirtualKeyID` when it stamps the context in `PreRequestHook`. This is cleanest — the ID is available as a context value.
- Option B: The semantic cache plugin uses the governance store's `GetVirtualKey(vkValue)` to get the full `TableVirtualKey` (which has `.ID`). This adds a lookup on every request but is simpler.

**Recommendation: Option A.** Add one line to `stampGovernanceCtxFromVK`:
```go
ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, vk.ID)
```

Then in the semantic cache plugin:
```go
func (plugin *Plugin) resolveVKID(ctx *schemas.BifrostContext) string {
    if id, ok := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string); ok {
        return id
    }
    return ""
}
```

**5d. Modify `PreLLMHook` to use `resolvedConfig`:**

Every reference to `plugin.config.X` in `PreLLMHook` must be replaced with the resolved config. The cleanest way:

- At the top of `PreLLMHook`, compute: `cfg := plugin.resolvedConfig(ctx)`
- Pass `cfg` down to all internal methods (`performDirectSearch`, `performSemanticSearch`, `resolveCacheKey`, etc.) instead of them reading `plugin.config` directly.
- OR — less invasive — set a per-request context-scoped config. But the explicit `cfg` parameter is safer and testable.

**5e. Handle per-VK vector store namespaces:**

Each VK might have its own `vector_store_namespace`. The `resolvedConfig` already picks up the namespace. But namespaces need to be created (via `store.CreateNamespace`) on first use.

- Add a method `ensureVKNamespace(ctx, cfg)` that calls `CreateNamespace` if the namespace is different from the global one and hasn't been created yet. Track created namespaces in a `sync.Map`.
- Call this in `PreLLMHook` when a VK-specific namespace is resolved.

**5f. Architecture of the change flow:**

```
PreLLMHook called
  → resolve VK value from BifrostContext
  → resolve VK ID (from context, stamped by governance plugin)
  → resolve effective config (global defaults + VK overrides)
  → ensure VK-specific vector store namespace exists
  → use effective config for all operations:
      - canDoSemanticSearch check uses cfg.Provider, cfg.EmbeddingModel, cfg.Dimension
      - embedding generation uses cfg.Provider, cfg.EmbeddingModel
      - direct/semantic search uses cfg.VectorStoreNamespace, cfg.Threshold, cfg.CacheBy*, etc.
      - TTL uses cfg.TTL
```

---

### Step 6 — Handle Vector Store Per-VK

**Key design decision**: Each VK that configures its own embedding model/dimension gets its own vector store namespace. VKs that inherit the global config share the global namespace. This keeps isolation without needing multiple vector store *instances*.

**Problem**: The current architecture has a single `vectorstore.VectorStore` instance shared globally. Different dimensions require different collections/classes in the underlying store. The `VectorStore` interface uses `namespace` as the isolation boundary — each namespace already has its own dimension (set at `CreateNamespace` time).

So the flow is:
1. Global vector store instance handles all namespaces
2. Each VK with custom config gets its own namespace (via `VectorStoreNamespace` in config)
3. The plugin creates the namespace on first use with the correct dimension

**What if two VKs share the same embedding model/dimension but need different thresholds?** They can share a namespace (or use separate ones — it's configurable via `vector_store_namespace`).

---

### Step 7 — Update Plugin Initialization

**File: `transports/bifrost-http/server/plugins.go`**

Modify the semantic cache plugin initialization to pass the VK config resolver:

```go
case semanticcache.PluginName:
    semanticConfig, err := MarshalPluginConfig[semanticcache.Config](pluginConfig)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal semantic cache plugin config: %w", err)
    }
    return semanticcache.Init(ctx, semanticConfig, logger, bifrostConfig.VectorStore, bifrostConfig.GovernanceStore)
```

---

### Step 8 — Extend UI to Manage VK Semantic Cache Config

**File: `ui/app/workspace/virtual-keys/`** (modify)

Add a new tab or section to the virtual key detail/edit page for "Semantic Cache Configuration."

Fields to expose:
- Provider (dropdown, from existing providers list)
- Embedding Model (text input, freeform or dropdown)
- Dimension (number input)
- TTL (duration input, e.g., "5m", "1h")
- Threshold (slider or number, 0.0–1.0)
- Vector Store Namespace (text input)
- Conversation History Threshold (number)
- Cache By Model (toggle)
- Cache By Provider (toggle)
- Exclude System Prompt (toggle)
- Default Cache Key (text input)

All fields are optional — empty/unset means "inherit from global config."

**API calls:**
- On page load: `GET /v1/gateway/virtual-keys/{id}/semantic-cache`
- On save: `PUT /v1/gateway/virtual-keys/{id}/semantic-cache`
- On delete config: `DELETE /v1/gateway/virtual-keys/{id}/semantic-cache`

---

### Step 9 — Tests

**9a. Unit tests for config merging** — `plugin_semantic_cache_test.go`:
- `TestResolvedConfig_NoVK` — returns global defaults
- `TestResolvedConfig_VKWithFullOverride` — all fields overridden
- `TestResolvedConfig_VKWithPartialOverride` — only threshold and TTL overridden
- `TestResolvedConfig_VKDeletedConfig` — falls back to global

**9b. Integration tests for handler** — `governance_test.go`:
- `TestVirtualKeySemanticCacheConfig_CreateGetUpdateDelete`

**9c. E2E tests** — `tests/e2e/features/virtual-keys/` or existing VK specs:
- Update virtual key with semantic cache config
- Verify cache behavior changes after config update

---

### Step 10 — Documentation

- Update `docs/features/semantic-caching.mdx` to mention per-VK configuration
- Update `docs/features/governance/virtual-keys.mdx` to show the new `semantic_cache_config` field
- Add API documentation for the new endpoints in `docs/openapi/openapi.json`

---

## Edge Cases to Handle

1. **VK with no config**: Falls through to global defaults silently. No error, no log noise.

2. **VK config deleted while requests are in-flight**: `PreLLMHook` reads the config fresh on every request (from `sync.Map`), so a deleted config is immediately invisible.

3. **Dimension mismatch**: If a VK sets a different `dimension` than the existing namespace's dimension, `CreateNamespace` will fail. The `ensureVKNamespace` method should return a descriptive error and the plugin should fall back to the global defaults for that request, logging a warning.

4. **Embedding provider unavailable for VK**: If the VK specifies a custom embedding provider/model but the keys for that provider don't exist, the embedding generation will fail. This is already handled — the semantic cache plugin treats embedding failures as non-fatal (falls through to the upstream LLM call). But it should log which VK caused the failure for debugging.

5. **TTL stored as seconds**: JSON `time.Duration` is nanoseconds, which is awkward for config stores. Store as seconds (`int64`) in the DB, convert to `time.Duration` at the Go boundary.

6. **Concurrent VK config updates**: The `sync.Map` in the governance store handles this — `Store` is atomic. No locking needed.

7. **No governance plugin loaded**: If the governance plugin is not configured, `BifrostContextKeyVirtualKey` is never set, and `BifrostContextKeyGovernanceVirtualKeyID` is never set. The semantic cache plugin already handles "no VK in context" gracefully (returns global defaults).

8. **Changing a VK's embedding model**: This changes the vector dimension, which requires a new namespace. The plugin should use the new namespace value (or a derived one from VK ID + dimension hash) to isolate the new embeddings from old ones. The existing cache in the old namespace becomes orphaned — document this as expected behavior. A future enhancement could add a migration tool.

9. **Cross-VK cache isolation**: By default, VKs with different namespaces have fully isolated caches. VKs that share the same namespace (e.g., because they use identical configs) will share cache entries. This is by design — document it.

10. **Global config still required**: The per-VK config is always an *override* of the global config. If the global plugin is disabled, no per-VK config can enable it. The global `enabled` flag controls the plugin lifecycle.

---

## Open Questions

1. **Should the VK semantic cache config be stored as a JSON blob or individual columns?** I recommend individual columns (as proposed above) for queryability and type safety. A JSON blob would be simpler to add but would lose referential integrity and make future migrations harder.

2. **How should the `VKConfigResolver` interface be passed to the semantic cache plugin?** Two paths:
   - **A**: Pass the entire `*governance.LocalGovernanceStore` — tight coupling but uses an existing dependency
   - **B**: Define a narrow interface in the semantic cache package and have the governance store implement it — looser coupling
   
   I recommend **B** (narrow interface) for clean separation.

3. **Should the vector store be created per-namespace eagerly (on plugin init) or lazily (on first VK request)?** Lazy is better — VKs with custom configs may never receive traffic. The `ensureVKNamespace` method handles this.

4. **Should the global semantic cache config (from `config.json`/`config_plugins` table) remain as the "base config," or should it move entirely to a new system-level table?** Keep the global config as-is for backward compatibility. The VK overrides always layer on top. This is the least disruptive path.

5. **What about the `default_cache_key` field?** This is a VK-scoped concept by nature (each tenant has its own cache key). Move it to be primarily set via the VK config, with the global config as fallback.
