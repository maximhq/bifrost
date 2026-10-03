package governance

import "github.com/maximhq/bifrost/core/schemas"

const mcpAuthorizationContextKey schemas.BifrostContextKey = "bf-governance-mcp-authorization"

// mcpAuthorization binds trusted transport approval to one exact execution target.
type mcpAuthorization struct{ clientName, toolName string }

// SetMCPExecutionAuthorization records a transport-verified approval for one MCP call.
// Only trusted handlers may call this after checking their own authorization policy;
// it replaces the MCP tool permit check, never identity, headers or usage limits.
func SetMCPExecutionAuthorization(ctx *schemas.BifrostContext, clientName, toolName string) {
	ctx.SetValue(mcpAuthorizationContextKey, mcpAuthorization{clientName, toolName})
}

// hasMCPExecutionAuthorization prevents approval from following a retargeted request.
func hasMCPExecutionAuthorization(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) bool {
	approval, ok := ctx.Value(mcpAuthorizationContextKey).(mcpAuthorization)
	return ok && approval.clientName != "" && approval.toolName != "" && req.ClientName == approval.clientName && req.GetToolName() == approval.toolName
}
