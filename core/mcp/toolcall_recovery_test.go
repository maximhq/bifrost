package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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

// Replay-safety regressions use the public executor and a real local MCP server.
// The ledger records completed mutations independently of transport attempts.
type replayDiagnosticFixture struct {
	t          *testing.T
	server     *httptest.Server
	transport  string
	fault      string
	failures   int
	readOnly   bool
	idempotent bool
	mu         sync.Mutex
	wire       int
	invoked    int
	ledger     []string
	wireIDs    []string
	sessions   []string
	dropIDs    map[string]bool
	started    chan struct{}
	committed  chan struct{}
	release    chan struct{}
	logger     *replayDiagnosticLogger
}

type replayDiagnosticLogger struct {
	MockLogger
	mu      sync.Mutex
	lines   []string
	backoff chan struct{}
}

func (l *replayDiagnosticLogger) Debug(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
	if strings.Contains(line, "retrying after") {
		select {
		case l.backoff <- struct{}{}:
		default:
		}
	}
}

func (l *replayDiagnosticLogger) Error(format string, args ...any) {
	l.Debug(format, args...)
}

func (f *replayDiagnosticFixture) snapshot() (wire, invoked, commits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wire, f.invoked, len(f.ledger)
}

func (f *replayDiagnosticFixture) execute(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	logical, _ := req.GetArguments()["operation"].(string)
	f.mu.Lock()
	f.invoked++
	invocation := f.invoked
	f.mu.Unlock()
	select {
	case f.started <- struct{}{}:
	default:
	}
	if f.fault == "late_commit" {
		// Deliberately ignore cancellation, as a remote operation can already be
		// committed or otherwise not be abortable when its caller times out.
		<-f.release
	}
	f.mu.Lock()
	if !f.readOnly && (!f.idempotent || len(f.ledger) == 0) {
		f.ledger = append(f.ledger, logical)
	}
	commits := len(f.ledger)
	f.mu.Unlock()
	f.t.Logf("UPSTREAM logical=%q invocation=%d committed_effects=%d", logical, invocation, commits)
	select {
	case f.committed <- struct{}{}:
	default:
	}
	if invocation <= f.failures {
		switch f.fault {
		case "rpc_transient":
			return nil, errors.New("temporary failure after commit")
		case "rpc_unknown":
			return nil, errors.New("business rule rejected after commit")
		case "rpc_auth":
			return nil, errors.New("downstream API returned 401 unauthorized after commit")
		case "rpc_session":
			return nil, errors.New("session not found in tool's domain after commit")
		case "tool_error":
			return mcpgo.NewToolResultError("temporary failure after commit"), nil
		}
	}
	return mcpgo.NewToolResultText(fmt.Sprintf("logical=%s invocation=%d commits=%d", logical, invocation, commits)), nil
}

// replayDiagnosticSSEWriter drops only the selected result event, never discovery
// or initialization. Returning len(p) models a server/proxy losing the response
// without turning the successful tool execution itself into a scripted error.
type replayDiagnosticSSEWriter struct {
	http.ResponseWriter
	f *replayDiagnosticFixture
}

func (w replayDiagnosticSSEWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }
func (w replayDiagnosticSSEWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var envelope struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &envelope) != nil {
			continue
		}
		w.f.mu.Lock()
		drop := w.f.dropIDs[string(envelope.ID)]
		w.f.mu.Unlock()
		if drop {
			w.f.t.Logf("UPSTREAM dropped SSE result wire_id=%s", envelope.ID)
			return len(p), nil
		}
	}
	return w.ResponseWriter.Write(p)
}

func newReplayDiagnosticFixture(t *testing.T, transportName, fault string, failures int, annotations mcpgo.ToolAnnotation, readOnly, idempotent bool) *replayDiagnosticFixture {
	t.Helper()
	f := &replayDiagnosticFixture{
		t: t, transport: transportName, fault: fault, failures: failures,
		readOnly: readOnly, idempotent: idempotent, dropIDs: make(map[string]bool),
		started: make(chan struct{}, 8), committed: make(chan struct{}, 8), release: make(chan struct{}),
		logger: &replayDiagnosticLogger{backoff: make(chan struct{}, 8)},
	}
	s := server.NewMCPServer("local-replay-diagnostics", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(mcpgo.NewTool("append", mcpgo.WithToolAnnotation(annotations), mcpgo.WithString("operation")), f.execute)
	var handler http.Handler = server.NewStreamableHTTPServer(s)
	if transportName == "sse" {
		handler = server.NewSSEServer(s, server.WithUseFullURLForMessageEndpoint(false))
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			if transportName == "sse" && fault == "event_loss" {
				handler.ServeHTTP(replayDiagnosticSSEWriter{w, f}, r)
			} else {
				handler.ServeHTTP(w, r)
			}
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad fixture request", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var envelope struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if json.Unmarshal(body, &envelope) != nil || envelope.Method != "tools/call" {
			handler.ServeHTTP(w, r)
			return
		}
		f.mu.Lock()
		f.wire++
		wire := f.wire
		f.wireIDs = append(f.wireIDs, string(envelope.ID))
		session := r.Header.Get("Mcp-Session-Id")
		if transportName == "sse" {
			session = r.URL.Query().Get("sessionId")
		}
		f.sessions = append(f.sessions, session)
		fail := wire <= failures
		if fail && fault == "event_loss" {
			f.dropIDs[string(envelope.ID)] = true
		}
		f.mu.Unlock()
		t.Logf("WIRE logical_request=1 attempt=%d session=%q body=%s", wire, session, body)
		if fail && (fault == "pre_503" || fault == "pre_401" || fault == "pre_session") {
			status, message := http.StatusServiceUnavailable, "unavailable before dispatch"
			if fault == "pre_401" {
				status, message = http.StatusUnauthorized, "unauthorized before dispatch"
			} else if fault == "pre_session" {
				status, message = http.StatusNotFound, "session expired before dispatch"
			}
			http.Error(w, message, status)
			return
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if transportName == "sse" {
			// Its real handler acknowledges asynchronously; wait for the actual
			// tool handler, not merely a 202, before injecting a post-commit fault.
			select {
			case <-f.committed:
			case <-time.After(3 * time.Second):
				t.Error("fixture tool handler did not commit")
				return
			}
		}
		if fail {
			switch {
			case strings.HasPrefix(fault, "http_"):
				status, _ := strconv.Atoi(strings.TrimPrefix(fault, "http_"))
				http.Error(w, "upstream response unavailable after commit", status)
				return
			case fault == "eof" || fault == "reset":
				if fault == "reset" {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Length", "9999")
					_, _ = io.WriteString(w, "{")
					w.(http.Flusher).Flush()
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				if tcp, ok := conn.(*net.TCPConn); ok && fault == "reset" {
					_ = tcp.SetLinger(0)
				}
				_ = conn.Close()
				return
			case fault == "withhold":
				<-r.Context().Done()
				return
			}
		}
		if transportName == "http-sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if !(fail && fault == "event_loss") {
				_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", strings.TrimSpace(recorder.Body.String()))
			}
			w.(http.Flusher).Flush()
			return
		}
		for k, values := range recorder.Header() {
			w.Header()[k] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *replayDiagnosticFixture) manager(t *testing.T, sticky bool, timeout time.Duration) (*MCPManager, string) {
	t.Helper()
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, f.logger, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	connectionType := schemas.MCPConnectionTypeHTTP
	url := f.server.URL
	if f.transport == "sse" {
		connectionType = schemas.MCPConnectionTypeSSE
		url += "/sse"
	}
	config := &schemas.MCPClientConfig{
		ID: "replay-diagnostics-id", Name: "replay", ConnectionType: connectionType,
		AuthType: schemas.MCPAuthTypeHeaders, ConnectionString: schemas.NewSecretVar(url),
		NeedsSessionStickiness: schemas.Ptr(sticky), ToolsToExecute: []string{"*"}, ToolsToAutoExecute: []string{"*"},
		ToolExecutionTimeout: timeout,
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	require.Eventually(t, func() bool { return m.GetClientForTool("replay-append") != nil }, 3*time.Second, 5*time.Millisecond)
	return m, "replay-append"
}

func replayDiagnosticCall(m *MCPManager, ctx context.Context, name string) (*schemas.ChatMessage, *schemas.BifrostError) {
	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	bfCtx.SetValue(schemas.BifrostContextKeyRequestID, "logical-request-one")
	return m.ExecuteChatTool(bfCtx, &schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("logical-tool-call-one"), Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: `{"operation":"customer-operation-one"}`},
	})
}

func (f *replayDiagnosticFixture) report(t *testing.T, result any, bErr *schemas.BifrostError) {
	t.Helper()
	wire, invoked, commits := f.snapshot()
	resultJSON, _ := json.Marshal(result)
	errorJSON, _ := json.Marshal(bErr)
	f.mu.Lock()
	t.Logf("EVIDENCE transport=%s fault=%s logical_requests=1 wire_calls=%d actual_invocations=%d committed_effects=%d ids=%v ledger=%v result=%s error=%s", f.transport, f.fault, wire, invoked, commits, f.wireIDs, f.ledger, resultJSON, errorJSON)
	f.mu.Unlock()
	f.logger.mu.Lock()
	defer f.logger.mu.Unlock()
	for _, line := range f.logger.lines {
		if strings.Contains(line, "retry") || strings.Contains(line, "execution failed") {
			t.Logf("BIFROST %s", line)
		}
	}
}

func TestMCPToolReplaySafety_RealTransports(t *testing.T) {
	additive := mcpgo.ToolAnnotation{DestructiveHint: schemas.Ptr(false), IdempotentHint: schemas.Ptr(false)}
	destructive := mcpgo.ToolAnnotation{DestructiveHint: schemas.Ptr(true), IdempotentHint: schemas.Ptr(false)}
	cases := []struct {
		name, fault              string
		annotations              mcpgo.ToolAnnotation
		readOnly, idempotent     bool
		failures, calls, effects int
		wantError, toolError     bool
	}{
		{name: "unannotated_success", calls: 1, effects: 1},
		{name: "readonly_success", annotations: mcpgo.ToolAnnotation{ReadOnlyHint: schemas.Ptr(true)}, readOnly: true, calls: 1},
		{name: "idempotent_success", annotations: mcpgo.ToolAnnotation{IdempotentHint: schemas.Ptr(true)}, idempotent: true, calls: 1, effects: 1},
		{name: "unannotated_postcommit_503", fault: "http_503", failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "additive_nonidempotent_postcommit_503", fault: "http_503", annotations: additive, failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "destructive_postcommit_503", fault: "http_503", annotations: destructive, failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "readonly_postcommit_503", fault: "http_503", annotations: mcpgo.ToolAnnotation{ReadOnlyHint: schemas.Ptr(true)}, readOnly: true, failures: 1, calls: 2},
		{name: "idempotent_postcommit_503", fault: "http_503", annotations: mcpgo.ToolAnnotation{IdempotentHint: schemas.Ptr(true)}, idempotent: true, failures: 1, calls: 2, effects: 1},
		// Hints authorize replay; they do not enforce deduplication. This
		// deliberately false server contract documents the remaining trust boundary.
		{name: "misannotated_nonidempotent_can_duplicate", fault: "http_503", annotations: mcpgo.ToolAnnotation{IdempotentHint: schemas.Ptr(true)}, failures: 1, calls: 2, effects: 2},
		{name: "readonly_ignores_other_hints", fault: "http_503", annotations: mcpgo.ToolAnnotation{ReadOnlyHint: schemas.Ptr(true), DestructiveHint: schemas.Ptr(true), IdempotentHint: schemas.Ptr(false)}, readOnly: true, failures: 1, calls: 2},
		{name: "rpc_transient_after_commit", fault: "rpc_transient", failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "idempotent_rpc_error_not_retried", fault: "rpc_transient", annotations: mcpgo.ToolAnnotation{IdempotentHint: schemas.Ptr(true)}, idempotent: true, failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "rpc_unknown_after_commit", fault: "rpc_unknown", failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "rpc_domain_session_no_recovery", fault: "rpc_session", annotations: additive, failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "structured_tool_error_not_retried", fault: "tool_error", failures: 1, calls: 1, effects: 1, toolError: true},
		{name: "idempotent_structured_tool_error_not_retried", fault: "tool_error", annotations: mcpgo.ToolAnnotation{IdempotentHint: schemas.Ptr(true)}, idempotent: true, failures: 1, calls: 1, effects: 1, toolError: true},
		{name: "postcommit_400", fault: "http_400", failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "postcommit_429", fault: "http_429", failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "postcommit_500", fault: "http_500", failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "postcommit_502", fault: "http_502", failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "postcommit_504", fault: "http_504", failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "preexecution_503_unknown_phase_is_not_replayed", fault: "pre_503", failures: 1, calls: 1, wantError: true},
		{name: "preexecution_503_readonly_still_retries", fault: "pre_503", annotations: mcpgo.ToolAnnotation{ReadOnlyHint: schemas.Ptr(true)}, readOnly: true, failures: 1, calls: 2},
		{name: "unsafe_retry_budget_is_not_used", fault: "http_503", failures: 3, calls: 1, effects: 1, wantError: true},
		{name: "idempotent_retry_budget_exhausted", fault: "http_503", annotations: mcpgo.ToolAnnotation{IdempotentHint: schemas.Ptr(true)}, idempotent: true, failures: 3, calls: 3, effects: 1, wantError: true},
		{name: "response_reset_after_commit", fault: "reset", failures: 1, calls: 1, effects: 1, wantError: true},
		{name: "response_eof_after_commit", fault: "eof", failures: 1, calls: 1, effects: 1, wantError: true},
	}
	for _, transportName := range []string{"http-json", "sse"} {
		for _, tc := range cases {
			t.Run(transportName+"/"+tc.name, func(t *testing.T) {
				f := newReplayDiagnosticFixture(t, transportName, tc.fault, tc.failures, tc.annotations, tc.readOnly, tc.idempotent)
				m, name := f.manager(t, true, 2*time.Second)
				msg, bErr := replayDiagnosticCall(m, context.Background(), name)
				f.report(t, msg, bErr)
				wire, invoked, effects := f.snapshot()
				require.Equal(t, tc.calls, wire)
				wantInvoked := tc.calls
				if tc.fault == "pre_503" {
					wantInvoked--
				}
				require.Equal(t, wantInvoked, invoked)
				require.Equal(t, tc.effects, effects)
				if tc.wantError {
					require.NotNil(t, bErr)
					if invoked > 0 && !tc.readOnly && !tc.idempotent {
						require.Contains(t, bErr.GetErrorString(), "may have succeeded")
					}
				} else {
					require.Nil(t, bErr)
					require.NotNil(t, msg)
					require.NotNil(t, msg.Content)
					if !tc.toolError {
						require.Contains(t, *msg.Content.ContentStr, "customer-operation-one")
					}
				}
				if tc.toolError {
					require.NotNil(t, msg.ChatToolMessage)
					require.Equal(t, schemas.Ptr(true), msg.IsError)
				}
			})
		}
	}
}

func TestMCPToolReplaySafety_Recovery(t *testing.T) {
	for _, sticky := range []bool{true, false} {
		for _, fault := range []string{"pre_401", "pre_session", "rpc_auth", "http_401"} {
			for _, policy := range []string{"missing", "additive", "idempotent"} {
				t.Run(fmt.Sprintf("sticky=%t/%s/policy=%s", sticky, fault, policy), func(t *testing.T) {
					annotations := mcpgo.ToolAnnotation{}
					if policy == "additive" {
						annotations.DestructiveHint = schemas.Ptr(false)
						annotations.IdempotentHint = schemas.Ptr(false)
					}
					idempotent := policy == "idempotent"
					if idempotent {
						annotations.IdempotentHint = schemas.Ptr(true)
					}
					f := newReplayDiagnosticFixture(t, "http-json", fault, 1, annotations, false, idempotent)
					m, name := f.manager(t, sticky, 2*time.Second)
					msg, bErr := replayDiagnosticCall(m, context.Background(), name)
					f.report(t, msg, bErr)
					wire, invoked, effects := f.snapshot()
					calls := 1
					if idempotent {
						calls++
						require.Nil(t, bErr, "authorized recovery must preserve availability")
						require.NotNil(t, msg)
					} else {
						require.NotNil(t, bErr, "neither absent hints nor additive-only hints authorize replay")
					}
					require.Equal(t, calls, wire)
					if strings.HasPrefix(fault, "pre_") {
						calls--
					}
					require.Equal(t, calls, invoked)
					wantEffects := calls
					if idempotent {
						wantEffects = 1
					}
					require.Equal(t, wantEffects, effects)
				})
			}
		}
	}
}

func TestMCPToolReplaySafety_RechecksDuringBackoff(t *testing.T) {
	f := newReplayDiagnosticFixture(t, "http-json", "http_503", 1, mcpgo.ToolAnnotation{IdempotentHint: schemas.Ptr(true)}, false, true)
	m, name := f.manager(t, true, 2*time.Second)
	done := make(chan *schemas.BifrostError, 1)
	go func() { _, err := replayDiagnosticCall(m, context.Background(), name); done <- err }()
	select {
	case <-f.logger.backoff:
	case <-time.After(time.Second):
		t.Fatal("no retry scheduled")
	}
	state := m.GetClientByName("replay")
	tool := state.ToolMap[name]
	tool.Annotations = &schemas.MCPToolAnnotations{DestructiveHint: schemas.Ptr(false), IdempotentHint: schemas.Ptr(false)}
	// Simulate the server's changed contract and its normal tool-list update,
	// without mutating a published tool map in place.
	f.mu.Lock()
	f.idempotent = false
	f.mu.Unlock()
	m.SetClientTools(state.ExecutionConfig.ID, map[string]schemas.ChatTool{name: tool}, state.ToolNameMapping, "")
	select {
	case err := <-done:
		f.report(t, nil, err)
		require.NotNil(t, err)
		require.Contains(t, err.GetErrorString(), "request failed with status 503")
		require.Contains(t, err.GetErrorString(), "upstream response unavailable after commit")
		require.NotContains(t, err.GetErrorString(), "tool replay no longer authorized")
		require.Contains(t, err.GetErrorString(), "may have succeeded")
	case <-time.After(3 * time.Second):
		t.Fatal("execution did not finish")
	}
	wire, invoked, effects := f.snapshot()
	require.Equal(t, 1, wire)
	require.Equal(t, 1, invoked)
	require.Equal(t, 1, effects)
}

func TestMCPToolReplaySafety_DeadlineAndCancellation(t *testing.T) {
	for _, scenario := range []string{"deadline_backoff", "cancel_backoff", "withhold", "late_commit", "event_loss_http", "event_loss_sse", "connection_refused"} {
		t.Run(scenario, func(t *testing.T) {
			transportName, fault := "http-json", "http_503"
			if scenario == "withhold" || scenario == "late_commit" {
				fault = scenario
			}
			if scenario == "event_loss_http" {
				transportName, fault = "http-sse", "event_loss"
			}
			if scenario == "event_loss_sse" {
				transportName, fault = "sse", "event_loss"
			}
			// Explicitly idempotent so the backoff controls still exercise an
			// authorized retry, rather than relying on replaying an unsafe tool.
			f := newReplayDiagnosticFixture(t, transportName, fault, 1, mcpgo.ToolAnnotation{IdempotentHint: schemas.Ptr(true)}, false, true)
			budget := 100 * time.Millisecond
			if scenario == "connection_refused" || scenario == "cancel_backoff" {
				budget = time.Second
			}
			m, name := f.manager(t, true, budget)
			if scenario == "connection_refused" {
				f.server.Close()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				msg *schemas.ChatMessage
				err *schemas.BifrostError
			}
			done := make(chan outcome, 1)
			start := time.Now()
			go func() { msg, err := replayDiagnosticCall(m, ctx, name); done <- outcome{msg, err} }()
			if scenario == "cancel_backoff" {
				select {
				case <-f.logger.backoff:
					cancel()
				case <-time.After(time.Second):
					t.Fatal("no retry scheduled")
				}
			}
			var out outcome
			select {
			case out = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("execution exceeded diagnostic bound")
			}
			elapsed := time.Since(start)
			if scenario == "late_commit" {
				_, _, effects := f.snapshot()
				require.Zero(t, effects, "operation has not committed at caller timeout")
				close(f.release)
				select {
				case <-f.committed:
				case <-time.After(time.Second):
					t.Fatal("late operation never committed")
				}
			}
			f.report(t, out.msg, out.err)
			t.Logf("elapsed=%s configured_tool_budget=%s", elapsed, budget)
			require.NotNil(t, out.err)
			wire, invoked, effects := f.snapshot()
			if scenario == "connection_refused" {
				require.Zero(t, wire)
				require.Zero(t, invoked)
				require.Zero(t, effects)
				return
			}
			require.Equal(t, 1, wire)
			require.Equal(t, 1, invoked)
			require.Equal(t, 1, effects)
			if scenario == "event_loss_http" {
				// An empty, closed Streamable HTTP SSE response closes mcp-go's
				// response channel: it reports unexpected nil response, not a
				// timeout. Neither outcome authorizes another wire invocation.
				require.Equal(t, "tool_error", mcpErrorType(out.err))
			} else if scenario != "cancel_backoff" {
				require.Equal(t, "timeout", mcpErrorType(out.err))
			}
			// This is a hang guard, not a hard real-time scheduling guarantee.
			// Concurrent cold/race builds produced one 671ms wall-clock outlier
			// for a 100ms deadline. Preserve the measured duration in evidence;
			// the stronger assertions are timeout classification and no replay.
			require.Less(t, elapsed, 2*time.Second)
		})
	}
}

func TestMCPToolReplaySafety_AgentUsesSharedExecutor(t *testing.T) {
	f := newReplayDiagnosticFixture(t, "http-json", "http_503", 1, mcpgo.ToolAnnotation{}, false, false)
	m, name := f.manager(t, true, 2*time.Second)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "logical-agent-one")
	call := schemas.ChatAssistantMessageToolCall{ID: schemas.Ptr("logical-tool-one"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: `{"operation":"customer-operation-one"}`}}
	initial := &schemas.BifrostChatResponse{Model: "local-model", Choices: []schemas.BifrostResponseChoice{{ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{call}}}}}}}
	var llmContinuations int
	result, bErr := m.CheckAndExecuteAgentForChatRequest(ctx, &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "local-model"}, initial, func(_ *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		llmContinuations++
		toolResults := 0
		for _, message := range req.Input {
			if message.Role == schemas.ChatMessageRoleTool {
				toolResults++
				require.Equal(t, call.ID, message.ToolCallID)
				require.Equal(t, schemas.Ptr(true), message.IsError)
				require.Contains(t, *message.Content.ContentStr, "request failed with status 503")
				require.Contains(t, *message.Content.ContentStr, "may have succeeded")
			}
		}
		require.Equal(t, 1, toolResults, "the failed execution remains one logical tool result")
		return &schemas.BifrostChatResponse{Model: "local-model", Choices: []schemas.BifrostResponseChoice{{ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("done")}}}}}}, nil
	})
	f.report(t, result, bErr)
	require.Nil(t, bErr)
	require.Equal(t, 1, llmContinuations)
	wire, invoked, effects := f.snapshot()
	require.Equal(t, 1, wire)
	require.Equal(t, 1, invoked)
	require.Equal(t, 1, effects)
}

func TestMCPToolReplaySafety_ResponsesUsesSharedExecutor(t *testing.T) {
	for _, agent := range []bool{false, true} {
		t.Run(fmt.Sprintf("agent=%t", agent), func(t *testing.T) {
			f := newReplayDiagnosticFixture(t, "http-json", "http_503", 1, mcpgo.ToolAnnotation{}, false, false)
			m, name := f.manager(t, true, 2*time.Second)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyRequestID, "logical-responses-one")
			call := &schemas.ResponsesToolMessage{CallID: schemas.Ptr("logical-tool-one"), Name: &name, Arguments: schemas.Ptr(`{"operation":"customer-operation-one"}`)}
			if !agent {
				msg, bErr := m.ExecuteResponsesTool(ctx, call)
				f.report(t, msg, bErr)
				require.NotNil(t, bErr)
				require.Nil(t, msg)
				require.Contains(t, bErr.GetErrorString(), "may have succeeded")
			} else {
				initial := &schemas.BifrostResponsesResponse{Model: "local-model", Output: []schemas.ResponsesMessage{{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall), ResponsesToolMessage: call}}}
				continuations := 0
				result, bErr := m.CheckAndExecuteAgentForResponsesRequest(ctx, &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "local-model"}, initial, func(_ *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
					continuations++
					toolResults := 0
					for _, message := range req.Input {
						if message.Type != nil && *message.Type == schemas.ResponsesMessageTypeFunctionCallOutput {
							toolResults++
							require.Equal(t, call.CallID, message.CallID)
							// The existing Chat-to-Responses adapter carries a text
							// function_call_output, not Chat's IsError marker.
							require.Contains(t, *message.Output.ResponsesToolCallOutputStr, "request failed with status 503")
							require.Contains(t, *message.Output.ResponsesToolCallOutputStr, "may have succeeded")
						}
					}
					require.Equal(t, 1, toolResults)
					return &schemas.BifrostResponsesResponse{Model: "local-model"}, nil
				})
				f.report(t, result, bErr)
				require.Nil(t, bErr)
				require.Equal(t, 1, continuations)
			}
			wire, invoked, effects := f.snapshot()
			require.Equal(t, 1, wire)
			require.Equal(t, 1, invoked)
			require.Equal(t, 1, effects)
		})
	}
}
