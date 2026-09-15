package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// isAuthFailureErrorText — unit coverage
// =============================================================================

func TestIsAuthFailureErrorText(t *testing.T) {
	tests := []struct {
		name   string
		errStr string
		want   bool
	}{
		{"plain 401", "tool call failed: 401 Unauthorized", true},
		{"plain 403", "tool call failed: 403 Forbidden", true},
		{"lowercase unauthorized text, no numeric code", "request rejected: unauthorized", true},
		{"lowercase forbidden text, no numeric code", "access forbidden for this resource", true},
		{"mixed case still matches", "Upstream returned Unauthorized", true},
		{"unrelated connectivity error", "connection refused", false},
		{"unrelated bad request", "400 bad request", false},
		{"timeout text alone does not match", "context deadline exceeded", false},
		{"empty string", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAuthFailureErrorText(tc.errStr); got != tc.want {
				t.Errorf("isAuthFailureErrorText(%q) = %v, want %v", tc.errStr, got, tc.want)
			}
		})
	}
}

// isAuthFailureErrorText must disagree with isTransientError on the exact
// same input for the auth-shaped substring class: isTransientError treats it
// as permanent (false — don't retry a connection attempt), this function
// treats it as the positive retry trigger (true). Confirms the two are
// deliberately opposite polarity, not accidentally identical.
func TestIsAuthFailureErrorText_OpposesIsTransientErrorOnAuthText(t *testing.T) {
	err := errors.New("tool call failed: 401 Unauthorized")
	if isTransientError(err) {
		t.Fatalf("isTransientError should treat a 401 as permanent (non-retryable) for its own connection-establishment callers")
	}
	if !isAuthFailureErrorText(err.Error()) {
		t.Fatalf("isAuthFailureErrorText should treat the same 401 text as a positive retry trigger")
	}
}

// isAuthFailureErrorText must NOT match Bifrost's own internal
// ErrOAuth2TokenExpired sentinel text (schemas.ErrOAuth2TokenExpired says
// "oauth2 token expired") — that sentinel is a genuinely different failure
// class (a connect-time failure already classified by Bifrost's own refresh
// logic via errors.Is, not text matching — see connectToMCPClient) vs. a raw
// unclassified upstream rejection here.
func TestIsAuthFailureErrorText_DoesNotOverlapOAuth2TokenExpiredSentinel(t *testing.T) {
	sentinelText := schemas.ErrOAuth2TokenExpired.Error()
	if isAuthFailureErrorText(sentinelText) {
		t.Fatalf("isAuthFailureErrorText unexpectedly matched Bifrost's own %q sentinel text", sentinelText)
	}
}

// =============================================================================
// Auth-failure retry — CallTool-level integration coverage
// =============================================================================
//
// These tests drive ToolsManager.ExecuteTool end to end against a real
// *client.Client wired to a fake transport.Interface (mark3labs/mcp-go's
// client.NewClient accepts any transport.Interface), so CallTool's actual
// error text — not a mocked shortcut — is what isAuthFailureErrorText
// evaluates. client.WithSession() marks the client pre-initialized so no
// real MCP `initialize` handshake is needed.

// fakeCallToolTransport is a minimal transport.Interface whose tools/call
// responses are scripted per invocation (by index): a non-nil entry in
// callErrs fails that attempt, a nil entry (or running past the slice)
// succeeds. Every other JSON-RPC method succeeds trivially.
//
// callResultIsError optionally scripts the "isError" field on a successful
// (non-callErrs) response by the same index — an upstream server reporting a
// failed tool execution over an otherwise-successful JSON-RPC call. A missing
// or out-of-range entry defaults to false (ordinary success).
type fakeCallToolTransport struct {
	mu                sync.Mutex
	callCount         int
	callErrs          []error
	callResultIsError []bool
}

func (f *fakeCallToolTransport) Start(_ context.Context) error { return nil }

func (f *fakeCallToolTransport) SendRequest(_ context.Context, request transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	if request.Method != "tools/call" {
		return &transport.JSONRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{}`)}, nil
	}

	f.mu.Lock()
	idx := f.callCount
	f.callCount++
	f.mu.Unlock()

	if idx < len(f.callErrs) && f.callErrs[idx] != nil {
		return nil, f.callErrs[idx]
	}

	isError := idx < len(f.callResultIsError) && f.callResultIsError[idx]
	result := json.RawMessage(fmt.Sprintf(`{"content":[{"type":"text","text":"ok"}],"isError":%t}`, isError))
	return &transport.JSONRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: result}, nil
}

func (f *fakeCallToolTransport) SendNotification(_ context.Context, _ mcp.JSONRPCNotification) error {
	return nil
}
func (f *fakeCallToolTransport) SetNotificationHandler(_ func(mcp.JSONRPCNotification)) {}
func (f *fakeCallToolTransport) Close() error                                           { return nil }
func (f *fakeCallToolTransport) GetSessionId() string                                   { return "fake-session" }

func (f *fakeCallToolTransport) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

// authRetryClientManager is a ClientManager test double that tracks
// AcquireClientConn / ReconnectClient invocations so tests can assert on
// exactly what attemptAuthFailureRecovery triggers. Optional signal channels
// let background-goroutine tests (the shared-connection path) synchronize
// without sleeping.
type authRetryClientManager struct {
	state *schemas.MCPClientState

	acquireConn  *client.Client
	acquireErr   error
	acquireCalls atomic.Int32

	reconnectErr    error
	reconnectCalls  atomic.Int32
	reconnectSignal chan struct{}

	// reconnectGate, when non-nil, blocks an in-flight ReconnectClient until
	// the channel is closed, letting tests hold the reconnect open while the
	// caller's bounded wait (or a concurrent second caller) races against it.
	reconnectGate chan struct{}
	// reconnectRejected counts callers that lost the in-flight race and got
	// the "already in progress" error, mirroring the real manager's guard.
	reconnectRejected atomic.Int32

	// rerouteToolState, when non-nil, is what GetClientForTool returns
	// instead of state — simulating global tool routing having picked a
	// different client for this tool name by the time a stale lookup runs.
	rerouteToolState *schemas.MCPClientState
	// byNameState, when non-nil, is what GetClientByName(state.Name) returns
	// instead of state itself — simulating the reconnected client having a
	// different identity (e.g. a different ExecutionConfig.ID, as if it was
	// deleted and recreated under the same name) by the time the
	// post-reconnect reacquire runs.
	byNameState *schemas.MCPClientState

	inflightMu sync.Mutex
	inflight   *inflightClientOp
}

func (m *authRetryClientManager) GetClientByName(clientName string) *schemas.MCPClientState {
	if m.byNameState != nil {
		return m.byNameState
	}
	if m.state != nil && m.state.Name == clientName {
		return m.state
	}
	return nil
}

func (m *authRetryClientManager) GetClientForTool(toolName string) *schemas.MCPClientState {
	if m.rerouteToolState != nil {
		if _, ok := m.rerouteToolState.ToolMap[toolName]; ok {
			return m.rerouteToolState
		}
	}
	if m.state != nil {
		if _, ok := m.state.ToolMap[toolName]; ok {
			return m.state
		}
	}
	return nil
}

func (m *authRetryClientManager) GetToolPerClient(_ context.Context) map[string][]schemas.ChatTool {
	return nil
}

func (m *authRetryClientManager) GetPluginPipeline() PluginPipeline      { return nil }
func (m *authRetryClientManager) ReleasePluginPipeline(_ PluginPipeline) {}

func (m *authRetryClientManager) AcquireClientConn(_ *schemas.BifrostContext, _ *schemas.MCPClientState) (*client.Client, func(), error) {
	m.acquireCalls.Add(1)
	if m.acquireErr != nil {
		return nil, nil, m.acquireErr
	}
	return m.acquireConn, func() {}, nil
}

// ReconnectClient mirrors the real MCPManager.beginExclusiveClientOp
// contract: a completed op is left in m.inflight rather than cleared, so a
// caller that lost the race and calls AwaitReconnect afterward still finds
// the (by-then-finished) op instead of racing a delete. A new call only
// starts a fresh op when none is present or the previous one has finished.
func (m *authRetryClientManager) ReconnectClient(_ string) error {
	op := &inflightClientOp{done: make(chan struct{})}
	m.inflightMu.Lock()
	if m.inflight != nil {
		select {
		case <-m.inflight.done:
			// Previous op finished; replace it and proceed as the new winner.
		default:
			m.inflightMu.Unlock()
			m.reconnectRejected.Add(1)
			return errors.New("reconnect already in progress for this client")
		}
	}
	m.inflight = op
	m.inflightMu.Unlock()

	m.reconnectCalls.Add(1)
	if m.reconnectSignal != nil {
		select {
		case m.reconnectSignal <- struct{}{}:
		default:
		}
	}
	if m.reconnectGate != nil {
		<-m.reconnectGate
	}
	err := m.reconnectErr

	op.err = err
	close(op.done)
	return err
}

// resetInflight clears any completed inflight record so the next
// ReconnectClient call starts from a clean no-op state. Only needed by tests
// that reuse the same authRetryClientManager across multiple reconnect
// phases and require no stale op to be observable via AwaitReconnect;
// ReconnectClient's own CompareAndSwap-style replace makes this unnecessary
// for tests that just call ReconnectClient again.
func (m *authRetryClientManager) resetInflight() {
	m.inflightMu.Lock()
	m.inflight = nil
	m.inflightMu.Unlock()
}

func (m *authRetryClientManager) AwaitReconnect(_ string, budget time.Duration) (bool, error) {
	m.inflightMu.Lock()
	op := m.inflight
	m.inflightMu.Unlock()
	if op == nil {
		return false, nil
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-op.done:
		return true, op.err
	case <-timer.C:
		return false, nil
	}
}

func (m *authRetryClientManager) RunWithPluginPipeline(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest, op MCPOpFunc) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	resp, err := op(req)
	if err != nil {
		return nil, &schemas.BifrostError{IsBifrostError: false, Error: &schemas.ErrorField{Message: err.Error()}}
	}
	return resp, nil
}

// authRetryCredStore is a schemas.MCPCredentialStore test double: configurable
// RequiresPerCallConnection, and a ForceRefresh whose call count (and,
// optionally, a signal channel for background-goroutine tests) is observable.
type authRetryCredStore struct {
	requiresPerCall bool

	forceRefreshErr    error
	forceRefreshCalls  atomic.Int32
	forceRefreshSignal chan struct{}
}

func (s *authRetryCredStore) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (s *authRetryCredStore) RequestHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (s *authRetryCredStore) RequiresPerCallConnection(_ *schemas.MCPClientConfig) bool {
	return s.requiresPerCall
}

func (s *authRetryCredStore) ForceRefresh(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	s.forceRefreshCalls.Add(1)
	if s.forceRefreshSignal != nil {
		select {
		case s.forceRefreshSignal <- struct{}{}:
		default:
		}
	}
	return s.forceRefreshErr
}

func (s *authRetryCredStore) AdminConnectionHeaders(_ context.Context, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

// newAuthRetryClientState builds a minimal MCPClientState + prefixed tool
// name pair for the tests below. destructive/idempotent (both optional,
// nil = unset) populate the tool's MCPToolAnnotations so the opt-out gate
// can be exercised.
func newAuthRetryClientState(clientName, toolName string, destructive, idempotent *bool) (*schemas.MCPClientState, string) {
	prefixedName := clientName + "-" + toolName
	tool := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: prefixedName,
		},
	}
	if destructive != nil || idempotent != nil {
		tool.Annotations = &schemas.MCPToolAnnotations{
			DestructiveHint: destructive,
			IdempotentHint:  idempotent,
		}
	}
	config := &schemas.MCPClientConfig{
		ID:   clientName + "-id",
		Name: clientName,
	}
	state := &schemas.MCPClientState{
		Name:            clientName,
		ExecutionConfig: config,
		ToolMap:         map[string]schemas.ChatTool{prefixedName: tool},
		ToolNameMapping: map[string]string{},
	}
	return state, prefixedName
}

func newAuthRetryToolCallRequest(toolName string) *schemas.BifrostMCPRequest {
	name := toolName
	return &schemas.BifrostMCPRequest{
		RequestType: schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      &name,
				Arguments: "{}",
			},
		},
	}
}

func newAuthRetryToolsManager(cm ClientManager, cs schemas.MCPCredentialStore) *ToolsManager {
	return NewToolsManager(
		&schemas.MCPToolManagerConfig{MaxAgentDepth: 5},
		cm,
		nil,
		cs,
		&MockLogger{},
	)
}

func boolPtr(b bool) *bool { return &b }

// TestExecuteTool_AuthFailureRetry_PerUser_SucceedsOnSecondAttempt covers the
// per-user path: a 401 on the first CallTool forces a refresh, re-acquires a
// connection, and retries the SAME call once — which succeeds here.
func TestExecuteTool_AuthFailureRetry_PerUser_SucceedsOnSecondAttempt(t *testing.T) {
	// Explicit idempotent hint: retry-safety now fails closed on missing
	// annotations (see TestExecuteTool_AuthFailureRetry_NoAnnotations_SkipsRetry),
	// so this retry-mechanics test needs an explicitly-safe tool to reach the
	// retry path at all.
	state, toolName := newAuthRetryClientState("testclient", "dotool", nil, boolPtr(true))

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn}
	cs := &authRetryCredStore{requiresPerCall: true}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	resp, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err != nil {
		t.Fatalf("expected the retry to succeed, got error: %v", err)
	}
	if resp == nil || resp.ChatMessage == nil {
		t.Fatalf("expected a populated chat message response, got %+v", resp)
	}
	if got := ft.CallCount(); got != 2 {
		t.Errorf("expected 2 CallTool invocations (original + exactly 1 retry), got %d", got)
	}
	if got := cs.forceRefreshCalls.Load(); got != 1 {
		t.Errorf("expected ForceRefresh to be called exactly once, got %d", got)
	}
	if got := cm.acquireCalls.Load(); got != 1 {
		t.Errorf("expected AcquireClientConn to be called exactly once for the retry, got %d", got)
	}
	if got := cm.reconnectCalls.Load(); got != 0 {
		t.Errorf("per-user path must never call ReconnectClient, got %d calls", got)
	}
}

// TestExecuteTool_AuthFailureRetry_PerUser_RetrySucceedsButReportsToolError
// pins that a retry which succeeds at the JSON-RPC level but reports
// isError:true (the upstream MCP server's own signal for a failed tool
// execution) still reaches the caller as a tool error, exactly like the
// non-retry success path at the bottom of ExecuteTool does via
// toolResponse.IsError. The retry branch calls createToolResponseMessage
// separately from that path, so it must thread the same IsError bit through
// rather than always reporting success once the retry call itself succeeds.
func TestExecuteTool_AuthFailureRetry_PerUser_RetrySucceedsButReportsToolError(t *testing.T) {
	state, toolName := newAuthRetryClientState("testclient", "dotool", nil, boolPtr(true))

	ft := &fakeCallToolTransport{
		callErrs:          []error{errors.New("tool call failed: 401 Unauthorized")},
		callResultIsError: []bool{false, true}, // retry (index 1) succeeds but is a tool error
	}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn}
	cs := &authRetryCredStore{requiresPerCall: true}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	resp, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err != nil {
		t.Fatalf("a successful (if tool-erroring) retry must not surface as a Go error, got: %v", err)
	}
	if resp == nil || resp.ChatMessage == nil || resp.ChatMessage.ChatToolMessage == nil {
		t.Fatalf("expected a populated tool-message response, got %+v", resp)
	}
	isErr := resp.ChatMessage.ChatToolMessage.IsError
	if isErr == nil || !*isErr {
		t.Errorf("expected the retried response's IsError to propagate as true, got %v", isErr)
	}
}

// TestExecuteTool_AuthFailureRetry_PerUser_RetryAlsoFails covers the
// give-up case: both the original call and the single retry fail, so the
// caller gets a normal ErrMCPToolCallFailed-wrapped error and no further
// (second) retry is attempted.
func TestExecuteTool_AuthFailureRetry_PerUser_RetryAlsoFails(t *testing.T) {
	// Explicit idempotent hint — see the sibling SucceedsOnSecondAttempt test.
	state, toolName := newAuthRetryClientState("testclient", "dotool", nil, boolPtr(true))

	ft := &fakeCallToolTransport{callErrs: []error{
		errors.New("tool call failed: 401 Unauthorized"),
		errors.New("tool call failed: 403 Forbidden"),
	}}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn}
	cs := &authRetryCredStore{requiresPerCall: true}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err == nil {
		t.Fatal("expected an error when the retry also fails")
	}
	if !errors.Is(err, ErrMCPToolCallFailed) {
		t.Errorf("expected error to wrap ErrMCPToolCallFailed, got: %v", err)
	}
	if got := ft.CallCount(); got != 2 {
		t.Errorf("expected exactly 2 CallTool invocations (original + 1 retry, no second retry), got %d", got)
	}
	if got := cs.forceRefreshCalls.Load(); got != 1 {
		t.Errorf("expected ForceRefresh to be called exactly once, got %d", got)
	}
	if got := cm.acquireCalls.Load(); got != 1 {
		t.Errorf("expected AcquireClientConn to be called exactly once, got %d", got)
	}
}

// TestExecuteTool_AuthFailureRetry_Shared_RetriesAfterReconnectCompletes
// covers the shared-connection happy path: the original call fails with a
// 401, a background goroutine forces a refresh and reconnects, and because
// the reconnect completes within the bounded wait budget the SAME call is
// retried once on the healed connection and succeeds.
func TestExecuteTool_AuthFailureRetry_Shared_RetriesAfterReconnectCompletes(t *testing.T) {
	// Explicit idempotent hint: retry-safety fails closed on missing
	// annotations (see TestExecuteTool_AuthFailureRetry_NoAnnotations_SkipsRetry),
	// so this reconnect-mechanics test needs an explicitly-safe tool to reach
	// the retry path at all.
	state, toolName := newAuthRetryClientState("sharedclient", "dotool", nil, boolPtr(true))

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn}
	cs := &authRetryCredStore{requiresPerCall: false}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	resp, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err != nil {
		t.Fatalf("expected the retry after a completed reconnect to succeed, got error: %v", err)
	}
	if resp == nil || resp.ChatMessage == nil {
		t.Fatalf("expected a populated chat message response, got %+v", resp)
	}
	if got := ft.CallCount(); got != 2 {
		t.Errorf("expected 2 CallTool invocations (original + exactly 1 retry), got %d", got)
	}
	if got := cm.reconnectCalls.Load(); got != 1 {
		t.Errorf("expected ReconnectClient to run exactly once, got %d", got)
	}
	if got := cs.forceRefreshCalls.Load(); got != 1 {
		t.Errorf("expected the background ForceRefresh to run exactly once, got %d", got)
	}
	if got := cm.acquireCalls.Load(); got != 1 {
		t.Errorf("expected AcquireClientConn to run exactly once for the retry, got %d", got)
	}
}

// TestExecuteTool_AuthFailureRetry_Shared_RoutingChangedDuringReconnect_RejectsRetry
// pins the gap CodeRabbit flagged: recoverSharedConnection re-resolved its
// retry target via GetClientForTool(toolName) — global tool-name routing
// across every client — instead of reacquiring the specific client that was
// just reconnected. If routing picks a different client for this tool name
// while the reconnect is in flight, the stale lookup would silently retry
// the ORIGINAL callRequest against an unintended upstream, breaking provider
// isolation. The retry must reacquire by the reconnected client's own name
// and refuse to proceed if the resolved state's ExecutionConfig.ID doesn't
// match, surfacing the original auth failure instead of retrying nowhere.
func TestExecuteTool_AuthFailureRetry_Shared_RoutingChangedDuringReconnect_RejectsRetry(t *testing.T) {
	state, toolName := newAuthRetryClientState("sharedclient", "dotool", nil, boolPtr(true))

	// What GetClientByName("sharedclient") resolves to by the time the
	// post-reconnect reacquire runs: same name, but a different
	// ExecutionConfig.ID — as if the client was deleted and recreated under
	// the same name while the reconnect was in flight. A stale caller
	// holding the original executionConfig must not treat this as the same
	// client it was retrying for.
	mismatched := &schemas.MCPClientState{
		Name:            "sharedclient",
		ExecutionConfig: &schemas.MCPClientConfig{ID: "sharedclient-id-v2", Name: "sharedclient"},
		ToolMap:         state.ToolMap,
		ToolNameMapping: map[string]string{},
	}

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn, byNameState: mismatched}
	cs := &authRetryCredStore{requiresPerCall: false}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err == nil {
		t.Fatalf("expected the original auth failure to surface when routing resolves to a different client, got no error")
	}
	if got := ft.CallCount(); got != 1 {
		t.Errorf("must not retry against the rerouted client: expected exactly 1 CallTool invocation (the original), got %d", got)
	}
	if got := cm.acquireCalls.Load(); got != 0 {
		t.Errorf("must not acquire a connection for the mismatched client, got %d AcquireClientConn calls", got)
	}
}

// TestExecuteTool_AuthFailureRetry_Shared_FallsBackWhenReconnectExceedsBudget
// covers the bounded-wait give-up: the reconnect is held open past the
// caller's remaining deadline (which caps the wait budget), so the original
// auth failure surfaces with no retry while the reconnect keeps running in
// the background.
func TestExecuteTool_AuthFailureRetry_Shared_FallsBackWhenReconnectExceedsBudget(t *testing.T) {
	// Explicit idempotent hint: retry-safety fails closed on missing
	// annotations (see TestExecuteTool_AuthFailureRetry_NoAnnotations_SkipsRetry),
	// so without it this test's "no retry" assertions would pass for the
	// wrong reason (annotation fail-closed) instead of proving the budget
	// give-up path this test is named for.
	state, toolName := newAuthRetryClientState("sharedclient", "dotool", nil, boolPtr(true))

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	reconnectGate := make(chan struct{})
	defer close(reconnectGate) // let the held reconnect goroutine finish after the test

	reconnectSignal := make(chan struct{}, 1)
	cm := &authRetryClientManager{state: state, acquireConn: conn, reconnectGate: reconnectGate, reconnectSignal: reconnectSignal}
	cs := &authRetryCredStore{requiresPerCall: false}
	tm := newAuthRetryToolsManager(cm, cs)

	// A short caller deadline caps the reconnect wait budget well below the
	// package const, keeping the test fast.
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(300*time.Millisecond))
	req := newAuthRetryToolCallRequest(toolName)

	start := time.Now()
	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the original error to surface when the reconnect exceeds the budget")
	}
	if !errors.Is(err, ErrMCPToolCallFailed) {
		t.Errorf("expected error to wrap ErrMCPToolCallFailed, got: %v", err)
	}
	if got := ft.CallCount(); got != 1 {
		t.Errorf("expected no retry when the reconnect exceeds the budget, got %d CallTool invocations", got)
	}
	if got := cm.acquireCalls.Load(); got != 0 {
		t.Errorf("AcquireClientConn must not run when the reconnect exceeds the budget, got %d calls", got)
	}
	if elapsed > 2*time.Second {
		t.Errorf("expected the bounded wait to give up quickly under a short caller deadline, took %v", elapsed)
	}

	// The reconnect itself must still have been triggered and left running.
	select {
	case <-reconnectSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the background goroutine to call ReconnectClient, but it never did")
	}
}

// TestExecuteTool_AuthFailureRetry_Shared_ReconnectFails_OriginalErrorSurfaces
// covers the reconnect-failed branch: the reconnect completes within budget
// but with an error, so no retry runs and the original failure surfaces.
func TestExecuteTool_AuthFailureRetry_Shared_ReconnectFails_OriginalErrorSurfaces(t *testing.T) {
	// Explicit idempotent hint: retry-safety fails closed on missing
	// annotations (see TestExecuteTool_AuthFailureRetry_NoAnnotations_SkipsRetry),
	// so this reconnect-mechanics test needs an explicitly-safe tool to reach
	// the retry path at all.
	state, toolName := newAuthRetryClientState("sharedclient", "dotool", nil, boolPtr(true))

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn, reconnectErr: errors.New("dial tcp: connection refused")}
	cs := &authRetryCredStore{requiresPerCall: false}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err == nil {
		t.Fatal("expected the original error to surface when the reconnect fails")
	}
	if !errors.Is(err, ErrMCPToolCallFailed) {
		t.Errorf("expected error to wrap ErrMCPToolCallFailed, got: %v", err)
	}
	if got := ft.CallCount(); got != 1 {
		t.Errorf("expected no retry when the reconnect fails, got %d CallTool invocations", got)
	}
	if got := cm.reconnectCalls.Load(); got != 1 {
		t.Errorf("expected ReconnectClient to run exactly once, got %d", got)
	}
	if got := cm.acquireCalls.Load(); got != 0 {
		t.Errorf("AcquireClientConn must not run when the reconnect fails, got %d calls", got)
	}
}

// TestExecuteTool_AuthFailureRetry_Shared_DestructiveNonIdempotent_ReconnectsWithoutRetry
// pins the opt-out ordering on the shared path: a destructive, non-idempotent
// tool must never be auto-retried, but the connection healing (background
// force-refresh + reconnect) must still run so subsequent calls succeed.
func TestExecuteTool_AuthFailureRetry_Shared_DestructiveNonIdempotent_ReconnectsWithoutRetry(t *testing.T) {
	state, toolName := newAuthRetryClientState("sharedclient", "dotool", boolPtr(true), nil)

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	reconnectSignal := make(chan struct{}, 1)
	forceRefreshSignal := make(chan struct{}, 1)

	cm := &authRetryClientManager{state: state, acquireConn: conn, reconnectSignal: reconnectSignal}
	cs := &authRetryCredStore{requiresPerCall: false, forceRefreshSignal: forceRefreshSignal}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err == nil {
		t.Fatal("expected the original error to surface for a destructive, non-idempotent tool")
	}
	if !errors.Is(err, ErrMCPToolCallFailed) {
		t.Errorf("expected error to wrap ErrMCPToolCallFailed, got: %v", err)
	}
	if got := ft.CallCount(); got != 1 {
		t.Errorf("destructive, non-idempotent tool must not be retried, got %d CallTool invocations", got)
	}
	if got := cm.acquireCalls.Load(); got != 0 {
		t.Errorf("AcquireClientConn must not run when the retry is opted out, got %d calls", got)
	}

	// The connection healing must still run despite the retry opt-out.
	select {
	case <-forceRefreshSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the background goroutine to call ForceRefresh despite the retry opt-out, but it never did")
	}
	select {
	case <-reconnectSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the background goroutine to call ReconnectClient despite the retry opt-out, but it never did")
	}
	if got := cm.reconnectCalls.Load(); got != 1 {
		t.Errorf("expected ReconnectClient to be triggered exactly once, got %d", got)
	}
}

// TestExecuteTool_AuthFailureRetry_Shared_Concurrent401sJoinOneReconnect
// covers the dedup contract: two concurrent 401s on the same shared client
// trigger only one actual reconnect; the loser of the race joins the
// winner's in-flight attempt and both calls retry successfully once it
// completes.
func TestExecuteTool_AuthFailureRetry_Shared_Concurrent401sJoinOneReconnect(t *testing.T) {
	// Explicit idempotent hint: retry-safety fails closed on missing
	// annotations (see TestExecuteTool_AuthFailureRetry_NoAnnotations_SkipsRetry),
	// so this reconnect-mechanics test needs an explicitly-safe tool to reach
	// the retry path at all.
	state, toolName := newAuthRetryClientState("sharedclient", "dotool", nil, boolPtr(true))

	ft := &fakeCallToolTransport{callErrs: []error{
		errors.New("tool call failed: 401 Unauthorized"),
		errors.New("tool call failed: 401 Unauthorized"),
	}}
	conn := client.NewClient(ft, client.WithSession())

	reconnectGate := make(chan struct{})
	reconnectSignal := make(chan struct{}, 1)
	cm := &authRetryClientManager{state: state, acquireConn: conn, reconnectGate: reconnectGate, reconnectSignal: reconnectSignal}
	cs := &authRetryCredStore{requiresPerCall: false}
	tm := newAuthRetryToolsManager(cm, cs)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			req := newAuthRetryToolCallRequest(toolName)
			_, errs[idx] = tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
		}(i)
	}

	// Hold the winner's reconnect open until the loser has demonstrably lost
	// the race (got the "already in progress" rejection), then release it.
	select {
	case <-reconnectSignal:
	case <-time.After(5 * time.Second):
		t.Fatal("expected a reconnect to start, but none did")
	}
	deadline := time.Now().Add(5 * time.Second)
	for cm.reconnectRejected.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("expected the second 401 to lose the reconnect race, but no rejection was observed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(reconnectGate)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("expected concurrent call %d to succeed after joining the shared reconnect, got: %v", i, err)
		}
	}
	if got := cm.reconnectCalls.Load(); got != 1 {
		t.Errorf("expected exactly one actual reconnect for concurrent 401s, got %d", got)
	}
	if got := cm.reconnectRejected.Load(); got != 1 {
		t.Errorf("expected exactly one caller to lose the reconnect race, got %d", got)
	}
	if got := ft.CallCount(); got != 4 {
		t.Errorf("expected 4 CallTool invocations (2 originals + 2 retries), got %d", got)
	}
	if got := cm.acquireCalls.Load(); got != 2 {
		t.Errorf("expected each concurrent caller to acquire the healed connection once, got %d", got)
	}
}

// TestExecuteTool_AuthFailureRetry_DestructiveNonIdempotent_SkipsRetry covers
// the safety opt-out: a destructive, non-idempotent tool must not be
// auto-retried even on a clean auth-shaped failure — the original error
// surfaces unchanged and none of the recovery machinery runs.
func TestExecuteTool_AuthFailureRetry_DestructiveNonIdempotent_SkipsRetry(t *testing.T) {
	state, toolName := newAuthRetryClientState("testclient", "dotool", boolPtr(true), nil)

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn}
	cs := &authRetryCredStore{requiresPerCall: true}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err == nil {
		t.Fatal("expected the original error to surface")
	}
	if !errors.Is(err, ErrMCPToolCallFailed) {
		t.Errorf("expected error to wrap ErrMCPToolCallFailed, got: %v", err)
	}
	if got := ft.CallCount(); got != 1 {
		t.Errorf("destructive, non-idempotent tool must not be retried, got %d CallTool invocations", got)
	}
	if got := cs.forceRefreshCalls.Load(); got != 0 {
		t.Errorf("ForceRefresh must not run when the retry is skipped, got %d calls", got)
	}
	if got := cm.acquireCalls.Load(); got != 0 {
		t.Errorf("AcquireClientConn must not run when the retry is skipped, got %d calls", got)
	}
}

// TestExecuteTool_AuthFailureRetry_DestructiveButIdempotent_StillRetries
// pins the exact opt-out condition (destructive AND NOT idempotent): a tool
// that is BOTH destructive and idempotent is safe to retry (repeated calls
// have no additional effect), so it must not be skipped.
func TestExecuteTool_AuthFailureRetry_DestructiveButIdempotent_StillRetries(t *testing.T) {
	state, toolName := newAuthRetryClientState("testclient", "dotool", boolPtr(true), boolPtr(true))

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn}
	cs := &authRetryCredStore{requiresPerCall: true}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err != nil {
		t.Fatalf("expected the retry to run and succeed for a destructive-but-idempotent tool, got error: %v", err)
	}
	if got := ft.CallCount(); got != 2 {
		t.Errorf("expected the retry to run (original + 1 retry), got %d CallTool invocations", got)
	}
	if got := cs.forceRefreshCalls.Load(); got != 1 {
		t.Errorf("expected ForceRefresh to run once, got %d calls", got)
	}
}

// TestExecuteTool_AuthFailureRetry_NoAnnotations_SkipsRetry pins the
// fail-closed default: a tool with no Annotations at all (both hints
// optional per the MCP spec, so this is common) must be treated the same as
// an explicitly destructive, non-idempotent one and skip the retry, rather
// than defaulting to "safe" because neither hint was set.
func TestExecuteTool_AuthFailureRetry_NoAnnotations_SkipsRetry(t *testing.T) {
	state, toolName := newAuthRetryClientState("testclient", "dotool", nil, nil)

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn}
	cs := &authRetryCredStore{requiresPerCall: true}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err == nil {
		t.Fatal("expected the original error to surface")
	}
	if !errors.Is(err, ErrMCPToolCallFailed) {
		t.Errorf("expected error to wrap ErrMCPToolCallFailed, got: %v", err)
	}
	if got := ft.CallCount(); got != 1 {
		t.Errorf("an unannotated tool must not be retried, got %d CallTool invocations", got)
	}
	if got := cs.forceRefreshCalls.Load(); got != 0 {
		t.Errorf("ForceRefresh must not run when the retry is skipped, got %d calls", got)
	}
	if got := cm.acquireCalls.Load(); got != 0 {
		t.Errorf("AcquireClientConn must not run when the retry is skipped, got %d calls", got)
	}
}

// TestExecuteTool_AuthFailureRetry_ReadOnly_StillRetries covers the other
// spec-defined safe case: readOnlyHint=true is safe to retry regardless of
// destructiveHint/idempotentHint (both are only meaningful when
// readOnlyHint is false per the MCP spec).
func TestExecuteTool_AuthFailureRetry_ReadOnly_StillRetries(t *testing.T) {
	state, toolName := newAuthRetryClientState("testclient", "dotool", nil, nil)
	tool := state.ToolMap[toolName]
	tool.Annotations = &schemas.MCPToolAnnotations{ReadOnlyHint: boolPtr(true)}
	state.ToolMap[toolName] = tool

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool call failed: 401 Unauthorized")}}
	conn := client.NewClient(ft, client.WithSession())

	cm := &authRetryClientManager{state: state, acquireConn: conn}
	cs := &authRetryCredStore{requiresPerCall: true}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err != nil {
		t.Fatalf("expected the retry to run and succeed for a read-only tool, got error: %v", err)
	}
	if got := ft.CallCount(); got != 2 {
		t.Errorf("expected the retry to run (original + 1 retry), got %d CallTool invocations", got)
	}
}

// TestExecuteTool_NonAuthFailure_NeverTriggersRetryLogic is the control
// case: a generic tool-call failure with no auth-shaped text must go
// through the normal error path untouched — no forced refresh, no
// reconnect attempt, no retry.
func TestExecuteTool_NonAuthFailure_NeverTriggersRetryLogic(t *testing.T) {
	state, toolName := newAuthRetryClientState("testclient", "dotool", nil, nil)

	ft := &fakeCallToolTransport{callErrs: []error{errors.New("tool execution panicked: division by zero")}}
	conn := client.NewClient(ft, client.WithSession())

	reconnectSignal := make(chan struct{}, 1)
	cm := &authRetryClientManager{state: state, acquireConn: conn, reconnectSignal: reconnectSignal}
	cs := &authRetryCredStore{requiresPerCall: true}
	tm := newAuthRetryToolsManager(cm, cs)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newAuthRetryToolCallRequest(toolName)

	_, err := tm.ExecuteTool(ctx, req, conn, state.ExecutionConfig, state.ToolNameMapping)
	if err == nil {
		t.Fatal("expected the call to fail")
	}
	if !errors.Is(err, ErrMCPToolCallFailed) {
		t.Errorf("expected error to wrap ErrMCPToolCallFailed, got: %v", err)
	}
	if got := ft.CallCount(); got != 1 {
		t.Errorf("a non-auth failure must not trigger a retry, got %d CallTool invocations", got)
	}
	if got := cs.forceRefreshCalls.Load(); got != 0 {
		t.Errorf("ForceRefresh must not run for a non-auth failure, got %d calls", got)
	}
	if got := cm.acquireCalls.Load(); got != 0 {
		t.Errorf("AcquireClientConn must not run (beyond the caller's own initial acquire) for a non-auth failure, got %d calls", got)
	}

	// Give a would-be background goroutine a moment to prove it does NOT run.
	select {
	case <-reconnectSignal:
		t.Fatal("ReconnectClient must not be triggered for a non-auth failure")
	case <-time.After(200 * time.Millisecond):
	}
	if got := cm.reconnectCalls.Load(); got != 0 {
		t.Errorf("ReconnectClient must not run for a non-auth failure, got %d calls", got)
	}
}
