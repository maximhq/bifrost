// Package queryscope provides the primitive that wrappers use to push a
// per-call SQL constraint onto the request context for inner stores to
// consume. The configstore and logstore packages both import it so the
// same QueryScope mechanism powers their ScopedDB read paths without
// introducing a cycle between the two stores.
package queryscope

import (
	"context"
	"encoding/json"
	"strings"

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

// InSet returns the right-hand side of a membership test against ids and the
// single argument it binds, for use as `column + " " + rhs`. GORM expands a slice
// bound to "col IN ?" into one parameter per element, and Postgres rejects a
// statement with more than 65,535 parameters (SQLite: 32,766), so a list of every
// team member or VK a caller can see must travel as ONE parameter instead:
//
//   - postgres: = ANY(?::text[]) with a text[] literal
//   - sqlite:   IN (SELECT value FROM json_each(?)) with a JSON array
//   - others:   IN ? with the slice (previous behavior)
//
// The Postgres form compares against text[], so the column must be text or varchar
// (or a text-valued expression). A uuid, integer, or other non-text column fails with
// "operator does not exist". Cast the column (col::text) only when losing its index
// is acceptable; otherwise add a typed variant.
func InSet(db *gorm.DB, ids []string) (string, any) {
	switch dialectName(db) {
	case "postgres":
		return "= ANY(?::text[])", postgresTextArray(ids)
	case "sqlite":
		if encoded, err := json.Marshal(ids); err == nil {
			return "IN (SELECT value FROM json_each(?))", string(encoded)
		}
	}
	return "IN ?", ids
}

// NotInSet is the negated form of InSet: the right-hand side matches values that
// are not in ids, binding the whole list as a single argument.
func NotInSet(db *gorm.DB, ids []string) (string, any) {
	switch dialectName(db) {
	case "postgres":
		return "<> ALL(?::text[])", postgresTextArray(ids)
	case "sqlite":
		if encoded, err := json.Marshal(ids); err == nil {
			return "NOT IN (SELECT value FROM json_each(?))", string(encoded)
		}
	}
	return "NOT IN ?", ids
}

// InStrings returns a complete "column <InSet>" WHERE fragment and its argument.
// column is a trusted identifier from the caller's code, never user input, and
// must be text-typed on Postgres (see InSet).
func InStrings(db *gorm.DB, column string, ids []string) (string, any) {
	rhs, arg := InSet(db, ids)
	return column + " " + rhs, arg
}

// NotInStrings returns a complete "column <NotInSet>" WHERE fragment and its argument.
func NotInStrings(db *gorm.DB, column string, ids []string) (string, any) {
	rhs, arg := NotInSet(db, ids)
	return column + " " + rhs, arg
}

// dialectName returns the active dialect of db, or "" when db has no dialector
// (a zero-value handle in tests), which selects the portable IN ? form.
func dialectName(db *gorm.DB) string {
	if db == nil || db.Dialector == nil {
		return ""
	}
	return db.Dialector.Name()
}

// postgresTextArray encodes ids as a Postgres text[] input literal, quoting every
// element and escaping backslashes and double quotes, so any string (including
// empty strings and ones containing commas or braces) round-trips unchanged.
func postgresTextArray(ids []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range id {
			if r == '"' || r == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
