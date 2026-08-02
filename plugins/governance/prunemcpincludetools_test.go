package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCtxWithIncludeTools returns a BifrostContext pre-stamped with a caller-provided
// include-tools list, mirroring what lib/ctx.go does for the x-bf-mcp-include-tools header.
func newCtxWithIncludeTools(tools []string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.MCPContextKeyIncludeTools, tools)
	return ctx
}

// includeToolsFromCtx reads back the (possibly pruned) include-tools list from ctx.
func includeToolsFromCtx(t *testing.T, ctx *schemas.BifrostContext) []string {
	t.Helper()
	value := ctx.Value(schemas.MCPContextKeyIncludeTools)
	require.NotNil(t, value, "include-tools ctx value should be set")
	tools, ok := value.([]string)
	require.True(t, ok, "include-tools ctx value should be a []string")
	return tools
}

// No caller-provided list on ctx → returns false and leaves ctx untouched.
func TestPruneMCPIncludeTools_NoCallerList(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"find_projects"})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	assert.False(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Nil(t, ctx.Value(schemas.MCPContextKeyIncludeTools),
		"ctx should remain unset when the caller provided no list")
}

// A tool the VK does not grant is dropped; the result is an empty (deny-all) list.
func TestPruneMCPIncludeTools_DisallowedToolDropped(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"find_projects"})
	ctx := newCtxWithIncludeTools([]string{"sentry-search_tools"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Empty(t, includeToolsFromCtx(t, ctx),
		"a tool outside the VK grant must be pruned, leaving a deny-all list")
}

// A tool the VK explicitly grants survives pruning.
func TestPruneMCPIncludeTools_GrantedToolKept(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"find_projects", "search_issues"})
	ctx := newCtxWithIncludeTools([]string{"sentry-find_projects", "sentry-search_tools"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Equal(t, []string{"sentry-find_projects"}, includeToolsFromCtx(t, ctx),
		"granted entries survive, ungranted entries are dropped")
}

// A specific tool requested under an unrestricted ("*") VK grant survives — the
// header narrows within the wildcard grant.
func TestPruneMCPIncludeTools_SpecificToolUnderUnrestrictedGrant(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"*"})
	ctx := newCtxWithIncludeTools([]string{"sentry-search_tools"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Equal(t, []string{"sentry-search_tools"}, includeToolsFromCtx(t, ctx),
		"specific request should be allowed by the client's unrestricted grant")
}

// A caller wildcard is kept verbatim only when the VK itself is unrestricted for that client.
func TestPruneMCPIncludeTools_WildcardKeptWhenVKUnrestricted(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"*"})
	ctx := newCtxWithIncludeTools([]string{"sentry-*"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Equal(t, []string{"sentry-*"}, includeToolsFromCtx(t, ctx))
}

// A caller wildcard against a specific VK grant is narrowed to the grant's entries —
// passing the wildcard through would read downstream as "all tools of this client".
func TestPruneMCPIncludeTools_WildcardNarrowedToSpecificGrants(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"find_projects", "search_issues"})
	ctx := newCtxWithIncludeTools([]string{"sentry-*"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Equal(t, []string{"sentry-find_projects", "sentry-search_issues"}, includeToolsFromCtx(t, ctx))
}

// A caller wildcard for a client the VK does not grant at all yields nothing.
func TestPruneMCPIncludeTools_WildcardForUngrantedClientDropped(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"find_projects"})
	ctx := newCtxWithIncludeTools([]string{"github-*"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Empty(t, includeToolsFromCtx(t, ctx))
}

// An empty header value (parsed as [""] by lib/ctx.go) prunes to a deny-all list,
// letting callers suppress tool injection for a request.
func TestPruneMCPIncludeTools_EmptyHeaderOptOut(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"*"})
	ctx := newCtxWithIncludeTools([]string{""})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Empty(t, includeToolsFromCtx(t, ctx))
}

// Wildcard expansion plus an overlapping specific request must not produce duplicates.
func TestPruneMCPIncludeTools_DedupAcrossWildcardAndSpecific(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"find_projects", "search_issues"})
	ctx := newCtxWithIncludeTools([]string{"sentry-*", "sentry-find_projects"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Equal(t, []string{"sentry-find_projects", "sentry-search_issues"}, includeToolsFromCtx(t, ctx))
}

// AllowOnAllVirtualKeys client with no explicit VK config: both specific and wildcard
// requests survive (the implicit grant is client-wide).
func TestPruneMCPIncludeTools_AllowOnAllVirtualKeysClient(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	})
	vk := buildVKNoMCPConfigs()
	ctx := newCtxWithIncludeTools([]string{"youtube-search", "youtube-*", "github-list_repos"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Equal(t, []string{"youtube-search", "youtube-*"}, includeToolsFromCtx(t, ctx),
		"AllowOnAllVirtualKeys grants the whole client; other clients are still pruned")
}

// An explicit empty VK config (deny-all) overrides the client's AllowOnAllVirtualKeys flag.
func TestPruneMCPIncludeTools_ExplicitEmptyConfigOverridesAllowAll(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	})
	vk := buildVKWithMCPConfigs("client-1", "youtube", []string{})
	ctx := newCtxWithIncludeTools([]string{"youtube-search", "youtube-*"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Empty(t, includeToolsFromCtx(t, ctx))
}

// Pruning spans multiple VK clients independently.
func TestPruneMCPIncludeTools_MultipleClients(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-multi",
		Name: "test-vk-multi",
		MCPConfigs: []configstoreTables.TableVirtualKeyMCPConfig{
			{
				MCPClient:      configstoreTables.TableMCPClient{ClientID: "client-1", Name: "sentry"},
				ToolsToExecute: []string{"find_projects"},
			},
			{
				MCPClient:      configstoreTables.TableMCPClient{ClientID: "client-2", Name: "github"},
				ToolsToExecute: []string{"*"},
			},
		},
	}
	ctx := newCtxWithIncludeTools([]string{"sentry-find_projects", "sentry-search_issues", "github-list_repos", "github-*"})

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Equal(t, []string{"sentry-find_projects", "github-list_repos", "github-*"}, includeToolsFromCtx(t, ctx))
}

// A ctx value of the wrong type fails closed: treated as a present-but-empty caller list.
func TestPruneMCPIncludeTools_WrongTypeFailsClosed(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{})
	vk := buildVKWithMCPConfigs("client-1", "sentry", []string{"*"})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.MCPContextKeyIncludeTools, "sentry-search_tools")

	assert.True(t, p.pruneMCPIncludeToolsFromContext(ctx, vk))
	assert.Empty(t, includeToolsFromCtx(t, ctx),
		"a malformed ctx value must prune to deny-all, not pass through")
}
