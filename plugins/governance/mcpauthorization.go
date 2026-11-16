package governance

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

const mcpAuthorizationContextKey schemas.BifrostContextKey = "bf-governance-mcp-authorization"

// mcpAuthorization binds trusted transport approval to one exact execution target.
type mcpAuthorization struct{ clientName, toolName string }

// SetMCPExecutionAuthorization records a transport-verified approval for one MCP call.
// Only trusted handlers may call this after checking their own authorization policy;
// it replaces the MCP tool permit check, never identity, headers or usage limits.
func SetMCPExecutionAuthorization(ctx *schemas.BifrostContext, clientName, toolName string) {
	ctx.SetValue(mcpAuthorizationContextKey, mcpAuthorization{clientName, toolName})
}

// mcpToolCallAllowed reports whether access lets the MCP call req run, for a call core
// vouches for as kind (bifrost.MCPToolCallKindOf). The sources, in order:
//
//  1. A provider-injected tool runs: the operator configured it in the provider's
//     injected_tools, which no virtual key grant lists.
//  2. A transport-verified approval for exactly this client and tool runs it
//     (SetMCPExecutionAuthorization); one for another target does not carry over.
//  3. A connect tool runs when access grants any tool of its client: core offers it
//     until the caller has discovered the client's tools, so no permit can name it.
//  4. Anything else runs when access permits the tool itself.
func mcpToolCallAllowed(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest, access schemas.Access) bool {
	kind := bifrost.MCPToolCallKindOf(ctx, req.ClientName, req.GetToolName())
	if kind == schemas.MCPToolCallKindInjected {
		return true
	}
	if approval, ok := ctx.Value(mcpAuthorizationContextKey).(mcpAuthorization); ok &&
		approval.clientName != "" && approval.toolName != "" &&
		approval.clientName == req.ClientName && approval.toolName == req.GetToolName() {
		return true
	}
	if kind == schemas.MCPToolCallKindConnect {
		return access.IsMCPToolAllowed(req.ClientName + "-*")
	}
	return access.IsMCPToolAllowed(req.GetToolName())
}
