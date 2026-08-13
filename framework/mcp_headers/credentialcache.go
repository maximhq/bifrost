package mcp_headers

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/lrucache"
)

// defaultCredentialCacheCapacity bounds the per-user header credential cache.
// Session-mode identities are caller-asserted strings, so the cache must stay
// bounded no matter what identities callers present; least-recently-used
// entries are dropped once the capacity is reached.
const defaultCredentialCacheCapacity = 4096

// cachedHeaderCredential is the value cached per (auth mode, identity,
// mcp client) binding: the row ID (for targeted eviction) and the parsed
// credential. Header credentials carry no expiry and have no refresh
// machinery, so there is no expiry-as-miss logic; explicit eviction is the
// only way a cached entry stops being served. The credential pointer is
// private to the cache: the provider hands callers a deep copy so a caller
// mutating the returned Headers map can never corrupt the cached value.
type cachedHeaderCredential struct {
	credentialID string
	credential   *schemas.MCPHeadersUserCredential
}

// headerCredentialCache adapts lrucache.Cache to per-user MCP header
// credential lookups: it owns the binding-key scheme, registers each entry
// under its credential row ID for targeted eviction (upsert, delete,
// revoke), and provides the scoped bulk evictions the credential lifecycle
// needs (by MCP client, virtual key, and user). No validator is installed:
// header credentials never expire, so explicit eviction is the only
// invalidation.
type headerCredentialCache struct {
	cache *lrucache.Cache[cachedHeaderCredential]
}

// headerCredentialCacheKey builds the cache key for a (mode, identity,
// mcp client) binding. identity is a caller-asserted string with no
// charset restriction, so this goes through lrucache.EncodeKey rather than
// a plain separator join — see its doc comment for why a naive join lets
// an identity value forge a component boundary and alias two distinct
// bindings onto one cache entry. Admin-mode bindings carry an empty
// identity component by design.
func headerCredentialCacheKey(mode schemas.MCPAuthMode, identity, mcpClientID string) string {
	return lrucache.EncodeKey(string(mode), identity, mcpClientID)
}

// splitHeaderCredentialCacheKey is headerCredentialCacheKey's inverse, for
// the scoped eviction predicates.
func splitHeaderCredentialCacheKey(key string) (mode, identity, clientID string, ok bool) {
	parts, ok := lrucache.DecodeKey(key, 3)
	if !ok {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func newHeaderCredentialCache(capacity int) *headerCredentialCache {
	if capacity <= 0 {
		capacity = defaultCredentialCacheCapacity
	}
	return &headerCredentialCache{cache: lrucache.New[cachedHeaderCredential](capacity)}
}

// Get returns the cached value for key. Header credentials have no expiry,
// so a hit is served as-is until an eviction removes it.
func (c *headerCredentialCache) Get(key string) (cachedHeaderCredential, bool) {
	if c == nil {
		return cachedHeaderCredential{}, false
	}
	return c.cache.Get(key)
}

// Fill runs fill for key with single-flight deduplication: concurrent
// callers for the same key wait for one leader and share its result and
// error, so a tool-call burst for one identity performs a single database
// read instead of a stampede. A successful result is cached under both the
// binding key and its credential row ID; errors are propagated but never
// cached.
func (c *headerCredentialCache) Fill(ctx context.Context, key string, fill func() (cachedHeaderCredential, error)) (cachedHeaderCredential, error) {
	if c == nil {
		return fill()
	}
	return c.cache.Fill(ctx, key, func() (cachedHeaderCredential, string, error) {
		value, err := fill()
		return value, value.credentialID, err
	})
}

// Evict removes the entry for an exact binding key, if present.
func (c *headerCredentialCache) Evict(key string) {
	if c == nil {
		return
	}
	c.cache.Evict(key)
}

// EvictByCredentialID removes the entry holding the given credential row ID,
// if any.
func (c *headerCredentialCache) EvictByCredentialID(credentialID string) {
	if c == nil {
		return
	}
	c.cache.EvictByIndex(credentialID)
}

// EvictByMCPClient removes every cached entry bound to the given MCP client,
// across all auth modes and identities, including the admin-mode binding
// (whose identity component is empty). Used when a client-level change
// invalidates its credential rows as a set, such as a header schema change
// or client deletion. A linear sweep is fine here: these are rare admin
// operations and the cache is bounded.
func (c *headerCredentialCache) EvictByMCPClient(mcpClientID string) {
	if c == nil || mcpClientID == "" {
		return
	}
	c.cache.EvictWhere(func(key string) bool {
		_, _, clientID, ok := splitHeaderCredentialCacheKey(key)
		return ok && clientID == mcpClientID
	})
}

// EvictByVirtualKey removes every cached vk-mode entry bound to the given
// virtual key, across all MCP clients. Used when a virtual key change
// orphans or deletes its credential rows as a set.
func (c *headerCredentialCache) EvictByVirtualKey(virtualKeyID string) {
	if c == nil || virtualKeyID == "" {
		return
	}
	c.cache.EvictWhere(func(key string) bool {
		mode, identity, _, ok := splitHeaderCredentialCacheKey(key)
		return ok && mode == string(schemas.MCPAuthModeVK) && identity == virtualKeyID
	})
}

// EvictByUser removes every cached user-mode entry bound to the given user,
// across all MCP clients. Used when a user-level change orphans or deletes
// the user's credential rows as a set.
func (c *headerCredentialCache) EvictByUser(userID string) {
	if c == nil || userID == "" {
		return
	}
	c.cache.EvictWhere(func(key string) bool {
		mode, identity, _, ok := splitHeaderCredentialCacheKey(key)
		return ok && mode == string(schemas.MCPAuthModeUser) && identity == userID
	})
}

// Flush drops every cached entry.
func (c *headerCredentialCache) Flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

// Len reports the number of cached entries.
func (c *headerCredentialCache) Len() int {
	if c == nil {
		return 0
	}
	return c.cache.Len()
}
