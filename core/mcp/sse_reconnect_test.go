package mcp

import (
	"context"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// SSE: a dropped event stream is repaired by the periodic checker, not by
// anything SSE-specific.
// =============================================================================

// TestSSEStreamDropped_CheckerReconnects covers the SSE half of the same gap
// the STDIO respawn test covers in the integration suite. mcp-go (v0.44.0 and
// later) invokes the OnConnectionLost hook on a server-side close, which flags
// the entry Unstable, but nothing converts transport death into Conn == nil, so
// the client sits on the checker's live-connection branch and that branch's
// reconnect is the only thing that can heal it. The assertions below pin both
// halves: the loss is flagged, and the checker then replaces the connection.
//
// httptest's CloseClientConnections drops the established stream while the
// server keeps accepting new ones, which is exactly the recoverable shape: an
// idle timeout or a proxy hang-up rather than a server that has gone away.
func TestSSEStreamDropped_CheckerReconnects(t *testing.T) {
	s := server.NewMCPServer("test-sse-reconnect", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(
		mcpgo.NewTool("echo", mcpgo.WithDescription("Echo tool")),
		func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		},
	)
	ts := server.NewTestServer(s)
	t.Cleanup(ts.Close)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID:               "client-sse-reconnect",
		Name:             "sse-client",
		AuthType:         schemas.MCPAuthTypeNone,
		ConnectionType:   schemas.MCPConnectionTypeSSE,
		ConnectionString: schemas.NewSecretVar(ts.URL + "/sse"),
		ToolsToExecute:   []string{"*"},
	}
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	// Registered after ts.Close above so it runs BEFORE it (t.Cleanup is LIFO):
	// a live SSE stream keeps httptest.Server.Close blocked indefinitely.
	t.Cleanup(func() {
		_ = m.RemoveClient(config.ID)
		ts.CloseClientConnections()
	})

	before, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	require.NotNil(t, before.Conn)
	require.Equal(t, schemas.MCPConnectionStateHealthy, before.State)

	// Drop every established stream. The server keeps listening, so a fresh
	// dial still succeeds.
	ts.CloseClientConnections()

	// Register a tool the dead session never saw. ToolMap is deliberately kept
	// as last-known-good when a connection fails, so asserting the original echo
	// tool at the end would pass on stale state; only a tool that appeared after
	// the drop proves list_tools actually re-ran over the replacement.
	s.AddTool(
		mcpgo.NewTool("reconnected", mcpgo.WithDescription("registered only after the stream was dropped")),
		func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		},
	)

	// Claude Sonnet 5.5 10/11/2026: mcp-go v0.44.0 fires OnConnectionLost on a
	// server-side close (mark3labs/mcp-go#709), so the drop is now noticed: the
	// entry goes Unstable with a transport-lost failure recorded. The loss
	// callback only flags it; the connection is not detached and the generation
	// does not move, so the checker's reconnect is still the only thing that
	// heals it.
	require.Eventually(t, func() bool {
		st, exists := snapshotClientState(m, config.ID)
		return exists &&
			st.State == schemas.MCPConnectionStateUnstable &&
			st.LastFailure != nil &&
			st.LastFailure.Stage == schemas.MCPConnectionFailureStageTransportLost &&
			st.ConnGeneration == before.ConnGeneration &&
			st.Conn != nil
	}, 10*time.Second, 50*time.Millisecond, "a dropped SSE stream must be flagged Unstable with a transport-lost failure, on the same connection, until the checker reconnects")

	// Why the post-reconnect tool assertion is not vacuous: right now the entry
	// still carries the pre-drop list, and cannot possibly carry the tool that
	// was registered after it. Asserting the original echo tool at the end would
	// therefore pass on this stale state alone.
	stale, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	require.Contains(t, stale.ToolMap, config.Name+"-echo", "the pre-drop tool list is retained as last-known-good")
	require.NotContains(t, stale.ToolMap, config.Name+"-reconnected", "and cannot contain a tool registered after the drop until rediscovery runs")

	checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})
	checker.performCheck()

	require.Eventually(t, func() bool {
		after, exists := snapshotClientState(m, config.ID)
		return exists &&
			after.State == schemas.MCPConnectionStateHealthy &&
			after.ConnGeneration > before.ConnGeneration
	}, 30*time.Second, 100*time.Millisecond, "the checker's reconnect must re-establish the stream")

	after, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.NotSame(t, before.Conn, after.Conn, "the dead stream must be replaced, not re-probed")
	assert.Contains(t, after.ToolMap, config.Name+"-reconnected",
		"rediscovery must run over the replacement connection, and only a tool registered after the drop can show that")
	assert.Contains(t, after.ToolMap, config.Name+"-echo", "without losing what the client already had")
}
