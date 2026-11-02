package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/require"
)

// coreToolManager serves the gateway from a real Bifrost core, converting each request's context
// the way the HTTP server does.
type coreToolManager struct{ b *bifrost.Bifrost }

// GetAvailableMCPTools lists the core's tools for ctx.
func (m coreToolManager) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	return m.b.GetAvailableMCPTools(schemas.NewBifrostContext(ctx, schemas.NoDeadline))
}

// GetMCPServerInstructions returns the core's aggregated upstream instructions for ctx.
func (m coreToolManager) GetMCPServerInstructions(ctx context.Context) string {
	return m.b.GetMCPServerInstructions(schemas.NewBifrostContext(ctx, schemas.NoDeadline))
}

// ExecuteChatMCPTool runs a tool call through the core.
func (m coreToolManager) ExecuteChatMCPTool(ctx context.Context, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	return m.b.ExecuteChatMCPTool(schemas.NewBifrostContext(ctx, schemas.NoDeadline), toolCall)
}

// ExecuteResponsesMCPTool runs a Responses-format tool call through the core.
func (m coreToolManager) ExecuteResponsesMCPTool(ctx context.Context, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	return m.b.ExecuteResponsesMCPTool(schemas.NewBifrostContext(ctx, schemas.NoDeadline), toolCall)
}

// TestMCPGateway_UpstreamToolChangeReachesListeners follows an upstream tool change all the way
// through: the upstream announces it, Bifrost re-lists that client on its own, and a client holding
// a /mcp notification stream is told the tools changed and then finds the new tool, with no admin
// refresh, on a legacy and on the current protocol revision.
func TestMCPGateway_UpstreamToolChangeReachesListeners(t *testing.T) {
	ctx := context.Background()
	upstream := server.NewMCPServer("alpha", "1.0.0", server.WithToolCapabilities(true))
	addTool := func(name string) {
		upstream.AddTool(mcp.NewTool(name), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(name), nil
		})
	}
	addTool("echo")
	ts := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	t.Cleanup(ts.Close)

	b, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account: lib.NewBaseAccount(&lib.Config{}),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
		MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{{
			ID:                     "alpha",
			Name:                   "alpha",
			ConnectionType:         schemas.MCPConnectionTypeHTTP,
			ConnectionString:       schemas.NewSecretVar(ts.URL),
			AuthType:               schemas.MCPAuthTypeNone,
			NeedsSessionStickiness: schemas.Ptr(true),
			ToolsToExecute:         []string{"*"},
		}}},
	})
	require.NoError(t, err)
	t.Cleanup(b.Shutdown)

	h, err := NewMCPServerHandler(ctx, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, coreToolManager{b}, nil, nil, nil)
	require.NoError(t, err)
	// As the HTTP server wires it: a discovery change re-syncs the served /mcp server.
	b.SetMCPDiscoveryChangeCallback(func(string, string, *schemas.MCPDiscovery) { go func() { _ = h.SyncMCPServer(ctx) }() })
	b.ConnectConfiguredMCPClients(ctx, func(string) { _ = h.SyncMCPServer(ctx) })
	baseURL := startGateway(t, h)

	for i, version := range []string{mcp.ProtocolVersion20251125, mcp.ProtocolVersion20260728} {
		t.Run(version, func(t *testing.T) {
			// Its own connection pool, closed at the end, so no idle keep-alive connection holds the
			// gateway's shutdown open.
			pool := &http.Transport{}
			t.Cleanup(pool.CloseIdleConnections)
			opts := []transport.StreamableHTTPCOption{transport.WithHTTPBasicClient(&http.Client{Transport: pool})}
			if !mcp.IsModernProtocol(version) {
				opts = append(opts, transport.WithContinuousListening())
			}
			tr, err := transport.NewStreamableHTTP(baseURL+"/mcp", opts...)
			require.NoError(t, err)
			c := client.NewClient(tr, client.WithProtocolVersion(version))
			var changed atomic.Bool
			c.OnNotification(func(n mcp.JSONRPCNotification) {
				if n.Method == string(mcp.MethodNotificationToolsListChanged) {
					changed.Store(true)
				}
			})
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })
			init := mcp.InitializeRequest{}
			init.Params.ProtocolVersion = version
			_, err = c.Initialize(ctx, init)
			require.NoError(t, err)
			if mcp.IsModernProtocol(version) {
				stop, err := c.ListenAsync(ctx, mcp.SubscriptionFilter{ToolsListChanged: true}, nil)
				require.NoError(t, err)
				t.Cleanup(stop)
			}
			names := func() []string {
				listed, err := c.ListTools(ctx, mcp.ListToolsRequest{})
				require.NoError(t, err)
				var out []string
				for _, tool := range listed.Tools {
					out = append(out, tool.Name)
				}
				return out
			}
			added := "added" + string(rune('a'+i))
			require.Contains(t, names(), "alpha-echo")
			require.NotContains(t, names(), "alpha-"+added)

			// The stream opens asynchronously, so re-sync until a notification lands, then clear the
			// flag so the next check sees only the upstream change.
			require.Eventually(t, func() bool {
				require.NoError(t, h.SyncMCPServer(ctx))
				return changed.Load()
			}, 5*time.Second, 50*time.Millisecond, "the /mcp stream is open")
			changed.Store(false)

			addTool(added)
			require.Eventually(t, changed.Load, 5*time.Second, 50*time.Millisecond, "the /mcp stream announces the change")
			require.Eventually(t, func() bool { return slices.Contains(names(), "alpha-"+added) }, 5*time.Second, 50*time.Millisecond, "the new tool is listed without an admin refresh")
		})
	}
}
