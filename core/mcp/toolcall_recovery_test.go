package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
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
	// connectToMCPClient starts a connection checker per client; without this
	// its goroutine outlives the test and can probe an httptest server that
	// t.Cleanup has already closed.
	t.Cleanup(func() { _ = m.Cleanup() })
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
	// connectToMCPClient starts a connection checker per client; without this
	// its goroutine outlives the test and can probe an httptest server that
	// t.Cleanup has already closed.
	t.Cleanup(func() { _ = m.Cleanup() })
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
	// connectToMCPClient starts a connection checker per client; without this
	// its goroutine outlives the test and can probe an httptest server that
	// t.Cleanup has already closed.
	t.Cleanup(func() { _ = m.Cleanup() })
	config := newRecoveryClientConfig("client-dead-session-deadline", "deadline-client", ts.URL)
	config.ToolExecutionTimeout = toolBudget
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	toolName := config.Name + "-echo"

	_, bErr := executeEchoTool(m, toolName)
	require.Nil(t, bErr, "sanity: the tool works before the session dies")

	upstream.expireCurrentSession()
	initDelay.Store(int64(3 * toolBudget)) // the reconnect cannot finish inside the budget

	start := time.Now()
	_, bErr = executeEchoTool(m, toolName)
	elapsed := time.Since(start)

	// Without this the elapsed-time assertion below passes for a regression that
	// gives up immediately with the wrong sentinel, and the trailing healed-call
	// assertion still passes on the background reconnect. The budget being spent
	// is what makes this a timeout rather than a tool error, so classify it.
	require.NotNil(t, bErr, "the caller must still see the failure the recovery could not salvage")
	assert.Equal(t, "timeout", mcpErrorType(bErr),
		"a call whose recovery consumed the tool budget is a timeout, not a generic tool error")

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

// TestIsDeadSessionError_TypedSentinelAndTextFallback pins both halves of the
// dead-session check and, more importantly, why both have to exist and where
// the text half is allowed to look.
//
// The typed half matches mcp-go's ErrSessionTerminated through its own wrapping
// without depending on how that sentinel is worded. The text half is the only
// signal a server gives when it answers a dead session with something other
// than the spec's 404: a 422 carrying the server's own prose, which mcp-go
// formats straight into the message with no typed error anywhere.
//
// The text half must only read transport-framed errors. client.sendRequest
// returns exactly two shapes: a transport failure wrapped in *transport.Error,
// or the tool's own JSON-RPC error, flattened to its message with no wrapper
// (mcp.JSONRPCErrorDetails.AsError). A tool that legitimately answers "invalid
// session" or "session not found" about its OWN domain arrives in the second
// shape, and must not be mistaken for a dead MCP session: the shared-path
// recovery would reconnect a healthy client and, for a retry-safe tool,
// replay the call.
func TestIsDeadSessionError_TypedSentinelAndTextFallback(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			// Exactly the shape a 404 arrives in: the sentinel, wrapped by
			// SendRequest with %w and again by client.sendRequest in a
			// *transport.Error that implements Unwrap.
			name: "typed sentinel survives mcp-go's wrapping",
			err:  transport.NewError(fmt.Errorf("failed to send request: %w", transport.ErrSessionTerminated)),
			want: true,
		},
		{
			// A non-compliant server: no typed error exists anywhere in this
			// chain, so the text is the only thing to go on. The shape is
			// exactly what client.sendRequest produces for a non-404 status:
			// SendRequest's formatted prose inside *transport.Error.
			name: "non-404 server is caught by text inside the transport frame",
			err:  transport.NewError(errors.New("request failed with status 422: Unexpected message, expect initialize request")),
			want: true,
		},
		{
			// Same 422, but ExecuteWithRetry and callers may wrap the
			// transport error further; the frame must still be found.
			name: "transport frame is found through outer wrapping",
			err:  fmt.Errorf("attempt 2: %w", transport.NewError(errors.New("request failed with status 422: Unexpected message, expect initialize request"))),
			want: true,
		},
		{
			name: "an ordinary transport failure is not a dead session",
			err:  transport.NewError(errors.New("failed to send request: connection reset by peer")),
			want: false,
		},
		{
			// Finding the frame is not a licence to read the whole chain.
			// The needle here is in a wrapper's prose, outside the frame; the
			// transport itself reported nothing session-shaped. Only the
			// frame's own text may match.
			name: "a needle outside the transport frame does not count",
			err:  fmt.Errorf("session not found in retry bookkeeping: %w", transport.NewError(errors.New("failed to send request: connection reset by peer"))),
			want: false,
		},
		{
			// Same rule through errors.Join, which errors.As also walks: a
			// tool's own error joined to an unrelated transport failure must
			// not have the tool's text matched on the transport's behalf.
			name: "a tool error joined to a benign transport error does not count",
			err: errors.Join(
				fmt.Errorf("%w: invalid session key for user", mcpgo.ErrInternalError),
				transport.NewError(errors.New("failed to send request: connection reset by peer")),
			),
			want: false,
		},
		{
			// Near miss: the tool's own JSON-RPC error, flattened by
			// AsError into "internal error: <tool's message>" with no
			// transport frame. The MCP session is fine; the tool is talking
			// about a session of its own.
			name: "a tool's own 'invalid session' error is not a dead MCP session",
			err:  fmt.Errorf("%w: invalid session key for user", mcpgo.ErrInternalError),
			want: false,
		},
		{
			name: "a tool's own 'session not found' error is not a dead MCP session",
			err:  errors.New("session not found in cache: sess_9f2a"),
			want: false,
		},
		{
			name: "a tool's own 'unknown session' error is not a dead MCP session",
			err:  fmt.Errorf("%w: unknown session id", mcpgo.ErrInvalidParams),
			want: false,
		},
		{
			// The bare, unframed text is not enough on its own even when it
			// is word-for-word the transport's prose: only the frame says
			// the transport, not the tool, produced it.
			name: "dead-session prose without a transport frame is not trusted",
			err:  errors.New("request failed with status 422: Unexpected message, expect initialize request"),
			want: false,
		},
		{
			name: "nil is not a dead session",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isDeadSessionError(tc.err))
		})
	}
}

// TestExecuteTool_ToolErrorMentioningSession_DoesNotTriggerRecovery is the
// end-to-end form of the near-miss cases above. A tool whose own error talks
// about a session ("session not found") is served by a healthy MCP session
// over a healthy connection. The call must fail as an ordinary tool error:
// no reconnect, no replay, same connection generation afterwards.
//
// Read-only annotation is deliberate. It is the case where a misclassification
// would do the most damage: the recovery gate would allow a synchronous retry,
// so a false positive here means a replayed call on top of a needless
// reconnect.
func TestExecuteTool_ToolErrorMentioningSession_DoesNotTriggerRecovery(t *testing.T) {
	s := server.NewMCPServer("test-tool-error-near-miss", "1.0.0", server.WithToolCapabilities(true))
	var calls atomic.Int32
	s.AddTool(
		mcpgo.NewTool("lookup", mcpgo.WithDescription("Session lookup tool"), mcpgo.WithReadOnlyHintAnnotation(true)),
		func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			calls.Add(1)
			// The tool's own domain error. mcp-go's server wraps it as a
			// JSON-RPC INTERNAL_ERROR carrying this text verbatim.
			return nil, errors.New("session not found: sess_9f2a")
		},
	)
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := newRecoveryClientConfig("client-tool-error-near-miss", "near-miss-client", ts.URL)
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	toolName := config.Name + "-lookup"

	before, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)

	_, bErr := executeEchoTool(m, toolName)
	require.NotNil(t, bErr, "the tool's own error must reach the caller")
	assert.Contains(t, bErr.Error.Message, "session not found", "the tool's own message must be preserved")

	after, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Equal(t, before.ConnGeneration, after.ConnGeneration, "a tool error is not a dead session: the connection must not be swapped")
	assert.Same(t, before.Conn, after.Conn)
	assert.Equal(t, int32(1), calls.Load(), "a tool error is not a dead session: the call must not be replayed")
}
