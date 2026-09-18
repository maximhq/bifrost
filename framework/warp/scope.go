package warp

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
)

// Scope is who is asking, as the system prompt needs to know it: whether the
// caller is identified, so the prompt can say their own traffic is the default
// or tell the model to ask which team, customer or business unit is meant.
//
// The default itself - narrowing a question that named no scope to the
// caller's own rows - is applied inside the tools, on Bifrost's MCP server,
// from an opt-in the dashboard transport stamps on the request context
// (mcptools.WithDefaultUserScope). This type only mirrors that decision for
// the prompt; it is not consulted by any query.
type Scope struct {
	// HasIdentity reports whether the caller is a known user. It drives whether
	// Warp defaults or asks.
	HasIdentity bool
	UserID      string
}

// ScopeFromContext derives the caller's scope.
//
// Read from the context, never from the request: a scope the caller can name in
// the body would be a suggestion, and this needs to be a fact about who asked.
func ScopeFromContext(ctx context.Context) Scope {
	userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	if userID == "" {
		return Scope{}
	}
	return Scope{HasIdentity: true, UserID: userID}
}
