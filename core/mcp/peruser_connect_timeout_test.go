package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// mockPerUserCredStore forces the per-call ephemeral-connect path:
// RequiresPerCallConnection=true and ConnectionHeaders resolves cleanly (so we
// do NOT short-circuit on MCPAuthRequiredError — we want to exercise the actual
// upstream connect, which is where the dead-endpoint hang lives).
type mockPerUserCredStore struct{}

func (m *mockPerUserCredStore) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{"X-Key": []string{"test"}}, nil
}
func (m *mockPerUserCredStore) RequestHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}
func (m *mockPerUserCredStore) RequiresPerCallConnection(config *schemas.MCPClientConfig) bool {
	return true
}

// TestPerUserConnectTimeoutOnDeadEndpoint is the regression guard for the OWUI
// per-turn stall: a per-user-auth MCP whose UPSTREAM is unreachable/hangs must
// fail FAST on connection-acquisition, not hang on the 30s
// MCPClientConnectionEstablishTimeout. Several such per turn = the 90-120s stall.
//
// FAIL-FIRST: on current code this takes ~30s (the establish timeout) -> fails the
// <5s assert. PASS-AFTER: with the bounded per-call connect + circuit-breaker it
// returns in <5s.
func TestPerUserConnectTimeoutOnDeadEndpoint(t *testing.T) {
	// Blackhole listener: accept TCP, never send an HTTP/MCP response, so the
	// client's Initialize hangs until the establish-timeout context fires.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	var held []net.Conn
	var heldMu sync.Mutex
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			heldMu.Lock()
			held = append(held, c) // keep open, never respond
			heldMu.Unlock()
		}
	}()
	// Close every accepted server-side conn at teardown — closing only the
	// listener leaves the accepted net.Conns (and their FDs) open, which leaks
	// descriptors across repeated package/race-test runs.
	defer func() {
		heldMu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		heldMu.Unlock()
	}()
	url := "http://" + ln.Addr().String() + "/mcp"

	mgr := NewMCPManager(context.Background(), schemas.MCPConfig{}, &mockPerUserCredStore{}, nil, nil)

	state := &schemas.MCPClientState{
		Name: "deadendpoint",
		ExecutionConfig: &schemas.MCPClientConfig{
			ID:                "deadendpoint-id",
			Name:              "deadendpoint",
			ConnectionType:    schemas.MCPConnectionTypeHTTP,
			ConnectionString:  schemas.NewSecretVar(url),
			AuthType:          schemas.MCPAuthTypePerUserHeaders,
			PerUserHeaderKeys: []string{"x-key"},
			ToolsToExecute:    []string{"*"},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	start := time.Now()
	_, release, acqErr := mgr.AcquireClientConn(ctx, state)
	elapsed := time.Since(start)
	if release != nil {
		release()
	}

	t.Logf("AcquireClientConn against dead/blackhole endpoint: elapsed=%v err=%v", elapsed, acqErr)
	require.Error(t, acqErr, "connect to a dead endpoint must fail")
	// Bound with margin from the 5s per-call timeout (breathing room, not razor-thin):
	// fail-first on current code = ~30s (the establish timeout) which is >> 10s;
	// pass-after = ~5s (MCPPerCallConnectTimeout) which is < 10s.
	require.Less(t, elapsed, 10*time.Second,
		"per-user connect to an unreachable MCP must fail FAST (bounded ~5s), not hang on the 30s establish timeout (this is the OWUI per-turn stall)")

	// Circuit-breaker: a SECOND call within the cooldown must fail INSTANTLY
	// (skip the connect entirely) — this is what bounds the per-turn stall to
	// ~one timeout regardless of how many dead-tool calls the agent makes.
	start2 := time.Now()
	_, release2, acqErr2 := mgr.AcquireClientConn(ctx, state)
	elapsed2 := time.Since(start2)
	if release2 != nil {
		release2()
	}
	t.Logf("2nd AcquireClientConn (circuit-breaker): elapsed=%v err=%v", elapsed2, acqErr2)
	require.Error(t, acqErr2, "breaker call still returns an error")
	require.Less(t, elapsed2, 1*time.Second,
		"circuit-breaker: a recently-failed per-user client must fail INSTANTLY (<1s), not re-attempt the ~5s connect")
}

// failingCredStore forces the per-call path but ALWAYS fails credential
// resolution with a plain (non-auth-required) error — i.e. a pre-connect
// failure that never contacts the upstream (stands in for a PreConnectionHook
// policy/plugin denial or a credstore error).
type failingCredStore struct{}

func (f *failingCredStore) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	return nil, fmt.Errorf("simulated credential/policy denial")
}
func (f *failingCredStore) RequestHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}
func (f *failingCredStore) RequiresPerCallConnection(config *schemas.MCPClientConfig) bool {
	return true
}

// TestPerUserConnectBreakerNotTrippedByPreConnectFailure guards the fix for the
// "plugin/policy denial trips the breaker" bug: a failure that happens BEFORE we
// contact the upstream (here a credential/policy denial) must NOT arm the
// circuit-breaker. Otherwise a single denial fails-fast every later valid
// per-user call for the whole cooldown against a perfectly healthy MCP server.
//
// FAIL-FIRST (pre-fix): the first denial stores the breaker, so the 2nd call
// returns the "recently unreachable; failing fast (circuit-breaker…)" message.
// PASS-AFTER: connectAttempted stays false on a pre-connect failure, so the
// breaker is never armed and the 2nd call still surfaces the denial.
func TestPerUserConnectBreakerNotTrippedByPreConnectFailure(t *testing.T) {
	mgr := NewMCPManager(context.Background(), schemas.MCPConfig{}, &failingCredStore{}, nil, nil)

	state := &schemas.MCPClientState{
		Name: "denied",
		ExecutionConfig: &schemas.MCPClientConfig{
			ID:                "denied-id",
			Name:              "denied",
			ConnectionType:    schemas.MCPConnectionTypeHTTP,
			ConnectionString:  schemas.NewSecretVar("http://127.0.0.1:0/mcp"),
			AuthType:          schemas.MCPAuthTypePerUserHeaders,
			PerUserHeaderKeys: []string{"x-key"},
			ToolsToExecute:    []string{"*"},
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, release1, err1 := mgr.AcquireClientConn(ctx, state)
	if release1 != nil {
		release1()
	}
	require.Error(t, err1, "credential/policy denial must surface an error")
	require.NotContains(t, err1.Error(), "circuit-breaker")

	// Second call must ALSO surface the denial — NOT a breaker fast-fail.
	_, release2, err2 := mgr.AcquireClientConn(ctx, state)
	if release2 != nil {
		release2()
	}
	require.Error(t, err2)
	require.NotContains(t, err2.Error(), "circuit-breaker",
		"a pre-connect failure (policy/credential denial) must not arm the breaker")
}
