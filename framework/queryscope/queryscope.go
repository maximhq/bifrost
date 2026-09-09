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

// DimensionScope bounds the VALUES a grouping dimension may take, which is a
// different constraint from QueryScope and not implied by it.
//
// QueryScope decides which rows a caller may receive. That is not enough for an
// aggregate that groups by an organisation column, because a row can be
// legitimately visible on one dimension while carrying, on another, an id the
// caller may not see. A user who belongs to two teams under different customers
// produces exactly that row: a teammate may hold it — they share a team — while
// the customer stamped on it belongs to an organisation they have no access to.
// Grouping that row by customer names the second customer, and every ranking,
// histogram and filter dropdown built on the same column repeats it.
//
// Given a scalar id column ("customer_id", "team_id", "business_unit_id",
// "user_id", "virtual_key_id"), a DimensionScope returns the ids the caller may
// be shown on that dimension and whether a ceiling applies at all. Returning
// false means "unconstrained" — dimensions that describe the request rather
// than the organisation (provider, model, alias) have no per-caller set to
// compare against. Returning an empty slice with true means the caller may be
// shown no id on that dimension, which is not the same thing.
type DimensionScope func(idCol string) (allowed []string, bounded bool)

// WithDimensionScope returns ctx carrying scope. Nil scope is a no-op.
func WithDimensionScope(ctx context.Context, scope DimensionScope) context.Context {
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, schemas.BifrostContextKeyDimensionScope, scope)
}

// DimensionFromContext returns the dimension scope stashed on ctx, or nil when
// none is present. Nil means no ceiling, matching FromContext's convention.
func DimensionFromContext(ctx context.Context) DimensionScope {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(schemas.BifrostContextKeyDimensionScope).(DimensionScope); ok {
		return v
	}
	return nil
}
