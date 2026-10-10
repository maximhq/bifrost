// Package injectedauth holds the marker that lets one provider-injected MCP tool run
// past the client and request tool filters.
//
// A plugin holds the request context, can lift BlockRestrictedWrites, and can read and
// copy every user value through GetUserValues. So the marker is not a user value: it
// lives in the context's trusted store under a key of an unexported type. Only code
// under core/ can import this package, and code that cannot name the key can neither
// plant the marker, read it, nor copy it to another context.
package injectedauth

import (
	"github.com/maximhq/bifrost/core/schemas"
)

type contextKey struct{}

type authorization struct {
	clientName string // MCP client that owns the tool
	toolName   string // prefixed tool name, "<client>-<tool>"
}

// Set marks ctx as running the provider-injected tool toolName owned by clientName.
func Set(ctx *schemas.BifrostContext, clientName, toolName string) {
	ctx.SetTrustedValue(contextKey{}, authorization{clientName: clientName, toolName: toolName})
}

// Authorizes reports whether ctx carries the marker for exactly this client's tool.
// It never authorizes a sibling tool or another client's tool of the same name.
func Authorizes(ctx *schemas.BifrostContext, clientName, toolName string) bool {
	auth, ok := ctx.TrustedValue(contextKey{}).(authorization)
	return ok && auth.toolName != "" && auth.toolName == toolName && auth.clientName == clientName
}
