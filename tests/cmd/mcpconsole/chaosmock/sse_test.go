package chaosmock_test

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// The legacy transport is not streamable HTTP with different framing. A
// request's response arrives out-of-band on a stream the client opened
// separately, which changes what a dropped connection costs, so it gets its
// own compatibility pass against the real client rather than an assumption
// that the protocol layer behaving means the transport does.

func sseFleet(t *testing.T, tools ...chaosmock.Tool) (*chaosmock.Fleet, *chaosmock.Server) {
	t.Helper()
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set(tools)
	fleet.Start()
	t.Cleanup(fleet.Stop)
	return fleet, srv
}

func connectSSE(t *testing.T, url string) (*client.Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	c, err := client.NewSSEMCPClient(url)
	if err != nil {
		t.Fatalf("failed to create SSE client: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start SSE client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-sse", Version: "1.0.0"}
	if _, err := c.Initialize(ctx, req); err != nil {
		t.Fatalf("initialize over SSE failed: %v", err)
	}
	return c, ctx
}

func TestSSEClientCompletesHandshakeAndListsTools(t *testing.T) {
	fleet, srv := sseFleet(t,
		chaosmock.Tool{Name: "alpha_one"},
		chaosmock.Tool{Name: "alpha_two"},
	)

	c, ctx := connectSSE(t, fleet.SSEURL("alpha"))

	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list over SSE failed: %v", err)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(res.Tools))
	}

	counters := srv.Counters().Snapshot()
	if counters.Initializes != 1 {
		t.Errorf("expected 1 initialize, got %d", counters.Initializes)
	}
	// Under this transport the stream is the session, so it exists from the
	// moment the client connects rather than from initialize.
	if counters.Sessions != 1 {
		t.Errorf("expected 1 session, got %d", counters.Sessions)
	}
	if srv.OpenStreams() != 1 {
		t.Errorf("expected the stream to still be open, got %d", srv.OpenStreams())
	}
}

func TestSSEClientCallsToolAndCounterRecordsDispatch(t *testing.T) {
	fleet, srv := sseFleet(t, chaosmock.Tool{Name: "echo_tool"})
	c, ctx := connectSSE(t, fleet.SSEURL("alpha"))

	var req mcp.CallToolRequest
	req.Params.Name = "echo_tool"
	req.Params.Arguments = map[string]any{"value": "hello"}

	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("tools/call over SSE failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected a successful call")
	}
	if got := srv.Counters().Snapshot().Calls["echo_tool"]; got != 1 {
		t.Errorf("expected 1 call to echo_tool, got %d", got)
	}
}

func TestSSEToolMutationIsVisibleToTheNextList(t *testing.T) {
	fleet, srv := sseFleet(t, chaosmock.Tool{Name: "before_tool"})
	c, ctx := connectSSE(t, fleet.SSEURL("alpha"))

	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("first tools/list failed: %v", err)
	}

	srv.Tools().Add(chaosmock.Tool{Name: "after_tool"})
	srv.Tools().Remove("before_tool")

	second, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("second tools/list failed: %v", err)
	}
	if len(second.Tools) != 1 || second.Tools[0].Name != "after_tool" {
		t.Fatalf("expected only after_tool, got %+v", second.Tools)
	}
	if got := srv.Counters().Snapshot().Sessions; got != 1 {
		t.Errorf("mutating tools must not force a new session, got %d", got)
	}
}

// TestSSEDroppedStreamStrandsTheRequest is the behaviour this transport has
// and streamable HTTP does not. The POST is acknowledged before the response
// exists, so dropping the stream leaves the caller waiting on an answer that
// has nowhere to land. A client is expected to give up on its own deadline
// rather than be told.
func TestSSEDroppedStreamStrandsTheRequest(t *testing.T) {
	fleet, srv := sseFleet(t, chaosmock.Tool{Name: "some_tool"})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := client.NewSSEMCPClient(fleet.SSEURL("alpha"))
	if err != nil {
		t.Fatalf("failed to create SSE client: %v", err)
	}
	defer c.Close()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-sse", Version: "1.0.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}

	srv.DropStreams()
	if !waitFor(t, 3*time.Second, func() bool { return srv.OpenStreams() == 0 }) {
		t.Fatalf("expected the stream to be gone, got %d", srv.OpenStreams())
	}

	// A short deadline stands in for the caller's own timeout. The request
	// must fail rather than succeed, and must not hang past it.
	callCtx, callCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer callCancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.ListTools(callCtx, mcp.ListToolsRequest{})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Errorf("expected the stranded request to fail once the stream was gone")
		}
	case <-time.After(5 * time.Second):
		t.Errorf("the stranded request hung well past its own deadline")
	}
}

func TestSSEInjectedStatusReachesTheClient(t *testing.T) {
	fleet, srv := sseFleet(t, chaosmock.Tool{Name: "some_tool"})
	srv.SetFaults(chaosmock.Faults{Status: 503, RetryAfter: "5"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := client.NewSSEMCPClient(fleet.SSEURL("alpha"))
	if err != nil {
		t.Fatalf("failed to create SSE client: %v", err)
	}
	defer c.Close()

	// The status lands on the stream open itself, so the client never gets as
	// far as a session.
	if err := c.Start(ctx); err == nil {
		t.Fatalf("expected opening the stream to fail while a 503 is injected")
	}
	if got := srv.SessionCount(); got != 0 {
		t.Errorf("a refused stream must not create a session, got %d", got)
	}
}
