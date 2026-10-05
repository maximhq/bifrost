package bifrost

import (
	"context"

	"github.com/maximhq/bifrost/core/internal/injectedauth"
)

// InjectedToolAuthorized reports whether ctx runs the provider-injected MCP tool toolName
// owned by clientName. Plugins that gate MCP tools (governance) read it to let that one
// tool past their own permits. Only core can set the marker, so a plugin cannot forge it.
func InjectedToolAuthorized(ctx context.Context, clientName, toolName string) bool {
	return injectedauth.Authorizes(ctx, clientName, toolName)
}
