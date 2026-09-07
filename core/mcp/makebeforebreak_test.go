package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
// Test doubles and helpers for the make-before-break connectToMCPClient tests.
// =============================================================================

// dialGate lets a test stall the dial of a NEW connection to a fake upstream
// MCP server while requests on ALREADY-ESTABLISHED sessions keep flowing.
// New-connection traffic is identified by the absence of the Mcp-Session-Id
// header (the streamable-HTTP initialize request has no session yet); every
// request of a live session carries the header and passes through untouched.
type dialGate struct {
	mu       sync.Mutex
	enabled  bool
	arrived  chan struct{}
	release  chan struct{}
	signaled bool
}

func newDialGate() *dialGate {
	return &dialGate{
		arrived: make(chan struct{}),
		release: make(chan struct{}),
	}
}

// enable arms the gate for the next sessionless request.
func (g *dialGate) enable() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.enabled = true
}

// maybeBlock is called by the server handler. It signals arrival (once) and
// parks the request until the test releases the gate.
func (g *dialGate) maybeBlock(r *http.Request) {
	if r.Header.Get("Mcp-Session-Id") != "" {
		return
	}
	g.mu.Lock()
	if !g.enabled {
		g.mu.Unlock()
		return
	}
	if !g.signaled {
		g.signaled = true
		close(g.arrived)
	}
	g.mu.Unlock()
	<-g.release
}

// releaseAll lets every parked request through and disarms the gate.
func (g *dialGate) releaseAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.enabled {
		g.enabled = false
		close(g.release)
	}
}

// buildGatedMCPServer starts a streamable-HTTP MCP server (one "echo" tool)
// whose middleware routes new-connection requests through the given dialGate.
func buildGatedMCPServer(t *testing.T, gate *dialGate) *httptest.Server {
	t.Helper()

	s := server.NewMCPServer("test-make-before-break", "1.0.0", server.WithToolCapabilities(true))
	echoTool := mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	)
	s.AddTool(echoTool, func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		return mcpgo.NewToolResultText(msg), nil
	})

	streamable := server.NewStreamableHTTPServer(s)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gate != nil {
			gate.maybeBlock(r)
		}
		streamable.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// newMakeBeforeBreakConfig builds a shared-connection (auth_type defaulting to
// headers) HTTP client config pointed at the fake upstream.
func newMakeBeforeBreakConfig(id, serverURL string) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:               id,
		Name:             "mbb-" + id,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(serverURL),
		ToolsToExecute:   []string{"*"},
	}
}

// callEchoTool executes the client's echo tool through the same seam real
// tool execution uses (GetClientForTool + AcquireClientConn + CallTool) and
// returns any error along the way.
func callEchoTool(m *MCPManager, toolName string) error {
	state := m.GetClientForTool(toolName)
	if state == nil {
		return fmt.Errorf("tool '%s' is not available or not permitted", toolName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	conn, release, err := m.AcquireClientConn(bfCtx, state)
	if err != nil {
		return err
	}
	defer release()

	_, callErr := conn.CallTool(ctx, mcpgo.CallToolRequest{
		Request: mcpgo.Request{Method: string(mcpgo.MethodToolsCall)},
		Params: mcpgo.CallToolParams{
			Name:      "echo",
			Arguments: map[string]interface{}{"message": "ping"},
		},
	})
	return callErr
}

// snapshotClientState copies the fields the assertions below care about while
// holding the manager lock.
func snapshotClientState(m *MCPManager, id string) (state schemas.MCPClientState, exists bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cs, ok := m.clientMap[id]
	if !ok {
		return schemas.MCPClientState{}, false
	}
	return *cs, true
}

// =============================================================================
// Success path: the old connection keeps serving during the dial, the swap is
// atomic, and the old connection is closed only after the swap.
// =============================================================================

func TestConnectToMCPClient_MakeBeforeBreak_ServesDuringDialAndClosesOldConn(t *testing.T) {
	gate := newDialGate()
	ts := buildGatedMCPServer(t, gate)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := newMakeBeforeBreakConfig("serves-during-dial", ts.URL)
	toolName := config.Name + "-echo"

	require.NoError(t, m.connectToMCPClient(context.Background(), config))

	before, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	require.NotNil(t, before.Conn)
	require.Equal(t, schemas.MCPConnectionStateHealthy, before.State)
	oldConn := before.Conn

	// Kick off the reconnect with the new dial parked behind the gate.
	gate.enable()
	reconnectDone := make(chan error, 1)
	go func() {
		reconnectDone <- m.connectToMCPClient(context.Background(), config)
	}()

	select {
	case <-gate.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("new dial never reached the gated server")
	}

	// Mid-dial: the entry must still be Connected on the OLD connection with
	// its tool map intact, and a tool call through it must succeed.
	mid, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Equal(t, schemas.MCPConnectionStateHealthy, mid.State, "client must stay Healthy for the whole dial window")
	assert.Same(t, oldConn, mid.Conn, "the old connection must keep serving during the dial")
	assert.Contains(t, mid.ToolMap, toolName, "the tool map must stay populated during the dial")
	require.NoError(t, callEchoTool(m, toolName), "a tool call through the old connection must succeed mid-dial")

	gate.releaseAll()
	require.NoError(t, <-reconnectDone)

	after, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Equal(t, schemas.MCPConnectionStateHealthy, after.State)
	require.NotNil(t, after.Conn)
	assert.NotSame(t, oldConn, after.Conn, "the swap must install a fresh connection")
	assert.Contains(t, after.ToolMap, toolName)
	assert.Equal(t, before.ConnGeneration+1, after.ConnGeneration, "the swap must bump the connection generation exactly once")

	// The old connection must be closed after the swap: a call on the
	// captured handle fails, while the swapped-in client keeps serving.
	callCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, oldErr := oldConn.CallTool(callCtx, mcpgo.CallToolRequest{
		Request: mcpgo.Request{Method: string(mcpgo.MethodToolsCall)},
		Params:  mcpgo.CallToolParams{Name: "echo", Arguments: map[string]interface{}{"message": "ping"}},
	})
	require.Error(t, oldErr, "the pre-swap connection must be closed once the swap completes")
	require.NoError(t, callEchoTool(m, toolName))
}

func TestConnectToMCPClient_MakeBeforeBreak_ConcurrentCallsAcrossSwap(t *testing.T) {
	ts := buildGatedMCPServer(t, nil)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := newMakeBeforeBreakConfig("concurrent-swap", ts.URL)
	toolName := config.Name + "-echo"

	require.NoError(t, m.connectToMCPClient(context.Background(), config))

	// Hammer the tool from many goroutines while several reconnect swaps run.
	// Calls racing the instant the old connection closes may see close-time
	// transport errors; what must NEVER appear are the dial-window failures
	// this change eliminates: a wiped tool map ("not available or not
	// permitted") or a nil Conn ("has no active connection").
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var windowErrs []error
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := callEchoTool(m, toolName); err != nil {
					msg := err.Error()
					if strings.Contains(msg, "no active connection") || strings.Contains(msg, "not available or not permitted") {
						mu.Lock()
						windowErrs = append(windowErrs, err)
						mu.Unlock()
					}
				}
			}
		}()
	}

	for range 3 {
		require.NoError(t, m.connectToMCPClient(context.Background(), config))
		time.Sleep(50 * time.Millisecond)
	}

	close(stop)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, windowErrs, "no call spanning a swap may observe a wiped tool map or a nil connection")
}

// =============================================================================
// Failure paths: a failed re-dial must land in the right post-failure state,
// which differs by classification. A generic failure keeps the live
// connection it could not replace; a credential the upstream confirmed dead
// makes that connection worthless, so it is torn down. Both keep their
// last-known-good tool maps.
// =============================================================================

// flipCredStore succeeds on the first ConnectionHeaders resolution (the
// initial connect) and fails every one after it (the reconnect), letting the
// tests below drive a live client into the connect-gate failure path.
type flipCredStore struct {
	mu       sync.Mutex
	calls    int
	failWith error
}

func (f *flipCredStore) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls > 1 {
		return nil, f.failWith
	}
	return http.Header{}, nil
}

func (f *flipCredStore) RequestHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (f *flipCredStore) RequiresPerCallConnection(_ *schemas.MCPClientConfig) bool {
	return false
}

func (f *flipCredStore) ForceRefresh(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	return nil
}

func (f *flipCredStore) AdminConnectionHeaders(_ context.Context, _ *schemas.MCPClientConfig) (http.Header, error) {
	return nil, fmt.Errorf("not implemented")
}

func TestConnectToMCPClient_MakeBeforeBreak_DialFailure_PostFailureState(t *testing.T) {
	tests := []struct {
		name      string
		failWith  error
		wantState schemas.MCPConnectionState
		// keepsOldConn is the classification split: only a credential the
		// upstream confirmed dead invalidates the connection the entry
		// already holds, so only that case tears it down.
		keepsOldConn bool
	}{
		{
			name:         "generic failure lands in Unstable and keeps the live connection",
			failWith:     fmt.Errorf("connection refused"),
			wantState:    schemas.MCPConnectionStateUnstable,
			keepsOldConn: true,
		},
		{
			name:         "dead OAuth2 credential lands in NeedsReauth and tears the connection down",
			failWith:     fmt.Errorf("refresh token rejected by upstream OAuth server, re-authentication required: %w", schemas.ErrOAuth2TokenExpired),
			wantState:    schemas.MCPConnectionStateNeedsReauth,
			keepsOldConn: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := buildGatedMCPServer(t, nil)

			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, &flipCredStore{failWith: tc.failWith}, nil, nil)
			config := newMakeBeforeBreakConfig("dial-failure", ts.URL)

			require.NoError(t, m.connectToMCPClient(context.Background(), config))

			before, ok := snapshotClientState(m, config.ID)
			require.True(t, ok)
			require.NotNil(t, before.Conn)
			oldConn := before.Conn

			require.Error(t, m.connectToMCPClient(context.Background(), config))

			after, ok := snapshotClientState(m, config.ID)
			require.True(t, ok, "the entry must survive a failed re-dial")
			assert.Equal(t, tc.wantState, after.State)
			if tc.keepsOldConn {
				assert.Same(t, oldConn, after.Conn, "a survivable failure must leave the connection it could not replace installed")
				assert.Equal(t, before.ConnGeneration, after.ConnGeneration, "nothing was swapped in, so the generation must not move")
			} else {
				assert.Nil(t, after.Conn, "a dead credential makes the open connection worthless; it must be detached")
				assert.Nil(t, after.CancelFunc)
				assert.Equal(t, before.ConnGeneration+1, after.ConnGeneration, "detaching must invalidate late writers bound to the old connection")
			}
			// The tool map is deliberately left as last-known-good, not cleared:
			// a dead connection stays distinguishable from a tool that never
			// existed (GetClientForTool still resolves it, and
			// prepareToolExecution's State-specific checks give the real
			// "needs re-authorization" / "disconnected" reason instead of a
			// generic "not available").
			assert.NotEmpty(t, after.ToolMap, "a failed re-dial must keep the last-known tool map")
			assert.NotEmpty(t, after.ToolNameMapping)

			// Reachability of the captured connection, which is the whole
			// point of the split above.
			callCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, oldErr := oldConn.CallTool(callCtx, mcpgo.CallToolRequest{
				Request: mcpgo.Request{Method: string(mcpgo.MethodToolsCall)},
				Params:  mcpgo.CallToolParams{Name: "echo", Arguments: map[string]interface{}{"message": "ping"}},
			})
			if tc.keepsOldConn {
				require.NoError(t, oldErr, "the kept connection must still be open and serving")
			} else {
				require.Error(t, oldErr, "the detached connection must be closed on a failed re-dial")
			}

			// A second consecutive failure. For the tearing-down
			// classification this takes the close-first path (Conn is already
			// nil after the first failure), which is exactly where a pre-dial
			// clear of the tool maps would go unnoticed by the
			// make-before-break assertions above. For the keep-alive
			// classification the entry still holds its connection, so this is
			// make-before-break again and must keep it a second time rather
			// than eventually giving it up.
			require.Error(t, m.connectToMCPClient(context.Background(), config))

			afterSecondFailure, ok := snapshotClientState(m, config.ID)
			require.True(t, ok, "the entry must survive a second failed re-dial")
			assert.Equal(t, tc.wantState, afterSecondFailure.State)
			if tc.keepsOldConn {
				assert.Same(t, oldConn, afterSecondFailure.Conn, "a repeated failure must not eventually give up the working connection")
			} else {
				assert.Nil(t, afterSecondFailure.Conn)
			}
			assert.Equal(t, before.ToolMap, afterSecondFailure.ToolMap, "a second failed re-dial must keep the original last-known tool map")
			assert.Equal(t, before.ToolNameMapping, afterSecondFailure.ToolNameMapping)
		})
	}
}

// TestConnectToMCPClient_MakeBeforeBreak_DialFailure_KeepsLiveConnectionServing
// pins the other half of the make-before-break contract: the old connection
// keeps serving not only for the duration of the dial, but past a dial that
// fails. Tearing it down on failure turns every failed re-dial (a manual
// reconnect during a blip, a credential update, the periodic checker's own
// recovery attempt) into a real outage on a connection that was working, and
// costs the client its connection until some later dial happens to succeed.
//
// The needs_reauth classification is deliberately excluded and stays covered
// by TestConnectToMCPClient_MakeBeforeBreak_DialFailure_ResetsStateAndClosesOldConn:
// a credential the upstream has confirmed dead makes the still-open
// connection worthless, so that path keeps tearing down.
func TestConnectToMCPClient_MakeBeforeBreak_DialFailure_KeepsLiveConnectionServing(t *testing.T) {
	ts := buildGatedMCPServer(t, nil)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, &flipCredStore{failWith: fmt.Errorf("connection refused")}, nil, nil)
	config := newMakeBeforeBreakConfig("dial-failure-keeps-conn", ts.URL)
	toolName := config.Name + "-echo"

	require.NoError(t, m.connectToMCPClient(context.Background(), config))

	before, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	require.NotNil(t, before.Conn)
	require.Equal(t, schemas.MCPConnectionStateHealthy, before.State)

	require.Error(t, m.connectToMCPClient(context.Background(), config), "the re-dial must still fail")

	after, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Same(t, before.Conn, after.Conn, "a failed re-dial must leave the working connection installed")
	assert.Equal(t, before.ConnGeneration, after.ConnGeneration, "nothing was swapped in, so the generation must not move")
	assert.Equal(t, schemas.MCPConnectionStateUnstable, after.State, "the failed attempt is still reported")
	require.NotNil(t, after.LastFailure, "and still carries its reason")
	assert.Contains(t, after.ToolMap, toolName, "the tool map must survive alongside the connection")

	// The point of every assertion above: traffic keeps flowing.
	require.NoError(t, callEchoTool(m, toolName), "the surviving connection must still serve tool calls")
}

// =============================================================================
// Generation guards: stale tool-sync write-backs and stale OnConnectionLost
// callbacks must be no-ops.
// =============================================================================

func TestConnectionChecker_PerformCheck_StaleGenerationWriteDropped(t *testing.T) {
	// Server whose tools/list handling can be parked, so a connection swap can
	// be interleaved between the check's snapshot and its write-back. The
	// gate is armed only after the initial connect, so the connect's own
	// tools/list passes through untouched and exactly the checker's request
	// parks.
	var gateMu sync.Mutex
	gateArmed := false
	gateSignaled := false
	listArrived := make(chan struct{})
	listRelease := make(chan struct{})

	s := server.NewMCPServer("test-stale-sync", "1.0.0", server.WithToolCapabilities(true))
	echoTool := mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	)
	s.AddTool(echoTool, func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("ok"), nil
	})
	streamable := server.NewStreamableHTTPServer(s)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"tools/list"`)) {
			gateMu.Lock()
			blocked := gateArmed
			if blocked && !gateSignaled {
				gateSignaled = true
				close(listArrived)
			}
			gateMu.Unlock()
			if blocked {
				<-listRelease
			}
		}
		streamable.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := newMakeBeforeBreakConfig("stale-sync", ts.URL)

	require.NoError(t, m.connectToMCPClient(context.Background(), config))

	// Arm the gate for the checker's tools/list.
	gateMu.Lock()
	gateArmed = true
	gateMu.Unlock()

	// isPingAvailable=false: skip straight to list_tools, matching the gate
	// above (which only parks tools/list requests).
	checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})
	checkDone := make(chan struct{})
	go func() {
		checker.performCheck()
		close(checkDone)
	}()

	select {
	case <-listArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("checker's tools/list never reached the server")
	}

	// While the check is parked, simulate a completed reconnect swap: bump
	// the generation and install a marker tool map.
	markerTools := map[string]schemas.ChatTool{"marker-tool": {}}
	m.mu.Lock()
	m.clientMap[config.ID].ConnGeneration++
	m.clientMap[config.ID].ToolMap = markerTools
	m.mu.Unlock()

	close(listRelease)
	select {
	case <-checkDone:
	case <-time.After(10 * time.Second):
		t.Fatal("performCheck never finished")
	}

	after, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Contains(t, after.ToolMap, "marker-tool", "a check that spanned a connection swap must not clobber the post-swap tool map")
	assert.Len(t, after.ToolMap, 1)
}

func TestHandleSSEConnectionLost_StaleGenerationNoOp(t *testing.T) {
	m := &MCPManager{
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}
	config := &schemas.MCPClientConfig{ID: "sse-client", Name: "sse-client"}
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
		ConnGeneration:  2,
	}

	// A callback registered against the replaced connection (generation 1)
	// must not flip the freshly connected client to Unstable.
	m.handleSSEConnectionLost(config.ID, config.Name, 1, fmt.Errorf("idle timeout"))
	assert.Equal(t, schemas.MCPConnectionStateHealthy, m.clientMap[config.ID].State)

	// An unknown client is a no-op.
	m.handleSSEConnectionLost("missing-client", "missing-client", 2, fmt.Errorf("idle timeout"))

	// The current generation's callback performs today's transition.
	m.handleSSEConnectionLost(config.ID, config.Name, 2, fmt.Errorf("idle timeout"))
	assert.Equal(t, schemas.MCPConnectionStateUnstable, m.clientMap[config.ID].State)

	// The Disabled guard is preserved even for a current-generation callback.
	m.clientMap[config.ID].State = schemas.MCPConnectionStateDisabled
	m.handleSSEConnectionLost(config.ID, config.Name, 2, fmt.Errorf("idle timeout"))
	assert.Equal(t, schemas.MCPConnectionStateDisabled, m.clientMap[config.ID].State)
}
