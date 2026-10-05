// Package injectedauth holds the marker that lets one provider-injected MCP tool run
// past the client and request tool filters.
//
// It is internal so only code under core/ can plant the marker. A plugin holds the
// request context and can lift BlockRestrictedWrites, so a marker under a public
// context key would let any plugin authorize any tool. Both the key and the value
// are unexported: a plugin that finds the key through GetUserValues still cannot
// build a value that names another tool.
package injectedauth

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
)

type contextKey struct{}

type authorization struct {
	clientName string // MCP client that owns the tool
	toolName   string // prefixed tool name, "<client>-<tool>"
}

// Set marks ctx as running the provider-injected tool toolName owned by clientName.
func Set(ctx *schemas.BifrostContext, clientName, toolName string) {
	ctx.SetValue(contextKey{}, authorization{clientName: clientName, toolName: toolName})
}

// Authorizes reports whether ctx carries the marker for exactly this client's tool.
// It never authorizes a sibling tool or another client's tool of the same name.
func Authorizes(ctx context.Context, clientName, toolName string) bool {
	auth, ok := ctx.Value(contextKey{}).(authorization)
	return ok && auth.toolName != "" && auth.toolName == toolName && auth.clientName == clientName
}
