package chaosmock_test

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// These tests point a real mcp-go client, the same library and version the
// system under test uses, at the hand-rolled mock. The mock deliberately
// implements the protocol itself so it can violate it on demand, and that
// freedom is only safe if a genuine client accepts it when it is behaving.
// Everything else in the harness rests on that, so it is checked first.

func newFleetWithTools(t *testing.T, tools ...chaosmock.Tool) (*chaosmock.Fleet, *chaosmock.Server) {
	t.Helper()
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set(tools)
	fleet.Start()
	t.Cleanup(fleet.Stop)
	return fleet, srv
}

func connect(t *testing.T, url string) (*client.Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	c, err := client.NewStreamableHttpClient(url)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-compat", Version: "1.0.0"}
	if _, err := c.Initialize(ctx, req); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	return c, ctx
}

func TestRealClientCompletesHandshakeAndListsTools(t *testing.T) {
	fleet, srv := newFleetWithTools(t,
		chaosmock.Tool{Name: "alpha_one"},
		chaosmock.Tool{Name: "alpha_two"},
	)

	c, ctx := connect(t, fleet.ServerURL("alpha"))

	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(res.Tools))
	}
	// Sorted by name, so the order is part of the contract rather than an
	// accident of map iteration.
	if res.Tools[0].Name != "alpha_one" || res.Tools[1].Name != "alpha_two" {
		t.Fatalf("unexpected tool names: %q, %q", res.Tools[0].Name, res.Tools[1].Name)
	}

	counters := srv.Counters().Snapshot()
	if counters.Initializes != 1 {
		t.Errorf("expected 1 initialize, got %d", counters.Initializes)
	}
	if counters.ToolsList != 1 {
		t.Errorf("expected 1 tools/list, got %d", counters.ToolsList)
	}
	if counters.Sessions != 1 {
		t.Errorf("expected 1 session, got %d", counters.Sessions)
	}
	// The handshake's own notification has to have landed, otherwise the
	// client is not as far along as it appears.
	if counters.Methods["notifications/initialized"] != 1 {
		t.Errorf("expected the initialized notification, got methods %v", counters.Methods)
	}
}

func TestRealClientCallsToolAndCounterRecordsDispatch(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo_tool"})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	var req mcp.CallToolRequest
	req.Params.Name = "echo_tool"
	req.Params.Arguments = map[string]any{"value": "hello"}

	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("tools/call failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected a successful call, got an error result")
	}
	if len(res.Content) == 0 {
		t.Fatalf("expected content in the result")
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	// The default reply names the tool that ran, so a misdispatch is visible
	// in the response body before the counters are even consulted.
	if want := "echo_tool:"; len(text.Text) < len(want) || text.Text[:len(want)] != want {
		t.Errorf("expected the reply to name the tool that ran, got %q", text.Text)
	}

	counters := srv.Counters().Snapshot()
	if counters.Calls["echo_tool"] != 1 {
		t.Errorf("expected 1 call to echo_tool, got %v", counters.Calls)
	}
}

func TestRealClientPings(t *testing.T) {
	fleet, srv := newFleetWithTools(t)
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if got := srv.Counters().Snapshot().Pings; got != 1 {
		t.Errorf("expected 1 ping, got %d", got)
	}
}

func TestToolMutationIsVisibleToTheNextList(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "before_tool"})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	first, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("first tools/list failed: %v", err)
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "before_tool" {
		t.Fatalf("unexpected initial tools: %+v", first.Tools)
	}

	// Mutate underneath the live session. The connection is not disturbed, so
	// this isolates "did the tool set change" from "did the client reconnect".
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
		t.Errorf("mutating tools must not force a new session, got %d sessions", got)
	}
}

func TestKilledSessionForcesReinitialization(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "some_tool"})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list before session kill failed: %v", err)
	}

	// The transport stays up; only the session goes away. A client that only
	// watches for transport errors should not notice until its next request.
	srv.KillSessions()

	_, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err == nil {
		t.Fatalf("expected tools/list to fail after the session was killed")
	}
	if srv.SessionCount() != 0 {
		t.Errorf("expected no live sessions, got %d", srv.SessionCount())
	}
}
