package semanticcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/maximhq/bifrost/framework/vectorstore"
)

// tenantScopePrefix separates this hash from any other SHA-256 of the same bytes. It is a label,
// not a secret.
const tenantScopePrefix = "bifrost-semantic-cache-tenant-scope/v1\x00"

const (
	// defaultCleanupBatchSize is how many expired entries one cleanup call deletes.
	defaultCleanupBatchSize = 1000
	// maxCleanupBatchesPerRun bounds one cleanup run, so a huge backlog is worked off over several
	// intervals rather than in one long burst.
	maxCleanupBatchesPerRun = 50
	// cleanupBatchTimeout bounds each batch.
	cleanupBatchTimeout = 30 * time.Second
)

// tenantScopeFor turns the identity scope (see resolveIdentityScope) into the opaque value that
// stores use to keep tenants apart. It is "" unless tenant_isolation is on.
//
// The value is a SHA-256 of the identity scope, shortened to 128 bits: it cannot be turned back
// into the virtual key or user id by reading it, and the same identity always gets the same value,
// so a store can compare it. It is not a secret-keyed hash, so someone who already holds a short,
// guessable id could hash it and compare. A request with no identity gets the hash of the empty scope, one fixed
// value shared by all anonymous requests (which already share entries today).
func (plugin *Plugin) tenantScopeFor(identityScope map[string]interface{}) string {
	if !plugin.config.TenantIsolation || identityScope == nil {
		return ""
	}
	// json.Marshal writes map keys in sorted order, so the bytes are stable.
	encoded, err := json.Marshal(identityScope)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(tenantScopePrefix), encoded...))
	return hex.EncodeToString(sum[:16])
}

// withTenantScope returns ctx carrying the tenant scope for the store. An empty scope (the default,
// tenant_isolation off) returns ctx untouched.
func (plugin *Plugin) withTenantScope(ctx context.Context, scope string) context.Context {
	if scope == "" {
		return ctx
	}
	return vectorstore.WithTenantScope(ctx, scope)
}

// withTenantAdmin marks a call as a deliberate cross-tenant operation (clear by key, clear by id,
// expiry cleanup). Without tenant_isolation it returns ctx untouched.
func (plugin *Plugin) withTenantAdmin(ctx context.Context) context.Context {
	if !plugin.config.TenantIsolation {
		return ctx
	}
	return vectorstore.WithTenantAdmin(ctx)
}

// startExpiredEntryCleanup starts the background cleanup when cleanup_interval is set and the
// store can delete expired entries.
func (plugin *Plugin) startExpiredEntryCleanup() {
	if plugin.config.CleanupInterval <= 0 {
		return
	}
	if _, ok := plugin.store.(vectorstore.ExpiredEntryRemover); !ok {
		plugin.logger.Warn("cleanup_interval is set, but the vector store cannot delete expired entries in bulk: expired entries are only deleted when they are read")
		return
	}
	if plugin.config.CleanupBatchSize == 0 {
		plugin.config.CleanupBatchSize = defaultCleanupBatchSize
	}
	plugin.cleanupWg.Add(1)
	go plugin.runExpiredEntryCleanupLoop()
}

// runExpiredEntryCleanupLoop deletes expired entries every cleanup_interval until stopCh closes.
func (plugin *Plugin) runExpiredEntryCleanupLoop() {
	defer plugin.cleanupWg.Done()
	ticker := time.NewTicker(plugin.config.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-plugin.stopCh:
			return
		case <-ticker.C:
			deleted, err := plugin.CleanupExpired(context.Background())
			if err != nil {
				plugin.logger.Warn("Failed to delete expired cache entries: %v", err)
			} else if deleted > 0 {
				plugin.logger.Debug("Deleted %d expired cache entries", deleted)
			}
		}
	}
}

// CleanupExpired deletes entries whose expires_at has passed, in batches of cleanup_batch_size,
// and reports how many it deleted. One call stops after at most maxCleanupBatchesPerRun batches,
// or when a batch comes back short, so it never runs unbounded; call it again for the rest.
// It works on any store that implements vectorstore.ExpiredEntryRemover (pgvector does), whether
// or not cleanup_interval is set.
func (plugin *Plugin) CleanupExpired(ctx context.Context) (int, error) {
	remover, ok := plugin.store.(vectorstore.ExpiredEntryRemover)
	if !ok {
		return 0, nil
	}
	batch := plugin.config.CleanupBatchSize
	if batch <= 0 {
		batch = defaultCleanupBatchSize
	}
	ctx = plugin.withTenantAdmin(ctx)
	total := 0
	for i := 0; i < maxCleanupBatchesPerRun; i++ {
		select {
		case <-plugin.stopCh:
			return total, nil
		default:
		}
		batchCtx, cancel := context.WithTimeout(ctx, cleanupBatchTimeout)
		deleted, err := remover.DeleteExpired(batchCtx, plugin.config.VectorStoreNamespace, time.Now(), batch)
		cancel()
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < batch {
			break
		}
	}
	return total, nil
}
