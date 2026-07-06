// This suite covers PreRequestHook's MCP include-tools stamping: how a caller-provided
// x-bf-mcp-include-tools list is pruned against the virtual key's tool grant, and when
// the grant itself is stamped onto the context for downstream injection. The rules under
// test, for both the normal request path and the large-payload branch:
//
//   - A caller include-tools list is always pruned to the grant (it can only narrow,
//     never expand), regardless of the DisableAutoToolInject setting.
//   - When no caller list is present, the grant is stamped if auto-injection is enabled
//     OR an include-clients filter is present — the latter because any explicit MCP
//     filter opts the request into injection downstream even when auto-inject is off.
//   - With no filters and auto-injection disabled, nothing is stamped and no injection
//     occurs.
//   - Deny-all is always an explicit empty list; an unset key means "no filtering" and
//     would expose every available tool.
package governance

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mcpTestVKValue = "sk-bf-mcp-test"

// buildVKForMCPStamping returns an active VK with an openai provider config (so load
// balancing has a provider pool) and an explicit sentry MCP config granting the given
// tools. Passing nil yields a VK with no MCP configs at all, which is semantically
// different from passing an empty slice (an explicit deny-all config for the client).
func buildVKForMCPStamping(tools []string) *configstoreTables.TableVirtualKey {
	vk := buildVirtualKeyWithProviders(
		"vk-mcp-stamp",
		mcpTestVKValue,
		"mcp-stamp-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("openai", []string{"*"}),
		},
	)
	if tools != nil {
		vk.MCPConfigs = []configstoreTables.TableVirtualKeyMCPConfig{
			{
				MCPClient:      configstoreTables.TableMCPClient{ClientID: "client-1", Name: "sentry"},
				ToolsToExecute: tools,
			},
		}
	}
	return vk
}

// newPluginForMCPStamping builds a governance plugin around a single VK with the
// given DisableAutoToolInject setting.
func newPluginForMCPStamping(t *testing.T, vk *configstoreTables.TableVirtualKey, disableAutoToolInject bool) *GovernancePlugin {
	t.Helper()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory:         boolPtr(false),
		DisableAutoToolInject: boolPtr(disableAutoToolInject),
	}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

// newPreRequestCtx returns a ctx carrying the test VK plus optional caller MCP filters,
// mirroring what lib/ctx.go stamps from the x-bf-mcp-include-tools and
// x-bf-mcp-include-clients request headers.
func newPreRequestCtx(includeTools, includeClients []string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, mcpTestVKValue)
	if includeTools != nil {
		ctx.SetValue(schemas.MCPContextKeyIncludeTools, includeTools)
	}
	if includeClients != nil {
		ctx.SetValue(schemas.MCPContextKeyIncludeClients, includeClients)
	}
	return ctx
}

// newChatRequest returns a chat request with the provider already resolved, so
// PreRequestHook's load-balancing step is a no-op and only MCP stamping is exercised.
func newChatRequest() *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"},
	}
}

// stampedIncludeTools runs PreRequestHook and returns the resulting include-tools ctx
// value (nil when nothing was stamped).
func stampedIncludeTools(t *testing.T, p *GovernancePlugin, ctx *schemas.BifrostContext, req *schemas.BifrostRequest) []string {
	t.Helper()
	require.NoError(t, p.PreRequestHook(ctx, req))
	value := ctx.Value(schemas.MCPContextKeyIncludeTools)
	if value == nil {
		return nil
	}
	tools, ok := value.([]string)
	require.True(t, ok, "include-tools ctx value should be a []string")
	return tools
}

// ============================================================================
// Decision matrix: include-tools present/absent × include-clients present/absent
// × DisableAutoToolInject on/off
// ============================================================================

// Baseline auto-injection: no caller filters, auto-injection enabled. Governance stamps
// the key's full tool grant (every granted tool, client-prefixed) onto the context, and
// downstream injection attaches exactly these tools to the outgoing request.
func TestPreRequestHookMCP_AutoInjectOn_NoFilters_StampsGrant(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), false)
	ctx := newPreRequestCtx(nil, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_a", "sentry-tool_b"}, tools)
}

// The caller filters by client only and sends no include-tools list. Governance still
// stamps the full grant: the grant is the tool-level ceiling, and the client-level
// narrowing is applied downstream where the include-clients filter intersects with it.
func TestPreRequestHookMCP_AutoInjectOn_IncludeClientsOnly_StampsGrant(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), false)
	ctx := newPreRequestCtx(nil, []string{"sentry"})

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_a", "sentry-tool_b"}, tools)
}

// The caller sends an include-tools list with one granted and one ungranted tool.
// Governance prunes the list in place instead of replacing it: the granted entry
// survives, the ungranted entry is dropped, and the full grant is NOT stamped over
// the caller's narrower selection.
func TestPreRequestHookMCP_AutoInjectOn_IncludeToolsPresent_Prunes(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), false)
	ctx := newPreRequestCtx([]string{"sentry-tool_a", "sentry-tool_c"}, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_a"}, tools,
		"granted entry survives, ungranted entry is pruned, grant is not re-expanded")
}

// With auto-injection disabled and no caller filters, the request has not opted into
// MCP tools in any way: governance must leave the include-tools key unset so downstream
// injection sees neither filter and skips entirely. This is the contract of the
// DisableAutoToolInject setting.
func TestPreRequestHookMCP_AutoInjectOff_NoFilters_StampsNothing(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), true)
	ctx := newPreRequestCtx(nil, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Nil(t, tools, "no filters + auto-inject disabled must leave include-tools unset")
}

// With auto-injection disabled, an include-clients filter still opts the request into
// injection downstream (any explicit MCP filter counts as opt-in). The grant must
// therefore be stamped even though auto-inject is off — left unset, the request would
// be injected with every tool of the included client, bypassing the key's grant.
func TestPreRequestHookMCP_AutoInjectOff_IncludeClientsOnly_StampsGrant(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), true)
	ctx := newPreRequestCtx(nil, []string{"sentry"})

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_a", "sentry-tool_b"}, tools,
		"include-clients triggers injection downstream, so the VK ceiling must be stamped")
}

// Pruning is independent of the auto-inject setting: a caller include-tools list is
// narrowed against the grant even when auto-injection is disabled, because the list
// itself opts the request into injection downstream.
func TestPreRequestHookMCP_AutoInjectOff_IncludeToolsPresent_Prunes(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), true)
	ctx := newPreRequestCtx([]string{"sentry-tool_b", "sentry-tool_c"}, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_b"}, tools)
}

// When the caller sends both filters, the pruned include-tools list is the final value:
// the presence of include-clients must not cause the full grant to overwrite the
// caller's narrower selection. Verified under both DisableAutoToolInject values.
func TestPreRequestHookMCP_BothFilters_PrunedListWins(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disableAutoToolInject=%v", disabled), func(t *testing.T) {
			p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), disabled)
			ctx := newPreRequestCtx([]string{"sentry-tool_a"}, []string{"sentry"})

			tools := stampedIncludeTools(t, p, ctx, newChatRequest())
			assert.Equal(t, []string{"sentry-tool_a"}, tools,
				"pruned caller list must not be overwritten by the grant")
		})
	}
}

// ============================================================================
// Grant-shape edge cases
// ============================================================================

// A key with no MCP configs at all has an empty effective grant. When include-clients
// opts the request into injection, governance must stamp an explicit empty list
// (deny-all) rather than leave the key unset — an unset key reads downstream as
// "no filtering" and would inject every available tool of the included client.
func TestPreRequestHookMCP_NoGrants_IncludeClients_StampsDenyAll(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping(nil), true)
	ctx := newPreRequestCtx(nil, []string{"sentry"})

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	require.NotNil(t, tools, "deny-all must be an empty list, not an unset key")
	assert.Empty(t, tools)
}

// Same deny-all outcome as the no-configs case, but through a different path in
// computeMCPIncludeTools: here the key has an explicit MCP config for the client whose
// tools list is empty, exercising the ToolsToExecute.IsEmpty guard that skips the
// client without emitting any grant entries.
func TestPreRequestHookMCP_ExplicitEmptyGrant_IncludeClients_StampsDenyAll(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{}), true)
	ctx := newPreRequestCtx(nil, []string{"sentry"})

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	require.NotNil(t, tools, "deny-all must be an empty list, not an unset key")
	assert.Empty(t, tools)
}

// An unrestricted ("*") grant is stamped as the client-scoped wildcard "sentry-*",
// which downstream filtering reads as "all tools of this client" — the grant never
// expands to other clients.
func TestPreRequestHookMCP_UnrestrictedGrant_StampsWildcard(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"*"}), false)
	ctx := newPreRequestCtx(nil, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-*"}, tools)
}

// The caller requests a tool of a client the key has no grants for. Pruning drops it
// and stamps an explicit empty list, so the request cannot reach another client's
// tools just by naming them in the header.
func TestPreRequestHookMCP_IncludeToolsForUngrantedClient_PrunesToDenyAll(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a"}), false)
	ctx := newPreRequestCtx([]string{"github-list_repos"}, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	require.NotNil(t, tools)
	assert.Empty(t, tools)
}

// An empty x-bf-mcp-include-tools header value reaches ctx as [""] (see lib/ctx.go).
// Pruning drops the empty entry and stamps deny-all: a caller can suppress tool
// injection for a single request, but cannot gain access through the empty value.
func TestPreRequestHookMCP_EmptyIncludeToolsHeader_DenyAll(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), false)
	ctx := newPreRequestCtx([]string{""}, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	require.NotNil(t, tools)
	assert.Empty(t, tools)
}

// ============================================================================
// Paths that must NOT stamp
// ============================================================================

// Without a virtual key on ctx (and no routing rules configured), PreRequestHook
// returns before any MCP handling: there is no grant to enforce, so caller filters
// pass through untouched for downstream layers to interpret.
func TestPreRequestHookMCP_NoVirtualKey_NoStamping(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a"}), false)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.MCPContextKeyIncludeClients, []string{"sentry"})

	require.NoError(t, p.PreRequestHook(ctx, newChatRequest()))
	assert.Nil(t, ctx.Value(schemas.MCPContextKeyIncludeTools))
}

// An inactive key is treated as absent: PreRequestHook returns before MCP handling
// and stamps nothing.
func TestPreRequestHookMCP_InactiveVK_NoStamping(t *testing.T) {
	vk := buildVKForMCPStamping([]string{"tool_a"})
	inactive := false
	vk.IsActive = &inactive
	p := newPluginForMCPStamping(t, vk, false)
	ctx := newPreRequestCtx(nil, []string{"sentry"})

	require.NoError(t, p.PreRequestHook(ctx, newChatRequest()))
	assert.Nil(t, ctx.Value(schemas.MCPContextKeyIncludeTools))
}

// Passthrough request types skip governance entirely, including MCP stamping.
func TestPreRequestHookMCP_PassthroughRequest_NoStamping(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a"}), false)
	ctx := newPreRequestCtx(nil, []string{"sentry"})
	req := &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest}

	require.NoError(t, p.PreRequestHook(ctx, req))
	assert.Nil(t, ctx.Value(schemas.MCPContextKeyIncludeTools))
}

// ============================================================================
// Large-payload branch (runPreRequestRouting) applies the same stamping rules
// ============================================================================

// newLargePayloadCtx returns a ctx that routes PreRequestHook through its large-payload
// branch: the body streams to the provider unparsed, so the model comes from
// LargePayloadMetadata instead of the request. The metadata pointer is returned so
// tests can assert the routed model is propagated (the streaming body rewriter
// consumes metadata.Model when rewriting the body prefix).
func newLargePayloadCtx(includeTools, includeClients []string) (*schemas.BifrostContext, *schemas.LargePayloadMetadata) {
	ctx := newPreRequestCtx(includeTools, includeClients)
	metadata := &schemas.LargePayloadMetadata{Model: "openai/gpt-4o"}
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, metadata)
	return ctx, metadata
}

// Large-payload counterpart of the include-clients opt-in case: with auto-injection
// disabled, the grant must still be stamped because include-clients triggers injection
// downstream. The provider-prefixed model must survive routing unchanged.
func TestPreRequestHookMCP_LargePayload_AutoInjectOff_IncludeClients_StampsGrant(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), true)
	ctx, metadata := newLargePayloadCtx(nil, []string{"sentry"})

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_a", "sentry-tool_b"}, tools)
	assert.Equal(t, "openai/gpt-4o", metadata.Model, "provider-prefixed model must survive routing unchanged")
}

// Large-payload counterpart of the baseline auto-injection case: no caller filters,
// auto-injection enabled, full grant stamped.
func TestPreRequestHookMCP_LargePayload_AutoInjectOn_NoFilters_StampsGrant(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), false)
	ctx, metadata := newLargePayloadCtx(nil, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_a", "sentry-tool_b"}, tools)
	assert.Equal(t, "openai/gpt-4o", metadata.Model, "provider-prefixed model must survive routing unchanged")
}

// Large-payload counterpart of the include-clients-only case with auto-injection
// enabled: the full grant is stamped as the tool-level ceiling.
func TestPreRequestHookMCP_LargePayload_AutoInjectOn_IncludeClientsOnly_StampsGrant(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), false)
	ctx, metadata := newLargePayloadCtx(nil, []string{"sentry"})

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_a", "sentry-tool_b"}, tools)
	assert.Equal(t, "openai/gpt-4o", metadata.Model, "provider-prefixed model must survive routing unchanged")
}

// Large-payload pruning with auto-injection disabled: the caller's include-tools list
// is narrowed against the grant — the toggle never disables grant enforcement.
func TestPreRequestHookMCP_LargePayload_AutoInjectOff_IncludeToolsPresent_Prunes(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), true)
	ctx, metadata := newLargePayloadCtx([]string{"sentry-tool_a", "sentry-tool_c"}, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_a"}, tools)
	assert.Equal(t, "openai/gpt-4o", metadata.Model, "provider-prefixed model must survive routing unchanged")
}

// Large-payload pruning with auto-injection enabled: granted entries survive,
// ungranted entries are dropped, and the grant is not re-stamped over the caller's
// narrower selection.
func TestPreRequestHookMCP_LargePayload_IncludeToolsPresent_Prunes(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), false)
	ctx, metadata := newLargePayloadCtx([]string{"sentry-tool_a", "sentry-tool_c"}, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Equal(t, []string{"sentry-tool_a"}, tools)
	assert.Equal(t, "openai/gpt-4o", metadata.Model, "provider-prefixed model must survive routing unchanged")
}

// Large-payload counterpart of the both-filters case: the pruned include-tools list is
// the final value and is not overwritten by the grant. Verified under both
// DisableAutoToolInject values.
func TestPreRequestHookMCP_LargePayload_BothFilters_PrunedListWins(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disableAutoToolInject=%v", disabled), func(t *testing.T) {
			p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), disabled)
			ctx, metadata := newLargePayloadCtx([]string{"sentry-tool_a"}, []string{"sentry"})

			tools := stampedIncludeTools(t, p, ctx, newChatRequest())
			assert.Equal(t, []string{"sentry-tool_a"}, tools,
				"pruned caller list must not be overwritten by the grant")
			assert.Equal(t, "openai/gpt-4o", metadata.Model, "provider-prefixed model must survive routing unchanged")
		})
	}
}

// Large-payload counterpart of the disabled-toggle baseline: no filters and
// auto-injection disabled leaves the include-tools key unset, so downstream injection
// is skipped entirely.
func TestPreRequestHookMCP_LargePayload_AutoInjectOff_NoFilters_StampsNothing(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"tool_a", "tool_b"}), true)
	ctx, metadata := newLargePayloadCtx(nil, nil)

	tools := stampedIncludeTools(t, p, ctx, newChatRequest())
	assert.Nil(t, tools)
	assert.Equal(t, "openai/gpt-4o", metadata.Model, "provider-prefixed model must survive routing unchanged")
}
