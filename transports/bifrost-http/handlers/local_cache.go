package handlers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/localcache"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// namespaceEnsurer is the minimal contract the handler needs from the local
// cache plugin to react to a structural config change (VectorStoreNamespace
// or Dimension). Defined here so tests can stub it without spinning up a
// real vector store.
type namespaceEnsurer interface {
	EnsureNamespace(ctx context.Context) error
}

// LocalCacheHandler manages the dedicated config surface for the local
// cache plugin: a single-row table, GET/PUT semantics, and live mutation
// of the *configstore.LocalCacheConfig pointer the plugin reads from each
// request. The plugin enable/disable toggle lives on ClientConfig
// (EnableLocalCache) and is handled by the generic config compat-shim, not
// here.
type LocalCacheHandler struct {
	store         *lib.Config
	configManager ConfigManager
}

// NewLocalCacheHandler constructs the handler.
func NewLocalCacheHandler(configManager ConfigManager, store *lib.Config) *LocalCacheHandler {
	return &LocalCacheHandler{configManager: configManager, store: store}
}

// RegisterRoutes wires GET /api/local-cache/config and PUT
// /api/local-cache/config under the supplied middleware stack.
func (h *LocalCacheHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/local-cache/config", lib.ChainMiddlewares(h.getConfig, middlewares...))
	r.PUT("/api/local-cache/config", lib.ChainMiddlewares(h.updateConfig, middlewares...))
}

// getConfig returns the live LocalCacheConfig, falling back to the DB when
// the in-memory pointer hasn't been hydrated yet (e.g. enabled=false at
// boot, no plugin loaded).
func (h *LocalCacheHandler) getConfig(ctx *fasthttp.RequestCtx) {
	if h.store.LocalCacheConfig != nil {
		SendJSON(ctx, *h.store.LocalCacheConfig)
		return
	}
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}
	dbConfig, err := h.store.ConfigStore.GetLocalCacheConfig(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to fetch local cache config: %v", err))
		return
	}
	if dbConfig == nil {
		// Empty config — return zeros so the UI can render an unconfigured form.
		SendJSON(ctx, configstore.LocalCacheConfig{})
		return
	}
	SendJSON(ctx, *dbConfig)
}

// updateConfig validates and persists a new LocalCacheConfig, then mutates
// the shared in-memory pointer in place so the running plugin sees the new
// values on its next request without needing a Reload. Structural changes
// (VectorStoreNamespace, Dimension) trigger an EnsureNamespace call so the
// plugin can begin writing to the new namespace immediately.
func (h *LocalCacheHandler) updateConfig(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not initialized")
		return
	}
	var payload configstore.LocalCacheConfig
	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}
	if err := validateLocalCachePayload(&payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	// Capture the old structural fields so we can decide whether to call
	// EnsureNamespace on the running plugin.
	oldNamespace := ""
	oldDimension := 0
	if h.store.LocalCacheConfig != nil {
		oldNamespace = h.store.LocalCacheConfig.VectorStoreNamespace
		oldDimension = h.store.LocalCacheConfig.Dimension
	}

	// Refresh hash so config-sync sees this as the current state.
	if hash, hashErr := payload.GenerateLocalCacheConfigHash(); hashErr == nil {
		payload.ConfigHash = hash
	}
	if err := h.store.ConfigStore.UpdateLocalCacheConfig(ctx, &payload); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to persist local cache config: %v", err))
		return
	}
	// Mutate the shared pointer in place so the plugin sees the new values
	// on its next read. We don't reload the plugin — only the on/off toggle
	// (PUT /api/config with EnableLocalCache flip) does that.
	if err := h.configManager.ReloadLocalCacheConfigFromConfigStore(ctx); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload local cache config: %v", err))
		return
	}
	// Structural change — namespace or dimension differs from what the
	// plugin currently has materialized. Call EnsureNamespace so the next
	// request lands on a valid namespace. Old data is left in the previous
	// namespace by design (no flush on dimension change, per spec).
	if payload.VectorStoreNamespace != oldNamespace || payload.Dimension != oldDimension {
		if plugin, err := lib.FindPluginAs[namespaceEnsurer](h.store, localcache.PluginName); err == nil && plugin != nil {
			if ensureErr := plugin.EnsureNamespace(context.Background()); ensureErr != nil {
				logger.Warn("local cache config persisted but EnsureNamespace failed: %v", ensureErr)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("config saved but namespace setup failed: %v", ensureErr))
				return
			}
		}
		// Plugin not loaded (toggle off) — namespace will be created at
		// next ReloadPlugin via Init. No action needed here.
	}
	SendJSON(ctx, payload)
}

// validateLocalCachePayload mirrors the legacy validateLocalCacheConfig
// rules in lib/config.go but operates on the typed configstore struct so
// the dedicated REST surface enforces the same invariants as config.json.
func validateLocalCachePayload(p *configstore.LocalCacheConfig) error {
	if p.Dimension < 1 {
		return fmt.Errorf("local_cache 'dimension' must be >= 1, got %d", p.Dimension)
	}
	if p.Provider != "" {
		if p.Dimension <= 1 {
			return fmt.Errorf("local_cache 'dimension' must be > 1 when 'provider' is set; use dimension=1 for direct-only mode without a provider")
		}
		if p.EmbeddingModel == "" {
			return fmt.Errorf("local_cache 'embedding_model' is required when 'provider' is set")
		}
	}
	if p.TTL < 0 {
		return fmt.Errorf("local_cache 'ttl' must be non-negative")
	}
	if p.Threshold < 0 || p.Threshold > 1 {
		return fmt.Errorf("local_cache 'threshold' must be in [0,1]")
	}
	return nil
}
