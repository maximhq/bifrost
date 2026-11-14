package vectorstore

import (
	"context"
	"time"
)

type tenantScopeContextKey struct{}
type tenantAdminContextKey struct{}

// WithTenantScope returns a context that tells a store which tenant a call is made for.
//
// The scope is an opaque, non-reversible label (the semantic cache passes a hash of the caller's
// identity, never the raw ids). A store that enforces tenant isolation itself, such as pgvector
// with row_level_security on, only lets the call see and write rows stamped with the same scope.
// Other stores ignore it.
func WithTenantScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, tenantScopeContextKey{}, scope)
}

// TenantScopeFromContext returns the tenant scope set by WithTenantScope. The second result is
// false when none was set or it is empty.
func TenantScopeFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	scope, _ := ctx.Value(tenantScopeContextKey{}).(string)
	return scope, scope != ""
}

// WithTenantAdmin returns a context for a deliberate cross-tenant operation, such as clearing a
// cache key for every tenant or sweeping expired entries. A store that enforces tenant isolation
// lets such a call see all rows. Use it only in code paths that are meant to cross tenants; a
// call without a tenant scope and without this marker sees nothing.
func WithTenantAdmin(ctx context.Context) context.Context {
	return context.WithValue(ctx, tenantAdminContextKey{}, true)
}

func tenantAdminRequested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	admin, _ := ctx.Value(tenantAdminContextKey{}).(bool)
	return admin
}

// TenantScopeEnforcer is implemented by a store that enforces tenant isolation itself, as a
// second wall behind the caller's own filters.
type TenantScopeEnforcer interface {
	EnforcesTenantScope() bool
}

// EnforcesTenantScope reports whether store enforces the tenant scope in its own engine. A store
// that does not say is taken not to.
func EnforcesTenantScope(store VectorStore) bool {
	enforcer, ok := store.(TenantScopeEnforcer)
	return ok && enforcer.EnforcesTenantScope()
}

// ExpiredEntryRemover is implemented by a store that can delete entries whose expires_at
// metadata (a Unix time in seconds, as the semantic cache writes it) has passed.
type ExpiredEntryRemover interface {
	// DeleteExpired deletes at most limit entries whose expires_at is before the given time and
	// reports how many it deleted. Entries without a numeric expires_at never expire.
	DeleteExpired(ctx context.Context, namespace string, before time.Time, limit int) (int, error)
}
