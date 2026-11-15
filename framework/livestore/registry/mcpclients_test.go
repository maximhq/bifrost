package registry

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// mcpClient is a stored MCP client with an ID, a name and a slug.
func mcpClient(id string) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID: id, Name: id, EndpointSlug: id + "-slug", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://" + id + ".example/mcp"), AuthType: schemas.MCPAuthTypeNone,
		ToolsToExecute: schemas.WhiteList{"*"},
	}
}

// TestMCPClientsPageWalksEveryClientInIDOrder checks that pages follow the client ID cursor to the
// end, whatever order the clients were created in.
func TestMCPClientsPageWalksEveryClientInIDOrder(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, nil)
	for _, id := range []string{"c", "a", "b"} {
		require.NoError(t, store.CreateMCPClientConfig(ctx, mcpClient(id)))
	}

	var ids []string
	after := ""
	for {
		rows, next, err := MCPClientsPage(ctx, store.DB(), after, 2)
		require.NoError(t, err)
		for _, c := range rows {
			ids = append(ids, MCPClientID(c))
		}
		if next == "" {
			break
		}
		after = next
	}
	require.Equal(t, []string{"a", "b", "c"}, ids)
}

// TestMCPClientsByIDLeavesOutMissingRows checks that an ID with no row is simply absent, which is how
// a refresh learns that a client was deleted, and that a record carries its stored row without the
// discovered tools, which the discovery store keeps.
func TestMCPClientsByIDLeavesOutMissingRows(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, nil)
	discovered := mcpClient("a")
	discovered.DiscoveredTools = map[string]schemas.ChatTool{"a-search": {Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "a-search"}}}
	discovered.DiscoveredInstructions = "Read first."
	require.NoError(t, store.CreateMCPClientConfig(ctx, discovered))

	rows, err := MCPClientsByID(ctx, store.DB(), []string{"a", "gone"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "a", MCPClientName(rows[0]))
	require.Equal(t, "a-slug", MCPClientSlug(rows[0]))
	require.Equal(t, "https://a.example/mcp", rows[0].Config.ConnectionString.GetValue())
	stored, err := store.GetMCPClientConfigByID(ctx, "a")
	require.NoError(t, err)
	require.Len(t, stored.DiscoveredTools, 1, "fixture check: the row holds the tools")
	require.Nil(t, rows[0].Config.DiscoveredTools, "the record carries no tools")
	require.Empty(t, rows[0].Config.DiscoveredInstructions)
}
