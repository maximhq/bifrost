package mcptests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildPerServerDelayServer creates an InProcess MCP server with a delay tool
// that respects context cancellation so per-server timeouts are observable.
func buildPerServerDelayServer(t *testing.T) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("delay-server", "1.0.0", server.WithToolCapabilities(true))
	delayTool := mcpgo.NewTool("delay",
		mcpgo.WithDescription("Sleeps for the given number of seconds, respects context cancellation"),
		mcpgo.WithNumber("seconds", mcpgo.Required(), mcpgo.Description("seconds to sleep")),
	)
	s.AddTool(delayTool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		seconds, _ := req.GetArguments()["seconds"].(float64)
		timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
		defer timer.Stop()
		select {
		case <-timer.C:
			return mcpgo.NewToolResultText("ok"), nil
		case <-ctx.Done():
			return mcpgo.NewToolResultError("timed out"), nil
		}
	})
	return s
}

// makeDelayToolCall builds a ChatAssistantMessageToolCall for the delay tool on the named client.
func makeDelayToolCall(clientName string, seconds float64) schemas.ChatAssistantMessageToolCall {
	args, _ := json.Marshal(map[string]interface{}{"seconds": seconds})
	toolName := clientName + "-delay"
	return schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-delay"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &toolName,
			Arguments: string(args),
		},
	}
}

// TestPerServerTimeout_OverridesGlobal verifies that a per-server ToolExecutionTimeout
// fires before the global timeout and before the tool naturally finishes.
// Setup: per-server = 1s, tool delay = 3s, context = 10s.
// Expected: call returns in < 2.5s (per-server timeout fires at ~1s).
func TestPerServerTimeout_OverridesGlobal(t *testing.T) {
	t.Parallel()

	clientName := "tsoverride"
	cfg := &schemas.MCPClientConfig{
		ID:                   clientName + "-id",
		Name:                 clientName,
		ConnectionType:       schemas.MCPConnectionTypeInProcess,
		InProcessServer:      buildPerServerDelayServer(t),
		ToolsToExecute:       []string{"*"},
		ToolExecutionTimeout: 1 * time.Second,
	}

	manager := setupMCPManager(t)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	bf := setupBifrost(t)
	bf.SetMCPManager(manager)

	ctx, cancel := createTestContextWithTimeout(10 * time.Second)
	defer cancel()

	start := time.Now()
	toolCall := makeDelayToolCall(clientName, 3.0)
	_, _ = bf.ExecuteChatMCPTool(ctx, &toolCall)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 2500*time.Millisecond,
		"per-server timeout (1s) should fire before the 3s tool delay; got %v", elapsed)
	t.Logf("elapsed: %v (expected < 2.5s)", elapsed)
}

// TestPerServerTimeout_AllowsLongerThanGlobal verifies that when the per-server
// timeout is longer than the tool's execution time the tool completes successfully.
// Setup: per-server = 3s, tool delay = 2s, context = 10s.
// Expected: call succeeds with no error.
func TestPerServerTimeout_AllowsLongerThanGlobal(t *testing.T) {
	t.Parallel()

	clientName := "tsallowslong"
	cfg := &schemas.MCPClientConfig{
		ID:                   clientName + "-id",
		Name:                 clientName,
		ConnectionType:       schemas.MCPConnectionTypeInProcess,
		InProcessServer:      buildPerServerDelayServer(t),
		ToolsToExecute:       []string{"*"},
		ToolExecutionTimeout: 3 * time.Second,
	}

	manager := setupMCPManager(t)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	bf := setupBifrost(t)
	bf.SetMCPManager(manager)

	ctx, cancel := createTestContextWithTimeout(10 * time.Second)
	defer cancel()

	toolCall := makeDelayToolCall(clientName, 2.0) // 2s delay < 3s timeout → must succeed
	result, bifrostErr := bf.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "tool should succeed when delay < per-server timeout")
	assert.NotNil(t, result, "should have a result")
}

// TestPerServerTimeout_FallsBackToGlobal verifies that a client with
// ToolExecutionTimeout = 0 falls back to the global timeout / context deadline.
// A short context (500 ms) is used to make the timeout observable without
// depending on the manager's exact configured global value.
// Setup: per-server = 0 (use global), tool delay = 5s, context = 500ms.
// Expected: call returns well under 2s (context fires).
func TestPerServerTimeout_FallsBackToGlobal(t *testing.T) {
	t.Parallel()

	clientName := "tsfallback"
	cfg := &schemas.MCPClientConfig{
		ID:                   clientName + "-id",
		Name:                 clientName,
		ConnectionType:       schemas.MCPConnectionTypeInProcess,
		InProcessServer:      buildPerServerDelayServer(t),
		ToolsToExecute:       []string{"*"},
		ToolExecutionTimeout: 0, // 0 = use global
	}

	manager := setupMCPManager(t)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	bf := setupBifrost(t)
	bf.SetMCPManager(manager)

	// Short context deadline — demonstrates that global/context applies when per-server = 0.
	ctx, cancel := createTestContextWithTimeout(500 * time.Millisecond)
	defer cancel()

	start := time.Now()
	toolCall := makeDelayToolCall(clientName, 5.0) // 5s delay
	_, _ = bf.ExecuteChatMCPTool(ctx, &toolCall)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 2*time.Second,
		"context deadline should cancel the tool (per-server=0 falls back to global); got %v", elapsed)
	t.Logf("elapsed: %v (expected < 2s)", elapsed)
}
