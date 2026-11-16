package handlers

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	bifrost "github.com/maximhq/bifrost/core"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// x-bf-mcp-tool-names: server lists a /mcp/<slug> client's tools by the names their own server gave
// them, and every listed name is callable. Without the header, or on /mcp and Virtual MCPs, nothing
// changes.
func TestMCPGateway_ServerToolNamesHeader(t *testing.T) {
	ctx := context.Background()
	appsURL := startNamedUpstream(t, "apps", mcp.NewTool("view"), mcp.NewTool("status"))
	// other's "apps-view" is installed as other-apps-view; the name apps-view stays apps's view, so
	// /mcp/other cannot list it as apps-view.
	otherURL := startNamedUpstream(t, "other", mcp.NewTool("status"), mcp.NewTool("apps-view"))

	m := coremcp.NewMCPManager(ctx, schemas.MCPConfig{}, nil, bifrost.NewDefaultLogger(schemas.LogLevelError), nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	for name, url := range map[string]string{"apps": appsURL, "other": otherURL} {
		require.NoError(t, m.AddClient(ctx, &schemas.MCPClientConfig{
			ID: name, Name: name, EndpointSlug: name, ConnectionType: schemas.MCPConnectionTypeHTTP,
			ConnectionString: schemas.NewSecretVar(url), ToolsToExecute: []string{"*"},
		}))
	}
	cfg := &lib.Config{ClientConfig: &configstore.ClientConfig{}, MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{
		{ID: "apps", Name: "apps", EndpointSlug: "apps"}, {ID: "other", Name: "other", EndpointSlug: "other"},
	}}}
	admitter := slugAdmitter{
		vmcps:   map[string][]string{"bundle": {"apps-status", "other-status"}},
		clients: map[string][]string{"apps": {"apps-*"}, "other": {"other-*"}},
	}
	tm := &recordingGatewayManager{coreGatewayManager: coreGatewayManager{m}}
	h, err := NewMCPServerHandler(ctx, cfg, tm, admitter, nil, nil)
	require.NoError(t, err)
	baseURL := startGateway(t, h)

	connect := func(t *testing.T, path, toolNames string) (*client.Client, error) {
		var options []transport.StreamableHTTPCOption
		if toolNames != "" {
			options = append(options, transport.WithHTTPHeaders(map[string]string{mcpToolNamesHeader: toolNames}))
		}
		tr, err := transport.NewStreamableHTTP(baseURL+path, options...)
		require.NoError(t, err)
		c := client.NewClient(tr)
		require.NoError(t, c.Start(ctx))
		t.Cleanup(func() { _ = c.Close() })
		init := mcp.InitializeRequest{}
		init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		_, err = c.Initialize(ctx, init)
		return c, err
	}
	listed := func(t *testing.T, c *client.Client) []string {
		tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		require.NoError(t, err)
		names := make([]string, 0, len(tools.Tools))
		for _, tool := range tools.Tools {
			names = append(names, tool.Name)
		}
		slices.Sort(names)
		return names
	}

	t.Run("lists and calls the server's own names", func(t *testing.T) {
		c, err := connect(t, "/mcp/apps", mcpToolNamesServer)
		require.NoError(t, err)
		assert.Equal(t, []string{"status", "view"}, listed(t, c))
		for _, name := range []string{"status", "view"} {
			tm.mu.Lock()
			tm.names = nil
			tm.mu.Unlock()
			req := mcp.CallToolRequest{}
			req.Params.Name = name
			result, err := c.CallTool(ctx, req)
			require.NoError(t, err, name)
			assert.False(t, result.IsError, resultText(result))
			assert.Equal(t, "apps:"+name, resultText(result))
			tm.mu.Lock()
			assert.Equal(t, []string{"apps-" + name}, tm.names, "plugins and logs still see the prefixed name")
			tm.mu.Unlock()
		}
	})

	t.Run("a name that is another tool's exact name keeps its full name", func(t *testing.T) {
		c, err := connect(t, "/mcp/other", mcpToolNamesServer)
		require.NoError(t, err)
		assert.Equal(t, []string{"other-apps-view", "status"}, listed(t, c))
		req := mcp.CallToolRequest{}
		req.Params.Name = "other-apps-view"
		result, err := c.CallTool(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, "other:apps-view", resultText(result))
	})

	t.Run("without the header, or with prefixed, nothing changes", func(t *testing.T) {
		for _, value := range []string{"", mcpToolNamesPrefixed} {
			c, err := connect(t, "/mcp/apps", value)
			require.NoError(t, err)
			assert.Equal(t, []string{"apps-status", "apps-view"}, listed(t, c), "header %q", value)
		}
	})

	t.Run("refused where names could be ambiguous, or for an unknown value", func(t *testing.T) {
		for _, tc := range []struct{ path, value string }{
			{"/mcp", mcpToolNamesServer},
			{"/mcp/bundle", mcpToolNamesServer},
			{"/mcp/apps", "bare"},
		} {
			req, err := http.NewRequest(http.MethodPost, baseURL+tc.path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set(mcpToolNamesHeader, tc.value)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "%s with %q: %s", tc.path, tc.value, body)
			assert.Contains(t, string(body), mcpToolNamesHeader, "%s with %q", tc.path, tc.value)
		}
	})
}
