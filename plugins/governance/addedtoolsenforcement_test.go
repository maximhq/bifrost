// This suite covers the MCP added-tools enforcement block in EvaluateGovernanceRequest:
// the tools actually injected into a request (BifrostContextKeyMCPAddedTools, stamped by
// AddToolsToRequest before PreLLMHook) must be validated against the virtual key's
// MCPConfigs on the normal request path.
//
// Regression coverage: the block was previously gated on result.VirtualKey, but the
// preceding customer/team/user evaluation steps return fresh results that never carry
// the VK, so the check only ever ran on the list-models skip path. The block now keys
// on the VK resolved for the hierarchy checks.
package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evaluateAddedTools runs EvaluateGovernanceRequest for the test VK with the given
// injected tool names stamped on ctx, mirroring the normal chat request path (no
// skip-budgets flag, no user ID).
func evaluateAddedTools(t *testing.T, p *GovernancePlugin, addedTools []string) (*EvaluationResult, *schemas.BifrostError) {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, mcpTestVKValue)
	if addedTools != nil {
		ctx.SetValue(schemas.BifrostContextKeyMCPAddedTools, addedTools)
	}
	return p.EvaluateGovernanceRequest(ctx, &EvaluationRequest{
		VirtualKey: mcpTestVKValue,
		Provider:   schemas.OpenAI,
		Model:      "gpt-4o",
	}, schemas.ChatCompletionRequest)
}

// An injected tool outside the VK's grant must block the request on the normal path.
func TestEvaluateGovernanceRequestMCP_AddedTools_UngrantedTool_Blocked(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"tool_a"})
	p := newPluginForMCPStamping(t, vk, false)

	result, bifrostErr := evaluateAddedTools(t, p, []string{"sentry-tool_b"})

	require.NotNil(t, bifrostErr, "ungranted injected tool must reject the request")
	require.NotNil(t, result)
	assert.Equal(t, DecisionMCPToolBlocked, result.Decision)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 403, *bifrostErr.StatusCode)
	require.NotNil(t, result.VirtualKey, "blocked result should carry the VK that denied the tool")
	assert.Equal(t, vk.ID, result.VirtualKey.ID, "blocked result should carry the fixture VK")
	require.NotNil(t, bifrostErr.Error)
	assert.Contains(t, bifrostErr.Error.Message, "sentry-tool_b", "rejection should name the blocked tool")
}

// An injected tool inside the VK's grant passes the added-tools check.
func TestEvaluateGovernanceRequestMCP_AddedTools_GrantedTool_Allowed(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a"}), false)

	result, bifrostErr := evaluateAddedTools(t, p, []string{"sentry-tool_a"})

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)
	assert.Equal(t, DecisionAllow, result.Decision)
}

// A mixed injected list is allowed only when every entry is granted.
func TestEvaluateGovernanceRequestMCP_AddedTools_MixedTools_Blocked(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a"}), false)

	result, bifrostErr := evaluateAddedTools(t, p, []string{"sentry-tool_a", "sentry-tool_b"})

	require.NotNil(t, bifrostErr)
	require.NotNil(t, result)
	assert.Equal(t, DecisionMCPToolBlocked, result.Decision)
}

// A keyless request has no VK grant to enforce against, so the added-tools check
// is skipped and the request is governed by the remaining keyless rules.
func TestEvaluateGovernanceRequestMCP_AddedTools_NoVirtualKey_SkipsCheck(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a"}), false)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyMCPAddedTools, []string{"sentry-tool_b"})

	result, bifrostErr := p.EvaluateGovernanceRequest(ctx, &EvaluationRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
	}, schemas.ChatCompletionRequest)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)
	assert.Equal(t, DecisionAllow, result.Decision)
}
