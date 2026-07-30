package oauth2

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/lrucache"
)

// defaultUserTokenCacheCapacity bounds the per-user token cache. Session-mode
// identities are caller-asserted strings, so the cache must stay bounded no
// matter what identities callers present; least-recently-used entries are
// dropped once the capacity is reached.
const defaultUserTokenCacheCapacity = 4096

// cachedUserToken is the trimmed value cached per (auth mode, identity,
// mcp client) binding. Only the fields the request path needs are kept:
// the row ID (for targeted eviction), the sanitized access token, and the
// expiry used to treat a stale hit as a miss. Refresh tokens are never
// cached; anything needing one goes back to the database row.
type cachedUserToken struct {
	tokenID     string
	accessToken string
	expiresAt   *time.Time
}

// userTokenCache adapts lrucache.Cache to per-user MCP OAuth access-token
// lookups: it owns the binding-key scheme, registers each entry under its
// token row ID for targeted eviction (refresh, revoke, needs_reauth), treats
// expired entries as misses via the validator so the database path keeps
// every expiry and refresh decision, and provides the scoped bulk evictions
// the credential lifecycle needs (by MCP client, virtual key, and user).
//
// Locking: the cache owns its consistency and must never be guarded by the
// provider's own lock. The provider's lock is held across token-endpoint
// network I/O during refresh and revocation, so sharing it would stall
// every cached read behind unrelated upstream traffic.
type userTokenCache struct {
	cache *lrucache.Cache[cachedUserToken]
}

// userTokenCacheKey builds the cache key for a (mode, identity, mcp client)
// binding. identity is a caller-asserted string (see the doc comment
// above) with no charset restriction, so this goes through
// lrucache.EncodeKey rather than a plain separator join — see its doc
// comment for why a naive join lets an identity value forge a component
// boundary and alias two distinct bindings onto one cache entry.
func userTokenCacheKey(mode schemas.MCPAuthMode, identity, mcpClientID string) string {
	return lrucache.EncodeKey(string(mode), identity, mcpClientID)
}

// splitUserTokenCacheKey is userTokenCacheKey's inverse, for the scoped
// eviction predicates.
func splitUserTokenCacheKey(key string) (mode, identity, clientID string, ok bool) {
	parts, ok := lrucache.DecodeKey(key, 3)
	if !ok {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func newUserTokenCache(capacity int) *userTokenCache {
	if capacity <= 0 {
		capacity = defaultUserTokenCacheCapacity
	}
	return &userTokenCache{
		cache: lrucache.New(capacity, lrucache.WithValidator(func(v cachedUserToken) bool {
			// An expired entry is a miss: the caller falls through to the
			// full database path, which owns all expiry and refresh
			// decisions. nil means non-expiring.
			return v.expiresAt == nil || time.Now().Before(*v.expiresAt)
		})),
	}
}

// Get returns the cached value for key. A hit whose expiry has passed is
// removed and reported as a miss.
func (c *userTokenCache) Get(key string) (cachedUserToken, bool) {
	if c == nil {
		return cachedUserToken{}, false
	}
	return c.cache.Get(key)
}

// Fill runs fill for key with single-flight deduplication: concurrent
// callers for the same key wait for one leader and share its result and
// error, so a refresh burst for one identity performs a single database
// read (and at most one upstream refresh) instead of a stampede. A
// successful result is cached under both the binding key and its token row
// ID; errors are propagated but never cached.
func (c *userTokenCache) Fill(ctx context.Context, key string, fill func() (cachedUserToken, error)) (cachedUserToken, error) {
	if c == nil {
		return fill()
	}
	return c.cache.Fill(ctx, key, func() (cachedUserToken, string, error) {
		value, err := fill()
		return value, value.tokenID, err
	})
}

// Evict removes the entry for an exact binding key, if present.
func (c *userTokenCache) Evict(key string) {
	if c == nil {
		return
	}
	c.cache.Evict(key)
}

// EvictByTokenID removes the entry holding the given token row ID, if any.
func (c *userTokenCache) EvictByTokenID(tokenID string) {
	if c == nil {
		return
	}
	c.cache.EvictByIndex(tokenID)
}

// EvictByMCPClient removes every cached entry bound to the given MCP client,
// across all auth modes and identities. Used when a client-level change
// invalidates its tokens as a set, such as credential rotation or client
// deletion. A linear sweep is fine here: these are rare admin operations and
// the cache is bounded.
func (c *userTokenCache) EvictByMCPClient(mcpClientID string) {
	if c == nil || mcpClientID == "" {
		return
	}
	c.cache.EvictWhere(func(key string) bool {
		_, _, clientID, ok := splitUserTokenCacheKey(key)
		return ok && clientID == mcpClientID
	})
}

// EvictByVirtualKey removes every cached vk-mode entry bound to the given
// virtual key, across all MCP clients. Used when a virtual key change
// orphans or deletes its token rows as a set.
func (c *userTokenCache) EvictByVirtualKey(virtualKeyID string) {
	if c == nil || virtualKeyID == "" {
		return
	}
	c.cache.EvictWhere(func(key string) bool {
		mode, identity, _, ok := splitUserTokenCacheKey(key)
		return ok && mode == string(schemas.MCPAuthModeVK) && identity == virtualKeyID
	})
}

// EvictByUser removes every cached user-mode entry bound to the given user,
// across all MCP clients. Used when a user-level change orphans or deletes
// the user's token rows as a set.
func (c *userTokenCache) EvictByUser(userID string) {
	if c == nil || userID == "" {
		return
	}
	c.cache.EvictWhere(func(key string) bool {
		mode, identity, _, ok := splitUserTokenCacheKey(key)
		return ok && mode == string(schemas.MCPAuthModeUser) && identity == userID
	})
}

// Flush drops every cached entry.
func (c *userTokenCache) Flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

// Len reports the number of cached entries.
func (c *userTokenCache) Len() int {
	if c == nil {
		return 0
	}
	return c.cache.Len()
}
