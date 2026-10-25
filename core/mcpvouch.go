package bifrost

import (
	"github.com/maximhq/bifrost/core/internal/mcpvouch"
	"github.com/maximhq/bifrost/core/schemas"
)

// MCPToolCallKindOf returns why core vouches for ctx's run of the MCP tool toolName
// owned by clientName, or MCPToolCallKindNone. Plugins that gate MCP tools
// (governance) read it: a provider-injected tool runs past their tool permits, and a
// connect tool is allowed by the caller's access to its client, since no permit can
// name a tool the caller has not discovered yet. Only core can set the mark, so a
// plugin cannot forge it.
func MCPToolCallKindOf(ctx *schemas.BifrostContext, clientName, toolName string) schemas.MCPToolCallKind {
	return mcpvouch.Kind(ctx, clientName, toolName)
}
