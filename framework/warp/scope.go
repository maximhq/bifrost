package warp

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
)

// Scope is who is asking, as the system prompt needs to know it: whether the
// caller is identified, so the prompt can say their own traffic is the default
// or tell the model to ask which team, customer or business unit is meant.
//
// The default itself - narrowing a question that named no scope to the
// caller's own rows - is applied inside the tools, on Bifrost's MCP server
// (see mcptools' package note on scope). Warp resolves the caller here and
// hands the result over on the caller view each turn (Agent.callerView): the
// default user, whether any row-level scope applies to them, and what slice a
// restricted caller may see. The same Scope drives what the prompt says.
//
// It is precision, not access control: queryscope still narrows every read
// inside the store, whatever this says.
type Scope struct {
	// HasIdentity reports whether the caller is a known user. It drives whether
	// Warp defaults or asks.
	HasIdentity bool
	UserID      string
	// Unrestricted reports that no row-level query scope applies to the caller,
	// so everything they may see is the whole deployment.
	Unrestricted bool
	// Visibility says, in a few words, which slice a restricted caller may see
	// ("their teams' traffic"). Empty when the caller is unrestricted or the
	// deployment has no resolver to say.
	Visibility string
	// Visible is the row ownership a restricted caller's reads are narrowed
	// to, when the deployment can say. Semantic search prefilters the index
	// with it; nil leaves the search unfiltered, as it is for everyone else.
	Visible *LogVisibility
}

// CallerRestriction is what the deployment's row-level access control says
// about one caller.
type CallerRestriction struct {
	// Restricted reports that the store will narrow this caller's reads.
	Restricted bool
	// Visibility is a short phrase for what the caller may see, written to
	// follow "the person asking may see ...". Optional.
	Visibility string
	// Logs is the row ownership the store grants the caller on log reads.
	// Optional: it only sharpens semantic search (see LogVisibility).
	Logs *LogVisibility
}

// CallerRestrictionResolver reports whether row-level access control narrows
// the caller's reads.
//
// It exists because ScopeFromContext can only see a queryscope that is already
// on the context, and a deployment whose store wrapper attaches the scope per
// read - the enterprise one does - never puts it there. Warp then took every
// caller for unrestricted: a team-scoped user's total was their team's rows,
// correctly filtered by the store, and described as the whole deployment's.
//
// Like the scope itself this is precision, not access control. The resolver is
// given the snapshotted context the tools run under, so it answers from the
// same identity the store will.
type CallerRestrictionResolver func(ctx context.Context) CallerRestriction

// withCallerRestriction folds a resolver's answer into a context-derived scope.
//
// It only ever narrows: a caller the context already shows as restricted stays
// restricted whatever the resolver says, so a resolver that knows nothing
// cannot widen a default. The local admin is left alone - they bypass RBAC by
// definition, and the store does not scope them either.
func withCallerRestriction(ctx context.Context, scope Scope, resolve CallerRestrictionResolver) Scope {
	if resolve == nil {
		return scope
	}
	if isLocalAdmin, _ := ctx.Value(schemas.IsLocalAdminContextKey).(bool); isLocalAdmin {
		return scope
	}
	restriction := resolve(ctx)
	if !restriction.Restricted {
		return scope
	}
	scope.Unrestricted = false
	scope.Visibility = restriction.Visibility
	scope.Visible = restriction.Logs
	return scope
}

// ScopeFromContext derives the caller's scope.
//
// Read from the context, never from the request: a scope the caller can name in
// the body would be a suggestion, and this needs to be a fact about who asked.
func ScopeFromContext(ctx context.Context) Scope {
	// A nil queryscope is the store's own convention for "no restriction". The
	// local admin bypasses RBAC by definition, so the flag counts on its own.
	isLocalAdmin, _ := ctx.Value(schemas.IsLocalAdminContextKey).(bool)
	scope := Scope{Unrestricted: isLocalAdmin || queryscope.FromContext(ctx) == nil}
	userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	if userID == "" {
		return scope
	}
	scope.HasIdentity, scope.UserID = true, userID
	return scope
}
