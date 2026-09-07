package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Reactive repair: a tool call that fails because the connection underneath it
// is dead. Uses sessionInvalidator / buildExpiringSessionMCPServer from
// connectionchecker_reconnect_test.go (same package).
// =============================================================================

// buildRecoveryMCPServer is buildExpiringSessionMCPServer with control over
// the echo tool's annotations, which decide whether the auto-retry is allowed
// at all: attemptCallFailureRecovery fails closed on missing hints (MCP's own
// defaults are destructiveHint=true, idempotentHint=false), so an unannotated
// tool is never retried automatically. Reuses sessionInvalidator from
// connectionchecker_reconnect_test.go.
func buildRecoveryMCPServer(t *testing.T, toolOpts ...mcpgo.ToolOption) (*httptest.Server, *sessionInvalidator) {
	t.Helper()

	s := server.NewMCPServer("test-toolcall-recovery", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(
		mcpgo.NewTool("echo", append([]mcpgo.ToolOption{mcpgo.WithDescription("Echo tool")}, toolOpts...)...),
		func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		},
	)

	streamable := server.NewStreamableHTTPServer(s)
	invalidator := &sessionInvalidator{dead: map[string]bool{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invalidator.reject(w, r) {
			return
		}
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, invalidator
}

func newRecoveryClientConfig(id, name, url string) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:                     id,
		Name:                   name,
		AuthType:               schemas.MCPAuthTypeHeaders,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(url),
		NeedsSessionStickiness: schemas.Ptr(true),
		ToolsToExecute:         []string{"*"},
	}
}

// executeEchoTool runs the client's echo tool through the real execution path
// (ExecuteChatTool -> prepareToolExecution -> executeToolInternal), unlike
// callEchoTool in makebeforebreak_test.go which talks to the connection
// directly. The recovery under test lives in executeToolInternal, so it is
// only reachable this way.
func executeEchoTool(m *MCPManager, toolName string) (*schemas.ChatMessage, *schemas.BifrostError) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	return m.ExecuteChatTool(bfCtx, &schemas.ChatAssistantMessageToolCall{
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &toolName,
			Arguments: "{}",
		},
	})
}

// TestExecuteTool_DeadSession_SalvagesTheCallForARetryableTool pins the
// reactive half of MCP connection repair. A tool call is the strongest
// evidence available that a connection is dead: a real request, over the real
// connection, from a real caller. Bifrost already acted on one class of call
// failure this way (a clean upstream auth rejection), reconnecting and
// retrying the same call once so the caller never saw the failure.
//
// A session the upstream had abandoned got none of that. It is not an auth
// rejection, so the recovery never ran, and it matches no transient substring,
// so ToolCallRetryConfig did not even retry it. The call failed outright and
// every later call failed the same way until the periodic checker happened to
// reconnect, up to a full check interval away.
func TestExecuteTool_DeadSession_SalvagesTheCallForARetryableTool(t *testing.T) {
	// Read-only: safe to auto-retry, so the caller's own request is salvaged.
	ts, upstream := buildRecoveryMCPServer(t, mcpgo.WithReadOnlyHintAnnotation(true))

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := newRecoveryClientConfig("client-dead-session-salvage", "salvage-client", ts.URL)
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	toolName := config.Name + "-echo"

	_, bErr := executeEchoTool(m, toolName)
	require.Nil(t, bErr, "sanity: the tool works before the session dies")

	before, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)

	// The upstream expires the session behind the live connection. A fresh
	// dial would still succeed, so this is recoverable with no human.
	upstream.expireCurrentSession()

	msg, bErr := executeEchoTool(m, toolName)
	require.Nil(t, bErr, "a dead session must be reconnected and the call retried, not surfaced to the caller")
	require.NotNil(t, msg)

	after, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Greater(t, after.ConnGeneration, before.ConnGeneration, "recovery means a genuinely fresh connection, not a lucky retry")
	assert.NotSame(t, before.Conn, after.Conn)
}

// TestExecuteTool_DeadSession_HealsEvenWhenTheRetryIsSuppressed covers the
// other side of the safety gate. A tool with no annotations is treated as
// destructive and non-idempotent (MCP's own defaults, and
// attemptCallFailureRecovery fails closed on missing hints), so replaying the
// call could cause a real-world side effect twice and the auto-retry is
// suppressed. The reconnect must still run: the caller eats this one failure,
// but the connection is repaired behind it so the next call succeeds instead
// of waiting on the periodic checker.
func TestExecuteTool_DeadSession_HealsEvenWhenTheRetryIsSuppressed(t *testing.T) {
	ts, upstream := buildRecoveryMCPServer(t) // unannotated: assumed destructive

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := newRecoveryClientConfig("client-dead-session-heal", "heal-client", ts.URL)
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	toolName := config.Name + "-echo"

	_, bErr := executeEchoTool(m, toolName)
	require.Nil(t, bErr, "sanity: the tool works before the session dies")

	before, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)

	upstream.expireCurrentSession()

	_, bErr = executeEchoTool(m, toolName)
	require.NotNil(t, bErr, "a destructive, non-idempotent tool must not be replayed automatically")

	require.Eventually(t, func() bool {
		after, exists := snapshotClientState(m, config.ID)
		return exists && after.ConnGeneration > before.ConnGeneration
	}, 15*time.Second, 100*time.Millisecond, "the reconnect must run regardless of whether the retry was allowed")

	_, bErr = executeEchoTool(m, toolName)
	require.Nil(t, bErr, "the next call must land on the healed connection")
}

// buildSlowReconnectMCPServer is buildRecoveryMCPServer with a knob that stalls
// the `initialize` of any NEW connection. A sessionless request is exactly the
// initialize of a fresh dial, so this lets a test make the recovery reconnect
// outlast the tool execution budget while the original session stays expired.
func buildSlowReconnectMCPServer(t *testing.T, toolOpts ...mcpgo.ToolOption) (*httptest.Server, *sessionInvalidator, *atomic.Int64) {
	t.Helper()

	s := server.NewMCPServer("test-slow-reconnect", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(
		mcpgo.NewTool("echo", append([]mcpgo.ToolOption{mcpgo.WithDescription("Echo tool")}, toolOpts...)...),
		func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		},
	)

	streamable := server.NewStreamableHTTPServer(s)
	invalidator := &sessionInvalidator{dead: map[string]bool{}}
	var initDelay atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invalidator.reject(w, r) {
			return
		}
		if d := initDelay.Load(); d > 0 && r.Header.Get("Mcp-Session-Id") == "" {
			time.Sleep(time.Duration(d))
		}
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, invalidator, &initDelay
}

// TestExecuteTool_DeadSession_RecoveryHonoursTheToolExecutionTimeout pins the
// budget the recovery runs under. `tool_execution_timeout` is what an operator
// sets to bound a tool call's worst-case latency, and the first attempt already
// runs inside it. Recovery must spend what is left of that same budget, not
// start a fresh one: waiting the full reconnect budget and then granting the
// retry another complete timeout makes a recovered call take up to twice the
// configured bound plus the wait, and in agent mode that overshoot is paid once
// per step.
//
// The upstream here stalls every new connection's initialize for longer than
// the tool budget, so a recovery bounded by the budget gives up inside it while
// an unbounded one waits out the reconnect and then retries.
func TestExecuteTool_DeadSession_RecoveryHonoursTheToolExecutionTimeout(t *testing.T) {
	const toolBudget = 2 * time.Second

	ts, upstream, initDelay := buildSlowReconnectMCPServer(t, mcpgo.WithReadOnlyHintAnnotation(true))

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := newRecoveryClientConfig("client-dead-session-deadline", "deadline-client", ts.URL)
	config.ToolExecutionTimeout = toolBudget
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	toolName := config.Name + "-echo"

	_, bErr := executeEchoTool(m, toolName)
	require.Nil(t, bErr, "sanity: the tool works before the session dies")

	upstream.expireCurrentSession()
	initDelay.Store(int64(3 * toolBudget)) // the reconnect cannot finish inside the budget

	start := time.Now()
	_, _ = executeEchoTool(m, toolName)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 2*toolBudget,
		"a recovered call must stay within tool_execution_timeout, not wait out the reconnect budget and then start a fresh one")

	// The budget bounds the caller's wait, it does not abandon the repair: the
	// reconnect keeps running and the next call lands on the healed connection.
	initDelay.Store(0)
	require.Eventually(t, func() bool {
		_, err := executeEchoTool(m, toolName)
		return err == nil
	}, 30*time.Second, 250*time.Millisecond, "the background reconnect must still heal the client")
}
