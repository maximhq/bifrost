// Package queryscope provides the primitive that wrappers use to push a
// per-call SQL constraint onto the request context for inner stores to
// consume. The configstore and logstore packages both import it so the
// same QueryScope mechanism powers their ScopedDB read paths without
// introducing a cycle between the two stores.
package queryscope

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// QueryScope mutates a query to enforce caller-driven row-level
// constraints. Set on ctx by an upstream wrapper; inner store query
// helpers apply it blindly via ScopedDB.
type QueryScope func(*gorm.DB) *gorm.DB

// WithQueryScope returns ctx carrying scope. Nil scope is a no-op.
func WithQueryScope(ctx context.Context, scope QueryScope) context.Context {
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, schemas.BifrostContextKeyQueryScope, scope)
}

// FromContext returns the scope stashed on ctx, or nil when no scope
// is present (background jobs, OSS-only deployments, internal lookups
// that bypassed the wrapper). A nil scope is equivalent to "no
// restriction": query builders apply no WHERE clause.
func FromContext(ctx context.Context) QueryScope {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(schemas.BifrostContextKeyQueryScope).(QueryScope); ok {
		return v
	}
	return nil
}
