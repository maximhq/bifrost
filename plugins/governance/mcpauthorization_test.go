package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestMCPExecutionAuthorization preserves governance while replacing only the exact tool permit.
func TestMCPExecutionAuthorization(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping(nil), false)
	for _, tc := range []struct {
		name, client, tool, key string
		approved, allowed       bool
	}{
		{"gateway", "local", "local-read", mcpTestVKValue, false, false},
		{"approved", "local", "local-read", mcpTestVKValue, true, true},
		{"other tool", "local", "local-write", mcpTestVKValue, true, false},
		{"other client", "other", "local-read", mcpTestVKValue, true, false},
		{"invalid identity", "local", "local-read", "invalid", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := presentCtx(tc.key)
			if tc.approved {
				SetMCPExecutionAuthorization(ctx, "local", "local-read")
			}
			req := &schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeChatToolCall, ClientName: tc.client, ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &tc.tool, Arguments: "{}"}}}
			_, short, err := p.PreMCPHook(ctx, req)
			require.NoError(t, err)
			if tc.allowed {
				require.Nil(t, short)
			} else {
				require.NotNil(t, short)
				require.NotNil(t, short.Error)
			}
		})
	}
}

// TestMCPExecutionAuthorizationRequiresHeaders keeps transport requirements ahead of tool approval.
func TestMCPExecutionAuthorizationRequiresHeaders(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping(nil), false)
	p.requiredHeaders = &[]string{"x-required"}
	ctx := presentCtx(mcpTestVKValue)
	SetMCPExecutionAuthorization(ctx, "local", "local-read")
	name := "local-read"
	req := &schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeChatToolCall, ClientName: "local", ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: "{}"}}}
	_, short, err := p.PreMCPHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, short)
	require.Equal(t, "missing_required_headers", *short.Error.Type)
}

// TestMCPExecutionAuthorizationForInjectedTool lets a provider-injected tool past the
// virtual key's tool permits. The operator chose the tool in the provider config, so
// the key's MCP grants never listed it; identity and limits still apply.
func TestMCPExecutionAuthorizationForInjectedTool(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping(nil), false)
	for _, tc := range []struct {
		name, injectedClient, injected, tool, key string
		allowed                                   bool
	}{
		{"injected tool", "local", "local-write", "local-write", mcpTestVKValue, true},
		{"sibling of the injected tool", "local", "local-write", "local-delete", mcpTestVKValue, false},
		{"another client's tool of the same name", "other", "local-write", "local-write", mcpTestVKValue, false},
		{"invalid identity", "local", "local-write", "local-write", "invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := isInjectedToolExecution
			t.Cleanup(func() { isInjectedToolExecution = restore })
			isInjectedToolExecution = func(_ context.Context, clientName, toolName string) bool {
				return clientName == tc.injectedClient && toolName == tc.injected
			}
			ctx := presentCtx(tc.key)
			req := &schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeChatToolCall, ClientName: "local", ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &tc.tool, Arguments: "{}"}}}
			_, short, err := p.PreMCPHook(ctx, req)
			require.NoError(t, err)
			if tc.allowed {
				require.Nil(t, short)
			} else {
				require.NotNil(t, short)
			}
		})
	}
}

// TestMCPExecutionAuthorizationRejectsForgedInjectedMarker keeps the injected-tool
// permit core-only. A plugin can lift BlockRestrictedWrites and call public SetValue,
// so a marker planted that way must not stand in for the virtual key's tool grants.
func TestMCPExecutionAuthorizationRejectsForgedInjectedMarker(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping(nil), false)
	ctx := presentCtx(mcpTestVKValue)
	ctx.Root().UnblockRestrictedWrites()
	ctx.SetValue(schemas.BifrostContextKey("bifrost-injected-tool-execution"), struct{ ClientName, ToolName string }{ClientName: "local", ToolName: "local-write"})
	tool := "local-write"
	req := &schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeChatToolCall, ClientName: "local", ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &tool, Arguments: "{}"}}}
	_, short, err := p.PreMCPHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, short, "a marker written through public SetValue must not bypass the virtual key's tool permits")
}
