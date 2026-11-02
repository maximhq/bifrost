package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// ClientConnectionChecker against an upstream that fails on request: outages,
// ping and tools/list failures, forgotten sessions, and how each recovers.
// =============================================================================

// unreliableUpstream serves a real streamable-HTTP MCP server with one "echo" tool and misbehaves
// on request: it refuses connections, fails ping or tools/list with an internal error, answers ping
// as an unknown method, forgets the sessions it has handed out, or answers slowly. It counts every
// JSON-RPC method it is sent, failed ones included, so a test can tell how often a client pinged,
// listed and connected. Pinned to 2025-11-25, the last revision with ping and sessions. Its tools
// are offered without listChanged, so a connect is one handshake: with it, a 2025-era connection is
// dialled once more for its notification stream.
type unreliableUpstream struct {
	t       *testing.T
	addr    string
	handler http.Handler

	mu       sync.Mutex
	srv      *http.Server
	methods  map[string]int
	seen     map[string]bool
	dead     map[string]bool
	pingFail bool
	listFail bool
	noPing   bool
	latency  time.Duration
}

// startUnreliableUpstream starts an upstream that behaves until told otherwise.
func startUnreliableUpstream(t *testing.T) *unreliableUpstream {
	t.Helper()
	s := server.NewMCPServer("unreliable", "1.0.0", server.WithToolCapabilities(false))
	s.AddTool(mcpgo.NewTool("echo", mcpgo.WithDescription("Echo tool")),
		func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		})
	streamable := server.NewStreamableHTTPServer(s, server.WithStreamableHTTPProtocolVersions(mcpgo.ProtocolVersion20251125))
	u := &unreliableUpstream{t: t, methods: map[string]int{}, seen: map[string]bool{}, dead: map[string]bool{}}
	u.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { u.serve(w, r, streamable) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	u.addr = ln.Addr().String()
	u.serveOn(ln)
	t.Cleanup(u.refuse)
	return u
}

// url is the address clients connect to.
func (u *unreliableUpstream) url() string { return "http://" + u.addr }

// serveOn answers requests arriving on ln.
func (u *unreliableUpstream) serveOn(ln net.Listener) {
	srv := &http.Server{Handler: u.handler}
	u.mu.Lock()
	u.srv = srv
	u.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
}

// refuse closes the listener and every open connection, so the next dial is refused outright.
func (u *unreliableUpstream) refuse() {
	u.mu.Lock()
	srv := u.srv
	u.srv = nil
	u.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

// accept listens again on the address refuse closed.
func (u *unreliableUpstream) accept() {
	ln, err := net.Listen("tcp", u.addr)
	require.NoError(u.t, err)
	u.serveOn(ln)
}

// forgetSessions invalidates every session served so far, as an upstream restart or session
// timeout does: requests carrying one get 404, while a new connection is served normally.
func (u *unreliableUpstream) forgetSessions() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for id := range u.seen {
		u.dead[id] = true
	}
}

// set changes the upstream's faults under its lock.
func (u *unreliableUpstream) set(change func(u *unreliableUpstream)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	change(u)
}

// count returns how many requests for method the upstream has received.
func (u *unreliableUpstream) count(method string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.methods[method]
}

// serve counts the request's JSON-RPC method, then applies the configured faults before handing
// it to the MCP server.
func (u *unreliableUpstream) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		_ = json.Unmarshal(body, &msg)
	}
	session := r.Header.Get("Mcp-Session-Id")

	u.mu.Lock()
	if msg.Method != "" {
		u.methods[msg.Method]++
	}
	forgotten := session != "" && u.dead[session]
	if session != "" && !forgotten {
		u.seen[session] = true
	}
	latency := u.latency
	var fault *jsonRPCFault
	switch {
	case msg.Method == string(mcpgo.MethodPing) && u.noPing:
		fault = &jsonRPCFault{Code: mcpgo.METHOD_NOT_FOUND, Message: "ping is not supported"}
	case msg.Method == string(mcpgo.MethodPing) && u.pingFail:
		fault = &jsonRPCFault{Code: mcpgo.INTERNAL_ERROR, Message: "ping failed (injected)"}
	case msg.Method == string(mcpgo.MethodToolsList) && u.listFail:
		fault = &jsonRPCFault{Code: mcpgo.INTERNAL_ERROR, Message: "tools/list failed (injected)"}
	}
	u.mu.Unlock()

	if r.Method == http.MethodPost {
		time.Sleep(latency)
	}
	switch {
	case forgotten:
		http.Error(w, "session terminated", http.StatusNotFound)
	case fault != nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": fault})
	default:
		next.ServeHTTP(w, r)
	}
}

// jsonRPCFault is the error object a failed request is answered with.
type jsonRPCFault struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// connectUnreliableClient registers a client of u with a fresh manager and waits for its first
// connection. Sticky clients hold one connection; the rest connect per call.
func connectUnreliableClient(t *testing.T, u *unreliableUpstream, id string, sticky bool) (*MCPManager, *schemas.MCPClientConfig) {
	t.Helper()
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID:                     id,
		Name:                   id,
		AuthType:               schemas.MCPAuthTypeNone,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(u.url()),
		NeedsSessionStickiness: schemas.Ptr(sticky),
		ToolsToExecute:         []string{"*"},
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	state, ok := snapshotClientState(m, id)
	require.True(t, ok)
	require.Equal(t, schemas.MCPConnectionStateHealthy, state.State)
	require.Contains(t, state.ToolMap, id+"-echo")
	return m, config
}

// clientOp returns the reconnect or other exclusive operation registered for id, or nil.
func clientOp(m *MCPManager, id string) *inflightClientOp {
	if v, ok := m.reconnectingClients.Load(id); ok {
		return v.(*inflightClientOp)
	}
	return nil
}

// waitForClientOp waits for the operation that replaces before, the one registered for id when
// the test triggered the new one, to finish, and returns its error. A finished operation stays
// registered until the next one replaces it, so waiting on whatever is registered could return
// the previous operation's result before the new one has started.
func waitForClientOp(t *testing.T, m *MCPManager, id string, before *inflightClientOp) error {
	t.Helper()
	var op *inflightClientOp
	require.Eventually(t, func() bool {
		op = clientOp(m, id)
		return op != nil && op != before
	}, 2*time.Second, 10*time.Millisecond, "a reconnect is started")
	select {
	case <-op.done:
		return op.err
	case <-time.After(45 * time.Second):
		t.Fatal("the reconnect did not finish")
		return nil
	}
}

// requireFailureStage asserts the client is unstable and its last failure happened at stage.
func requireFailureStage(t *testing.T, m *MCPManager, id string, stage schemas.MCPConnectionFailureStage) schemas.MCPClientState {
	t.Helper()
	state, ok := snapshotClientState(m, id)
	require.True(t, ok)
	require.Equal(t, schemas.MCPConnectionStateUnstable, state.State)
	require.NotNil(t, state.LastFailure)
	require.Equal(t, stage, state.LastFailure.Stage)
	return state
}

// requireRecovered asserts the client is healthy again with its failure record cleared.
func requireRecovered(t *testing.T, m *MCPManager, id string) schemas.MCPClientState {
	t.Helper()
	state, ok := snapshotClientState(m, id)
	require.True(t, ok)
	require.Equal(t, schemas.MCPConnectionStateHealthy, state.State)
	require.Nil(t, state.LastFailure, "recovery clears the failure record")
	return state
}

// TestConnectionChecker_PerCallOutageAndRecovery: a per-call client has no connection to
// lose, so an outage shows first in its calls and then in the checker's own discovery, which marks
// it unstable at stage tool_discovery and checks again on the unstable cadence. The first check
// after the upstream returns marks it healthy.
func TestConnectionChecker_PerCallOutageAndRecovery(t *testing.T) {
	u := startUnreliableUpstream(t)
	m, config := connectUnreliableClient(t, u, "percall", false)
	checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})

	u.refuse()
	_, bErr := executeEchoTool(m, "percall-echo")
	require.NotNil(t, bErr)
	assert.Contains(t, bErr.Error.Message, "failed to establish ephemeral MCP connection")

	next, onSteady := checker.performCheck()
	assert.Equal(t, UnstableConnectionCheckInterval, next)
	assert.False(t, onSteady)
	down := requireFailureStage(t, m, config.ID, schemas.MCPConnectionFailureStageToolDiscovery)
	assert.Contains(t, down.ToolMap, "percall-echo", "the tools stay listed through the outage")

	u.accept()
	next, onSteady = checker.performCheck()
	assert.Equal(t, time.Minute, next, "a healthy client is checked on its tool sync interval")
	assert.True(t, onSteady)
	requireRecovered(t, m, config.ID)
	_, bErr = executeEchoTool(m, "percall-echo")
	require.Nil(t, bErr)
}

// TestConnectionChecker_StickyOutageAndRecovery: while a sticky client's upstream refuses
// connections its calls fail, and a check marks it unstable at stage ping (or connect, once the
// reconnect it starts has failed too). Once the upstream accepts again the reconnect lands, the
// client is healthy with no failure record, and calls succeed.
func TestConnectionChecker_StickyOutageAndRecovery(t *testing.T) {
	u := startUnreliableUpstream(t)
	m, config := connectUnreliableClient(t, u, "sticky", true)
	checker := NewClientConnectionChecker(m, config.ID, time.Minute, true, &MockLogger{})

	u.refuse()
	_, bErr := executeEchoTool(m, "sticky-echo")
	require.NotNil(t, bErr, "a call fails while the upstream is down")

	before := clientOp(m, config.ID)
	next, onSteady := checker.performCheck()
	assert.Equal(t, UnstableConnectionCheckInterval, next)
	assert.False(t, onSteady)
	state, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	require.Equal(t, schemas.MCPConnectionStateUnstable, state.State)
	require.NotNil(t, state.LastFailure)
	assert.Contains(t, []schemas.MCPConnectionFailureStage{schemas.MCPConnectionFailureStagePing, schemas.MCPConnectionFailureStageConnect}, state.LastFailure.Stage)

	u.accept()
	require.NoError(t, waitForClientOp(t, m, config.ID, before), "the reconnect the failed check started lands once the upstream is back")
	requireRecovered(t, m, config.ID)
	_, bErr = executeEchoTool(m, "sticky-echo")
	require.Nil(t, bErr)
}

// TestConnectionChecker_TransientPingFailureReconnects: a ping that fails with an
// internal error is retried three times, so one check sends four pings before marking the client
// unstable at stage ping. The failed check then opens one new connection, which leaves the client
// healthy, and the next check pings once and keeps it.
func TestConnectionChecker_TransientPingFailureReconnects(t *testing.T) {
	u := startUnreliableUpstream(t)
	m, config := connectUnreliableClient(t, u, "pingfail", true)
	checker := NewClientConnectionChecker(m, config.ID, time.Minute, true, &MockLogger{})
	pings, connects := u.count("ping"), u.count("initialize")

	u.set(func(u *unreliableUpstream) { u.pingFail = true })
	before := clientOp(m, config.ID)
	next, _ := checker.performCheck()
	assert.Equal(t, UnstableConnectionCheckInterval, next)
	requireFailureStage(t, m, config.ID, schemas.MCPConnectionFailureStagePing)
	assert.Equal(t, pings+4, u.count("ping"), "one ping and three retries")

	u.set(func(u *unreliableUpstream) { u.pingFail = false })
	require.NoError(t, waitForClientOp(t, m, config.ID, before))
	assert.Equal(t, connects+1, u.count("initialize"), "the failed check opens exactly one new connection")
	requireRecovered(t, m, config.ID)

	next, onSteady := checker.performCheck()
	assert.Equal(t, time.Minute, next)
	assert.True(t, onSteady)
	assert.Equal(t, pings+5, u.count("ping"), "the next check pings once")
	assert.Equal(t, connects+1, u.count("initialize"), "and keeps the connection")
	requireRecovered(t, m, config.ID)
}

// TestConnectionChecker_ListFailureKeepsKnownTools: a later tools/list failure marks the
// client unstable at stage list_tools, but the tools it already knew stay listed and callable.
func TestConnectionChecker_ListFailureKeepsKnownTools(t *testing.T) {
	u := startUnreliableUpstream(t)
	m, config := connectUnreliableClient(t, u, "listfail", true)
	checker := NewClientConnectionChecker(m, config.ID, time.Minute, true, &MockLogger{})

	u.set(func(u *unreliableUpstream) { u.listFail = true })
	before := clientOp(m, config.ID)
	next, _ := checker.performCheck()
	assert.Equal(t, UnstableConnectionCheckInterval, next)
	state := requireFailureStage(t, m, config.ID, schemas.MCPConnectionFailureStageListTools)
	assert.Contains(t, state.ToolMap, "listfail-echo", "the known tools are kept")
	_, bErr := executeEchoTool(m, "listfail-echo")
	require.Nil(t, bErr, "and an unstable client still serves calls")

	u.set(func(u *unreliableUpstream) { u.listFail = false })
	require.NoError(t, waitForClientOp(t, m, config.ID, before))
	requireRecovered(t, m, config.ID)
}

// TestConnectionChecker_ForgottenSessionReconnects: when the upstream forgets the
// session with no traffic in between, the check's ping gets a 404, is not retried, and the client
// opens one new connection and is healthy again.
func TestConnectionChecker_ForgottenSessionReconnects(t *testing.T) {
	u := startUnreliableUpstream(t)
	m, config := connectUnreliableClient(t, u, "forgotten", true)
	checker := NewClientConnectionChecker(m, config.ID, time.Minute, true, &MockLogger{})
	pings, connects := u.count("ping"), u.count("initialize")

	u.forgetSessions()
	before := clientOp(m, config.ID)
	next, _ := checker.performCheck()
	assert.Equal(t, UnstableConnectionCheckInterval, next)
	requireFailureStage(t, m, config.ID, schemas.MCPConnectionFailureStagePing)
	assert.Equal(t, pings+1, u.count("ping"), "a dead session is not retried")

	require.NoError(t, waitForClientOp(t, m, config.ID, before))
	assert.Equal(t, connects+1, u.count("initialize"))
	requireRecovered(t, m, config.ID)
}

// TestConnectionChecker_UpstreamWithoutPing: with is_ping_available false a check against
// a server that does not implement ping sends none and keeps the one connection healthy. With it
// true, each check's ping fails at once and the client reconnects, which is intended: ping support
// is the setting's to declare.
func TestConnectionChecker_UpstreamWithoutPing(t *testing.T) {
	u := startUnreliableUpstream(t)
	u.set(func(u *unreliableUpstream) { u.noPing = true })
	m, config := connectUnreliableClient(t, u, "noping", true)
	connects := u.count("initialize")
	require.Equal(t, 0, u.count("ping"), "connecting does not ping")

	t.Run("not available", func(t *testing.T) {
		checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})
		for range 2 {
			next, onSteady := checker.performCheck()
			assert.Equal(t, time.Minute, next)
			assert.True(t, onSteady)
		}
		assert.Equal(t, 0, u.count("ping"))
		assert.Equal(t, connects, u.count("initialize"), "one connection throughout")
		requireRecovered(t, m, config.ID)
	})

	t.Run("available", func(t *testing.T) {
		checker := NewClientConnectionChecker(m, config.ID, time.Minute, true, &MockLogger{})
		for i := 1; i <= 2; i++ {
			before := clientOp(m, config.ID)
			next, _ := checker.performCheck()
			assert.Equal(t, UnstableConnectionCheckInterval, next)
			requireFailureStage(t, m, config.ID, schemas.MCPConnectionFailureStagePing)
			assert.Equal(t, i, u.count("ping"), "method not found is not retried")
			require.NoError(t, waitForClientOp(t, m, config.ID, before))
			assert.Equal(t, connects+i, u.count("initialize"), "each failed check reconnects")
			requireRecovered(t, m, config.ID)
		}
	})
}

// TestReconnectClient_ConcurrentReconnectsRunOnce: of two admin reconnects of the same
// client at once, one runs and the other is refused while it does, rather than both dialling.
func TestReconnectClient_ConcurrentReconnectsRunOnce(t *testing.T) {
	u := startUnreliableUpstream(t)
	m, config := connectUnreliableClient(t, u, "concurrent", true)
	connects := u.count("initialize")
	u.set(func(u *unreliableUpstream) { u.latency = 500 * time.Millisecond })

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { errs[i] = m.ReconnectClient(config.ID) })
	}
	wg.Wait()

	var refused []error
	for _, err := range errs {
		if err != nil {
			refused = append(refused, err)
		}
	}
	require.Len(t, refused, 1, "exactly one of the two reconnects runs")
	assert.True(t, errors.Is(refused[0], errReconnectInProgress))
	assert.EqualError(t, refused[0], "reconnect already in progress for this client")
	assert.Equal(t, connects+1, u.count("initialize"), "the client is dialled once")
	requireRecovered(t, m, config.ID)
}
