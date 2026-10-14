package governance

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// isInjectedToolExecution reads core's provider-injected tool marker. Only core can set
// it; tests swap this to stand in for an execution core started.
var isInjectedToolExecution = bifrost.InjectedToolAuthorized

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
//
// Core's provider-injected tool marker is the second approval source: Bifrost sets it
// when it runs the tool an operator configured in the provider's injected_tools, which
// no virtual key grant lists. Like the approval above it names one client and one exact
// prefixed tool, and both must match the request.
func hasMCPExecutionAuthorization(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) bool {
	if isInjectedToolExecution(ctx, req.ClientName, req.GetToolName()) {
		return true
	}
	approval, ok := ctx.Value(mcpAuthorizationContextKey).(mcpAuthorization)
	return ok && approval.clientName != "" && approval.toolName != "" && req.ClientName == approval.clientName && req.GetToolName() == approval.toolName
}
